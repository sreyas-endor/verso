package main

// deck.go — the deck the driver deals from.
//
// The real deck (internal/words) is right for a real match and wrong for a leak
// hunt: "Cat" is three bytes, and three arbitrary bytes turn up inside a packed
// array of zigzag varints often enough to make a byte-level search flaky. The
// canary deck deals long, distinctive, ASCII-only words instead, so a hit is a
// leak and never a coincidence — and so the full-frame scan in watchdog.go,
// stroke coordinates included, is worth running at all.
//
// It implements room.Deck, so the server under test is the real server: the
// swap happens at registry.Config.NewDeck and nowhere else.

import (
	"fmt"
	"sync"

	mrand "math/rand/v2"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/room"
)

// CanaryDeck deals a fresh, unique, easily-searched pair every time it is
// asked. One instance per room, matching how registry.Config.NewDeck is used.
type CanaryDeck struct {
	mu sync.Mutex
	n  int
	// Prefix distinguishes one table's canaries from another's when several
	// matches run in parallel.
	Prefix string
}

// NewCanaryDeck builds a deck whose words are impossible to mistake for
// anything else on the wire.
func NewCanaryDeck(prefix string) *CanaryDeck {
	if prefix == "" {
		prefix = "CANARY"
	}
	return &CanaryDeck{Prefix: prefix}
}

// Pair implements room.Deck. The difficulty tier is echoed into the word so a
// misrouted deck tier would be visible in the reveal.
func (d *CanaryDeck) Pair(difficulty genpb.Difficulty, rnd *mrand.Rand) (string, string) {
	d.mu.Lock()
	n := d.n
	d.n++
	d.mu.Unlock()

	tier := "MED"
	switch difficulty {
	case genpb.Difficulty_DIFFICULTY_EASY:
		tier = "EASY"
	case genpb.Difficulty_DIFFICULTY_HARD:
		tier = "HARD"
	}
	// The two sides must be distinguishable but must not be substrings of one
	// another, or "contains" would report a false leak in either direction.
	a := fmt.Sprintf("%s-%s-ALPHA-%02d", d.Prefix, tier, n)
	b := fmt.Sprintf("%s-%s-OMEGA-%02d", d.Prefix, tier, n)
	if rnd != nil && rnd.IntN(2) == 1 {
		// The room flips the pair itself; flipping here too only proves the
		// deck may list a pair in one direction (assign.go).
		a, b = b, a
	}
	return a, b
}

var _ room.Deck = (*CanaryDeck)(nil)
