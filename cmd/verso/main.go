// Command verso runs the whole game: the WebSocket endpoint, the room registry
// and the embedded client, in one static binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sreyas-endor/verso/internal/registry"
	"github.com/sreyas-endor/verso/internal/room"
	"github.com/sreyas-endor/verso/internal/transport"
	"github.com/sreyas-endor/verso/internal/words"
)

const (
	// shutdownGrace bounds the graceful stop before the process exits anyway.
	shutdownGrace = 10 * time.Second

	// readHeaderTimeout must be set explicitly. Leaving it at zero is
	// GO-2026-6089's precondition and a slowloris invitation
	// (IMPLEMENTATION_PLAN.md §3).
	readHeaderTimeout = 10 * time.Second

	// idleTimeout closes a kept-alive HTTP connection that is doing nothing.
	// It does not apply to a hijacked WebSocket.
	idleTimeout = 2 * time.Minute
)

func main() {
	var (
		addr     = flag.String("addr", defaultAddr(), "listen address")
		dev      = flag.Bool("dev", false, "serve the client from -webroot on disk instead of the embedded build")
		webroot  = flag.String("webroot", "web/dist", "directory served in -dev mode")
		origins  = flag.String("origins", "", "extra comma-separated Origin host patterns allowed to open a WebSocket")
		maxRooms = flag.Int("max-rooms", registry.DefaultMaxRooms, "cap on simultaneous live rooms")
		maxConns = flag.Int("max-conns", transport.DefaultMaxConns, "cap on simultaneous WebSocket connections")
		logLevel = flag.String("log", "info", "log level: debug, info, warn, error")
	)
	flag.Parse()

	log := newLogger(*logLevel)
	slog.SetDefault(log)

	if err := run(log, *addr, *dev, *webroot, *origins, *maxRooms, *maxConns); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, addr string, dev bool, webroot, origins string, maxRooms, maxConns int) error {
	fsys, err := staticFS(dev, webroot)
	if err != nil {
		return fmt.Errorf("client assets: %w", err)
	}

	// appCtx is the lifetime of everything that outlives a request: the rooms
	// and the live sockets. It is cancelled deliberately during shutdown, not
	// by the signal, so the order of teardown is ours to choose.
	appCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()

	reg := registry.New(appCtx, registry.Config{
		// One deck per room: internal/words draws without replacement, so a
		// shared deck would let one match shorten the next one's catalogue.
		NewDeck:  func() room.Deck { return words.New() },
		Settings: room.DefaultSettings(),
		Logger:   log,
		MaxRooms: maxRooms,
	})

	ws := transport.New(appCtx, transport.Config{
		Registry:       reg,
		Logger:         log,
		OriginPatterns: originPatterns(origins),
		MaxConns:       maxConns,
	})

	mux := http.NewServeMux()
	mux.Handle("/ws", ws.Handler())
	// NOT /healthz. Google Frontend reserves the internal z-page names and
	// answers them itself with its own 404 page, so a request for /healthz
	// never reaches this process on Cloud Run. Verified against the deployed
	// service: /healthz, /statusz, /varz and /rpcz are all swallowed, while
	// /health, /healthz2 and /debugz arrive normally. The give-away is the
	// response headers — an intercepted 404 carries no `server: Google
	// Frontend` and no x-cloud-trace-context, because it never entered the
	// Cloud Run serving path at all.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "ok rooms=%d conns=%d\n", reg.Count(), ws.Live())
	})
	mux.Handle("/", newSPAHandler(fsys))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		// ReadTimeout and WriteTimeout stay unset on purpose: they would apply
		// to the hijacked WebSocket and kill every long-lived connection. The
		// per-frame deadlines in package transport are the real bound.
		ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext: func(net.Listener) context.Context { return appCtx },
	}
	// x/net/http2/h2c is deprecated; this is its replacement
	// (IMPLEMENTATION_PLAN.md §3). WebSockets still require HTTP/1.1 — Go has
	// no RFC 8441 — and the /ws handler says so.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv.Protocols = protocols

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	announce(log, ln.Addr(), dev)

	serveErr := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	select {
	case err := <-serveErr:
		if err != nil {
			return err
		}
	case <-sigCtx.Done():
		stopSignals() // a second signal now kills the process outright
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	// Stop accepting first, then drop the live sockets, then wait for the room
	// actors. Cancelling appCtx is what makes the WebSocket handlers return —
	// http.Server.Shutdown does not track hijacked connections.
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		log.Warn("http shutdown", "err", err)
	}
	stopApp()
	if err := ws.Wait(shutdownCtx); err != nil {
		log.Warn("connections did not close in time", "err", err)
	}
	if err := reg.Close(shutdownCtx); err != nil {
		log.Warn("rooms did not close in time", "err", err)
	}

	log.Info("stopped")
	return nil
}

// defaultAddr is the listen address before any flag is parsed. A managed
// runtime — Cloud Run, Heroku, Fly — tells the process which port to bind
// through $PORT and kills a container that binds anything else, so the
// environment wins over the built-in default and -addr still overrides both.
func defaultAddr() string {
	if p := strings.TrimSpace(os.Getenv("PORT")); p != "" {
		return net.JoinHostPort("", p)
	}
	return ":8080"
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}

// originPatterns builds the WebSocket Origin allowlist. websocket.Accept always
// authorizes the request's own Host, so this only has to cover the cross-origin
// cases: a Vite dev server on another port, and a phone that reached the laptop
// by mDNS name while the browser tab was opened by IP.
func originPatterns(extra string) []string {
	pats := []string{
		"localhost", "localhost:*",
		"127.0.0.1", "127.0.0.1:*",
		"[::1]", "[::1]:*",
		"*.local", "*.local:*",
		// RFC 1918 and link-local. path.Match's '*' spans dots, so one star
		// covers the whole remaining address.
		"10.*", "10.*:*",
		"192.168.*", "192.168.*:*",
		"169.254.*", "169.254.*:*",
	}
	for i := 16; i <= 31; i++ {
		pats = append(pats, fmt.Sprintf("172.%d.*", i), fmt.Sprintf("172.%d.*:*", i))
	}
	for _, p := range strings.Split(extra, ",") {
		if p = strings.TrimSpace(p); p != "" {
			pats = append(pats, p)
		}
	}
	return pats
}

// announce prints the URLs the host can read out to the room. This runs on a
// laptop at a party, so the LAN address is the whole point.
func announce(log *slog.Logger, listen net.Addr, dev bool) {
	tcp, ok := listen.(*net.TCPAddr)
	if !ok {
		log.Info("verso listening", "addr", listen.String(), "dev", dev)
		return
	}
	port := tcp.Port

	fmt.Fprintf(os.Stdout, "\n  verso is running%s\n\n", devSuffix(dev))
	fmt.Fprintf(os.Stdout, "    local    http://localhost:%d\n", port)
	for _, ip := range lanAddrs(tcp.IP) {
		fmt.Fprintf(os.Stdout, "    network  http://%s\n", net.JoinHostPort(ip, fmt.Sprint(port)))
	}
	fmt.Fprintln(os.Stdout)
}

func devSuffix(dev bool) string {
	if dev {
		return " (dev: client served from disk)"
	}
	return ""
}

// lanAddrs lists the IPv4 addresses friends on the same network can reach. When
// the listener is bound to one specific address there is nothing to discover.
func lanAddrs(bound net.IP) []string {
	if bound != nil && !bound.IsUnspecified() {
		if bound.IsLoopback() {
			return nil
		}
		return []string{bound.String()}
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return out
}
