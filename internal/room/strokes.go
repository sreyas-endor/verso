package room

// strokes.go — the append-only canvas log (IMPLEMENTATION_PLAN.md §4.7).
//
// The canvas is append-only evidence, not a sabotage tool (DESIGN.md:85):
// there is no erase, no undo, and no way to remove a committed stroke. All
// three server-side authorities are enforced here and are non-negotiable:
//
//  1. Stroke commands from anyone who is not the current artist are rejected.
//  2. Brush width is clamped, so one client cannot lag every other with an
//     absurdly thick line.
//  3. Total points per turn are capped, and so is the number of strokes. An
//     append-only log with no cap is a trivial memory-exhaustion vector, so
//     MaxStrokesPerTurn always holds. The effective stroke ceiling is now
//     settings-derived: the host's pen rule (DESIGN.md:104) lowers it further
//     and never raises it, and a spent budget only locks the pen for the rest
//     of the turn — it never ends the turn early (DESIGN.md:114).
//
// Colours are palette indices validated against the server-owned palette, never
// CSS strings. Coordinates keep their full signed int16 range on purpose: a
// stroke dragged past the canvas edge must survive the round trip rather than
// being chopped off.
//
// Every stroke event carries r.seq, monotonic for the life of the room, so a
// client that sees a gap recovers with RequestSnapshot.

import (
	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// artistGate is authority (1). It is the only place a stroke command may pass.
//
// A turn running out its TurnGrace window is still PHASE_DRAWING with the same
// artist, so points and the end for the open stroke pass here unchanged. Only
// StrokeBegin is turned away, in onStrokeBegin itself.
func (r *Room) artistGate(p *Player, cid string) bool {
	if r.phase != genpb.Phase_PHASE_DRAWING {
		r.SendError(p.ID, cid, ErrWrongPhase)
		return false
	}
	if p.ID != r.artistID {
		r.SendError(p.ID, cid, ErrNotArtist)
		return false
	}
	return true
}

// strokeCeiling is authority (3)'s stroke half: how many strokes this turn may
// hold under the host's pen rule (DESIGN.md:104). MaxStrokesPerTurn remains the
// anti-abuse ceiling for every rule; a handicap only ever cuts below it, and an
// unset or unknown rule draws freely.
func (r *Room) strokeCeiling() int {
	switch r.settings.GetPenRule() {
	case genpb.PenRule_PEN_RULE_ONE_LINE:
		return OneLineStrokes
	case genpb.PenRule_PEN_RULE_MAX_FIVE:
		return MaxFiveStrokes
	default:
		return MaxStrokesPerTurn
	}
}

func (r *Room) onStrokeBegin(p *Player, cid string, c *genpb.StrokeBegin) {
	if !r.artistGate(p, cid) {
		return
	}
	// Authority (3), part one: an out-of-range palette index is rejected, not
	// clamped — a silently repainted canvas hides the bug.
	if !ValidColorIndex(c.GetColorIndex()) || !ValidPoints(c.GetPoints()) {
		r.SendError(p.ID, cid, ErrInvalidCommand)
		return
	}
	// The turn's clock is out and the room is only waiting for the tail of the
	// stroke that was already under the pen (see TurnGrace). A new one is past
	// the buzzer. Silent, like the stroke cap below: the artist's own pen has
	// already closed on the same deadline, so this is a straggler, not news.
	if r.turnGrace {
		r.log.Debug("stroke begun after the turn expired, dropping", "player", p.ID)
		return
	}
	// A begin without a matching end means the client dropped a frame; commit
	// what is open rather than losing it.
	r.commitOpen(nil)

	if r.strokesThisTurn >= r.strokeCeiling() {
		// Fire-and-forget command, so this stays quiet: the artist's own gauge
		// already knows the budget is gone, and an error frame per dropped
		// pointerdown would be noise, not news.
		r.log.Debug("per-turn stroke cap reached, dropping", "player", p.ID)
		return
	}
	pts := r.takePoints(c.GetPoints())
	if len(pts) == 0 {
		return
	}

	r.strokesThisTurn++
	r.open = &openStroke{
		id:         r.nextStrokeID,
		colorIndex: c.GetColorIndex(),
		width:      ClampWidth(c.GetWidth()), // authority (2)
		points:     pts,
	}
	r.nextStrokeID++
	r.seq++

	r.Broadcast(EvStrokeBegan{&genpb.StrokeBegan{
		StrokeId:   r.open.id,
		ColorIndex: r.open.colorIndex,
		Width:      r.open.width,
		Points:     append([]int32(nil), r.open.points...),
		Seq:        r.seq,
	}})
}

func (r *Room) onStrokePoints(p *Player, cid string, c *genpb.StrokePoints) {
	if !r.artistGate(p, cid) {
		return
	}
	if r.open == nil {
		// Nothing to append to. Fire-and-forget command, so stay quiet.
		return
	}
	if !ValidPoints(c.GetPoints()) {
		r.SendError(p.ID, cid, ErrInvalidCommand)
		return
	}
	// The client-supplied stroke_id and seq are ignored entirely: there is
	// exactly one open stroke per artist, and the server is the sequencer.
	pts := r.takePoints(r.fitStroke(c.GetPoints()))
	if len(pts) == 0 {
		return
	}

	r.open.points = append(r.open.points, pts...)
	r.seq++
	r.Broadcast(EvStrokePoints{&genpb.StrokePoints{
		StrokeId: r.open.id,
		Points:   append([]int32(nil), pts...),
		Seq:      r.seq,
	}})
}

func (r *Room) onStrokeEnd(p *Player, cid string, c *genpb.StrokeEnd) {
	if !r.artistGate(p, cid) {
		return
	}
	if r.open == nil {
		return
	}
	// StrokeEnd.points is the RDP-simplified replacement for the whole stroke,
	// computed once on pointerup. Empty means keep exactly what was streamed.
	var replacement []int32
	if pts := c.GetPoints(); len(pts) > 0 {
		if !ValidPoints(pts) {
			r.SendError(p.ID, cid, ErrInvalidCommand)
			return
		}
		// A "simplification" that grows the stroke is not one; it also must not
		// buy the artist extra budget, so it is capped at what was streamed.
		if len(pts) <= len(r.open.points) {
			replacement = pts
		}
	}
	r.commitOpen(replacement)
	// The whole reason the turn was still open. The tail is in, so nothing is
	// left to wait for and the handoff runs now rather than at the end of a
	// window that has served its purpose.
	if r.turnGrace {
		r.endTurn()
	}
}

// commitOpen moves the in-flight stroke onto the append-only log and announces
// it. Safe to call with nothing open. replacement is the accepted simplified
// geometry, or nil to keep the streamed points.
func (r *Room) commitOpen(replacement []int32) {
	if r.open == nil {
		return
	}
	final := r.open.points
	var announce []int32
	if replacement != nil {
		final = replacement
		// Clients that receive points on StrokeEnded MUST replace the stroke
		// rather than append; an empty field means the streamed points stand.
		announce = append([]int32(nil), replacement...)
	}

	r.strokes = append(r.strokes, &genpb.Stroke{
		StrokeId:   r.open.id,
		ColorIndex: r.open.colorIndex,
		Width:      r.open.width,
		Points:     append([]int32(nil), final...),
	})
	r.seq++
	r.Broadcast(EvStrokeEnded{&genpb.StrokeEnded{
		StrokeId: r.open.id,
		Points:   announce,
		Seq:      r.seq,
	}})
	r.open = nil
}

// takePoints is authority (3): it trims an incoming batch to whatever is left
// of the per-turn budget and charges the turn for what it returns. Returning a
// short slice rather than rejecting the whole command keeps the canvas
// consistent for viewers when a prolific artist hits the ceiling.
func (r *Room) takePoints(pts []int32) []int32 {
	pairs := len(pts) / 2
	if pairs == 0 {
		return nil
	}
	budget := MaxPointsPerTurn - r.pointsThisTurn
	if budget <= 0 {
		return nil
	}
	if pairs > budget {
		pairs = budget
	}
	r.pointsThisTurn += pairs
	return append([]int32(nil), pts[:pairs*2]...)
}

// fitStroke trims a batch to what the currently open stroke can still hold.
func (r *Room) fitStroke(pts []int32) []int32 {
	room := 2*MaxPointsPerStroke - len(r.open.points)
	if room <= 0 {
		return nil
	}
	if len(pts) > room {
		return pts[:room-room%2]
	}
	return pts
}
