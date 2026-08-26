// Package words holds the curated word clusters a match draws its pairs from.
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
// The catalogue is authored as clusters rather than pairs. A cluster is
// ClusterSize words that are mutually confusable *at one difficulty*, so every
// one of its C(5,2)=10 combinations is a legitimate pair of that tier. That is
// the whole reason difficulty stays meaningful: a cluster belongs to exactly
// one tier and is calibrated as a set, so no combination of its members can
// come out easier or harder than the tier the host chose. Authoring five
// mutually confusable words instead of two multiplies the deck tenfold for
// rather less than tenfold the work.
//
// Clusters are expanded into pairs once, at package init, and everything below
// that point still deals in pairs — the draw machinery, the no-repeat
// guarantee and the public API are unchanged by the clustering.
//
// The catalogue is immutable. Draw state lives in a Deck, which is what the
// room holds.
package words

import (
	mrand "math/rand/v2"
	"sync"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// ClusterSize is how many words a catalogue cluster holds. Five is a
// deliberate balance: C(5,2) is ten pairs, which is a large multiplier, while
// five words is still few enough that a human can check every combination by
// eye when calibrating the tier. Six would be fifteen combinations and the
// weakest of them would start to drift out of tier.
const ClusterSize = 5

// Cluster is a set of mutually confusable words at a single difficulty.
//
// Every unordered combination of its members is a valid pair of that tier.
// Membership is unordered and carries no meaning; see Pair on why nothing here
// may bias which side becomes the imposter word.
type Cluster []string

// Pair is one word pair, expanded from a cluster.
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
	// origin[i] is the index of the cluster pairs[i] was expanded from.
	// Pairs of one cluster share four words out of five with each other, so
	// serving two of them in a row reads as a repeat even though the pair is
	// new; draw uses this to avoid that.
	origin []int
	drawn  []bool
	left   int
	// lastOrigin is the cluster most recently drawn from, -1 before the first
	// draw. Tracking the cluster rather than the pair subsumes the older
	// "never the same pair twice running" guarantee, because a pair is always
	// in its own cluster.
	lastOrigin int
}

// Compile-time proof that *Deck still matches room.Deck. Spelled structurally
// rather than by importing internal/room, which would tie this leaf package to
// the game loop for no gain.
var _ interface {
	Pair(difficulty genpb.Difficulty, rnd *mrand.Rand) (a, b string)
} = (*Deck)(nil)

// New returns a Deck over the curated catalogue with nothing drawn yet.
func New() *Deck { return newDeck(easyClusters, mediumClusters, hardClusters) }

func newDeck(easy, medium, hard []Cluster) *Deck {
	return &Deck{tiers: map[genpb.Difficulty]*tier{
		genpb.Difficulty_DIFFICULTY_EASY:   newTier(easy),
		genpb.Difficulty_DIFFICULTY_MEDIUM: newTier(medium),
		genpb.Difficulty_DIFFICULTY_HARD:   newTier(hard),
	}}
}

func newTier(clusters []Cluster) *tier {
	pairs, origin := expand(clusters)
	if len(pairs) == 0 {
		panic("words: empty difficulty tier")
	}
	return &tier{
		pairs:      pairs,
		origin:     origin,
		drawn:      make([]bool, len(pairs)),
		left:       len(pairs),
		lastOrigin: -1,
	}
}

// expand turns clusters into every unordered pair, remembering which cluster
// each pair came from. Order is deterministic, which is what lets a seeded
// generator replay a room's draws exactly.
func expand(clusters []Cluster) (pairs []Pair, origin []int) {
	for c, cl := range clusters {
		for i := 0; i < len(cl); i++ {
			for j := i + 1; j < len(cl); j++ {
				pairs = append(pairs, Pair{cl[i], cl[j]})
				origin = append(origin, c)
			}
		}
	}
	return pairs, origin
}

// Pair returns two distinct, related words from the requested difficulty deck.
// The two words come back in catalogue order, which carries no meaning: the
// caller decides which side becomes the common word.
//
// All randomness is drawn from rnd, so a room with a seeded generator replays
// its draws exactly. A pair never repeats until every pair of that difficulty
// has been handed out; the tier then recycles, and consecutive draws never
// come from the same cluster while any other cluster still has a pair left.
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
		t.lastOrigin = -1
	}
}

func (t *tier) draw(rnd *mrand.Rand) Pair {
	if t.left == 0 {
		t.recycle()
	}
	// Prefer any cluster other than the one just served. A single-cluster tier,
	// or a recycle that leaves only that cluster standing, has nothing else to
	// offer: yield rather than loop.
	avoid := t.lastOrigin
	n := t.eligible(avoid)
	if n == 0 {
		avoid = -1
		n = t.left
	}

	k := rnd.IntN(n)
	idx := -1
	for i, used := range t.drawn {
		if used || (avoid >= 0 && t.origin[i] == avoid) {
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
	t.lastOrigin = t.origin[idx]
	return t.pairs[idx]
}

// eligible counts the undrawn pairs that did not come from cluster avoid.
func (t *tier) eligible(avoid int) int {
	n := 0
	for i, used := range t.drawn {
		if used || (avoid >= 0 && t.origin[i] == avoid) {
			continue
		}
		n++
	}
	return n
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

// Pairs returns a copy of one difficulty's expanded catalogue. Callers may not
// mutate the catalogue itself, hence the copy.
func Pairs(difficulty genpb.Difficulty) []Pair {
	src := catalogue(Normalize(difficulty))
	out := make([]Pair, len(src))
	copy(out, src)
	return out
}

// Count reports how many pairs a difficulty's expanded catalogue holds.
func Count(difficulty genpb.Difficulty) int { return len(catalogue(Normalize(difficulty))) }

// Clusters returns a copy of one difficulty's authored clusters. The clusters
// themselves are shared, so a caller must not write through the elements.
func Clusters(difficulty genpb.Difficulty) []Cluster {
	src := clusterCatalogue(Normalize(difficulty))
	out := make([]Cluster, len(src))
	copy(out, src)
	return out
}

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

func clusterCatalogue(d genpb.Difficulty) []Cluster {
	switch d {
	case genpb.Difficulty_DIFFICULTY_EASY:
		return easyClusters
	case genpb.Difficulty_DIFFICULTY_HARD:
		return hardClusters
	default:
		return mediumClusters
	}
}

// The expanded decks. Package-level so Pairs and Count stay O(1) lookups and
// the expansion cost is paid once.
var (
	easyPairs, _   = expand(easyClusters)
	mediumPairs, _ = expand(mediumClusters)
	hardPairs, _   = expand(hardClusters)
)

// ---------------------------------------------------------------------------
// The catalogue
//
// Curation rules (DESIGN.md "Word-Deck Curation Rules"), all enforced by
// words_test.go as far as a machine can check them:
//
//   - every cluster holds exactly ClusterSize words, and every one of its ten
//     combinations is a fair pair for that tier — this is the rule that keeps
//     the host's difficulty choice honest, and it is the only one a machine
//     cannot check;
//   - no word needs text, flags, logos, brand names or specialist knowledge;
//   - no word appears in two clusters of the same deck — an overlapping word
//     leaks information from one match into the next. Sharing words *within* a
//     cluster is not overlap, it is the point;
//   - no pair is duplicated in either direction, within or across decks;
//   - every word is a concrete drawable noun or a depictable activity.
//
// The first eight clusters of each deck are built around the worked examples
// from DESIGN.md, which set the calibration for everything after them.
// ---------------------------------------------------------------------------

// easyClusters: any two members look plainly different on the canvas. A
// cautious artist can still draw something generic enough to hide behind.
var easyClusters = []Cluster{
	{"Cat", "Dog", "Rabbit", "Goldfish", "Hamster"},
	{"Pizza", "Burger", "Hot Dog", "Taco", "Doughnut"},
	{"Train", "Bus", "Car", "Tractor", "Van"},
	{"Bicycle", "Motorcycle", "Scooter", "Skateboard", "Unicycle"},
	{"Shark", "Dolphin", "Whale", "Crab", "Jellyfish"},
	{"Apple", "Orange", "Banana", "Grapes", "Pineapple"},
	{"Doctor", "Firefighter", "Soldier", "Astronaut", "Farmer"},
	{"Guitar", "Piano", "Drum", "Trumpet", "Harp"},

	{"Sun", "Moon", "Star", "Cloud", "Rainbow"},
	{"Tree", "Flower", "Cactus", "Fern", "Mushroom"},
	{"House", "Tent", "Castle", "Igloo", "Lighthouse"},
	{"Boat", "Airplane", "Rocket", "Submarine", "Helicopter"},
	{"Turtle", "Snail", "Frog", "Hedgehog", "Caterpillar"},
	{"Hat", "Shoe", "Sock", "Glove", "Belt"},
	{"Television", "Camera", "Telephone", "Radio", "Lamp"},
	{"Kettle", "Frying Pan", "Whisk", "Grater", "Rolling Pin"},
	{"Hammer", "Screwdriver", "Saw", "Drill", "Axe"},
	{"Snowflake", "Raindrop", "Tornado", "Icicle", "Puddle"},
	{"Basketball", "Tennis Racket", "Skis", "Boxing Glove", "Surfboard"},
	{"Owl", "Peacock", "Penguin", "Flamingo", "Woodpecker"},
	{"Horse", "Elephant", "Lion", "Kangaroo", "Monkey"},
	{"Spider", "Dragonfly", "Ant", "Ladybug", "Worm"},
	{"Carrot", "Pumpkin", "Corn", "Broccoli", "Onion"},
	{"Ice Cream", "Cake", "Lollipop", "Cupcake", "Popcorn"},
	{"Chair", "Ladder", "Bookshelf", "Bed", "Mirror"},
	{"Key", "Padlock", "Anchor", "Horseshoe", "Bell"},
	{"Torch", "Chandelier", "Streetlight", "Fireworks", "Flashlight"},
	{"Island", "Desert", "Forest", "Mountain", "Waterfall"},
	{"Hospital", "Skyscraper", "Stadium", "Barn", "Windmill"},
	{"Raft", "Jet Ski", "Ferry", "Gondola", "Rowboat"},
	{"Backpack", "Suitcase", "Basket", "Bucket", "Wheelbarrow"},
	{"Snowman", "Sled", "Ice Skate", "Mitten", "Snow Globe"},
	{"Watering Can", "Rake", "Shovel", "Flowerpot", "Birdhouse"},
	{"Kite", "Balloon", "Teddy Bear", "Yo Yo", "Jigsaw Puzzle"},
	{"Crocodile", "Snake", "Lizard", "Scorpion", "Centipede"},
	{"Cow", "Chicken", "Sheep", "Pig", "Duck"},
	{"Toothbrush", "Comb", "Soap", "Razor", "Hairdryer"},
	{"Pencil", "Scissors", "Stapler", "Paperclip", "Eraser"},
	{"Bridge", "Tunnel", "Fence", "Staircase", "Well"},
	{"Crown", "Necklace", "Sword", "Shield", "Trophy"},
}

// mediumClusters: any two members share several visual clues. It takes more
// than one drawing, and usually some discussion, to tell them apart.
var mediumClusters = []Cluster{
	{"Lion", "Giraffe", "Zebra", "Antelope", "Warthog"},
	{"Beach", "Desert", "Dune", "Oasis", "Canyon"},
	{"Forest", "Jungle", "Orchard", "Vineyard", "Bamboo Grove"},
	{"Mountain", "Volcano", "Hill", "Cliff", "Glacier"},
	{"Hospital", "School", "Courthouse", "Fire Station", "Post Office"},
	{"Castle", "Palace", "Fortress", "Mansion", "Temple"},
	{"Swimming", "Surfing", "Diving", "Rowing", "Water Polo"},
	{"Camera", "Mobile Phone", "Tablet", "Camcorder", "Binoculars"},

	{"Coffee", "Tea", "Hot Chocolate", "Milkshake", "Smoothie"},
	{"Sandwich", "Taco", "Burrito", "Spring Roll", "Samosa"},
	{"Farm", "Zoo", "Aquarium", "Stable", "Kennel"},
	{"Violin", "Banjo", "Cello", "Harmonica", "Accordion"},
	{"Helicopter", "Drone", "Glider", "Blimp", "Jet"},
	{"Sailboat", "Canoe", "Kayak", "Speedboat", "Houseboat"},
	{"Suitcase", "Duffel Bag", "Handbag", "Trunk", "Tote Bag"},
	{"Wedding", "Birthday Party", "Graduation", "Parade", "Picnic"},
	{"Skiing", "Sledding", "Snowboarding", "Ice Skating", "Snowshoeing"},
	{"Fireplace", "Campfire", "Barbecue Grill", "Furnace", "Stove"},
	{"Waterfall", "Fountain", "Geyser", "Sprinkler", "Rapids"},
	{"Barn", "Cottage", "Cabin", "Watermill", "Greenhouse"},
	{"Telescope", "Microscope", "Magnifying Glass", "Periscope", "Kaleidoscope"},
	{"Watch", "Compass", "Pocket Knife", "Lighter", "Keyring"},
	{"Cupcake", "Doughnut", "Croissant", "Pretzel", "Cookie"},
	{"Sushi", "Dumpling", "Noodles", "Rice Bowl", "Curry"},
	{"Bee", "Ladybug", "Beetle", "Wasp", "Grasshopper"},
	{"Squirrel", "Hedgehog", "Chipmunk", "Mole", "Porcupine"},
	{"Ambulance", "Fire Truck", "Police Car", "Tow Truck", "Garbage Truck"},
	{"Police Officer", "Security Guard", "Pilot", "Sailor", "Referee"},
	{"Diver", "Beekeeper", "Welder", "Skydiver", "Mountaineer"},
	{"Bucket", "Crate", "Barrel", "Jar", "Tin"},
	{"Kitchen", "Dining Room", "Living Room", "Bedroom", "Study"},
	{"Bookshelf", "Cupboard", "Dresser", "Desk", "Nightstand"},
	{"Stadium", "Theater", "Cinema", "Circus Tent", "Bandstand"},
	{"Playground", "Gym", "Skate Park", "Swimming Pool", "Bowling Alley"},
	{"Screwdriver", "Wrench", "Pliers", "Chisel", "Clamp"},
	{"Paintbrush", "Broom", "Mop", "Feather Duster", "Squeegee"},
	{"Rhinoceros", "Hippopotamus", "Buffalo", "Walrus", "Manatee"},
	{"Cheese", "Butter", "Yogurt", "Cream", "Honey"},
	{"Watermelon", "Pumpkin", "Cabbage", "Coconut", "Gourd"},
	{"Mailbox", "Trash Can", "Fire Hydrant", "Parking Meter", "Bus Stop"},
}

// hardClusters: substantial overlap. Almost every honest drawing fits every
// member, which is the point — but a deliberate detail still separates them,
// so the group can win without guessing.
var hardClusters = []Cluster{
	{"Library", "Bookstore", "Archive", "Reading Room", "Study Hall"},
	{"Restaurant", "Café", "Diner", "Bistro", "Canteen"},
	{"Camping", "Hiking", "Fishing", "Birdwatching", "Foraging"},
	{"Airport", "Train Station", "Bus Terminal", "Ferry Terminal", "Subway Station"},
	{"River", "Lake", "Stream", "Creek", "Canal"},
	{"Rain", "Snow", "Sleet", "Hail", "Drizzle"},
	{"Circus", "Carnival", "Fairground", "Amusement Park", "Street Fair"},
	{"Museum", "Art Gallery", "Exhibition Hall", "Showroom", "Planetarium"},

	{"Pancake", "Waffle", "Crepe", "Omelette", "Tortilla"},
	{"Chef", "Baker", "Butcher", "Barista", "Waiter"},
	{"Wolf", "Fox", "Coyote", "Jackal", "Dingo"},
	{"Duck", "Goose", "Swan", "Pelican", "Heron"},
	{"Parrot", "Toucan", "Macaw", "Cockatoo", "Lorikeet"},
	{"Saxophone", "Clarinet", "Oboe", "Flute", "Bassoon"},
	{"Tennis", "Badminton", "Squash", "Table Tennis", "Racquetball"},
	{"Pond", "Swamp", "Marsh", "Bog", "Lagoon"},
	{"Cave", "Tunnel", "Grotto", "Mineshaft", "Burrow"},
	{"Grocery Store", "Farmers Market", "Supermarket", "Corner Shop", "Bazaar"},
	{"Hotel", "Apartment Building", "Motel", "Hostel", "Dormitory"},
	{"Office", "Classroom", "Meeting Room", "Lecture Hall", "Cubicle"},
	{"Bathroom", "Laundry Room", "Washroom", "Utility Room", "Pantry"},
	{"Lobster", "Shrimp", "Crayfish", "Prawn", "Krill"},
	{"Bench", "Sofa", "Loveseat", "Settee", "Futon"},
	{"Mug", "Teapot", "Pitcher", "Jug", "Carafe"},
	{"Briefcase", "Toolbox", "Satchel", "Portfolio", "Attaché Case"},
	{"Wallet", "Purse", "Pouch", "Coin Purse", "Billfold"},
	{"Ring", "Bracelet", "Bangle", "Anklet", "Cufflink"},
	{"Curtain", "Blanket", "Quilt", "Throw", "Tapestry"},
	{"Scarf", "Necktie", "Bow Tie", "Cravat", "Shawl"},
	{"Boots", "Sandals", "Loafers", "Clogs", "Moccasins"},
	{"Jacket", "Sweater", "Cardigan", "Hoodie", "Blazer"},
	{"Baseball Cap", "Helmet", "Beanie", "Beret", "Bonnet"},
	{"Traffic Light", "Street Lamp", "Signpost", "Utility Pole", "Flagpole"},
	{"Octopus", "Squid", "Cuttlefish", "Nautilus", "Sea Anemone"},
	{"Fence", "Gate", "Railing", "Hedge", "Turnstile"},
	{"Candle", "Lantern", "Oil Lamp", "Tea Light", "Candlestick"},
	{"Corn", "Wheat", "Barley", "Oats", "Rye"},
	{"Sunflower", "Daisy", "Marigold", "Chrysanthemum", "Dandelion"},
	{"Sheep", "Goat", "Llama", "Camel", "Alpaca"},
	{"Otter", "Beaver", "Muskrat", "Mink", "Platypus"},
}
