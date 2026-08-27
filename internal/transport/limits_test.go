package transport

// limits_test.go — the inbound resource limits
// (PERFORMANCE_OPTIMIZATION_PLAN.md S2 and S3).
//
// Every constant this file asserts is a bound on work the server does BEFORE
// it knows whether it wanted to: bytes read, strings allocated, arrays decoded,
// stroke logs traversed. The room's own validation is downstream of all of it
// and cannot help — by the time ValidPoints sees a slice, the slice exists.
//
// Two properties are asserted together throughout: a violation is answered with
// an ordinary INVALID_COMMAND or RATE_LIMITED and never a panic or a silent
// stall, and a legal command of the same shape still goes through, so a limit
// is a ceiling and not a new wall.

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/room"
)

// maxLegalCommand builds the largest command the protocol permits: a full
// MaxPointsPerStroke-pair stroke at the extreme coordinates, which is where the
// zigzag varints reach their widest, under a maximum-length correlation id.
func maxLegalCommand(cid string) *genpb.ClientCommand {
	pts := make([]int32, 0, maxPointValues)
	for i := range room.MaxPointsPerStroke {
		// Alternating extremes: both room.CoordMin and room.CoordMax zigzag to
		// a 3-byte varint, which is the widest a legal coordinate can encode.
		if i%2 == 0 {
			pts = append(pts, room.CoordMax, room.CoordMin)
		} else {
			pts = append(pts, room.CoordMin, room.CoordMax)
		}
	}
	return &genpb.ClientCommand{
		Cid: cid,
		Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
			ColorIndex: room.PaletteSize - 1,
			Width:      room.MaxStrokeWidth,
			Points:     pts,
		}},
	}
}

// TestTheReadLimitFitsTheLargestLegalCommand is the check that lets
// DefaultReadLimit be tightened at all.
//
// A read limit below the protocol's own maximum is not a rate limit, it is a
// bug that only shows up for the artists who draw the longest strokes — and it
// shows up as a dropped socket, because coder/websocket fails the read rather
// than the frame. So the constant is measured, not reasoned about.
func TestTheReadLimitFitsTheLargestLegalCommand(t *testing.T) {
	b, err := proto.Marshal(maxLegalCommand(strings.Repeat("c", MaxCidLen)))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if int64(len(b)) > DefaultReadLimit {
		t.Fatalf("the largest legal command encodes to %d bytes, over the %d-byte read limit",
			len(b), DefaultReadLimit)
	}
	// Headroom, so a future field on StrokeBegin does not silently start
	// truncating real strokes. 2x is the margin the constant was chosen with.
	if int64(len(b))*2 > DefaultReadLimit {
		t.Fatalf("the largest legal command is %d bytes against a %d-byte read limit: "+
			"under 2x headroom, raise DefaultReadLimit or justify the margin",
			len(b), DefaultReadLimit)
	}
	t.Logf("largest legal command: %d bytes, read limit %d", len(b), DefaultReadLimit)
}

// TestALegalMaximumStrokeSurvivesTheReadLimit is the same fact end to end: the
// biggest frame an honest client can send is accepted by a real socket.
func TestALegalMaximumStrokeSurvivesTheReadLimit(t *testing.T) {
	hs, _ := newTestServer(t)
	c := dial(t, hs)

	send(t, c, &genpb.ClientCommand{Cid: "j",
		Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{DisplayName: "ada"}}})

	// The stroke is refused on a game rule — nobody is drawing — which is
	// exactly the point: it was read, decoded and routed rather than killing
	// the socket at the read limit.
	send(t, c, maxLegalCommand("big"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("the socket died on a legal maximum stroke: %v", err)
		}
		var ev genpb.ServerEvent
		if err := proto.Unmarshal(data, &ev); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if e := ev.GetError(); e != nil && ev.GetCid() == "big" {
			if e.GetCode() == genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND {
				t.Fatalf("a legal maximum stroke was rejected as invalid: %s", e.GetMessage())
			}
			return // WRONG_PHASE or NOT_ARTIST: it reached the room.
		}
	}
	t.Fatal("no answer to the maximum-size stroke")
}

// TestValidateBoundsEveryClientSuppliedLength walks the fields a client can
// make arbitrarily long. Each one is checked at its limit and one byte past it,
// so the test pins the boundary rather than merely observing that something
// enormous is refused.
func TestValidateBoundsEveryClientSuppliedLength(t *testing.T) {
	pts := func(n int) []int32 { return make([]int32, n) }

	cases := []struct {
		name string
		at   *genpb.ClientCommand // exactly at the limit: must pass
		over *genpb.ClientCommand // one past it: must fail
	}{
		{
			name: "display_name",
			at:   join(strings.Repeat("a", MaxRawNameLen), "", ""),
			over: join(strings.Repeat("a", MaxRawNameLen+1), "", ""),
		},
		{
			name: "room_code",
			at:   join("ada", strings.Repeat("A", MaxRoomCodeLen), ""),
			over: join("ada", strings.Repeat("A", MaxRoomCodeLen+1), ""),
		},
		{
			name: "seat_token",
			at:   join("ada", "ABCDE", strings.Repeat("0", MaxSeatTokenLen)),
			over: join("ada", "ABCDE", strings.Repeat("0", MaxSeatTokenLen+1)),
		},
		{
			name: "kick target_player_id",
			at:   kick(strings.Repeat("f", MaxPlayerIDLen)),
			over: kick(strings.Repeat("f", MaxPlayerIDLen+1)),
		},
		{
			name: "stroke_begin points",
			at: &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeBegin{
				StrokeBegin: &genpb.StrokeBegin{Points: pts(maxPointValues)}}},
			over: &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeBegin{
				StrokeBegin: &genpb.StrokeBegin{Points: pts(maxPointValues + 1)}}},
		},
		{
			name: "stroke_points points",
			at: &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokePoints{
				StrokePoints: &genpb.StrokePoints{Points: pts(maxPointValues)}}},
			over: &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokePoints{
				StrokePoints: &genpb.StrokePoints{Points: pts(maxPointValues + 1)}}},
		},
		{
			name: "stroke_end points",
			at: &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeEnd{
				StrokeEnd: &genpb.StrokeEnd{Points: pts(maxPointValues)}}},
			over: &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeEnd{
				StrokeEnd: &genpb.StrokeEnd{Points: pts(maxPointValues + 1)}}},
		},
	}

	for _, tc := range cases {
		if err := validate(tc.at); err != nil {
			t.Errorf("%s: rejected at the limit: %v", tc.name, err)
		}
		if err := validate(tc.over); err == nil {
			t.Errorf("%s: accepted one byte past the limit", tc.name)
		}
	}
}

func join(name, code, token string) *genpb.ClientCommand {
	return &genpb.ClientCommand{Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{
		DisplayName: name, RoomCode: code, SeatToken: token,
	}}}
}

func kick(target string) *genpb.ClientCommand {
	return &genpb.ClientCommand{Cmd: &genpb.ClientCommand_Kick{
		Kick: &genpb.KickPlayer{TargetPlayerId: target}}}
}

// TestAnOverlongCorrelationIdIsNotEchoedBack is the amplification case that
// motivates MaxCidLen. The cid comes back on the event a command produced, so
// answering an over-long one with itself would be the server helping.
func TestAnOverlongCorrelationIdIsNotEchoedBack(t *testing.T) {
	hs, _ := newTestServer(t)
	c := dial(t, hs)

	huge := strings.Repeat("z", MaxCidLen+1)
	send(t, c, &genpb.ClientCommand{Cid: huge,
		Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{DisplayName: "ada"}}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var ev genpb.ServerEvent
	if err := proto.Unmarshal(data, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.GetError() == nil {
		t.Fatalf("event = %T, want Error", ev.GetEvt())
	}
	if ev.GetCid() != "" {
		t.Fatalf("the rejection echoed a %d-byte cid back", len(ev.GetCid()))
	}
	if strings.Contains(ev.GetError().GetMessage(), huge) {
		t.Fatal("the rejection message quoted the client's cid")
	}
}

// TestSnapshotRequestsAreRateLimitedSeparately is S3.
//
// The generic command bucket allows 40 a second, and a snapshot is the one
// command whose answer is the entire room. Bursting past the snapshot bucket
// must produce RATE_LIMITED rather than a pile of full-canvas encodes — and
// crucially, must not cost the connection: the client is told to slow down and
// stays joined.
func TestSnapshotRequestsAreRateLimitedSeparately(t *testing.T) {
	hs := newTunedServer(t, Config{
		// Generous enough that the generic bucket cannot be what refuses.
		CommandBurst: 500, CommandRate: 500,
		SnapshotBurst: 2, SnapshotRate: 1,
	})
	c := dial(t, hs)

	send(t, c, &genpb.ClientCommand{Cid: "j",
		Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{DisplayName: "ada"}}})

	const asks = 12
	for i := range asks {
		send(t, c, &genpb.ClientCommand{Cid: snapCid(i),
			Cmd: &genpb.ClientCommand_RequestSnapshot{RequestSnapshot: &genpb.RequestSnapshot{}}})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	snapshots, limited := 0, 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && snapshots+limited < asks {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("the socket died under a snapshot burst: %v", err)
		}
		var ev genpb.ServerEvent
		if err := proto.Unmarshal(data, &ev); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		switch {
		case ev.GetSnapshot() != nil && ev.GetCid() != "":
			snapshots++
		case ev.GetError().GetCode() == genpb.ErrorCode_ERROR_CODE_RATE_LIMITED:
			limited++
		}
	}

	// The join's own snapshot carries no cid and is not counted: it comes out
	// of Attach, not from a command, and the limiter must never delay it.
	if snapshots > 3 {
		t.Fatalf("%d of %d snapshot requests were answered; the bucket allows a burst of 2",
			snapshots, asks)
	}
	if limited == 0 {
		t.Fatal("a burst of snapshot requests was never rate limited")
	}

	// Still a live, joined connection: this is a refusal, not a punishment.
	send(t, c, &genpb.ClientCommand{Cid: "ready",
		Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}})
	for time.Now().Before(deadline.Add(5 * time.Second)) {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("the connection was dropped for asking too often: %v", err)
		}
		var ev genpb.ServerEvent
		if err := proto.Unmarshal(data, &ev); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if ev.GetLobbyState() != nil {
			return
		}
	}
	t.Fatal("the connection stopped answering after the snapshot limiter fired")
}

func snapCid(i int) string { return "s" + string(rune('a'+i%26)) }
