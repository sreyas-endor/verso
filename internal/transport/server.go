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
	// DefaultReadLimit caps one inbound frame. The largest legitimate command is
	// a StrokeBegin or StrokePoints carrying room.MaxPointsPerStroke pairs,
	// which is a few kilobytes of varints. 64 KiB is generous and still stops a
	// client from asking the server to buffer a megabyte.
	DefaultReadLimit = 64 << 10

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

	// DefaultMaxConns caps concurrent sockets for the whole process.
	DefaultMaxConns = 4096

	// maxRateStrikes is how many rate-limited frames a connection may send
	// before it is closed rather than merely told off. A well-behaved client
	// backs off after the first Error.
	maxRateStrikes = 20
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
		wdone:  make(chan struct{}),
		cmds:   newBucket(s.cfg.CommandBurst, s.cfg.CommandRate),
		remote: remoteAddr(r),
	}
	c.touch(time.Now())
	c.log = s.log.With("conn", c.id, "remote", c.remote)

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: s.cfg.OriginPatterns,
		// Context takeover keeps the 32 KiB deflate window between messages,
		// which matters because consecutive stroke frames are near-identical.
		// It is also the mode that does NOT ask the peer to honour
		// client_no_context_takeover — the negotiation iOS Safari has
		// historically got wrong (IMPLEMENTATION_PLAN.md §8, open question 6).
		CompressionMode:      websocket.CompressionContextTakeover,
		CompressionThreshold: 256,
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

	if !s.add(c) {
		_ = ws.Close(websocket.StatusGoingAway, "server shutting down")
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
// once the server is shutting down.
func (s *Server) add(c *conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shut {
		return false
	}
	s.wg.Add(1)
	s.conns[c] = struct{}{}
	s.live.Add(1)
	return true
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
