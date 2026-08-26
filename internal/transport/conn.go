package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/registry"
	"github.com/sreyas-endor/verso/internal/room"
)

// conn is one WebSocket. Two goroutines touch it: the HTTP handler runs the read
// loop, and writePump drains out. Everything below the "read loop only" comment
// belongs to the handler goroutine alone.
type conn struct {
	id     uint64
	srv    *Server
	log    *slog.Logger
	ws     *websocket.Conn
	remote string

	ctx    context.Context
	cancel context.CancelFunc

	// out is the room's view of this socket. The room goroutine sends into it
	// with a non-blocking send and drops on a full queue, so a slow client can
	// never stall a match (room/api.go, deliver).
	out chan *genpb.ServerEvent

	// ping carries a nudge from the shared sweep. Depth 1: a second nudge while
	// the first is unserviced tells us nothing new.
	ping chan struct{}

	// wdone closes when writePump returns.
	wdone chan struct{}

	// last is the most recent sign of life, in Unix nanoseconds. Written by the
	// read loop and by the ping/pong callbacks, read by the sweep.
	last atomic.Int64

	// --- read loop only ---
	cmds     bucket
	strikes  int
	room     *room.Room
	code     string
	playerID string
	held     bool
}

func (c *conn) touch(t time.Time)   { c.last.Store(t.UnixNano()) }
func (c *conn) lastSeen() time.Time { return time.Unix(0, c.last.Load()) }

// enqueue posts an event straight into this socket's queue. Transport-generated
// errors travel the same path as room events so their ordering is preserved.
func (c *conn) enqueue(ev *genpb.ServerEvent) {
	select {
	case c.out <- ev:
	default:
		c.log.Warn("outbound queue full, frame dropped")
	}
}

// reject answers one command with a typed Error, correlated by cid.
func (c *conn) reject(cid string, code genpb.ErrorCode, msg string) {
	c.enqueue(&genpb.ServerEvent{
		Cid: cid,
		Evt: &genpb.ServerEvent_Error{Error: &genpb.Error{Code: code, Message: msg}},
	})
}

// leave releases everything this socket held. Called once, after the writer has
// stopped.
func (c *conn) leave() {
	if c.playerID != "" && c.room != nil {
		c.room.Detach(c.playerID, c.out)
		c.log.Info("player detached", "room", c.code, "player", c.playerID)
	}
	if c.held {
		c.srv.reg.Release(c.code)
		c.held = false
	}
}

// ---------------------------------------------------------------------------
// write side
// ---------------------------------------------------------------------------

// writePump is the only writer on this socket. coder/websocket's Write is
// concurrency-safe, but keeping a single writer means the ping and the frames
// cannot interleave and the close handshake has a definite point to wait for.
func (c *conn) writePump() {
	defer close(c.wdone)

	for {
		select {
		case <-c.ctx.Done():
			return

		case <-c.ping:
			ctx, cancel := context.WithTimeout(c.ctx, c.srv.cfg.PongTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				if c.ctx.Err() == nil {
					c.log.Info("ping failed, closing", "err", err)
				}
				c.cancel()
				return
			}

		case ev := <-c.out:
			b, err := proto.Marshal(ev)
			if err != nil {
				// A ServerEvent the room built cannot fail to marshal; if it
				// does, the frame is the problem, not the socket.
				c.log.Error("marshal server event failed", "err", err)
				continue
			}
			ctx, cancel := context.WithTimeout(c.ctx, c.srv.cfg.WriteTimeout)
			err = c.ws.Write(ctx, websocket.MessageBinary, b)
			cancel()
			if err != nil {
				if c.ctx.Err() == nil {
					c.log.Debug("write failed, closing", "err", err)
				}
				c.cancel()
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// read side
// ---------------------------------------------------------------------------

// readLoop consumes frames until the socket dies. It returns the close status to
// send back.
func (c *conn) readLoop() (websocket.StatusCode, string) {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return closeStatusFor(c.ctx, err)
		}
		now := time.Now()
		c.touch(now)

		// ProtoJSON is not accepted, ever: the protocol is binary protobuf and a
		// text frame is either a confused client or a probe.
		if typ != websocket.MessageBinary {
			return websocket.StatusUnsupportedData, "binary frames only"
		}

		if !c.cmds.allow(now) {
			c.strikes++
			if c.strikes > maxRateStrikes {
				c.log.Info("rate limit exceeded, closing")
				return websocket.StatusPolicyViolation, "command rate limit exceeded"
			}
			c.reject("", genpb.ErrorCode_ERROR_CODE_RATE_LIMITED, "slow down")
			continue
		}
		// Strikes count *consecutive* refusals, so a client that backs off is
		// forgiven rather than accumulating a death sentence over a long match.
		c.strikes = 0

		cmd := &genpb.ClientCommand{}
		if err := proto.Unmarshal(data, cmd); err != nil {
			c.reject("", genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, "malformed frame")
			continue
		}

		// The union is validated here and nowhere else downstream: past this
		// line the room may assume the oneof is set and its payload is non-nil.
		if err := validate(cmd); err != nil {
			c.reject(cmd.GetCid(), genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, err.Error())
			continue
		}

		if join, ok := cmd.GetCmd().(*genpb.ClientCommand_Join); ok {
			c.handleJoin(cmd.GetCid(), join.Join)
			continue
		}

		if c.playerID == "" {
			c.reject(cmd.GetCid(), genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, "join a room first")
			continue
		}

		// PlayerID comes from the seat this socket established, never from the
		// frame. There is no field on ClientCommand that could carry identity,
		// and this is why.
		c.room.Submit(room.Command{PlayerID: c.playerID, Cmd: cmd, Out: c.out})
	}
}

// validate checks the command union at the socket boundary. It rejects an unset
// oneof and a set-but-empty variant; it does not check game rules, which belong
// to the room.
func validate(cmd *genpb.ClientCommand) error {
	switch v := cmd.GetCmd().(type) {
	case nil:
		return errors.New("empty command")
	case *genpb.ClientCommand_Join:
		if v.Join == nil {
			return errors.New("join: missing payload")
		}
	case *genpb.ClientCommand_SetReady:
		if v.SetReady == nil {
			return errors.New("set_ready: missing payload")
		}
	case *genpb.ClientCommand_UpdateSettings:
		if v.UpdateSettings == nil {
			return errors.New("update_settings: missing payload")
		}
	case *genpb.ClientCommand_StartMatch:
		if v.StartMatch == nil {
			return errors.New("start_match: missing payload")
		}
	case *genpb.ClientCommand_StrokeBegin:
		if v.StrokeBegin == nil {
			return errors.New("stroke_begin: missing payload")
		}
	case *genpb.ClientCommand_StrokePoints:
		if v.StrokePoints == nil {
			return errors.New("stroke_points: missing payload")
		}
	case *genpb.ClientCommand_StrokeEnd:
		if v.StrokeEnd == nil {
			return errors.New("stroke_end: missing payload")
		}
	case *genpb.ClientCommand_CastVote:
		if v.CastVote == nil {
			return errors.New("cast_vote: missing payload")
		}
		// The inner oneof is a union too, and an unset choice is not "Skip" —
		// Skip is an explicit, irreversible answer (DESIGN.md:51).
		switch cv := v.CastVote.GetChoice().(type) {
		case nil:
			return errors.New("cast_vote: no choice")
		case *genpb.CastVote_Skip:
			if !cv.Skip {
				return errors.New("cast_vote: skip must be true when set")
			}
		}
	case *genpb.ClientCommand_RequestSnapshot:
		if v.RequestSnapshot == nil {
			return errors.New("request_snapshot: missing payload")
		}
	case *genpb.ClientCommand_Rematch:
		if v.Rematch == nil {
			return errors.New("rematch: missing payload")
		}
	default:
		// A newer client sent a variant this server does not know.
		return errors.New("unsupported command")
	}
	return nil
}

// handleJoin binds this socket to a seat: it creates a room, takes a fresh seat,
// or reclaims one with a seat token.
func (c *conn) handleJoin(cid string, j *genpb.JoinRoom) {
	if c.playerID != "" {
		c.reject(cid, genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, "already joined")
		return
	}

	// Zero means the client did not state a version; anything else must match
	// exactly, because a mismatched oneof number is a silently wrong game.
	if v := j.GetProtocolVersion(); v != 0 && v != room.ProtocolVersion {
		c.reject(cid, genpb.ErrorCode_ERROR_CODE_PROTOCOL_VERSION,
			fmt.Sprintf("server speaks protocol version %d", room.ProtocolVersion))
		return
	}

	name := sanitizeName(j.GetDisplayName())
	code := registry.NormalizeCode(j.GetRoomCode())
	token := j.GetSeatToken()

	if code == "" {
		c.createRoom(cid, name, token)
		return
	}

	rm, ok := c.srv.reg.Lookup(code)
	if !ok {
		c.reject(cid, genpb.ErrorCode_ERROR_CODE_ROOM_NOT_FOUND, "no room with that code")
		return
	}
	if !c.srv.reg.Hold(code) {
		c.reject(cid, genpb.ErrorCode_ERROR_CODE_ROOM_NOT_FOUND, "no room with that code")
		return
	}

	var (
		playerID string
		err      error
	)
	if token != "" {
		// The token is opaque and room-local. Transport passes it through
		// byte for byte: it does not parse it, derive from it, or log it.
		playerID, err = rm.Attach(token, c.out)
	} else {
		if name == "" {
			c.srv.reg.Release(code)
			c.reject(cid, genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, "a display name is required")
			return
		}
		playerID, _, err = rm.Seat(name, c.out)
	}
	if err != nil {
		c.srv.reg.Release(code)
		c.rejectJoin(cid, err)
		return
	}

	c.bind(rm, code, playerID, token != "")
}

// createRoom mints a room for a client that sent no code and attaches its host.
func (c *conn) createRoom(cid, name, token string) {
	if token != "" {
		c.reject(cid, genpb.ErrorCode_ERROR_CODE_ROOM_NOT_FOUND, "reconnecting needs a room code")
		return
	}
	if name == "" {
		c.reject(cid, genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, "a display name is required")
		return
	}
	if !c.srv.allowCreate(c.remote) {
		c.reject(cid, genpb.ErrorCode_ERROR_CODE_RATE_LIMITED, "too many rooms created from this address")
		return
	}

	created, err := c.srv.reg.Create(name)
	if err != nil {
		switch {
		case errors.Is(err, registry.ErrTooManyRooms), errors.Is(err, registry.ErrCodeExhausted):
			c.reject(cid, genpb.ErrorCode_ERROR_CODE_RATE_LIMITED, "the server is full, try again shortly")
		default:
			c.reject(cid, genpb.ErrorCode_ERROR_CODE_UNSPECIFIED, "could not create a room")
		}
		return
	}

	// Create already holds one reference on this socket's behalf, so the room
	// cannot be collected in the gap before the host attaches.
	playerID, err := created.Room.Attach(created.HostToken, c.out)
	if err != nil {
		c.srv.reg.Release(created.Code)
		c.rejectJoin(cid, err)
		return
	}

	c.bind(created.Room, created.Code, playerID, false)
}

// bind records the seat this socket now owns. The room itself has already sent
// Joined, and the lobby state or snapshot behind it.
func (c *conn) bind(rm *room.Room, code, playerID string, reconnect bool) {
	c.room = rm
	c.code = code
	c.playerID = playerID
	c.held = true
	c.log = c.log.With("room", code, "player", playerID)
	c.log.Info("player joined", "reconnect", reconnect)
}

// rejectJoin maps a room-level failure onto the wire enum. The sentinel strings
// in package room are safe to show; anything else is reported generically so an
// internal message shape never leaks.
func (c *conn) rejectJoin(cid string, err error) {
	code := room.ErrorCodeFor(err)
	msg := err.Error()
	switch {
	case errors.Is(err, room.ErrClosed):
		code, msg = genpb.ErrorCode_ERROR_CODE_ROOM_NOT_FOUND, "that room has closed"
	case code == genpb.ErrorCode_ERROR_CODE_UNSPECIFIED:
		msg = "could not join that room"
	}
	c.reject(cid, code, msg)
}

// sanitizeName strips control characters and collapses whitespace, then leaves
// length to the room, which owns MaxDisplayNameLen. A name is echoed to every
// other player, so it must not be able to carry a line break into a log or a
// zero-width run into the roster.
func sanitizeName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == ' ':
			space = true
		case unicode.IsControl(r), !unicode.IsGraphic(r):
			// Dropped: format characters, zero-width joiners, unassigned runes.
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

// closeStatusFor turns a read error into the status this server should send.
func closeStatusFor(ctx context.Context, err error) (websocket.StatusCode, string) {
	if ctx.Err() != nil {
		return websocket.StatusGoingAway, "server shutting down"
	}
	switch websocket.CloseStatus(err) {
	case -1:
		// Not a close frame: a read-limit breach, a protocol violation, or the
		// TCP connection simply going away.
		if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) ||
			errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.Canceled) {
			return websocket.StatusNormalClosure, ""
		}
		return websocket.StatusProtocolError, "read failed"
	default:
		return websocket.StatusNormalClosure, ""
	}
}
