package transport

// boundary_test.go — everything transport owns because it must not be trusted:
// the frame, the command union, the seat credential, the command rate, and the
// queue policy that keeps one bad client from taking a room down with it.
//
// No game rule is asserted here. The room actor is the only authority
// (IMPLEMENTATION_PLAN.md §4.4); what is asserted is that nothing illegitimate
// ever reaches it.

import (
	"context"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/registry"
	"github.com/sreyas-endor/verso/internal/room"
)

// boundaryDeck deals real words, so the private-word path is exercised where a
// test needs it. nilDeck (conn_test.go) deals empty strings, which the room
// correctly declines to send.
type boundaryDeck struct{}

func (boundaryDeck) Pair(genpb.Difficulty, *mrand.Rand) (string, string) {
	return "BOUNDARY_ALPHA", "BOUNDARY_BETA"
}

// newTunedServer is newTestServer with the knobs a specific test needs to move.
func newTunedServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.DiscardHandler)

	reg := registry.New(ctx, registry.Config{
		NewDeck: func() room.Deck { return boundaryDeck{} },
		Settings: &genpb.MatchSettings{
			MaxRounds: 1, DrawSeconds: room.MinDrawSeconds, DiscussSeconds: room.MinDiscussSeconds,
		},
		Logger: log,
	})
	cfg.Registry = reg
	cfg.Logger = log
	ws := New(ctx, cfg)

	mux := http.NewServeMux()
	mux.Handle("/ws", ws.Handler())
	hs := httptest.NewServer(mux)

	t.Cleanup(func() {
		cancel()
		hs.Close()
		_ = reg.Close(context.Background())
	})
	return hs
}

// ---------------------------------------------------------------------------
// The command union at the socket boundary
// ---------------------------------------------------------------------------

// TestValidateRejectsEveryEmptyVariant walks the whole ClientCommand union.
// Past validate, the room is entitled to assume the oneof is set and its
// payload is non-nil, so every hole here is a nil dereference in the actor
// goroutine — which takes the whole room with it.
func TestValidateRejectsEveryEmptyVariant(t *testing.T) {
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("validate panicked instead of returning an error: %v", p)
		}
	}()

	bad := map[string]*genpb.ClientCommand{
		"unset oneof":            {Cid: "a"},
		"nil join":               {Cmd: &genpb.ClientCommand_Join{}},
		"nil set_ready":          {Cmd: &genpb.ClientCommand_SetReady{}},
		"nil update_settings":    {Cmd: &genpb.ClientCommand_UpdateSettings{}},
		"nil start_match":        {Cmd: &genpb.ClientCommand_StartMatch{}},
		"nil stroke_begin":       {Cmd: &genpb.ClientCommand_StrokeBegin{}},
		"nil stroke_points":      {Cmd: &genpb.ClientCommand_StrokePoints{}},
		"nil stroke_end":         {Cmd: &genpb.ClientCommand_StrokeEnd{}},
		"nil cast_vote":          {Cmd: &genpb.ClientCommand_CastVote{}},
		"nil request_snapshot":   {Cmd: &genpb.ClientCommand_RequestSnapshot{}},
		"nil rematch":            {Cmd: &genpb.ClientCommand_Rematch{}},
		"cast_vote, no choice":   {Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{}}},
		"cast_vote, skip false":  {Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{Choice: &genpb.CastVote_Skip{Skip: false}}}},
		"nil command altogether": nil,
	}
	for name, cmd := range bad {
		if err := validate(cmd); err == nil {
			t.Errorf("%s: validate accepted it", name)
		}
	}

	// And the whole union is accepted when it is properly filled in, so the
	// check above is a gate and not a wall.
	good := map[string]*genpb.ClientCommand{
		"join":             {Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{DisplayName: "ada"}}},
		"set_ready":        {Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}},
		"update_settings":  {Cmd: &genpb.ClientCommand_UpdateSettings{UpdateSettings: &genpb.UpdateSettings{}}},
		"start_match":      {Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}}},
		"stroke_begin":     {Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{}}},
		"stroke_points":    {Cmd: &genpb.ClientCommand_StrokePoints{StrokePoints: &genpb.StrokePoints{}}},
		"stroke_end":       {Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}},
		"vote for someone": {Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{Choice: &genpb.CastVote_CandidateId{CandidateId: "x"}}}},
		"vote skip":        {Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{Choice: &genpb.CastVote_Skip{Skip: true}}}},
		"request_snapshot": {Cmd: &genpb.ClientCommand_RequestSnapshot{RequestSnapshot: &genpb.RequestSnapshot{}}},
		"rematch":          {Cmd: &genpb.ClientCommand_Rematch{Rematch: &genpb.Rematch{}}},
	}
	for name, cmd := range good {
		if err := validate(cmd); err != nil {
			t.Errorf("%s: validate rejected a well-formed command: %v", name, err)
		}
	}
}

// TestEnvelopeRoundTrip checks that the oneof survives the wire in both
// directions, and that a frame whose only content is an unknown field decodes
// cleanly to an unset union rather than to something the room might act on.
func TestEnvelopeRoundTrip(t *testing.T) {
	cmds := []*genpb.ClientCommand{
		{Cid: "1", Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
			RoomCode: "ABCDE", DisplayName: "ada", SeatToken: "tok", ProtocolVersion: 1}}},
		{Cid: "2", Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
			ColorIndex: 3, Width: 7, Points: []int32{-32768, 32767, 0, 0}}}},
		{Cid: "3", Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{
			Choice: &genpb.CastVote_Skip{Skip: true}}}},
	}
	for _, in := range cmds {
		b, err := proto.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		out := &genpb.ClientCommand{}
		if err := proto.Unmarshal(b, out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !proto.Equal(in, out) {
			t.Errorf("round trip changed the frame:\n in: %v\nout: %v", in, out)
		}
		if err := validate(out); err != nil {
			t.Errorf("a round-tripped frame failed validation: %v", err)
		}
	}

	evs := []*genpb.ServerEvent{
		{Cid: "1", Evt: &genpb.ServerEvent_Joined{Joined: &genpb.Joined{RoomCode: "ABCDE"}}},
		{Evt: &genpb.ServerEvent_YourWord{YourWord: &genpb.YourWord{Word: "CAT", Round: 1}}},
		{Evt: &genpb.ServerEvent_MatchEnded{MatchEnded: &genpb.MatchEnded{
			Winner: genpb.WinnerSide_WINNER_SIDE_GROUP}}},
	}
	for _, in := range evs {
		b, err := proto.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		out := &genpb.ServerEvent{}
		if err := proto.Unmarshal(b, out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !proto.Equal(in, out) {
			t.Errorf("round trip changed the event:\n in: %v\nout: %v", in, out)
		}
	}

	// A frame carrying only a field number this build has never heard of: the
	// oneof stays unset and validate refuses it. This is the shape a newer
	// client's new command arrives in.
	future := []byte{0xF8, 0x3F, 0x2A} // field 1023, varint 42
	unknown := &genpb.ClientCommand{}
	if err := proto.Unmarshal(future, unknown); err != nil {
		t.Fatalf("an unknown field must decode, not fail: %v", err)
	}
	if unknown.GetCmd() != nil {
		t.Fatalf("an unknown field set the union to %T", unknown.GetCmd())
	}
	if err := validate(unknown); err == nil {
		t.Fatal("a frame with no known command was accepted")
	}
}

// ---------------------------------------------------------------------------
// Seat tokens
// ---------------------------------------------------------------------------

// TestSeatTokenIsUnguessable pins the credential's shape. The token is an
// opaque room-local random string rather than a signed one, so its entire
// strength is its entropy: 32 bytes from crypto/rand, rendered as 64 hex
// characters, never reused between seats.
func TestSeatTokenIsUnguessable(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		r := room.New("AAAAA", "host", room.Options{})
		_, tok := r.HostSeat()
		if len(tok) != 64 {
			t.Fatalf("seat token %q is %d characters, want 64 hex (256 bits)", tok, len(tok))
		}
		if strings.Trim(tok, "0123456789abcdef") != "" {
			t.Fatalf("seat token %q is not hex", tok)
		}
		if seen[tok] {
			t.Fatalf("seat token %q was issued twice", tok)
		}
		seen[tok] = true
	}
}

// TestTamperedAndForeignSeatTokensAreRejected covers the three ways a token can
// be wrong: altered, belonging to a different room, and structurally absurd.
//
// The room resolves a token by map lookup on 256 bits of crypto randomness, not
// by comparing a signature, so there is nothing to misvalidate and no partial
// match to accept. What must hold is that every near-miss is a miss.
func TestTamperedAndForeignSeatTokensAreRejected(t *testing.T) {
	hs := newTunedServer(t, Config{})

	// Room A, whose host token is the credential under test.
	a := newClient(t, hs)
	a.send(&genpb.ClientCommand{Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		DisplayName: "ada", ProtocolVersion: room.ProtocolVersion}}})
	joinA := a.awaitJoined()
	tokenA, codeA := joinA.GetSeatToken(), joinA.GetRoomCode()

	// Room B, a different room entirely.
	b := newClient(t, hs)
	b.send(&genpb.ClientCommand{Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		DisplayName: "grace", ProtocolVersion: room.ProtocolVersion}}})
	codeB := b.awaitJoined().GetRoomCode()
	if codeA == codeB {
		t.Fatal("two rooms got the same code")
	}

	flip := func(s string) string {
		r := []byte(s)
		if r[0] == 'a' {
			r[0] = 'b'
		} else {
			r[0] = 'a'
		}
		return string(r)
	}

	cases := map[string]struct{ code, token string }{
		"one character altered":  {codeA, flip(tokenA)},
		"truncated":              {codeA, tokenA[:len(tokenA)-1]},
		"extended":               {codeA, tokenA + "0"},
		"uppercased":             {codeA, strings.ToUpper(tokenA)},
		"empty-ish":              {codeA, " "},
		"valid token, room B":    {codeB, tokenA},
		"all zeroes":             {codeA, strings.Repeat("0", len(tokenA))},
		"a room code as a token": {codeA, codeA},
	}
	for name, c := range cases {
		f := newClient(t, hs)
		f.send(&genpb.ClientCommand{Cid: "bad", Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
			RoomCode: c.code, SeatToken: c.token, ProtocolVersion: room.ProtocolVersion}}})
		if got := expectError(t, f.ws).GetCode(); got != genpb.ErrorCode_ERROR_CODE_BAD_SEAT {
			t.Errorf("%s: got %v, want BAD_SEAT", name, got)
		}
	}

	// The genuine article still works, so the rejections above are not a blanket
	// refusal of every reconnect.
	a.ws.CloseNow()
	back := newClient(t, hs)
	back.send(&genpb.ClientCommand{Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		RoomCode: codeA, SeatToken: tokenA, ProtocolVersion: room.ProtocolVersion}}})
	if got := back.awaitJoined().GetPlayerId(); got != joinA.GetPlayerId() {
		t.Fatalf("reclaimed seat as %q, want %q", got, joinA.GetPlayerId())
	}
}

// TestAnExpiredSeatTokenIsRejected drives a real room with plain channels — no
// socket — so the 60 s grace window can pass in virtual time.
//
// The two outcomes are deliberately different. In the lobby the seat is freed
// outright and the token stops working. Mid-match the seat and its word are
// retained (DESIGN.md:113), so the token keeps working and brings its holder
// back as a spectator rather than as an active player.
func TestAnExpiredSeatTokenIsRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r := room.New("EXPY", "host", room.Options{
			Deck:   boundaryDeck{},
			Logger: slog.New(slog.DiscardHandler),
		})
		go r.Run(ctx)

		hostCh := make(chan *genpb.ServerEvent, 256)
		if _, err := r.Attach(func() string { _, tok := r.HostSeat(); return tok }(), hostCh); err != nil {
			t.Fatal(err)
		}

		ch := make(chan *genpb.ServerEvent, 256)
		id, token, err := r.Seat("bee", ch)
		if err != nil {
			t.Fatal(err)
		}

		r.Detach(id, ch)
		synctest.Wait()

		// Inside the window the token still works.
		time.Sleep(room.GraceWindow / 2)
		synctest.Wait()
		if _, err := r.Attach(token, ch); err != nil {
			t.Fatalf("token rejected inside the grace window: %v", err)
		}

		// Past it, in the lobby, the seat is gone and so is the credential.
		r.Detach(id, ch)
		synctest.Wait()
		time.Sleep(room.GraceWindow + 2*room.SweepInterval)
		synctest.Wait()

		if _, err := r.Attach(token, ch); err == nil {
			t.Fatal("an expired lobby seat token was accepted")
		} else if !errorsIsBadSeat(err) {
			t.Fatalf("expired token gave %v, want ErrBadSeat", err)
		}

		cancel()
		synctest.Wait()
	})
}

func errorsIsBadSeat(err error) bool {
	return err != nil && strings.Contains(err.Error(), "bad seat token")
}

// ---------------------------------------------------------------------------
// Backpressure: a slow client must never stall a room
// ---------------------------------------------------------------------------

// TestEnqueueDropsRatherThanBlocks is the transport half of the queue policy.
// The room goroutine writes into these queues directly — there is no
// per-connection writer goroutine (IMPLEMENTATION_PLAN.md §4.4) — so a blocking
// send here would be a blocked room.
func TestEnqueueDropsRatherThanBlocks(t *testing.T) {
	c := &conn{
		out: make(chan *genpb.ServerEvent, 2),
		log: slog.New(slog.DiscardHandler),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			c.enqueue(&genpb.ServerEvent{Cid: "x"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue blocked on a full queue")
	}
	if n := len(c.out); n != 2 {
		t.Fatalf("queue holds %d frames, want its capacity of 2", n)
	}
}

// TestAFullOutboundQueueDoesNotStallTheRoom is the room half of the same
// policy, driven through the exported API with a queue that is never drained.
//
// One client whose socket has stopped consuming must cost the other players
// nothing at all: the match keeps running, their frames keep arriving, and a
// request from any of them is still answered.
func TestAFullOutboundQueueDoesNotStallTheRoom(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := room.New("JAMM", "host", room.Options{
		Deck:   boundaryDeck{},
		Logger: slog.New(slog.DiscardHandler),
	})
	hostID, hostTok := r.HostSeat()
	go r.Run(ctx)

	live := make(chan *genpb.ServerEvent, 4096)
	if _, err := r.Attach(hostTok, live); err != nil {
		t.Fatal(err)
	}
	second := make(chan *genpb.ServerEvent, 4096)
	secondID, _, err := r.Seat("grace", second)
	if err != nil {
		t.Fatal(err)
	}
	// Capacity 1 and never read: full after the very first frame, and full for
	// the rest of the test. This is a socket that has stopped consuming.
	stuck := make(chan *genpb.ServerEvent, 1)
	stuckID, _, err := r.Seat("stuck", stuck)
	if err != nil {
		t.Fatal(err)
	}

	drain := func(ch chan *genpb.ServerEvent) int {
		n := 0
		for {
			select {
			case <-ch:
				n++
			default:
				return n
			}
		}
	}
	drain(live)
	drain(second)

	// Hundreds of broadcasts while one queue is jammed solid. Submitted in
	// chunks: the room's inbox is bounded too, and Submit drops rather than
	// blocks, so a test that overran it would be measuring the wrong queue.
	seats := []struct {
		id  string
		out chan *genpb.ServerEvent
	}{{hostID, live}, {secondID, second}, {stuckID, stuck}}
	for i := range 400 {
		for _, s := range seats {
			r.Submit(room.Command{PlayerID: s.id, Out: s.out, Cmd: &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: i%2 == 0}}}})
		}
		if i%20 == 19 {
			time.Sleep(2 * time.Millisecond)
		}
	}

	// The room is still answering: a snapshot request comes back promptly.
	r.Submit(room.Command{PlayerID: hostID, Out: live, Cmd: &genpb.ClientCommand{
		Cid: "snap", Cmd: &genpb.ClientCommand_RequestSnapshot{
			RequestSnapshot: &genpb.RequestSnapshot{}}}})

	got, hostFrames := false, 0
	deadline := time.Now().Add(10 * time.Second)
	for !got && time.Now().Before(deadline) {
		select {
		case ev := <-live:
			hostFrames++
			if ev.GetSnapshot() != nil && ev.GetCid() == "snap" {
				got = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the room stopped answering while one client's queue was full")
		}
	}
	if !got {
		t.Fatal("no Snapshot came back; the room stalled behind a full queue")
	}

	// The healthy players kept receiving throughout.
	if hostFrames < 100 {
		t.Fatalf("the host received only %d frames from 1200 commands while another "+
			"client was jammed", hostFrames)
	}
	if n := drain(second); n < 100 {
		t.Fatalf("the second player received only %d frames while another client was jammed", n)
	}
	// And the jammed queue is exactly as full as it was: frames were dropped,
	// not buffered without bound and not blocked on.
	if n := len(stuck); n != 1 {
		t.Fatalf("the jammed queue holds %d frames, want its capacity of 1", n)
	}

	cancel()
}

// pump reads a socket in the background so it keeps answering pings while the
// test is doing something else. coder/websocket handles control frames inside
// Read, so a client that is not reading is a client that never pongs — which is
// the whole point of the test below, and a hazard for every other client in it.
type pump struct {
	evs  chan *genpb.ServerEvent
	done chan error
}

func newPump(ws *websocket.Conn) *pump {
	p := &pump{evs: make(chan *genpb.ServerEvent, 512), done: make(chan error, 1)}
	go func() {
		for {
			_, data, err := ws.Read(context.Background())
			if err != nil {
				p.done <- err
				return
			}
			ev := &genpb.ServerEvent{}
			if proto.Unmarshal(data, ev) != nil {
				continue
			}
			select {
			case p.evs <- ev:
			default:
			}
		}
	}()
	return p
}

func (p *pump) await(t *testing.T, what string, match func(*genpb.ServerEvent) bool) *genpb.ServerEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-p.evs:
			if match(ev) {
				return ev
			}
		case err := <-p.done:
			t.Fatalf("waiting for %s, the socket closed: %v", what, err)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// TestASocketThatStopsReadingIsKicked proves the other half of "drop or kick".
// A client that never reads also never answers a ping, so the shared liveness
// sweep closes it — and the sockets around it are untouched.
func TestASocketThatStopsReadingIsKicked(t *testing.T) {
	hs := newTunedServer(t, Config{
		PingInterval: 50 * time.Millisecond,
		PongTimeout:  50 * time.Millisecond,
		IdleTimeout:  150 * time.Millisecond,
	})

	// The healthy client creates a room and keeps reading throughout.
	goodWS := dial(t, hs)
	good := newPump(goodWS)
	send(t, goodWS, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		DisplayName: "ada", ProtocolVersion: room.ProtocolVersion}}})
	code := good.await(t, "Joined", func(e *genpb.ServerEvent) bool {
		return e.GetJoined() != nil
	}).GetJoined().GetRoomCode()

	// The silent one joins and then stops reading entirely.
	silent := dial(t, hs)
	send(t, silent, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		RoomCode: code, DisplayName: "quiet", ProtocolVersion: room.ProtocolVersion}}})
	good.await(t, "the roster to show two players", func(e *genpb.ServerEvent) bool {
		return len(e.GetLobbyState().GetPlayers()) == 2
	})

	// Wait out several ping intervals without the silent client reading.
	time.Sleep(600 * time.Millisecond)

	// Frames the server sent before it gave up are still in the silent client's
	// receive buffer, so read past them: the close is what comes after.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closed := false
	for range 4096 {
		if _, _, err := silent.Read(ctx); err != nil {
			closed = true
			break
		}
	}
	if !closed {
		t.Fatal("a socket that never answered a ping was not closed")
	}

	// The healthy client is unaffected and the room is still serving it.
	select {
	case err := <-good.done:
		t.Fatalf("the healthy socket was closed too: %v", err)
	default:
	}
	send(t, goodWS, &genpb.ClientCommand{Cid: "still-here",
		Cmd: &genpb.ClientCommand_RequestSnapshot{RequestSnapshot: &genpb.RequestSnapshot{}}})
	good.await(t, "a Snapshot after the neighbour was kicked", func(e *genpb.ServerEvent) bool {
		return e.GetSnapshot() != nil
	})
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

func TestCommandRateLimiterRefusesABurst(t *testing.T) {
	hs := newTunedServer(t, Config{CommandBurst: 3, CommandRate: 1})
	c := dial(t, hs)

	// Every frame is counted before it is decoded, so the cheapest possible
	// frame is still charged.
	for range 12 {
		send(t, c, &genpb.ClientCommand{Cid: "f",
			Cmd: &genpb.ClientCommand_RequestSnapshot{RequestSnapshot: &genpb.RequestSnapshot{}}})
	}

	limited := 0
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range 12 {
		_, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		ev := &genpb.ServerEvent{}
		if err := proto.Unmarshal(data, ev); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if ev.GetError().GetCode() == genpb.ErrorCode_ERROR_CODE_RATE_LIMITED {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("12 frames against a burst of 3 produced no RATE_LIMITED error")
	}
	t.Logf("%d of 12 frames were rate limited", limited)
}

func TestAPersistentFlooderIsDisconnected(t *testing.T) {
	hs := newTunedServer(t, Config{CommandBurst: 1, CommandRate: 0.001})
	c := dial(t, hs)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := proto.Marshal(&genpb.ClientCommand{Cid: "f",
		Cmd: &genpb.ClientCommand_RequestSnapshot{RequestSnapshot: &genpb.RequestSnapshot{}}})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly enough to spend the strike budget and no more: frames still unread
	// by the server when it closes would make the kernel answer with a reset
	// instead of letting the close frame through.
	for range maxRateStrikes + 2 {
		if err := c.Write(ctx, websocket.MessageBinary, b); err != nil {
			break // the server already hung up
		}
	}

	limited := 0
	for range 4096 {
		_, data, err := c.Read(ctx)
		if err == nil {
			ev := &genpb.ServerEvent{}
			if proto.Unmarshal(data, ev) == nil &&
				ev.GetError().GetCode() == genpb.ErrorCode_ERROR_CODE_RATE_LIMITED {
				limited++
			}
			continue
		}
		// The socket is gone, which is the point. A flooder that keeps being
		// told off forever is not being limited.
		//
		// The close STATUS is best effort: transport cancels the write pump the
		// instant the read loop decides to close, and coder/websocket tears a
		// connection down rather than resume a write whose context died, so the
		// close frame carrying PolicyViolation frequently loses that race. Assert
		// it when it arrives; never require it.
		if st := websocket.CloseStatus(err); st != -1 && st != websocket.StatusPolicyViolation {
			t.Fatalf("close status = %v, want PolicyViolation", st)
		}
		if limited == 0 {
			t.Fatalf("the socket closed without a single RATE_LIMITED warning (err %v)", err)
		}
		t.Logf("%d warnings, then disconnected (%v)", limited, websocket.CloseStatus(err))
		return
	}
	t.Fatal("a client that broke the rate limit 21 times in a row was never disconnected")
}

func TestRoomCreationIsRateLimitedPerAddress(t *testing.T) {
	hs := newTunedServer(t, Config{CreateBurst: 2, CreateRate: 0.001})

	created, refused := 0, 0
	for range 6 {
		c := newClient(t, hs)
		c.send(&genpb.ClientCommand{Cid: "mk", Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
			DisplayName: "ada", ProtocolVersion: room.ProtocolVersion}}})

		ev := c.next(5 * time.Second)
		switch {
		case ev.GetJoined() != nil:
			created++
		case ev.GetError().GetCode() == genpb.ErrorCode_ERROR_CODE_RATE_LIMITED:
			refused++
		default:
			t.Fatalf("unexpected first frame: %T %v", ev.GetEvt(), ev)
		}
	}
	if created != 2 {
		t.Errorf("created %d rooms against a burst of 2", created)
	}
	if refused != 4 {
		t.Errorf("refused %d attempts, want 4", refused)
	}
}

func TestBucketMapPrunesRefilledKeys(t *testing.T) {
	// A per-address map keyed by attacker-chosen strings is a slow leak unless
	// spent keys are collected. A key that has refilled is indistinguishable
	// from one never seen.
	bm := newBucketMap(2, 1)
	now := time.Now()
	for i := range 100 {
		bm.allow(string(rune('a'+i%26))+string(rune('a'+i/26)), now)
	}
	if len(bm.m) == 0 {
		t.Fatal("no buckets were created")
	}
	bm.prune(now)
	if len(bm.m) == 0 {
		t.Fatal("prune dropped buckets that still hold state")
	}
	bm.prune(now.Add(time.Hour))
	if n := len(bm.m); n != 0 {
		t.Fatalf("%d buckets survived an hour of refill", n)
	}
}

// ---------------------------------------------------------------------------
// Connection accounting
// ---------------------------------------------------------------------------

func TestMaxConnsIsEnforced(t *testing.T) {
	hs := newTunedServer(t, Config{MaxConns: 1})

	first := dial(t, hs)
	// Make sure the first socket is registered before racing the second.
	send(t, first, &genpb.ClientCommand{Cid: "hello",
		Cmd: &genpb.ClientCommand_RequestSnapshot{RequestSnapshot: &genpb.RequestSnapshot{}}})
	expectError(t, first)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+hs.URL[len("http"):]+"/ws", nil)
	if err == nil {
		c.CloseNow()
		t.Fatal("a second socket was accepted past MaxConns of 1")
	}
	if resp != nil && resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestRemoteAddrIgnoresProxyHeaders(t *testing.T) {
	// Trusting an unverified X-Forwarded-For would let any client forge its own
	// rate-limit bucket, which is the same as having no bucket.
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.RemoteAddr = "192.0.2.7:51234"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.Header.Set("X-Real-IP", "203.0.113.9")
	if got := remoteAddr(r); got != "192.0.2.7" {
		t.Fatalf("remoteAddr = %q, want the real peer 192.0.2.7", got)
	}
}
