package room

// rules_test.go — milestone 6. The game rules at 3, 6 and 10 players: role
// assignment, the plurality rule and its exact boundary, elimination, every win
// path, and the settings ranges.
//
// The rules that matter here are the ones DESIGN.md states as inequalities.
// Every one of them is tested one below the line and exactly on it, because
// "strictly ahead of Skip" and "level with Skip" differ only at that single
// value and a test that never sits on the boundary cannot tell them apart.

import (
	"bytes"
	"fmt"
	mrand "math/rand/v2"
	"slices"
	"testing"
	"testing/synctest"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// ---------------------------------------------------------------------------
// Plurality, with Skip on the ballot
// ---------------------------------------------------------------------------

// TestPluralityBoundary — DESIGN.md:58. Elimination is a plurality with Skip on
// the ballot: the leader must be strictly ahead of every other candidate AND
// strictly ahead of the Skip count. Every size is driven twice, once level with
// Skip and once one vote clear of it, because that single value is the whole
// difference between this rule and "most votes wins".
//
// The even sizes are the ones a `>=` bug gets wrong: 3 for a candidate against
// 3 Skips at 6 players is a dead heat, not a win.
func TestPluralityBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		players   int
		votes     int // votes for the target; the rest of the room Skips
		eliminate bool
	}{
		{players: 3, votes: 1, eliminate: false},  // 1 vs 2 Skip
		{players: 3, votes: 2, eliminate: true},   // 2 vs 1 Skip
		{players: 6, votes: 3, eliminate: false},  // dead heat, 3 vs 3 Skip
		{players: 6, votes: 4, eliminate: true},   // 4 vs 2 Skip
		{players: 7, votes: 3, eliminate: false},  // 3 vs 4 Skip
		{players: 7, votes: 4, eliminate: true},   // 4 vs 3 Skip
		{players: 10, votes: 5, eliminate: false}, // dead heat, 5 vs 5 Skip
		{players: 10, votes: 6, eliminate: true},  // 6 vs 4 Skip
	}
	for _, tc := range cases {
		name := fmt.Sprintf("%d_players_%d_votes", tc.players, tc.votes)
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, tc.players, mkSettings(4, 5, 60), uint64(tc.players*100+tc.votes))
				defer h.stop()
				h.discard()
				h.start()
				h.toDiscussion()

				target := h.ids[tc.players-1]
				for i := range tc.players {
					if i < tc.votes {
						h.vote(i, target)
					} else {
						h.voteSkip(i)
					}
				}
				synctest.Wait()

				if got := h.phase(); got != genpb.Phase_PHASE_RESOLVING {
					t.Fatalf("phase = %v after every player voted, want RESOLVING", got)
				}
				resolution := h.drain(0)
				tally := lastTally(resolution)
				if tally == nil {
					t.Fatal("no VoteTally")
				}
				// A plurality has no threshold, and the deprecated field must
				// not start carrying one again.
				if got := tally.GetMajorityThreshold(); got != 0 {
					t.Fatalf("majority_threshold = %d, want 0 under a plurality", got)
				}
				if got := tally.GetActiveCount(); got != int32(tc.players) {
					t.Fatalf("tally active_count = %d, want %d", got, tc.players)
				}
				if got := countVotes(tally, target); got != int32(tc.votes) {
					t.Fatalf("votes for target = %d, want %d", got, tc.votes)
				}
				if got, want := tally.GetSkipCount(), int32(tc.players-tc.votes); got != want {
					t.Fatalf("skip_count = %d, want %d", got, want)
				}
				if got := h.eliminated(tc.players - 1); got != tc.eliminate {
					t.Fatalf("%d votes against %d Skip: eliminated = %v, want %v",
						tc.votes, tc.players-tc.votes, got, tc.eliminate)
				}
				el := lastEliminated(resolution)
				if el == nil {
					t.Fatal("no PlayerEliminated")
				}
				if el.GetEliminated() != tc.eliminate {
					t.Fatalf("PlayerEliminated.eliminated = %v, want %v",
						el.GetEliminated(), tc.eliminate)
				}
			})
		})
	}
}

// TestPluralityIgnoresAbstentions — DESIGN.md:52. A player who never answers
// abstains. The old rule promoted that silence to Skip, which is exactly what
// would flip this case: one vote against two abstentions eliminates, because
// Skip's total is zero.
func TestPluralityIgnoresAbstentions(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(1, 5, 30), 2101)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		target := h.ids[2]
		h.vote(0, target)
		// Seats 1 and 2 say nothing at all. The window has to time out.
		h.advance(h.discussDur())

		tally := lastTally(h.drain(0))
		if tally == nil {
			t.Fatal("no VoteTally")
		}
		if got := tally.GetSkipCount(); got != 0 {
			t.Fatalf("skip_count = %d, want 0: two abstentions became Skip votes", got)
		}
		if got := countVotes(tally, target); got != 1 {
			t.Fatalf("votes for target = %d, want 1", got)
		}
		if got := tally.GetActiveCount(); got != 3 {
			t.Fatalf("active_count = %d, want 3", got)
		}
		if !h.eliminated(2) {
			t.Fatal("one real vote against nothing at all should eliminate")
		}
	})
}

// TestAnExplicitSkipMajorityEliminatesNobody — the other half of the same rule.
// Swap the two abstentions above for deliberate Skips and the outcome inverts,
// which is what makes Skip a choice rather than a default.
func TestAnExplicitSkipMajorityEliminatesNobody(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(1, 5, 30), 2102)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		h.vote(0, h.ids[2])
		h.voteSkip(1)
		h.voteSkip(2)
		synctest.Wait()

		tally := lastTally(h.drain(0))
		if tally == nil {
			t.Fatal("no VoteTally")
		}
		if got := tally.GetSkipCount(); got != 2 {
			t.Fatalf("skip_count = %d, want 2", got)
		}
		if h.eliminated(2) {
			t.Fatal("1 vote against 2 explicit Skips must eliminate nobody")
		}
	})
}

// TestATieBetweenTwoCandidatesEliminatesNobody — a tie for first place is a
// tie whoever holds it, so the rule needs a case where Skip is not involved.
func TestATieBetweenTwoCandidatesEliminatesNobody(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(1, 5, 30), 2103)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		// Two for seat 2, two for seat 3, nobody skips.
		h.vote(0, h.ids[2])
		h.vote(1, h.ids[2])
		h.vote(2, h.ids[3])
		h.vote(3, h.ids[3])
		synctest.Wait()

		tally := lastTally(h.drain(0))
		if tally == nil {
			t.Fatal("no VoteTally")
		}
		if got := tally.GetSkipCount(); got != 0 {
			t.Fatalf("skip_count = %d, want 0", got)
		}
		if h.eliminated(2) || h.eliminated(3) {
			t.Fatal("a 2-2 tie for first place must eliminate nobody")
		}
	})
}

// TestSelfVoteIsLegalAndCounted — DESIGN.md:51 lists the voter themself as a
// valid choice, and it is a real choice: it counts toward the majority that
// eliminates them.
func TestSelfVoteIsLegalAndCounted(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(4, 5, 60), 2001)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		self := h.ids[0]
		h.vote(0, self)
		synctest.Wait()

		var accepted *genpb.VoteAccepted
		for _, e := range h.drain(0) {
			if v := e.GetVoteAccepted(); v != nil {
				accepted = v
			}
		}
		if accepted == nil {
			t.Fatal("a self-vote was not accepted")
		}
		if accepted.GetCandidateId() != self {
			t.Fatalf("VoteAccepted candidate = %q, want the voter %q", accepted.GetCandidateId(), self)
		}
		if accepted.GetSkip() {
			t.Fatal("a self-vote was recorded as Skip")
		}

		h.vote(1, self)
		h.voteSkip(2)
		synctest.Wait()

		tally := lastTally(h.drain(0))
		if got := countVotes(tally, self); got != 2 {
			t.Fatalf("votes for the self-voter = %d, want 2", got)
		}
		if !h.eliminated(0) {
			t.Fatal("2 of 3 including a self-vote is a strict majority but nobody was eliminated")
		}
	})
}

// TestSecondVoteRejected — DESIGN.md:49. One vote, irreversible. The rejection
// must not disturb the vote already on record.
func TestSecondVoteRejected(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(4, 5, 60), 2002)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		h.voteSkip(0)
		synctest.Wait()
		h.drain(0)

		h.vote(0, h.ids[1])
		synctest.Wait()
		evs := h.drain(0)
		if !hasErrorCode(evs, genpb.ErrorCode_ERROR_CODE_ALREADY_VOTED) {
			t.Fatalf("second vote was not rejected: %v", errorCodes(evs))
		}
		for _, e := range evs {
			if e.GetVoteAccepted() != nil {
				t.Fatal("the room confirmed a second vote")
			}
		}

		// Everyone else skips; the tally must show six Skips and no vote for
		// the candidate the rejected frame named.
		for i := 1; i < 6; i++ {
			h.voteSkip(i)
		}
		synctest.Wait()
		tally := lastTally(h.drain(0))
		if tally == nil {
			t.Fatal("no VoteTally")
		}
		if got := tally.GetSkipCount(); got != 6 {
			t.Fatalf("skip_count = %d, want 6: the first vote must stand", got)
		}
		if got := countVotes(tally, h.ids[1]); got != 0 {
			t.Fatalf("the overwritten candidate collected %d votes", got)
		}
	})
}

// TestVoteForAnInactiveCandidateRejected — DESIGN.md:51 restricts choices to
// active players, the voter themself, or Skip.
func TestVoteForAnInactiveCandidateRejected(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(4, 5, 60), 2003)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		// Unknown player id.
		h.vote(0, "0000000000000000")
		synctest.Wait()
		if !hasErrorCode(h.drain(0), genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND) {
			t.Fatal("a vote for an unseated id was not rejected")
		}

		// Eliminate a non-imposter, so the match carries on into a round where
		// they are a spectator.
		victim := h.anyIdxExcept(h.imposterIdx())
		voted := 0
		for i := range h.ids {
			if i != victim && voted < 4 {
				h.vote(i, h.ids[victim])
				voted++
				continue
			}
			h.voteSkip(i)
		}
		synctest.Wait()
		if !h.eliminated(victim) {
			t.Fatal("4 of 6 did not eliminate")
		}
		if got := h.phase(); got == genpb.Phase_PHASE_ENDED {
			t.Fatalf("the match ended on a non-imposter elimination: %v", got)
		}
		h.nextRound()
		h.discard()

		voter := h.anyIdxExcept(victim)
		h.vote(voter, h.ids[victim])
		synctest.Wait()
		if !hasErrorCode(h.drain(voter), genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND) {
			t.Fatal("a vote for an eliminated player was not rejected")
		}

		// And the eliminated player is a silent spectator: their own vote is
		// refused too (DESIGN.md:66).
		h.voteSkip(victim)
		synctest.Wait()
		if !hasErrorCode(h.drain(victim), genpb.ErrorCode_ERROR_CODE_NOT_ACTIVE) {
			t.Fatal("an eliminated player was allowed to vote")
		}
	})
}

// ---------------------------------------------------------------------------
// What the tally is allowed to say
// ---------------------------------------------------------------------------

// TestTallyPublishesAggregatesAndNothingElse — DESIGN.md:56. The result screen
// gets per-candidate totals plus Skip. Who voted for whom is never disclosed,
// and neither is who voted early.
//
// Two independent assertions: the payload's declared shape, so a new field
// cannot be added without this test noticing, and the raw bytes of the frame
// that actually reached every socket.
func TestTallyPublishesAggregatesAndNothingElse(t *testing.T) {
	t.Parallel()

	wantTally := []string{"active_count", "counts", "majority_threshold", "round", "skip_count"}
	if got := fieldNames(&genpb.VoteTally{}); !sameStrings(got, wantTally) {
		t.Fatalf("VoteTally fields = %v, want exactly %v", got, wantTally)
	}
	wantCount := []string{"candidate_id", "votes"}
	if got := fieldNames(&genpb.VoteCount{}); !sameStrings(got, wantCount) {
		t.Fatalf("VoteCount fields = %v, want exactly %v", got, wantCount)
	}
	wantProgress := []string{"active_count", "round", "votes_cast"}
	if got := fieldNames(&genpb.VoteCastCount{}); !sameStrings(got, wantProgress) {
		t.Fatalf("VoteCastCount fields = %v, want exactly %v", got, wantProgress)
	}

	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(4, 5, 60), 2004)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		// Four distinguishable voters back one candidate; two skip. If any
		// voter identity leaked into the tally, these ids would be in the
		// bytes.
		candidate := h.ids[0]
		for i := range 4 {
			h.vote(i, candidate)
		}
		h.voteSkip(4)
		h.voteSkip(5)
		synctest.Wait()

		for sock := range h.socks {
			var raw []byte
			for _, e := range h.drain(sock) {
				if e.GetVoteTally() == nil {
					continue
				}
				b, err := proto.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				raw = b
			}
			if raw == nil {
				t.Fatalf("socket %d never received the tally", sock)
			}
			if !bytes.Contains(raw, []byte(candidate)) {
				t.Fatalf("socket %d: tally does not name the candidate", sock)
			}
			for i := 1; i < len(h.ids); i++ {
				if bytes.Contains(raw, []byte(h.ids[i])) {
					t.Fatalf("socket %d: the tally frame contains player %d's id — "+
						"voter identity must never be derivable from it", sock, i)
				}
			}
		}
	})
}

// TestVotedFlagIsPerSeatButNeverTheChoice — a roster entry may say THAT a seat
// locked in a vote this round, never WHAT they chose (DESIGN.md:65). The
// declared shape guards against a new field sneaking the choice onto
// PlayerInfo; the raw bytes guard the frame that actually reaches a socket.
func TestVotedFlagIsPerSeatButNeverTheChoice(t *testing.T) {
	t.Parallel()

	wantFields := []string{
		"avatar", "connected", "eliminated", "id", "is_host", "name", "ready", "seat", "voted",
	}
	if got := fieldNames(&genpb.PlayerInfo{}); !sameStrings(got, wantFields) {
		t.Fatalf("PlayerInfo fields = %v, want exactly %v", got, wantFields)
	}

	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(1, 5, 60), 3001)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		voter, candidate := 0, h.ids[1]
		h.vote(voter, candidate)
		synctest.Wait()

		for sock := range h.socks {
			var votedTrue, sawOtherVoted bool
			var raw []byte
			for _, e := range h.drain(sock) {
				pp := e.GetPlayerPresence()
				if pp == nil {
					continue
				}
				isVoter := pp.GetPlayer().GetId() == h.ids[voter]
				if isVoter && pp.GetPlayer().GetVoted() {
					votedTrue = true
					b, err := proto.Marshal(e)
					if err != nil {
						t.Fatal(err)
					}
					raw = b
				}
				if !isVoter && pp.GetPlayer().GetVoted() {
					sawOtherVoted = true
				}
			}
			if !votedTrue {
				t.Fatalf("socket %d: never told the voter locked in", sock)
			}
			if sawOtherVoted {
				t.Fatalf("socket %d: a non-voter was reported as voted", sock)
			}
			if bytes.Contains(raw, []byte(candidate)) {
				t.Fatalf("socket %d: the presence frame names the candidate — the choice leaked", sock)
			}
		}

		// Everybody else skips, which closes the window early. The resolve must
		// reset the flag before the next round can begin (invariant: it can only
		// climb from false to true mid-round, never the reverse).
		for i := 1; i < len(h.ids); i++ {
			h.voteSkip(i)
		}
		synctest.Wait()

		for sock := range h.socks {
			var latest *bool
			for _, e := range h.drain(sock) {
				if pp := e.GetPlayerPresence(); pp != nil && pp.GetPlayer().GetId() == h.ids[voter] {
					v := pp.GetPlayer().GetVoted()
					latest = &v
				}
			}
			if latest == nil {
				t.Fatalf("socket %d: never told the voter's ballot cleared", sock)
			}
			if *latest {
				t.Fatalf("socket %d: voter still shows voted=true after the round resolved", sock)
			}
		}
	})
}

// TestVoterConfirmationIsUnicastOnly — the one message that does pair a voter
// with their candidate must be impossible to broadcast, and must in fact reach
// only its subject.
func TestVoterConfirmationIsUnicastOnly(t *testing.T) {
	t.Parallel()

	if IsBroadcastable(EvVoteAccepted{&genpb.VoteAccepted{}}) {
		t.Fatal("EvVoteAccepted is broadcastable: it names a voter and their candidate")
	}
	if !IsBroadcastable(EvVoteTally{&genpb.VoteTally{}}) {
		t.Fatal("EvVoteTally must be broadcastable")
	}
	if !IsBroadcastable(EvVoteCastCount{&genpb.VoteCastCount{}}) {
		t.Fatal("EvVoteCastCount must be broadcastable")
	}

	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(4, 5, 60), 2005)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		h.vote(2, h.ids[3])
		synctest.Wait()

		for i, evs := range h.drainAll() {
			n := 0
			for _, e := range evs {
				if e.GetVoteAccepted() != nil {
					n++
				}
			}
			want := 0
			if i == 2 {
				want = 1
			}
			if n != want {
				t.Fatalf("socket %d received %d VoteAccepted frames, want %d", i, n, want)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// End conditions
// ---------------------------------------------------------------------------

// TestImposterEliminatedGroupWins — DESIGN.md:63.
func TestImposterEliminatedGroupWins(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(4, 5, 60), 3001)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		oi := h.imposterIdx()
		if oi < 0 {
			t.Fatal("no imposter was dealt")
		}
		imposter := h.ids[oi]

		voters := 0
		for i := range h.ids {
			if i == oi || voters >= 4 {
				h.voteSkip(i)
				continue
			}
			h.vote(i, imposter)
			voters++
		}
		synctest.Wait()

		el := lastEliminated(h.drain(0))
		if el == nil || !el.GetEliminated() || el.GetPlayerId() != imposter {
			t.Fatalf("PlayerEliminated = %v, want the imposter", el)
		}
		if !el.GetWasImposter() {
			t.Fatal("was_imposter must be true on the resolution that wins it for the group")
		}
		// Decided at the moment of the tally, not at the end of the result
		// screen: "the group wins immediately" (DESIGN.md:63).
		if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_GROUP ||
			reason != genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_ELIMINATED {
			t.Fatalf("outcome = %v / %v, want GROUP / IMPOSTER_ELIMINATED", w, reason)
		}

		h.advance(ResolveDuration)
		if got := h.phase(); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("phase = %v, want ENDED", got)
		}

		me := lastMatchEnded(h.drain(0))
		if me == nil {
			t.Fatal("no MatchEnded")
		}
		if me.GetWinner() != genpb.WinnerSide_WINNER_SIDE_GROUP {
			t.Fatalf("winner = %v", me.GetWinner())
		}
		if got := me.GetImposterPlayerIds(); len(got) != 1 || got[0] != imposter {
			t.Fatalf("MatchEnded named %v as the imposters, want [%q]", got, imposter)
		}
		if me.GetRoundsPlayed() != 1 {
			t.Fatalf("rounds_played = %d, want 1", me.GetRoundsPlayed())
		}
		// The final reveal: every player, their word, exactly one imposter
		// (DESIGN.md:75).
		if n := len(me.GetReveals()); n != 6 {
			t.Fatalf("reveals = %d rows, want 6", n)
		}
		imposters := 0
		for _, rev := range me.GetReveals() {
			idx := h.indexOf(rev.GetPlayerId())
			if idx < 0 {
				t.Fatalf("reveal names unknown player %q", rev.GetPlayerId())
			}
			if got := h.word(idx); rev.GetWord() != got {
				t.Fatalf("reveal for player %d says %q, they hold %q", idx, rev.GetWord(), got)
			}
			if rev.GetWasImposter() {
				imposters++
				if rev.GetWord() != me.GetImposterWord() {
					t.Fatalf("the imposter's reveal word %q is not the imposter word %q",
						rev.GetWord(), me.GetImposterWord())
				}
			} else if rev.GetWord() != me.GetCommonWord() {
				t.Fatalf("a non-imposter holds %q, common word is %q",
					rev.GetWord(), me.GetCommonWord())
			}
		}
		if imposters != 1 {
			t.Fatalf("%d reveals flagged as the imposter, want exactly 1", imposters)
		}
		if me.GetCommonWord() == me.GetImposterWord() {
			t.Fatal("common and imposter words are identical")
		}
	})
}

// TestImposterSurvivesTheFinalRound — DESIGN.md:71. Nobody is ever eliminated,
// the configured final round resolves, and the imposter takes it.
func TestImposterSurvivesTheFinalRound(t *testing.T) {
	t.Parallel()
	for _, players := range []int{3, 6, 10} {
		t.Run(fmt.Sprintf("%d_players", players), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, players, mkSettings(2, 5, 30), uint64(3100+players))
				defer h.stop()
				h.discard()
				h.start()

				h.toDiscussion()
				h.skipAll()
				h.advance(ResolveDuration)
				h.toDrawing()
				h.toDiscussion()
				if got := h.round(); got != 2 {
					t.Fatalf("round = %d, want the configured final round 2", got)
				}
				h.skipAll()

				if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_IMPOSTER ||
					reason != genpb.MatchEndReason_MATCH_END_REASON_FINAL_ROUND_SURVIVED {
					t.Fatalf("outcome = %v / %v, want IMPOSTER / FINAL_ROUND_SURVIVED", w, reason)
				}
				if h.eliminated(h.imposterIdx()) {
					t.Fatal("the imposter was eliminated yet won by surviving")
				}
				h.advance(ResolveDuration)
				me := lastMatchEnded(h.drain(0))
				if me == nil || me.GetWinner() != genpb.WinnerSide_WINNER_SIDE_IMPOSTER {
					t.Fatalf("MatchEnded = %v", me)
				}
				if got := me.GetRoundsPlayed(); got != 2 {
					t.Fatalf("rounds_played = %d, want 2", got)
				}
			})
		})
	}
}

// TestTwoActivePlayersRemainImposterWins — DESIGN.md:72. The headcount rule
// fires on its own, several rounds before the configured last one.
func TestTwoActivePlayersRemainImposterWins(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(4, 5, 60), 3002)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		oi := h.imposterIdx()
		// Round 1: three of four vote out a non-imposter, one Skips. 3 beats 1.
		first := h.anyIdxExcept(oi)
		voted := 0
		for i := range h.ids {
			if i != first && voted < 3 {
				h.vote(i, h.ids[first])
				voted++
				continue
			}
			h.voteSkip(i)
		}
		synctest.Wait()
		if !h.eliminated(first) {
			t.Fatal("3 of 4 did not eliminate")
		}
		if got := h.activeCount(); got != 3 {
			t.Fatalf("active = %d after one elimination, want 3", got)
		}
		if w, _ := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_UNSPECIFIED {
			t.Fatalf("match decided at 3 active players: %v", w)
		}

		// Round 2: three active. Two vote, one Skips, so 2 beats 1.
		h.nextRound()
		second := h.anyIdxExcept(oi, first)
		voted = 0
		for i := range h.ids {
			if !h.active(i) {
				continue
			}
			if i != second && voted < 2 {
				h.vote(i, h.ids[second])
				voted++
				continue
			}
			h.voteSkip(i)
		}
		synctest.Wait()

		if !h.eliminated(second) {
			t.Fatal("2 of 3 did not eliminate")
		}
		if got := h.activeCount(); got != 2 {
			t.Fatalf("active = %d, want 2", got)
		}
		if got := h.round(); got >= 4 {
			t.Fatalf("round = %d: the match reached its configured end, so this "+
				"is not the two-players-remain rule", got)
		}
		if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_IMPOSTER ||
			reason != genpb.MatchEndReason_MATCH_END_REASON_TWO_PLAYERS_REMAIN {
			t.Fatalf("outcome = %v / %v, want IMPOSTER / TWO_PLAYERS_REMAIN", w, reason)
		}
	})
}

// TestImposterDisconnectEndsTheMatchForTheGroup — DESIGN.md:125.
//
// Note the timing. DESIGN.md:125 says "immediately"; the server holds the seat
// for the 60 s grace window first, and awards the group win when the window
// expires rather than when the socket drops. That is deliberate — a one-second
// network blip would otherwise hand the match away — but it does mean the word
// "immediately" describes the end condition, not the latency.
func TestImposterDisconnectEndsTheMatchForTheGroup(t *testing.T) {
	t.Parallel()
	for _, players := range []int{3, 6, 10} {
		t.Run(fmt.Sprintf("%d_players", players), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, players, mkSettings(2, 30, 120), uint64(3200+players))
				defer h.stop()
				h.discard()
				h.start()
				h.advance(AssignDuration)

				oi := h.imposterIdx()
				h.r.detach(h.ids[oi], h.socks[oi])
				synctest.Wait()

				// Inside the window the seat is still theirs to reclaim, so
				// nothing is decided yet.
				h.advance(GraceWindow - SweepInterval)
				if got := h.phase(); got == genpb.Phase_PHASE_ENDED {
					t.Fatal("the match ended before the imposter's grace window expired")
				}

				h.advance(2 * SweepInterval)
				if got := h.phase(); got != genpb.Phase_PHASE_ENDED {
					t.Fatalf("phase = %v after the imposter's window expired, want ENDED", got)
				}
				if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_GROUP ||
					reason != genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_DISCONNECTED {
					t.Fatalf("outcome = %v / %v, want GROUP / IMPOSTER_DISCONNECTED", w, reason)
				}
				// Everyone still connected learns the outcome, and it is the
				// full reveal (DESIGN.md:75).
				for i := range h.socks {
					if i == oi {
						continue
					}
					me := lastMatchEnded(h.drain(i))
					if me == nil {
						t.Fatalf("socket %d never received MatchEnded", i)
					}
					if got := len(me.GetReveals()); got != players {
						t.Fatalf("socket %d: %d reveals, want %d", i, got, players)
					}
				}
			})
		})
	}
}

// TestNonImposterEliminationTellsTheGroupNothingMore — DESIGN.md:65. The room
// says only that a non-imposter went; the imposter's identity goes to exactly one
// socket, the new spectator's.
func TestNonImposterEliminationTellsTheGroupNothingMore(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(4, 5, 60), 3003)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		oi := h.imposterIdx()
		victim := h.anyIdxExcept(oi)
		voted := 0
		for i := range h.ids {
			if i != victim && voted < 4 {
				h.vote(i, h.ids[victim])
				voted++
				continue
			}
			h.voteSkip(i)
		}
		synctest.Wait()

		if !h.eliminated(victim) {
			t.Fatal("4 of 6 did not eliminate")
		}
		if h.active(victim) {
			t.Fatal("an eliminated player is still active")
		}
		if w, _ := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_UNSPECIFIED {
			t.Fatalf("the match ended on a non-imposter elimination: %v", w)
		}

		frames := h.drainAll()
		for i, evs := range frames {
			el := lastEliminated(evs)
			if el == nil {
				t.Fatalf("socket %d never heard about the elimination", i)
			}
			if !el.GetEliminated() || el.GetPlayerId() != h.ids[victim] {
				t.Fatalf("socket %d: PlayerEliminated = %v", i, el)
			}
			if el.GetWasImposter() {
				t.Fatalf("socket %d was told the eliminated player was the imposter", i)
			}

			spec := allSpectatorInfo(evs)
			if i == victim {
				if len(spec) != 1 {
					t.Fatalf("the new spectator received %d SpectatorInfo frames, want 1", len(spec))
				}
				named := spec[0].GetImposters()
				if len(named) != 1 || named[0].GetPlayerId() != h.ids[oi] {
					t.Fatalf("SpectatorInfo names %d imposters, want just %q", len(named), h.ids[oi])
				}
				if named[0].GetName() == "" {
					t.Fatal("SpectatorInfo carries no imposter name")
				}
				// The dossier is the whole match so far, not just a name
				// (MULTIPLE_IMPOSTERS.md, "Eliminated-player Spectator View").
				rounds := spec[0].GetRounds()
				if len(rounds) != 1 {
					t.Fatalf("dossier carries %d rounds, want 1", len(rounds))
				}
				if n := len(rounds[0].GetAssignments()); n != len(h.ids) {
					t.Fatalf("dossier assigns %d seats, want %d", n, len(h.ids))
				}
				continue
			}
			if len(spec) != 0 {
				t.Fatalf("socket %d received SpectatorInfo; only the eliminated "+
					"player may learn the imposter's identity", i)
			}
		}

		// The spectator stays in the room and keeps receiving broadcasts, they
		// simply cannot act (DESIGN.md:66).
		h.nextRound()
		if got := len(allRoundStarted(h.drain(victim))); got == 0 {
			t.Fatal("the spectator stopped receiving room broadcasts")
		}
		if got := h.activeCount(); got != 5 {
			t.Fatalf("active = %d after one elimination, want 5", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Role assignment
// ---------------------------------------------------------------------------

// assignOnly builds a room with n seats and deals it, without running the
// actor. assignWords touches nothing but r.players, r.deck and r.rnd, so this
// is the whole of role assignment isolated from the phase machine.
func assignOnly(t *testing.T, n int, seed uint64) *Room {
	t.Helper()
	r := New("ROLE", "host", genpb.Avatar_AVATAR_BEETLE, Options{
		Deck:   pairDeck{"CAT", "DOG"},
		Rand:   mrand.New(mrand.NewPCG(seed, seed^0xa5a5a5a5)),
		Logger: discardLogger(),
	})
	for i := 1; i < n; i++ {
		p := &Player{
			ID:        fmt.Sprintf("seat-%02d", i),
			Name:      fmt.Sprintf("p%d", i),
			SeatToken: fmt.Sprintf("tok-%02d", i),
			Seat:      int32(i),
			Connected: true,
		}
		r.players = append(r.players, p)
		r.byID[p.ID] = p
		r.bySeatToken[p.SeatToken] = p
	}
	r.assignWords()
	return r
}

// TestRoleAssignment — DESIGN.md:22. Exactly one imposter, everyone else on the
// common word, and the common/imposter orientation of the pair drawn at random
// so the deck itself gives nothing away (DESIGN.md:172).
func TestRoleAssignment(t *testing.T) {
	t.Parallel()
	for _, n := range []int{3, 6, 10} {
		t.Run(fmt.Sprintf("%d_players", n), func(t *testing.T) {
			t.Parallel()
			orientation := map[string]int{}
			imposterSeats := map[string]int{}

			for seed := range uint64(200) {
				r := assignOnly(t, n, seed)

				odd, common := 0, 0
				for _, p := range r.players {
					switch p.word {
					case r.imposterWord:
						odd++
					case r.commonWord:
						common++
					default:
						t.Fatalf("seed %d: player holds %q, neither side of the pair", seed, p.word)
					}
				}
				if odd != 1 {
					t.Fatalf("seed %d: %d players hold the imposter word, want exactly 1", seed, odd)
				}
				if common != n-1 {
					t.Fatalf("seed %d: %d players hold the common word, want %d", seed, common, n-1)
				}
				if len(r.imposterIDs) != 1 {
					t.Fatalf("seed %d: %d imposters recorded, want 1", seed, len(r.imposterIDs))
				}
				if p := r.byID[r.imposterIDs[0]]; p == nil || p.word != r.imposterWord {
					t.Fatalf("seed %d: imposterIDs[0] does not hold the imposter word", seed)
				}
				if r.commonWord == r.imposterWord {
					t.Fatalf("seed %d: both sides of the pair are %q", seed, r.commonWord)
				}
				orientation[r.commonWord]++
				imposterSeats[r.imposterIDs[0]]++
			}

			// Both directions of the pair really occur.
			if orientation["CAT"] == 0 || orientation["DOG"] == 0 {
				t.Fatalf("the pair is always dealt the same way round: %v", orientation)
			}
			// And the imposter is not always the same seat.
			if len(imposterSeats) < 2 {
				t.Fatalf("the imposter is always seat %v", imposterSeats)
			}
			t.Logf("common word: %v; distinct imposter seats: %d", orientation, len(imposterSeats))
		})
	}
}

// TestNoPlayerIsToldTheirRole — DESIGN.md:25. YourWord carries a word and a
// round number, and there is no field anywhere on the wire that could tell a
// player their own role before the reveal.
func TestNoPlayerIsToldTheirRole(t *testing.T) {
	t.Parallel()

	if got, want := fieldNames(&genpb.YourWord{}), []string{"round", "word"}; !sameStrings(got, want) {
		t.Fatalf("YourWord fields = %v, want exactly %v", got, want)
	}

	// Sweep the whole schema for anything that names the imposter. Only these
	// messages may, and each for a stated reason.
	//
	// RoundWords is on this list for the same reason PlayerReveal is: it has no
	// existence outside MatchEnded, which is emitted only in PHASE_ENDED. The
	// three Spectator* messages have no existence outside SpectatorInfo, which
	// only sendSpectatorInfo produces and only an eliminated player receives.
	//
	// MatchSettings.imposter_count is the odd one out and is deliberate: the
	// COUNT is a public lobby setting every player reads before the match
	// starts, and knowing that a room has two imposters tells nobody which
	// seats they are (MULTIPLE_IMPOSTERS.md, "Role Assignment"). It is exactly
	// because the count is public that YourWord does not carry it.
	allowed := map[string]bool{
		"SpectatorInfo":       true,
		"SpectatorImposter":   true,
		"SpectatorAssignment": true,
		"SpectatorRound":      true,
		"MatchEnded":          true,
		"PlayerReveal":        true,
		"RoundWords":          true,
		"PlayerEliminated":    true,
		"MatchSettings":       true,
	}
	file := (&genpb.VoteTally{}).ProtoReflect().Descriptor().ParentFile()
	var offenders []string
	var walk func(protoreflect.MessageDescriptors)
	walk = func(msgs protoreflect.MessageDescriptors) {
		for i := range msgs.Len() {
			m := msgs.Get(i)
			for j := range m.Fields().Len() {
				f := m.Fields().Get(j)
				if !bytes.Contains([]byte(f.Name()), []byte("imposter")) {
					continue
				}
				if !allowed[string(m.Name())] {
					offenders = append(offenders, fmt.Sprintf("%s.%s", m.Name(), f.Name()))
				}
			}
			walk(m.Messages())
		}
	}
	walk(file.Messages())
	if len(offenders) > 0 {
		slices.Sort(offenders)
		t.Fatalf("these messages disclose the imposter: %v", offenders)
	}

	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6, mkSettings(1, 5, 30), 3004)
		defer h.stop()
		h.discard()
		h.start()

		for i, evs := range h.drainAll() {
			words := 0
			for _, e := range evs {
				w := e.GetYourWord()
				if w == nil {
					continue
				}
				words++
				if got := h.word(i); w.GetWord() != got {
					t.Fatalf("player %d was sent %q, holds %q", i, w.GetWord(), got)
				}
				if got := w.GetRound(); got != 1 {
					t.Fatalf("player %d: YourWord.round = %d, want 1", i, got)
				}
			}
			if words != 1 {
				t.Fatalf("player %d received %d YourWord frames, want exactly 1", i, words)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Settings and roster bounds
// ---------------------------------------------------------------------------

// TestSettingsBoundaries — DESIGN.md:224. Every configurable range, one step
// outside each end and exactly on it. Out-of-range values are forced into
// range rather than rejected, so a client that sends nonsense gets the nearest
// legal game instead of an unstartable room.
func TestSettingsBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                  string
		in                    *genpb.MatchSettings
		rounds, draw, discuss int32
		difficulty            genpb.Difficulty
	}{
		{"nil_is_the_recommended_game", nil, DefaultRounds, DefaultDrawSeconds, DefaultDiscussSeconds,
			genpb.Difficulty_DIFFICULTY_MEDIUM},
		{"zero_is_the_recommended_game", &genpb.MatchSettings{}, DefaultRounds, DefaultDrawSeconds,
			DefaultDiscussSeconds, genpb.Difficulty_DIFFICULTY_MEDIUM},

		{"rounds_below_range", mkSettings(-1, 15, 120), MinRounds, 15, 120, genpb.Difficulty_DIFFICULTY_MEDIUM},
		{"rounds_at_min", mkSettings(1, 15, 120), 1, 15, 120, genpb.Difficulty_DIFFICULTY_MEDIUM},
		{"rounds_at_max", mkSettings(4, 15, 120), 4, 15, 120, genpb.Difficulty_DIFFICULTY_MEDIUM},
		{"rounds_above_range", mkSettings(5, 15, 120), MaxRounds, 15, 120, genpb.Difficulty_DIFFICULTY_MEDIUM},

		{"draw_below_range", mkSettings(2, 4, 120), 2, MinDrawSeconds, 120, genpb.Difficulty_DIFFICULTY_MEDIUM},
		{"draw_at_min", mkSettings(2, 5, 120), 2, 5, 120, genpb.Difficulty_DIFFICULTY_MEDIUM},
		{"draw_at_max", mkSettings(2, 60, 120), 2, 60, 120, genpb.Difficulty_DIFFICULTY_MEDIUM},
		{"draw_above_range", mkSettings(2, 61, 120), 2, MaxDrawSeconds, 120, genpb.Difficulty_DIFFICULTY_MEDIUM},

		{"discuss_below_range", mkSettings(2, 15, 29), 2, 15, MinDiscussSeconds, genpb.Difficulty_DIFFICULTY_MEDIUM},
		{"discuss_at_min", mkSettings(2, 15, 30), 2, 15, 30, genpb.Difficulty_DIFFICULTY_MEDIUM},
		{"discuss_at_max", mkSettings(2, 15, 180), 2, 15, 180, genpb.Difficulty_DIFFICULTY_MEDIUM},
		{"discuss_above_range", mkSettings(2, 15, 181), 2, 15, MaxDiscussSeconds, genpb.Difficulty_DIFFICULTY_MEDIUM},

		{"difficulty_easy", &genpb.MatchSettings{Difficulty: genpb.Difficulty_DIFFICULTY_EASY},
			DefaultRounds, DefaultDrawSeconds, DefaultDiscussSeconds, genpb.Difficulty_DIFFICULTY_EASY},
		{"difficulty_hard", &genpb.MatchSettings{Difficulty: genpb.Difficulty_DIFFICULTY_HARD},
			DefaultRounds, DefaultDrawSeconds, DefaultDiscussSeconds, genpb.Difficulty_DIFFICULTY_HARD},
		{"difficulty_unknown_falls_back", &genpb.MatchSettings{Difficulty: genpb.Difficulty(99)},
			DefaultRounds, DefaultDrawSeconds, DefaultDiscussSeconds, genpb.Difficulty_DIFFICULTY_MEDIUM},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ClampSettings(tc.in)
			if got.GetMaxRounds() != tc.rounds {
				t.Errorf("max_rounds = %d, want %d", got.GetMaxRounds(), tc.rounds)
			}
			if got.GetDrawSeconds() != tc.draw {
				t.Errorf("draw_seconds = %d, want %d", got.GetDrawSeconds(), tc.draw)
			}
			if got.GetDiscussSeconds() != tc.discuss {
				t.Errorf("discuss_seconds = %d, want %d", got.GetDiscussSeconds(), tc.discuss)
			}
			if got.GetDifficulty() != tc.difficulty {
				t.Errorf("difficulty = %v, want %v", got.GetDifficulty(), tc.difficulty)
			}
			if tc.in != nil && got == tc.in {
				t.Error("ClampSettings returned the caller's own struct")
			}
		})
	}
}

// TestPenRuleIsClampedToTheEnumerable — DESIGN.md:104. The pen rule is a host
// setting like every other, so an unset or unknown value becomes the default
// instead of an error: a newer client cannot hand this room a handicap it does
// not enforce, and an older one that omits the field still gets a legal game.
func TestPenRuleIsClampedToTheEnumerable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   *genpb.MatchSettings
		want genpb.PenRule
	}{
		{"nil_is_free", nil, genpb.PenRule_PEN_RULE_FREE},
		{"unset_is_free", &genpb.MatchSettings{}, genpb.PenRule_PEN_RULE_FREE},
		{"unspecified_is_free", &genpb.MatchSettings{PenRule: genpb.PenRule_PEN_RULE_UNSPECIFIED},
			genpb.PenRule_PEN_RULE_FREE},
		{"free_survives", &genpb.MatchSettings{PenRule: genpb.PenRule_PEN_RULE_FREE},
			genpb.PenRule_PEN_RULE_FREE},
		{"one_line_survives", &genpb.MatchSettings{PenRule: genpb.PenRule_PEN_RULE_ONE_LINE},
			genpb.PenRule_PEN_RULE_ONE_LINE},
		{"max_five_survives", &genpb.MatchSettings{PenRule: genpb.PenRule_PEN_RULE_MAX_FIVE},
			genpb.PenRule_PEN_RULE_MAX_FIVE},
		{"unknown_falls_back", &genpb.MatchSettings{PenRule: genpb.PenRule(99)},
			genpb.PenRule_PEN_RULE_FREE},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ClampSettings(tc.in)
			if got.GetPenRule() != tc.want {
				t.Errorf("pen_rule = %v, want %v", got.GetPenRule(), tc.want)
			}
			// A pen rule must not disturb the rest of the recommended game.
			if got.GetMaxRounds() != DefaultRounds || got.GetDrawSeconds() != DefaultDrawSeconds {
				t.Errorf("pen rule changed the other defaults: rounds %d, draw %d",
					got.GetMaxRounds(), got.GetDrawSeconds())
			}
		})
	}
}

// TestHostSettingsAreClampedOnTheWire — the clamp is not just a helper; the
// value the host actually gets back is the clamped one.
func TestHostSettingsAreClampedOnTheWire(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(2, 15, 120), 4001)
		defer h.stop()
		h.discard()

		h.send(0, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_UpdateSettings{
			UpdateSettings: &genpb.UpdateSettings{Settings: mkSettings(9, 999, 1)}}})
		synctest.Wait()

		var got *genpb.MatchSettings
		for _, e := range h.drain(0) {
			if v := e.GetSettingsChanged(); v != nil {
				got = v.GetSettings()
			}
		}
		if got == nil {
			t.Fatal("no SettingsChanged")
		}
		if got.GetMaxRounds() != MaxRounds || got.GetDrawSeconds() != MaxDrawSeconds ||
			got.GetDiscussSeconds() != MinDiscussSeconds {
			t.Fatalf("settings echoed back unclamped: %v", got)
		}

		// And a non-host cannot change them at all.
		h.discard()
		h.send(1, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_UpdateSettings{
			UpdateSettings: &genpb.UpdateSettings{Settings: mkSettings(1, 5, 30)}}})
		synctest.Wait()
		if !hasErrorCode(h.drain(1), genpb.ErrorCode_ERROR_CODE_NOT_HOST) {
			t.Fatal("a non-host changed the match settings")
		}
	})
}

// TestRosterBoundaries — DESIGN.md:20. A match needs 3 to 10 players; 2 is one
// short and 11 is one too many.
func TestRosterBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("two_players_cannot_start", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, MinPlayers-1, mkSettings(2, 5, 30), 4002)
			defer h.stop()
			h.discard()
			if smokeGet(h.r, func(r *Room) bool { return r.canStart() }) {
				t.Fatal("canStart() is true with 2 seated players")
			}
			h.send(0, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StartMatch{
				StartMatch: &genpb.StartMatch{}}})
			synctest.Wait()
			if !hasErrorCode(h.drain(0), genpb.ErrorCode_ERROR_CODE_NOT_ENOUGH_PLAYERS) {
				t.Fatal("a 2-player match was allowed to start")
			}
			if got := h.phase(); got != genpb.Phase_PHASE_LOBBY {
				t.Fatalf("phase = %v, want LOBBY", got)
			}
		})
	})

	t.Run("three_players_can_start", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, MinPlayers, mkSettings(2, 5, 30), 4003)
			defer h.stop()
			if !smokeGet(h.r, func(r *Room) bool { return r.canStart() }) {
				t.Fatal("canStart() is false with 3 ready, connected players")
			}
			h.start()
		})
	})

	t.Run("eleventh_seat_refused", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, MaxPlayers, mkSettings(2, 5, 30), 4004)
			defer h.stop()
			if got := h.seatCount(); got != MaxPlayers {
				t.Fatalf("seats = %d, want %d", got, MaxPlayers)
			}
			extra := newSmokeSock()
			if _, _, err := h.r.seat("eleventh", genpb.Avatar_AVATAR_COURIER, extra); err != ErrRoomFull {
				t.Fatalf("seating an 11th player returned %v, want ErrRoomFull", err)
			}
			if got := h.seatCount(); got != MaxPlayers {
				t.Fatalf("seats = %d after a refused join, want %d", got, MaxPlayers)
			}
			h.start()
		})
	})

	t.Run("no_seats_once_the_match_starts", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, 3, mkSettings(2, 5, 30), 4005)
			defer h.stop()
			h.start()
			latecomer := newSmokeSock()
			if _, _, err := h.r.seat("late", genpb.Avatar_AVATAR_COURIER, latecomer); err != ErrMatchInProgress {
				t.Fatalf("seating mid-match returned %v, want ErrMatchInProgress", err)
			}
		})
	})

	t.Run("an_unready_player_blocks_the_start", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, 4, mkSettings(2, 5, 30), 4006)
			defer h.stop()
			h.send(2, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_SetReady{
				SetReady: &genpb.SetReady{Ready: false}}})
			synctest.Wait()
			h.discard()
			h.send(0, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StartMatch{
				StartMatch: &genpb.StartMatch{}}})
			synctest.Wait()
			if !hasErrorCode(h.drain(0), genpb.ErrorCode_ERROR_CODE_NOT_READY) {
				t.Fatal("the match started with an unready player")
			}
		})
	})
}

// ---------------------------------------------------------------------------
// Known failure — reported, not weakened
// ---------------------------------------------------------------------------

// TestMatchNeverRunsPastTheConfiguredFinalRound — DESIGN.md:71 and
// DESIGN.md:224: max_rounds is a maximum. The room may end a match sooner, and
// it may hold the end open, but it may not announce a round the host never
// configured.
//
// THIS TEST FAILS AGAINST THE CURRENT IMPLEMENTATION. It is not weakened on
// purpose. The mechanism:
//
//	evaluateEnd (end.go:85) refuses to end a match on any headcount while the
//	imposter's socket is dark — correctly, because DESIGN.md:125 reserves that
//	situation for a group win. But the final-round rule sits in the same switch,
//	below the same guard, so it is skipped too. resolveRound therefore leaves
//	endReason UNSPECIFIED, and afterResolve (phase.go:210) has no cap of its own:
//	it calls beginRound(r.round + 1) unconditionally.
//
//	The result is RoundStarted{round: 3, total_rounds: 2} on the wire, a client
//	showing "Round 3 of 2", and a whole extra round of drawing evidence handed
//	to the group because one player's connection blinked.
//
// Two independent fixes, either of which closes it: cap afterResolve at
// max_rounds and park in PHASE_RESOLVING until the imposter's seat resolves one
// way or the other, or call reevaluateEnd from attachOnActor so a returning
// imposter settles the match the moment they are back.
func TestMatchNeverRunsPastTheConfiguredFinalRound(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const maxRounds = 2
		h := newHarness(t, 5, mkSettings(maxRounds, 5, 30), 3005)
		defer h.stop()
		h.discard()
		h.start()

		// Round 1 passes with nobody eliminated.
		h.toDiscussion()
		h.skipAll()
		h.advance(ResolveDuration)
		h.toDiscussion()
		if got := h.round(); got != maxRounds {
			t.Fatalf("round = %d, want the configured final round %d", got, maxRounds)
		}

		// The imposter's connection blinks during the final round's voting
		// window. Their seat, word and match state are retained; they are
		// simply not active for the moment (DESIGN.md, "Active players").
		oi := h.imposterIdx()
		watcher := h.anyIdxExcept(oi)
		h.r.detach(h.ids[oi], h.socks[oi])
		synctest.Wait()
		h.discard()

		// Everyone still here votes, so the final round resolves.
		h.skipAll()
		if got := h.phase(); got != genpb.Phase_PHASE_RESOLVING {
			t.Fatalf("phase = %v, want RESOLVING", got)
		}
		h.advance(ResolveDuration)

		for _, rs := range allRoundStarted(h.drain(watcher)) {
			if rs.GetRound() > maxRounds {
				t.Errorf("RoundStarted announced round %d of %d",
					rs.GetRound(), rs.GetTotalRounds())
			}
		}
		if got := h.round(); got > maxRounds {
			t.Fatalf("the match is playing round %d of a %d-round game: the "+
				"final-round check sits under evaluateEnd's dark-imposter guard, "+
				"and afterResolve has no cap of its own", got, maxRounds)
		}
	})
}
