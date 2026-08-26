package room

// end.go — elimination outcomes and the end conditions (DESIGN.md:61).
//
//	imposter eliminated              -> group wins immediately
//	non-imposter eliminated          -> spectator, privately told who the imposter
//	                                   is, match continues
//	imposter survives the last round -> imposter wins
//	only two active players remain  -> imposter wins
//	imposter's seat expires          -> group wins (DESIGN.md:125)

import (
	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// matchInProgress reports whether words have been dealt and the match has not
// yet resolved.
func (r *Room) matchInProgress() bool {
	switch r.phase {
	case genpb.Phase_PHASE_ASSIGNING,
		genpb.Phase_PHASE_INTERMISSION,
		genpb.Phase_PHASE_DRAWING,
		genpb.Phase_PHASE_DISCUSSION,
		genpb.Phase_PHASE_RESOLVING:
		return true
	default:
		return false
	}
}

// applyElimination publishes the outcome of a tally, including "nobody", and
// decides whether the match is over.
//
// PlayerEliminated.was_imposter is set only here, and only when the same
// resolution produces a group win: whenever the match continues, active players
// are told nothing more than that a non-imposter went (DESIGN.md:65).
func (r *Room) applyElimination(eliminatedID string) {
	if eliminatedID == "" {
		// The top vote was tied (or everybody skipped), so nobody is eliminated.
		r.Broadcast(EvPlayerEliminated{&genpb.PlayerEliminated{
			Round:      r.round,
			Eliminated: false,
		}})
		r.evaluateEnd("")
		return
	}

	p := r.byID[eliminatedID]
	if p == nil {
		return
	}
	p.Eliminated = true
	wasImposter := eliminatedID == r.imposterID

	// Decided before the announcement so the was_imposter invariant is provable
	// at a glance: it is true here exactly when evaluateEnd has just set a
	// group win.
	r.evaluateEnd(eliminatedID)

	r.Broadcast(EvPlayerEliminated{&genpb.PlayerEliminated{
		Round:       r.round,
		Eliminated:  true,
		PlayerId:    eliminatedID,
		WasImposter: wasImposter,
	}})

	if !wasImposter {
		// The eliminated player becomes a silent spectator and privately learns
		// the real imposter's identity (DESIGN.md:67). Unicast: broadcasting it
		// would end the game instantly and invisibly.
		if imposter := r.byID[r.imposterID]; imposter != nil {
			r.SendTo(eliminatedID, EvSpectatorInfo{&genpb.SpectatorInfo{
				ImposterPlayerId: imposter.ID,
				ImposterName:     imposter.Name,
			}})
		}
	}
}

// evaluateEnd records the winner and reason if the match is over, and leaves
// both unset if it is not. eliminatedID is the player just voted out, or "".
//
// Order matters: eliminating the imposter is a group win even when it also
// leaves only two players standing.
func (r *Room) evaluateEnd(eliminatedID string) {
	// Settled first, because it outranks every headcount below.
	if eliminatedID != "" && eliminatedID == r.imposterID {
		r.winner = genpb.WinnerSide_WINNER_SIDE_GROUP
		r.endReason = genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_ELIMINATED
		return
	}

	// While the imposter's own socket is dark, no headcount may end the match.
	//
	// A disconnected seat is not active (DESIGN.md "Active players"), so the
	// imposter dropping shrinks the very count these rules read — in a 3-player
	// room it takes it straight to two and would award the imposter the match for
	// having left. DESIGN.md:125 reserves this exact situation for the opposite
	// verdict: a group win, once their grace window expires. Either they come
	// back, or expireSeat calls it for the group. Nothing here may pre-empt that.
	if o := r.byID[r.imposterID]; o != nil && !o.Connected && !o.Eliminated {
		return
	}

	active := r.ActiveCount()
	switch {
	case active < 2:
		// Too few seats left to play at all. Nobody wins.
		r.winner = genpb.WinnerSide_WINNER_SIDE_UNSPECIFIED
		r.endReason = genpb.MatchEndReason_MATCH_END_REASON_ABANDONED
	case active == 2:
		// DESIGN.md:70 — deliberately means a 3-player match ends on a single
		// wrong vote.
		r.winner = genpb.WinnerSide_WINNER_SIDE_IMPOSTER
		r.endReason = genpb.MatchEndReason_MATCH_END_REASON_TWO_PLAYERS_REMAIN
	case r.phase == genpb.Phase_PHASE_RESOLVING && r.round >= r.settings.GetMaxRounds():
		// Only from the resolution of the final round's vote. DESIGN.md:71 says
		// the imposter wins by remaining active AFTER the configured final round,
		// so a seat expiring part-way through that round's drawing or discussion
		// must not hand them the match early. reevaluateEnd reaches this switch
		// from those phases too; the two-players-remain case above is genuinely
		// immediate, this one is not.
		r.winner = genpb.WinnerSide_WINNER_SIDE_IMPOSTER
		r.endReason = genpb.MatchEndReason_MATCH_END_REASON_FINAL_ROUND_SURVIVED
	}
}

// endNow ends a running match immediately, skipping the vote-result screen. It
// is the imposter-disconnect and abandonment path.
func (r *Room) endNow(winner genpb.WinnerSide, reason genpb.MatchEndReason) {
	if !r.matchInProgress() {
		return
	}
	r.winner = winner
	r.endReason = reason
	r.finishMatch()
}

// reevaluateEnd re-checks the end conditions after the roster shrank for a
// reason other than a vote — a grace window expiring mid-match.
func (r *Room) reevaluateEnd() {
	if !r.matchInProgress() {
		return
	}
	r.evaluateEnd("")
	if r.endReason == genpb.MatchEndReason_MATCH_END_REASON_UNSPECIFIED {
		return
	}
	if r.phase == genpb.Phase_PHASE_RESOLVING {
		// The vote-result screen is up; afterResolve will finish on its timer.
		return
	}
	r.finishMatch()
}

// finishMatch publishes the final reveal. r.winner and r.endReason must already
// be set.
func (r *Room) finishMatch() {
	r.commitOpen(nil)
	r.disarmPhase()
	r.artistID = ""
	r.turnOrder = nil
	r.turnIndex = 0
	clear(r.votes)
	r.phase = genpb.Phase_PHASE_ENDED

	r.Broadcast(r.phaseChanged(0))
	r.Broadcast(EvMatchEnded{&genpb.MatchEnded{
		Winner:           r.winner,
		Reason:           r.endReason,
		CommonWord:       r.commonWord,
		ImposterWord:     r.imposterWord,
		ImposterPlayerId: r.imposterID,
		Reveals:          r.buildReveals(),
		RoundsPlayed:     r.round,
	}})

	// Never the words: the reveal is on the wire, not in the log.
	r.log.Info("match ended",
		"winner", r.winner.String(),
		"reason", r.endReason.String(),
		"rounds", r.round)
}
