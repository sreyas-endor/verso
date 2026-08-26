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

// assignWords deals the match. It writes Player.word and reads it nowhere.
//
// No player is told their role, the full pair, or anyone else's word
// (DESIGN.md:25) — the only thing that leaves this function is one EvYourWord
// per player, sent later by beginAssigning.
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

	a, b := r.deck.Pair(r.settings.GetDifficulty(), r.rnd)
	if r.rnd.IntN(2) == 1 {
		a, b = b, a
	}
	common, imposter := a, b

	pick := r.rnd.IntN(len(dealt))
	for _, p := range dealt {
		p.word = common
	}
	dealt[pick].word = imposter

	r.commonWord = common
	r.imposterWord = imposter
	r.imposterID = dealt[pick].ID

	// Deliberately no word and no imposter id in this line.
	r.log.Debug("words dealt", "players", len(dealt), "difficulty", r.settings.GetDifficulty().String())
}
