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
// buildSpectatorInfo is the same trick for the same reason. It publishes every
// seat's word for every round dealt so far, and it reads r.history rather than
// Player.word to do it — so the field still has exactly one reader. What guards
// it instead is the recipient: sendSpectatorInfo refuses to send to a player
// who is not eliminated, and EvSpectatorInfo has no broadcastSafe marker.
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

	// A spectator resyncing gets their dossier back with it. Snapshot carries
	// the live canvas and the recipient's own word and nothing else, so without
	// this a player who dropped after being eliminated would return holding
	// strictly less than they had — and a seat that was eliminated BY its grace
	// window expiring would come back never having seen one at all.
	//
	// Only while a match is running: in the lobby there is nothing to tell, and
	// in PHASE_ENDED the broadcast reveal has already said all of it.
	if p := r.byID[playerID]; p != nil && p.Eliminated && r.matchInProgress() {
		r.sendSpectatorInfo(p)
	}
}

// sendSpectatorInfo unicasts one eliminated player their complete
// behind-the-scenes view (MULTIPLE_IMPOSTERS.md, "Eliminated-player Spectator
// View").
//
// Two conditions gate it, and both live here rather than at the four call
// sites so a fifth cannot forget one.
//
// The recipient must be ELIMINATED. That is the whole access control.
// p.Eliminated is set before this is reached on every path: applyElimination
// flags the seat before announcing, and the reconnect and round-boundary paths
// only look at seats that are already out.
//
// The match must still be UNDECIDED. Once a winner is recorded, MatchEnded is
// what tells everybody everything (DESIGN.md:75) — including the spectator,
// and including the eight seconds of result screen before it goes out. A
// dossier in that window is not a leak, it is just a second answer to a
// question already settled, and a resync landing there would produce one out of
// nowhere. Deferring the verdict while an imposter's socket is dark leaves the
// reason unset, which is correct: that match really is still being played.
//
// It is sent whole every time rather than as a delta — on the elimination, on
// every later deal, and beside the Snapshot of a reconnect — for the same
// reason Snapshot replays the entire stroke log: there is no catch-up path to
// get wrong (IMPLEMENTATION_PLAN.md §4.6).
func (r *Room) sendSpectatorInfo(p *Player) {
	if p == nil || !p.Eliminated {
		return
	}
	if r.endReason != genpb.MatchEndReason_MATCH_END_REASON_UNSPECIFIED {
		return
	}
	r.SendTo(p.ID, EvSpectatorInfo{r.buildSpectatorInfo()})
}

// sendSpectatorUpdates re-issues the dossier to every player already out. Call
// it whenever the dossier gained something a spectator is owed: a fresh deal,
// or a canvas being archived.
func (r *Room) sendSpectatorUpdates() {
	for _, p := range r.players {
		if p.Eliminated {
			r.sendSpectatorInfo(p)
		}
	}
}

// buildSpectatorInfo renders the whole match so far: every imposter, and for
// every round dealt, the pair, each seat's word and the finished canvas.
//
// SPECTATOR-ONLY. The result carries other players' secrets, so it may only
// ever be handed to sendSpectatorInfo, which is the one function that checks
// the recipient is out of the match. Do not call this from viewFor, from any
// broadcast path, or from anything that runs for an active player.
//
// Like buildReveals it derives each seat's word rather than storing one per
// player: a round has exactly two words in it, and which one a seat got is one
// comparison against the pinned imposter set.
//
// SIZE. The canvases dominate, and this is the largest frame the server sends:
// a Snapshot replays one round's stroke log, and a late-match dossier replays
// every round's. The worst case is bounded — MaxPointsPerTurn per artist per
// round, so roughly four Snapshots at MaxRounds — and a real match is orders
// of magnitude under it. If that ever stops being true, the fix is to send the
// canvases once rather than to drop them: sendSpectatorUpdates re-sends the
// whole dossier on every deal precisely because there is no catch-up path, and
// adding one would need its own reconnect story.
func (r *Room) buildSpectatorInfo() *genpb.SpectatorInfo {
	imposters := make([]*genpb.SpectatorImposter, 0, len(r.imposterIDs))
	for _, id := range r.imposterIDs {
		p := r.byID[id]
		if p == nil {
			continue
		}
		imposters = append(imposters, &genpb.SpectatorImposter{
			PlayerId: p.ID,
			Name:     p.Name,
			Avatar:   p.Avatar,
		})
	}

	rounds := make([]*genpb.SpectatorRound, 0, len(r.history))
	for _, h := range r.history {
		// Seat order, from r.players rather than from h.dealt, so the table is
		// stable frame to frame and does not depend on map iteration.
		assignments := make([]*genpb.SpectatorAssignment, 0, len(h.dealt))
		for _, p := range r.players {
			if !h.dealt[p.ID] {
				continue
			}
			word := h.common
			if r.isImposter[p.ID] {
				word = h.imposter
			}
			assignments = append(assignments, &genpb.SpectatorAssignment{
				PlayerId:   p.ID,
				Word:       word,
				IsImposter: r.isImposter[p.ID],
			})
		}
		rounds = append(rounds, &genpb.SpectatorRound{
			Round:        h.round,
			CommonWord:   h.common,
			ImposterWord: h.imposter,
			Assignments:  assignments,
			// Shared, not copied: the archive is frozen and neither the room nor
			// the marshaller writes through it. The live round has none yet.
			Strokes: h.strokes,
		})
	}

	return &genpb.SpectatorInfo{Imposters: imposters, Rounds: rounds}
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
			if r.isImposter[p.ID] {
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
			Avatar:      p.Avatar,
			Word:        last,
			WasImposter: r.isImposter[p.ID],
			Eliminated:  p.Eliminated,
			Words:       words,
		})
	}
	return out
}
