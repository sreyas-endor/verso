package room

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	mrand "math/rand/v2"
	"time"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// ---------------------------------------------------------------------------
// Constants. Everything a sibling package needs to agree with the room about.
// ---------------------------------------------------------------------------

// Roster limits (DESIGN.md:20).
const (
	MinPlayers = 3
	MaxPlayers = 10
)

// Match settings ranges and defaults (DESIGN.md:224). The room clamps to these
// and never trusts a client-supplied value.
const (
	MinRounds     = 1
	MaxRounds     = 4
	DefaultRounds = 2

	MinDrawSeconds     = 5
	MaxDrawSeconds     = 60
	DefaultDrawSeconds = 15

	MinDiscussSeconds     = 30
	MaxDiscussSeconds     = 180
	DefaultDiscussSeconds = 120

	MinIntermissionSeconds     = 3
	MaxIntermissionSeconds     = 30
	DefaultIntermissionSeconds = 10
)

// Canvas grid and stroke limits (IMPLEMENTATION_PLAN.md §4.7).
//
// The logical canvas is 1024x768 at quarter-unit precision, giving 4096x3072.
// Coordinates are signed and are NOT clipped to the grid: a stroke dragged past
// the edge must survive the round trip, so only the int16 range is enforced.
const (
	GridWidth  = 4096
	GridHeight = 3072

	CoordMin = -32768
	CoordMax = 32767

	// Palette is server-owned; a client sends an index, never a color string.
	PaletteSize = 12

	MinStrokeWidth = 1
	MaxStrokeWidth = 32

	// Per-turn caps. An append-only log with no cap is a memory-exhaustion
	// vector (IMPLEMENTATION_PLAN.md §4.7).
	MaxPointsPerTurn   = 4000
	MaxStrokesPerTurn  = 128
	MaxPointsPerStroke = 1200

	// Client-side batching interval; the room uses it only to size limits.
	StrokeBatchWindow = 50 * time.Millisecond
)

// Timing and plumbing.
const (
	// GraceWindow is how long a disconnected player keeps their seat and their
	// word (open question 2). It does not keep their place in the majority
	// denominator — see Player.Active.
	GraceWindow = 60 * time.Second

	// SweepInterval drives the one shared liveness ticker per room. There are
	// deliberately no per-connection timers (IMPLEMENTATION_PLAN.md §4.4).
	SweepInterval = 1 * time.Second

	// AssignDuration is how long PHASE_ASSIGNING holds while each player reads
	// their own word (DESIGN.md:29).
	AssignDuration = 6 * time.Second

	// ResolveDuration is how long PHASE_RESOLVING holds on the vote-result
	// screen before the next round or the final reveal.
	ResolveDuration = 8 * time.Second

	// IdleTTL is how long a room with no connected players survives before the
	// registry may GC it.
	IdleTTL = 10 * time.Minute

	// InboxDepth bounds the room's command queue. Submit drops when it is full.
	InboxDepth = 256

	// OutboundQueueDepth bounds one client's send queue. The room goroutine
	// writes into it directly — there is no per-connection writer goroutine.
	// Full queue means a slow client: drop or kick, never block the room.
	OutboundQueueDepth = 64

	// MaxDisplayNameLen truncates a submitted display name.
	MaxDisplayNameLen = 24

	// ProtocolVersion is echoed in Joined. Bump on any incompatible change to
	// proto/verso/v1/game.proto.
	ProtocolVersion = 1
)

// ---------------------------------------------------------------------------
// Sentinel errors. Transport maps these to an ErrorCode with ErrorCodeFor.
// ---------------------------------------------------------------------------

var (
	// ErrRoomFull means the room already seats MaxPlayers.
	ErrRoomFull = errors.New("room: full")
	// ErrMatchInProgress means a new player cannot take a seat right now.
	ErrMatchInProgress = errors.New("room: match in progress")
	// ErrBadSeat means the seat token is unknown, expired, or already live on
	// another connection.
	ErrBadSeat = errors.New("room: bad seat token")
	// ErrNotHost means a host-only command came from a non-host.
	ErrNotHost = errors.New("room: not the host")
	// ErrNotArtist means a stroke command came from someone who is not the
	// current artist.
	ErrNotArtist = errors.New("room: not the current artist")
	// ErrAlreadyVoted means the player already cast their one irreversible vote
	// this round (DESIGN.md:51).
	ErrAlreadyVoted = errors.New("room: already voted")
	// ErrNotEnoughPlayers means fewer than MinPlayers are seated.
	ErrNotEnoughPlayers = errors.New("room: not enough players")
	// ErrWrongPhase means the command is not legal in the current phase.
	ErrWrongPhase = errors.New("room: wrong phase")
	// ErrNotActive means the player is eliminated or unseated.
	ErrNotActive = errors.New("room: player not active")
	// ErrInvalidCommand means the frame failed validation at the room boundary.
	ErrInvalidCommand = errors.New("room: invalid command")
	// ErrClosed means the room goroutine has stopped.
	ErrClosed = errors.New("room: closed")
)

// ErrorCodeFor maps a sentinel to the wire enum. Unknown errors become
// ERROR_CODE_UNSPECIFIED so an internal failure never leaks its message shape
// into a machine-readable field.
func ErrorCodeFor(err error) genpb.ErrorCode {
	switch {
	case err == nil:
		return genpb.ErrorCode_ERROR_CODE_UNSPECIFIED
	case errors.Is(err, ErrRoomFull):
		return genpb.ErrorCode_ERROR_CODE_ROOM_FULL
	case errors.Is(err, ErrMatchInProgress):
		return genpb.ErrorCode_ERROR_CODE_MATCH_IN_PROGRESS
	case errors.Is(err, ErrBadSeat):
		return genpb.ErrorCode_ERROR_CODE_BAD_SEAT
	case errors.Is(err, ErrNotHost):
		return genpb.ErrorCode_ERROR_CODE_NOT_HOST
	case errors.Is(err, ErrNotArtist):
		return genpb.ErrorCode_ERROR_CODE_NOT_ARTIST
	case errors.Is(err, ErrAlreadyVoted):
		return genpb.ErrorCode_ERROR_CODE_ALREADY_VOTED
	case errors.Is(err, ErrNotEnoughPlayers):
		return genpb.ErrorCode_ERROR_CODE_NOT_ENOUGH_PLAYERS
	case errors.Is(err, ErrWrongPhase):
		return genpb.ErrorCode_ERROR_CODE_WRONG_PHASE
	case errors.Is(err, ErrNotActive):
		return genpb.ErrorCode_ERROR_CODE_NOT_ACTIVE
	case errors.Is(err, ErrInvalidCommand):
		return genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND
	default:
		return genpb.ErrorCode_ERROR_CODE_UNSPECIFIED
	}
}

// ---------------------------------------------------------------------------
// Collaborator interfaces
// ---------------------------------------------------------------------------

// Deck supplies word pairs. Implemented by internal/words.
type Deck interface {
	// Pair returns two distinct, related words from the requested difficulty
	// deck. The caller — not the deck — decides which side becomes the common
	// word (DESIGN.md:33), so an implementation must not bias the ordering.
	//
	// rnd is the room's own generator, so a seeded room replays exactly.
	// Implementations must draw all randomness from it and must be safe to call
	// from the room goroutine only.
	//
	// An unrecognised difficulty falls back to DIFFICULTY_MEDIUM.
	Pair(difficulty genpb.Difficulty, rnd *mrand.Rand) (a, b string)
}

// Registry is the small callback surface the room needs from its owner.
// Implemented by internal/registry.
type Registry interface {
	// RoomClosed is called exactly once, from the room goroutine, as Run
	// returns. The registry drops the code so it can be reused.
	RoomClosed(code string)
}

// ---------------------------------------------------------------------------
// Command plumbing
// ---------------------------------------------------------------------------

// Command is one authenticated client frame on its way to the room actor.
//
// Submit is the only path from transport into a room. PlayerID is filled in by
// transport from the seat it already established with Seat or Attach; the room
// never re-derives identity from the frame body.
type Command struct {
	// PlayerID identifies the sender. Never empty: a connection that has not
	// been seated has nothing to submit.
	PlayerID string

	// Cmd is the decoded frame. Never nil.
	Cmd *genpb.ClientCommand

	// Out is the connection that sent the frame. The room compares it against
	// the player's current queue and ignores frames from a socket that has
	// already been replaced by a reconnect.
	Out chan<- *genpb.ServerEvent
}

// ---------------------------------------------------------------------------
// Player
// ---------------------------------------------------------------------------

// Player is one seat. Everything here is owned by the room goroutine; no field
// may be read or written from any other goroutine.
type Player struct {
	// ID is the stable, public player id. Safe to put on the wire.
	ID string

	// Name is the display name, already truncated to MaxDisplayNameLen.
	Name string

	// SeatToken is the bearer credential for reclaiming this seat. It is an
	// opaque, room-local random string: transport must pass it through
	// verbatim and must never parse, derive from, log, or broadcast it.
	SeatToken string

	// Seat is the join-order index. Stable for the life of the room.
	Seat int32

	// word is this player's secret. It is unexported so no other package can
	// read it, and within this package exactly one function reads it:
	// Room.viewFor. Do not add a getter. Do not log it. Do not copy it into any
	// type in audience.go other than through viewFor.
	word string

	// Connected is false while the socket is gone but the seat is held. A
	// disconnected player keeps their word and their seat, but not their place
	// in the majority denominator (DESIGN.md, "Active players").
	Connected bool

	// Eliminated players are silent spectators (DESIGN.md:66).
	Eliminated bool

	// IsHost marks the single host. On host loss the longest-connected active
	// player is promoted (open question 4).
	IsHost bool

	// Ready is the lobby readiness flag. Meaningless outside PHASE_LOBBY.
	Ready bool

	// JoinedAt is when the seat was first taken. It is the host-migration
	// ordering key: earliest JoinedAt among connected, non-eliminated players.
	JoinedAt time.Time

	// DisconnectedAt is when the current grace window started; zero when
	// connected.
	DisconnectedAt time.Time

	// outbound is the bounded queue for this player's live socket, or nil while
	// disconnected. The room goroutine writes into it with a non-blocking send.
	outbound chan<- *genpb.ServerEvent
}

// Active reports whether the player counts as an active player for turn order,
// the majority denominator and the win conditions.
//
// A player is active only while connected and not eliminated (DESIGN.md,
// "Active players"). A disconnected seat is NOT active: it drops out of the
// strict-majority denominator, out of the turn order and out of the
// two-players-remain count for as long as it stays dark, and comes back the
// moment the socket does.
//
// That is the whole point. A disconnected player cannot vote, so keeping them
// in the denominator turns one dropped laptop into a permanent block on ever
// reaching a majority — the round can no longer be decided by the people who
// are actually still playing.
//
// The seat, the word and the match state are retained either way; this governs
// participation, not tenancy.
func (p *Player) Active() bool { return p != nil && p.Connected && !p.Eliminated }

// Info renders the public roster entry. It reads no secret.
func (p *Player) Info() *genpb.PlayerInfo {
	return &genpb.PlayerInfo{
		Id:         p.ID,
		Name:       p.Name,
		Seat:       p.Seat,
		Connected:  p.Connected,
		Ready:      p.Ready,
		IsHost:     p.IsHost,
		Eliminated: p.Eliminated,
	}
}

// ---------------------------------------------------------------------------
// Room
// ---------------------------------------------------------------------------

// openStroke is the single in-flight stroke of the current artist. There is at
// most one per room, which is why the client never sends a stroke id.
type openStroke struct {
	id         int32
	colorIndex int32
	width      int32
	points     []int32
}

// Options configures a new room.
type Options struct {
	// Deck is required for a match to start.
	Deck Deck

	// Rand seeds every shuffle, word draw and side choice. Nil means a fresh
	// non-deterministic generator. Tests pass a seeded one for reproducibility.
	Rand *mrand.Rand

	// Settings overrides the lobby defaults. Nil means DefaultSettings. The
	// value is clamped either way.
	Settings *genpb.MatchSettings

	// Registry receives the RoomClosed callback. May be nil.
	Registry Registry

	// Logger is used for room lifecycle only. It must NEVER be handed a word:
	// there is no code path that logs Player.word, and adding one defeats
	// IMPLEMENTATION_PLAN.md §1. Nil means slog.Default.
	Logger *slog.Logger
}

// Room is one match. A single goroutine — Run — owns every field below, so
// there are no mutexes anywhere in this package.
//
// A room is drivable with no network at all: construct it, call Seat once per
// player with a plain channel, start Run, and push Commands with Submit. That
// is how the whole match is tested under testing/synctest (milestone 5).
type Room struct {
	// Code is the short join code. Immutable after New.
	Code string

	// inbox carries client commands. Bounded; Submit drops when full.
	inbox chan Command

	// ctl carries closures that must run on the room goroutine. Seat, Attach
	// and Detach use it to make a synchronous round trip without a second lock,
	// so they are not a separate way in — they land in the same select as
	// everything else. Run must service it.
	ctl chan func()

	// done closes when Run returns, so a blocked ctl round trip can bail out.
	done chan struct{}

	// phaseTimer fires when the current phase or turn expires. Always armed
	// through armPhase and disarmed through disarmPhase.
	phaseTimer *time.Timer

	// sweep is the single shared liveness ticker (IMPLEMENTATION_PLAN.md §4.4).
	sweep *time.Ticker

	// players is seat order; byID and bySeatToken index it.
	players     []*Player
	byID        map[string]*Player
	bySeatToken map[string]*Player

	// settings is the clamped, host-configured match configuration.
	settings *genpb.MatchSettings

	// phase is the authoritative phase (IMPLEMENTATION_PLAN.md §4.5).
	phase genpb.Phase

	// phaseDeadline is when phaseTimer will fire; zero when untimed.
	phaseDeadline time.Time

	// round is 1-based; 0 before the first round.
	round int32

	// turnOrder holds the active player ids for this round, reshuffled at the
	// start of every round (DESIGN.md:36). turnIndex points at the live turn.
	turnOrder []string
	turnIndex int

	// artistID is the current artist, or "" outside PHASE_DRAWING.
	artistID string
	// nextArtistID is announced during PHASE_INTERMISSION. An empty value means
	// voting opens when the handoff ends.
	nextArtistID string

	// strokes is the append-only canvas log. Never truncated mid-match, and
	// entries carry no artist id (DESIGN.md:40).
	strokes []*genpb.Stroke

	// open is the artist's in-flight stroke, or nil.
	open *openStroke

	// seq is the monotonic per-room stroke-event counter. A client that sees a
	// gap sends RequestSnapshot.
	seq int32

	// nextStrokeID assigns Stroke.stroke_id.
	nextStrokeID int32

	// pointsThisTurn and strokesThisTurn enforce the per-turn caps.
	pointsThisTurn  int
	strokesThisTurn int

	// votes maps voter id to candidate id, "" meaning Skip. THIS MAP NEVER GOES
	// ON THE WIRE and is never logged: DESIGN.md:56 publishes aggregates only.
	// Cleared at the start of every voting window.
	votes map[string]string

	// commonWord, imposterWord and imposterID are the dealt assignment. They are
	// broadcast exactly once, in MatchEnded (DESIGN.md:75).
	commonWord   string
	imposterWord string
	imposterID   string

	// endReason and winner are set when the match resolves.
	winner    genpb.WinnerSide
	endReason genpb.MatchEndReason

	// nextSeat is the seat counter for new players.
	nextSeat int32

	deck     Deck
	rnd      *mrand.Rand
	registry Registry
	log      *slog.Logger
}

// New creates a room with its host already seated but not yet connected.
//
// The caller — the registry — hands the host's seat token to whoever created
// the room; that client then presents it to Attach when its socket opens. Every
// other player takes a seat with Seat.
//
// New does not start the actor. Call Run.
func New(code string, hostName string, opts Options) *Room {
	rnd := opts.Rand
	if rnd == nil {
		rnd = mrand.New(mrand.NewPCG(mrand.Uint64(), mrand.Uint64()))
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	settings := ClampSettings(opts.Settings)

	r := &Room{
		Code:         code,
		inbox:        make(chan Command, InboxDepth),
		ctl:          make(chan func()),
		done:         make(chan struct{}),
		phaseTimer:   time.NewTimer(time.Hour),
		sweep:        time.NewTicker(SweepInterval),
		byID:         map[string]*Player{},
		bySeatToken:  map[string]*Player{},
		settings:     settings,
		phase:        genpb.Phase_PHASE_LOBBY,
		votes:        map[string]string{},
		nextStrokeID: 1,
		deck:         opts.Deck,
		rnd:          rnd,
		registry:     opts.Registry,
		log:          logger.With("room", code),
	}
	r.phaseTimer.Stop()

	host := &Player{
		ID:        newID(),
		Name:      truncateName(hostName),
		SeatToken: newToken(),
		Seat:      r.nextSeat,
		IsHost:    true,
		JoinedAt:  time.Now(),
	}
	r.nextSeat++
	r.players = append(r.players, host)
	r.byID[host.ID] = host
	r.bySeatToken[host.SeatToken] = host

	return r
}

// HostSeat returns the host's player id and seat token. Safe to call before Run
// starts; both values are immutable once New returns.
func (r *Room) HostSeat() (playerID, seatToken string) {
	h := r.players[0]
	return h.ID, h.SeatToken
}

// Run is the actor loop. It owns every field on Room until it returns.
//
//	for {
//	    select {
//	    case <-ctx.Done():     return
//	    case fn := <-r.ctl:    fn()
//	    case m := <-r.inbox:   r.handle(m)
//	    case <-r.phaseTimer.C: r.onDeadline()
//	    case <-r.sweep.C:      r.checkLiveness()
//	    }
//	}
//
// On return it must close r.done, stop r.sweep and r.phaseTimer, and call
// Registry.RoomClosed exactly once.
// Implemented in room.go.
func (r *Room) Run(ctx context.Context) { r.run(ctx) }

// Submit hands one command to the actor. It is the only way into a room from
// transport, and it never blocks: a full inbox means the room is saturated, and
// dropping is correct because a client recovers with RequestSnapshot. Callable
// from any goroutine.
func (r *Room) Submit(cmd Command) {
	select {
	case r.inbox <- cmd:
	case <-r.done:
	default:
		r.log.Warn("inbox full, command dropped", "player", cmd.PlayerID)
	}
}

// Seat takes a new seat for a first-time player and returns its identity and
// bearer token. It runs on the room goroutine via r.ctl.
//
// Returns ErrRoomFull past MaxPlayers, ErrMatchInProgress outside PHASE_LOBBY,
// and ErrClosed once Run has returned.
//
// On success the room sends EvJoined followed by EvLobbyState on out.
// Implemented in reconnect.go.
func (r *Room) Seat(displayName string, out chan<- *genpb.ServerEvent) (playerID, seatToken string, err error) {
	return r.seat(displayName, out)
}

// Attach binds a live connection to an existing seat, for the host's first
// socket and for every reconnect inside the grace window. It runs on the room
// goroutine via r.ctl.
//
// Returns ErrBadSeat for an unknown or expired token, and ErrClosed once Run
// has returned. A token that is already live on another socket is honoured: the
// old connection is displaced, because that is what a flaky network looks like.
//
// On success the room sends EvJoined, then the player's EvSnapshot built by
// viewFor, and broadcasts EvPlayerPresence.
// Implemented in reconnect.go.
func (r *Room) Attach(seatToken string, out chan<- *genpb.ServerEvent) (playerID string, err error) {
	return r.attach(seatToken, out)
}

// Detach releases a connection. out is compared against the player's current
// queue and the call is ignored if it does not match, so a late detach from a
// socket that has already been replaced cannot knock the new one offline.
//
// The seat survives for GraceWindow. If the detaching player is the imposter and
// the window expires mid-match, the match ends with a group win
// (DESIGN.md:125); if they are the host, the longest-connected active player is
// promoted. It runs on the room goroutine via r.ctl.
// Implemented in reconnect.go.
func (r *Room) Detach(playerID string, out chan<- *genpb.ServerEvent) { r.detach(playerID, out) }

// Broadcast delivers e to every connected socket in the room.
//
// The Broadcastable argument type is the compile-time half of the secret-leak
// defense: EvYourWord, EvSnapshot, EvJoined, EvSpectatorInfo and EvVoteAccepted
// have no broadcastSafe method and cannot be passed here (audience.go).
// Call only from the room goroutine.
func (r *Room) Broadcast(e Broadcastable) {
	env := e.Envelope("")
	for _, p := range r.players {
		r.deliver(p, env)
	}
}

// BroadcastReply is Broadcast with a correlation id, for a room-wide event that
// answers one player's command.
func (r *Room) BroadcastReply(cid string, e Broadcastable) {
	env := e.Envelope(cid)
	for _, p := range r.players {
		r.deliver(p, env)
	}
}

// SendTo delivers e to one player. This is the only way a unicast-only event
// reaches a socket. Call only from the room goroutine.
func (r *Room) SendTo(playerID string, e Event) {
	r.SendReply(playerID, "", e)
}

// SendReply is SendTo with a correlation id echoed from the command that caused
// the event.
func (r *Room) SendReply(playerID, cid string, e Event) {
	p := r.byID[playerID]
	if p == nil {
		return
	}
	r.deliver(p, e.Envelope(cid))
}

// SendError is the common rejection path: a unicast Error carrying the code for
// err, correlated to the offending command.
func (r *Room) SendError(playerID, cid string, err error) {
	r.SendReply(playerID, cid, EvError{&genpb.Error{
		Code:    ErrorCodeFor(err),
		Message: err.Error(),
	}})
}

// deliver does the one non-blocking send. A full queue is a slow client; the
// frame is dropped and the client recovers with RequestSnapshot. The room must
// never block on a socket.
func (r *Room) deliver(p *Player, env *genpb.ServerEvent) {
	if p.outbound == nil {
		return
	}
	select {
	case p.outbound <- env:
	default:
		r.log.Warn("outbound queue full, frame dropped", "player", p.ID)
	}
}

// ---------------------------------------------------------------------------
// viewFor — the ONLY place a secret word is read
// ---------------------------------------------------------------------------

// viewFor builds the private state of exactly one player.
//
// THIS IS THE ONLY FUNCTION IN THE SERVER THAT READS Player.word, and the
// Snapshot it returns is unicast-only: EvSnapshot has no broadcastSafe marker,
// so the result cannot reach Broadcast (IMPLEMENTATION_PLAN.md §1, defense 2).
//
// There is no exception. The final reveal in MatchEnded publishes every word on
// purpose once the match is over (DESIGN.md:75), and it too comes through here
// — buildReveals calls viewFor once per player rather than reading the field.
// If a second reader of Player.word ever appears, the structural defense is
// gone.
//
// Returns nil for an unknown player id.
func (r *Room) viewFor(playerID string) *genpb.Snapshot {
	p := r.byID[playerID]
	if p == nil {
		return nil
	}

	strokes := make([]*genpb.Stroke, 0, len(r.strokes)+1)
	strokes = append(strokes, r.strokes...)
	if r.open != nil {
		strokes = append(strokes, &genpb.Stroke{
			StrokeId:   r.open.id,
			ColorIndex: r.open.colorIndex,
			Width:      r.open.width,
			Points:     append([]int32(nil), r.open.points...),
		})
	}

	turnOrder := append([]string(nil), r.turnOrder...)
	_, voted := r.votes[playerID]

	// The one read. Nothing below this line may be copied into a broadcast.
	word := p.word

	return &genpb.Snapshot{
		RoomCode:         r.Code,
		PlayerId:         p.ID,
		Phase:            r.phase,
		Round:            r.round,
		TotalRounds:      r.settings.GetMaxRounds(),
		Settings:         r.settings,
		Players:          r.PlayerInfos(),
		TurnOrder:        turnOrder,
		TurnIndex:        int32(r.turnIndex),
		ArtistId:         r.artistID,
		NextArtistId:     r.nextArtistID,
		RemainingMs:      r.RemainingMS(),
		Strokes:          strokes,
		Seq:              r.seq,
		YourWord:         word,
		YouAreEliminated: p.Eliminated,
		YouHaveVoted:     voted,
		VotesCast:        int32(r.votesFromActive()),
		ActiveCount:      int32(r.ActiveCount()),
	}
}

// ---------------------------------------------------------------------------
// Shared read helpers. Implemented here so the phase, vote and stroke code all
// agree on the denominator and the clock.
// ---------------------------------------------------------------------------

// Player returns the seated player, or nil.
func (r *Room) Player(id string) *Player { return r.byID[id] }

// Players returns the seat-ordered roster. Do not mutate the slice.
func (r *Room) Players() []*Player { return r.players }

// PlayerInfos renders the public roster.
func (r *Room) PlayerInfos() []*genpb.PlayerInfo {
	out := make([]*genpb.PlayerInfo, 0, len(r.players))
	for _, p := range r.players {
		out = append(out, p.Info())
	}
	return out
}

// ActivePlayers returns every connected, non-eliminated player in seat order.
// A dark seat is absent from this list until its socket returns.
func (r *Room) ActivePlayers() []*Player {
	out := make([]*Player, 0, len(r.players))
	for _, p := range r.players {
		if p.Active() {
			out = append(out, p)
		}
	}
	return out
}

// ActiveCount is the majority denominator (DESIGN.md:58), evaluated fresh at
// every tally rather than fixed when the round opened — connections come and
// go mid-window.
func (r *Room) ActiveCount() int {
	n := 0
	for _, p := range r.players {
		if p.Active() {
			n++
		}
	}
	return n
}

// Settings returns the clamped match settings. Do not mutate the result.
func (r *Room) Settings() *genpb.MatchSettings { return r.settings }

// Phase returns the current phase.
func (r *Room) Phase() genpb.Phase { return r.phase }

// Round returns the 1-based round number, or 0 before the first round.
func (r *Room) Round() int32 { return r.round }

// Host returns the current host, or nil if the room is empty.
func (r *Room) Host() *Player {
	for _, p := range r.players {
		if p.IsHost {
			return p
		}
	}
	return nil
}

// RemainingMS is the milliseconds left on the current phase or turn, clamped at
// zero, and 0 when nothing is armed. Durations on the wire are always relative:
// there is no absolute timestamp anywhere in game.proto, because an epoch
// millisecond count does not fit in an int32 and int64 becomes bigint in TS.
func (r *Room) RemainingMS() int32 {
	if r.phaseDeadline.IsZero() {
		return 0
	}
	d := time.Until(r.phaseDeadline)
	if d <= 0 {
		return 0
	}
	return int32(d / time.Millisecond)
}

// armPhase restarts the phase timer for d and records the deadline. Use this
// rather than touching phaseTimer, so RemainingMS never drifts from the timer.
func (r *Room) armPhase(d time.Duration) {
	r.phaseTimer.Stop()
	r.phaseDeadline = time.Now().Add(d)
	r.phaseTimer.Reset(d)
}

// disarmPhase stops the phase timer and clears the deadline.
func (r *Room) disarmPhase() {
	r.phaseTimer.Stop()
	r.phaseDeadline = time.Time{}
}

// ---------------------------------------------------------------------------
// Settings validation
// ---------------------------------------------------------------------------

// DefaultSettings is the recommended configuration (DESIGN.md:224).
func DefaultSettings() *genpb.MatchSettings {
	return &genpb.MatchSettings{
		Difficulty:          genpb.Difficulty_DIFFICULTY_MEDIUM,
		MaxRounds:           DefaultRounds,
		DrawSeconds:         DefaultDrawSeconds,
		DiscussSeconds:      DefaultDiscussSeconds,
		IntermissionSeconds: DefaultIntermissionSeconds,
	}
}

// ClampSettings returns a copy of s with every field forced into its documented
// range. A zero or unknown value becomes the default rather than an error: a
// client that omits a field gets the recommended game, not a rejection. The
// clamped result is what SettingsChanged echoes back.
func ClampSettings(s *genpb.MatchSettings) *genpb.MatchSettings {
	out := DefaultSettings()
	if s == nil {
		return out
	}
	switch s.GetDifficulty() {
	case genpb.Difficulty_DIFFICULTY_EASY,
		genpb.Difficulty_DIFFICULTY_MEDIUM,
		genpb.Difficulty_DIFFICULTY_HARD:
		out.Difficulty = s.GetDifficulty()
	}
	if v := s.GetMaxRounds(); v != 0 {
		out.MaxRounds = clamp32(v, MinRounds, MaxRounds)
	}
	if v := s.GetDrawSeconds(); v != 0 {
		out.DrawSeconds = clamp32(v, MinDrawSeconds, MaxDrawSeconds)
	}
	if v := s.GetDiscussSeconds(); v != 0 {
		out.DiscussSeconds = clamp32(v, MinDiscussSeconds, MaxDiscussSeconds)
	}
	if v := s.GetIntermissionSeconds(); v != 0 {
		out.IntermissionSeconds = clamp32(v, MinIntermissionSeconds, MaxIntermissionSeconds)
	}
	return out
}

// ClampWidth forces a client-supplied brush width into range, so one client
// cannot lag every other with an absurdly thick line.
func ClampWidth(w int32) int32 { return clamp32(w, MinStrokeWidth, MaxStrokeWidth) }

// ValidColorIndex reports whether i addresses the server-owned palette.
// Out-of-range indices are rejected, not clamped: a wrong colour is a bug worth
// surfacing, and clamping would silently repaint the canvas.
func ValidColorIndex(i int32) bool { return i >= 0 && i < PaletteSize }

// ValidPoints reports whether a flat interleaved coordinate slice is usable:
// an even, non-empty length within the per-message cap, every value inside the
// signed int16 range. Coordinates outside the 4096x3072 grid are legal — a
// stroke dragged past the edge must not be chopped off.
func ValidPoints(pts []int32) bool {
	if len(pts) == 0 || len(pts)%2 != 0 || len(pts) > 2*MaxPointsPerStroke {
		return false
	}
	for _, v := range pts {
		if v < CoordMin || v > CoordMax {
			return false
		}
	}
	return true
}

func clamp32(v, lo, hi int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func truncateName(name string) string {
	runes := []rune(name)
	if len(runes) > MaxDisplayNameLen {
		return string(runes[:MaxDisplayNameLen])
	}
	return string(runes)
}

// newID mints a public player id.
func newID() string { return randomHex(8) }

// newToken mints a seat token. 256 bits of crypto randomness, opaque to
// everyone but this room: for an in-memory room a token that cannot be guessed
// is exactly as strong as a signed one, and has nothing to misvalidate.
func newToken() string { return randomHex(32) }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("room: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
