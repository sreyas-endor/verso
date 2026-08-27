package room

// reconnect.go — seats, the grace window, host migration, the host's kick and
// the liveness sweep (IMPLEMENTATION_PLAN.md §4.6).
//
// Seat, Attach and Detach are synchronous round trips over r.ctl: they run on
// the room goroutine like everything else, so they are not a second way into
// the state.
//
// Resolved decisions this file implements:
//   - 60 s grace window (open question 2).
//   - A disconnected seat keeps its word and its place in the room but LEAVES
//     the majority denominator, the turn order and the two-players-remain count
//     until its socket returns (open question 3, DESIGN.md "Active players").
//   - Host migration promotes the longest-connected active player (open
//     question 4).
//   - A socket that drops mid-drawing-turn skips that turn immediately (open
//     question 5, DESIGN.md:122).
//   - The imposter's grace window expiring ends the match with a group win
//     (DESIGN.md:125).

import (
	"time"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// seat takes a new seat for a first-time player. See Seat in api.go for the
// contract.
func (r *Room) seat(displayName string, out Session) (string, string, error) {
	var id, token string
	var serr error
	if err := r.do(func() { id, token, serr = r.seatOnActor(displayName, out) }); err != nil {
		return "", "", err
	}
	return id, token, serr
}

func (r *Room) seatOnActor(displayName string, out Session) (string, string, error) {
	if r.phase != genpb.Phase_PHASE_LOBBY {
		return "", "", ErrMatchInProgress
	}
	if len(r.players) >= MaxPlayers {
		return "", "", ErrRoomFull
	}

	p := &Player{
		ID:        newID(),
		Name:      truncateName(displayName),
		SeatToken: newToken(),
		Seat:      r.nextSeat,
		Connected: true,
		JoinedAt:  time.Now(),
		outbound:  out,
	}
	r.nextSeat++
	r.players = append(r.players, p)
	r.byID[p.ID] = p
	r.bySeatToken[p.SeatToken] = p
	if r.Host() == nil {
		p.IsHost = true
	}

	r.SendTo(p.ID, r.joinedFor(p, false))
	r.Broadcast(r.lobbyState())
	r.log.Info("player seated", "player", p.ID, "seat", p.Seat)
	return p.ID, p.SeatToken, nil
}

// attach binds a live connection to an existing seat. See Attach in api.go.
func (r *Room) attach(seatToken string, out Session) (string, error) {
	var id string
	var serr error
	if err := r.do(func() { id, serr = r.attachOnActor(seatToken, out) }); err != nil {
		return "", err
	}
	return id, serr
}

func (r *Room) attachOnActor(seatToken string, out Session) (string, error) {
	p := r.bySeatToken[seatToken]
	if p == nil {
		return "", ErrBadSeat
	}

	// A token already live on another socket is honoured rather than rejected:
	// that is what a flaky network looks like from the server's side.
	reclaim := p.Connected || !p.DisconnectedAt.IsZero()
	old := p.outbound

	p.outbound = out
	p.Connected = true
	p.DisconnectedAt = time.Time{}
	if r.Host() == nil {
		p.IsHost = true
	}

	if old != nil && old != out {
		// Tell the displaced socket why it is about to go quiet, THEN ask it to
		// leave. Order matters and so does the split: the error goes on its
		// queue first, and Close is a request to drain that queue and shut
		// down, never a cancellation — cancelling here would race the writer
		// and swallow the explanation.
		//
		// Closing at all is the point. Without it the old socket stays open,
		// answers pings, and holds a connection slot and a 64-frame queue for
		// as long as the client leaves the tab alone, while receiving nothing:
		// the room has already moved the seat to the new session. A phone
		// flapping between wifi and cellular can leave a trail of them.
		old.Send(EvError{&genpb.Error{
			Code:    genpb.ErrorCode_ERROR_CODE_BAD_SEAT,
			Message: "seat reclaimed by another connection",
		}}.Envelope(""))
		old.Close()
	}

	r.SendTo(p.ID, r.joinedFor(p, reclaim))
	// One full Snapshot with the entire stroke log replayed in it. No
	// incremental catch-up, no gap detection (IMPLEMENTATION_PLAN.md §4.6).
	r.sendSnapshot(p.ID, "")
	r.Broadcast(r.presence(p))
	if r.phase == genpb.Phase_PHASE_LOBBY {
		r.Broadcast(r.lobbyState())
	}
	// This seat just re-entered the denominator, so the "n of m voted" line every
	// other client is showing is now wrong by one.
	if r.phase == genpb.Phase_PHASE_DISCUSSION {
		r.broadcastVoteCount()
	}
	// afterResolve parks a final-round result while a disconnected imposter is
	// protected by the grace window. Once a seat returns, settle that parked
	// result immediately: its display timer has already elapsed, and there is
	// no later phase transition left to re-run evaluateEnd.
	if r.phase == genpb.Phase_PHASE_RESOLVING && r.round >= r.settings.GetMaxRounds() {
		r.evaluateEnd("")
		if r.endReason != genpb.MatchEndReason_MATCH_END_REASON_UNSPECIFIED {
			r.finishMatch()
		}
	}
	r.log.Info("seat attached", "player", p.ID, "reclaim", reclaim)
	return p.ID, nil
}

// detach releases a connection. See Detach in api.go.
func (r *Room) detach(playerID string, out Session) {
	_ = r.do(func() { r.detachOnActor(playerID, out) })
}

func (r *Room) detachOnActor(playerID string, out Session) {
	p := r.byID[playerID]
	if p == nil {
		return
	}
	// A late detach from a socket a reconnect already replaced must not knock
	// the new one offline.
	if out != nil && p.outbound != out {
		return
	}

	p.outbound = nil
	p.Connected = false
	p.DisconnectedAt = time.Now()

	// The seat and the word survive; the place in the denominator does not
	// (DESIGN.md:121, and "Active players" above it).
	var promoted *Player
	if p.IsHost {
		if next := r.bestHost(p); next != nil {
			p.IsHost = false
			next.IsHost = true
			promoted = next
			r.log.Info("host migrated", "from", p.ID, "to", next.ID)
		}
	}
	r.Broadcast(r.presence(p))
	if promoted != nil {
		r.Broadcast(r.presence(promoted))
	}
	if r.phase == genpb.Phase_PHASE_LOBBY {
		r.Broadcast(r.lobbyState())
	}
	r.log.Info("seat detached", "player", p.ID)

	// A disconnected player misses any drawing turn that occurs while they are
	// absent; if it is theirs right now, skip it immediately rather than
	// letting the room watch an empty timer run down.
	if r.phase == genpb.Phase_PHASE_DRAWING && r.artistID == p.ID {
		r.skipCurrentTurn()
	}

	// This seat just left the denominator. Everyone still here may already have
	// voted, in which case the window is over the moment the socket drops —
	// waiting out the remaining minutes on an absent player is exactly the stall
	// DESIGN.md "Active players" exists to prevent.
	if r.phase == genpb.Phase_PHASE_DISCUSSION {
		r.broadcastVoteCount()
		r.maybeResolve()
	}
}

// bestHost returns the longest-connected active player, excluding skip, or nil
// when there is no candidate. JoinedAt is the ordering key and is stable for
// the life of the seat.
func (r *Room) bestHost(skip *Player) *Player {
	var best *Player
	for _, p := range r.players {
		if p == skip || !p.Active() {
			continue
		}
		if best == nil || p.JoinedAt.Before(best.JoinedAt) {
			best = p
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// The shared liveness sweep
// ---------------------------------------------------------------------------

// checkLiveness runs once per SweepInterval on the single shared ticker — there
// are deliberately no per-connection timers. It expires grace windows and
// reports whether the room should close.
func (r *Room) checkLiveness() bool {
	now := time.Now()

	var expired []*Player
	for _, p := range r.players {
		if p.Connected || p.DisconnectedAt.IsZero() {
			continue
		}
		if now.Sub(p.DisconnectedAt) >= GraceWindow {
			expired = append(expired, p)
		}
	}
	for _, p := range expired {
		r.expireSeat(p)
	}
	if len(expired) > 0 && r.phase == genpb.Phase_PHASE_LOBBY {
		r.Broadcast(r.lobbyState())
	}

	// Cheap room GC. An empty room has nothing left to reconnect to it.
	if len(r.players) == 0 {
		return true
	}
	idleSince := time.Time{}
	for _, p := range r.players {
		if p.Connected {
			return false
		}
		t := p.DisconnectedAt
		if t.IsZero() {
			t = p.JoinedAt
		}
		if t.After(idleSince) {
			idleSince = t
		}
	}
	return !idleSince.IsZero() && now.Sub(idleSince) >= IdleTTL
}

// expireSeat runs when a player's 60 s grace window has run out.
func (r *Room) expireSeat(p *Player) {
	r.log.Info("grace window expired", "player", p.ID)

	if r.matchInProgress() {
		if p.ID == r.imposterID {
			// DESIGN.md:125 — end the match immediately and award a group win.
			// The outcome intentionally leaks the disconnected player's role.
			r.log.Info("imposter lost, group wins by default")
			r.endNow(genpb.WinnerSide_WINNER_SIDE_GROUP,
				genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_DISCONNECTED)
			return
		}
		// Out for good now, not merely dark: the seat and the word stay, but if
		// they come back they return as a spectator rather than an active player.
		p.Eliminated = true
		p.DisconnectedAt = time.Time{}
		delete(r.votes, p.ID)
		r.Broadcast(r.presence(p))
		r.reevaluateEnd()

		// The denominator just shrank. Republish the progress count so the UI is
		// not left showing "3 of 4", and re-check the early-resolve condition:
		// losing a seat can complete the tally exactly as a final vote would.
		if r.phase == genpb.Phase_PHASE_DISCUSSION {
			r.broadcastVoteCount()
			r.maybeResolve()
		}
		return
	}

	// Lobby or post-match: free the seat outright.
	r.removeSeat(p)
}

// onKick removes another player's seat at the host's request.
//
// Lobby only, and that restriction is the whole reason this is cheap: outside
// PHASE_LOBBY a seat holds a word, a place in the turn order and a place in the
// vote denominator, and unseating it would have to answer what happens when the
// target is the imposter — which cannot be answered without telling the room
// that they were. In the lobby a seat holds nothing, so removeSeat is the
// entire operation.
//
// The kick is not a ban. It destroys the seat token so the target cannot
// reconnect into that seat, but the room code still works and they may take a
// fresh seat; there is no stable identity here to ban on.
func (r *Room) onKick(p *Player, cid string, k *genpb.KickPlayer) {
	if r.phase != genpb.Phase_PHASE_LOBBY {
		r.SendError(p.ID, cid, ErrWrongPhase)
		return
	}
	if !p.IsHost {
		r.SendError(p.ID, cid, ErrNotHost)
		return
	}
	target := r.byID[k.GetTargetPlayerId()]
	// Self-kick is refused rather than treated as leaving: it would unseat the
	// host and migrate the role in one step, and there is no UI that wants
	// that. A host leaves by closing the tab.
	if target == nil || target == p {
		r.SendError(p.ID, cid, ErrInvalidCommand)
		return
	}

	// Told before the seat goes: SendReply resolves the socket through byID,
	// which removeSeat is about to empty. Uncorrelated — this answers the
	// host's command, not the target's.
	r.sendErrorCode(target.ID, "", genpb.ErrorCode_ERROR_CODE_KICKED,
		"The host removed you from the room. You can rejoin with the room code.")

	// Held across removeSeat, which unlinks the player from every index. The
	// close is requested after, and only after, the KICKED error is on the
	// target's queue, so the socket drains that frame before it goes.
	sess := target.outbound
	r.removeSeat(target)
	if sess != nil {
		// A kick that relied on the client closing voluntarily leaves the
		// removed player holding a live connection slot in a room that will
		// never send them anything again — and a client that ignores the error
		// holds it indefinitely.
		sess.Close()
	}
	r.log.Info("player kicked", "host", p.ID, "player", target.ID)
	// Carries the shrunk roster and the recomputed can_start. The target is no
	// longer in r.players, so this does not reach them.
	r.BroadcastReply(cid, r.lobbyState())
}

// removeSeat drops a seat entirely. Only legal outside a running match, where
// nobody holds a word.
func (r *Room) removeSeat(p *Player) {
	delete(r.byID, p.ID)
	delete(r.bySeatToken, p.SeatToken)
	delete(r.votes, p.ID)
	for i, q := range r.players {
		if q == p {
			r.players = append(r.players[:i], r.players[i+1:]...)
			break
		}
	}
	if p.IsHost {
		p.IsHost = false
		if next := r.bestHost(nil); next != nil {
			next.IsHost = true
			r.log.Info("host migrated", "from", p.ID, "to", next.ID)
			r.Broadcast(r.presence(next))
		}
	}
	r.log.Info("seat removed", "player", p.ID)
}
