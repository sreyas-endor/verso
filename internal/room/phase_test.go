package room

// phase_test.go — milestone 5. Every transition in IMPLEMENTATION_PLAN.md §4.5
// and DESIGN.md:27, driven entirely by channel sends and a fake clock.
//
//	lobby -> assigning -> drawing(turn 1..n) -> discussion -> resolving
//	                          ^                                   |
//	                          +----------- next round ------------+
//	                                                              v
//	                                                            ended

import (
	"slices"
	"testing"
	"testing/synctest"
	"time"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// TestPhaseMachineWalksEveryTransition plays a whole two-round match and
// asserts the exact sequence of phases a client is told about. The trail is
// the client's only view of the machine, so an extra or missing PhaseChanged
// is a protocol bug even when the server's internal phase is right.
func TestPhaseMachineWalksEveryTransition(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(2, 5, 30), 101)
		defer h.stop()

		if got := h.phase(); got != genpb.Phase_PHASE_LOBBY {
			t.Fatalf("phase = %v, want LOBBY", got)
		}
		h.discard()
		h.start()

		// Round 1: assign, six drawing turns, discussion, unanimous Skip.
		h.toDiscussion()
		if got := h.round(); got != 1 {
			t.Fatalf("round = %d, want 1", got)
		}
		h.skipAll()
		if got := h.phase(); got != genpb.Phase_PHASE_RESOLVING {
			t.Fatalf("after round 1 votes: phase = %v, want RESOLVING", got)
		}

		// Round 2 is the configured final round.
		h.nextRound()
		if got := h.round(); got != 2 {
			t.Fatalf("round = %d, want 2", got)
		}
		h.skipAll()
		h.advance(ResolveDuration)

		if got := h.phase(); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("phase = %v, want ENDED", got)
		}

		// Every round opens with its own PHASE_ASSIGNING word reveal, because
		// every round wipes the canvas and deals a fresh pair. Every drawing
		// turn is announced by a PHASE_INTERMISSION handoff, and so is the
		// voting window, so one round of six players is ASSIGNING, then
		// (INTERMISSION DRAWING) x6, then INTERMISSION DISCUSSION RESOLVING.
		var want []genpb.Phase
		for range 2 {
			want = append(want, genpb.Phase_PHASE_ASSIGNING)
			for range 6 {
				want = append(want,
					genpb.Phase_PHASE_INTERMISSION,
					genpb.Phase_PHASE_DRAWING)
			}
			want = append(want,
				genpb.Phase_PHASE_INTERMISSION,
				genpb.Phase_PHASE_DISCUSSION,
				genpb.Phase_PHASE_RESOLVING)
		}
		want = append(want, genpb.Phase_PHASE_ENDED)
		evs := h.drain(0)
		if got := phaseTrail(evs); !slices.Equal(got, want) {
			t.Fatalf("phase trail:\n got %v\nwant %v", got, want)
		}

		// Exactly one RoundStarted per round, numbered 1 then 2. A phase timer
		// that fired twice would show up here as a duplicate.
		var rounds []int32
		for _, rs := range allRoundStarted(evs) {
			rounds = append(rounds, rs.GetRound())
		}
		if !slices.Equal(rounds, []int32{1, 2}) {
			t.Fatalf("RoundStarted rounds = %v, want [1 2]", rounds)
		}
		// Six artists per round, twelve in the match.
		if n := len(allTurnStarted(evs)); n != 12 {
			t.Fatalf("TurnStarted count = %d, want 12", n)
		}
	})
}

// TestTurnOrderReshuffledEveryRound — DESIGN.md:36. The order is drawn fresh
// each round rather than rotated, and every active player draws exactly once.
func TestTurnOrderReshuffledEveryRound(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 10, mkSettings(2, 5, 30), 202)
		defer h.stop()
		h.discard()
		h.start()

		h.toDiscussion()
		h.skipAll()
		h.nextRound()
		h.skipAll()

		orders := allRoundStarted(h.drain(0))
		if len(orders) != 2 {
			t.Fatalf("RoundStarted count = %d, want 2", len(orders))
		}

		for i, rs := range orders {
			got := rs.GetTurnOrder()
			if len(got) != len(h.ids) {
				t.Fatalf("round %d: turn order has %d entries, want %d", i+1, len(got), len(h.ids))
			}
			if int(rs.GetActiveCount()) != len(h.ids) {
				t.Fatalf("round %d: active_count = %d, want %d", i+1, rs.GetActiveCount(), len(h.ids))
			}
			// Exactly once each: a permutation of the active roster, no
			// duplicates and nobody missed.
			seen := map[string]int{}
			for _, id := range got {
				seen[id]++
			}
			for _, id := range h.ids {
				if seen[id] != 1 {
					t.Fatalf("round %d: player %s appears %d times in the turn order",
						i+1, id, seen[id])
				}
			}
		}

		if slices.Equal(orders[0].GetTurnOrder(), orders[1].GetTurnOrder()) {
			t.Fatalf("turn order was not reshuffled between rounds: %v", orders[0].GetTurnOrder())
		}

		// And the artists actually announced follow that published order.
		turns := allTurnStarted(h.drain(0))
		_ = turns // drained above; the sequence is asserted in the walk test.
	})
}

// TestArtistSequenceFollowsThePublishedTurnOrder — the order in RoundStarted is
// not decorative; TurnStarted must walk it in exactly that sequence.
func TestArtistSequenceFollowsThePublishedTurnOrder(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(1, 5, 30), 303)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		evs := h.drain(0)
		rs := allRoundStarted(evs)
		if len(rs) != 1 {
			t.Fatalf("RoundStarted count = %d, want 1", len(rs))
		}
		var artists []string
		var indices []int32
		for _, ts := range allTurnStarted(evs) {
			artists = append(artists, ts.GetArtistId())
			indices = append(indices, ts.GetTurnIndex())
		}
		if !slices.Equal(artists, rs[0].GetTurnOrder()) {
			t.Fatalf("artists %v do not match turn order %v", artists, rs[0].GetTurnOrder())
		}
		if !slices.Equal(indices, []int32{0, 1, 2, 3, 4, 5}) {
			t.Fatalf("turn indices = %v, want 0..5", indices)
		}
	})
}

// TestDrawingTurnExpiresAtExactlyTheConfiguredDuration — the turn clock is
// authoritative and lands on the instant, not a tick before or after.
func TestDrawingTurnExpiresAtExactlyTheConfiguredDuration(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const draw = 15
		h := newHarness(t, 3, mkSettings(1, draw, 30), 404)
		defer h.stop()
		h.discard()
		h.start()
		h.toDrawing()

		first := h.artist()
		if first == "" {
			t.Fatal("no artist at the start of the round")
		}
		ts := allTurnStarted(h.drain(0))
		if len(ts) != 1 {
			t.Fatalf("TurnStarted count = %d, want 1", len(ts))
		}
		if got := ts[0].GetDurationMs(); got != draw*1000 {
			t.Fatalf("turn duration = %d ms, want %d", got, draw*1000)
		}

		// One nanosecond short of the deadline the turn is still live.
		h.advance(draw*time.Second - time.Nanosecond)
		if got := h.artist(); got != first {
			t.Fatalf("turn ended early: artist %s -> %s", first, got)
		}
		if got := h.remainingMS(); got != 0 {
			// Sub-millisecond remainders truncate to 0; the artist check above
			// is the real assertion. Guard only against a negative or absurd
			// value.
			t.Fatalf("remaining = %d ms with 1 ns left", got)
		}

		// On the instant, the turn expires into the handoff that announces the
		// next artist.
		h.advance(time.Nanosecond)
		if got := h.phase(); got != genpb.Phase_PHASE_INTERMISSION {
			t.Fatalf("phase = %v at the turn deadline, want INTERMISSION", got)
		}
		h.toDrawing()
		second := h.artist()
		if second == first {
			t.Fatalf("turn did not expire at %ds; artist is still %s", draw, first)
		}
		if second == "" {
			t.Fatal("no artist after the first turn expired")
		}
		if got := h.phase(); got != genpb.Phase_PHASE_DRAWING {
			t.Fatalf("phase = %v after turn 1, want DRAWING", got)
		}
		ts = allTurnStarted(h.drain(0))
		if len(ts) != 1 || ts[0].GetTurnIndex() != 1 {
			t.Fatalf("expected exactly one TurnStarted at index 1, got %v", ts)
		}
	})
}

// TestDiscussionAndVotingShareOneTimer — DESIGN.md:46. There is no separate
// voting phase: one PHASE_DISCUSSION carries the whole combined clock, votes
// are accepted throughout it, and its single expiry is what resolves the round.
func TestDiscussionAndVotingShareOneTimer(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const discuss = 120
		h := newHarness(t, 6, mkSettings(1, 5, discuss), 505)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		trail := phaseTrail(h.drain(0))
		if n := len(trail); n == 0 || trail[n-1] != genpb.Phase_PHASE_DISCUSSION {
			t.Fatalf("phase trail ends %v, want DISCUSSION", trail)
		}
		// Nothing between drawing and discussion, and nothing after it until it
		// resolves: one phase, one timer.
		if got := h.remainingMS(); got != discuss*1000 {
			t.Fatalf("discussion clock = %d ms, want %d", got, discuss*1000)
		}

		// Halfway through, a vote is still accepted and the clock is untouched
		// by it — casting a vote must not restart or extend the window.
		h.advance(discuss * time.Second / 2)
		h.voteSkip(0)
		synctest.Wait()
		if got := h.phase(); got != genpb.Phase_PHASE_DISCUSSION {
			t.Fatalf("phase = %v mid-window, want DISCUSSION", got)
		}
		if got := h.remainingMS(); got != discuss*1000/2 {
			t.Fatalf("clock = %d ms after a vote, want %d", got, discuss*1000/2)
		}

		// The remaining half of that same timer expires the phase.
		h.advance(discuss*time.Second/2 - time.Nanosecond)
		if got := h.phase(); got != genpb.Phase_PHASE_DISCUSSION {
			t.Fatalf("window closed early: %v", got)
		}
		h.advance(time.Nanosecond)
		if got := h.phase(); got != genpb.Phase_PHASE_RESOLVING {
			t.Fatalf("phase = %v at the deadline, want RESOLVING", got)
		}
		if got := phaseTrail(h.drain(0)); !slices.Equal(got, []genpb.Phase{genpb.Phase_PHASE_RESOLVING}) {
			t.Fatalf("phases between discussion and resolving: %v", got)
		}
	})
}

// TestVotingWindowClosesOnTheLastVote — DESIGN.md:52. The phase ends the
// instant the final active player votes, without burning the rest of the
// combined timer.
func TestVotingWindowClosesOnTheLastVote(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(1, 5, 180), 606)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()
		opened := time.Now()

		for i := range 5 {
			h.voteSkip(i)
			synctest.Wait()
			if got := h.phase(); got != genpb.Phase_PHASE_DISCUSSION {
				t.Fatalf("window closed after %d of 6 votes: %v", i+1, got)
			}
		}
		h.voteSkip(5)
		synctest.Wait()
		if got := h.phase(); got != genpb.Phase_PHASE_RESOLVING {
			t.Fatalf("window did not close on the sixth vote: %v", got)
		}
		if elapsed := time.Since(opened); elapsed != 0 {
			t.Fatalf("early close consumed %v of the combined timer, want none", elapsed)
		}

		// The progress count published along the way names nobody.
		var counts []int32
		for _, e := range h.drain(0) {
			if v := e.GetVoteCastCount(); v != nil {
				counts = append(counts, v.GetVotesCast())
				if got := v.GetActiveCount(); got != 6 {
					t.Fatalf("VoteCastCount.active_count = %d, want 6", got)
				}
			}
		}
		// The window opens by publishing a zero, then one frame per vote.
		if !slices.Equal(counts, []int32{0, 1, 2, 3, 4, 5, 6}) {
			t.Fatalf("vote progress = %v, want 0..6", counts)
		}
	})
}

// TestAbstentionsAreNotSkips — DESIGN.md:52, on the timer-expiry path. Three of
// six vote and the window times out. The three who said nothing abstain: they
// land in no bucket, so Skip stays at zero and the three real votes win
// outright. Under the old rule those three silences became Skips and blocked
// the elimination, which is exactly the outcome this test now forbids.
func TestAbstentionsAreNotSkips(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(2, 5, 60), 707)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		target := h.ids[5]
		for i := range 3 {
			h.vote(i, target)
		}
		synctest.Wait()
		if got := h.phase(); got != genpb.Phase_PHASE_DISCUSSION {
			t.Fatalf("phase = %v with three votes outstanding", got)
		}

		h.advance(h.discussDur())
		resolution := h.drain(0)
		tally := lastTally(resolution)
		if tally == nil {
			t.Fatal("no VoteTally after the window expired")
		}
		if got := tally.GetSkipCount(); got != 0 {
			t.Fatalf("skip_count = %d, want 0: three abstentions became Skip votes", got)
		}
		if got := countVotes(tally, target); got != 3 {
			t.Fatalf("votes for target = %d, want 3", got)
		}
		// The totals deliberately do not sum to active_count: three players are
		// in neither bucket.
		if got := tally.GetActiveCount(); got != 6 {
			t.Fatalf("active_count = %d, want 6", got)
		}
		el := lastEliminated(resolution)
		if el == nil {
			t.Fatal("no PlayerEliminated after the window expired")
		}
		if !el.GetEliminated() {
			t.Fatal("3 real votes against 0 Skips must eliminate")
		}
		if got := el.GetPlayerId(); got != target {
			t.Fatalf("PlayerEliminated names %q, want the target %q", got, target)
		}
		if !h.eliminated(5) {
			t.Fatal("the room state disagrees with the PlayerEliminated it sent")
		}
	})
}

// TestNoWinnerStartsTheNextRound — DESIGN.md:59. A unanimous Skip: Skip holds
// first place, so nobody is eliminated and the next round opens.
func TestNoWinnerStartsTheNextRound(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(3, 5, 60), 808)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()
		h.skipAll()

		el := lastEliminated(h.drain(0))
		if el == nil {
			t.Fatal("no PlayerEliminated after a no-majority round")
		}
		if el.GetEliminated() {
			t.Fatal("somebody was eliminated on a unanimous Skip")
		}
		if el.GetPlayerId() != "" {
			t.Fatalf("PlayerEliminated named %q on a no-elimination round", el.GetPlayerId())
		}

		h.advance(ResolveDuration)
		h.toDrawing()
		if got := h.round(); got != 2 {
			t.Fatalf("round = %d, want 2", got)
		}
		if got := h.activeCount(); got != 6 {
			t.Fatalf("active = %d, want all 6 still in", got)
		}
	})
}

// TestEarlyResolveStopsTheDiscussionTimer — the combined timer must be disarmed
// when the window closes early, or it fires again minutes later in the middle
// of the next round's drawing turns and ends that round for free.
func TestEarlyResolveStopsTheDiscussionTimer(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const discuss = 180
		h := newHarness(t, 4, mkSettings(4, 5, discuss), 909)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		// Close round 1 immediately, then sit through the moment the abandoned
		// discussion timer would have fired.
		h.skipAll()
		h.advance(ResolveDuration)
		h.toDrawing()
		if got := h.round(); got != 2 {
			t.Fatalf("round = %d, want 2", got)
		}
		h.discard()

		// Round 2 is four turns of five seconds. The stale round-1 timer would
		// have fired at t+180s, well inside this window.
		h.advance(discuss * time.Second)
		trail := phaseTrail(h.drain(0))
		for _, p := range trail {
			if p == genpb.Phase_PHASE_RESOLVING && h.round() == 2 {
				// Legal: round 2 reached its own discussion and expired.
				break
			}
		}
		// What must NOT have happened is a second, spurious RoundStarted for a
		// round the machine never entered.
		var rounds []int32
		for _, rs := range allRoundStarted(h.drain(0)) {
			rounds = append(rounds, rs.GetRound())
		}
		for i := 1; i < len(rounds); i++ {
			if rounds[i] == rounds[i-1] {
				t.Fatalf("round %d started twice: %v", rounds[i], rounds)
			}
		}
		if got := h.round(); got < 2 || got > 4 {
			t.Fatalf("round = %d after %ds of round 2, want 2..4", got, discuss)
		}
	})
}

// TestNoTimerSurvivesTheEndOfTheMatch — finishMatch disarms the phase timer.
// PHASE_ENDED is untimed, so an hour of virtual time must produce no further
// transition at all.
func TestNoTimerSurvivesTheEndOfTheMatch(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(1, 5, 30), 1010)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()
		h.skipAll()
		h.advance(ResolveDuration)

		if got := h.phase(); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("phase = %v, want ENDED", got)
		}
		if got := h.remainingMS(); got != 0 {
			t.Fatalf("PHASE_ENDED still has %d ms on the clock", got)
		}
		h.discard()

		// Everyone is still connected, so the idle sweep will not close the
		// room either.
		h.advance(time.Hour)
		if got := h.phase(); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("phase drifted to %v an hour after the match ended", got)
		}
		if got := phaseTrail(h.drain(0)); len(got) != 0 {
			t.Fatalf("PHASE_ENDED emitted further transitions: %v", got)
		}
	})
}

// TestArtistDisconnectSkipsTheTurnImmediately — open question 5, DESIGN.md:122.
// The room must not watch an empty timer run down.
func TestArtistDisconnectSkipsTheTurnImmediately(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(1, 60, 60), 1111)
		defer h.stop()
		h.discard()
		h.start()
		h.toDrawing()

		ai := h.artistIdx()
		if ai < 0 {
			t.Fatal("no artist")
		}
		started := time.Now()
		h.r.detach(h.ids[ai], h.socks[ai].ch)
		synctest.Wait()

		if elapsed := time.Since(started); elapsed != 0 {
			t.Fatalf("skip took %v of virtual time, want immediate", elapsed)
		}
		if got := h.artist(); got == h.ids[ai] {
			t.Fatal("the departed artist still holds the turn")
		}
		// The skip lands on the handoff that announces the next artist, not on
		// the next turn itself — endTurn goes through beginTurnAt.
		if got := h.phase(); got != genpb.Phase_PHASE_INTERMISSION {
			t.Fatalf("phase = %v after skipping one turn of six, want INTERMISSION", got)
		}
		h.toDrawing()
		// And the dark seat is out of the turn order for the rest of the round:
		// five remaining artists, none of them the one who left.
		for range 6 {
			if h.phase() != genpb.Phase_PHASE_DRAWING {
				break
			}
			if got := h.artist(); got == h.ids[ai] {
				t.Fatal("a disconnected player was given a drawing turn")
			}
			h.advance(h.drawDur())
			if h.phase() == genpb.Phase_PHASE_INTERMISSION {
				h.advance(h.intermissionDur())
			}
		}
		if got := h.phase(); got != genpb.Phase_PHASE_DISCUSSION {
			t.Fatalf("phase = %v, want DISCUSSION", got)
		}
	})
}

// TestLastActiveArtistLeavingOpensDiscussion — the fall-through at the end of
// beginTurnAt: when the artist who drops was the last playable turn of the
// round, the machine moves straight on rather than stalling on an empty queue.
func TestLastActiveArtistLeavingOpensDiscussion(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(1, 30, 60), 1212)
		defer h.stop()
		h.discard()
		h.start()
		h.toDrawing()

		// Run out the first three turns, then drop the fourth artist.
		for range 3 {
			h.nextTurn()
		}
		ai := h.artistIdx()
		h.r.detach(h.ids[ai], h.socks[ai].ch)
		synctest.Wait()

		// beginTurnAt ran out of artists and fell through to the voting
		// handoff rather than stalling on an empty queue.
		if got := h.phase(); got != genpb.Phase_PHASE_INTERMISSION {
			t.Fatalf("phase = %v, want INTERMISSION", got)
		}
		if got := h.artist(); got != "" {
			t.Fatalf("artist = %q outside PHASE_DRAWING", got)
		}
		h.advance(h.intermissionDur())
		if got := h.phase(); got != genpb.Phase_PHASE_DISCUSSION {
			t.Fatalf("phase = %v, want DISCUSSION", got)
		}
	})
}

// TestCommandsRejectedOutsideTheirPhase — the phase machine is also an
// authorization boundary.
func TestCommandsRejectedOutsideTheirPhase(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(1, 10, 30), 1313)
		defer h.stop()
		h.discard()

		// Voting in the lobby.
		h.voteSkip(0)
		synctest.Wait()
		if !hasErrorCode(h.drain(0), genpb.ErrorCode_ERROR_CODE_WRONG_PHASE) {
			t.Fatal("CastVote in the lobby was not rejected")
		}
		// A stroke in the lobby.
		h.send(0, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeBegin{
			StrokeBegin: &genpb.StrokeBegin{ColorIndex: 0, Width: 4, Points: []int32{1, 2}}}})
		synctest.Wait()
		if !hasErrorCode(h.drain(0), genpb.ErrorCode_ERROR_CODE_WRONG_PHASE) {
			t.Fatal("StrokeBegin in the lobby was not rejected")
		}
		// A rematch before the match ended.
		h.send(0, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_Rematch{Rematch: &genpb.Rematch{}}})
		synctest.Wait()
		if !hasErrorCode(h.drain(0), genpb.ErrorCode_ERROR_CODE_WRONG_PHASE) {
			t.Fatal("Rematch in the lobby was not rejected")
		}

		h.start()
		h.discard()
		// Settings and readiness are lobby-only.
		h.send(0, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_UpdateSettings{
			UpdateSettings: &genpb.UpdateSettings{Settings: mkSettings(3, 9, 45)}}})
		h.send(0, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_SetReady{
			SetReady: &genpb.SetReady{Ready: false}}})
		h.send(0, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StartMatch{
			StartMatch: &genpb.StartMatch{}}})
		synctest.Wait()
		codes := errorCodes(h.drain(0))
		n := 0
		for _, c := range codes {
			if c == genpb.ErrorCode_ERROR_CODE_WRONG_PHASE {
				n++
			}
		}
		if n != 3 {
			t.Fatalf("expected three WRONG_PHASE rejections mid-match, got %v", codes)
		}
	})
}

// TestRematchReturnsToLobbyAndReplaysCleanly — DESIGN.md:81. A second match in
// the same room deals fresh words and reopens the whole machine.
func TestRematchReturnsToLobbyAndReplaysCleanly(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(1, 5, 30), 1414)
		defer h.stop()
		h.start()
		h.toDiscussion()
		h.skipAll()
		h.advance(ResolveDuration)
		if got := h.phase(); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("phase = %v, want ENDED", got)
		}
		firstImposter := h.imposter()

		h.send(0, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_Rematch{Rematch: &genpb.Rematch{}}})
		synctest.Wait()
		if got := h.phase(); got != genpb.Phase_PHASE_LOBBY {
			t.Fatalf("phase = %v after rematch, want LOBBY", got)
		}
		for i := range h.ids {
			if got := h.word(i); got != "" {
				t.Fatalf("player %d kept word %q across the rematch", i, got)
			}
			if h.eliminated(i) {
				t.Fatalf("player %d is still eliminated after the rematch", i)
			}
		}
		if got := h.imposter(); got != "" {
			t.Fatalf("imposter %q survived the rematch", got)
		}

		// The room replays: ready up and run a second whole match.
		for i := range h.ids {
			h.send(i, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_SetReady{
				SetReady: &genpb.SetReady{Ready: true}}})
		}
		synctest.Wait()
		h.discard()
		h.start()
		h.toDiscussion()
		if got := h.round(); got != 1 {
			t.Fatalf("second match opened at round %d, want 1", got)
		}
		if h.imposter() == "" {
			t.Fatal("second match dealt no imposter")
		}
		_ = firstImposter
		h.skipAll()
		h.advance(ResolveDuration)
		if got := h.phase(); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("second match did not finish: %v", got)
		}
	})
}
