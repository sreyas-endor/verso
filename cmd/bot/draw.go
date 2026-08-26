package main

// draw.go — the bot's drawing hand.
//
// A bot draws only on its own turn, and only the shape a real client can
// produce: StrokeBegin, several batched StrokePoints, StrokeEnd. Everything the
// server enforces is respected here rather than probed, because a harness that
// only ever sends illegal traffic never proves the legal path works:
//
//   - one batch per room.StrokeBatchWindow (50 ms), never faster;
//   - coordinates inside the 4096x3072 grid;
//   - a palette index inside room.PaletteSize;
//   - a width inside room.MinStrokeWidth..MaxStrokeWidth;
//   - a per-turn budget well under room.MaxPointsPerTurn, and a per-stroke
//     count well under room.MaxPointsPerStroke.
//
// The one deliberate exception is DrawPlan.ClampProbe, which sends an absurd
// width on the first stroke of the match so the bot can assert the server
// clamped it on the way back out.

import (
	mrand "math/rand/v2"

	"github.com/sreyas-endor/verso/internal/room"
)

// DrawPlan is how much a bot draws on one turn.
type DrawPlan struct {
	// Strokes per turn.
	Strokes int
	// Batches of points sent after each StrokeBegin.
	BatchesPerStroke int
	// Coordinate pairs in one batch. Three to six is what a 50 ms window
	// actually yields from a pointer device.
	PointsPerBatch int
	// Width requested. Clamped by the server; keep it legal.
	Width int32
	// ClampProbe sends width 9999 on the very first stroke, and the bot then
	// checks the broadcast came back at room.MaxStrokeWidth.
	ClampProbe bool
}

// DefaultDrawPlan is a plausible scribble: three short strokes, about 75
// coordinate pairs, roughly 1.2 s of wall time out of a 5 s turn.
func DefaultDrawPlan() DrawPlan {
	return DrawPlan{
		Strokes:          3,
		BatchesPerStroke: 6,
		PointsPerBatch:   4,
		Width:            6,
	}
}

func (p DrawPlan) withDefaults() DrawPlan {
	if p.Strokes <= 0 {
		p.Strokes = 3
	}
	if p.BatchesPerStroke <= 0 {
		p.BatchesPerStroke = 6
	}
	if p.PointsPerBatch <= 0 {
		p.PointsPerBatch = 4
	}
	if p.Width < room.MinStrokeWidth {
		p.Width = 6
	}
	if p.Width > room.MaxStrokeWidth {
		p.Width = room.MaxStrokeWidth
	}
	return p
}

// penStage is where the pen is in one stroke.
type penStage int

const (
	stageBegin penStage = iota
	stagePoints
	stageEnd
	stageIdle
)

// pen is the per-turn drawing state machine. It advances one step per batch
// tick, so the 50 ms window is enforced by construction rather than by a sleep.
type pen struct {
	plan  DrawPlan
	rnd   *mrand.Rand
	stage penStage

	strokeIdx int
	batchIdx  int

	// x, y is the tip, in grid coordinates.
	x, y int32
	// dx, dy is the current heading; it is perturbed each sample so the result
	// looks like a hand rather than a line.
	dx, dy int32

	color int32
	width int32

	// streamed is every coordinate pair emitted for the open stroke, kept so
	// StrokeEnd can offer a genuine simplification of it.
	streamed []int32

	// pairsThisTurn is the bot's own accounting against MaxPointsPerTurn.
	pairsThisTurn int
}

func newPen(plan DrawPlan, rnd *mrand.Rand, firstOfMatch bool) *pen {
	plan = plan.withDefaults()
	p := &pen{
		plan:  plan,
		rnd:   rnd,
		stage: stageBegin,
		x:     int32(rnd.IntN(room.GridWidth-1200) + 600),
		y:     int32(rnd.IntN(room.GridHeight-900) + 450),
		dx:    int32(rnd.IntN(90) - 45),
		dy:    int32(rnd.IntN(90) - 45),
		color: int32(rnd.IntN(room.PaletteSize)),
		width: plan.Width,
	}
	if plan.ClampProbe && firstOfMatch {
		p.width = 9999
	}
	return p
}

// step advances the pen one batch tick and reports what to send. Exactly one of
// the three return values is meaningful, selected by the returned stage.
func (p *pen) step() (stage penStage, points []int32, color, width int32) {
	switch p.stage {
	case stageBegin:
		if p.strokeIdx >= p.plan.Strokes || p.budgetLeft() < 1 {
			p.stage = stageIdle
			return stageIdle, nil, 0, 0
		}
		p.streamed = p.streamed[:0]
		p.jitterHeading()
		pt := p.sample()
		p.streamed = append(p.streamed, pt...)
		p.batchIdx = 0
		p.stage = stagePoints
		return stageBegin, pt, p.color, p.width

	case stagePoints:
		n := p.plan.PointsPerBatch
		if left := p.budgetLeft(); left < n {
			n = left
		}
		if n <= 0 {
			p.stage = stageEnd
			return p.step()
		}
		batch := make([]int32, 0, n*2)
		for range n {
			p.jitterHeading()
			batch = append(batch, p.sample()...)
		}
		p.streamed = append(p.streamed, batch...)
		p.batchIdx++
		if p.batchIdx >= p.plan.BatchesPerStroke {
			p.stage = stageEnd
		}
		return stagePoints, batch, 0, 0

	case stageEnd:
		simplified := decimate(p.streamed)
		p.strokeIdx++
		// A fresh colour and width per stroke; the width probe is for the first
		// stroke only, so the rest of the match uses a legal one.
		p.color = int32(p.rnd.IntN(room.PaletteSize))
		p.width = p.plan.Width
		if p.strokeIdx >= p.plan.Strokes {
			p.stage = stageIdle
		} else {
			p.stage = stageBegin
		}
		return stageEnd, simplified, 0, 0

	default:
		return stageIdle, nil, 0, 0
	}
}

// done reports whether the pen has nothing left to send this turn.
func (p *pen) done() bool { return p.stage == stageIdle }

// budgetLeft is how many more coordinate pairs this pen will allow itself. It
// stays an order of magnitude under the server's per-turn cap on purpose: this
// is a well-behaved client, and the cap is proved elsewhere.
func (p *pen) budgetLeft() int {
	const selfCap = room.MaxPointsPerTurn / 4
	left := selfCap - p.pairsThisTurn
	if left < 0 {
		return 0
	}
	if headroom := room.MaxPointsPerStroke - len(p.streamed)/2; headroom < left {
		left = headroom
	}
	return left
}

// sample moves the tip one step and returns it as one interleaved pair.
func (p *pen) sample() []int32 {
	p.x = clampCoord(p.x+p.dx, room.GridWidth)
	p.y = clampCoord(p.y+p.dy, room.GridHeight)
	p.pairsThisTurn++
	return []int32{p.x, p.y}
}

// jitterHeading nudges the direction so the stroke curves.
func (p *pen) jitterHeading() {
	p.dx = clampCoord(p.dx+int32(p.rnd.IntN(31)-15), 120)
	p.dy = clampCoord(p.dy+int32(p.rnd.IntN(31)-15), 120)
	if p.dx == 0 && p.dy == 0 {
		p.dx = 17
	}
}

// clampCoord keeps v inside [0, limit).
func clampCoord(v, limit int32) int32 {
	if v < 0 {
		return 0
	}
	if v >= limit {
		return limit - 1
	}
	return v
}

// decimate keeps every second coordinate pair. It stands in for the client's
// Ramer-Douglas-Peucker pass: the point of sending it is that the replacement
// is never longer than what was streamed, which is the only property the server
// checks (internal/room/strokes.go, onStrokeEnd).
func decimate(pts []int32) []int32 {
	if len(pts) < 8 {
		// Too short to be worth replacing; an empty StrokeEnd keeps what was
		// streamed, which is also a path worth exercising.
		return nil
	}
	out := make([]int32, 0, len(pts)/2+2)
	for i := 0; i+1 < len(pts); i += 4 {
		out = append(out, pts[i], pts[i+1])
	}
	// Always keep the true last sample: dropping it would visibly shorten the
	// stroke.
	if n := len(pts); out[len(out)-2] != pts[n-2] || out[len(out)-1] != pts[n-1] {
		out = append(out, pts[n-2], pts[n-1])
	}
	return out
}
