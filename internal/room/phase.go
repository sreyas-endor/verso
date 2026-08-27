package room

// phase.go — the phase machine (IMPLEMENTATION_PLAN.md §4.5, DESIGN.md:27).
//
//	lobby -> assigning -> drawing(turn 1..n) -> discussion -> resolving
//	             ^                                               |
//	             +------------------ next round -----------------+
//	                                                             v
//	                                                           ended
//
// Discussion and voting are one phase with one combined timer (DESIGN.md:46).
// Turn order is reshuffled independently at the start of every round
// (DESIGN.md:36). Every transition is driven either by r.phaseTimer firing or
// by a command, and both land on the room goroutine.
//
// Note where the "next round" arrow lands: on ASSIGNING, not on DRAWING. Every
// round opens with its own word reveal, because every round wipes the canvas
// and deals a fresh pair (DESIGN.md:36). The imposter is not re-rolled.

import (
	"time"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// onDeadline runs when the phase timer fires.
func (r *Room) onDeadline() {
	if r.turnGrace {
		// The turn's clock expired a moment ago and the artist was mid-stroke.
		// Nothing arrived to close it, so the room closes it now: the deadline
		// was already spent and consumed on the firing that opened the window.
		r.endTurn()
		return
	}
	if r.phaseDeadline.IsZero() {
		// A disarmed timer that fired anyway. Nothing is owed.
		return
	}
	r.phaseDeadline = time.Time{}

	switch r.phase {
	case genpb.Phase_PHASE_ASSIGNING:
		// The reveal that opens round n runs while r.round is still n-1, so this
		// is 1 at match start and n+1 between rounds.
		r.beginRound(r.round + 1)
	case genpb.Phase_PHASE_INTERMISSION:
		if r.nextArtistID == "" {
			r.beginDiscussion()
			return
		}
		r.beginTurn()
	case genpb.Phase_PHASE_DRAWING:
		if r.beginTurnGrace() {
			return
		}
		r.endTurn()
	case genpb.Phase_PHASE_DISCUSSION:
		// Time expired with votes missing. Those players abstained: they are
		// counted in no bucket, not promoted to Skip (DESIGN.md:52).
		r.resolveRound()
	case genpb.Phase_PHASE_RESOLVING:
		r.afterResolve()
	default:
		// PHASE_LOBBY and PHASE_ENDED are untimed.
	}
}

// ---------------------------------------------------------------------------
// lobby -> assigning
// ---------------------------------------------------------------------------

func (r *Room) onStartMatch(p *Player, cid string) {
	if r.phase != genpb.Phase_PHASE_LOBBY {
		r.SendError(p.ID, cid, ErrWrongPhase)
		return
	}
	if !p.IsHost {
		r.SendError(p.ID, cid, ErrNotHost)
		return
	}
	if n := len(r.players); n < MinPlayers || n > MaxPlayers {
		r.SendError(p.ID, cid, ErrNotEnoughPlayers)
		return
	}
	if r.deck == nil {
		r.SendError(p.ID, cid, ErrInvalidCommand)
		return
	}
	if !r.canStart() {
		r.sendErrorCode(p.ID, cid, genpb.ErrorCode_ERROR_CODE_NOT_READY,
			"every seated player must be ready")
		return
	}
	r.beginAssigning()
}

// beginAssigning opens the match: it clears every trace of a previous one and
// then runs the same word reveal that opens each later round (DESIGN.md:29).
// The round counter stays 0 until the first round opens.
func (r *Room) beginAssigning() {
	r.round = 0
	r.imposterID = ""
	r.history = nil
	r.winner = genpb.WinnerSide_WINNER_SIDE_UNSPECIFIED
	r.endReason = genpb.MatchEndReason_MATCH_END_REASON_UNSPECIFIED

	r.openWordReveal()
	r.log.Info("match started", "players", len(r.players), "rounds", r.settings.GetMaxRounds())
}

// beginRoundReveal opens rounds 2..n. It is beginAssigning without the
// match-level reset: the imposter, the round counter and the accumulated
// history all survive, and only the round's own state is cleared.
func (r *Room) beginRoundReveal() {
	r.openWordReveal()
	r.log.Info("round reveal", "next_round", r.round+1)
}

// openWordReveal wipes the canvas, deals the round's pair and holds the
// private-reveal screen while every player reads their new word.
//
// Wiping here rather than in beginRound is deliberate: the client clears its
// own canvas when it sees PHASE_ASSIGNING (web/src/main.ts), so doing it at the
// same transition keeps the two sides in step, and it gives the client the
// whole reveal to archive the finished drawing before the next round marks it.
func (r *Room) openWordReveal() {
	r.phase = genpb.Phase_PHASE_ASSIGNING
	r.turnOrder = nil
	r.turnIndex = 0
	r.artistID = ""
	r.nextArtistID = ""
	// Each round is judged on its own canvas. seq and nextStrokeID are NOT
	// reset — they stay monotonic for the life of the room, so a client can
	// never mistake a new round for a replayed gap (see resetToLobby).
	r.strokes = nil
	r.open = nil
	r.turnGrace = false
	r.pointsThisTurn = 0
	r.strokesThisTurn = 0
	clear(r.votes)

	r.assignWords()

	r.armPhase(AssignDuration)
	r.Broadcast(r.phaseChanged(AssignDuration))

	// One unicast per player, each carrying only that player's own word. No
	// player is told their role or anyone else's word (DESIGN.md:25).
	for _, p := range r.players {
		r.sendYourWord(p)
	}
}

// ---------------------------------------------------------------------------
// rounds and turns
// ---------------------------------------------------------------------------

// beginRound opens round n: reshuffle the turn order over the active players
// and start the first playable turn.
func (r *Room) beginRound(n int32) {
	r.round = n
	clear(r.votes)

	active := r.ActivePlayers()
	order := make([]string, 0, len(active))
	for _, p := range active {
		order = append(order, p.ID)
	}
	// DESIGN.md:36 — independently reshuffled at the start of EVERY round, not
	// rotated, so seat order leaks nothing across rounds.
	r.rnd.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	r.turnOrder = order
	r.turnIndex = 0

	r.disarmPhase()
	r.Broadcast(EvRoundStarted{&genpb.RoundStarted{
		Round:       r.round,
		TotalRounds: r.settings.GetMaxRounds(),
		TurnOrder:   append([]string(nil), order...),
		ActiveCount: int32(len(active)),
	}})
	r.beginTurnAt(0)
}

// beginTurnAt starts the first playable turn at or after index i, and falls
// through to the discussion phase when the round is out of artists.
//
// An inactive seat — eliminated, or simply dark right now — is skipped rather
// than waited on: a disconnected player misses any drawing turn that occurs
// while they are absent (DESIGN.md:122, open question 5 resolved as "skip
// immediately").
func (r *Room) beginTurnAt(i int) {
	for ; i < len(r.turnOrder); i++ {
		p := r.byID[r.turnOrder[i]]
		if p == nil || !p.Active() {
			continue
		}
		break
	}
	r.turnIndex = i
	if i >= len(r.turnOrder) {
		r.beginVotingIntermission()
		return
	}

	r.nextArtistID = r.turnOrder[i]
	r.beginIntermission()
}

// beginIntermission announces the next activity before it becomes live. It is
// a real phase so reconnecting clients inherit the same authoritative clock.
func (r *Room) beginIntermission() {
	r.phase = genpb.Phase_PHASE_INTERMISSION
	r.artistID = ""
	d := time.Duration(r.settings.GetIntermissionSeconds()) * time.Second
	r.armPhase(d)
	r.Broadcast(r.phaseChanged(d))
}

// beginTurn starts the artist announced by the preceding intermission.
func (r *Room) beginTurn() {
	if r.nextArtistID == "" {
		r.beginDiscussion()
		return
	}
	r.phase = genpb.Phase_PHASE_DRAWING
	r.artistID = r.nextArtistID
	r.nextArtistID = ""
	r.open = nil
	r.turnGrace = false
	r.pointsThisTurn = 0
	r.strokesThisTurn = 0

	d := time.Duration(r.settings.GetDrawSeconds()) * time.Second
	r.armPhase(d)
	// Drawing has a per-turn clock carried by TurnStarted, but the explicit
	// phase transition is still needed so clients leave the handoff screen.
	// It carries the same duration rather than 0: phaseChanged fills
	// remaining_ms from the armed deadline either way, and duration_ms = 0 with
	// remaining_ms = 4999 contradicts the proto's own "0 for an untimed phase".
	r.Broadcast(r.phaseChanged(d))
	r.Broadcast(EvTurnStarted{&genpb.TurnStarted{
		Round:       r.round,
		TurnIndex:   int32(r.turnIndex),
		ArtistId:    r.artistID,
		DurationMs:  int32(d / time.Millisecond),
		RemainingMs: r.RemainingMS(),
	}})
}

// beginTurnGrace holds an expired drawing turn open for TurnGrace so the stroke
// under the artist's pen can finish arriving. It reports whether the turn is
// now in that window, i.e. whether the caller must not end it yet.
//
// Only a turn that expired mid-stroke gets one. With nothing open there is
// nothing in flight to wait for, and the turn ends on the tick exactly as it
// always did — which is the common case, so the ordinary handoff is unchanged.
func (r *Room) beginTurnGrace() bool {
	if r.turnGrace || r.open == nil {
		return false
	}
	r.turnGrace = true
	// Deliberately not armPhase: phaseDeadline stays zero, because the turn's
	// clock really has run out and RemainingMS must keep saying so. A client
	// that joins or resyncs inside the window is told the turn has no time
	// left, which is the truth — the window buys the room the tail of one
	// stroke, it does not hand the artist another 400 ms to draw in.
	r.phaseTimer.Stop()
	r.phaseTimer.Reset(TurnGrace)
	r.log.Debug("turn expired mid-stroke, holding for the tail", "artist", r.artistID)
	return true
}

// endTurn closes the current turn and moves to the next one.
func (r *Room) endTurn() {
	// A turn that expires mid-stroke still commits what was drawn: the canvas
	// is append-only evidence (DESIGN.md:85). By here the stroke has either
	// arrived in full through the grace window or run that window out.
	r.turnGrace = false
	r.commitOpen(nil)
	r.artistID = ""
	r.disarmPhase()
	r.beginTurnAt(r.turnIndex + 1)
}

// skipCurrentTurn ends the live turn early. Called when the artist's socket
// drops mid-turn (open question 5).
func (r *Room) skipCurrentTurn() {
	if r.phase != genpb.Phase_PHASE_DRAWING {
		return
	}
	r.log.Info("artist left mid-turn, skipping", "artist", r.artistID)
	r.endTurn()
}

// ---------------------------------------------------------------------------
// discussion + voting (one phase, one timer)
// ---------------------------------------------------------------------------

func (r *Room) beginVotingIntermission() {
	r.commitOpen(nil)
	r.artistID = ""
	// Snapshot.turn_order is specified as empty outside PHASE_DRAWING.
	r.turnOrder = nil
	r.turnIndex = 0
	clear(r.votes)

	r.nextArtistID = ""
	r.beginIntermission()
}

func (r *Room) beginDiscussion() {
	r.phase = genpb.Phase_PHASE_DISCUSSION
	d := time.Duration(r.settings.GetDiscussSeconds()) * time.Second
	r.armPhase(d)
	r.Broadcast(r.phaseChanged(d))
	r.broadcastVoteCount()
}

// ---------------------------------------------------------------------------
// resolving -> next round or ended
// ---------------------------------------------------------------------------

// afterResolve runs when the vote-result screen has been up long enough: either
// the match is over, or the next round begins (DESIGN.md:59).
func (r *Room) afterResolve() {
	if r.endReason != genpb.MatchEndReason_MATCH_END_REASON_UNSPECIFIED {
		r.finishMatch()
		return
	}
	// max_rounds is a hard ceiling, including when evaluateEnd deliberately
	// defers its verdict while the imposter is temporarily disconnected.  In
	// that case keep the final result visible until the seat reconnects or its
	// grace period expires; starting another round would announce a round the
	// host did not configure.
	if r.round >= r.settings.GetMaxRounds() {
		return
	}
	// Not beginRound: the next round needs its own pair and its own blank
	// canvas, and every player needs to be shown the new word before anybody
	// draws on it.
	r.beginRoundReveal()
}

// ---------------------------------------------------------------------------
// rematch
// ---------------------------------------------------------------------------

func (r *Room) onRematch(p *Player, cid string) {
	if r.phase != genpb.Phase_PHASE_ENDED {
		r.SendError(p.ID, cid, ErrWrongPhase)
		return
	}
	if !p.IsHost {
		r.SendError(p.ID, cid, ErrNotHost)
		return
	}
	r.resetToLobby()
	r.BroadcastReply(cid, r.lobbyState())
}

// resetToLobby returns the room to a pre-match state (DESIGN.md:81). Words are
// cleared so a stale assignment cannot survive into the next match; seq and
// stroke ids stay monotonic for the life of the room so a client can never
// mistake a rematch for a replayed gap.
func (r *Room) resetToLobby() {
	r.disarmPhase()
	r.phase = genpb.Phase_PHASE_LOBBY
	r.round = 0
	r.turnOrder = nil
	r.turnIndex = 0
	r.artistID = ""
	r.nextArtistID = ""
	r.strokes = nil
	r.open = nil
	r.turnGrace = false
	r.pointsThisTurn = 0
	r.strokesThisTurn = 0
	clear(r.votes)
	r.commonWord = ""
	r.imposterWord = ""
	r.imposterID = ""
	r.history = nil
	r.winner = genpb.WinnerSide_WINNER_SIDE_UNSPECIFIED
	r.endReason = genpb.MatchEndReason_MATCH_END_REASON_UNSPECIFIED

	for _, p := range r.players {
		p.word = ""
		p.Eliminated = false
		p.Ready = false
	}

	// A rematch is a new match, and any seat with no live socket is not in it.
	// The grace window is a promise about the match in progress, not a standing
	// claim on the room: whoever is gone when the lobby reopens rejoins with the
	// room code like anybody else.
	//
	// This is load-bearing, not tidying. expireSeat clears DisconnectedAt when it
	// eliminates a seat mid-match, so the liveness sweep deliberately stops
	// re-expiring that seat — which means nothing else would ever collect it.
	// Left in the roster it can never be Ready again, and canStart() requires
	// every seated player to be ready, so one mid-match timeout would make every
	// future rematch in that room unstartable.
	for _, p := range append([]*Player(nil), r.players...) {
		if !p.Connected {
			r.removeSeat(p)
		}
	}
}

// phaseChanged renders the transition event for the current phase. d is the
// whole phase length; pass 0 for an untimed phase.
func (r *Room) phaseChanged(d time.Duration) EvPhaseChanged {
	return EvPhaseChanged{&genpb.PhaseChanged{
		Phase:        r.phase,
		Round:        r.round,
		DurationMs:   int32(d / time.Millisecond),
		RemainingMs:  r.RemainingMS(),
		NextArtistId: r.nextArtistID,
	}}
}
