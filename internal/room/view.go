package room

// view.go — every unicast a player receives, and the accounting for the secret
// word.
//
// ===========================================================================
// THE ONLY PLACE IN THIS PROGRAM THAT READS Player.word IS Room.viewFor
// (internal/room/api.go). That is defense 2 of IMPLEMENTATION_PLAN.md §1, and
// it is verifiable with a single grep:
//
//	grep -rn 'p\.word\|\.word\b' internal/
//
// sendYourWord goes through viewFor rather than touching the field: it takes
// the value out of that player's own private view. The cost is one Snapshot
// per player per round; the benefit is that a second reader is a visible new
// line rather than an easy accident.
//
// buildReveals does not read Player.word at all. It cannot: the final reveal
// now shows EVERY round's words, and the field only ever holds the current
// one. It reads r.history instead, which assignWords fills in from the pair it
// has just drawn. Fewer readers of the field, not more.
//
// viewFor's result is unicast-only: EvSnapshot carries no broadcastSafe
// marker, so it cannot be handed to Broadcast. MatchEnded is the one
// broadcast that legitimately carries words, and it is emitted only in
// PHASE_ENDED (DESIGN.md:75).
//
// The word is never logged, never formatted into an error string, and never
// copied onto a broadcast-safe payload.
// ===========================================================================

import (
	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// sendYourWord delivers one player their own word and nothing else
// (DESIGN.md:31).
//
// It deliberately does NOT read p.word: it takes the value out of that player's
// own private view, so the field keeps exactly one reader. EvYourWord has no
// broadcastSafe method, so this cannot be broadcast even by accident.
func (r *Room) sendYourWord(p *Player) {
	view := r.viewFor(p.ID)
	if view == nil || view.GetYourWord() == "" {
		return
	}
	r.SendTo(p.ID, EvYourWord{&genpb.YourWord{
		Word: view.GetYourWord(),
		// The reveal that deals round n runs while r.round is still n-1, so
		// this names the round the word is FOR, not the one just finished.
		Round: r.round + 1,
	}})
}

// sendSnapshot unicasts the full private state: phase, clock, roster, the whole
// stroke log replayed in one message, and the recipient's own word. There is no
// incremental catch-up path (IMPLEMENTATION_PLAN.md §4.6).
func (r *Room) sendSnapshot(playerID, cid string) {
	view := r.viewFor(playerID)
	if view == nil {
		return
	}
	r.SendReply(playerID, cid, EvSnapshot{view})
}

// buildRoundWords renders every round's pair for the final reveal, oldest
// first. Valid only once the match is over, for the same reason buildReveals
// is.
func (r *Room) buildRoundWords() []*genpb.RoundWords {
	out := make([]*genpb.RoundWords, 0, len(r.history))
	for _, h := range r.history {
		out = append(out, &genpb.RoundWords{
			Round:        h.round,
			CommonWord:   h.common,
			ImposterWord: h.imposter,
		})
	}
	return out
}

// buildReveals renders the final-reveal rows: every player who was dealt a
// word in any round, the word they held in each of them, whether they were the
// imposter, and whether they were eliminated (DESIGN.md:75).
//
// This is the one place the whole roster's words are published, and it is
// valid only once the match is over — finishMatch has already moved the room
// into PHASE_ENDED before calling it. Do not call it from anywhere else.
//
// It reads no words off Player, so the field keeps its single reader (see the
// header above). Every word here comes out of r.history, and each round's row
// is derived rather than stored per player: within a round there are exactly
// two words, and which one a seat got is decided by one comparison against the
// pinned imposter id.
//
// Rounds a player was already eliminated for contribute an empty string, which
// is how the client tells "held the common word" apart from "was not in this
// round at all". A seat that was never dealt in at all is skipped entirely.
func (r *Room) buildReveals() []*genpb.PlayerReveal {
	out := make([]*genpb.PlayerReveal, 0, len(r.players))
	for _, p := range r.players {
		words := make([]string, 0, len(r.history))
		last := ""
		dealtIn := false
		for _, h := range r.history {
			if !h.dealt[p.ID] {
				words = append(words, "")
				continue
			}
			dealtIn = true
			w := h.common
			if p.ID == r.imposterID {
				w = h.imposter
			}
			words = append(words, w)
			last = w
		}
		if !dealtIn {
			continue
		}
		out = append(out, &genpb.PlayerReveal{
			PlayerId:    p.ID,
			Name:        p.Name,
			Word:        last,
			WasImposter: p.ID == r.imposterID,
			Eliminated:  p.Eliminated,
			Words:       words,
		})
	}
	return out
}
