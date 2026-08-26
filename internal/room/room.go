package room

// room.go — the actor. One goroutine owns every field on Room, so there is not
// a single mutex in this package (IMPLEMENTATION_PLAN.md §4.4).
//
// Everything that mutates state runs on that goroutine: commands arrive on
// r.inbox via Submit, seat lifecycle calls arrive on r.ctl as closures, and the
// two timers land in the same select. There is no per-connection writer
// goroutine — coder/websocket's Write is concurrency-safe, so the room drains
// each client's bounded queue itself — and no per-connection ticker, only the
// one shared r.sweep.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// ---------------------------------------------------------------------------
// Player redaction
// ---------------------------------------------------------------------------

// LogValue renders a player for slog with the secret word replaced by a
// literal. Player.word has no getter and is formatted nowhere else, so this is
// what a logger sees when a *Player is handed to it as an attribute
// (IMPLEMENTATION_PLAN.md §1: a word must not reach a log line either).
func (p *Player) LogValue() slog.Value {
	if p == nil {
		return slog.StringValue("<nil>")
	}
	return slog.GroupValue(
		slog.String("id", p.ID),
		slog.Int("seat", int(p.Seat)),
		slog.Bool("connected", p.Connected),
		slog.Bool("eliminated", p.Eliminated),
		slog.Bool("host", p.IsHost),
		slog.String("word", "[redacted]"),
	)
}

// String never includes the word. Always log a *Player rather than a Player: a
// value copy printed with %+v would still expose every field.
func (p *Player) String() string {
	if p == nil {
		return "player{nil}"
	}
	return fmt.Sprintf("player{id:%s seat:%d connected:%t eliminated:%t host:%t word:[redacted]}",
		p.ID, p.Seat, p.Connected, p.Eliminated, p.IsHost)
}

// ---------------------------------------------------------------------------
// The actor loop
// ---------------------------------------------------------------------------

// run is the actor loop. See the doc comment on Run in api.go.
func (r *Room) run(ctx context.Context) {
	defer func() {
		r.disarmPhase()
		r.sweep.Stop()
		// Closing done first releases any Seat/Attach/Detach round trip that is
		// parked on r.ctl before the registry drops the code.
		close(r.done)
		if r.registry != nil {
			r.registry.RoomClosed(r.Code)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case fn := <-r.ctl:
			fn()
		case m := <-r.inbox:
			r.handle(m)
		case <-r.phaseTimer.C:
			r.onDeadline()
		case <-r.sweep.C:
			if r.checkLiveness() {
				r.log.Info("room idle, closing")
				return
			}
		}
	}
}

// do runs fn on the room goroutine and waits for it. It is how Seat, Attach and
// Detach mutate state without a second synchronisation mechanism: they are not
// another way in, they are the same select.
func (r *Room) do(fn func()) error {
	ack := make(chan struct{})
	select {
	case r.ctl <- func() { fn(); close(ack) }:
	case <-r.done:
		return ErrClosed
	}
	select {
	case <-ack:
		return nil
	case <-r.done:
		return ErrClosed
	}
}

// ---------------------------------------------------------------------------
// Command dispatch
// ---------------------------------------------------------------------------

// handle validates the envelope, then routes one client frame.
//
// Two checks happen for every command before any handler sees it: the sender
// must hold a seat in this room, and the frame must have arrived on that seat's
// current socket. The second is what makes a reconnect safe — a late frame from
// the socket a reconnect displaced is dropped rather than replayed into the
// match.
func (r *Room) handle(m Command) {
	if m.Cmd == nil {
		return
	}
	p := r.byID[m.PlayerID]
	if p == nil {
		r.log.Debug("command from unseated sender", "player", m.PlayerID)
		return
	}
	if m.Out != nil && p.outbound != m.Out {
		return
	}

	cid := m.Cmd.GetCid()
	switch c := m.Cmd.GetCmd().(type) {
	case *genpb.ClientCommand_Join:
		r.onJoin(p, cid, c.Join)
	case *genpb.ClientCommand_SetReady:
		r.onSetReady(p, cid, c.SetReady)
	case *genpb.ClientCommand_UpdateSettings:
		r.onUpdateSettings(p, cid, c.UpdateSettings)
	case *genpb.ClientCommand_StartMatch:
		r.onStartMatch(p, cid)
	case *genpb.ClientCommand_StrokeBegin:
		r.onStrokeBegin(p, cid, c.StrokeBegin)
	case *genpb.ClientCommand_StrokePoints:
		r.onStrokePoints(p, cid, c.StrokePoints)
	case *genpb.ClientCommand_StrokeEnd:
		r.onStrokeEnd(p, cid, c.StrokeEnd)
	case *genpb.ClientCommand_CastVote:
		r.onCastVote(p, cid, c.CastVote)
	case *genpb.ClientCommand_RequestSnapshot:
		r.onRequestSnapshot(p, cid)
	case *genpb.ClientCommand_Rematch:
		r.onRematch(p, cid)
	default:
		// Includes the nil case a proto3 oneof leaves when a newer client sends
		// a variant this build does not know.
		r.SendError(p.ID, cid, ErrInvalidCommand)
	}
}

// onJoin answers an in-band JoinRoom. Transport has already established the
// seat with Seat or Attach — identity is never re-derived from a frame body —
// so this is a resync: re-issue the seat and the player's private view.
func (r *Room) onJoin(p *Player, cid string, j *genpb.JoinRoom) {
	if v := j.GetProtocolVersion(); v != 0 && v != ProtocolVersion {
		r.sendErrorCode(p.ID, cid, genpb.ErrorCode_ERROR_CODE_PROTOCOL_VERSION,
			"unsupported protocol version")
		return
	}
	r.SendReply(p.ID, cid, r.joinedFor(p, true))
	r.sendSnapshot(p.ID, cid)
}

func (r *Room) onSetReady(p *Player, cid string, sr *genpb.SetReady) {
	if r.phase != genpb.Phase_PHASE_LOBBY {
		r.SendError(p.ID, cid, ErrWrongPhase)
		return
	}
	if p.Ready == sr.GetReady() {
		return
	}
	p.Ready = sr.GetReady()
	r.BroadcastReply(cid, r.lobbyState())
}

func (r *Room) onUpdateSettings(p *Player, cid string, us *genpb.UpdateSettings) {
	if r.phase != genpb.Phase_PHASE_LOBBY {
		r.SendError(p.ID, cid, ErrWrongPhase)
		return
	}
	if !p.IsHost {
		r.SendError(p.ID, cid, ErrNotHost)
		return
	}
	// Out-of-range values are clamped, not rejected: a client that omits a
	// field gets the recommended game (DESIGN.md:224).
	r.settings = ClampSettings(us.GetSettings())
	r.BroadcastReply(cid, EvSettingsChanged{&genpb.SettingsChanged{Settings: r.settings}})
	r.Broadcast(r.lobbyState())
}

func (r *Room) onRequestSnapshot(p *Player, cid string) {
	// have_seq is advisory: the answer is always the complete state, never a
	// delta (IMPLEMENTATION_PLAN.md §4.6).
	r.sendSnapshot(p.ID, cid)
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

// lobbyState renders the roster event. It reads no secret.
func (r *Room) lobbyState() EvLobbyState {
	return EvLobbyState{&genpb.LobbyState{
		RoomCode:   r.Code,
		Players:    r.PlayerInfos(),
		Settings:   r.settings,
		Phase:      r.phase,
		MinPlayers: MinPlayers,
		MaxPlayers: MaxPlayers,
		CanStart:   r.canStart(),
	}}
}

// canStart reports whether the host may send StartMatch right now: lobby phase,
// a deck to draw from, 3..10 seated players (DESIGN.md:16), every one of them
// connected and ready.
//
// Connected matters as much as ready. Detach does not clear Ready, so a seat
// that goes dark in the lobby stays flagged ready — and starting a match around
// one would open it with a player who is not there to see their own word.
func (r *Room) canStart() bool {
	if r.phase != genpb.Phase_PHASE_LOBBY || r.deck == nil {
		return false
	}
	n := len(r.players)
	if n < MinPlayers || n > MaxPlayers {
		return false
	}
	for _, p := range r.players {
		if !p.Ready || !p.Connected {
			return false
		}
	}
	return true
}

// joinedFor builds the seat grant. Unicast only: Joined carries the seat token,
// which is a bearer credential for one seat (audience.go).
func (r *Room) joinedFor(p *Player, reconnected bool) EvJoined {
	return EvJoined{&genpb.Joined{
		RoomCode:        r.Code,
		PlayerId:        p.ID,
		SeatToken:       p.SeatToken,
		GraceSeconds:    int32(GraceWindow / time.Second),
		IsHost:          p.IsHost,
		Reconnected:     reconnected,
		ProtocolVersion: ProtocolVersion,
	}}
}

// presence renders one player's connection state plus the grace countdown.
func (r *Room) presence(p *Player) EvPlayerPresence {
	return EvPlayerPresence{&genpb.PlayerPresence{
		Player:                p.Info(),
		GraceSecondsRemaining: r.graceRemaining(p),
	}}
}

// graceRemaining is whole seconds left in the reconnect window, rounded up, and
// 0 for a connected player.
func (r *Room) graceRemaining(p *Player) int32 {
	if p.Connected || p.DisconnectedAt.IsZero() {
		return 0
	}
	d := GraceWindow - time.Since(p.DisconnectedAt)
	if d <= 0 {
		return 0
	}
	return int32((d + time.Second - 1) / time.Second)
}

// sendErrorCode is SendError for the codes that have no sentinel, so a
// rejection never has to fall back to ERROR_CODE_UNSPECIFIED.
func (r *Room) sendErrorCode(playerID, cid string, code genpb.ErrorCode, msg string) {
	r.SendReply(playerID, cid, EvError{&genpb.Error{Code: code, Message: msg}})
}
