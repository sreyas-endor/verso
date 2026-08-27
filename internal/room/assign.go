package room

// assign.go — role assignment (DESIGN.md:22).
//
// The server draws one pair from the chosen difficulty deck, then flips a coin
// for which SIDE of that pair is the common word, then picks exactly one player
// to receive the other side. Both draws come from r.rnd, so a seeded room
// deals the same match every time.
//
// The coin flip is why a deck may list a pair in one direction only: role
// assignment supplies the other direction, and players therefore cannot learn
// which word is the common one from the deck itself (DESIGN.md:172).
//
// This runs once per ROUND, not once per match. The pair is fresh every round
// and the canvas is wiped with it; the imposter is not re-rolled, so suspicion
// carries across rounds even though the evidence does not.

// roundWords is one round's dealt pair, kept for the final reveal. dealt is the
// set of seats that received a word that round — everyone not yet eliminated —
// so the reveal can leave a blank rather than a word for the rounds a player
// was already out of.
type roundWords struct {
	round    int32
	common   string
	imposter string
	dealt    map[string]bool
}

// assignWords deals one round. It writes Player.word and reads it nowhere.
//
// The imposter is chosen only on the first deal of a match, and every later
// round hands the same seat the odd word again (DESIGN.md:24). A seat
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

	// The imposter is a property of the match, not of the round. Re-rolling it
	// would make every round an unrelated game and throw away the only thing
	// that accumulates across them.
	if r.imposterID == "" {
		r.imposterID = dealt[r.rnd.IntN(len(dealt))].ID
	}

	seats := make(map[string]bool, len(dealt))
	for _, p := range dealt {
		p.word = common
		seats[p.ID] = true
	}
	if o := r.byID[r.imposterID]; o != nil && seats[o.ID] {
		o.word = imposter
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

	// Deliberately no word and no imposter id in this line.
	r.log.Debug("words dealt", "round", r.round+1, "players", len(dealt),
		"difficulty", r.settings.GetDifficulty().String())
}
