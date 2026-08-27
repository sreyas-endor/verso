package transport

// session_test.go — the transport half of ordered session closure
// (PERFORMANCE_OPTIMIZATION_PLAN.md S1), over real sockets.
//
// The room only ever REQUESTS a close; the write pump is what carries it out,
// and the ordering it has to preserve is the whole point. A displaced socket
// must receive the Error explaining itself and then close — not close first,
// and not stay open answering pings forever while the seat it thinks it holds
// has moved to another connection.

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// awaitClose reads until the socket ends, returning every event that arrived
// first and the close error. It fails if the socket is still answering after
// the timeout, which is exactly the leak S1 is about.
func awaitClose(t *testing.T, c *websocket.Conn, timeout time.Duration) ([]*genpb.ServerEvent, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var got []*genpb.ServerEvent
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return got, err
		}
		ev := &genpb.ServerEvent{}
		if proto.Unmarshal(data, ev) == nil {
			got = append(got, ev)
		}
	}
}

func lastError(evs []*genpb.ServerEvent) *genpb.Error {
	var last *genpb.Error
	for _, ev := range evs {
		if e := ev.GetError(); e != nil {
			last = e
		}
	}
	return last
}

// TestADisplacedSocketIsToldWhyAndThenClosed is the S1 acceptance check, end to
// end: reclaim a seat from a second socket and watch the first one receive
// BAD_SEAT and go.
//
// Both halves matter. Without the Error the client is disconnected for no
// stated reason and its own reconnect logic cannot tell a lost seat from a lost
// network. Without the close the socket stays live — pinged, queued for, and
// counted against MaxConns — while receiving nothing ever again.
func TestADisplacedSocketIsToldWhyAndThenClosed(t *testing.T) {
	hs := newTunedServer(t, Config{})

	first := newClient(t, hs)
	first.send(&genpb.ClientCommand{Cid: "j1",
		Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{DisplayName: "ada"}}})
	joined := first.awaitJoined()

	// A second socket reclaims the same seat with the same token — a phone
	// coming back on a different network, from the server's point of view.
	second := newClient(t, hs)
	second.send(&genpb.ClientCommand{Cid: "j2", Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		DisplayName: "ada",
		RoomCode:    joined.GetRoomCode(),
		SeatToken:   joined.GetSeatToken(),
	}}})
	if reclaimed := second.awaitJoined(); reclaimed.GetPlayerId() != joined.GetPlayerId() {
		t.Fatalf("reclaimed seat is %s, want %s", reclaimed.GetPlayerId(), joined.GetPlayerId())
	}

	evs, err := awaitClose(t, first.ws, 10*time.Second)
	if err == nil {
		t.Fatal("the displaced socket is still open")
	}
	e := lastError(evs)
	if e == nil {
		t.Fatalf("the displaced socket closed with no explanation (%d frames, close: %v)", len(evs), err)
	}
	if e.GetCode() != genpb.ErrorCode_ERROR_CODE_BAD_SEAT {
		t.Fatalf("displaced socket got %v, want BAD_SEAT", e.GetCode())
	}
	if status := websocket.CloseStatus(err); status != websocket.StatusNormalClosure {
		t.Fatalf("close status = %v (err %v), want a normal closure", status, err)
	}

	// The replacement is untouched and still being served.
	second.send(&genpb.ClientCommand{Cid: "r",
		Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}})
	second.await("LobbyState", func(ev *genpb.ServerEvent) bool { return ev.GetLobbyState() != nil })
}

// TestReconnectChurnDoesNotAccumulateSockets is the capacity claim behind S1.
//
// Thirty reclaims of one seat used to leave thirty live connections, each with
// its own goroutines and its own 64-frame queue, none of which would ever
// receive another room event. The count has to come back down on its own.
func TestReconnectChurnDoesNotAccumulateSockets(t *testing.T) {
	hs, srv := newTunedServerWith(t, Config{})

	first := newClient(t, hs)
	first.send(&genpb.ClientCommand{Cid: "j",
		Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{DisplayName: "ada"}}})
	joined := first.awaitJoined()

	const churn = 30
	live := first
	for range churn {
		next := newClient(t, hs)
		next.send(&genpb.ClientCommand{Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
			DisplayName: "ada",
			RoomCode:    joined.GetRoomCode(),
			SeatToken:   joined.GetSeatToken(),
		}}})
		next.awaitJoined()
		// Do not read `live` to completion: a real displaced client is a tab in
		// the background, and the server may not depend on it noticing.
		live = next
	}

	// One socket per seat, plus whatever is still finishing its close.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Live() <= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := srv.Live(); n > 2 {
		t.Fatalf("%d sockets still live after %d reclaims of one seat; displaced sockets are not closing",
			n, churn)
	}

	// And the surviving one is the seat's.
	live.send(&genpb.ClientCommand{Cid: "r",
		Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}})
	live.await("LobbyState", func(ev *genpb.ServerEvent) bool { return ev.GetLobbyState() != nil })
}

// TestAKickedSocketIsToldWhyAndThenClosed is the kick half. A kicked client
// that ignores the error must not be able to hold its connection open.
func TestAKickedSocketIsToldWhyAndThenClosed(t *testing.T) {
	hs := newTunedServer(t, Config{})

	host := newClient(t, hs)
	host.send(&genpb.ClientCommand{Cid: "j",
		Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{DisplayName: "host"}}})
	hostJoin := host.awaitJoined()

	guest := newClient(t, hs)
	guest.send(&genpb.ClientCommand{Cid: "g", Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		DisplayName: "guest", RoomCode: hostJoin.GetRoomCode(),
	}}})
	guestJoin := guest.awaitJoined()

	host.send(&genpb.ClientCommand{Cid: "k", Cmd: &genpb.ClientCommand_Kick{
		Kick: &genpb.KickPlayer{TargetPlayerId: guestJoin.GetPlayerId()}}})

	evs, err := awaitClose(t, guest.ws, 10*time.Second)
	if err == nil {
		t.Fatal("the kicked socket is still open")
	}
	e := lastError(evs)
	if e == nil {
		t.Fatalf("the kicked socket closed with no explanation (%d frames, close: %v)", len(evs), err)
	}
	if e.GetCode() != genpb.ErrorCode_ERROR_CODE_KICKED {
		t.Fatalf("kicked socket got %v, want KICKED", e.GetCode())
	}

	// The kicked seat's token is gone with it: reconnecting cannot get it back.
	back := newClient(t, hs)
	back.send(&genpb.ClientCommand{Cid: "b", Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		DisplayName: "guest",
		RoomCode:    hostJoin.GetRoomCode(),
		SeatToken:   guestJoin.GetSeatToken(),
	}}})
	ev := back.next(5 * time.Second)
	if ev.GetError().GetCode() != genpb.ErrorCode_ERROR_CODE_BAD_SEAT {
		t.Fatalf("a kicked seat token gave %T/%v, want BAD_SEAT",
			ev.GetEvt(), ev.GetError().GetCode())
	}
}
