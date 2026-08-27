package room

// bench_test.go — the server-side numbers PERFORMANCE_OPTIMIZATION_PLAN.md §6.2
// asks for before S4 and S5 are decided.
//
// Every benchmark here runs the room's own code with the actor loop stopped and
// the state set up by hand. That is safe precisely because of the actor design:
// one goroutine owns every field, and a benchmark that never starts Run is that
// goroutine. It is also the only way to measure the handlers rather than the
// channel plumbing around them.
//
// What these are FOR:
//
//   BenchmarkStrokePointsBroadcast   S4. Cost per recipient of one broadcast.
//                                    If it scales linearly in recipients, the
//                                    per-connection marshal is the term S4
//                                    proposes to share; if the room-side work
//                                    dominates, sharing buys little.
//   BenchmarkMarshal*                S4. The half a shared encoded frame would
//                                    actually remove.
//   BenchmarkSnapshot*               S3 and S5. What one RequestSnapshot costs
//                                    at realistic and worst-case canvas sizes,
//                                    which is what the snapshot bucket exists
//                                    to bound and what sizes the heap budget.

import (
	"log/slog"
	mrand "math/rand/v2"
	"testing"

	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// benchSink is a Session that throws everything away at the same cost the real
// one pays: one non-blocking channel send into a queue that is always drained.
type benchSink struct{ ch chan *genpb.ServerEvent }

func newBenchSink() *benchSink { return &benchSink{ch: make(chan *genpb.ServerEvent, 1024)} }

func (s *benchSink) Send(ev *genpb.ServerEvent) {
	select {
	case s.ch <- ev:
	default:
	}
}

func (s *benchSink) Close() {}

func (s *benchSink) drain() {
	for {
		select {
		case <-s.ch:
		default:
			return
		}
	}
}

// benchRoom builds a room with n seats parked in PHASE_DRAWING, with seat 0 as
// the artist. The actor is never started: this goroutine is the owner.
func benchRoom(n int) (*Room, []*benchSink) {
	r := New("BNCH", "host", Options{
		Deck:   pairDeck{"CAT", "DOG"},
		Rand:   mrand.New(mrand.NewPCG(1, 2)),
		Logger: slog.New(slog.DiscardHandler),
	})

	sinks := make([]*benchSink, 0, n)
	host := r.players[0]
	s0 := newBenchSink()
	host.outbound = s0
	host.Connected = true
	sinks = append(sinks, s0)

	for i := 1; i < n; i++ {
		sk := newBenchSink()
		if _, _, err := r.seatOnActor("p", sk); err != nil {
			panic(err)
		}
		sinks = append(sinks, sk)
	}

	r.phase = genpb.Phase_PHASE_DRAWING
	r.artistID = host.ID
	for _, sk := range sinks {
		sk.drain()
	}
	return r, sinks
}

// benchPoints is one client batch: STROKE_BATCH_MS of pointer samples, which is
// what actually travels 20 times a second while somebody is drawing.
func benchPoints(pairs int, seed int32) []int32 {
	out := make([]int32, 0, pairs*2)
	for i := range pairs {
		out = append(out, seed+int32(i), seed-int32(i))
	}
	return out
}

// BenchmarkStrokePointsBroadcast is the hot path of a live drawing turn: admit
// one batch, then hand the resulting event to every seat in the room.
//
// Run with -benchmem across recipient counts to see whether the cost is in the
// admission (per command) or in the delivery (per recipient). S4 is only worth
// doing if it is the latter — and even then, only the marshal that happens
// afterwards in each write pump, which this does not include.
func BenchmarkStrokePointsBroadcast(b *testing.B) {
	for _, recipients := range []int{1, 10} {
		b.Run(name(recipients), func(b *testing.B) {
			r, sinks := benchRoom(recipients)
			r.onStrokeBegin(r.players[0], "", &genpb.StrokeBegin{
				ColorIndex: 1, Width: 6, Points: benchPoints(2, 0)})

			const batch = 8 // pairs, one STROKE_BATCH_MS window of samples

			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				// Both budgets are finite — the per-turn point cap and the
				// per-stroke one — and past either the handler returns early
				// without broadcasting. Resetting keeps the benchmark measuring
				// the work rather than the refusal.
				if r.pointsThisTurn > MaxPointsPerTurn-4*batch ||
					len(r.open.points) > 2*MaxPointsPerStroke-4*batch {
					b.StopTimer()
					r.pointsThisTurn = 0
					r.open.points = r.open.points[:2]
					for _, sk := range sinks {
						sk.drain()
					}
					b.StartTimer()
				}
				r.onStrokePoints(r.players[0], "", &genpb.StrokePoints{
					Points: benchPoints(batch, int32(i%1000)),
				})
			}
		})
	}
}

// BenchmarkMarshalStrokePoints is the term S4 would share: one broadcast frame,
// encoded once per recipient by their own write pump.
//
// Multiply by the recipient count to get what a full room pays per stroke
// batch, and compare against BenchmarkStrokePointsBroadcast to see how much of
// the total that actually is.
func BenchmarkMarshalStrokePoints(b *testing.B) {
	ev := &genpb.ServerEvent{Evt: &genpb.ServerEvent_StrokePoints{
		StrokePoints: &genpb.StrokePoints{StrokeId: 7, Seq: 99, Points: benchPoints(8, 3)}}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := proto.Marshal(ev); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSnapshotBuild and BenchmarkSnapshotMarshal split the cost of one
// RequestSnapshot into the room's half and the write pump's half.
//
// "full" is the per-turn ceiling: MaxPointsPerTurn coordinate pairs spread over
// MaxStrokesPerTurn strokes, which is the largest canvas a single turn can
// leave behind. It is the number the snapshot bucket (S3) is sized against and
// the one the heap budget (S5) has to allow for per room.
func BenchmarkSnapshotBuild(b *testing.B) {
	for _, size := range []string{"empty", "ordinary", "full"} {
		b.Run(size, func(b *testing.B) {
			r, _ := benchRoom(10)
			fillCanvas(r, size)
			id := r.players[0].ID

			b.ReportAllocs()
			for b.Loop() {
				if r.viewFor(id) == nil {
					b.Fatal("no view")
				}
			}
		})
	}
}

func BenchmarkSnapshotMarshal(b *testing.B) {
	for _, size := range []string{"empty", "ordinary", "full"} {
		b.Run(size, func(b *testing.B) {
			r, _ := benchRoom(10)
			fillCanvas(r, size)
			ev := EvSnapshot{r.viewFor(r.players[0].ID)}.Envelope("")

			raw, err := proto.Marshal(ev)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := proto.Marshal(ev); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// fillCanvas puts a stroke log of the named size onto the room, going through
// the real admission path so the caps are the ones the room actually applies.
func fillCanvas(r *Room, size string) {
	strokes, pairs := 0, 0
	switch size {
	case "empty":
		return
	case "ordinary":
		// A turn somebody drew properly but not obsessively.
		strokes, pairs = 20, 40
	case "full":
		// The per-turn ceiling, exactly.
		strokes, pairs = MaxStrokesPerTurn, MaxPointsPerTurn/MaxStrokesPerTurn
	}
	p := r.players[0]
	for i := range strokes {
		r.onStrokeBegin(p, "", &genpb.StrokeBegin{
			ColorIndex: int32(i % PaletteSize), Width: 6, Points: benchPoints(2, int32(i))})
		r.onStrokePoints(p, "", &genpb.StrokePoints{Points: benchPoints(pairs-2, int32(i))})
		r.onStrokeEnd(p, "", &genpb.StrokeEnd{})
	}
}

func name(n int) string {
	if n == 1 {
		return "1-recipient"
	}
	return "10-recipients"
}
