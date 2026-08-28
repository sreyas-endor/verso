// Package registry owns the set of live rooms: it mints join codes, starts and
// stops room actors, and garbage-collects abandoned matches.
//
// This is the one place in the server where a mutex is correct. Everything
// inside a room is owned by that room's goroutine (IMPLEMENTATION_PLAN.md §4.4),
// but the map from join code to room is touched by every accepting HTTP
// goroutine, so it is guarded here — outside the actor.
package registry

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	mrand "math/rand/v2"
	"strings"
	"sync"
	"time"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/room"
)

// Alphabet is the join-code character set. O/0 and I/1/L are absent: a code is
// read aloud across a room or typed off a phone screen, and those are the pairs
// people get wrong.
const Alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// CodeLen is the join-code length. 31^5 is about 28.6 million codes, so with a
// few hundred live rooms a collision is rare and a guess is hopeless.
const CodeLen = 5

// Defaults for Config. Every one of these is a memory bound, not a preference.
const (
	// DefaultMaxRooms caps live rooms so an abusive client cannot exhaust
	// memory by opening sockets and creating rooms in a loop.
	DefaultMaxRooms = 512

	// DefaultEmptyGrace is how long a room with no live socket survives. It
	// must comfortably exceed room.GraceWindow, or a player reconnecting at
	// second 59 would find their room already collected.
	DefaultEmptyGrace = 3 * time.Minute

	// DefaultHardTTL bounds the lifetime of any room, however busy. A party
	// game left open overnight is a leak, not a session.
	DefaultHardTTL = 6 * time.Hour

	// DefaultSweepInterval is how often the collector runs. One goroutine for
	// the whole process, not one timer per room.
	DefaultSweepInterval = 30 * time.Second

	// maxCodeAttempts bounds the collision retry loop so a nearly-full code
	// space fails fast instead of spinning.
	maxCodeAttempts = 12
)

var (
	// ErrTooManyRooms means the live-room cap is reached.
	ErrTooManyRooms = errors.New("registry: too many rooms")
	// ErrCodeExhausted means no free join code was found in maxCodeAttempts.
	ErrCodeExhausted = errors.New("registry: could not allocate a room code")
	// ErrClosed means the registry is shutting down.
	ErrClosed = errors.New("registry: closed")
)

// Config configures a Registry. Only NewDeck is required.
type Config struct {
	// NewDeck builds the deck for one room. It is a factory, not a value,
	// because internal/words draws without replacement: a deck per room keeps
	// one match's draw history from shortening another's, and keeps a room with
	// a seeded generator replaying exactly (words.Deck doc comment).
	NewDeck func() room.Deck

	// Settings is the starting configuration for a new room's lobby. Nil means
	// room.DefaultSettings. The room clamps it either way.
	Settings *genpb.MatchSettings

	// Logger is used for room lifecycle. Nil means slog.Default. It is never
	// handed a word or a seat token.
	Logger *slog.Logger

	// NewRand, when set, supplies each room's generator. Tests use it to make a
	// match replay exactly. Nil means every room gets its own seeded generator.
	NewRand func() *mrand.Rand

	MaxRooms      int
	EmptyGrace    time.Duration
	HardTTL       time.Duration
	SweepInterval time.Duration
}

func (c Config) withDefaults() Config {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.MaxRooms <= 0 {
		c.MaxRooms = DefaultMaxRooms
	}
	if c.EmptyGrace <= 0 {
		c.EmptyGrace = DefaultEmptyGrace
	}
	if c.EmptyGrace < room.GraceWindow+30*time.Second {
		// Collecting a room out from under a reconnecting player is the exact
		// bug the grace window exists to prevent.
		c.EmptyGrace = room.GraceWindow + 30*time.Second
	}
	if c.HardTTL <= 0 {
		c.HardTTL = DefaultHardTTL
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = DefaultSweepInterval
	}
	return c
}

// entry is one live room plus the bookkeeping the collector needs. Every field
// is guarded by Registry.mu.
type entry struct {
	room   *room.Room
	code   string
	cancel context.CancelFunc

	createdAt time.Time

	// holds counts live sockets bound to this room. The registry cannot read
	// Player.Connected — that field belongs to the room goroutine — so socket
	// occupancy is tracked here, by the transport that owns the sockets.
	holds int

	// emptySince is when holds last fell to zero; zero while occupied.
	emptySince time.Time

	// closing is set once the room's context has been cancelled. A closing room
	// is invisible to Lookup but keeps its code reserved until its actor exits,
	// so a new room can never inherit a dying room's code.
	closing bool
}

// Registry is the process-wide room table.
type Registry struct {
	cfg Config
	log *slog.Logger

	base   context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	rooms map[string]*entry
	shut  bool

	wg sync.WaitGroup
}

// Created is the result of Create: a started room and the host's credentials.
//
// HostToken is the host's seat token. It is opaque and room-local: pass it
// straight to Room.Attach, never parse it, never log it (room/api.go).
type Created struct {
	Room       *room.Room
	Code       string
	HostPlayer string
	HostToken  string
}

// New builds a Registry and starts its single collector goroutine. Cancelling
// ctx, or calling Close, stops every room.
func New(ctx context.Context, cfg Config) *Registry {
	cfg = cfg.withDefaults()
	base, cancel := context.WithCancel(ctx)

	g := &Registry{
		cfg:    cfg,
		log:    cfg.Logger,
		base:   base,
		cancel: cancel,
		rooms:  make(map[string]*entry),
	}

	g.wg.Add(1)
	go g.collect()

	return g
}

// Create mints a code, starts a room actor, and returns the host's seat.
//
// The room is already occupied for accounting purposes when Create returns: the
// caller holds one reference and must Release it, exactly as if it had called
// Hold. That keeps the collector from reaping a room in the window between
// creating it and attaching the host's socket.
func (g *Registry) Create(hostName string, avatar genpb.Avatar) (Created, error) {
	g.mu.Lock()
	if g.shut {
		g.mu.Unlock()
		return Created{}, ErrClosed
	}
	if len(g.rooms) >= g.cfg.MaxRooms {
		g.mu.Unlock()
		return Created{}, ErrTooManyRooms
	}

	code, err := g.allocCodeLocked()
	if err != nil {
		g.mu.Unlock()
		return Created{}, err
	}

	var rnd *mrand.Rand
	if g.cfg.NewRand != nil {
		rnd = g.cfg.NewRand()
	}

	var deck room.Deck
	if g.cfg.NewDeck != nil {
		deck = g.cfg.NewDeck()
	}

	rm := room.New(code, hostName, avatar, room.Options{
		Deck:     deck,
		Rand:     rnd,
		Settings: g.cfg.Settings,
		Registry: g,
		Logger:   g.log,
	})

	ctx, cancel := context.WithCancel(g.base)
	e := &entry{
		room:      rm,
		code:      code,
		cancel:    cancel,
		createdAt: time.Now(),
		holds:     1,
	}
	g.rooms[code] = e
	// Taken under the same lock that publishes g.shut, so Close cannot observe a
	// zero WaitGroup and report success while this actor is still starting up.
	g.wg.Add(1)
	g.mu.Unlock()

	playerID, token := rm.HostSeat()

	go func() {
		defer g.wg.Done()
		defer cancel()
		rm.Run(ctx)
		// Removal is keyed on identity, not on the code string: a room that
		// outlives its cancellation must never delete an entry that some later
		// room has since taken over.
		g.remove(e)
	}()

	g.log.Info("room created", "room", code, "host", playerID)

	return Created{Room: rm, Code: code, HostPlayer: playerID, HostToken: token}, nil
}

// Lookup returns the live room for a code. A room whose context has already
// been cancelled is not live and is not returned.
func (g *Registry) Lookup(code string) (*room.Room, bool) {
	code = NormalizeCode(code)
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.rooms[code]
	if e == nil || e.closing {
		return nil, false
	}
	return e.room, true
}

// Hold registers one live socket against a room and returns false if the room
// is gone. Every successful Hold must be matched by exactly one Release.
func (g *Registry) Hold(code string) bool {
	code = NormalizeCode(code)
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.rooms[code]
	if e == nil || e.closing {
		return false
	}
	e.holds++
	e.emptySince = time.Time{}
	return true
}

// Release drops one socket's reference. When the last one goes the empty-grace
// clock starts; the room is not collected until it expires, so the disconnected
// players inside still have their full reconnect window.
func (g *Registry) Release(code string) {
	code = NormalizeCode(code)
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.rooms[code]
	if e == nil {
		return
	}
	if e.holds > 0 {
		e.holds--
	}
	if e.holds == 0 && e.emptySince.IsZero() {
		e.emptySince = time.Now()
	}
}

// RoomClosed implements room.Registry. The room actor calls it as Run returns.
//
// It only marks the entry; the actual removal happens in the goroutine that ran
// the actor, which can compare identities. Doing the delete here on a bare code
// string would let a slow-exiting room evict its own replacement.
func (g *Registry) RoomClosed(code string) {
	code = NormalizeCode(code)
	g.mu.Lock()
	if e := g.rooms[code]; e != nil {
		e.closing = true
	}
	g.mu.Unlock()
	g.log.Info("room closed", "room", code)
}

// Count reports the number of rooms still in the table, closing ones included.
func (g *Registry) Count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.rooms)
}

// Close cancels every room and waits for the actors to exit, or until ctx
// expires. It is safe to call more than once.
func (g *Registry) Close(ctx context.Context) error {
	g.mu.Lock()
	g.shut = true
	for _, e := range g.rooms {
		e.closing = true
		e.cancel()
	}
	g.mu.Unlock()

	g.cancel()

	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NormalizeCode folds a user-typed code into canonical form: upper-cased, with
// the separators people add when reading a code aloud removed. Characters
// outside the alphabet are dropped rather than guessed at — the alphabet exists
// precisely so nobody has to guess whether that glyph was an O or a zero, and a
// silent substitution would send a typo to the wrong room.
//
// Longer than CodeLen is left alone: it will simply not match a live room.
func NormalizeCode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if strings.ContainsRune(Alphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// internals
// ---------------------------------------------------------------------------

// allocCodeLocked picks an unused code. Caller holds g.mu.
func (g *Registry) allocCodeLocked() (string, error) {
	for range maxCodeAttempts {
		code := randomCode()
		if _, taken := g.rooms[code]; !taken {
			return code, nil
		}
	}
	return "", ErrCodeExhausted
}

// randomCode draws CodeLen characters from Alphabet using crypto/rand with
// rejection sampling, so every code is uniform and none is guessable from a
// previous one.
func randomCode() string {
	const n = len(Alphabet)
	// Largest multiple of n that fits in a byte; values at or above it are
	// rejected so the modulus does not skew the distribution.
	limit := byte(256 / n * n)

	out := make([]byte, 0, CodeLen)
	buf := make([]byte, CodeLen*2)
	for len(out) < CodeLen {
		if _, err := rand.Read(buf); err != nil {
			panic("registry: crypto/rand failed: " + err.Error())
		}
		for _, b := range buf {
			if b >= limit {
				continue
			}
			out = append(out, Alphabet[int(b)%n])
			if len(out) == CodeLen {
				break
			}
		}
	}
	return string(out)
}

// remove drops e from the table, but only if it is still the entry that owns
// the code.
func (g *Registry) remove(e *entry) {
	g.mu.Lock()
	if g.rooms[e.code] == e {
		delete(g.rooms, e.code)
	}
	g.mu.Unlock()
}

// collect is the single GC goroutine. It cancels rooms that are empty past the
// grace period or older than the hard TTL. Cancelling the context is what makes
// the actor goroutine return — without it, every abandoned match leaks a
// goroutine and its whole stroke log for the life of the process.
func (g *Registry) collect() {
	defer g.wg.Done()

	t := time.NewTicker(g.cfg.SweepInterval)
	defer t.Stop()

	for {
		select {
		case <-g.base.Done():
			g.mu.Lock()
			for _, e := range g.rooms {
				e.closing = true
				e.cancel()
			}
			g.mu.Unlock()
			return

		case now := <-t.C:
			g.mu.Lock()
			for _, e := range g.rooms {
				if e.closing {
					continue
				}
				var reason string
				switch {
				case e.holds == 0 && !e.emptySince.IsZero() && now.Sub(e.emptySince) > g.cfg.EmptyGrace:
					reason = "empty"
				case now.Sub(e.createdAt) > g.cfg.HardTTL:
					reason = "expired"
				default:
					continue
				}
				e.closing = true
				e.cancel()
				g.log.Info("room collected", "room", e.code, "reason", reason,
					"age", now.Sub(e.createdAt).Round(time.Second))
			}
			g.mu.Unlock()
		}
	}
}

// Compile-time proof that the registry satisfies what the room asks of its
// owner.
var _ room.Registry = (*Registry)(nil)
