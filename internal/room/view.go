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
// Every consumer in this file goes through viewFor rather than touching the
// field — including buildReveals, which needs every player's word for the
// final reveal and gets each one out of that player's own private view. The
// cost is one Snapshot per player, once, at the end of a match; the benefit is
// that a second reader is a visible new line rather than an easy accident.
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
		// The word is dealt before round 1 opens and holds for the whole match.
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

// buildReveals renders the final-reveal rows: every player who was dealt a
// word, with that word, whether they were the imposter, and whether they were
// eliminated (DESIGN.md:75).
//
// This is the one place the whole roster's words are published, and it is
// valid only once the match is over — finishMatch has already moved the room
// into PHASE_ENDED before calling it. Do not call it from anywhere else.
//
// It reads no words itself. Each one comes out of that player's own viewFor
// Snapshot, so Player.word keeps exactly one reader (see the header above).
// An empty word means a seat that was never dealt in, and it is skipped.
func (r *Room) buildReveals() []*genpb.PlayerReveal {
	out := make([]*genpb.PlayerReveal, 0, len(r.players))
	for _, p := range r.players {
		view := r.viewFor(p.ID)
		if view == nil || view.GetYourWord() == "" {
			continue
		}
		out = append(out, &genpb.PlayerReveal{
			PlayerId:    p.ID,
			Name:        p.Name,
			Word:        view.GetYourWord(),
			WasImposter: p.ID == r.imposterID,
			Eliminated:  p.Eliminated,
		})
	}
	return out
}
