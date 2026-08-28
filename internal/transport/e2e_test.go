package transport

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/registry"
	"github.com/sreyas-endor/verso/internal/room"
	"github.com/sreyas-endor/verso/internal/words"
)

// client is a minimal protocol client: enough to drive a real match over a real
// socket, and nothing else. It keeps every frame it has seen so a test can
// assert on what did — and did not — arrive.
type client struct {
	t    *testing.T
	ws   *websocket.Conn
	seen []*genpb.ServerEvent
}

func newClient(t *testing.T, hs *httptest.Server) *client {
	t.Helper()
	return &client{t: t, ws: dial(t, hs)}
}

func (c *client) send(cmd *genpb.ClientCommand) { send(c.t, c.ws, cmd) }

// next reads one frame.
func (c *client) next(timeout time.Duration) *genpb.ServerEvent {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageBinary {
		c.t.Fatalf("frame type = %v, want binary", typ)
	}
	ev := &genpb.ServerEvent{}
	if err := proto.Unmarshal(data, ev); err != nil {
		c.t.Fatalf("unmarshal: %v", err)
	}
	c.seen = append(c.seen, ev)
	return ev
}

// await reads until match returns true, or fails the test.
func (c *client) await(what string, match func(*genpb.ServerEvent) bool) *genpb.ServerEvent {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ev := c.next(time.Until(deadline))
		if e, ok := ev.GetEvt().(*genpb.ServerEvent_Error); ok {
			c.t.Fatalf("waiting for %s, got Error %v: %s", what, e.Error.GetCode(), e.Error.GetMessage())
		}
		if match(ev) {
			return ev
		}
	}
	c.t.Fatalf("timed out waiting for %s", what)
	return nil
}

func (c *client) awaitJoined() *genpb.Joined {
	c.t.Helper()
	ev := c.await("Joined", func(e *genpb.ServerEvent) bool { return e.GetJoined() != nil })
	return ev.GetJoined()
}

func (c *client) awaitYourWord() string {
	c.t.Helper()
	ev := c.await("YourWord", func(e *genpb.ServerEvent) bool { return e.GetYourWord() != nil })
	return ev.GetYourWord().GetWord()
}

// awaitRoster reads until a LobbyState carries an entry for id that satisfies
// want, and returns it. The roster arrives repeatedly and says different things
// each time, so a caller states which edition it is waiting for rather than
// taking whichever one happens to be next out of the socket.
func (c *client) awaitRoster(id string, want func(*genpb.PlayerInfo) bool) *genpb.PlayerInfo {
	c.t.Helper()
	var found *genpb.PlayerInfo
	c.await("roster entry for "+id, func(e *genpb.ServerEvent) bool {
		for _, pi := range e.GetLobbyState().GetPlayers() {
			if pi.GetId() == id && want(pi) {
				found = pi
				return true
			}
		}
		return false
	})
	return found
}

// TestMatchOverTheWire drives a real three-player match through the real
// transport and asserts the one thing that must never break: no player's word
// reaches another player's socket (IMPLEMENTATION_PLAN.md §1). This is the
// empirical defense at the layer that actually puts bytes on a network.
func TestMatchOverTheWire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.DiscardHandler)

	reg := registry.New(ctx, registry.Config{
		NewDeck: func() room.Deck { return words.New() },
		Settings: &genpb.MatchSettings{
			Difficulty:     genpb.Difficulty_DIFFICULTY_EASY,
			MaxRounds:      1,
			DrawSeconds:    room.MinDrawSeconds,
			DiscussSeconds: room.MinDiscussSeconds,
		},
		Logger: log,
	})
	ws := New(ctx, Config{Registry: reg, Logger: log})

	mux := http.NewServeMux()
	mux.Handle("/ws", ws.Handler())
	hs := httptest.NewServer(mux)
	t.Cleanup(func() {
		cancel()
		hs.Close()
		_ = reg.Close(context.Background())
	})

	// The host creates the room by joining with no code.
	host := newClient(t, hs)
	host.send(&genpb.ClientCommand{Cid: "j1", Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		DisplayName:     "ada",
		ProtocolVersion: room.ProtocolVersion,
	}}})
	hostJoin := host.awaitJoined()
	code := hostJoin.GetRoomCode()
	if len(code) != registry.CodeLen {
		t.Fatalf("room code %q, want %d characters", code, registry.CodeLen)
	}
	if !hostJoin.GetIsHost() {
		t.Fatal("creator is not the host")
	}
	if hostJoin.GetSeatToken() == "" {
		t.Fatal("no seat token issued")
	}

	guests := make([]*client, 0, 2)
	joins := []*genpb.Joined{hostJoin}
	for _, name := range []string{"grace", "alan"} {
		g := newClient(t, hs)
		g.send(&genpb.ClientCommand{Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
			RoomCode:        strings.ToLower(code), // codes are normalised
			DisplayName:     name,
			ProtocolVersion: room.ProtocolVersion,
		}}})
		joins = append(joins, g.awaitJoined())
		guests = append(guests, g)
	}
	all := append([]*client{host}, guests...)

	for _, c := range all {
		c.send(&genpb.ClientCommand{Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}})
	}
	// The host may only start once every seat is ready, so wait for the lobby
	// state that says so rather than racing it.
	host.await("can_start", func(e *genpb.ServerEvent) bool { return e.GetLobbyState().GetCanStart() })

	host.send(&genpb.ClientCommand{Cid: "start", Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}}})

	mine := make([]string, len(all))
	for i, c := range all {
		mine[i] = c.awaitYourWord()
		if mine[i] == "" {
			t.Fatalf("player %d received an empty word", i)
		}
	}

	// Exactly one word differs: two players share the common word and one holds
	// the imposter's (DESIGN.md:31).
	counts := map[string]int{}
	for _, w := range mine {
		counts[w]++
	}
	if len(counts) != 2 {
		t.Fatalf("words %v span %d distinct values, want 2", mine, len(counts))
	}

	// The canary. Every frame each client received, other than the YourWord and
	// Snapshot addressed to it, must be free of every other player's word.
	for i, c := range all {
		for _, ev := range c.seen {
			switch ev.GetEvt().(type) {
			case *genpb.ServerEvent_YourWord, *genpb.ServerEvent_Snapshot:
				continue // the recipient's own secret, by design
			}
			raw, err := proto.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			for j, w := range mine {
				if j == i {
					continue
				}
				if mine[j] == mine[i] {
					continue // indistinguishable: both hold the common word
				}
				if strings.Contains(string(raw), w) {
					t.Fatalf("player %d received a frame (%T) containing player %d's word",
						i, ev.GetEvt(), j)
				}
			}
		}
	}

	// A guest drops and reclaims its seat with the token it was issued. The
	// token is opaque to transport: it goes back out exactly as it came in.
	dropped := guests[0]
	token := joins[1].GetSeatToken()
	playerID := joins[1].GetPlayerId()
	dropped.ws.CloseNow()

	back := newClient(t, hs)
	back.send(&genpb.ClientCommand{Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		RoomCode:        code,
		SeatToken:       token,
		ProtocolVersion: room.ProtocolVersion,
	}}})
	rejoin := back.awaitJoined()
	if !rejoin.GetReconnected() {
		t.Fatal("reclaiming a seat did not report reconnected")
	}
	if rejoin.GetPlayerId() != playerID {
		t.Fatalf("reconnected as %q, want %q", rejoin.GetPlayerId(), playerID)
	}

	snap := back.await("Snapshot", func(e *genpb.ServerEvent) bool { return e.GetSnapshot() != nil }).GetSnapshot()
	if got := snap.GetYourWord(); got != mine[1] {
		t.Fatalf("snapshot word = %q, want the seat's original %q", got, mine[1])
	}

	// A forged token is refused.
	forger := newClient(t, hs)
	forger.send(&genpb.ClientCommand{Cid: "bad", Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		RoomCode:        code,
		SeatToken:       strings.Repeat("0", len(token)),
		ProtocolVersion: room.ProtocolVersion,
	}}})
	if got := expectError(t, forger.ws).GetCode(); got != genpb.ErrorCode_ERROR_CODE_BAD_SEAT {
		t.Fatalf("forged seat token gave %v, want BAD_SEAT", got)
	}
}

// TestReclaimIgnoresTheAvatarOnTheWire is the wire-level half of
// room.TestReconnectKeepsTheSeatedAvatar. That test can only show that
// Room.Attach has nowhere to put a conflicting avatar; here the conflicting
// value genuinely exists — a second JoinRoom frame, on a second socket, naming
// a different portrait for a seat the server already painted.
//
// The regression it catches is a plausible one line: handleJoin reading
// JoinRoom.avatar on the reclaim branch the way it does on the fresh-seat
// branch. A client that lets a player re-pick on the reconnect screen, or one
// that simply forgot what it chose and sent the field's zero value, would then
// repaint a seat the rest of the table has been looking at all match.
func TestReclaimIgnoresTheAvatarOnTheWire(t *testing.T) {
	hs, _ := newTestServer(t)

	host := newClient(t, hs)
	host.send(&genpb.ClientCommand{Cid: "j1", Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		DisplayName:     "ada",
		Avatar:          genpb.Avatar_AVATAR_MASON,
		ProtocolVersion: room.ProtocolVersion,
	}}})
	code := host.awaitJoined().GetRoomCode()

	guest := newClient(t, hs)
	guest.send(&genpb.ClientCommand{Cid: "j2", Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		RoomCode:        code,
		DisplayName:     "grace",
		Avatar:          genpb.Avatar_AVATAR_SCOUT,
		ProtocolVersion: room.ProtocolVersion,
	}}})
	guestJoin := guest.awaitJoined()
	seat, token := guestJoin.GetPlayerId(), guestJoin.GetSeatToken()
	if token == "" {
		t.Fatal("no seat token issued")
	}
	if got := host.awaitRoster(seat, func(pi *genpb.PlayerInfo) bool {
		return pi.GetConnected()
	}).GetAvatar(); got != genpb.Avatar_AVATAR_SCOUT {
		t.Fatalf("roster avatar on join = %v, want SCOUT", got)
	}

	// Drop the socket, and wait for the room to have noticed before reclaiming:
	// otherwise the roster we assert on at the end could be the one published
	// for the join, which proves nothing about the reclaim.
	guest.ws.CloseNow()
	host.awaitRoster(seat, func(pi *genpb.PlayerInfo) bool { return !pi.GetConnected() })

	back := newClient(t, hs)
	back.send(&genpb.ClientCommand{Cid: "j3", Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		RoomCode:        code,
		SeatToken:       token,
		Avatar:          genpb.Avatar_AVATAR_COOK, // the conflicting value
		ProtocolVersion: room.ProtocolVersion,
	}}})
	rejoin := back.awaitJoined()
	if !rejoin.GetReconnected() {
		t.Fatal("reclaiming a seat did not report reconnected")
	}
	if rejoin.GetPlayerId() != seat {
		t.Fatalf("reconnected as %q, want %q", rejoin.GetPlayerId(), seat)
	}

	// Both views of the roster, because they are two different broadcasts and a
	// leak into either one is the bug: what the returning player is shown, and
	// what everyone who never went anywhere is shown.
	for _, tc := range []struct {
		who string
		c   *client
	}{
		{"the returning player", back},
		{"the host", host},
	} {
		got := tc.c.awaitRoster(seat, func(pi *genpb.PlayerInfo) bool {
			return pi.GetConnected()
		}).GetAvatar()
		if got != genpb.Avatar_AVATAR_SCOUT {
			t.Fatalf("roster avatar after the reclaim, seen by %s = %v, want the seated SCOUT",
				tc.who, got)
		}
	}
}
