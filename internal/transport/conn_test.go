package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/registry"
	"github.com/sreyas-endor/verso/internal/room"
)

// newTestServer stands up the real transport in front of a real registry. No
// room actor is ever started here: every case below is a rejection that must
// happen at the socket boundary, before anything reaches a room.
func newTestServer(t *testing.T) (*httptest.Server, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.DiscardHandler)

	reg := registry.New(ctx, registry.Config{NewDeck: func() room.Deck { return nilDeck{} }, Logger: log})
	ws := New(ctx, Config{Registry: reg, Logger: log})

	mux := http.NewServeMux()
	mux.Handle("/ws", ws.Handler())
	hs := httptest.NewServer(mux)

	t.Cleanup(func() {
		cancel()
		hs.Close()
		_ = reg.Close(context.Background())
	})
	return hs, cancel
}

type nilDeck struct{}

func (nilDeck) Pair(genpb.Difficulty, *mrand.Rand, []string) (string, string) { return "", "" }

func dial(t *testing.T, hs *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, "ws"+hs.URL[len("http"):]+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func send(t *testing.T, c *websocket.Conn, cmd *genpb.ClientCommand) {
	t.Helper()
	b, err := proto.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func expectError(t *testing.T, c *websocket.Conn) *genpb.Error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("frame type = %v, want binary", typ)
	}
	var ev genpb.ServerEvent
	if err := proto.Unmarshal(data, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	e, ok := ev.GetEvt().(*genpb.ServerEvent_Error)
	if !ok {
		t.Fatalf("event = %T, want Error", ev.GetEvt())
	}
	return e.Error
}

func TestTextFrameIsRejected(t *testing.T) {
	hs, _ := newTestServer(t)
	c := dial(t, hs)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte("{}")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusUnsupportedData {
		t.Fatalf("close status = %v (err %v), want %v",
			websocket.CloseStatus(err), err, websocket.StatusUnsupportedData)
	}
}

func TestMalformedFrameIsRejected(t *testing.T) {
	hs, _ := newTestServer(t)
	c := dial(t, hs)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Field 1 declared as a varint but truncated: not a decodable ClientCommand.
	if err := c.Write(ctx, websocket.MessageBinary, []byte{0x08}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := expectError(t, c).GetCode(); got != genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND {
		t.Fatalf("code = %v, want INVALID_COMMAND", got)
	}
}

func TestUnsetOneofIsRejected(t *testing.T) {
	hs, _ := newTestServer(t)
	c := dial(t, hs)

	send(t, c, &genpb.ClientCommand{Cid: "c1"})
	e := expectError(t, c)
	if e.GetCode() != genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND {
		t.Fatalf("code = %v, want INVALID_COMMAND", e.GetCode())
	}
}

func TestCommandBeforeJoinIsRejected(t *testing.T) {
	hs, _ := newTestServer(t)
	c := dial(t, hs)

	send(t, c, &genpb.ClientCommand{
		Cid: "c2",
		Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}},
	})
	if got := expectError(t, c).GetCode(); got != genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND {
		t.Fatalf("code = %v, want INVALID_COMMAND", got)
	}
}

func TestUnknownRoomCode(t *testing.T) {
	hs, _ := newTestServer(t)
	c := dial(t, hs)

	send(t, c, &genpb.ClientCommand{
		Cid: "c3",
		Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
			RoomCode:        "ZZZZZ",
			DisplayName:     "ada",
			ProtocolVersion: room.ProtocolVersion,
		}},
	})
	if got := expectError(t, c).GetCode(); got != genpb.ErrorCode_ERROR_CODE_ROOM_NOT_FOUND {
		t.Fatalf("code = %v, want ROOM_NOT_FOUND", got)
	}
}

func TestProtocolVersionMismatch(t *testing.T) {
	hs, _ := newTestServer(t)
	c := dial(t, hs)

	send(t, c, &genpb.ClientCommand{
		Cid: "c4",
		Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
			DisplayName:     "ada",
			ProtocolVersion: room.ProtocolVersion + 1,
		}},
	})
	if got := expectError(t, c).GetCode(); got != genpb.ErrorCode_ERROR_CODE_PROTOCOL_VERSION {
		t.Fatalf("code = %v, want PROTOCOL_VERSION", got)
	}
}

func TestCastVoteWithoutChoiceIsRejected(t *testing.T) {
	hs, _ := newTestServer(t)
	c := dial(t, hs)

	// The inner union is validated at the boundary too: an unset choice is not
	// silently promoted to Skip.
	send(t, c, &genpb.ClientCommand{
		Cid: "c5",
		Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{}},
	})
	if got := expectError(t, c).GetCode(); got != genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND {
		t.Fatalf("code = %v, want INVALID_COMMAND", got)
	}
}

func TestReadLimitClosesTheSocket(t *testing.T) {
	hs, _ := newTestServer(t)
	c := dial(t, hs)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, make([]byte, DefaultReadLimit+1)); err != nil {
		// Some stacks report the peer's close before the write completes.
		if !errors.Is(err, io.EOF) && websocket.CloseStatus(err) == -1 {
			t.Fatalf("write: %v", err)
		}
		return
	}
	if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("close status = %v (err %v), want %v",
			websocket.CloseStatus(err), err, websocket.StatusMessageTooBig)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"  ada  ":        "ada",
		"ada\nlovelace":  "ada lovelace",
		"a\u200bb":       "ab",
		"\x00\x01":       "",
		"grace  hopper ": "grace hopper",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBucketRefills(t *testing.T) {
	b := newBucket(2, 1)
	now := time.Now()
	if !b.allow(now) || !b.allow(now) {
		t.Fatal("burst should admit two")
	}
	if b.allow(now) {
		t.Fatal("third call should be refused")
	}
	if !b.allow(now.Add(time.Second)) {
		t.Fatal("one token should have refilled after a second")
	}
}
