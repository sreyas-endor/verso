package room

// Regression tests for three end-condition and roster bugs found while
// reconciling the room, transport and registry seams. Each one is a state the
// phase machine could reach on its own; none needed a malicious client.

import (
	"context"
	mrand "math/rand/v2"
	"testing"
	"testing/synctest"
	"time"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// regRoom builds an n-player room, starts the match, and returns it parked at
// the first drawing turn of round 1.
func regRoom(t *testing.T, n int, s *genpb.MatchSettings) (*Room, []string, []*smokeSock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	r := New("REGR", "host", genpb.Avatar_AVATAR_BEETLE, Options{
		Deck: smokeDeck{}, Rand: mrand.New(mrand.NewPCG(7, 9)), Settings: s,
	})
	hostID, hostTok := r.HostSeat()
	go r.run(ctx)

	s0 := newSmokeSock()
	if _, err := r.attach(hostTok, s0); err != nil {
		t.Fatal(err)
	}
	ids, socks := []string{hostID}, []*smokeSock{s0}
	for i := 1; i < n; i++ {
		sk := newSmokeSock()
		id, _, err := r.seat("p", genpb.Avatar_AVATAR_COURIER, sk)
		if err != nil {
			t.Fatal(err)
		}
		ids, socks = append(ids, id), append(socks, sk)
	}
	for i, id := range ids {
		r.Submit(Command{PlayerID: id, Out: socks[i], Cmd: &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}}})
	}
	synctest.Wait()
	r.Submit(Command{PlayerID: hostID, Out: s0, Cmd: &genpb.ClientCommand{
		Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}}}})
	synctest.Wait()
	time.Sleep(AssignDuration + time.Millisecond)
	synctest.Wait()
	// Assignment hands off through a PHASE_INTERMISSION before the first turn
	// goes live (phase.go, beginIntermission).
	time.Sleep(smokeIntermission(r))
	synctest.Wait()
	return r, ids, socks
}

// regToDiscussion walks the clock to the voting window, spending whatever the
// live phase is owed rather than assuming which phase follows which.
func regToDiscussion(t *testing.T, r *Room) {
	t.Helper()
	for range 6 * MaxPlayers {
		switch ph := smokePhase(r); ph {
		case genpb.Phase_PHASE_DISCUSSION:
			return
		case genpb.Phase_PHASE_ASSIGNING, genpb.Phase_PHASE_INTERMISSION, genpb.Phase_PHASE_DRAWING:
			remain := smokeGet(r, func(r *Room) int32 { return r.RemainingMS() })
			if remain == 0 {
				// A drawing turn that expired mid-stroke holds on for TurnGrace
				// while the tail of that stroke arrives (phase.go,
				// beginTurnGrace). Its clock is genuinely at zero, so sleeping
				// the remainder would not advance anything.
				time.Sleep(TurnGrace + time.Millisecond)
				break
			}
			time.Sleep(time.Duration(remain)*time.Millisecond + time.Millisecond)
		default:
			t.Fatalf("regToDiscussion: stuck in %v", ph)
		}
		synctest.Wait()
	}
	t.Fatalf("regToDiscussion: never opened, phase = %v", smokePhase(r))
}

// DESIGN.md:71 gives the imposter the win for remaining active AFTER the final
// round. A seat expiring part-way through that round used to satisfy
// evaluateEnd's round check from reevaluateEnd and end the match on the spot,
// mid-drawing, with FINAL_ROUND_SURVIVED.
func TestSeatExpiryDoesNotEndTheFinalRoundEarly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, ids, socks := regRoom(t, 4, &genpb.MatchSettings{
			MaxRounds: 1, DrawSeconds: 60, DiscussSeconds: 60})
		if got := smokePhase(r); got != genpb.Phase_PHASE_DRAWING {
			t.Fatalf("phase = %v", got)
		}

		imposter, artist := smokeImposter(r), smokeArtist(r)
		victim := -1
		for i, id := range ids {
			if id != imposter && id != artist {
				victim = i
				break
			}
		}
		r.detach(ids[victim], socks[victim])
		synctest.Wait()
		time.Sleep(GraceWindow + 2*SweepInterval)
		synctest.Wait()

		if !smokeEliminated(r, ids[victim]) {
			t.Fatal("expired seat should have left the denominator")
		}
		if got := smokePhase(r); got == genpb.Phase_PHASE_ENDED {
			w, reason := smokeOutcome(r)
			t.Fatalf("match ended mid-drawing: %v / %v", w, reason)
		}
	})
}

// DESIGN.md:52 ends the window as soon as every active player has voted, and a
// disconnected seat is not active. So a socket dropping shrinks the denominator
// and can complete the tally exactly as a final vote does — the early-resolve
// check has to run on both events, or the room sits on a satisfied tally until
// the combined timer runs out.
func TestLosingASeatResolvesASatisfiedVote(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, ids, socks := regRoom(t, 4, &genpb.MatchSettings{
			MaxRounds: 4, DrawSeconds: 5, DiscussSeconds: 120})
		regToDiscussion(t, r)

		// Three of the four vote. The fourth is still connected, so it is still
		// in the denominator and the window must stay open for it.
		for i := range 3 {
			r.Submit(Command{PlayerID: ids[i], Out: socks[i], Cmd: &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{
					Choice: &genpb.CastVote_Skip{Skip: true}}}}})
		}
		synctest.Wait()
		if got := smokePhase(r); got != genpb.Phase_PHASE_DISCUSSION {
			t.Fatalf("must still wait on the fourth active player, got %v", got)
		}

		// Its socket drops: three of three active players have now voted, so the
		// window closes at once rather than burning the remaining ~two minutes.
		r.detach(ids[3], socks[3])
		synctest.Wait()
		if got := smokePhase(r); got == genpb.Phase_PHASE_DISCUSSION {
			t.Fatal("every remaining active player had voted but the window did not close")
		}
	})
}

// expireSeat clears DisconnectedAt so the liveness sweep stops re-expiring an
// eliminated seat. Nothing else collected it, so after a rematch it sat in the
// roster unable to ever be Ready, and canStart() was false for the life of the
// room.
func TestRematchDropsSeatsWithNoSocket(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, ids, socks := regRoom(t, 4, &genpb.MatchSettings{
			MaxRounds: 4, DrawSeconds: 60, DiscussSeconds: 180})

		hostID := ids[0]
		imposter := smokeImposter(r)
		victim := -1
		for i, id := range ids {
			if id != imposter && id != hostID {
				victim = i
				break
			}
		}
		r.detach(ids[victim], socks[victim])
		synctest.Wait()
		time.Sleep(GraceWindow + 2*SweepInterval)
		synctest.Wait()
		if !smokeEliminated(r, ids[victim]) {
			t.Fatal("seat should have expired mid-match")
		}

		for range 400 {
			if smokePhase(r) == genpb.Phase_PHASE_ENDED {
				break
			}
			time.Sleep(10 * time.Second)
			synctest.Wait()
		}
		if got := smokePhase(r); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("match did not finish, phase = %v", got)
		}

		r.Submit(Command{PlayerID: hostID, Out: socks[0], Cmd: &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_Rematch{Rematch: &genpb.Rematch{}}}})
		synctest.Wait()
		for i, id := range ids {
			if i == victim {
				continue
			}
			r.Submit(Command{PlayerID: id, Out: socks[i], Cmd: &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}}})
		}
		synctest.Wait()

		if n := smokeGet(r, func(r *Room) int { return len(r.players) }); n != 3 {
			t.Errorf("rematch roster = %d seats, want 3", n)
		}
		if !smokeGet(r, func(r *Room) bool { return r.canStart() }) {
			t.Error("rematch can never start: a seat with no socket is still in the roster")
		}
	})
}
