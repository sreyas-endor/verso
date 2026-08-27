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

	// term carries the room's request to close this socket after the frames
	// already queued have been written. Depth 1 and never closed, so a second
	// request while the first is unserviced is dropped rather than panicking.
	term chan struct{}

	// terminating records that the close came from the room rather than from
	// shutdown or a dead peer. It changes only what the close frame says.
	terminating atomic.Bool

	// wdone closes when writePump returns.
	wdone chan struct{}

	// last is the most recent sign of life, in Unix nanoseconds. Written by the
	// read loop and by the ping/pong callbacks, read by the sweep.
	last atomic.Int64

	// --- read loop only ---
	cmds     bucket
	snaps    bucket
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

// Send implements room.Session. The room hands events here from its own
// goroutine; enqueue never blocks, so the actor is never held up by one client.
func (c *conn) Send(ev *genpb.ServerEvent) { c.enqueue(ev) }

// Close implements room.Session: write what is already queued, then shut the
// socket. It only raises a flag — the write pump does the work — so the room
// actor returns immediately and never waits on a socket it is closing.
//
// Deliberately NOT c.cancel(). Cancelling here would race the writer and
// discard the terminal Error the room queued a line earlier, leaving a
// displaced or kicked client with a closed socket and no reason for it.
func (c *conn) Close() {
	c.terminating.Store(true)
	select {
	case c.term <- struct{}{}:
	default:
		// A close is already pending; one is all it takes.
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
//
// It passes itself, not its queue: the room compares the session against the
// seat's current one and ignores the call when a reconnect has already replaced
// it, so a displaced socket finishing its teardown cannot mark the live
// replacement disconnected.
func (c *conn) leave() {
	if c.playerID != "" && c.room != nil {
		c.room.Detach(c.playerID, c)
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

		case <-c.term:
			// The room displaced or removed this seat and has already queued
			// the Error saying so. Put that on the wire before going.
			c.flushAndClose()
			return

		case ev := <-c.out:
			if !c.writeEvent(c.ctx, ev) {
				return
			}
		}
	}
}

// writeEvent marshals and writes one frame, under a WriteTimeout derived from
// ctx. It returns false once the socket is finished, having already cancelled
// it.
//
// ctx is a parameter rather than always c.ctx so the terminal flush can put one
// deadline across every frame it writes; the per-frame timeout nests inside it,
// and the tighter of the two wins.
func (c *conn) writeEvent(ctx context.Context, ev *genpb.ServerEvent) bool {
	b, err := proto.Marshal(ev)
	if err != nil {
		// A ServerEvent the room built cannot fail to marshal; if it does, the
		// frame is the problem, not the socket.
		c.log.Error("marshal server event failed", "err", err)
		return true
	}
	wctx, cancel := context.WithTimeout(ctx, c.srv.cfg.WriteTimeout)
	err = c.ws.Write(wctx, websocket.MessageBinary, b)
	cancel()
	if err != nil {
		if c.ctx.Err() == nil {
			c.log.Debug("write failed, closing", "err", err)
		}
		c.cancel()
		return false
	}
	return true
}

// terminalCloseReason is what a client sees when the room ended its session on
// purpose. The Error frame that went out first carries the actual reason.
const terminalCloseReason = "seat closed"

// flushAndClose drains the frames the room queued before it asked for the
// close, then shuts the socket down.
//
// Bounded three ways, because this runs on behalf of a client that may be gone:
// the queue itself is bounded at room.OutboundQueueDepth, the drain stops at
// the first frame that is not already waiting rather than lingering for more,
// and one WriteTimeout covers the whole flush instead of each frame in it — so
// a dead peer costs the write pump one timeout, not sixty-four.
//
// The socket is then closed with a real close handshake rather than by
// cancelling c.ctx. That distinction is the whole delivery guarantee:
// coder/websocket cannot interrupt a read in progress, so cancelling the read
// context makes it drop the TCP connection outright — the peer gets an EOF, no
// close frame, and no way to tell a deliberate removal from a flaky network.
// Close writes the frame and waits, on its own bounded timer, for the peer's
// reply; that reply is also what unblocks the read loop. The cancel afterwards
// is the backstop for a Close that could not complete.
//
// A peer that has stopped reading costs coder/websocket's 5 s close-handshake
// timeout before the socket is released, on this goroutine and no other. That
// is the pathological case — a browser's WebSocket answers a close frame
// whether or not the page is looking — and five seconds bounded is the point:
// the alternative it replaces is a displaced socket that never closes at all.
func (c *conn) flushAndClose() {
	ctx, cancel := context.WithTimeout(c.ctx, c.srv.cfg.WriteTimeout)
	defer cancel()

drain:
	for {
		select {
		case ev := <-c.out:
			if !c.writeEvent(ctx, ev) {
				// The socket is already gone; there is nothing left to say to
				// it and nothing to hand a close frame to.
				return
			}
		default:
			break drain
		}
	}

	if err := c.ws.Close(websocket.StatusNormalClosure, terminalCloseReason); err != nil {
		c.log.Debug("terminal close failed", "err", err)
	}
	c.cancel()
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
			if c.terminating.Load() {
				// The room ended this session on purpose and has already said
				// why in an Error frame. "server shutting down" would be a lie.
				return websocket.StatusNormalClosure, "seat closed"
			}
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
		// DiscardUnknown so a frame padded with fields this build has no name
		// for is not retained field by field in the message's unknown-fields
		// buffer. A newer client's extra data is not something this server can
		// act on, and keeping it would let a client choose how much memory each
		// decoded command costs.
		if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, cmd); err != nil {
			c.reject("", genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, "malformed frame")
			continue
		}

		// The correlation id is checked before anything is answered with it.
		// reject echoes the cid back, so quoting an over-long one would make
		// the rejection itself the amplification — and the reply is the only
		// place it could go, because nothing here logs it.
		cid := cmd.GetCid()
		if len(cid) > MaxCidLen {
			c.reject("", genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, "correlation id too long")
			continue
		}

		// The union is validated here and nowhere else downstream: past this
		// line the room may assume the oneof is set and its payload is non-nil.
		if err := validate(cmd); err != nil {
			c.reject(cid, genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, err.Error())
			continue
		}

		if join, ok := cmd.GetCmd().(*genpb.ClientCommand_Join); ok {
			c.handleJoin(cid, join.Join)
			continue
		}

		if c.playerID == "" {
			c.reject(cid, genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, "join a room first")
			continue
		}

		// One command, one bounded reply — except this one, whose answer is the
		// whole room. It rides its own bucket on top of the generic limiter;
		// see DefaultSnapshotBurst for why it cannot share.
		if _, ok := cmd.GetCmd().(*genpb.ClientCommand_RequestSnapshot); ok && !c.snaps.allow(now) {
			c.reject(cid, genpb.ErrorCode_ERROR_CODE_RATE_LIMITED,
				"too many snapshot requests, slow down")
			continue
		}

		// PlayerID comes from the seat this socket established, never from the
		// frame. There is no field on ClientCommand that could carry identity,
		// and this is why.
		c.room.Submit(room.Command{PlayerID: c.playerID, Cmd: cmd, Out: c})
	}
}

// maxPointValues is the largest coordinate array any one command may carry:
// room.MaxPointsPerStroke pairs, interleaved x then y.
//
// The room enforces this too, and more besides — ValidPoints also rejects an
// odd length and an out-of-range coordinate. This copy is not redundant: it is
// the difference between a bound the protocol states and a bound one handler
// happens to apply, and it keeps an oversized array from being carried across
// the actor queue at all.
const maxPointValues = 2 * room.MaxPointsPerStroke

func boundPoints(name string, pts []int32) error {
	if len(pts) > maxPointValues {
		return fmt.Errorf("%s: at most %d coordinates", name, maxPointValues)
	}
	return nil
}

func boundString(name, v string, max int) error {
	if len(v) > max {
		return fmt.Errorf("%s: at most %d bytes", name, max)
	}
	return nil
}

// validate checks the command union at the socket boundary. It rejects an unset
// oneof, a set-but-empty variant, and any field longer than the protocol allows
// one to be; it does not check game rules, which belong to the room.
//
// The length checks are resource limits, not validation: a client learns that
// its name is too long, but the reason they exist is that every byte past the
// limit is a byte this process allocated, scanned and may have carried into a
// room. The values themselves are still the room's to judge — a coordinate in
// range, a palette index that exists, a name that survives sanitizing.
func validate(cmd *genpb.ClientCommand) error {
	switch v := cmd.GetCmd().(type) {
	case nil:
		return errors.New("empty command")
	case *genpb.ClientCommand_Join:
		if v.Join == nil {
			return errors.New("join: missing payload")
		}
		// Length is checked on the raw field, before sanitizeName collapses it
		// and before the room truncates it. Truncating a decoded string is not
		// a resource limit; by then it has been read and allocated.
		if err := boundString("join: display_name", v.Join.GetDisplayName(), MaxRawNameLen); err != nil {
			return err
		}
		if err := boundString("join: room_code", v.Join.GetRoomCode(), MaxRoomCodeLen); err != nil {
			return err
		}
		// The token is opaque and is never echoed, logged or parsed. Only its
		// length is this layer's business.
		if err := boundString("join: seat_token", v.Join.GetSeatToken(), MaxSeatTokenLen); err != nil {
			return err
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
		if err := boundPoints("stroke_begin", v.StrokeBegin.GetPoints()); err != nil {
			return err
		}
	case *genpb.ClientCommand_StrokePoints:
		if v.StrokePoints == nil {
			return errors.New("stroke_points: missing payload")
		}
		if err := boundPoints("stroke_points", v.StrokePoints.GetPoints()); err != nil {
			return err
		}
	case *genpb.ClientCommand_StrokeEnd:
		if v.StrokeEnd == nil {
			return errors.New("stroke_end: missing payload")
		}
		if err := boundPoints("stroke_end", v.StrokeEnd.GetPoints()); err != nil {
			return err
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
	case *genpb.ClientCommand_Kick:
		if v.Kick == nil {
			return errors.New("kick: missing payload")
		}
		if v.Kick.GetTargetPlayerId() == "" {
			return errors.New("kick: empty target")
		}
		if err := boundString("kick: target_player_id", v.Kick.GetTargetPlayerId(), MaxPlayerIDLen); err != nil {
			return err
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
		playerID, err = rm.Attach(token, c)
	} else {
		if name == "" {
			c.srv.reg.Release(code)
			c.reject(cid, genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, "a display name is required")
			return
		}
		playerID, _, err = rm.Seat(name, c)
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
	playerID, err := created.Room.Attach(created.HostToken, c)
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
