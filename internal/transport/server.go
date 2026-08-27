// Package transport is the WebSocket edge: it upgrades sockets, decodes and
// validates protobuf frames, binds a socket to a seat in a room, and pumps the
// room's events back out as binary frames.
//
// It is deliberately thin. No game rule lives here — the room actor is the only
// authority (IMPLEMENTATION_PLAN.md §4.4). What this package owns is everything
// that must not be trusted: frame size, frame type, the command union, the
// command rate, and the origin of the handshake.
package transport

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/registry"
	"github.com/sreyas-endor/verso/internal/room"
)

// Defaults for Config.
const (
	// DefaultReadLimit caps one inbound frame.
	//
	// The largest legitimate command is a StrokeBegin or StrokeEnd carrying a
	// full room.MaxPointsPerStroke-pair stroke: 2,400 sint32 values, each a
	// zigzag varint of at most 3 bytes because room.CoordMin/CoordMax hold them
	// to the signed 16-bit range. That is ~7.2 KiB of coordinates, plus a
	// packed-field header, a colour index, a width, the command envelope, and a
	// correlation id bounded by MaxCidLen — call it 7.5 KiB in the worst case.
	// TestTheReadLimitFitsTheLargestLegalCommand measures the real number and
	// fails if this constant stops covering it.
	//
	// 16 KiB leaves better than 2x headroom over that and is still a quarter of
	// the 64 KiB it replaces. The point is not the buffer itself but everything
	// downstream of it: every byte accepted here is decoded, and a frame that
	// is mostly padding costs allocation and CPU in proto.Unmarshal before any
	// validation gets a chance to reject it.
	DefaultReadLimit = 16 << 10

	// DefaultPingInterval is how often the shared sweep pings every live socket.
	// There is no per-connection ping timer (IMPLEMENTATION_PLAN.md §4.4).
	DefaultPingInterval = 20 * time.Second

	// DefaultPongTimeout is how long a ping waits for its pong before the
	// connection is considered dead. This is the primary liveness detector: a
	// half-open TCP connection accepts the ping into the kernel buffer and never
	// answers.
	DefaultPongTimeout = 10 * time.Second

	// DefaultIdleTimeout is the backstop for a socket that produces no frame,
	// pong or ping for this long. It must exceed a few ping intervals.
	DefaultIdleTimeout = 90 * time.Second

	// DefaultWriteTimeout bounds one outbound frame.
	DefaultWriteTimeout = 15 * time.Second

	// DefaultCommandBurst and DefaultCommandRate bound inbound commands per
	// connection. A drawing client batches at room.StrokeBatchWindow, so ~20
	// commands a second is the honest peak; the burst absorbs a join storm and
	// a fast scribble in the same window.
	DefaultCommandBurst = 80
	DefaultCommandRate  = 40

	// DefaultCreateBurst and DefaultCreateRate bound room creation per remote
	// address. Creating a room allocates a goroutine and a match; it is the
	// expensive verb and gets its own, much tighter, bucket.
	DefaultCreateBurst = 5
	DefaultCreateRate  = 1.0 / 20.0

	// DefaultSnapshotBurst and DefaultSnapshotRate bound RequestSnapshot per
	// connection, on top of the generic command bucket.
	//
	// A snapshot is the one command whose cost is unbounded by the frame that
	// asks for it: the room walks the entire stroke log, copies every point
	// slice, and the write pump marshals the result — up to room.MaxPointsPerTurn
	// coordinates per turn, for one 30-byte request. Under the generic limiter
	// alone one authenticated client could ask 40 times a second and turn a
	// legal seat into a CPU and bandwidth amplifier against its own room.
	//
	// Two in reserve and one a second is deliberately generous for the honest
	// case, which is a gap in the stroke sequence: the client asks once and
	// waits (web/src/net/socket.ts, RESYNC_RETRY_BASE_MS backs off from 2 s).
	// The snapshot every client gets on join and reconnect does not come from a
	// command at all — the room sends it out of Attach — so this cannot delay
	// or refuse it.
	DefaultSnapshotBurst = 2
	DefaultSnapshotRate  = 1.0

	// DefaultMaxConns caps concurrent sockets for the whole process.
	DefaultMaxConns = 4096

	// maxRateStrikes is how many rate-limited frames a connection may send
	// before it is closed rather than merely told off. A well-behaved client
	// backs off after the first Error.
	maxRateStrikes = 20
)

// Protocol string limits, enforced at the socket boundary.
//
// Sanitizing or truncating a decoded string is not a resource limit: by the
// time sanitizeName runs, the megabyte has already been read, allocated and
// scanned. These are the lengths a frame may carry at all, checked before any
// of that. They are generous next to what an honest client sends — a seat token
// is 64 hex characters, a room code is registry.CodeLen — because the job is to
// stop amplification, not to second-guess the room's own validation.
const (
	// MaxCidLen bounds the correlation id. It is echoed back on the event a
	// command produced, so an unbounded one is an amplifier the client aims
	// at itself and at any log that records it.
	MaxCidLen = 64

	// MaxRawNameLen bounds a display name as received, in bytes, before
	// sanitizing collapses it and the room truncates it to
	// room.MaxDisplayNameLen runes. Wide enough for 24 runes of any script
	// plus the whitespace and combining marks a real name carries.
	MaxRawNameLen = 256

	// MaxRoomCodeLen bounds a join code as received. NormalizeCode drops every
	// character outside the alphabet, so a long one is simply a long scan.
	MaxRoomCodeLen = 64

	// MaxSeatTokenLen bounds a seat token as received. Real ones are 64 hex
	// characters; this leaves room to lengthen them without a protocol change.
	MaxSeatTokenLen = 256

	// MaxPlayerIDLen bounds a player id as received, currently the kick
	// target. Real ones are 16 hex characters.
	MaxPlayerIDLen = 64
)

// Config configures a Server. Only Registry is required.
type Config struct {
	// Registry supplies and accounts for rooms.
	Registry *registry.Registry

	// Logger receives connection lifecycle and rejections. It is never handed a
	// word, a seat token, or a decoded command payload.
	Logger *slog.Logger

	// OriginPatterns is the allowlist passed to websocket.Accept. The request's
	// own Host is always authorized, so this only needs to cover cross-origin
	// cases such as the Vite dev server. Empty means same-origin only.
	OriginPatterns []string

	ReadLimit    int64
	PingInterval time.Duration
	PongTimeout  time.Duration
	IdleTimeout  time.Duration
	WriteTimeout time.Duration

	CommandBurst int
	CommandRate  float64
	CreateBurst  int
	CreateRate   float64

	// SnapshotBurst and SnapshotRate bound RequestSnapshot per connection, on
	// top of the generic command bucket. See DefaultSnapshotBurst.
	SnapshotBurst int
	SnapshotRate  float64

	MaxConns int
}

func (c Config) withDefaults() Config {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.ReadLimit <= 0 {
		c.ReadLimit = DefaultReadLimit
	}
	if c.PingInterval <= 0 {
		c.PingInterval = DefaultPingInterval
	}
	if c.PongTimeout <= 0 {
		c.PongTimeout = DefaultPongTimeout
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleTimeout
	}
	if c.IdleTimeout < 3*c.PingInterval {
		c.IdleTimeout = 3 * c.PingInterval
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = DefaultWriteTimeout
	}
	if c.CommandBurst <= 0 {
		c.CommandBurst = DefaultCommandBurst
	}
	if c.CommandRate <= 0 {
		c.CommandRate = DefaultCommandRate
	}
	if c.CreateBurst <= 0 {
		c.CreateBurst = DefaultCreateBurst
	}
	if c.CreateRate <= 0 {
		c.CreateRate = DefaultCreateRate
	}
	if c.SnapshotBurst <= 0 {
		c.SnapshotBurst = DefaultSnapshotBurst
	}
	if c.SnapshotRate <= 0 {
		c.SnapshotRate = DefaultSnapshotRate
	}
	if c.MaxConns <= 0 {
		c.MaxConns = DefaultMaxConns
	}
	return c
}

// Server is the WebSocket endpoint. One per process.
type Server struct {
	cfg Config
	log *slog.Logger
	reg *registry.Registry

	base context.Context

	creates *bucketMap

	// mu guards conns, shut and every wg.Add. Adding to the WaitGroup under the
	// same lock that publishes the shutdown flag is what makes Wait safe: once
	// shut is set no handler can register, and until it is set the sweep
	// goroutine is still holding a count of its own.
	mu    sync.Mutex
	conns map[*conn]struct{}
	shut  bool

	nextID atomic.Uint64
	live   atomic.Int64

	wg sync.WaitGroup
}

// New builds a Server and starts its single sweep goroutine. Cancelling ctx
// closes every live socket; Wait then blocks until they are gone.
func New(ctx context.Context, cfg Config) *Server {
	cfg = cfg.withDefaults()

	s := &Server{
		cfg:     cfg,
		log:     cfg.Logger,
		reg:     cfg.Registry,
		base:    ctx,
		creates: newBucketMap(cfg.CreateBurst, cfg.CreateRate),
		conns:   make(map[*conn]struct{}),
	}

	s.wg.Add(1)
	go s.sweep()

	return s
}

// Handler is the /ws endpoint.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

// Live reports the number of open sockets.
func (s *Server) Live() int { return int(s.live.Load()) }

// Wait blocks until the sweep goroutine and every connection handler have
// returned. Call it after cancelling the context passed to New.
func (s *Server) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// serve upgrades one request and runs the connection to completion.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	select {
	case <-s.base.Done():
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	default:
	}

	// coder/websocket needs to hijack the connection, which HTTP/2 cannot do.
	// Unencrypted HTTP/2 is enabled on this server for everything else, and
	// RFC 8441 (WebSockets over HTTP/2) is not implemented in Go, so say so
	// plainly instead of failing inside Accept (IMPLEMENTATION_PLAN.md §2).
	if r.ProtoMajor != 1 {
		http.Error(w, "websocket requires HTTP/1.1", http.StatusUpgradeRequired)
		return
	}

	// A cheap pre-upgrade read, so an over-capacity server answers with an HTTP
	// error instead of completing a handshake it is about to abandon. It is not
	// the enforcement point: see add, which re-checks under the lock.
	if s.live.Load() >= int64(s.cfg.MaxConns) {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithCancel(s.base)
	defer cancel()

	c := &conn{
		id:     s.nextID.Add(1),
		srv:    s,
		ctx:    ctx,
		cancel: cancel,
		out:    make(chan *genpb.ServerEvent, room.OutboundQueueDepth),
		ping:   make(chan struct{}, 1),
		term:   make(chan struct{}, 1),
		wdone:  make(chan struct{}),
		cmds:   newBucket(s.cfg.CommandBurst, s.cfg.CommandRate),
		snaps:  newBucket(s.cfg.SnapshotBurst, s.cfg.SnapshotRate),
		remote: remoteAddr(r),
	}
	c.touch(time.Now())
	c.log = s.log.With("conn", c.id, "remote", c.remote)

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: s.cfg.OriginPatterns,
		// Compression is off, and the stroke path is why. A StrokePoints frame
		// is a stroke id, a seq and a handful of zigzag varint pairs — well
		// under any useful deflate threshold, so it was never being compressed
		// on the way out. What context takeover did buy was cost: the mode
		// pins a ~1.2 MB flate.Writer per connection for the life of the
		// socket (it is allocated on the first frame over the threshold, and
		// every client gets a Snapshot right after joining, so every
		// connection paid it). At the deployed --concurrency=200 that is
		// ~240 MB of deflate state inside a 512 MiB container, which turns
		// into GC pressure, which on a single-vCPU instance is time taken
		// directly from the write pumps. The inbound half was worse: browsers
		// compress every data frame once the extension is negotiated, and the
		// read path inflates them regardless of size, so the threshold bought
		// nothing there at all.
		//
		// The one thing that does compress well is Snapshot, and a snapshot is
		// rare, bounded by the per-turn point caps, and already the burstiest
		// thing the server does — paying for it in bandwidth is the better
		// trade than paying for it in resident memory on every idle lobby
		// socket. This also sidesteps the iOS Safari
		// client_no_context_takeover negotiation noted in
		// IMPLEMENTATION_PLAN.md §8, open question 6.
		CompressionMode: websocket.CompressionDisabled,
		// Both callbacks keep the liveness clock honest for a socket that is
		// alive but has nothing to say — a lobby can sit silent for minutes.
		OnPingReceived: func(context.Context, []byte) bool {
			c.touch(time.Now())
			return true
		},
		OnPongReceived: func(context.Context, []byte) {
			c.touch(time.Now())
		},
	})
	if err != nil {
		// Accept has already written the HTTP error response.
		s.log.Debug("websocket upgrade rejected", "remote", remoteAddr(r), "err", err)
		return
	}
	defer ws.CloseNow()

	ws.SetReadLimit(s.cfg.ReadLimit)
	c.ws = ws

	if status, reason := s.add(c); reason != "" {
		_ = ws.Close(status, reason)
		return
	}
	defer s.remove(c)

	go c.writePump()

	status, reason := c.readLoop()

	// Stop the writer and wait for it before touching the socket again, so the
	// close frame cannot interleave with a half-written message.
	c.cancel()
	<-c.wdone

	c.leave()

	_ = ws.Close(status, reason)
}

// add registers a connection and takes its WaitGroup count. It returns false
// once the server is shutting down, or once MaxConns is actually reached.
//
// The cap is enforced HERE and not only at the pre-upgrade read in serve. That
// read is racy by construction: every handler that passes it goes on to run an
// upgrade before it registers, so a burst of simultaneous handshakes — exactly
// what a reconnect storm is — can all observe the same under-capacity count and
// all admit. Checking against s.conns under the same lock that publishes it is
// what makes MaxConns a number rather than an estimate.
//
// It returns the close status and reason to send when it refuses, and an empty
// reason when the connection is registered.
func (s *Server) add(c *conn) (websocket.StatusCode, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shut {
		return websocket.StatusGoingAway, "server shutting down"
	}
	if len(s.conns) >= s.cfg.MaxConns {
		// 1013 Try Again Later, not 1001 Going Away: the server is fine, it is
		// full, and the client should come back rather than give up.
		return websocket.StatusTryAgainLater, "too many connections"
	}
	s.wg.Add(1)
	s.conns[c] = struct{}{}
	s.live.Add(1)
	return 0, ""
}

func (s *Server) remove(c *conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	s.live.Add(-1)
	s.wg.Done()
}

func (s *Server) snapshotConns() []*conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		out = append(out, c)
	}
	return out
}

// sweep is the one shared liveness ticker for the whole process. It pings every
// socket, kills the ones that have gone quiet, and prunes the per-address rate
// buckets. Per-connection ping timers are what this replaces.
func (s *Server) sweep() {
	defer s.wg.Done()

	t := time.NewTicker(s.cfg.PingInterval)
	defer t.Stop()

	for {
		select {
		case <-s.base.Done():
			s.mu.Lock()
			s.shut = true
			s.mu.Unlock()
			for _, c := range s.snapshotConns() {
				c.cancel()
			}
			return

		case now := <-t.C:
			for _, c := range s.snapshotConns() {
				if now.Sub(c.lastSeen()) > s.cfg.IdleTimeout {
					c.log.Info("connection idle, closing")
					c.cancel()
					continue
				}
				// Buffered depth 1: if the previous ping has not been serviced
				// yet there is no point queueing another.
				select {
				case c.ping <- struct{}{}:
				default:
				}
			}
			s.creates.prune(now)
		}
	}
}

// allowCreate applies the room-creation bucket for one remote address.
func (s *Server) allowCreate(remote string) bool {
	return s.creates.allow(remote, time.Now())
}

// remoteAddr reduces a request to the address the rate limiter keys on. Proxy
// headers are deliberately ignored: this server is meant to be reached directly
// on a LAN, and trusting an unverified X-Forwarded-For would let any client
// forge its own bucket.
func remoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
