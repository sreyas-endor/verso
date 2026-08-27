package room

// strokes_test.go — the three server-side stroke authorities
// (IMPLEMENTATION_PLAN.md §4.7, milestone 8) and the append-only invariant
// (DESIGN.md:82).
//
//  1. A stroke command from anyone who is not the current artist is rejected.
//  2. Brush width is clamped server-side.
//  3. Total points per turn are capped, and the stroke ceiling is whatever the
//     host's pen rule allows (DESIGN.md:104).
//
// Plus the two properties that make the canvas evidence rather than a sabotage
// tool: coordinates are validated but NOT clipped to the grid, and nothing a
// client can send erases, shrinks or rewrites a committed stroke.

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	mrand "math/rand/v2"

	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

type strkDeck struct{}

func (strkDeck) Pair(genpb.Difficulty, *mrand.Rand, []string) (string, string) { return "CAT", "DOG" }

type strkSock struct{ ch chan *genpb.ServerEvent }

func newStrkSock() *strkSock { return &strkSock{ch: make(chan *genpb.ServerEvent, 32768)} }

// The room's Session contract. These tests only ever read frames back, so Close
// has nothing to record.
func (s *strkSock) Send(ev *genpb.ServerEvent) {
	select {
	case s.ch <- ev:
	default:
	}
}

func (s *strkSock) Close() {}

func (s *strkSock) drain() []*genpb.ServerEvent {
	var out []*genpb.ServerEvent
	for {
		select {
		case e := <-s.ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

func strkGet[T any](r *Room, fn func(*Room) T) T {
	var v T
	if err := r.do(func() { v = fn(r) }); err != nil {
		panic(err)
	}
	return v
}

// strkState is everything the stroke layer owns, read in one hop onto the room
// goroutine so a before/after comparison cannot tear.
type strkState struct {
	phase    genpb.Phase
	artist   string
	strokes  int
	points   int // committed coordinate values, both axes
	openID   int32
	openLen  int
	openW    int32
	seq      int32
	perTurnP int
	perTurnS int
	nextID   int32
	log      []byte // the whole committed log, marshalled
}

func strkSnap(r *Room) strkState {
	return strkGet(r, func(r *Room) strkState {
		st := strkState{
			phase: r.phase, artist: r.artistID, strokes: len(r.strokes),
			seq: r.seq, perTurnP: r.pointsThisTurn, perTurnS: r.strokesThisTurn,
			nextID: r.nextStrokeID, openID: -1, openLen: -1, openW: -1,
		}
		for _, s := range r.strokes {
			st.points += len(s.GetPoints())
			b, err := proto.Marshal(s)
			if err != nil {
				panic(err)
			}
			st.log = append(st.log, b...)
		}
		if r.open != nil {
			st.openID, st.openLen, st.openW = r.open.id, len(r.open.points), r.open.width
		}
		return st
	})
}

// strkMatch is a started match parked in PHASE_DRAWING on the first turn.
type strkMatch struct {
	r      *Room
	ids    []string
	socks  []*strkSock
	cancel context.CancelFunc
}

func (m *strkMatch) idx(id string) int {
	for i, v := range m.ids {
		if v == id {
			return i
		}
	}
	return -1
}

func (m *strkMatch) submit(i int, c *genpb.ClientCommand) {
	m.r.Submit(Command{PlayerID: m.ids[i], Out: m.socks[i], Cmd: c})
}

// artist returns the index of the current artist, and the index of somebody who
// is not.
func (m *strkMatch) artist(t *testing.T) (int, int) {
	t.Helper()
	a := m.idx(strkGet(m.r, func(r *Room) string { return r.artistID }))
	if a < 0 {
		t.Fatal("no current artist")
	}
	other := (a + 1) % len(m.ids)
	return a, other
}

func (m *strkMatch) drainAll() {
	for _, s := range m.socks {
		s.drain()
	}
}

// strkErrors pulls the error codes out of one socket's queue.
func strkErrors(evs []*genpb.ServerEvent) []genpb.ErrorCode {
	var out []genpb.ErrorCode
	for _, e := range evs {
		if er := e.GetError(); er != nil {
			out = append(out, er.GetCode())
		}
	}
	return out
}

func strkHas(codes []genpb.ErrorCode, want genpb.ErrorCode) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

// strkStart seats n players, starts the match, and advances to the first
// drawing turn. Call inside synctest.Test.
func strkStart(t *testing.T, n int) *strkMatch {
	t.Helper()
	return strkStartRule(t, n, genpb.PenRule_PEN_RULE_UNSPECIFIED)
}

// strkStartRule is strkStart with the host's pen rule supplied. UNSPECIFIED
// leaves the field unset, which is what a client that has never heard of the
// setting sends.
func strkStartRule(t *testing.T, n int, rule genpb.PenRule) *strkMatch {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	r := New("STRK", "host", Options{
		Deck: strkDeck{},
		Rand: mrand.New(mrand.NewPCG(5, 9)),
		Settings: &genpb.MatchSettings{
			MaxRounds: 2, DrawSeconds: 30, DiscussSeconds: 30, PenRule: rule,
		},
	})
	hostID, hostTok := r.HostSeat()
	go r.run(ctx)

	s0 := newStrkSock()
	if _, err := r.attach(hostTok, s0); err != nil {
		t.Fatal(err)
	}
	m := &strkMatch{r: r, ids: []string{hostID}, socks: []*strkSock{s0}, cancel: cancel}
	for i := 1; i < n; i++ {
		sk := newStrkSock()
		id, _, err := r.seat("player", sk)
		if err != nil {
			t.Fatal(err)
		}
		m.ids = append(m.ids, id)
		m.socks = append(m.socks, sk)
	}
	t.Cleanup(func() {
		cancel()
		synctest.Wait()
	})

	for i := range m.ids {
		m.submit(i, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}})
	}
	synctest.Wait()
	m.submit(0, &genpb.ClientCommand{
		Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}}})
	synctest.Wait()

	// Assignment hands off to a PHASE_INTERMISSION before the first turn goes
	// live (phase.go, beginIntermission), so both clocks have to run out.
	time.Sleep(AssignDuration + time.Millisecond)
	synctest.Wait()
	time.Sleep(strkIntermission(r))
	synctest.Wait()

	if ph := strkGet(r, func(r *Room) genpb.Phase { return r.phase }); ph != genpb.Phase_PHASE_DRAWING {
		t.Fatalf("phase = %v, want DRAWING", ph)
	}
	m.drainAll()
	return m
}

// strkIntermission is the room's own clamped handoff length, so a test never
// hard-codes a duration the settings could change.
func strkIntermission(r *Room) time.Duration {
	return strkGet(r, func(r *Room) time.Duration {
		return time.Duration(r.settings.GetIntermissionSeconds()) * time.Second
	})
}

// strkPoints builds a flat interleaved batch of n coordinate pairs.
func strkPoints(n int, x0 int32) []int32 {
	out := make([]int32, 0, n*2)
	for i := range n {
		out = append(out, x0+int32(i%1000), int32(i%700))
	}
	return out
}

// stroke draws one complete stroke: down, one batch, up. A single coordinate
// pair per stroke keeps the point budget out of the way, so only the stroke
// ceiling can ever be what stops a caller.
func (m *strkMatch) stroke(i int, x int32) {
	m.submit(i, &genpb.ClientCommand{
		Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
			ColorIndex: 1, Width: 4, Points: []int32{x, x}}}})
	m.submit(i, &genpb.ClientCommand{
		Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}})
}

// nextTurn runs the live turn's clock out and lands on the following one. The
// per-turn budget is zeroed in beginTurn, on the far side of the intermission
// handoff, so both clocks have to run out before the reset is observable.
func (m *strkMatch) nextTurn(t *testing.T) {
	t.Helper()
	draw := strkGet(m.r, func(r *Room) int32 { return r.settings.GetDrawSeconds() })
	time.Sleep(time.Duration(draw)*time.Second + time.Millisecond)
	synctest.Wait()
	time.Sleep(strkIntermission(m.r))
	synctest.Wait()
	if ph := strkGet(m.r, func(r *Room) genpb.Phase { return r.phase }); ph != genpb.Phase_PHASE_DRAWING {
		t.Fatalf("nextTurn: phase = %v, want DRAWING", ph)
	}
	m.drainAll()
}

// ---------------------------------------------------------------------------
// Authority 1 — only the current artist may draw
// ---------------------------------------------------------------------------

func TestStrokeFromANonArtistIsRejectedAndMutatesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 4)
		_, other := m.artist(t)

		before := strkSnap(m.r)

		// All three stroke verbs, from somebody whose turn it is not.
		m.submit(other, &genpb.ClientCommand{Cid: "b",
			Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 2, Width: 8, Points: strkPoints(50, 0)}}})
		m.submit(other, &genpb.ClientCommand{Cid: "p",
			Cmd: &genpb.ClientCommand_StrokePoints{StrokePoints: &genpb.StrokePoints{
				Points: strkPoints(50, 100)}}})
		m.submit(other, &genpb.ClientCommand{Cid: "e",
			Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}})
		synctest.Wait()

		after := strkSnap(m.r)
		if after.strokes != before.strokes || after.points != before.points ||
			after.seq != before.seq || after.openLen != before.openLen ||
			after.perTurnP != before.perTurnP || after.perTurnS != before.perTurnS ||
			after.nextID != before.nextID {
			t.Fatalf("a non-artist mutated stroke state\nbefore %+v\nafter  %+v", before, after)
		}

		codes := strkErrors(m.socks[other].drain())
		n := 0
		for _, c := range codes {
			if c == genpb.ErrorCode_ERROR_CODE_NOT_ARTIST {
				n++
			}
		}
		if n != 3 {
			t.Fatalf("got %d NOT_ARTIST rejections for 3 commands, codes = %v", n, codes)
		}

		// And nobody else saw a thing: a rejected stroke must not be relayed.
		for i, s := range m.socks {
			if i == other {
				continue
			}
			for _, e := range s.drain() {
				switch e.GetEvt().(type) {
				case *genpb.ServerEvent_StrokeBegan, *genpb.ServerEvent_StrokePoints,
					*genpb.ServerEvent_StrokeEnded:
					t.Fatalf("player %d received a stroke event from a non-artist", i)
				}
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Authority 2 — brush width is clamped server-side
// ---------------------------------------------------------------------------

func TestBrushWidthIsClampedServerSide(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 3)
		artist, _ := m.artist(t)

		cases := []struct{ sent, want int32 }{
			{99999, MaxStrokeWidth},
			{MaxStrokeWidth + 1, MaxStrokeWidth},
			{0, MinStrokeWidth},
			{-1000, MinStrokeWidth},
			{4, 4},
		}
		for _, c := range cases {
			m.drainAll()
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
					ColorIndex: 1, Width: c.sent, Points: []int32{10, 10}}}})
			synctest.Wait()

			if got := strkSnap(m.r).openW; got != c.want {
				t.Errorf("width %d opened at %d, want %d", c.sent, got, c.want)
			}
			// The clamp must be on the wire too, not merely in memory: viewers
			// render from the broadcast, not from the server's copy.
			var began *genpb.StrokeBegan
			for _, e := range m.socks[artist].drain() {
				if b := e.GetStrokeBegan(); b != nil {
					began = b
				}
			}
			if began == nil {
				t.Fatalf("width %d produced no StrokeBegan", c.sent)
			}
			if began.GetWidth() != c.want {
				t.Errorf("width %d broadcast as %d, want %d", c.sent, began.GetWidth(), c.want)
			}

			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}})
			synctest.Wait()
		}

		// Nothing out of range survived into the committed log.
		strkGet(m.r, func(r *Room) struct{} {
			for i, s := range r.strokes {
				if w := s.GetWidth(); w < MinStrokeWidth || w > MaxStrokeWidth {
					t.Errorf("committed stroke %d has width %d, outside [%d,%d]",
						i, w, MinStrokeWidth, MaxStrokeWidth)
				}
			}
			return struct{}{}
		})
	})
}

// ---------------------------------------------------------------------------
// Authority 3 — the per-turn point cap
// ---------------------------------------------------------------------------

func TestPerTurnPointCapBoundsTheLog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 3)
		artist, _ := m.artist(t)

		// Flood: far more points than a turn may hold, spread over more strokes
		// than one stroke can hold, each batch at the per-message maximum.
		const batches = 40
		for i := range batches {
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
					ColorIndex: 1, Width: 4, Points: strkPoints(MaxPointsPerStroke, int32(i))}}})
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokePoints{StrokePoints: &genpb.StrokePoints{
					Points: strkPoints(MaxPointsPerStroke, int32(i))}}})
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}})
			synctest.Wait()
			m.drainAll()
		}

		st := strkSnap(m.r)
		offered := batches * 2 * MaxPointsPerStroke
		// The client offered ~96,000 points; the turn may keep 4,000.
		if st.points > 2*MaxPointsPerTurn {
			t.Fatalf("committed %d coordinate values from %d offered pairs, "+
				"cap allows %d values", st.points, offered, 2*MaxPointsPerTurn)
		}
		if st.perTurnP > MaxPointsPerTurn {
			t.Fatalf("pointsThisTurn = %d, cap is %d", st.perTurnP, MaxPointsPerTurn)
		}
		if st.perTurnP != MaxPointsPerTurn {
			t.Fatalf("flooding did not reach the cap: pointsThisTurn = %d, want %d",
				st.perTurnP, MaxPointsPerTurn)
		}
		if st.strokes > MaxStrokesPerTurn {
			t.Fatalf("committed %d strokes in one turn, cap is %d", st.strokes, MaxStrokesPerTurn)
		}

		// The log has genuinely stopped growing: another flood adds nothing.
		for range 10 {
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
					ColorIndex: 1, Width: 4, Points: strkPoints(MaxPointsPerStroke, 7)}}})
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}})
			synctest.Wait()
		}
		m.drainAll()
		if got := strkSnap(m.r); got.points != st.points || got.strokes != st.strokes {
			t.Fatalf("the log grew past the cap: %d strokes / %d values, was %d / %d",
				got.strokes, got.points, st.strokes, st.points)
		}

		// The budget is per TURN, so the next artist starts with a full one. The
		// reset happens in beginTurn, on the far side of the intermission
		// handoff, so both clocks have to run out before it is observable.
		time.Sleep(time.Duration(strkGet(m.r, func(r *Room) int32 { return r.settings.GetDrawSeconds() }))*time.Second + time.Millisecond)
		synctest.Wait()
		time.Sleep(strkIntermission(m.r))
		synctest.Wait()
		if got := strkSnap(m.r); got.perTurnP != 0 || got.perTurnS != 0 {
			t.Fatalf("the new turn inherited a spent budget: %d points / %d strokes",
				got.perTurnP, got.perTurnS)
		}
		t.Logf("offered %d pairs, kept %d coordinate values across %d strokes",
			offered, st.points, st.strokes)
	})
}

func TestPerTurnStrokeCapIsEnforced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 3)
		artist, _ := m.artist(t)

		// One pair per stroke, so the point budget can never be what stops this.
		for range MaxStrokesPerTurn + 50 {
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
					ColorIndex: 1, Width: 4, Points: []int32{1, 1}}}})
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}})
		}
		synctest.Wait()
		m.drainAll()

		st := strkSnap(m.r)
		if st.perTurnS != MaxStrokesPerTurn {
			t.Fatalf("strokesThisTurn = %d, want the cap %d", st.perTurnS, MaxStrokesPerTurn)
		}
		if st.strokes > MaxStrokesPerTurn {
			t.Fatalf("committed %d strokes, cap is %d", st.strokes, MaxStrokesPerTurn)
		}
	})
}

// ---------------------------------------------------------------------------
// Pen rules — the host's per-turn stroke handicap (DESIGN.md:104)
// ---------------------------------------------------------------------------

// TestOneLinePenRuleAllowsExactlyOneStroke — "lifting the pen finishes their
// drawing" (DESIGN.md:110). The second pointerdown of the turn buys nothing,
// and what the artist already committed is not touched by the refusal: the
// rule locks the pen, it does not roll the canvas back.
func TestOneLinePenRuleAllowsExactlyOneStroke(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStartRule(t, 3, genpb.PenRule_PEN_RULE_ONE_LINE)
		artist, _ := m.artist(t)

		m.stroke(artist, 10)
		synctest.Wait()
		m.drainAll()
		before := strkSnap(m.r)
		if before.strokes != 1 || before.perTurnS != 1 {
			t.Fatalf("first stroke: %d committed / %d charged, want 1 / 1",
				before.strokes, before.perTurnS)
		}

		m.stroke(artist, 20)
		synctest.Wait()

		after := strkSnap(m.r)
		if after.strokes != 1 {
			t.Fatalf("committed %d strokes under ONE_LINE, want %d", after.strokes, OneLineStrokes)
		}
		if after.perTurnS != OneLineStrokes {
			t.Fatalf("strokesThisTurn = %d, want the ceiling %d", after.perTurnS, OneLineStrokes)
		}
		if string(after.log) != string(before.log) {
			t.Fatal("the refused stroke rewrote the committed log")
		}
		if after.openLen != -1 {
			t.Fatalf("a stroke is open after the budget was spent: %d points", after.openLen)
		}
		// The pen is locked, not the turn: nobody was moved along by running out.
		if after.phase != genpb.Phase_PHASE_DRAWING || after.artist != before.artist {
			t.Fatalf("spending the budget ended the turn: phase %v, artist %q",
				after.phase, after.artist)
		}
		// A dropped StrokeBegin is fire-and-forget, so the artist is told nothing.
		if codes := strkErrors(m.socks[artist].drain()); len(codes) != 0 {
			t.Fatalf("the dropped stroke sent errors: %v", codes)
		}
	})
}

// TestMaxFivePenRuleStopsAtTheSixthStroke — five strokes for the whole turn
// (DESIGN.md:112). The sixth is dropped, not trimmed to fit.
func TestMaxFivePenRuleStopsAtTheSixthStroke(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStartRule(t, 3, genpb.PenRule_PEN_RULE_MAX_FIVE)
		artist, _ := m.artist(t)

		for i := range MaxFiveStrokes {
			m.stroke(artist, int32(i))
		}
		synctest.Wait()
		m.drainAll()
		before := strkSnap(m.r)
		if before.strokes != MaxFiveStrokes {
			t.Fatalf("committed %d of %d allowed strokes", before.strokes, MaxFiveStrokes)
		}

		m.stroke(artist, 99)
		synctest.Wait()

		after := strkSnap(m.r)
		if after.strokes != MaxFiveStrokes {
			t.Fatalf("committed %d strokes under MAX_FIVE, want %d", after.strokes, MaxFiveStrokes)
		}
		if after.perTurnS != MaxFiveStrokes {
			t.Fatalf("strokesThisTurn = %d, want the ceiling %d", after.perTurnS, MaxFiveStrokes)
		}
		if string(after.log) != string(before.log) {
			t.Fatal("the sixth stroke rewrote the committed log")
		}
		if after.phase != genpb.Phase_PHASE_DRAWING {
			t.Fatalf("the sixth stroke ended the turn: phase = %v", after.phase)
		}
	})
}

// TestPenRuleBudgetResetsEveryTurn — the handicap is per turn, not per match.
// The next artist inherits a fresh allowance however thoroughly the previous
// one spent theirs, and the canvas they inherit still holds every earlier mark.
func TestPenRuleBudgetResetsEveryTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStartRule(t, 3, genpb.PenRule_PEN_RULE_ONE_LINE)
		first, _ := m.artist(t)

		m.stroke(first, 10)
		m.stroke(first, 20) // spent: dropped
		synctest.Wait()
		m.drainAll()
		if st := strkSnap(m.r); st.strokes != 1 {
			t.Fatalf("first turn committed %d strokes, want 1", st.strokes)
		}

		m.nextTurn(t)
		st := strkSnap(m.r)
		if st.perTurnS != 0 {
			t.Fatalf("the new turn inherited a spent budget: strokesThisTurn = %d", st.perTurnS)
		}
		if st.artist == m.ids[first] {
			t.Fatal("the pen never left the first artist")
		}
		second := m.idx(st.artist)

		m.stroke(second, 30)
		synctest.Wait()
		if got := strkSnap(m.r); got.strokes != 2 {
			t.Fatalf("the fresh allowance did not land: %d strokes on the log, want 2", got.strokes)
		}

		// And it is one stroke again, not one more than last time.
		m.stroke(second, 40)
		synctest.Wait()
		if got := strkSnap(m.r); got.strokes != 2 || got.perTurnS != OneLineStrokes {
			t.Fatalf("the reset handed out extra strokes: %d on the log, %d charged",
				got.strokes, got.perTurnS)
		}
	})
}

// TestFreePenRuleDrawsUpToTheAntiAbuseCeiling — FREE is the absence of a rule,
// not the absence of a cap. MaxStrokesPerTurn is anti-abuse and still holds
// (IMPLEMENTATION_PLAN.md §4.7).
func TestFreePenRuleDrawsUpToTheAntiAbuseCeiling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStartRule(t, 3, genpb.PenRule_PEN_RULE_FREE)
		artist, _ := m.artist(t)

		for i := range MaxStrokesPerTurn + 20 {
			m.stroke(artist, int32(i%1000))
		}
		synctest.Wait()
		m.drainAll()

		st := strkSnap(m.r)
		if st.perTurnS != MaxStrokesPerTurn {
			t.Fatalf("strokesThisTurn = %d, want the anti-abuse cap %d", st.perTurnS, MaxStrokesPerTurn)
		}
		if st.strokes != MaxStrokesPerTurn {
			t.Fatalf("committed %d strokes, want exactly the cap %d", st.strokes, MaxStrokesPerTurn)
		}
	})
}

// ---------------------------------------------------------------------------
// Coordinates: validated, never clipped
// ---------------------------------------------------------------------------

func TestCoordinatesOffTheGridSurviveAndOutOfRangeIsRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 3)
		artist, _ := m.artist(t)

		// Well outside the 4096x3072 grid but inside signed int16: a stroke
		// dragged past the canvas edge must survive the round trip verbatim,
		// not be chopped off (IMPLEMENTATION_PLAN.md §4.7).
		off := []int32{-32768, -32768, CoordMax, CoordMax, GridWidth + 500, -GridHeight - 500}
		m.submit(artist, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 0, Width: 3, Points: off}}})
		synctest.Wait()

		var began *genpb.StrokeBegan
		for _, e := range m.socks[artist].drain() {
			if b := e.GetStrokeBegan(); b != nil {
				began = b
			}
		}
		if began == nil {
			t.Fatal("off-grid coordinates were dropped instead of relayed")
		}
		if len(began.GetPoints()) != len(off) {
			t.Fatalf("relayed %d values, sent %d", len(began.GetPoints()), len(off))
		}
		for i, v := range began.GetPoints() {
			if v != off[i] {
				t.Fatalf("coordinate %d relayed as %d, sent %d — clipping desyncs viewers",
					i, v, off[i])
			}
		}
		m.drainAll()

		// Outside int16, an odd-length batch, an empty batch and an oversized
		// batch are all rejected outright, and none of them touches state.
		bad := map[string][]int32{
			"beyond int16 high": {40000, 0},
			"beyond int16 low":  {0, -40000},
			"odd length":        {1, 2, 3},
			"empty":             {},
			"oversized":         make([]int32, 2*MaxPointsPerStroke+2),
		}
		for name, pts := range bad {
			m.drainAll()
			before := strkSnap(m.r)
			m.submit(artist, &genpb.ClientCommand{Cid: "bad",
				Cmd: &genpb.ClientCommand_StrokePoints{StrokePoints: &genpb.StrokePoints{
					Points: pts}}})
			synctest.Wait()
			after := strkSnap(m.r)
			if after.openLen != before.openLen || after.seq != before.seq ||
				after.perTurnP != before.perTurnP {
				t.Errorf("%s: mutated state (open %d->%d, seq %d->%d, budget %d->%d)",
					name, before.openLen, after.openLen, before.seq, after.seq,
					before.perTurnP, after.perTurnP)
			}
			if codes := strkErrors(m.socks[artist].drain()); !strkHas(codes, genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND) {
				t.Errorf("%s: got %v, want INVALID_COMMAND", name, codes)
			}
		}
	})
}

func TestInvalidPaletteIndexIsRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 3)
		artist, _ := m.artist(t)

		for _, idx := range []int32{-1, PaletteSize, PaletteSize + 1, 1 << 20} {
			m.drainAll()
			before := strkSnap(m.r)
			m.submit(artist, &genpb.ClientCommand{Cid: "color",
				Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
					ColorIndex: idx, Width: 4, Points: []int32{5, 5}}}})
			synctest.Wait()
			after := strkSnap(m.r)

			// Rejected, not clamped: silently repainting the canvas hides the bug.
			if after.openLen != before.openLen || after.seq != before.seq ||
				after.strokes != before.strokes {
				t.Errorf("colour index %d opened a stroke instead of being rejected", idx)
			}
			if codes := strkErrors(m.socks[artist].drain()); !strkHas(codes, genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND) {
				t.Errorf("colour index %d: got %v, want INVALID_COMMAND", idx, codes)
			}
		}

		// Every legal index is accepted, so the check is a range and not a
		// blanket refusal.
		for idx := int32(0); idx < PaletteSize; idx++ {
			m.drainAll()
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
					ColorIndex: idx, Width: 4, Points: []int32{5, 5}}}})
			synctest.Wait()
			if codes := strkErrors(m.socks[artist].drain()); len(codes) != 0 {
				t.Errorf("legal colour index %d was rejected: %v", idx, codes)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Phase gating
// ---------------------------------------------------------------------------

func TestStrokeCommandsOutsideTheDrawingPhaseAreRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// PHASE_LOBBY, before any match exists.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := New("PHZ", "host", Options{Deck: strkDeck{}, Rand: mrand.New(mrand.NewPCG(3, 4))})
		hostID, hostTok := r.HostSeat()
		go r.run(ctx)
		s0 := newStrkSock()
		if _, err := r.attach(hostTok, s0); err != nil {
			t.Fatal(err)
		}
		s0.drain()
		r.Submit(Command{PlayerID: hostID, Out: s0, Cmd: &genpb.ClientCommand{Cid: "lobby",
			Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 1, Width: 4, Points: []int32{1, 1}}}}})
		synctest.Wait()
		if codes := strkErrors(s0.drain()); !strkHas(codes, genpb.ErrorCode_ERROR_CODE_WRONG_PHASE) {
			t.Errorf("lobby: got %v, want WRONG_PHASE", codes)
		}
		if n := strkGet(r, func(r *Room) int { return len(r.strokes) }); n != 0 {
			t.Errorf("a lobby stroke reached the log (%d strokes)", n)
		}
		cancel()
		synctest.Wait()
	})
}

func TestStrokeCommandsInDiscussionAndAfterTheMatchAreRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 3)

		// Run every drawing turn out to reach the discussion phase.
		for range len(m.ids) + 1 {
			time.Sleep(time.Duration(strkGet(m.r, func(r *Room) int32 { return r.settings.GetDrawSeconds() }))*time.Second + time.Millisecond)
			synctest.Wait()
			if strkGet(m.r, func(r *Room) genpb.Phase { return r.phase }) == genpb.Phase_PHASE_DISCUSSION {
				break
			}
		}
		if ph := strkGet(m.r, func(r *Room) genpb.Phase { return r.phase }); ph != genpb.Phase_PHASE_DISCUSSION {
			t.Fatalf("phase = %v, want DISCUSSION", ph)
		}
		m.drainAll()

		before := strkSnap(m.r)
		for i := range m.ids {
			m.submit(i, &genpb.ClientCommand{Cid: "disc",
				Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
					ColorIndex: 1, Width: 4, Points: []int32{1, 1}}}})
			m.submit(i, &genpb.ClientCommand{Cid: "disc",
				Cmd: &genpb.ClientCommand_StrokePoints{StrokePoints: &genpb.StrokePoints{
					Points: []int32{2, 2}}}})
			m.submit(i, &genpb.ClientCommand{Cid: "disc",
				Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}})
		}
		synctest.Wait()

		after := strkSnap(m.r)
		if after.strokes != before.strokes || after.points != before.points || after.seq != before.seq {
			t.Fatalf("a stroke landed during discussion\nbefore %+v\nafter %+v", before, after)
		}
		for i := range m.ids {
			if codes := strkErrors(m.socks[i].drain()); !strkHas(codes, genpb.ErrorCode_ERROR_CODE_WRONG_PHASE) {
				t.Errorf("player %d in discussion: got %v, want WRONG_PHASE", i, codes)
			}
		}

		// Vote everyone out of the discussion and check the resolving and ended
		// phases too. A strict majority of three is two.
		for i := range m.ids {
			m.submit(i, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{
					Choice: &genpb.CastVote_CandidateId{CandidateId: m.ids[1]}}}})
		}
		synctest.Wait()

		for _, phase := range []genpb.Phase{genpb.Phase_PHASE_RESOLVING, genpb.Phase_PHASE_ENDED} {
			for range 20 {
				if strkGet(m.r, func(r *Room) genpb.Phase { return r.phase }) == phase {
					break
				}
				time.Sleep(time.Second)
				synctest.Wait()
			}
			if ph := strkGet(m.r, func(r *Room) genpb.Phase { return r.phase }); ph != phase {
				t.Fatalf("could not reach %v, stuck at %v", phase, ph)
			}
			m.drainAll()
			pre := strkSnap(m.r)
			m.submit(0, &genpb.ClientCommand{Cid: "late",
				Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
					ColorIndex: 1, Width: 4, Points: []int32{1, 1}}}})
			synctest.Wait()
			post := strkSnap(m.r)
			if post.strokes != pre.strokes || post.seq != pre.seq {
				t.Errorf("%v: a stroke mutated state after the drawing phase", phase)
			}
			if codes := strkErrors(m.socks[0].drain()); !strkHas(codes, genpb.ErrorCode_ERROR_CODE_WRONG_PHASE) {
				t.Errorf("%v: got %v, want WRONG_PHASE", phase, codes)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Append-only (DESIGN.md:82)
// ---------------------------------------------------------------------------

// TestNoCommandCanErodeTheCanvasLog fires every client command this protocol
// has, from every player, in the middle of a match, and asserts the committed
// log never shrinks and its existing entries never change.
//
// There is no erase and no undo in this game by design. That is a property of
// the command set, so the strongest form of the test is to exercise the whole
// command set rather than the ones that look dangerous.
func TestNoCommandCanErodeTheCanvasLog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 4)
		artist, other := m.artist(t)

		// Lay down some evidence first.
		for i := range 5 {
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
					ColorIndex: int32(i % PaletteSize), Width: 5, Points: strkPoints(20, int32(i*10))}}})
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokePoints{StrokePoints: &genpb.StrokePoints{
					Points: strkPoints(20, int32(i*10+5))}}})
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}})
		}
		synctest.Wait()
		m.drainAll()

		base := strkSnap(m.r)
		if base.strokes < 5 {
			t.Fatalf("only %d strokes committed, expected at least 5", base.strokes)
		}

		// Every variant of ClientCommand, from the artist and from someone else.
		every := []*genpb.ClientCommand{
			{Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{DisplayName: "x"}}},
			{Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: false}}},
			{Cmd: &genpb.ClientCommand_UpdateSettings{UpdateSettings: &genpb.UpdateSettings{
				Settings: &genpb.MatchSettings{MaxRounds: 4}}}},
			{Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}}},
			{Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 999, Width: -1, Points: []int32{0, 0}}}},
			{Cmd: &genpb.ClientCommand_StrokePoints{StrokePoints: &genpb.StrokePoints{
				StrokeId: 1, Seq: 1, Points: nil}}},
			{Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{
				Points: []int32{0, 0}}}},
			{Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{
				Choice: &genpb.CastVote_Skip{Skip: true}}}},
			{Cmd: &genpb.ClientCommand_RequestSnapshot{RequestSnapshot: &genpb.RequestSnapshot{}}},
			{Cmd: &genpb.ClientCommand_Rematch{Rematch: &genpb.Rematch{}}},
			{}, // the unset oneof a newer client would leave
		}
		for _, c := range every {
			for _, who := range []int{artist, other} {
				m.submit(who, proto.Clone(c).(*genpb.ClientCommand))
			}
		}
		synctest.Wait()
		m.drainAll()

		now := strkSnap(m.r)
		if now.strokes < base.strokes {
			t.Fatalf("the log shrank from %d strokes to %d", base.strokes, now.strokes)
		}
		if now.points < base.points {
			t.Fatalf("the log lost coordinates: %d -> %d", base.points, now.points)
		}
		// Byte-for-byte: the existing entries are untouched, not merely counted.
		if !strings.HasPrefix(string(now.log), string(base.log)) {
			t.Fatal("a committed stroke was rewritten; the log is not append-only")
		}
		if now.phase != genpb.Phase_PHASE_DRAWING {
			t.Fatalf("the command sweep left the match in %v, want DRAWING", now.phase)
		}
	})
}

// TestTheProtocolHasNoEraseVerb asserts the absence at the schema level. A
// command that could erase would have to exist in the oneof before any handler
// could implement it, so this fails the moment somebody adds one — which is far
// earlier, and far louder, than a behavioural test would.
func TestTheProtocolHasNoEraseVerb(t *testing.T) {
	banned := []string{"erase", "undo", "clear", "delete", "remove", "eraser"}

	cmd := (&genpb.ClientCommand{}).ProtoReflect().Descriptor()
	fields := cmd.Fields()
	for i := range fields.Len() {
		name := strings.ToLower(string(fields.Get(i).Name()))
		for _, b := range banned {
			if strings.Contains(name, b) {
				t.Errorf("ClientCommand has a %q verb; the canvas is append-only "+
					"evidence, not a sabotage tool (DESIGN.md:82)", name)
			}
		}
	}

	// The same for the stroke payload: no tool or blend-mode selector that could
	// smuggle an eraser in as a colour.
	strokeBegin := (&genpb.StrokeBegin{}).ProtoReflect().Descriptor().Fields()
	for i := range strokeBegin.Len() {
		name := strings.ToLower(string(strokeBegin.Get(i).Name()))
		for _, b := range append(banned, "tool", "composite", "blend") {
			if strings.Contains(name, b) {
				t.Errorf("StrokeBegin has a %q field; colours are palette indices and "+
					"there is no tool selector", name)
			}
		}
	}
}

// TestStrokeEndReplacementCannotGrowAStroke covers the RDP simplification path.
// StrokeEnd.points replaces the whole stroke, so an artist who sends more points
// than they streamed would be buying geometry the per-turn budget never charged
// for.
func TestStrokeEndReplacementCannotGrowAStroke(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 3)
		artist, _ := m.artist(t)

		m.submit(artist, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 1, Width: 4, Points: strkPoints(10, 0)}}})
		synctest.Wait()
		streamed := strkSnap(m.r).openLen
		m.drainAll()

		// Ten times the geometry that was streamed.
		m.submit(artist, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{
				Points: strkPoints(100, 500)}}})
		synctest.Wait()

		committed := strkGet(m.r, func(r *Room) int {
			if len(r.strokes) == 0 {
				return -1
			}
			return len(r.strokes[len(r.strokes)-1].GetPoints())
		})
		if committed > streamed {
			t.Fatalf("StrokeEnd grew the stroke from %d values to %d", streamed, committed)
		}

		// And a genuine simplification is accepted and relayed.
		m.drainAll()
		m.submit(artist, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 1, Width: 4, Points: strkPoints(40, 0)}}})
		synctest.Wait()
		m.submit(artist, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{
				Points: strkPoints(6, 0)}}})
		synctest.Wait()

		var ended *genpb.StrokeEnded
		for _, e := range m.socks[artist].drain() {
			if x := e.GetStrokeEnded(); x != nil {
				ended = x
			}
		}
		if ended == nil {
			t.Fatal("no StrokeEnded for the simplified stroke")
		}
		if len(ended.GetPoints()) != 12 {
			t.Fatalf("simplified stroke relayed %d values, want 12", len(ended.GetPoints()))
		}
		last := strkGet(m.r, func(r *Room) int {
			return len(r.strokes[len(r.strokes)-1].GetPoints())
		})
		if last != 12 {
			t.Fatalf("committed %d values for the simplified stroke, want 12", last)
		}
	})
}

// TestStrokeSeqIsStrictlyMonotonic is what makes the client's gap detection
// work: a viewer that sees a hole sends RequestSnapshot. A repeated or
// decreasing seq means a viewer silently diverges instead.
func TestStrokeSeqIsStrictlyMonotonic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 3)
		artist, _ := m.artist(t)
		m.drainAll()

		for i := range 12 {
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
					ColorIndex: 1, Width: 4, Points: strkPoints(4, int32(i))}}})
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokePoints{StrokePoints: &genpb.StrokePoints{
					Points: strkPoints(4, int32(i+100))}}})
			m.submit(artist, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}})
		}
		synctest.Wait()

		// Every viewer must see the same sequence, or the canvas desyncs.
		var reference []int32
		for i, s := range m.socks {
			var seqs []int32
			for _, e := range s.drain() {
				switch v := e.GetEvt().(type) {
				case *genpb.ServerEvent_StrokeBegan:
					seqs = append(seqs, v.StrokeBegan.GetSeq())
				case *genpb.ServerEvent_StrokePoints:
					seqs = append(seqs, v.StrokePoints.GetSeq())
				case *genpb.ServerEvent_StrokeEnded:
					seqs = append(seqs, v.StrokeEnded.GetSeq())
				}
			}
			if len(seqs) != 36 {
				t.Fatalf("player %d saw %d stroke events, want 36", i, len(seqs))
			}
			for n := 1; n < len(seqs); n++ {
				if seqs[n] <= seqs[n-1] {
					t.Fatalf("player %d: seq went %d -> %d at event %d",
						i, seqs[n-1], seqs[n], n)
				}
			}
			if reference == nil {
				reference = seqs
				continue
			}
			for n := range seqs {
				if seqs[n] != reference[n] {
					t.Fatalf("player %d event %d has seq %d, player 0 saw %d — viewers diverge",
						i, n, seqs[n], reference[n])
				}
			}
		}
	})
}

// ---------------------------------------------------------------------------
// TurnGrace — the tail of the stroke under the pen when the clock runs out
// ---------------------------------------------------------------------------

// strkExpireTurn runs the drawing clock out and settles. It deliberately does
// NOT assume the turn ended: with a stroke open it enters the grace window
// instead (phase.go, beginTurnGrace), which is the whole point of these tests.
func (m *strkMatch) strkExpireTurn() {
	draw := strkGet(m.r, func(r *Room) int32 { return r.settings.GetDrawSeconds() })
	time.Sleep(time.Duration(draw)*time.Second + time.Millisecond)
	synctest.Wait()
}

// The artist's pen is not on the room's clock: the last ~50 ms of a live stroke
// is still in the client's batch when the turn expires, and one one-way latency
// behind that. Committing on the tick threw it away.
func TestATurnExpiringMidStrokeKeepsTheTail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 4)
		a, _ := m.artist(t)
		artist := m.ids[a]

		m.submit(a, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 1, Width: 4, Points: strkPoints(4, 0)}}})
		synctest.Wait()
		m.strkExpireTurn()

		st := strkSnap(m.r)
		if st.phase != genpb.Phase_PHASE_DRAWING || st.artist != artist {
			t.Fatalf("grace: phase = %v artist = %q, want DRAWING and the same artist", st.phase, st.artist)
		}
		if st.openLen != 8 {
			t.Fatalf("grace: open stroke holds %d coordinates, want the 8 it had", st.openLen)
		}
		if st.strokes != 0 {
			t.Fatalf("grace: %d strokes committed, want the open one still open", st.strokes)
		}
		// The clock really is spent — the window buys the room a tail, it does
		// not hand the artist more time, and RemainingMS must not pretend it did.
		if ms := strkGet(m.r, func(r *Room) int32 { return r.RemainingMS() }); ms != 0 {
			t.Fatalf("grace: RemainingMS = %d, want 0", ms)
		}

		// The tail the client was still holding when the buzzer went.
		m.submit(a, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokePoints{StrokePoints: &genpb.StrokePoints{
				Points: strkPoints(3, 100)}}})
		synctest.Wait()
		if st := strkSnap(m.r); st.openLen != 14 {
			t.Fatalf("grace: open stroke holds %d coordinates after the tail, want 14", st.openLen)
		}

		// And the end that closes it, which closes the turn with it rather than
		// sitting out the rest of a window that has served its purpose.
		m.submit(a, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}})
		synctest.Wait()

		st = strkSnap(m.r)
		if st.strokes != 1 || st.points != 14 {
			t.Fatalf("after the end: %d strokes / %d coordinates, want 1 / 14", st.strokes, st.points)
		}
		if st.phase != genpb.Phase_PHASE_INTERMISSION {
			t.Fatalf("after the end: phase = %v, want the handoff to have run", st.phase)
		}
	})
}

// The window is for finishing one stroke, not for starting another. Nothing new
// may be laid down after the buzzer.
func TestTurnGraceRefusesANewStroke(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 4)
		a, _ := m.artist(t)

		m.submit(a, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 1, Width: 4, Points: strkPoints(2, 0)}}})
		synctest.Wait()
		m.strkExpireTurn()
		before := strkSnap(m.r)

		m.submit(a, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 2, Width: 4, Points: strkPoints(2, 500)}}})
		synctest.Wait()

		after := strkSnap(m.r)
		if after.openID != before.openID || after.openLen != before.openLen {
			t.Fatalf("a begin inside the grace window displaced the open stroke: %+v -> %+v", before, after)
		}
		if after.nextID != before.nextID || after.perTurnS != before.perTurnS {
			t.Fatalf("a begin inside the grace window spent budget: %+v -> %+v", before, after)
		}
	})
}

// Nothing arrives — the artist's tab froze, or their socket is molasses. The
// window is a bound, not a promise, and the turn ends on its own.
func TestTurnGraceEndsTheTurnOnItsOwn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 4)
		a, _ := m.artist(t)

		m.submit(a, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
				ColorIndex: 1, Width: 4, Points: strkPoints(4, 0)}}})
		synctest.Wait()
		m.strkExpireTurn()

		time.Sleep(TurnGrace)
		synctest.Wait()

		st := strkSnap(m.r)
		if st.strokes != 1 || st.points != 8 {
			t.Fatalf("%d strokes / %d coordinates, want the open one committed as 1 / 8", st.strokes, st.points)
		}
		if st.phase != genpb.Phase_PHASE_INTERMISSION {
			t.Fatalf("phase = %v, want the handoff to have run", st.phase)
		}
	})
}

// A turn with the pen already up has nothing in flight to wait for, so the
// ordinary handoff — every turn in a normal match — keeps its exact timing.
func TestATurnWithNothingOpenEndsOnTheTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := strkStart(t, 4)
		a, _ := m.artist(t)
		m.stroke(a, 10)
		synctest.Wait()
		m.strkExpireTurn()

		if st := strkSnap(m.r); st.phase != genpb.Phase_PHASE_INTERMISSION {
			t.Fatalf("phase = %v, want INTERMISSION on the tick", st.phase)
		}
	})
}
