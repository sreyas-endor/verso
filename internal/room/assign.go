package room

// assign.go — role assignment (DESIGN.md:22).
//
// The server draws one pair from the chosen difficulty deck, then flips a coin
// for which SIDE of that pair is the common word, then picks the configured
// number of players to receive the other side. Both draws come from r.rnd, so a
// seeded room deals the same match every time.
//
// There is one pair and one odd word however many imposters there are. Two
// imposters hold the SAME word and are told nothing about each other
// (MULTIPLE_IMPOSTERS.md, "Role Assignment"): no private channel, no shared
// tools, no count in the private reveal. They may work out that a player with
// compatible clues is holding what they are holding, the same way anybody else
// reads the canvas, and that is the whole of their coordination.
//
// The coin flip is why a deck may list a pair in one direction only: role
// assignment supplies the other direction, and players therefore cannot learn
// which word is the common one from the deck itself (DESIGN.md:172).
//
// This runs once per ROUND, not once per match. The pair is fresh every round
// and the canvas is wiped with it; the imposter seats are not re-rolled, so
// suspicion carries across rounds even though the evidence does not.

import (
	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// roundWords is one round's dealt pair, kept for the final reveal. dealt is the
// set of seats that received a word that round — everyone not yet eliminated —
// so the reveal can leave a blank rather than a word for the rounds a player
// was already out of.
//
// strokes is that round's finished canvas, kept for the spectator dossier
// (MULTIPLE_IMPOSTERS.md, "Eliminated-player Spectator View"). It is nil until
// archiveCanvas freezes it at the end of the round's drawing, and stays nil for
// a round the match ended part-way through. Only ever handed to a player who is
// already out; an active player's Snapshot carries the live canvas and nothing
// else.
type roundWords struct {
	round    int32
	common   string
	imposter string
	dealt    map[string]bool
	strokes  []*genpb.Stroke
}

// assignWords deals one round. It writes Player.word and reads it nowhere.
//
// The imposter seats are chosen only on the first deal of a match, and every
// later round hands those same seats the odd word again (DESIGN.md:24). A seat
// eliminated in an earlier round is not dealt in, so it keeps the word it went
// out holding and receives no further YourWord.
//
// No player is told their role, the full pair, or anyone else's word
// (DESIGN.md:25) — the only thing that leaves this function is one EvYourWord
// per player, sent later by openWordReveal.
func (r *Room) assignWords() {
	// Every seat is dealt in, not just the connected ones. Player.Active governs
	// participation and swings with the socket; a word is a property of the seat
	// and must survive a drop, because a player who reconnects mid-match holding
	// no word has no way back into the game.
	dealt := make([]*Player, 0, len(r.players))
	for _, p := range r.players {
		if !p.Eliminated {
			dealt = append(dealt, p)
		}
	}
	if len(dealt) == 0 || r.deck == nil {
		return
	}

	// Every word this match has already dealt, so the deck can skip the clusters
	// they came from. Blocking the previous cluster alone is not enough past two
	// rounds: round 3 would be free to land back on round 1's cluster and hand
	// somebody the same word twice, which tells them they hold the common one.
	avoid := make([]string, 0, 2*len(r.history))
	for _, h := range r.history {
		avoid = append(avoid, h.common, h.imposter)
	}

	a, b := r.deck.Pair(r.settings.GetDifficulty(), r.rnd, avoid)
	if r.rnd.IntN(2) == 1 {
		a, b = b, a
	}
	common, imposter := a, b

	// The imposter seats are a property of the match, not of the round.
	// Re-rolling them would make every round an unrelated game and throw away
	// the only thing that accumulates across them.
	if len(r.imposterIDs) == 0 {
		r.chooseImposters(dealt)
	}

	seats := make(map[string]bool, len(dealt))
	for _, p := range dealt {
		// Everyone starts on the common word and the imposters are moved off it,
		// rather than each seat asking which side it is on. One branch, and a
		// seat that is somehow in neither set still ends up holding a legal word.
		p.word = common
		seats[p.ID] = true
	}
	for _, id := range r.imposterIDs {
		if o := r.byID[id]; o != nil && seats[id] {
			o.word = imposter
		}
	}

	r.commonWord = common
	r.imposterWord = imposter
	r.history = append(r.history, roundWords{
		// The reveal that deals round n runs while r.round is still n-1.
		round:    r.round + 1,
		common:   common,
		imposter: imposter,
		dealt:    seats,
	})

	// Deliberately no word and no imposter id in this line. The COUNT is safe:
	// it is a public lobby setting every player can already read.
	r.log.Debug("words dealt", "round", r.round+1, "players", len(dealt),
		"imposters", len(r.imposterIDs),
		"difficulty", r.settings.GetDifficulty().String())
}

// chooseImposters picks the configured number of distinct seats uniformly at
// random from the seats dealt into the opening round, and pins them for the
// match.
//
// A partial Fisher-Yates over a copy of `dealt`, so every set of that size is
// equally likely and no seat can be picked twice — sampling with replacement
// would quietly deal a two-imposter match with one imposter in it.
//
// The count is clamped to leave at least one seat on the common word. Settings
// clamping already caps it at MaxImposters and the roster at MinPlayers, so at
// the supported sizes this cannot bite; it is here so the invariant holds by
// construction rather than by two other constants agreeing.
//
// r.imposterIDs is stored in SEAT order, not draw order: it goes on the wire in
// MatchEnded and SpectatorInfo, and the order the shuffle happened to pick two
// seats in is not something either of those should encode.
func (r *Room) chooseImposters(dealt []*Player) {
	n := int(r.settings.GetImposterCount())
	if n < MinImposters {
		n = MinImposters
	}
	if n > len(dealt)-1 {
		n = len(dealt) - 1
	}
	if n <= 0 {
		return
	}

	pool := append([]*Player(nil), dealt...)
	for i := 0; i < n; i++ {
		j := i + r.rnd.IntN(len(pool)-i)
		pool[i], pool[j] = pool[j], pool[i]
	}
	chosen := make(map[string]bool, n)
	for _, p := range pool[:n] {
		chosen[p.ID] = true
	}

	// dealt is already in seat order (assignWords walks r.players), so filtering
	// it re-imposes that order on the draw.
	ids := make([]string, 0, n)
	for _, p := range dealt {
		if chosen[p.ID] {
			ids = append(ids, p.ID)
		}
	}
	r.imposterIDs = ids
	r.isImposter = chosen
}

// impostersRemaining counts the imposter seats that have not been eliminated.
// The group wins only when it reaches zero (MULTIPLE_IMPOSTERS.md, "Win
// Conditions").
//
// Elimination, not connection: a dark imposter seat is still holding the odd
// word and still has to be voted out. What their silence does is defer every
// headcount verdict until their grace window settles it — see evaluateEnd.
func (r *Room) impostersRemaining() int {
	n := 0
	for _, id := range r.imposterIDs {
		if p := r.byID[id]; p != nil && !p.Eliminated {
			n++
		}
	}
	return n
}

// archiveCanvas freezes the current round's finished canvas onto its history
// entry, for the spectator dossier.
//
// Called once the round's last drawing turn is over, and again — defensively,
// and idempotently — just before the next reveal wipes the canvas. Nothing
// appends to r.strokes after the drawing phase, and openWordReveal replaces the
// slice rather than truncating it, so the copied header can never be rewritten
// under the archive.
func (r *Room) archiveCanvas() {
	if len(r.history) == 0 || len(r.strokes) == 0 {
		return
	}
	h := &r.history[len(r.history)-1]
	if h.round != r.round || h.strokes != nil {
		return
	}
	h.strokes = append([]*genpb.Stroke(nil), r.strokes...)
}
