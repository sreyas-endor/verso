package room

// reconnect_test.go — milestone 7. Seats, the 60 s grace window, the denominator
// a disconnected seat leaves behind, and host migration.
//
// ⚠ One rule here reverses an earlier decision. DESIGN.md ("Active players")
// and IMPLEMENTATION_PLAN.md §4.6 now both say a disconnected-but-seated player
// is NOT active: they are excluded from the strict-majority denominator, the
// turn order and the two-players-remain count, and their missing vote is not
// counted as a Skip either — they are simply not in the tally at all. The
// earlier resolution ("stays in the denominator, votes Skip") is dead. The
// tests below assert the rule as the specs now state it; see
// TestDisconnectedSeatLeavesTheMajorityDenominator.

import (
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// drawOneStroke puts a committed stroke on the canvas as the current artist,
// then opens a second one and leaves it in flight.
func (h *harness) drawOneStroke(ai int) {
	h.send(ai, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeBegin{
		StrokeBegin: &genpb.StrokeBegin{ColorIndex: 2, Width: 5, Points: []int32{10, 20, 30, 40}}}})
	h.send(ai, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokePoints{
		StrokePoints: &genpb.StrokePoints{Points: []int32{50, 60}}}})
	h.send(ai, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeEnd{
		StrokeEnd: &genpb.StrokeEnd{}}})
	h.send(ai, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeBegin{
		StrokeBegin: &genpb.StrokeBegin{ColorIndex: 7, Width: 9, Points: []int32{-100, 700}}}})
	synctest.Wait()
}

// snapshotOf asks for a full resync on a player's own socket and returns it.
func (h *harness) snapshotOf(i int) *genpb.Snapshot {
	h.t.Helper()
	h.send(i, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_RequestSnapshot{
		RequestSnapshot: &genpb.RequestSnapshot{}}})
	synctest.Wait()
	snap := lastSnapshot(h.drain(i))
	if snap == nil {
		h.t.Fatalf("player %d received no Snapshot", i)
	}
	return snap
}

// TestReconnectMidTurnRestoresIdenticalState — IMPLEMENTATION_PLAN.md §4.6 and
// milestone 7. A client drops in the middle of a drawing turn and comes back to
// exactly the state it left: same seat, same seat token, same word, the whole
// stroke log replayed in one message including the stroke still in flight.
//
// The assertion is a whole-Snapshot comparison rather than a handful of
// spot-checks. Only the countdown is allowed to differ, because only the
// countdown actually moved.
func TestReconnectMidTurnRestoresIdenticalState(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(2, 60, 120), 5001)
		defer h.stop()
		h.discard()
		h.start()
		h.toDrawing()

		ai := h.artistIdx()
		h.drawOneStroke(ai)

		// Somebody who is neither the artist nor the imposter drops, so the turn
		// is not skipped and the match cannot end underneath the test.
		victim := h.anyIdxExcept(ai, h.imposterIdx())
		before := h.snapshotOf(victim)
		if before.GetYourWord() == "" {
			t.Fatal("the player holds no word before dropping")
		}
		if n := len(before.GetStrokes()); n != 2 {
			t.Fatalf("stroke log has %d entries before the drop, want 2 "+
				"(one committed, one still open)", n)
		}

		h.r.detach(h.ids[victim], h.socks[victim])
		synctest.Wait()
		if h.active(victim) {
			t.Fatal("a detached seat is still active")
		}
		if got := h.activeCount(); got != 5 {
			t.Fatalf("active = %d while one seat is dark, want 5", got)
		}

		h.advance(5 * time.Second)

		// A brand new socket, the same seat token.
		sock := newSmokeSock()
		h.socks[victim] = sock
		id, err := h.r.attach(h.toks[victim], sock)
		if err != nil {
			t.Fatalf("reattach: %v", err)
		}
		if id != h.ids[victim] {
			t.Fatalf("reattach returned player %q, want %q", id, h.ids[victim])
		}
		synctest.Wait()

		evs := h.drain(victim)
		joined := lastJoined(evs)
		if joined == nil {
			t.Fatal("no Joined on reconnect")
		}
		if !joined.GetReconnected() {
			t.Fatal("Joined.reconnected is false for a returning player")
		}
		if joined.GetPlayerId() != h.ids[victim] {
			t.Fatalf("Joined.player_id = %q, want %q", joined.GetPlayerId(), h.ids[victim])
		}
		if joined.GetSeatToken() != h.toks[victim] {
			t.Fatal("the seat token changed across a reconnect")
		}
		if got := joined.GetGraceSeconds(); got != int32(GraceWindow/time.Second) {
			t.Fatalf("Joined.grace_seconds = %d, want %d", got, GraceWindow/time.Second)
		}

		after := lastSnapshot(evs)
		if after == nil {
			t.Fatal("no Snapshot on reconnect")
		}
		if after.GetYourWord() != before.GetYourWord() {
			t.Fatalf("word changed across the reconnect: %q -> %q",
				before.GetYourWord(), after.GetYourWord())
		}
		if n := len(after.GetStrokes()); n != 2 {
			t.Fatalf("stroke log replayed %d strokes, want 2", n)
		}

		// Everything except the countdown must be byte-for-byte the state the
		// player left. Five virtual seconds of a 60 s turn went by, so the clock
		// is the one field allowed to move.
		a := proto.Clone(before).(*genpb.Snapshot)
		b := proto.Clone(after).(*genpb.Snapshot)
		if b.GetRemainingMs() >= a.GetRemainingMs() {
			t.Fatalf("the turn clock did not advance: %d -> %d",
				a.GetRemainingMs(), b.GetRemainingMs())
		}
		a.RemainingMs, b.RemainingMs = 0, 0
		if !proto.Equal(a, b) {
			t.Fatalf("state differs across the reconnect:\nbefore %v\nafter  %v", a, b)
		}

		// And the seat itself is unchanged in the public roster.
		var seat int32 = -1
		for _, pi := range after.GetPlayers() {
			if pi.GetId() == h.ids[victim] {
				seat = pi.GetSeat()
				if !pi.GetConnected() {
					t.Fatal("the reconnected player is still listed as disconnected")
				}
			}
		}
		if seat != int32(victim) {
			t.Fatalf("seat = %d, want %d", seat, victim)
		}
		if got := h.activeCount(); got != 6 {
			t.Fatalf("active = %d after the reconnect, want 6", got)
		}
	})
}

// TestSeatReclaimableUntilTheGraceWindowExpires — open question 2, resolved at
// 60 s. The seat is still there one sweep before the window closes.
func TestSeatReclaimableUntilTheGraceWindowExpires(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(4, 60, 120), 5002)
		defer h.stop()
		h.discard()
		h.start()
		h.toDrawing()

		victim := h.anyIdxExcept(h.artistIdx(), h.imposterIdx())
		h.r.detach(h.ids[victim], h.socks[victim])
		synctest.Wait()

		// The countdown is published so the UI can show it.
		var grace int32
		for _, e := range h.drain(0) {
			if pp := e.GetPlayerPresence(); pp != nil && pp.GetPlayer().GetId() == h.ids[victim] {
				grace = pp.GetGraceSecondsRemaining()
				if pp.GetPlayer().GetConnected() {
					t.Fatal("presence says a detached player is connected")
				}
			}
		}
		if grace != int32(GraceWindow/time.Second) {
			t.Fatalf("grace_seconds_remaining = %d at the moment of the drop, want %d",
				grace, GraceWindow/time.Second)
		}

		h.advance(GraceWindow - 2*SweepInterval)
		if h.eliminated(victim) {
			t.Fatal("the seat expired before the grace window ran out")
		}

		sock := newSmokeSock()
		h.socks[victim] = sock
		if _, err := h.r.attach(h.toks[victim], sock); err != nil {
			t.Fatalf("reattach inside the window: %v", err)
		}
		synctest.Wait()
		if !h.active(victim) {
			t.Fatal("a player who returned inside the window is not active")
		}
		if h.eliminated(victim) {
			t.Fatal("a player who returned inside the window was eliminated")
		}

		// The window restarts clean: the seat survives another full 60 s of
		// being present, and only expires if it goes dark again.
		h.advance(GraceWindow + 2*SweepInterval)
		if h.eliminated(victim) {
			t.Fatal("a connected seat expired")
		}
	})
}

// TestGraceWindowExpiryRetiresTheSeat — the window closes; the seat and its
// word are kept, but the player comes back as a spectator, not a participant.
func TestGraceWindowExpiryRetiresTheSeat(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(4, 60, 120), 5003)
		defer h.stop()
		h.discard()
		h.start()
		h.toDrawing()

		victim := h.anyIdxExcept(h.artistIdx(), h.imposterIdx())
		word := h.word(victim)
		h.r.detach(h.ids[victim], h.socks[victim])
		synctest.Wait()

		h.advance(GraceWindow + 2*SweepInterval)
		if !h.eliminated(victim) {
			t.Fatal("the seat did not expire after the grace window")
		}
		if h.active(victim) {
			t.Fatal("an expired seat is still active")
		}
		if got := h.activeCount(); got != 5 {
			t.Fatalf("active = %d, want 5", got)
		}
		if got := h.seatCount(); got != 6 {
			t.Fatalf("seats = %d: an expired seat is retired mid-match, not removed", got)
		}
		if got := h.word(victim); got != word {
			t.Fatalf("the expired seat lost its word: %q -> %q", word, got)
		}

		// The token still works — a late return is allowed, as a spectator.
		sock := newSmokeSock()
		h.socks[victim] = sock
		if _, err := h.r.attach(h.toks[victim], sock); err != nil {
			t.Fatalf("late reattach: %v", err)
		}
		synctest.Wait()
		snap := lastSnapshot(h.drain(victim))
		if snap == nil {
			t.Fatal("no Snapshot on a late reattach")
		}
		if !snap.GetYouAreEliminated() {
			t.Fatal("a player whose window expired came back as an active player")
		}
		if snap.GetYourWord() != word {
			t.Fatalf("the returning spectator was given %q, held %q", snap.GetYourWord(), word)
		}
		if h.active(victim) {
			t.Fatal("an expired seat became active again by reconnecting")
		}
		if got := h.activeCount(); got != 5 {
			t.Fatalf("active = %d after the spectator returned, want 5", got)
		}
	})
}

// TestDisconnectedSeatLeavesTheMajorityDenominator — DESIGN.md, "Active
// players".
//
// This is the rule that reversed. A disconnected seat is out of the
// denominator for as long as it is dark, and its absence is NOT counted as a
// Skip: the numerator and the denominator move together, so the window still
// closes on the players who are actually here. Keeping an absent player in
// would hold every round open until the combined timer ran out.
func TestDisconnectedSeatLeavesTheVotingDenominator(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(4, 5, 120), 5004)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		gone := 5
		h.r.detach(h.ids[gone], h.socks[gone])
		synctest.Wait()

		if got := h.activeCount(); got != 5 {
			t.Fatalf("active = %d with one seat dark, want 5", got)
		}
		// The room republishes the progress line so no client is left showing a
		// denominator that no longer exists.
		var lastCount *genpb.VoteCastCount
		for _, e := range h.drain(0) {
			if v := e.GetVoteCastCount(); v != nil {
				lastCount = v
			}
		}
		if lastCount == nil {
			t.Fatal("the denominator changed without a VoteCastCount")
		}
		if got := lastCount.GetActiveCount(); got != 5 {
			t.Fatalf("VoteCastCount.active_count = %d, want 5", got)
		}

		// Three of the five who are actually here now win outright against two
		// explicit Skips.
		target := h.ids[1]
		for i := range 3 {
			h.vote(i, target)
		}
		h.voteSkip(3)
		h.voteSkip(4)
		synctest.Wait()

		resolution := h.drain(0)
		tally := lastTally(resolution)
		if tally == nil {
			t.Fatal("no VoteTally")
		}
		if got := tally.GetActiveCount(); got != 5 {
			t.Fatalf("tally active_count = %d, want 5", got)
		}
		if got := tally.GetSkipCount(); got != 2 {
			t.Fatalf("skip_count = %d, want 2 — the dark seat is out of the tally "+
				"entirely, not counted as a Skip", got)
		}
		if got := countVotes(tally, target); got != 3 {
			t.Fatalf("votes for target = %d, want 3", got)
		}
		if !h.eliminated(1) {
			t.Fatal("3 votes against 2 Skips wins outright but nobody was eliminated")
		}
	})
}

// TestReconnectingSeatRejoinsTheDenominator — the exclusion lasts exactly as
// long as the socket is gone.
func TestReconnectingSeatRejoinsTheDenominator(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(4, 5, 120), 5005)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		gone := 5
		h.r.detach(h.ids[gone], h.socks[gone])
		synctest.Wait()
		if got := h.activeCount(); got != 5 {
			t.Fatalf("active = %d, want 5", got)
		}
		h.discard()

		sock := newSmokeSock()
		h.socks[gone] = sock
		if _, err := h.r.attach(h.toks[gone], sock); err != nil {
			t.Fatalf("reattach: %v", err)
		}
		synctest.Wait()

		if got := h.activeCount(); got != 6 {
			t.Fatalf("active = %d after the reconnect, want 6", got)
		}
		var lastCount *genpb.VoteCastCount
		for _, e := range h.drain(0) {
			if v := e.GetVoteCastCount(); v != nil {
				lastCount = v
			}
		}
		if lastCount == nil {
			t.Fatal("the denominator grew without a VoteCastCount")
		}
		if got := lastCount.GetActiveCount(); got != 6 {
			t.Fatalf("VoteCastCount.active_count = %d, want 6", got)
		}
		// The returning player may still vote: nothing was recorded for them
		// while they were away.
		h.voteSkip(gone)
		synctest.Wait()
		found := false
		for _, e := range h.drain(gone) {
			if e.GetVoteAccepted() != nil {
				found = true
			}
		}
		if !found {
			t.Fatal("a reconnected player was not allowed to vote")
		}
	})
}

// TestVoteIsDroppedWhenTheVoterDisconnects documents an interaction the two
// rules produce together, which neither states on its own.
//
// DESIGN.md:49 calls a vote irreversible. DESIGN.md ("Active players") takes a
// disconnected seat out of the denominator. tally() filters voters through
// Active(), so a player who votes and then loses their connection loses the
// vote as well. It is self-consistent — they leave the numerator and the
// denominator together, so no round is ever decided by a phantom — but it does
// mean a network blip inside the voting window silently revokes a cast vote.
func TestVoteIsDroppedWhenTheVoterDisconnects(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(4, 5, 120), 5006)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		target := h.ids[0]
		h.vote(5, target)
		synctest.Wait()

		h.r.detach(h.ids[5], h.socks[5])
		synctest.Wait()
		h.discard()

		// Five active players remain and none of them voted for the target.
		for i := range 5 {
			h.voteSkip(i)
		}
		synctest.Wait()

		tally := lastTally(h.drain(0))
		if tally == nil {
			t.Fatal("no VoteTally")
		}
		if got := tally.GetActiveCount(); got != 5 {
			t.Fatalf("active_count = %d, want 5", got)
		}
		if got := countVotes(tally, target); got != 0 {
			t.Fatalf("the departed player's vote still counts: target has %d votes", got)
		}
		if got := tally.GetSkipCount(); got != 5 {
			t.Fatalf("skip_count = %d, want 5", got)
		}
	})
}

// TestHostMigrationPromotesTheLongestConnectedActivePlayer — open question 4.
// The key is JoinedAt, and a dark or eliminated seat is not a candidate.
func TestHostMigrationPromotesTheLongestConnectedActivePlayer(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// newHarness takes seats one virtual second apart, so JoinedAt is a
		// strict order and "longest connected" is not just "lowest seat".
		h := newHarness(t, 5, mkSettings(2, 5, 30), 5007)
		defer h.stop()
		h.discard()

		if got := h.hostIdx(); got != 0 {
			t.Fatalf("host = %d, want the room's creator at 0", got)
		}

		// The next-longest-connected player goes dark first, so promotion has
		// to skip them.
		h.r.detach(h.ids[1], h.socks[1])
		synctest.Wait()
		h.discard()

		h.r.detach(h.ids[0], h.socks[0])
		synctest.Wait()

		if got := h.hostIdx(); got != 2 {
			t.Fatalf("host = %d, want 2 — seat 1 is disconnected and cannot host", got)
		}
		// The room says so out loud, on both seats it touched.
		hosts := map[string]bool{}
		for _, e := range h.drain(3) {
			if pp := e.GetPlayerPresence(); pp != nil {
				hosts[pp.GetPlayer().GetId()] = pp.GetPlayer().GetIsHost()
			}
		}
		if hosts[h.ids[0]] {
			t.Fatal("the departed host is still flagged as host on the wire")
		}
		if !hosts[h.ids[2]] {
			t.Fatal("the promotion was never broadcast")
		}

		// Coming back does not take the room back.
		sock := newSmokeSock()
		h.socks[1] = sock
		if _, err := h.r.attach(h.toks[1], sock); err != nil {
			t.Fatalf("reattach: %v", err)
		}
		synctest.Wait()
		if got := h.hostIdx(); got != 2 {
			t.Fatalf("host = %d after seat 1 returned, want 2", got)
		}

		// And the next migration picks seat 1, now the longest-connected
		// active player again.
		h.r.detach(h.ids[2], h.socks[2])
		synctest.Wait()
		if got := h.hostIdx(); got != 1 {
			t.Fatalf("host = %d, want 1", got)
		}
	})
}

// TestHostMigrationSkipsEliminatedPlayers — an eliminated player is a silent
// spectator (DESIGN.md:66) and must not end up running the room.
func TestHostMigrationSkipsEliminatedPlayers(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(4, 5, 60), 5008)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		// Vote out seat 1 — the longest-connected player after the host.
		victim := 1
		if h.imposterIdx() == victim {
			victim = h.anyIdxExcept(0, h.imposterIdx())
		}
		voted := 0
		for i := range h.ids {
			if i != victim && voted < 3 {
				h.vote(i, h.ids[victim])
				voted++
				continue
			}
			h.voteSkip(i)
		}
		synctest.Wait()
		if !h.eliminated(victim) {
			t.Fatal("3 of 4 did not eliminate")
		}
		if h.phase() == genpb.Phase_PHASE_ENDED {
			t.Fatal("the match ended; nothing left to migrate")
		}

		h.r.detach(h.ids[0], h.socks[0])
		synctest.Wait()
		got := h.hostIdx()
		if got == victim {
			t.Fatal("an eliminated player was promoted to host")
		}
		if got <= 0 {
			t.Fatalf("host = %d, want a surviving player", got)
		}
	})
}

// TestDisplacedSocketCannotDisturbTheNewOne — a reconnect replaces the socket;
// the old one is told why, is ignored from then on, and its late Detach must
// not knock the live connection offline.
func TestDisplacedSocketCannotDisturbTheNewOne(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(2, 5, 30), 5009)
		defer h.stop()
		h.discard()

		old := h.socks[1]
		fresh := newSmokeSock()
		if _, err := h.r.attach(h.toks[1], fresh); err != nil {
			t.Fatalf("attach: %v", err)
		}
		synctest.Wait()

		if !hasErrorCode(old.drain(), genpb.ErrorCode_ERROR_CODE_BAD_SEAT) {
			t.Fatal("the displaced socket was not told its seat was reclaimed")
		}

		// A frame from the stale socket is dropped: identity comes from the
		// seat, not from the frame body.
		h.r.Submit(Command{PlayerID: h.ids[1], Out: old, Cmd: &genpb.ClientCommand{
			Cid: "stale", Cmd: &genpb.ClientCommand_SetReady{
				SetReady: &genpb.SetReady{Ready: false}}}})
		synctest.Wait()
		if got := old.drain(); len(got) != 0 {
			t.Fatalf("the stale socket got a reply to a dropped frame: %v", smokeKinds(got))
		}
		if !smokeGet(h.r, func(r *Room) bool { return r.byID[h.ids[1]].Ready }) {
			t.Fatal("a frame from a displaced socket changed room state")
		}

		// And a late detach for the old socket is ignored.
		h.r.detach(h.ids[1], old)
		synctest.Wait()
		if !smokeGet(h.r, func(r *Room) bool { return r.byID[h.ids[1]].Connected }) {
			t.Fatal("a late detach from a replaced socket knocked the live one offline")
		}

		h.socks[1] = fresh
		h.discard()
		h.start()
		if got := h.phase(); got != genpb.Phase_PHASE_ASSIGNING {
			t.Fatalf("phase = %v", got)
		}
	})
}

// TestUnknownSeatTokenRejected — the token is the only credential, and it is
// not guessable.
func TestUnknownSeatTokenRejected(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(2, 5, 30), 5010)
		defer h.stop()

		sock := newSmokeSock()
		if _, err := h.r.attach("not-a-real-token", sock); err != ErrBadSeat {
			t.Fatalf("attach with a bogus token returned %v, want ErrBadSeat", err)
		}
		if got := h.seatCount(); got != 3 {
			t.Fatalf("seats = %d after a rejected attach, want 3", got)
		}
	})
}

// TestLobbyDisconnectFreesTheSeatOutright — outside a running match nobody
// holds a word, so an expired seat is removed rather than retired, and the
// roster shrinks.
func TestLobbyDisconnectFreesTheSeatOutright(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(2, 5, 30), 5011)
		defer h.stop()
		h.discard()

		h.r.detach(h.ids[3], h.socks[3])
		synctest.Wait()
		h.advance(GraceWindow + 2*SweepInterval)

		if got := h.seatCount(); got != 3 {
			t.Fatalf("seats = %d after a lobby seat expired, want 3", got)
		}
		if smokeGet(h.r, func(r *Room) bool { return r.byID[h.ids[3]] != nil }) {
			t.Fatal("the expired lobby seat is still indexed by id")
		}
		// The token is dead with it.
		sock := newSmokeSock()
		if _, err := h.r.attach(h.toks[3], sock); err != ErrBadSeat {
			t.Fatalf("attach with a freed seat token returned %v, want ErrBadSeat", err)
		}
		// The remaining three can still start.
		h.start()
	})
}

// TestSeatExpiringOnTheResultScreenWaitsForIt — reevaluateEnd must not publish
// the final reveal while the vote-result screen is still up. The end condition
// is already recorded; afterResolve owns the transition, and jumping it would
// snatch the tally off the screen mid-read.
func TestSeatExpiringOnTheResultScreenWaitsForIt(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(1, 5, 120), 5012)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		victim := h.anyIdxExcept(h.imposterIdx())
		h.r.detach(h.ids[victim], h.socks[victim])
		synctest.Wait()

		// Sit inside the voting window until the grace deadline is a few
		// seconds out, then close the round: the seat now expires while the
		// result screen is showing.
		h.advance(GraceWindow - 5*time.Second)
		h.skipAll()
		if got := h.phase(); got != genpb.Phase_PHASE_RESOLVING {
			t.Fatalf("phase = %v, want RESOLVING", got)
		}

		h.advance(6 * time.Second)
		if !h.eliminated(victim) {
			t.Fatal("the grace window did not expire")
		}
		if got := h.phase(); got != genpb.Phase_PHASE_RESOLVING {
			t.Fatalf("phase = %v: the result screen was cut short by a seat expiry", got)
		}

		h.advance(3 * time.Second)
		if got := h.phase(); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("phase = %v, want ENDED once the result screen finished", got)
		}
		if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_IMPOSTER ||
			reason != genpb.MatchEndReason_MATCH_END_REASON_FINAL_ROUND_SURVIVED {
			t.Fatalf("outcome = %v / %v, want IMPOSTER / FINAL_ROUND_SURVIVED", w, reason)
		}
	})
}

// TestEmptyRoomClosesItself — the cheap room GC. Once every seat has expired
// there is nothing left to reconnect to, so the actor stops and releases the
// code rather than living for the idle TTL.
func TestEmptyRoomClosesItself(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(2, 5, 30), 5013)
		defer h.stop()

		for i := range h.ids {
			h.r.detach(h.ids[i], h.socks[i])
		}
		synctest.Wait()
		h.advance(GraceWindow + 2*SweepInterval)

		if err := h.r.do(func() {}); err != ErrClosed {
			t.Fatalf("the room is still running with no seats left: %v", err)
		}
		select {
		case <-h.r.done:
		default:
			t.Fatal("Run returned without closing done")
		}
		// Every entry point is safe after the actor is gone.
		if _, _, err := h.r.Seat("late", genpb.Avatar_AVATAR_COURIER, newSmokeSock()); err != ErrClosed {
			t.Fatalf("Seat on a closed room = %v, want ErrClosed", err)
		}
	})
}

// TestHostPromotionWhenTheLastDarkSeatIsCollected — a host who goes dark while
// nobody else is connected has no successor to migrate to, so they keep the
// flag. The promotion then has to happen when their seat is finally collected,
// not when they left.
func TestHostPromotionWhenTheLastDarkSeatIsCollected(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(2, 5, 30), 5014)
		defer h.stop()
		h.discard()

		// Order matters: the host leaves last, so bestHost has no candidate and
		// the flag stays on a disconnected seat.
		h.r.detach(h.ids[1], h.socks[1])
		h.r.detach(h.ids[2], h.socks[2])
		h.r.detach(h.ids[0], h.socks[0])
		synctest.Wait()
		if got := h.hostIdx(); got != 0 {
			t.Fatalf("host = %d, want the original host to keep the flag with "+
				"nobody to hand it to", got)
		}

		h.advance(GraceWindow / 2)
		sock := newSmokeSock()
		h.socks[2] = sock
		if _, err := h.r.attach(h.toks[2], sock); err != nil {
			t.Fatalf("reattach: %v", err)
		}
		synctest.Wait()
		if got := h.hostIdx(); got != 0 {
			t.Fatalf("host = %d: a returning player took the room from a seat "+
				"that is still inside its grace window", got)
		}

		// The two abandoned seats expire and are freed; the flag lands on the
		// only player still here.
		h.advance(GraceWindow/2 + 2*SweepInterval)
		if got := h.seatCount(); got != 1 {
			t.Fatalf("seats = %d, want 1", got)
		}
		if got := h.hostIdx(); got != 2 {
			t.Fatalf("host = %d, want 2", got)
		}
	})
}

// TestEveryoneButTheImposterLeavingAbandonsTheMatch — fewer than two active
// players is not a win for anybody. DESIGN.md names no winner for this, and
// neither does the room.
func TestEveryoneButTheImposterLeavingAbandonsTheMatch(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(4, 60, 120), 5015)
		defer h.stop()
		h.discard()
		h.start()
		h.toDrawing()

		oi := h.imposterIdx()
		for i := range h.ids {
			if i == oi {
				continue
			}
			h.r.detach(h.ids[i], h.socks[i])
		}
		synctest.Wait()
		if got := h.activeCount(); got != 1 {
			t.Fatalf("active = %d, want only the imposter", got)
		}
		if got := h.phase(); got == genpb.Phase_PHASE_ENDED {
			t.Fatal("the match ended before any grace window expired")
		}

		h.advance(GraceWindow + 2*SweepInterval)
		if got := h.phase(); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("phase = %v, want ENDED", got)
		}
		if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_UNSPECIFIED ||
			reason != genpb.MatchEndReason_MATCH_END_REASON_ABANDONED {
			t.Fatalf("outcome = %v / %v, want no winner / ABANDONED", w, reason)
		}
		me := lastMatchEnded(h.drain(oi))
		if me == nil {
			t.Fatal("the last player standing was never told the match ended")
		}
		if me.GetWinner() != genpb.WinnerSide_WINNER_SIDE_UNSPECIFIED {
			t.Fatalf("MatchEnded winner = %v", me.GetWinner())
		}
	})
}
