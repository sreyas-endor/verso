package room

// end.go — elimination outcomes and the end conditions (DESIGN.md:61,
// MULTIPLE_IMPOSTERS.md "Win Conditions").
//
//	LAST imposter eliminated          -> group wins immediately
//	an imposter eliminated, more left -> match continues; Reveal says so
//	non-imposter eliminated           -> match continues
//	any imposter survives the last round -> imposter wins
//	only two active players remain    -> imposter wins
//	any imposter's seat expires       -> group wins (DESIGN.md:125)
//
// Every eliminated player, imposter or not, becomes a spectator holding the
// full behind-the-scenes dossier (view.go, buildSpectatorInfo).
//
// The one-imposter match is the same code with a set of size one: "all
// imposters eliminated" is "the imposter was eliminated", and the base
// design's outcomes are unchanged.

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
// PlayerEliminated.alignment_revealed is decided only here. Under
// ELIMINATION_RESULTS_REVEAL the room is told which side the eliminated player
// was on; under ELIMINATION_RESULTS_HIDDEN it is told only who went. The
// resolution that ENDS the match discloses either way, because the MatchEnded
// following it publishes every alignment on purpose (DESIGN.md:75) and
// withholding the flag would conceal nothing.
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
	wasImposter := r.isImposter[eliminatedID]

	// Run before the announcement, because whether the match is ending is an
	// input to what the announcement is allowed to say.
	r.evaluateEnd(eliminatedID)
	over := r.endReason != genpb.MatchEndReason_MATCH_END_REASON_UNSPECIFIED

	revealed := over ||
		r.settings.GetEliminationResults() != genpb.EliminationResults_ELIMINATION_RESULTS_HIDDEN

	r.Broadcast(EvPlayerEliminated{&genpb.PlayerEliminated{
		Round:      r.round,
		Eliminated: true,
		PlayerId:   eliminatedID,
		// Never sent as a bare truth: a client that ignores alignment_revealed
		// must read "false", not the answer it was not supposed to have.
		WasImposter:       revealed && wasImposter,
		AlignmentRevealed: revealed,
	}})

	// The eliminated player becomes a silent spectator and privately receives
	// the whole match so far — every imposter, every round's pair, every seat's
	// word, every finished canvas (MULTIPLE_IMPOSTERS.md, "Eliminated-player
	// Spectator View"). An eliminated IMPOSTER gets it too: they are out, and
	// by then the only thing left to learn is who they were unknowingly sharing
	// a word with.
	//
	// Unicast: broadcasting it would end the game instantly and invisibly. No
	// `over` check here — sendSpectatorInfo declines for a decided match itself,
	// so the rule holds for the resync path as well as this one.
	r.sendSpectatorInfo(p)
}

// evaluateEnd records the winner and reason if the match is over, and leaves
// both unset if it is not. eliminatedID is the player just voted out, or "".
//
// Order matters: eliminating the LAST imposter is a group win even when it also
// leaves only two players standing. Catching a first imposter out of two is not
// a win at all — it falls through to the headcounts below, which is how a
// four-player two-imposter match can be won on the very next vote and lost on
// the one after.
func (r *Room) evaluateEnd(eliminatedID string) {
	// Settled first, because it outranks every headcount below.
	if eliminatedID != "" && r.isImposter[eliminatedID] && r.impostersRemaining() == 0 {
		r.winner = genpb.WinnerSide_WINNER_SIDE_GROUP
		r.endReason = genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_ELIMINATED
		return
	}

	// While ANY imposter's socket is dark, no headcount may end the match.
	//
	// A disconnected seat is not active (DESIGN.md "Active players"), so an
	// imposter dropping shrinks the very count these rules read — in a 3-player
	// room it takes it straight to two and would award the imposter side the
	// match for having left. DESIGN.md:125 reserves this exact situation for the
	// opposite verdict: a group win, once their grace window expires. Either
	// they come back, or expireSeat calls it for the group. Nothing here may
	// pre-empt that.
	//
	// One dark imposter is enough to defer, even with the other one sitting
	// there connected: the count is distorted either way, and the verdict that
	// resolves it is the same one.
	for _, id := range r.imposterIDs {
		if o := r.byID[id]; o != nil && !o.Connected && !o.Eliminated {
			return
		}
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
		//
		// Reached only when at least one imposter is still standing: the branch
		// at the top of this function has already claimed the group win for the
		// resolution that took the last one out.
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
		Winner:            r.winner,
		Reason:            r.endReason,
		CommonWord:        r.commonWord,
		ImposterWord:      r.imposterWord,
		ImposterPlayerIds: append([]string(nil), r.imposterIDs...),
		Reveals:           r.buildReveals(),
		RoundsPlayed:      r.round,
		Rounds:            r.buildRoundWords(),
	}})

	// Never the words: the reveal is on the wire, not in the log.
	r.log.Info("match ended",
		"winner", r.winner.String(),
		"reason", r.endReason.String(),
		"imposters", len(r.imposterIDs),
		"rounds", r.round)
}
