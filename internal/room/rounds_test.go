package room

// rounds_test.go — every round deals a fresh pair on a blank canvas, and the
// imposter is the same seat throughout (DESIGN.md:22, DESIGN.md:36).
//
// The three properties that make this a game rather than N unrelated games:
//
//   - the words change every round,
//   - the canvas is wiped with them, so each round is argued on its own marks,
//   - the imposter does NOT change, so suspicion is the one thing that carries.
//
// The fourth is negative and lives in internal/words: no word may be dealt
// twice in a match, because a player whose word repeats while the pairing moves
// has worked out they hold the common one.

import (
	"fmt"
	"slices"
	"testing"
	"testing/synctest"

	mrand "math/rand/v2"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// seqDeck deals a distinct pair per draw and records the avoid set it was
// handed each time, so a test can assert both what came out and what the room
// asked for.
type seqDeck struct {
	n     int
	avoid [][]string
}

func (d *seqDeck) Pair(_ genpb.Difficulty, _ *mrand.Rand, avoid []string) (string, string) {
	d.avoid = append(d.avoid, slices.Clone(avoid))
	n := d.n
	d.n++
	return fmt.Sprintf("COMMON-%d", n), fmt.Sprintf("ODD-%d", n)
}

// newRoundHarness is newHarness with a deck that varies per round. It has to
// duplicate the constructor rather than take an option because the shared
// harness pins pairDeck, whose whole point is that the pair never changes.
func newRoundHarness(t *testing.T, n int, s *genpb.MatchSettings, seed uint64) (*harness, *seqDeck) {
	t.Helper()
	deck := &seqDeck{}
	h := newHarnessWithDeck(t, n, s, seed, deck)
	return h, deck
}

// TestEveryRoundDealsAFreshPair — the words change, the imposter does not.
func TestEveryRoundDealsAFreshPair(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h, deck := newRoundHarness(t, 4, mkSettings(3, 5, 30), 9001)
		defer h.stop()
		h.start()

		imposter := h.imposter()
		if imposter == "" {
			t.Fatal("no imposter was picked")
		}

		type deal struct {
			round int32
			words []string
		}
		var deals []deal

		for {
			h.toDiscussion()
			words := make([]string, len(h.ids))
			for i := range h.ids {
				words[i] = h.word(i)
			}
			deals = append(deals, deal{round: h.round(), words: words})

			// Nobody out: a plurality needs a strict lead, and all-Skip gives
			// none. The match then runs its full round count.
			h.skipAll()
			if h.phase() == genpb.Phase_PHASE_ENDED {
				break
			}
			if h.round() >= 3 {
				h.advance(ResolveDuration)
				break
			}
			h.nextRound()
		}

		if len(deals) != 3 {
			t.Fatalf("played %d rounds, want 3", len(deals))
		}
		if got := h.imposter(); got != imposter {
			t.Fatalf("the imposter changed mid-match, %q then %q", imposter, got)
		}

		// Round n's words are all-new, and every seat moved together.
		seen := map[string]int32{}
		for _, d := range deals {
			for i, w := range d.words {
				if w == "" {
					t.Fatalf("round %d: seat %d holds no word", d.round, i)
				}
				if prev, dup := seen[w]; dup && prev != d.round {
					t.Fatalf("%q was dealt in round %d and again in round %d", w, prev, d.round)
				}
				seen[w] = d.round
			}
			// Exactly one seat holds the minority word, and it is the same seat
			// every round.
			//
			// Which word that is has to be found by counting, not by name: the
			// room flips a coin for which SIDE of the drawn pair is the common
			// word (DESIGN.md:23), so the deck's own ordering means nothing here.
			counts := map[string]int{}
			for _, w := range d.words {
				counts[w]++
			}
			if len(counts) != 2 {
				t.Fatalf("round %d dealt %d distinct words, want exactly 2", d.round, len(counts))
			}
			odd := 0
			for i, w := range d.words {
				if counts[w] != 1 {
					continue
				}
				odd++
				if h.ids[i] != imposter {
					t.Fatalf("round %d: seat %d holds the minority word but %q is the imposter",
						d.round, i, imposter)
				}
			}
			if odd != 1 {
				t.Fatalf("round %d: %d seats hold the minority word, want exactly 1", d.round, odd)
			}
		}

		// The room asked the deck to avoid everything it had already dealt.
		// Round 1 has nothing to avoid; each later round carries both words of
		// every round before it.
		if len(deck.avoid) != 3 {
			t.Fatalf("the deck was drawn from %d times, want 3", len(deck.avoid))
		}
		for i, av := range deck.avoid {
			if got, want := len(av), 2*i; got != want {
				t.Fatalf("draw %d was told to avoid %d words (%v), want %d", i+1, got, av, want)
			}
		}
	})
}

// TestRoundWipesTheCanvas — each round is judged on its own marks
// (DESIGN.md:41). seq is deliberately NOT rewound with it: it stays monotonic
// for the life of the room so a client cannot mistake a new round for a gap.
func TestRoundWipesTheCanvas(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h, _ := newRoundHarness(t, 3, mkSettings(2, 5, 30), 9002)
		defer h.stop()
		h.start()

		h.toDrawing()
		h.drawOneStroke(h.artistIdx())
		if got := h.strokeCount(); got == 0 {
			t.Fatal("the stroke was not committed, so the wipe proves nothing")
		}
		seqBefore := h.seq()

		h.toDiscussion()
		if got := h.strokeCount(); got == 0 {
			t.Fatal("the canvas emptied before the round was over")
		}
		h.skipAll()
		if h.phase() == genpb.Phase_PHASE_ENDED {
			t.Fatal("the match ended after one round; this test needs two")
		}
		h.advance(ResolveDuration)

		// The reveal that opens round 2 is where the wipe happens.
		if got := h.phase(); got != genpb.Phase_PHASE_ASSIGNING {
			t.Fatalf("after the result screen, phase = %v, want ASSIGNING", got)
		}
		if got := h.strokeCount(); got != 0 {
			t.Fatalf("round 2 opened with %d strokes still on the canvas, want 0", got)
		}
		if got := h.seq(); got < seqBefore {
			t.Fatalf("seq went backwards across the round boundary, %d then %d", seqBefore, got)
		}
	})
}

// TestEveryRoundIsPrecededByItsOwnReveal — a player must be shown the new word
// before anybody draws with it, and told which round it is for.
func TestEveryRoundIsPrecededByItsOwnReveal(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h, _ := newRoundHarness(t, 3, mkSettings(2, 5, 30), 9003)
		defer h.stop()
		h.discard()
		h.start()

		// Round 1's reveal.
		assertRevealFor(t, h, 1)

		h.toDiscussion()
		h.skipAll()
		if h.phase() == genpb.Phase_PHASE_ENDED {
			t.Fatal("the match ended after one round; this test needs two")
		}
		h.discard()
		h.advance(ResolveDuration)

		// Round 2's reveal, dealt from the resolution of round 1.
		assertRevealFor(t, h, 2)
	})
}

// assertRevealFor checks that the room is sitting in a word reveal for round n
// and that every seat was just handed a word labelled with that round.
func assertRevealFor(t *testing.T, h *harness, round int32) {
	t.Helper()
	if got := h.phase(); got != genpb.Phase_PHASE_ASSIGNING {
		t.Fatalf("round %d: phase = %v, want ASSIGNING", round, got)
	}
	// The reveal that deals round n runs while the counter still reads n-1.
	if got, want := h.round(), round-1; got != want {
		t.Fatalf("during round %d's reveal, r.round = %d, want %d", round, got, want)
	}
	for i, evs := range h.drainAll() {
		var got []*genpb.YourWord
		for _, e := range evs {
			if y := e.GetYourWord(); y != nil {
				got = append(got, y)
			}
		}
		if len(got) != 1 {
			t.Fatalf("round %d: seat %d received %d YourWord frames, want exactly 1", round, i, len(got))
		}
		if got[0].GetWord() == "" {
			t.Fatalf("round %d: seat %d was dealt an empty word", round, i)
		}
		if got[0].GetRound() != round {
			t.Fatalf("round %d: seat %d was told round %d", round, i, got[0].GetRound())
		}
	}
}

// TestFinalRevealCarriesEveryRound — the reveal describes the whole match, not
// only the pair that happened to be live when it ended (DESIGN.md:75).
func TestFinalRevealCarriesEveryRound(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h, _ := newRoundHarness(t, 4, mkSettings(2, 5, 30), 9004)
		defer h.stop()
		h.start()

		// Round 1: vote out somebody who is not the imposter, so the match runs
		// on with one seat holding a stale word.
		h.toDiscussion()
		victim := h.anyIdxExcept(h.imposterIdx())
		for i := range h.ids {
			h.vote(i, h.ids[victim])
		}
		synctest.Wait()
		if !h.eliminated(victim) {
			t.Fatalf("seat %d was not eliminated, so the blank-cell case is untested", victim)
		}
		if h.phase() == genpb.Phase_PHASE_ENDED {
			t.Fatal("the match ended in round 1; this test needs two rounds")
		}
		h.advance(ResolveDuration)

		// Round 2, run to the end.
		h.discard()
		h.toDiscussion()
		h.skipAll()
		for range 4 {
			if h.phase() == genpb.Phase_PHASE_ENDED {
				break
			}
			h.advance(ResolveDuration)
		}
		if got := h.phase(); got != genpb.Phase_PHASE_ENDED {
			t.Fatalf("the match did not end, phase = %v", got)
		}

		var m *genpb.MatchEnded
		for _, e := range h.drain(0) {
			if v := e.GetMatchEnded(); v != nil {
				m = v
			}
		}
		if m == nil {
			t.Fatal("no MatchEnded was broadcast")
		}

		if got, want := len(m.GetRounds()), 2; got != want {
			t.Fatalf("the reveal carries %d rounds, want %d", got, want)
		}
		if got, want := int32(len(m.GetRounds())), m.GetRoundsPlayed(); got != want {
			t.Fatalf("len(rounds) = %d but rounds_played = %d", got, want)
		}
		for i, rw := range m.GetRounds() {
			if got, want := rw.GetRound(), int32(i+1); got != want {
				t.Fatalf("rounds[%d] is numbered %d, want %d", i, got, want)
			}
			if rw.GetCommonWord() == "" || rw.GetImposterWord() == "" {
				t.Fatalf("round %d did not reveal both words", rw.GetRound())
			}
			if rw.GetCommonWord() == rw.GetImposterWord() {
				t.Fatalf("round %d revealed the same word twice", rw.GetRound())
			}
		}
		// The headline is the final round's pair.
		last := m.GetRounds()[len(m.GetRounds())-1]
		if m.GetCommonWord() != last.GetCommonWord() || m.GetImposterWord() != last.GetImposterWord() {
			t.Fatal("the headline pair is not the final round's")
		}

		// Every row carries one cell per round, on the right side of the pair.
		// The seat voted out in round 1 has a word for round 1 and a blank for
		// round 2 — it was not dealt into a round it had already left.
		victimID := h.ids[victim]
		sawVictim := false
		for _, rv := range m.GetReveals() {
			if got, want := len(rv.GetWords()), len(m.GetRounds()); got != want {
				t.Fatalf("player %q has %d per-round words, want %d", rv.GetPlayerId(), got, want)
			}
			for i, w := range rv.GetWords() {
				rw := m.GetRounds()[i]
				want := rw.GetCommonWord()
				if rv.GetWasImposter() {
					want = rw.GetImposterWord()
				}
				if rv.GetPlayerId() == victimID && i == 1 {
					want = "" // eliminated in round 1, not dealt into round 2
				}
				if w != want {
					t.Fatalf("player %q round %d word = %q, want %q",
						rv.GetPlayerId(), rw.GetRound(), w, want)
				}
			}
			if rv.GetPlayerId() == victimID {
				sawVictim = true
				if !rv.GetEliminated() {
					t.Fatal("the eliminated seat is not marked eliminated in the reveal")
				}
				// PlayerReveal.word is the last round this seat was dealt into,
				// which for them is round 1 — not the final round's word.
				if got, want := rv.GetWord(), m.GetRounds()[0].GetCommonWord(); got != want {
					t.Fatalf("the eliminated seat is revealed holding %q, want %q", got, want)
				}
			}
		}
		if !sawVictim {
			t.Fatal("the eliminated seat has no row in the reveal")
		}
	})
}
