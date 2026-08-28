package room

// imposters_test.go — the multi-imposter rules (MULTIPLE_IMPOSTERS.md).
//
// The base game is a two-imposter match with the count set to one, so nothing
// here re-tests what rules_test.go already covers. What is new is exactly four
// things, and every test below is one of them:
//
//   1. two seats, one shared odd word, neither told about the other;
//   2. the group wins only when BOTH are gone;
//   3. Reveal and Hidden say different things about the same elimination;
//   4. an eliminated player holds the whole match, and nobody else holds any
//      of it.

import (
	"bytes"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// mkImposterSettings is mkSettings plus the two knobs this file exists for.
func mkImposterSettings(rounds, draw, discuss, imposters int32, results genpb.EliminationResults) *genpb.MatchSettings {
	s := mkSettings(rounds, draw, discuss)
	s.ImposterCount = imposters
	s.EliminationResults = results
	return s
}

// gangUp makes every active seat vote for one candidate, which is a plurality
// unless the room is down to a single voter.
func gangUp(h *harness, candidate string) {
	h.t.Helper()
	for i := range h.ids {
		if h.active(i) {
			h.vote(i, candidate)
		}
	}
	synctest.Wait()
}

// ---------------------------------------------------------------------------
// 1. The deal
// ---------------------------------------------------------------------------

// TestTwoImpostersShareOneWord — MULTIPLE_IMPOSTERS.md, "Role Assignment".
// Two distinct seats, both holding the same alternate word, every round, and
// the pair is still just a pair: there is no third word anywhere.
func TestTwoImpostersShareOneWord(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarnessWithDeck(t, 6,
			mkImposterSettings(2, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
			7101, roundDeck())
		defer h.stop()
		h.start()

		ids := h.imposters()
		if len(ids) != 2 {
			t.Fatalf("dealt %d imposters, want 2", len(ids))
		}
		if ids[0] == ids[1] {
			t.Fatal("the same seat was chosen twice; the two imposters are one player")
		}

		for round := 1; round <= 2; round++ {
			odd, common := 0, 0
			var oddWord string
			for i := range h.ids {
				w := h.word(i)
				if h.indexOf(ids[0]) == i || h.indexOf(ids[1]) == i {
					odd++
					if oddWord == "" {
						oddWord = w
					} else if w != oddWord {
						t.Fatalf("round %d: the two imposters hold %q and %q — they must share one word",
							round, oddWord, w)
					}
					continue
				}
				common++
				if w == oddWord && oddWord != "" {
					t.Fatalf("round %d: a non-imposter holds the imposter word %q", round, w)
				}
			}
			if odd != 2 || common != 4 {
				t.Fatalf("round %d: %d imposters and %d others, want 2 and 4", round, odd, common)
			}

			if round == 2 {
				break
			}
			h.toDiscussion()
			h.skipAll()
			h.nextRound()
			// The seats do not re-roll between rounds; only the words do.
			if got := h.imposters(); !sameStrings(got, ids) {
				t.Fatalf("round 2 re-rolled the imposters: %v, want %v", got, ids)
			}
		}
	})
}

// TestNeitherImposterLearnsTheOther — MULTIPLE_IMPOSTERS.md, "Role
// Assignment": "two imposters do not receive each other's identity and must
// not be given a private team channel".
//
// Asserted over the raw bytes of every frame either imposter received, because
// a leak here would not be a field anybody named — it would be a field
// somebody added.
func TestNeitherImposterLearnsTheOther(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 5,
			mkImposterSettings(1, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
			7102)
		defer h.stop()
		h.discard()
		h.start()
		h.toDiscussion()

		ids := h.imposters()
		if len(ids) != 2 {
			t.Fatalf("dealt %d imposters, want 2", len(ids))
		}

		for _, seat := range h.imposterIdxs() {
			// Everything this imposter received, up to and not including the
			// final reveal — which is allowed to name both of them.
			for n, ev := range h.drain(seat) {
				if ev.GetMatchEnded() != nil {
					continue
				}
				raw, err := proto.Marshal(ev)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				// The OTHER imposter's id. Their own id is on every roster and
				// is not a disclosure; the pairing is.
				other := ids[0]
				if h.ids[seat] == other {
					other = ids[1]
				}
				// A roster entry names every seat, so an id alone is not the
				// test. What must not exist is a frame that names the other
				// imposter WITHOUT also naming everybody — i.e. a private
				// channel. YourWord and VoteAccepted are the private frames an
				// imposter gets; neither may carry a player id at all.
				switch ev.GetEvt().(type) {
				case *genpb.ServerEvent_YourWord, *genpb.ServerEvent_SpectatorInfo:
					if containsBytes(raw, other) {
						t.Fatalf("seat %d frame %d (%T) names the other imposter %q",
							seat, n, ev.GetEvt(), other)
					}
				}
			}
		}

		// And structurally: YourWord has room for a word and a round, and
		// nothing that could name a teammate or count them.
		if got, want := fieldNames(&genpb.YourWord{}), []string{"round", "word"}; !sameStrings(got, want) {
			t.Fatalf("YourWord fields = %v, want exactly %v", got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// 2. Win conditions
// ---------------------------------------------------------------------------

// TestCatchingOneOfTwoImpostersContinuesTheMatch — MULTIPLE_IMPOSTERS.md,
// "Win Conditions": the group wins only when BOTH imposters are gone. Catching
// the first is a real result the match survives, which is the single biggest
// behavioural difference from the base design.
func TestCatchingOneOfTwoImpostersContinuesTheMatch(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarnessWithDeck(t, 6,
			mkImposterSettings(3, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
			7103, roundDeck())
		defer h.stop()
		h.start()
		h.toDiscussion()

		ids := h.imposters()
		first, second := ids[0], ids[1]

		h.discard()
		gangUp(h, first)
		if got := h.phase(); got != genpb.Phase_PHASE_RESOLVING {
			t.Fatalf("phase = %v, want RESOLVING", got)
		}

		el := lastEliminated(h.drain(0))
		if el == nil || !el.GetEliminated() || el.GetPlayerId() != first {
			t.Fatalf("PlayerEliminated = %v, want the first imposter", el)
		}
		if !el.GetWasImposter() || !el.GetAlignmentRevealed() {
			t.Fatal("Reveal must say an imposter was caught, even when the match continues")
		}
		// The match is NOT over. This is the assertion the base design would
		// have failed: there, eliminating an imposter ends it.
		if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_UNSPECIFIED ||
			reason != genpb.MatchEndReason_MATCH_END_REASON_UNSPECIFIED {
			t.Fatalf("catching one of two imposters ended the match %v/%v", w, reason)
		}

		h.nextRound()
		if got := h.round(); got != 2 {
			t.Fatalf("round = %d, want the match to have gone on to 2", got)
		}
		if !h.eliminated(h.indexOf(first)) {
			t.Fatal("the caught imposter is still active in round 2")
		}

		// Now the second. Four seats are active, so this is the elimination
		// that both empties the imposter side AND leaves three standing —
		// unambiguously a group win rather than a headcount.
		h.discard()
		gangUp(h, second)
		el = lastEliminated(h.drain(0))
		if el == nil || el.GetPlayerId() != second || !el.GetWasImposter() {
			t.Fatalf("PlayerEliminated = %v, want the second imposter", el)
		}
		if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_GROUP ||
			reason != genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_ELIMINATED {
			t.Fatalf("outcome = %v/%v, want GROUP/IMPOSTER_ELIMINATED", w, reason)
		}

		h.advance(ResolveDuration)
		me := lastMatchEnded(h.drain(0))
		if me == nil {
			t.Fatal("no MatchEnded")
		}
		if got := me.GetImposterPlayerIds(); !sameStrings(sorted(got), sorted(ids)) {
			t.Fatalf("MatchEnded names %v as the imposters, want %v", got, ids)
		}
		marked := 0
		for _, rv := range me.GetReveals() {
			if rv.GetWasImposter() {
				marked++
			}
		}
		if marked != 2 {
			t.Fatalf("%d reveal rows marked as imposters, want 2", marked)
		}
	})
}

// TestSurvivingImposterWinsTheFinalRound — one of two left standing after the
// last round is still an imposter-side win. The group getting halfway is worth
// nothing (MULTIPLE_IMPOSTERS.md, "Win Conditions").
func TestSurvivingImposterWinsTheFinalRound(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarnessWithDeck(t, 6,
			mkImposterSettings(2, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
			7104, roundDeck())
		defer h.stop()
		h.start()
		h.toDiscussion()

		first := h.imposters()[0]
		gangUp(h, first)
		h.nextRound()

		// Round 2 is the last. Nobody goes out, so an imposter is still active
		// when it resolves.
		h.skipAll()
		if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_IMPOSTER ||
			reason != genpb.MatchEndReason_MATCH_END_REASON_FINAL_ROUND_SURVIVED {
			t.Fatalf("outcome = %v/%v, want IMPOSTER/FINAL_ROUND_SURVIVED", w, reason)
		}
	})
}

// TestThreePlayersTwoImpostersIsStartableAndUnwinnable — MULTIPLE_IMPOSTERS.md,
// "Three-player warning". The server does not refuse the configuration; it just
// plays out exactly as the warning says it will.
func TestThreePlayersTwoImpostersIsStartableAndUnwinnable(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3,
			mkImposterSettings(2, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
			7105)
		defer h.stop()

		// Startable: the lobby warns, the server does not block.
		if !smokeGet(h.r, func(r *Room) bool { return r.canStart() }) {
			t.Fatal("the server refused to start 3 players with 2 imposters")
		}
		h.start()

		if got := len(h.imposters()); got != 2 {
			t.Fatalf("dealt %d imposters, want 2", got)
		}
		h.toDiscussion()

		// Whoever goes, two players are left and the imposter side takes it —
		// even when the seat eliminated was an imposter, because one of them
		// is still holding the odd word.
		gangUp(h, h.imposters()[0])
		if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_IMPOSTER ||
			reason != genpb.MatchEndReason_MATCH_END_REASON_TWO_PLAYERS_REMAIN {
			t.Fatalf("outcome = %v/%v, want IMPOSTER/TWO_PLAYERS_REMAIN", w, reason)
		}
	})
}

// TestAnEliminatedImposterLeavingDoesNotEndTheMatch — the guard in expireSeat.
//
// The base design never had to think about this: there, an eliminated imposter
// ends the match, so the seat cannot outlive its own elimination. With two,
// the first one caught sits in the roster for the rest of the match and may
// well close the tab — and handing the group a win for that would take the
// match away from the imposter still holding the odd word.
func TestAnEliminatedImposterLeavingDoesNotEndTheMatch(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarnessWithDeck(t, 6,
			mkImposterSettings(3, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
			7106, roundDeck())
		defer h.stop()
		h.start()
		h.toDiscussion()

		caught := h.imposters()[0]
		seat := h.indexOf(caught)
		gangUp(h, caught)
		h.nextRound()
		if !h.eliminated(seat) {
			t.Fatal("the first imposter was not eliminated")
		}

		// Their socket goes for good.
		h.r.detach(h.ids[seat], h.socks[seat])
		h.advance(GraceWindow + 2*time.Second)

		if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_UNSPECIFIED ||
			reason != genpb.MatchEndReason_MATCH_END_REASON_UNSPECIFIED {
			t.Fatalf("an ALREADY eliminated imposter's grace window ended the match %v/%v", w, reason)
		}
		if got := h.phase(); got == genpb.Phase_PHASE_ENDED {
			t.Fatal("the match ended when a seat that was already out went dark")
		}
	})
}

// TestAnActiveImposterLeavingStillEndsTheMatch — the other half of the same
// guard. One of a pair can stall the room exactly as well as a lone imposter,
// so DESIGN.md:125 still applies to whichever of them drops.
func TestAnActiveImposterLeavingStillEndsTheMatch(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 6,
			mkImposterSettings(3, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
			7107)
		defer h.stop()
		h.start()
		h.toDiscussion()

		seat := h.imposterIdxs()[0]
		h.r.detach(h.ids[seat], h.socks[seat])
		h.advance(GraceWindow + 2*time.Second)

		if w, reason := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_GROUP ||
			reason != genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_DISCONNECTED {
			t.Fatalf("outcome = %v/%v, want GROUP/IMPOSTER_DISCONNECTED", w, reason)
		}
	})
}

// ---------------------------------------------------------------------------
// 3. Elimination results
// ---------------------------------------------------------------------------

// TestHiddenResultsWithholdTheAlignment — MULTIPLE_IMPOSTERS.md, "Hidden": the
// public result names who went and nothing else.
func TestHiddenResultsWithholdTheAlignment(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarnessWithDeck(t, 6,
			mkImposterSettings(3, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_HIDDEN),
			7108, roundDeck())
		defer h.stop()
		h.start()
		h.toDiscussion()

		// Catch a real imposter, so there is genuinely something to withhold.
		first := h.imposters()[0]
		h.discard()
		gangUp(h, first)

		for i, evs := range h.drainAll() {
			el := lastEliminated(evs)
			if el == nil {
				t.Fatalf("socket %d never heard about the elimination", i)
			}
			if el.GetPlayerId() != first {
				t.Fatalf("socket %d: PlayerEliminated names %q, want %q", i, el.GetPlayerId(), first)
			}
			if el.GetAlignmentRevealed() {
				t.Fatalf("socket %d: Hidden results set alignment_revealed on a "+
					"result the match survived", i)
			}
			if el.GetWasImposter() {
				t.Fatalf("socket %d: Hidden results set was_imposter", i)
			}
		}
		if w, _ := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_UNSPECIFIED {
			t.Fatalf("the match ended %v; there is a second imposter still in it", w)
		}
	})
}

// TestHiddenResultsStillDiscloseTheEndingElimination — the elimination that
// ends the match is followed by MatchEnded, which publishes every alignment on
// purpose. Withholding the flag there would conceal nothing and would leave a
// client rendering "somebody went" over a finished game.
func TestHiddenResultsStillDiscloseTheEndingElimination(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarnessWithDeck(t, 6,
			mkImposterSettings(3, 5, 30, 1, genpb.EliminationResults_ELIMINATION_RESULTS_HIDDEN),
			7109, roundDeck())
		defer h.stop()
		h.start()
		h.toDiscussion()

		h.discard()
		gangUp(h, h.imposter())

		el := lastEliminated(h.drain(0))
		if el == nil || !el.GetEliminated() {
			t.Fatalf("PlayerEliminated = %v", el)
		}
		if !el.GetAlignmentRevealed() || !el.GetWasImposter() {
			t.Fatal("the elimination that ends the match must disclose, even under Hidden")
		}
		if w, _ := h.outcome(); w != genpb.WinnerSide_WINNER_SIDE_GROUP {
			t.Fatalf("winner = %v, want GROUP", w)
		}
	})
}

// TestRevealIsTheDefault — an unset or unknown value becomes Reveal, and the
// count becomes 1, so an older client that never learned about either setting
// gets the base game rather than a surprise.
func TestSettingsDefaultsAndClamping(t *testing.T) {
	t.Parallel()

	d := ClampSettings(nil)
	if d.GetImposterCount() != DefaultImposters {
		t.Fatalf("default imposter_count = %d, want %d", d.GetImposterCount(), DefaultImposters)
	}
	if d.GetEliminationResults() != genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL {
		t.Fatalf("default elimination_results = %v, want REVEAL", d.GetEliminationResults())
	}

	for _, tc := range []struct {
		name string
		in   int32
		want int32
	}{
		{"unset becomes the default", 0, DefaultImposters},
		{"one is honoured", 1, 1},
		{"two is honoured", 2, 2},
		{"three is clamped down", 3, MaxImposters},
		{"negative is clamped up", -4, MinImposters},
	} {
		got := ClampSettings(&genpb.MatchSettings{ImposterCount: tc.in}).GetImposterCount()
		if got != tc.want {
			t.Errorf("%s: imposter_count %d clamped to %d, want %d", tc.name, tc.in, got, tc.want)
		}
	}

	// An enum value this build has no name for degrades to the default rather
	// than being taken at face value.
	unknown := ClampSettings(&genpb.MatchSettings{EliminationResults: genpb.EliminationResults(97)})
	if unknown.GetEliminationResults() != genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL {
		t.Fatalf("an unknown elimination_results became %v, want REVEAL",
			unknown.GetEliminationResults())
	}
}

// TestSettingsLockAtMatchStart — MULTIPLE_IMPOSTERS.md: both values lock when
// the host starts the match.
func TestImposterSettingsLockAtMatchStart(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 5,
			mkImposterSettings(2, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
			7110)
		defer h.stop()
		h.start()
		h.toDiscussion()
		h.discard()

		h.send(0, &genpb.ClientCommand{Cid: "late", Cmd: &genpb.ClientCommand_UpdateSettings{
			UpdateSettings: &genpb.UpdateSettings{
				Settings: mkImposterSettings(2, 5, 30, 1,
					genpb.EliminationResults_ELIMINATION_RESULTS_HIDDEN),
			}}})
		synctest.Wait()

		got := smokeGet(h.r, func(r *Room) *genpb.MatchSettings { return r.settings })
		if got.GetImposterCount() != 2 {
			t.Fatalf("imposter_count changed mid-match to %d", got.GetImposterCount())
		}
		if got.GetEliminationResults() != genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL {
			t.Fatalf("elimination_results changed mid-match to %v", got.GetEliminationResults())
		}
		if len(h.imposters()) != 2 {
			t.Fatalf("the dealt imposter count changed mid-match to %d", len(h.imposters()))
		}
	})
}

// ---------------------------------------------------------------------------
// 4. The spectator dossier
// ---------------------------------------------------------------------------

// TestSpectatorGetsTheWholeMatchAndNobodyElseDoes — MULTIPLE_IMPOSTERS.md,
// "Eliminated-player Spectator View". The eliminated player receives every
// imposter, every round's pair, every seat's word and the finished canvas; a
// player still in the match receives none of it, ever.
func TestSpectatorGetsTheWholeMatchAndNobodyElseDoes(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarnessWithDeck(t, 6,
			mkImposterSettings(3, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
			7111, roundDeck())
		defer h.stop()
		h.start()

		ids := h.imposters()
		// Draw something, so there is a canvas worth archiving.
		h.toDrawing()
		artist := h.indexOf(h.artist())
		drawOneStroke(h, artist)
		h.toDiscussion()

		victim := h.anyIdxExcept(h.imposterIdxs()...)
		h.discard()
		gangUp(h, h.ids[victim])

		for i, evs := range h.drainAll() {
			spec := allSpectatorInfo(evs)
			if i != victim {
				if len(spec) != 0 {
					t.Fatalf("socket %d received a spectator dossier while still in the match", i)
				}
				continue
			}
			if len(spec) != 1 {
				t.Fatalf("the new spectator received %d dossiers, want 1", len(spec))
			}
			si := spec[0]

			named := make([]string, 0, 2)
			for _, im := range si.GetImposters() {
				named = append(named, im.GetPlayerId())
				if im.GetName() == "" {
					t.Errorf("imposter %q has no name in the dossier", im.GetPlayerId())
				}
			}
			if !sameStrings(sorted(named), sorted(ids)) {
				t.Fatalf("dossier names %v as the imposters, want %v", named, ids)
			}

			if len(si.GetRounds()) != 1 {
				t.Fatalf("dossier carries %d rounds, want 1", len(si.GetRounds()))
			}
			rd := si.GetRounds()[0]
			if rd.GetRound() != 1 {
				t.Fatalf("dossier round = %d, want 1", rd.GetRound())
			}
			if rd.GetCommonWord() == "" || rd.GetImposterWord() == "" ||
				rd.GetCommonWord() == rd.GetImposterWord() {
				t.Fatalf("dossier pair = %q/%q", rd.GetCommonWord(), rd.GetImposterWord())
			}
			if n := len(rd.GetStrokes()); n == 0 {
				t.Fatal("dossier carries no canvas for a round that was drawn on")
			}

			// One row per seat, each on the correct side of the pair.
			if n := len(rd.GetAssignments()); n != len(h.ids) {
				t.Fatalf("dossier assigns %d seats, want %d", n, len(h.ids))
			}
			odd := 0
			for _, a := range rd.GetAssignments() {
				want := rd.GetCommonWord()
				if a.GetIsImposter() {
					want = rd.GetImposterWord()
					odd++
				}
				if a.GetWord() != want {
					t.Errorf("dossier gives %q the word %q, want %q", a.GetPlayerId(), a.GetWord(), want)
				}
				if got := smokeWordOf(h.r, a.GetPlayerId()); got != a.GetWord() {
					t.Errorf("dossier gives %q %q but the room dealt them %q",
						a.GetPlayerId(), a.GetWord(), got)
				}
			}
			if odd != 2 {
				t.Fatalf("dossier marks %d seats as imposters, want 2", odd)
			}
		}

		// The next round's assignments land as they are dealt, without the
		// spectator asking for anything.
		h.discard()
		h.nextRound()
		spec := allSpectatorInfo(h.drain(victim))
		if len(spec) == 0 {
			t.Fatal("the spectator was not sent round 2's assignments as they were dealt")
		}
		latest := spec[len(spec)-1]
		if n := len(latest.GetRounds()); n != 2 {
			t.Fatalf("dossier carries %d rounds after round 2 was dealt, want 2", n)
		}
		if latest.GetRounds()[0].GetCommonWord() == latest.GetRounds()[1].GetCommonWord() {
			t.Fatal("round 2 of the dossier repeats round 1's word")
		}
		// A seat eliminated before round 2 was dealt is absent from it rather
		// than blank.
		for _, a := range latest.GetRounds()[1].GetAssignments() {
			if a.GetPlayerId() == h.ids[victim] {
				t.Fatal("the eliminated seat was dealt into a round it was out of")
			}
		}

		// And nobody still playing has picked any of it up in the meantime.
		for i := range h.ids {
			if i == victim {
				continue
			}
			if n := len(allSpectatorInfo(h.drain(i))); n != 0 {
				t.Fatalf("socket %d received %d dossiers while still in the match", i, n)
			}
		}
	})
}

// TestSpectatorDossierSurvivesAReconnect — MULTIPLE_IMPOSTERS.md validation
// item 7. A Snapshot carries the live canvas and the recipient's own word, so
// without the dossier riding beside it a spectator would come back holding
// strictly less than they left with.
func TestSpectatorDossierSurvivesAReconnect(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarnessWithDeck(t, 6,
			mkImposterSettings(3, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
			7112, roundDeck())
		defer h.stop()
		h.start()
		h.toDiscussion()

		victim := h.anyIdxExcept(h.imposterIdxs()...)
		gangUp(h, h.ids[victim])
		h.nextRound()

		// Drop and come back on a fresh socket.
		h.r.detach(h.ids[victim], h.socks[victim])
		h.advance(time.Second)
		sk := newSmokeSock()
		if _, err := h.r.attach(h.toks[victim], sk); err != nil {
			t.Fatalf("reattach: %v", err)
		}
		h.socks[victim] = sk
		synctest.Wait()

		spec := allSpectatorInfo(sk.drain())
		if len(spec) != 1 {
			t.Fatalf("a reconnecting spectator received %d dossiers, want 1", len(spec))
		}
		if n := len(spec[0].GetImposters()); n != 2 {
			t.Fatalf("the restored dossier names %d imposters, want 2", n)
		}
		if n := len(spec[0].GetRounds()); n != 2 {
			t.Fatalf("the restored dossier carries %d rounds, want 2 — the whole match so far", n)
		}

		// An ACTIVE player resyncing gets a Snapshot and nothing else.
		other := h.anyIdxExcept(victim)
		h.discard()
		h.send(other, &genpb.ClientCommand{Cid: "rs",
			Cmd: &genpb.ClientCommand_RequestSnapshot{RequestSnapshot: &genpb.RequestSnapshot{}}})
		synctest.Wait()
		evs := h.drain(other)
		if lastSnapshot(evs) == nil {
			t.Fatal("an active player's resync produced no Snapshot")
		}
		if n := len(allSpectatorInfo(evs)); n != 0 {
			t.Fatalf("an active player's resync produced %d dossiers", n)
		}
	})
}

// TestEliminatedImposterAlsoBecomesASpectator — "Every eliminated player
// becomes a silent spectator, regardless of the Elimination results setting."
// The base design deliberately never sent this to the imposter, because there
// the imposter going out ended the match. With two, the first one caught is a
// spectator like anybody else — and the one thing they did not already know is
// who they had been sharing a word with.
func TestEliminatedImposterAlsoBecomesASpectator(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarnessWithDeck(t, 6,
			mkImposterSettings(3, 5, 30, 2, genpb.EliminationResults_ELIMINATION_RESULTS_HIDDEN),
			7113, roundDeck())
		defer h.stop()
		h.start()
		h.toDiscussion()

		ids := h.imposters()
		caught, partner := ids[0], ids[1]
		seat := h.indexOf(caught)

		h.discard()
		gangUp(h, caught)

		spec := allSpectatorInfo(h.drain(seat))
		if len(spec) != 1 {
			t.Fatalf("the eliminated imposter received %d dossiers, want 1", len(spec))
		}
		found := false
		for _, im := range spec[0].GetImposters() {
			if im.GetPlayerId() == partner {
				found = true
			}
		}
		if !found {
			t.Fatalf("the eliminated imposter's dossier does not name their partner %q", partner)
		}
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// roundDeck deals a distinct pair every round, so a test about what changes
// between them has something that does. seqDeck already is that deck; this is
// just a name for "a fresh one, per harness".
func roundDeck() Deck { return &seqDeck{} }

// drawOneStroke commits a single stroke for the current artist, so a round has
// a canvas the dossier can carry.
func drawOneStroke(h *harness, seat int) {
	h.t.Helper()
	h.send(seat, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeBegin{
		StrokeBegin: &genpb.StrokeBegin{ColorIndex: 1, Width: 4, Points: []int32{10, 10}}}})
	h.send(seat, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeEnd{
		StrokeEnd: &genpb.StrokeEnd{}}})
	synctest.Wait()
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func containsBytes(haystack []byte, needle string) bool {
	return needle != "" && bytes.Contains(haystack, []byte(needle))
}

// TestEveryPlayerCountDealsTheConfiguredImposters — MULTIPLE_IMPOSTERS.md
// validation item 1: one- and two-imposter matches at every supported player
// count. Two imposters is legal at all of them, including the three-player case
// the lobby warns about.
//
// What it pins at each size is the deal itself, which is the one thing every
// other rule is built on: the right number of DISTINCT seats, all of them
// holding the same odd word, and nobody else holding it.
func TestEveryPlayerCountDealsTheConfiguredImposters(t *testing.T) {
	t.Parallel()
	for players := MinPlayers; players <= MaxPlayers; players++ {
		for _, want := range []int32{1, 2} {
			t.Run(fmt.Sprintf("%d_players_%d_imposters", players, want), func(t *testing.T) {
				t.Parallel()
				synctest.Test(t, func(t *testing.T) {
					h := newHarness(t, players,
						mkImposterSettings(1, 5, 30, want,
							genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
						uint64(7200+players*10)+uint64(want))
					defer h.stop()
					h.start()

					ids := h.imposters()
					if len(ids) != int(want) {
						t.Fatalf("dealt %d imposters, want %d", len(ids), want)
					}
					seen := map[string]bool{}
					for _, id := range ids {
						if seen[id] {
							t.Fatalf("seat %q was chosen twice", id)
						}
						seen[id] = true
					}

					odd, common := 0, 0
					var oddWord, commonWord string
					for i := range h.ids {
						w := h.word(i)
						if seen[h.ids[i]] {
							odd++
							if oddWord != "" && w != oddWord {
								t.Fatalf("imposters hold %q and %q", oddWord, w)
							}
							oddWord = w
							continue
						}
						common++
						if commonWord != "" && w != commonWord {
							t.Fatalf("non-imposters hold %q and %q", commonWord, w)
						}
						commonWord = w
					}
					if odd != int(want) || common != players-int(want) {
						t.Fatalf("%d odd and %d common, want %d and %d",
							odd, common, want, players-int(want))
					}
					// At least one seat must be left on the common word, or
					// there is no game.
					if common == 0 {
						t.Fatal("every seat was dealt the odd word")
					}
					if oddWord == commonWord {
						t.Fatalf("both sides of the pair are %q", oddWord)
					}
				})
			})
		}
	}
}

// TestImposterSeatsAreDrawnWithoutReplacement — the partial Fisher-Yates in
// chooseImposters. Sampling twice from the same pool would deal a two-imposter
// match with one imposter in it roughly a fifth of the time at six players,
// which is exactly the kind of bug a single scripted match never shows.
//
// Also checks the draw actually moves: over many seeds, more than one pair of
// seats comes up.
func TestImposterSeatsAreDrawnWithoutReplacement(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		pairs := map[string]int{}
		for seed := range uint64(120) {
			func() {
				h := newHarness(t, 6,
					mkImposterSettings(1, 5, 30, 2,
						genpb.EliminationResults_ELIMINATION_RESULTS_REVEAL),
					seed+9000)
				defer h.stop()
				h.start()

				ids := h.imposters()
				if len(ids) != 2 {
					t.Fatalf("seed %d: dealt %d imposters, want 2", seed, len(ids))
				}
				if ids[0] == ids[1] {
					t.Fatalf("seed %d: drew the same seat twice", seed)
				}
				// Seat order on the wire, not draw order.
				if h.indexOf(ids[0]) > h.indexOf(ids[1]) {
					t.Fatalf("seed %d: imposterIDs is not in seat order: %v", seed, ids)
				}
				pairs[fmt.Sprintf("%d,%d", h.indexOf(ids[0]), h.indexOf(ids[1]))]++
			}()
		}
		// C(6,2) is 15. Insisting on all of them would be a flaky test; more
		// than a handful is enough to show the draw is not pinned.
		if len(pairs) < 8 {
			t.Fatalf("only %d distinct imposter pairs over 120 seeds: %v", len(pairs), pairs)
		}
	})
}
