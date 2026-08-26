package room

// vote.go — anonymous, irreversible plurality voting
// (DESIGN.md:49).
//
// Four invariants hold everywhere in this file:
//
//  1. r.votes maps voter -> candidate. It NEVER goes on the wire and is never
//     logged. Only aggregates are published (DESIGN.md:56).
//  2. A disconnected voter is excluded from the tally.
//  3. Skip is on the ballot, not a residue. It competes for first place against
//     the named candidates, so a 3-3 split between one player and Skip is a tie
//     and eliminates nobody (DESIGN.md:58).
//  4. An absent vote is an abstention, NOT a Skip. A player who never answers
//     is counted in no bucket at all, so sum(counts) + skip_count can be less
//     than active_count. Promoting silence to Skip would let the quiet half of
//     a room outvote the half that actually chose.

import (
	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// onCastVote records one vote. Valid choices are any active player, the voter
// themself, or Skip (DESIGN.md:51).
func (r *Room) onCastVote(p *Player, cid string, cv *genpb.CastVote) {
	if r.phase != genpb.Phase_PHASE_DISCUSSION {
		r.SendError(p.ID, cid, ErrWrongPhase)
		return
	}
	if !p.Active() {
		// Eliminated players are silent spectators (DESIGN.md:66).
		r.SendError(p.ID, cid, ErrNotActive)
		return
	}
	if _, done := r.votes[p.ID]; done {
		// One vote, irreversible (DESIGN.md:49).
		r.SendError(p.ID, cid, ErrAlreadyVoted)
		return
	}

	var candidate string
	switch c := cv.GetChoice().(type) {
	case *genpb.CastVote_Skip:
		if !c.Skip {
			r.SendError(p.ID, cid, ErrInvalidCommand)
			return
		}
	case *genpb.CastVote_CandidateId:
		target := r.byID[c.CandidateId]
		if target == nil || !target.Active() {
			r.SendError(p.ID, cid, ErrInvalidCommand)
			return
		}
		// Voting for yourself is explicitly legal.
		candidate = target.ID
	default:
		r.SendError(p.ID, cid, ErrInvalidCommand)
		return
	}

	r.votes[p.ID] = candidate

	// The confirmation names a voter and their candidate, so it is unicast and
	// EvVoteAccepted has no broadcastSafe marker.
	r.SendReply(p.ID, cid, EvVoteAccepted{&genpb.VoteAccepted{
		Round:       r.round,
		CandidateId: candidate,
		Skip:        candidate == "",
	}})
	r.broadcastVoteCount()
	r.maybeResolve()
}

// votesFromActive counts the recorded votes that still belong to an active
// player. It is the numerator everywhere — the progress event, the private
// view and the early-resolve check — so it can never disagree with
// ActiveCount() the way a bare len(r.votes) can once a seat has left the
// denominator mid-window.
func (r *Room) votesFromActive() int {
	n := 0
	for voter := range r.votes {
		if p := r.byID[voter]; p != nil && p.Active() {
			n++
		}
	}
	return n
}

// maybeResolve closes the voting window early once every active player has
// voted (DESIGN.md:52).
//
// It is checked on two events, not one: a vote arriving, and the denominator
// shrinking. A socket dropping takes that seat out of the denominator
// (DESIGN.md "Active players"), which completes the count exactly as a final
// vote would — and without this second check the room would sit on a satisfied
// tally until the combined timer ran out, waiting on somebody who has left.
func (r *Room) maybeResolve() {
	if r.phase != genpb.Phase_PHASE_DISCUSSION {
		return
	}
	if r.votesFromActive() >= r.ActiveCount() {
		r.resolveRound()
	}
}

// broadcastVoteCount publishes progress as a bare count. It exists so the UI
// can show "4 of 6 voted" without ever learning who voted (DESIGN.md:56).
func (r *Room) broadcastVoteCount() {
	r.Broadcast(EvVoteCastCount{&genpb.VoteCastCount{
		Round:       r.round,
		VotesCast:   int32(r.votesFromActive()),
		ActiveCount: int32(r.ActiveCount()),
	}})
}

// tally reduces r.votes to the aggregate that may be published, and names the
// player eliminated by the plurality — nobody, when the lead is tied or Skip
// holds it.
//
// The returned VoteTally is the only thing derived from r.votes that ever
// leaves this package.
func (r *Room) tally() (*genpb.VoteTally, string) {
	active := r.ActiveCount()

	counts := make(map[string]int32, len(r.votes))
	skip := int32(0)
	for voter, candidate := range r.votes {
		if v := r.byID[voter]; v == nil || !v.Active() {
			continue
		}
		if candidate == "" {
			skip++
			continue
		}
		counts[candidate]++
	}
	// Nothing is added for the players who never answered. Invariant 4: an
	// abstention is not a Skip, so it lands in no bucket and cannot lend weight
	// to the option that eliminates nobody.

	// Stable order: seat order, so the wire bytes do not depend on map
	// iteration and a test can compare frames.
	out := make([]*genpb.VoteCount, 0, len(counts))
	leader := ""
	mostVotes := int32(0)
	tiedForMost := false
	for _, p := range r.players {
		n, ok := counts[p.ID]
		if !ok || n == 0 {
			continue
		}
		out = append(out, &genpb.VoteCount{CandidateId: p.ID, Votes: n})
		switch {
		case n > mostVotes:
			mostVotes, leader, tiedForMost = n, p.ID, false
		case n == mostVotes:
			tiedForMost = true
		}
	}

	// Invariant 3: winning outright means strictly ahead of every other
	// candidate AND strictly ahead of Skip. `<=` rather than `<` is the whole
	// rule — drawing level with Skip is a tie, and a tie eliminates nobody.
	// It also covers the empty vote: mostVotes and skip are both 0 there.
	eliminated := leader
	if tiedForMost || mostVotes <= skip {
		eliminated = ""
	}

	return &genpb.VoteTally{
		Round:       r.round,
		Counts:      out,
		SkipCount:   skip,
		ActiveCount: int32(active),
		// Deprecated and always 0: a plurality has no threshold to publish.
		MajorityThreshold: 0,
	}, eliminated
}

// resolveRound closes the voting window, publishes the aggregate result, and
// applies the elimination. It then decides whether the match continues, and
// parks on the vote-result screen for ResolveDuration either way.
func (r *Room) resolveRound() {
	r.disarmPhase()
	r.phase = genpb.Phase_PHASE_RESOLVING

	tally, eliminatedID := r.tally()
	// The vote map has done its work. Drop it here so nothing downstream can
	// reach for it.
	clear(r.votes)

	r.armPhase(ResolveDuration)
	r.Broadcast(r.phaseChanged(ResolveDuration))
	r.Broadcast(EvVoteTally{tally})

	r.applyElimination(eliminatedID)
}
