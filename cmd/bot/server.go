package main

// server.go — an in-process server on an ephemeral port.
//
// This is the whole point of milestone 9 being runnable in CI: no browser, no
// external process, no fixed port, and the real registry, transport and room
// actor rather than a stand-in. The only thing swapped is the deck, so the
// canary words are searchable (deck.go).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/sreyas-endor/verso/internal/registry"
	"github.com/sreyas-endor/verso/internal/room"
	"github.com/sreyas-endor/verso/internal/transport"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// ServerOptions configures the in-process server.
type ServerOptions struct {
	// Settings is the starting lobby configuration for every room.
	Settings *genpb.MatchSettings
	// NewDeck supplies each room's deck. Nil means a canary deck.
	NewDeck func() room.Deck
	// Logger receives server-side lines. Nil discards.
	Logger *slog.Logger
	// Addr is the listen address. Empty means 127.0.0.1 on an ephemeral port.
	Addr string
}

// LocalServer is a running verso server plus the handle to shut it down.
type LocalServer struct {
	// URL is the WebSocket endpoint, ws://host:port/ws.
	URL string
	// Addr is the bound address.
	Addr string

	reg    *registry.Registry
	ws     *transport.Server
	http   *http.Server
	cancel context.CancelFunc
	serve  chan error
}

// StartServer binds a listener and serves until Close. It returns once the
// listener is up, so a bot may dial the returned URL immediately.
func StartServer(ctx context.Context, opts ServerOptions) (*LocalServer, error) {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	newDeck := opts.NewDeck
	if newDeck == nil {
		newDeck = func() room.Deck { return NewCanaryDeck("VERSOBOT") }
	}

	appCtx, cancel := context.WithCancel(ctx)

	reg := registry.New(appCtx, registry.Config{
		NewDeck:  newDeck,
		Settings: opts.Settings,
		Logger:   log,
	})
	ws := transport.New(appCtx, transport.Config{
		Registry: reg,
		Logger:   log,
		// A whole table of bots creates its rooms from 127.0.0.1, and the
		// per-address create bucket is deliberately tight in production. The
		// harness runs many matches from one address, so it is widened here and
		// nowhere else.
		CreateBurst: 64,
		CreateRate:  32,
	})

	mux := http.NewServeMux()
	mux.Handle("/ws", ws.Handler())
	// Same name as the real server (cmd/verso/main.go), not /healthz.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "ok rooms=%d conns=%d\n", reg.Count(), ws.Live())
	})

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("listen %s: %w", opts.Addr, err)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return appCtx },
	}

	s := &LocalServer{
		URL:    "ws://" + ln.Addr().String() + "/ws",
		Addr:   ln.Addr().String(),
		reg:    reg,
		ws:     ws,
		http:   srv,
		cancel: cancel,
		serve:  make(chan error, 1),
	}

	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.serve <- err
	}()

	return s, nil
}

// Rooms reports how many rooms are live right now.
func (s *LocalServer) Rooms() int { return s.reg.Count() }

// Conns reports how many sockets are open right now.
func (s *LocalServer) Conns() int { return s.ws.Live() }

// Close stops accepting, drops the live sockets, and waits for the room actors.
// The order matters: cancelling the app context is what makes the hijacked
// WebSocket handlers return, since http.Server.Shutdown does not track them.
func (s *LocalServer) Close(ctx context.Context) error {
	shutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	err := s.http.Shutdown(shutCtx)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("http shutdown: %w", err)
	} else {
		err = nil
	}
	s.cancel()
	if e := s.ws.Wait(shutCtx); e != nil && err == nil {
		err = fmt.Errorf("sockets did not close: %w", e)
	}
	if e := s.reg.Close(shutCtx); e != nil && err == nil {
		err = fmt.Errorf("rooms did not close: %w", e)
	}
	if e := <-s.serve; e != nil && err == nil {
		err = e
	}
	return err
}
