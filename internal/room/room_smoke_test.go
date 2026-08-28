package room

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	mrand "math/rand/v2"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

type smokeDeck struct{}

func (smokeDeck) Pair(d genpb.Difficulty, rnd *mrand.Rand, _ []string) (string, string) {
	return "CAT", "DOG"
}

// smokeSock is one fake client socket: the room's Session contract over a
// buffered channel a test can drain, plus a record of whether the room asked
// for it to be closed.
type smokeSock struct {
	ch chan *genpb.ServerEvent
	// closed is atomic because the room goroutine sets it and the test
	// goroutine reads it. Every other field here is written once, before the
	// socket is handed to the room.
	closed atomic.Bool
}

func newSmokeSock() *smokeSock { return &smokeSock{ch: make(chan *genpb.ServerEvent, 8192)} }

// Send mirrors the real transport: non-blocking, drop on a full queue.
func (s *smokeSock) Send(ev *genpb.ServerEvent) {
	select {
	case s.ch <- ev:
	default:
	}
}

// Close records the request. It does NOT shut the channel: the room's contract
// is "write what is queued, then go", so anything already delivered must stay
// readable — a test asserting the terminal error arrived reads it after this.
func (s *smokeSock) Close() { s.closed.Store(true) }

// wasClosed reports whether the room asked this session to close.
func (s *smokeSock) wasClosed() bool { return s.closed.Load() }

func (s *smokeSock) drain() []*genpb.ServerEvent {
	var out []*genpb.ServerEvent
	for {
		select {
		case e := <-s.ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

// smokeIntermission is the room's own clamped handoff length. Every drawing
// turn and the voting window are preceded by a PHASE_INTERMISSION
// (phase.go, beginIntermission), so a test walking the clock has to spend it.
func smokeIntermission(r *Room) time.Duration {
	return smokeGet(r, func(r *Room) time.Duration {
		return time.Duration(r.settings.GetIntermissionSeconds()) * time.Second
	})
}

func smokeKinds(evs []*genpb.ServerEvent) []string {
	var out []string
	for _, e := range evs {
		switch e.GetEvt().(type) {
		case *genpb.ServerEvent_LobbyState:
			out = append(out, "lobby")
		case *genpb.ServerEvent_RoundStarted:
			out = append(out, "round")
		case *genpb.ServerEvent_TurnStarted:
			out = append(out, "turn")
		case *genpb.ServerEvent_PhaseChanged:
			out = append(out, "phase:"+e.GetPhaseChanged().GetPhase().String())
		case *genpb.ServerEvent_VoteTally:
			out = append(out, "tally")
		case *genpb.ServerEvent_PlayerEliminated:
			out = append(out, "elim")
		case *genpb.ServerEvent_MatchEnded:
			out = append(out, "ended")
		case *genpb.ServerEvent_YourWord:
			out = append(out, "word")
		case *genpb.ServerEvent_Snapshot:
			out = append(out, "snap")
		case *genpb.ServerEvent_Joined:
			out = append(out, "joined")
		case *genpb.ServerEvent_Error:
			out = append(out, "err:"+e.GetError().GetCode().String())
		}
	}
	return out
}

// Every field on Room is owned by the actor goroutine, so a test must not read
// one directly. smokeGet hops onto that goroutine the same way Seat and Detach
// do.
func smokeGet[T any](r *Room, fn func(*Room) T) T {
	var v T
	if err := r.do(func() { v = fn(r) }); err != nil {
		panic(err)
	}
	return v
}

func smokePhase(r *Room) genpb.Phase {
	return smokeGet(r, func(r *Room) genpb.Phase { return r.phase })
}

func smokeArtist(r *Room) string {
	return smokeGet(r, func(r *Room) string { return r.artistID })
}

// smokeImposter is the single imposter of a default-settings match, or "" when
// none has been dealt. It fails the test outright on a match dealt more than
// one, so a multi-imposter room can never be silently read as a
// single-imposter one by a caller that only ever wanted the base game.
func smokeImposter(r *Room) string {
	ids := smokeImposters(r)
	switch len(ids) {
	case 0:
		return ""
	case 1:
		return ids[0]
	default:
		panic("smokeImposter: match has more than one imposter; use smokeImposters")
	}
}

func smokeImposters(r *Room) []string {
	return smokeGet(r, func(r *Room) []string {
		return append([]string(nil), r.imposterIDs...)
	})
}

func smokeWordOf(r *Room, id string) string {
	return smokeGet(r, func(r *Room) string {
		if p := r.byID[id]; p != nil {
			return p.word
		}
		return ""
	})
}

func smokeEliminated(r *Room, id string) bool {
	return smokeGet(r, func(r *Room) bool {
		p := r.byID[id]
		return p != nil && p.Eliminated
	})
}

func smokeOpenWidth(r *Room) int32 {
	return smokeGet(r, func(r *Room) int32 {
		if r.open == nil {
			return -1
		}
		return r.open.width
	})
}

func smokeStrokeCount(r *Room) int {
	return smokeGet(r, func(r *Room) int { return len(r.strokes) })
}

func smokeOutcome(r *Room) (genpb.WinnerSide, genpb.MatchEndReason) {
	type outcome struct {
		w genpb.WinnerSide
		e genpb.MatchEndReason
	}
	o := smokeGet(r, func(r *Room) outcome { return outcome{r.winner, r.endReason} })
	return o.w, o.e
}

func smokeAnyAssignment(r *Room) bool {
	return smokeGet(r, func(r *Room) bool {
		for _, p := range r.players {
			if p.word != "" || p.Eliminated {
				return true
			}
		}
		return false
	})
}

func TestRoomFullMatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r := New("ABCD", "host", Options{
			Deck: smokeDeck{},
			Rand: mrand.New(mrand.NewPCG(1, 2)),
			Settings: &genpb.MatchSettings{
				MaxRounds: 2, DrawSeconds: 10, DiscussSeconds: 30,
			},
		})
		hostID, hostTok := r.HostSeat()
		go r.run(ctx)

		s0 := newSmokeSock()
		if _, err := r.attach(hostTok, s0); err != nil {
			t.Fatal(err)
		}
		s1, s2 := newSmokeSock(), newSmokeSock()
		p1, _, err := r.seat("bee", s1)
		if err != nil {
			t.Fatal(err)
		}
		p2, _, err := r.seat("cee", s2)
		if err != nil {
			t.Fatal(err)
		}

		ids := []string{hostID, p1, p2}
		socks := []*smokeSock{s0, s1, s2}
		for i, id := range ids {
			r.Submit(Command{PlayerID: id, Out: socks[i], Cmd: &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}},
			}})
		}
		synctest.Wait()

		// Non-host cannot start.
		r.Submit(Command{PlayerID: p1, Out: s1, Cmd: &genpb.ClientCommand{
			Cid: "x", Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}},
		}})
		synctest.Wait()
		if got := smokeKinds(s1.drain()); len(got) == 0 || got[len(got)-1] != "err:ERROR_CODE_NOT_HOST" {
			t.Fatalf("expected NOT_HOST, got %v", got)
		}
		s0.drain()
		s2.drain()

		r.Submit(Command{PlayerID: hostID, Out: s0, Cmd: &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}},
		}})
		synctest.Wait()

		ev0 := s0.drain()
		if k := smokeKinds(ev0); len(k) < 2 || k[0] != "phase:PHASE_ASSIGNING" || k[1] != "word" {
			t.Fatalf("assigning: %v", k)
		}
		// Exactly one player holds the odd word.
		odd := 0
		for _, s := range []*smokeSock{s1, s2} {
			for _, e := range s.drain() {
				if w := e.GetYourWord(); w != nil && w.GetWord() == "DOG" {
					odd++
				}
			}
		}
		for _, e := range ev0 {
			if w := e.GetYourWord(); w != nil && w.GetWord() == "DOG" {
				odd++
			}
		}
		if odd != 1 {
			t.Fatalf("expected exactly one imposter, got %d", odd)
		}

		// Assigning -> round 1, via the intermission handoff.
		time.Sleep(AssignDuration + time.Millisecond)
		synctest.Wait()
		time.Sleep(smokeIntermission(r))
		synctest.Wait()
		k := smokeKinds(s0.drain())
		if len(k) < 4 || k[0] != "round" || k[1] != "phase:PHASE_INTERMISSION" ||
			k[2] != "phase:PHASE_DRAWING" || k[3] != "turn" {
			t.Fatalf("round start: %v", k)
		}

		// Only the artist may draw.
		artist := smokeArtist(r)
		var other string
		for _, id := range ids {
			if id != artist {
				other = id
			}
		}
		oi := 0
		for i, id := range ids {
			if id == other {
				oi = i
			}
		}
		r.Submit(Command{PlayerID: other, Out: socks[oi], Cmd: &genpb.ClientCommand{
			Cid: "s", Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 1, Width: 4, Points: []int32{10, 10},
			}},
		}})
		synctest.Wait()
		found := false
		for _, kk := range smokeKinds(socks[oi].drain()) {
			if kk == "err:ERROR_CODE_NOT_ARTIST" {
				found = true
			}
		}
		if !found {
			t.Fatal("non-artist stroke was not rejected")
		}

		ai := 0
		for i, id := range ids {
			if id == artist {
				ai = i
			}
		}
		r.Submit(Command{PlayerID: artist, Out: socks[ai], Cmd: &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 1, Width: 999, Points: []int32{10, 10},
			}},
		}})
		synctest.Wait()
		if w := smokeOpenWidth(r); w != MaxStrokeWidth {
			t.Fatalf("width not clamped: %d", w)
		}

		// Run all three drawing turns out, plus the handoff before each.
		regToDiscussion(t, r)
		if n := smokeStrokeCount(r); n != 1 {
			t.Fatalf("expected 1 committed stroke, got %d", n)
		}

		// Everyone votes for the same target: strict majority of 3 is 2.
		target := ids[1]
		for i, id := range ids {
			r.Submit(Command{PlayerID: id, Out: socks[i], Cmd: &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{
					Choice: &genpb.CastVote_CandidateId{CandidateId: target},
				}},
			}})
		}
		synctest.Wait()
		// Early end (DESIGN.md:52) — no need to wait out the 30 s timer.
		if got := smokePhase(r); got != genpb.Phase_PHASE_RESOLVING {
			t.Fatalf("expected resolving, got %v", got)
		}
		if !smokeEliminated(r, target) {
			t.Fatal("target not eliminated")
		}
		// Two active players remain => imposter wins, unless the imposter was the
		// one voted out.
		time.Sleep(ResolveDuration + time.Millisecond)
		synctest.Wait()
		if got := smokePhase(r); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("expected ended, got %v", got)
		}
		var me *genpb.MatchEnded
		for _, e := range s0.drain() {
			if m := e.GetMatchEnded(); m != nil {
				me = m
			}
		}
		if me == nil {
			t.Fatal("no MatchEnded")
		}
		if len(me.GetReveals()) != 3 {
			t.Fatalf("reveals: %v", me.GetReveals())
		}
		if target == smokeImposter(r) {
			if me.GetWinner() != genpb.WinnerSide_WINNER_SIDE_GROUP ||
				me.GetReason() != genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_ELIMINATED {
				t.Fatalf("expected group win, got %v/%v", me.GetWinner(), me.GetReason())
			}
		} else if me.GetWinner() != genpb.WinnerSide_WINNER_SIDE_IMPOSTER ||
			me.GetReason() != genpb.MatchEndReason_MATCH_END_REASON_TWO_PLAYERS_REMAIN {
			t.Fatalf("expected imposter win by two-remain, got %v/%v", me.GetWinner(), me.GetReason())
		}
		t.Logf("winner=%v reason=%v", me.GetWinner(), me.GetReason())

		// Rematch returns to the lobby with words cleared.
		r.Submit(Command{PlayerID: hostID, Out: s0, Cmd: &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_Rematch{Rematch: &genpb.Rematch{}},
		}})
		synctest.Wait()
		if got := smokePhase(r); got != genpb.Phase_PHASE_LOBBY || smokeImposter(r) != "" {
			t.Fatalf("rematch failed: %v", got)
		}
		if smokeAnyAssignment(r) {
			t.Fatal("stale assignment survived the rematch")
		}
		cancel()
		synctest.Wait()
	})
}

func TestRoomImposterDisconnectEndsMatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r := New("BCDE", "host", Options{
			Deck: smokeDeck{}, Rand: mrand.New(mrand.NewPCG(7, 9)),
			Settings: &genpb.MatchSettings{MaxRounds: 2, DrawSeconds: 5, DiscussSeconds: 30},
		})
		hostID, hostTok := r.HostSeat()
		go r.run(ctx)

		s0 := newSmokeSock()
		if _, err := r.attach(hostTok, s0); err != nil {
			t.Fatal(err)
		}
		s1, s2 := newSmokeSock(), newSmokeSock()
		p1, _, _ := r.seat("bee", s1)
		p2, _, _ := r.seat("cee", s2)
		ids := []string{hostID, p1, p2}
		socks := []*smokeSock{s0, s1, s2}
		for i, id := range ids {
			r.Submit(Command{PlayerID: id, Out: socks[i], Cmd: &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}},
			}})
		}
		r.Submit(Command{PlayerID: hostID, Out: s0, Cmd: &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}},
		}})
		synctest.Wait()

		imposter := smokeImposter(r)
		var oi int
		for i, id := range ids {
			if id == imposter {
				oi = i
			}
		}
		r.detach(imposter, socks[oi])
		synctest.Wait()
		if smokePhase(r) == genpb.Phase_PHASE_ENDED {
			t.Fatal("match ended before the grace window expired")
		}
		time.Sleep(GraceWindow + 2*SweepInterval)
		synctest.Wait()
		if got := smokePhase(r); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("expected ended, got %v", got)
		}
		if w, e := smokeOutcome(r); w != genpb.WinnerSide_WINNER_SIDE_GROUP ||
			e != genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_DISCONNECTED {
			t.Fatalf("got %v/%v", w, e)
		}
		cancel()
		synctest.Wait()
	})
}

func TestRoomReconnectReplaysCanvas(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r := New("CDEF", "host", Options{
			Deck: smokeDeck{}, Rand: mrand.New(mrand.NewPCG(3, 4)),
			Settings: &genpb.MatchSettings{MaxRounds: 1, DrawSeconds: 20, DiscussSeconds: 30},
		})
		hostID, hostTok := r.HostSeat()
		go r.run(ctx)
		s0 := newSmokeSock()
		if _, err := r.attach(hostTok, s0); err != nil {
			t.Fatal(err)
		}
		s1, s2 := newSmokeSock(), newSmokeSock()
		p1, tok1, _ := r.seat("bee", s1)
		p2, _, _ := r.seat("cee", s2)
		ids := []string{hostID, p1, p2}
		socks := []*smokeSock{s0, s1, s2}
		for i, id := range ids {
			r.Submit(Command{PlayerID: id, Out: socks[i], Cmd: &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}},
			}})
		}
		r.Submit(Command{PlayerID: hostID, Out: s0, Cmd: &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}},
		}})
		synctest.Wait()
		time.Sleep(AssignDuration + time.Millisecond)
		synctest.Wait()
		time.Sleep(smokeIntermission(r))
		synctest.Wait()

		artist := smokeArtist(r)
		var ai int
		for i, id := range ids {
			if id == artist {
				ai = i
			}
		}
		r.Submit(Command{PlayerID: artist, Out: socks[ai], Cmd: &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 2, Width: 5, Points: []int32{1, 2},
			}},
		}})
		r.Submit(Command{PlayerID: artist, Out: socks[ai], Cmd: &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}},
		}})
		synctest.Wait()

		// p1 drops and returns inside the grace window.
		r.detach(p1, s1)
		synctest.Wait()
		time.Sleep(5 * time.Second)
		s1b := newSmokeSock()
		if _, err := r.attach(tok1, s1b); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		var snap *genpb.Snapshot
		for _, e := range s1b.drain() {
			if s := e.GetSnapshot(); s != nil {
				snap = s
			}
		}
		if snap == nil {
			t.Fatal("no snapshot on reconnect")
		}
		if len(snap.GetStrokes()) != 1 {
			t.Fatalf("stroke log not replayed: %v", snap.GetStrokes())
		}
		if snap.GetYourWord() == "" {
			t.Fatal("reconnecting player lost their word")
		}
		if snap.GetPlayerId() != p1 {
			t.Fatalf("snapshot addressed to %s", snap.GetPlayerId())
		}
		_ = p2
		cancel()
		synctest.Wait()
	})
}

// TestRoomNoWordCrossesSockets is the empirical half of the secret-leak defense: drive
// a match and assert neither word appears in any frame delivered to a socket
// other than its owner's.
func TestRoomNoWordCrossesSockets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := New("DEFG", "host", Options{
			Deck: smokeDeck{}, Rand: mrand.New(mrand.NewPCG(11, 13)),
			Settings: &genpb.MatchSettings{MaxRounds: 1, DrawSeconds: 5, DiscussSeconds: 30},
		})
		hostID, hostTok := r.HostSeat()
		go r.run(ctx)
		s0 := newSmokeSock()
		if _, err := r.attach(hostTok, s0); err != nil {
			t.Fatal(err)
		}
		s1, s2 := newSmokeSock(), newSmokeSock()
		p1, _, _ := r.seat("bee", s1)
		p2, _, _ := r.seat("cee", s2)
		ids := []string{hostID, p1, p2}
		socks := []*smokeSock{s0, s1, s2}
		for i, id := range ids {
			r.Submit(Command{PlayerID: id, Out: socks[i], Cmd: &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}},
			}})
		}
		r.Submit(Command{PlayerID: hostID, Out: s0, Cmd: &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}},
		}})
		synctest.Wait()
		time.Sleep(AssignDuration + 4*5*time.Second + time.Millisecond)
		synctest.Wait()

		for i, s := range socks {
			mine := smokeWordOf(r, ids[i])
			for _, e := range s.drain() {
				var seen string
				if w := e.GetYourWord(); w != nil {
					seen = w.GetWord()
				}
				if sn := e.GetSnapshot(); sn != nil {
					seen = sn.GetYourWord()
				}
				if seen != "" && seen != mine {
					t.Fatalf("player %d received word %q, owns %q", i, seen, mine)
				}
			}
		}
		cancel()
		synctest.Wait()
	})
}
