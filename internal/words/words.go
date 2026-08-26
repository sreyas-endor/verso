// Package words holds the curated word-pair decks a match draws from.
//
// Difficulty here is a measure of visual overlap, not of vocabulary
// (DESIGN.md "Word-Pair Difficulty"). Every word is a concrete object or a
// depictable activity that a player can draw literally inside one short
// freehand turn — no text, letters, numbers, arrows or symbols, no flags,
// logos, brand names or specialist knowledge.
//
//   - Easy   — a clear visual difference; a generic drawing can still hide you.
//   - Medium — several shared visual clues; needs several drawings and discussion.
//   - Hard   — substantial overlap, rewards deliberate ambiguity, still solvable.
//
// The catalogue is immutable. Draw state lives in a Deck, which is what the
// room holds.
package words

import (
	mrand "math/rand/v2"
	"sync"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// Pair is one word pair from the catalogue.
//
// A Pair is *unordered*: neither side is "the common word" and neither side is
// "the imposter word". The room performs its own coin flip to decide which side
// every player but one receives (DESIGN.md:33), so nothing in this package may
// ever bias that choice. A and B are only storage slots.
type Pair struct{ A, B string }

// String renders a pair for test failure messages. It is not a wire format.
func (p Pair) String() string { return p.A + " / " + p.B }

// Deck draws pairs without replacement. It satisfies the room's Deck
// interface.
//
// Give each room its own Deck: a room that carries a seeded generator then
// replays its draws exactly, and one room's history cannot shorten another's.
// A shared Deck is still safe — every method takes the mutex — but its draw
// history is then global and seeded replay no longer holds.
type Deck struct {
	mu    sync.Mutex
	tiers map[genpb.Difficulty]*tier
}

type tier struct {
	pairs []Pair
	drawn []bool
	left  int
	last  int // index most recently handed out, -1 before the first draw
}

// Compile-time proof that *Deck still matches room.Deck. Spelled structurally
// rather than by importing internal/room, which would tie this leaf package to
// the game loop for no gain.
var _ interface {
	Pair(difficulty genpb.Difficulty, rnd *mrand.Rand) (a, b string)
} = (*Deck)(nil)

// New returns a Deck over the curated catalogue with nothing drawn yet.
func New() *Deck { return newDeck(easyPairs, mediumPairs, hardPairs) }

func newDeck(easy, medium, hard []Pair) *Deck {
	d := &Deck{tiers: map[genpb.Difficulty]*tier{
		genpb.Difficulty_DIFFICULTY_EASY:   newTier(easy),
		genpb.Difficulty_DIFFICULTY_MEDIUM: newTier(medium),
		genpb.Difficulty_DIFFICULTY_HARD:   newTier(hard),
	}}
	return d
}

func newTier(pairs []Pair) *tier {
	if len(pairs) == 0 {
		panic("words: empty difficulty tier")
	}
	return &tier{pairs: pairs, drawn: make([]bool, len(pairs)), left: len(pairs), last: -1}
}

// Pair returns two distinct, related words from the requested difficulty deck.
// The two words come back in catalogue order, which carries no meaning: the
// caller decides which side becomes the common word.
//
// All randomness is drawn from rnd, so a room with a seeded generator replays
// its draws exactly. A pair never repeats until every pair of that difficulty
// has been handed out; the tier then recycles, and the wrap-around draw still
// avoids repeating the pair that came immediately before it.
//
// An unrecognised difficulty falls back to DIFFICULTY_MEDIUM.
func (d *Deck) Pair(difficulty genpb.Difficulty, rnd *mrand.Rand) (a, b string) {
	if rnd == nil {
		rnd = mrand.New(mrand.NewPCG(mrand.Uint64(), mrand.Uint64()))
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.tiers[Normalize(difficulty)].draw(rnd)
	return p.A, p.B
}

// Remaining reports how many pairs of that difficulty have not been handed out
// since the last exhaustion or Reset.
func (d *Deck) Remaining(difficulty genpb.Difficulty) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tiers[Normalize(difficulty)].left
}

// Reset forgets the draw history of every tier.
func (d *Deck) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, t := range d.tiers {
		t.recycle()
		t.last = -1
	}
}

func (t *tier) draw(rnd *mrand.Rand) Pair {
	if t.left == 0 {
		t.recycle()
	}
	// After a recycle the previous pair is available again; skip it so a long
	// session never serves the same pair back to back.
	avoid := -1
	if t.last >= 0 && !t.drawn[t.last] && t.left > 1 {
		avoid = t.last
	}
	n := t.left
	if avoid >= 0 {
		n--
	}
	k := rnd.IntN(n)
	idx := -1
	for i, used := range t.drawn {
		if used || i == avoid {
			continue
		}
		if k == 0 {
			idx = i
			break
		}
		k--
	}
	t.drawn[idx] = true
	t.left--
	t.last = idx
	return t.pairs[idx]
}

func (t *tier) recycle() {
	for i := range t.drawn {
		t.drawn[i] = false
	}
	t.left = len(t.pairs)
}

// Normalize maps any difficulty the client may send onto a real deck.
// Unspecified and out-of-range values become DIFFICULTY_MEDIUM, the documented
// default (DESIGN.md "Initial Balance Defaults").
func Normalize(d genpb.Difficulty) genpb.Difficulty {
	switch d {
	case genpb.Difficulty_DIFFICULTY_EASY, genpb.Difficulty_DIFFICULTY_HARD:
		return d
	default:
		return genpb.Difficulty_DIFFICULTY_MEDIUM
	}
}

// Pairs returns a copy of one difficulty's catalogue. Callers may not mutate
// the catalogue itself, hence the copy.
func Pairs(difficulty genpb.Difficulty) []Pair {
	src := catalogue(Normalize(difficulty))
	out := make([]Pair, len(src))
	copy(out, src)
	return out
}

// Count reports how many pairs a difficulty's catalogue holds.
func Count(difficulty genpb.Difficulty) int { return len(catalogue(Normalize(difficulty))) }

func catalogue(d genpb.Difficulty) []Pair {
	switch d {
	case genpb.Difficulty_DIFFICULTY_EASY:
		return easyPairs
	case genpb.Difficulty_DIFFICULTY_HARD:
		return hardPairs
	default:
		return mediumPairs
	}
}

// ---------------------------------------------------------------------------
// The catalogue
//
// Curation rules (DESIGN.md "Word-Deck Curation Rules"), all enforced by
// words_test.go as far as a machine can check them:
//
//   - no pair is nearly identical, and no pair is unrelated;
//   - no word needs text, flags, logos, brand names or specialist knowledge;
//   - no word appears in two pairs of the same deck — an overlapping word
//     leaks information from one match into the next;
//   - no pair is duplicated in either direction, within or across decks;
//   - every word is a concrete drawable noun or a depictable activity.
//
// The first eight pairs of each deck are the worked examples from DESIGN.md
// and set the calibration for everything after them.
// ---------------------------------------------------------------------------

// easyPairs: the two words look plainly different on the canvas. A cautious
// artist can still draw something generic enough to hide behind.
var easyPairs = []Pair{
	{"Cat", "Dog"},
	{"Pizza", "Burger"},
	{"Train", "Bus"},
	{"Bicycle", "Motorcycle"},
	{"Shark", "Dolphin"},
	{"Apple", "Orange"},
	{"Doctor", "Firefighter"},
	{"Guitar", "Piano"},
	{"Sun", "Moon"},
	{"Tree", "Flower"},
	{"House", "Tent"},
	{"Boat", "Airplane"},
	{"Rabbit", "Turtle"},
	{"Hat", "Shoe"},
	{"Book", "Television"},
	{"Clock", "Hourglass"},
	{"Snowman", "Scarecrow"},
	{"Umbrella", "Kite"},
	{"Horse", "Elephant"},
	{"Spider", "Dragonfly"},
	{"Carrot", "Mushroom"},
	{"Ice Cream", "Cake"},
	{"Chair", "Ladder"},
	{"Key", "Hammer"},
	{"Balloon", "Lantern"},
	{"Cactus", "Fern"},
	{"Rocket", "Submarine"},
	{"Windmill", "Lighthouse"},
	{"Bridge", "Tunnel"},
	{"Sword", "Shield"},
	{"Crown", "Necklace"},
	{"Drum", "Trumpet"},
	{"Snail", "Frog"},
	{"Owl", "Peacock"},
	{"Banana", "Grapes"},
	{"Pencil", "Scissors"},
	{"Lamp", "Kettle"},
	{"Car", "Tractor"},
	{"Whale", "Crab"},
	{"Cow", "Chicken"},
	{"Sock", "Glove"},
	{"Toothbrush", "Comb"},
	{"Basketball", "Skateboard"},
	{"Lion", "Giraffe"},
}

// mediumPairs: the two words share several visual clues — the same setting,
// the same silhouette, the same props — so one drawing rarely settles it.
var mediumPairs = []Pair{
	{"Beach", "Desert"},
	{"Forest", "Jungle"},
	{"Mountain", "Volcano"},
	{"Hospital", "School"},
	{"Castle", "Palace"},
	{"Swimming", "Surfing"},
	{"Camera", "Phone"},
	{"Coffee", "Tea"},
	{"Sandwich", "Taco"},
	{"Farm", "Zoo"},
	{"Violin", "Banjo"},
	{"Helicopter", "Drone"},
	{"Sailboat", "Canoe"},
	{"Backpack", "Suitcase"},
	{"Wedding", "Birthday Party"},
	{"Skiing", "Sledding"},
	{"Fireplace", "Campfire"},
	{"Waterfall", "Fountain"},
	{"Barn", "Cottage"},
	{"Telescope", "Microscope"},
	{"Watch", "Compass"},
	{"Cupcake", "Doughnut"},
	{"Sushi", "Dumpling"},
	{"Bee", "Ladybug"},
	{"Squirrel", "Hedgehog"},
	{"Tiger", "Zebra"},
	{"Crocodile", "Snake"},
	{"Ambulance", "Fire Truck"},
	{"Police Officer", "Soldier"},
	{"Astronaut", "Diver"},
	{"Basket", "Bucket"},
	{"Kitchen", "Dining Room"},
	{"Bookshelf", "Cupboard"},
	{"Stadium", "Theater"},
	{"Playground", "Gym"},
	{"Screwdriver", "Wrench"},
	{"Paintbrush", "Broom"},
	{"Rhinoceros", "Hippopotamus"},
	{"Cheese", "Butter"},
	{"Watermelon", "Pumpkin"},
	{"Mailbox", "Trash Can"},
	{"Snowflake", "Star"},
	{"Hot Dog", "Burrito"},
	{"Running", "Cycling"},
}

// hardPairs: substantial overlap. Almost every honest drawing fits both words,
// which is the point — but a deliberate detail still separates them, so the
// group can win without guessing.
var hardPairs = []Pair{
	{"Library", "Bookstore"},
	{"Restaurant", "Café"},
	{"Camping", "Hiking"},
	{"Airport", "Train Station"},
	{"River", "Lake"},
	{"Rain", "Snow"},
	{"Circus", "Carnival"},
	{"Museum", "Art Gallery"},
	{"Pancake", "Waffle"},
	{"Chef", "Baker"},
	{"Wolf", "Fox"},
	{"Duck", "Goose"},
	{"Butterfly", "Moth"},
	{"Saxophone", "Clarinet"},
	{"Tennis", "Badminton"},
	{"Pond", "Swamp"},
	{"Cave", "Tunnel"},
	{"Grocery Store", "Farmers Market"},
	{"Hotel", "Apartment Building"},
	{"Office", "Classroom"},
	{"Bathroom", "Laundry Room"},
	{"Garage", "Shed"},
	{"Bench", "Sofa"},
	{"Mug", "Teapot"},
	{"Briefcase", "Toolbox"},
	{"Wallet", "Purse"},
	{"Ring", "Bracelet"},
	{"Curtain", "Blanket"},
	{"Scarf", "Necktie"},
	{"Boots", "Sandals"},
	{"Jacket", "Sweater"},
	{"Baseball Cap", "Helmet"},
	{"Traffic Light", "Street Lamp"},
	{"Water Tower", "Silo"},
	{"Fence", "Gate"},
	{"Staircase", "Escalator"},
	{"Candle", "Lantern"},
	{"Mop", "Rake"},
	{"Corn", "Wheat"},
	{"Sunflower", "Daisy"},
	{"Sheep", "Goat"},
	{"Camel", "Llama"},
	{"Otter", "Beaver"},
	{"Parrot", "Toucan"},
	{"Lobster", "Shrimp"},
	{"Octopus", "Squid"},
}
