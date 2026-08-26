package words

import (
	mrand "math/rand/v2"
	"strings"
	"sync"
	"testing"
	"unicode"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// What these tests do and do not prove.
//
// Mechanically enforced below: the 40-pair floor per deck, canonical casing
// and whitespace, the drawable-character set, a word never appearing in two
// pairs of the same deck, no pair duplicated in either direction anywhere in
// the catalogue, draw-without-replacement, graceful exhaustion, seeded
// reproducibility, and the medium fallback.
//
// Left to human judgement, because no test can settle them:
//
//   - that a pair is neither nearly identical nor unrelated — the whole
//     difficulty calibration is a play-testing result (DESIGN.md asks for
//     exactly that), not a property of the strings;
//   - that a word is drawable in one short freehand turn without text;
//   - that a word is concrete rather than abstract, and common rather than
//     specialist. TestNoAbstractionsOrProperNouns is a canary for the obvious
//     regressions, not a proof.

func decks(t *testing.T) map[string][]Pair {
	t.Helper()
	return map[string][]Pair{
		"easy":   easyPairs,
		"medium": mediumPairs,
		"hard":   hardPairs,
	}
}

func TestDecksMeetMinimumSize(t *testing.T) {
	const min = 40
	for name, deck := range decks(t) {
		if len(deck) < min {
			t.Errorf("%s deck has %d pairs, want at least %d", name, len(deck), min)
		}
	}
}

func TestWordsAreWellFormed(t *testing.T) {
	for name, deck := range decks(t) {
		for _, p := range deck {
			for _, w := range []string{p.A, p.B} {
				checkWord(t, name, p, w)
			}
			if p.A == p.B {
				t.Errorf("%s: pair %v repeats the same word", name, p)
			}
			if strings.EqualFold(p.A, p.B) {
				t.Errorf("%s: pair %v differs only by case", name, p)
			}
		}
	}
}

func checkWord(t *testing.T, deck string, p Pair, w string) {
	t.Helper()
	switch {
	case w == "":
		t.Errorf("%s: pair %v has an empty word", deck, p)
		return
	case w != strings.TrimSpace(w):
		t.Errorf("%s: %q has leading or trailing whitespace", deck, w)
	case strings.Contains(w, "  "):
		t.Errorf("%s: %q has a double space", deck, w)
	}
	for _, r := range w {
		if !unicode.IsLetter(r) && r != ' ' {
			t.Errorf("%s: %q contains %q — words must be letters and spaces only, no digits, punctuation or symbols", deck, w, r)
		}
	}
	for _, tok := range strings.Split(w, " ") {
		if tok == "" {
			continue
		}
		if first := []rune(tok)[0]; !unicode.IsUpper(first) {
			t.Errorf("%s: %q is not capitalised (%q)", deck, w, tok)
		}
	}
	if strings.ToUpper(w) == w && len([]rune(w)) > 1 {
		t.Errorf("%s: %q is all caps — likely an acronym, which needs specialist knowledge", deck, w)
	}
}

// A word used by two pairs of the same deck leaks across matches: a player who
// saw it last game recognises half of this game's pair.
func TestNoWordRepeatsWithinADeck(t *testing.T) {
	for name, deck := range decks(t) {
		seen := make(map[string]Pair, len(deck)*2)
		for _, p := range deck {
			for _, w := range []string{p.A, p.B} {
				k := strings.ToLower(w)
				if prev, dup := seen[k]; dup {
					t.Errorf("%s: %q appears in both %v and %v", name, w, prev, p)
					continue
				}
				seen[k] = p
			}
		}
	}
}

func TestNoDuplicatePairsAnywhere(t *testing.T) {
	type origin struct {
		deck string
		pair Pair
	}
	seen := map[string]origin{}
	for name, deck := range decks(t) {
		for _, p := range deck {
			a, b := strings.ToLower(p.A), strings.ToLower(p.B)
			if a > b {
				a, b = b, a
			}
			k := a + "\x00" + b
			if prev, dup := seen[k]; dup {
				t.Errorf("duplicate pair %v in %s deck; already in %s deck as %v", p, name, prev.deck, prev.pair)
				continue
			}
			seen[k] = origin{name, p}
		}
	}
}

// A canary, not a proof: it catches the obvious regressions if someone extends
// a deck with an abstraction or a brand.
func TestNoAbstractionsOrProperNouns(t *testing.T) {
	banned := []string{
		"freedom", "justice", "love", "hope", "peace", "happiness", "anger",
		"time", "truth", "luck", "power", "energy", "democracy", "friendship",
		"apple inc", "coca cola", "mcdonalds", "starbucks", "nike", "google",
		"eiffel tower", "america", "france", "christmas", "halloween",
	}
	for name, deck := range decks(t) {
		for _, p := range deck {
			for _, w := range []string{p.A, p.B} {
				lower := strings.ToLower(w)
				for _, b := range banned {
					if lower == b {
						t.Errorf("%s: %q is an abstraction or proper noun; every word must be a concrete drawable noun or a depictable activity", name, w)
					}
				}
			}
		}
	}
}

func allDifficulties() []genpb.Difficulty {
	return []genpb.Difficulty{
		genpb.Difficulty_DIFFICULTY_EASY,
		genpb.Difficulty_DIFFICULTY_MEDIUM,
		genpb.Difficulty_DIFFICULTY_HARD,
	}
}

func seeded(a, b uint64) *mrand.Rand { return mrand.New(mrand.NewPCG(a, b)) }

func key(a, b string) string { return a + "\x00" + b }

func TestDrawsNeverRepeatUntilTheDeckIsSpent(t *testing.T) {
	for _, diff := range allDifficulties() {
		d := New()
		rnd := seeded(1, 2)
		n := Count(diff)
		seen := make(map[string]bool, n)
		for i := range n {
			a, b := d.Pair(diff, rnd)
			if seen[key(a, b)] {
				t.Fatalf("%v: pair %q/%q repeated on draw %d of %d", diff, a, b, i+1, n)
			}
			seen[key(a, b)] = true
			if got, want := d.Remaining(diff), n-i-1; got != want {
				t.Fatalf("%v: Remaining=%d after %d draws, want %d", diff, got, i+1, want)
			}
		}
		if len(seen) != n {
			t.Errorf("%v: %d distinct pairs over %d draws", diff, len(seen), n)
		}
	}
}

func TestExhaustionRecyclesWithoutRepeatingBackToBack(t *testing.T) {
	diff := genpb.Difficulty_DIFFICULTY_HARD
	d := New()
	rnd := seeded(7, 9)
	n := Count(diff)

	catalogued := map[string]bool{}
	for _, p := range Pairs(diff) {
		catalogued[key(p.A, p.B)] = true
	}

	var prev string
	for i := range n*3 + 5 {
		a, b := d.Pair(diff, rnd)
		k := key(a, b)
		if !catalogued[k] {
			t.Fatalf("draw %d produced %q/%q, which is not in the catalogue", i+1, a, b)
		}
		if k == prev {
			t.Errorf("draw %d repeated %q/%q back to back across the recycle", i+1, a, b)
		}
		prev = k
	}
}

func TestResetForgetsHistory(t *testing.T) {
	diff := genpb.Difficulty_DIFFICULTY_EASY
	d := New()
	rnd := seeded(3, 4)
	d.Pair(diff, rnd)
	d.Pair(diff, rnd)
	if got, want := d.Remaining(diff), Count(diff)-2; got != want {
		t.Fatalf("Remaining=%d, want %d", got, want)
	}
	d.Reset()
	if got, want := d.Remaining(diff), Count(diff); got != want {
		t.Fatalf("Remaining after Reset=%d, want %d", got, want)
	}
}

func TestSeededDrawsAreReproducible(t *testing.T) {
	const draws = 20
	run := func(seed uint64) []string {
		d := New()
		rnd := seeded(seed, seed*31+7)
		out := make([]string, 0, draws)
		for range draws {
			a, b := d.Pair(genpb.Difficulty_DIFFICULTY_MEDIUM, rnd)
			out = append(out, key(a, b))
		}
		return out
	}
	first, second := run(42), run(42)
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("same seed diverged at draw %d: %q vs %q", i+1, first[i], second[i])
		}
	}
	other := run(1337)
	same := true
	for i := range first {
		if first[i] != other[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("two different seeds produced identical draw sequences")
	}
}

func TestUnknownDifficultyFallsBackToMedium(t *testing.T) {
	medium := map[string]bool{}
	for _, p := range Pairs(genpb.Difficulty_DIFFICULTY_MEDIUM) {
		medium[key(p.A, p.B)] = true
	}
	for _, diff := range []genpb.Difficulty{
		genpb.Difficulty_DIFFICULTY_UNSPECIFIED,
		genpb.Difficulty(-3),
		genpb.Difficulty(99),
	} {
		if got := Normalize(diff); got != genpb.Difficulty_DIFFICULTY_MEDIUM {
			t.Errorf("Normalize(%v)=%v, want medium", diff, got)
		}
		d := New()
		rnd := seeded(5, 6)
		for i := range 25 {
			a, b := d.Pair(diff, rnd)
			if !medium[key(a, b)] {
				t.Fatalf("%v draw %d produced %q/%q, which is not in the medium deck", diff, i+1, a, b)
			}
		}
	}
}

// The room performs the coin flip that picks the common word, so all this
// package owes is a pair whose two sides carry no ordering meaning.
func TestPairReturnsACataloguedPairInEitherRole(t *testing.T) {
	for _, diff := range allDifficulties() {
		unordered := map[string]bool{}
		for _, p := range Pairs(diff) {
			a, b := p.A, p.B
			if a > b {
				a, b = b, a
			}
			unordered[key(a, b)] = true
		}
		d := New()
		rnd := seeded(11, 13)
		for range Count(diff) {
			a, b := d.Pair(diff, rnd)
			if a == "" || b == "" || a == b {
				t.Fatalf("%v: degenerate pair %q/%q", diff, a, b)
			}
			x, y := a, b
			if x > y {
				x, y = y, x
			}
			if !unordered[key(x, y)] {
				t.Fatalf("%v: %q/%q is not a catalogued pair", diff, a, b)
			}
		}
	}
}

// Pairs hands out a copy: a caller that mutates it must not corrupt the deck.
func TestPairsReturnsACopy(t *testing.T) {
	got := Pairs(genpb.Difficulty_DIFFICULTY_EASY)
	got[0] = Pair{"Mutated", "Ruined"}
	if easyPairs[0].A == "Mutated" {
		t.Fatal("Pairs exposed the catalogue slice")
	}
}

// A Deck shared between rooms is documented as safe. Run under -race.
func TestConcurrentDrawsAreSafe(t *testing.T) {
	d := New()
	const goroutines, draws = 8, 50
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			rnd := seeded(uint64(g)+1, 99)
			diff := allDifficulties()[g%3]
			for range draws {
				if a, b := d.Pair(diff, rnd); a == "" || b == "" {
					t.Errorf("empty word from concurrent draw")
					return
				}
				d.Remaining(diff)
			}
		}(g)
	}
	wg.Wait()
}

func TestNilRandStillDraws(t *testing.T) {
	d := New()
	a, b := d.Pair(genpb.Difficulty_DIFFICULTY_HARD, nil)
	if a == "" || b == "" || a == b {
		t.Fatalf("nil rand produced %q/%q", a, b)
	}
}

func TestNewDeckRejectsAnEmptyTier(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("newDeck accepted an empty tier")
		}
	}()
	newDeck(easyPairs, nil, hardPairs)
}

// A one-pair tier has nothing else to hand out, so the back-to-back guard must
// yield rather than deadlock or panic.
func TestSinglePairTierRecyclesForever(t *testing.T) {
	only := []Pair{{"Kite", "Balloon"}}
	d := newDeck(only, only, only)
	rnd := seeded(2, 3)
	for range 5 {
		if a, b := d.Pair(genpb.Difficulty_DIFFICULTY_EASY, rnd); a != "Kite" || b != "Balloon" {
			t.Fatalf("got %q/%q", a, b)
		}
	}
}
