// Command bot is the headless protocol client and match driver for verso
// (IMPLEMENTATION_PLAN.md §6, milestone 9).
//
// A ten-player game cannot be manually playtested, so the protocol has to be
// drivable without a browser. These bots speak the real wire protocol over a
// real WebSocket against a real server: they join, ready up, draw on their turn,
// vote, reconnect with their seat token, and check every frame they receive for
// somebody else's secret.
//
// Usage:
//
//	bot play  [flags]           one match; boots a server unless -url is given
//	bot suite [flags]           the 3-, 6- and 10-player matches, in parallel
//	bot serve [flags]           just run a server, for driving from elsewhere
//
// Exit status is non-zero if any match leaked a secret, violated the protocol,
// or failed to reach MatchEnded.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/room"
	"github.com/sreyas-endor/verso/internal/words"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "play":
		err = cmdPlay(os.Args[2:])
	case "suite":
		err = cmdSuite(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nbot: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `bot — headless verso protocol client

  bot play  [flags]   run one match (boots a server unless -url is given)
  bot suite [flags]   run the 3-, 6- and 10-player matches in parallel
  bot serve [flags]   run a server and wait for a signal

Run "bot play -h" for the flags.
`)
}

// commonFlags are shared by play and suite.
type commonFlags struct {
	url          string
	rounds       int
	draw         int
	discuss      int
	intermission int
	difficulty   string
	strategy     string
	deck         string
	seed         uint64
	logLevel     string
	verbose      bool
}

func (c *commonFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.url, "url", "", "WebSocket endpoint of a running server; empty boots one in-process")
	fs.IntVar(&c.rounds, "rounds", 1, "max rounds (1..4)")
	fs.IntVar(&c.draw, "draw", room.MinDrawSeconds, "drawing seconds per turn (5..60)")
	fs.IntVar(&c.discuss, "discuss", room.MinDiscussSeconds, "discussion seconds (30..180)")
	fs.IntVar(&c.intermission, "intermission", room.MinIntermissionSeconds, "handoff seconds between turns (3..30)")
	fs.StringVar(&c.difficulty, "difficulty", "medium", "deck tier: easy, medium, hard")
	fs.StringVar(&c.strategy, "strategy", "skip", "vote strategy: random, skip, self, gang, silent")
	fs.StringVar(&c.deck, "deck", "canary", "in-process deck: canary (searchable words) or words (the real deck)")
	fs.Uint64Var(&c.seed, "seed", 0, "client-side seed; 0 picks one")
	fs.StringVar(&c.logLevel, "log", "warn", "log level: debug, info, warn, error")
	fs.BoolVar(&c.verbose, "v", false, "print every bot's per-frame accounting")
}

func (c *commonFlags) settings() (*genpb.MatchSettings, error) {
	var d genpb.Difficulty
	switch strings.ToLower(c.difficulty) {
	case "easy":
		d = genpb.Difficulty_DIFFICULTY_EASY
	case "medium", "":
		d = genpb.Difficulty_DIFFICULTY_MEDIUM
	case "hard":
		d = genpb.Difficulty_DIFFICULTY_HARD
	default:
		return nil, fmt.Errorf("unknown difficulty %q", c.difficulty)
	}
	return &genpb.MatchSettings{
		Difficulty:          d,
		MaxRounds:           int32(c.rounds),
		DrawSeconds:         int32(c.draw),
		DiscussSeconds:      int32(c.discuss),
		IntermissionSeconds: int32(c.intermission),
	}, nil
}

func (c *commonFlags) logger() *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(c.logLevel)); err != nil {
		lv = slog.LevelWarn
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}

func (c *commonFlags) newDeck() func() room.Deck {
	if strings.EqualFold(c.deck, "words") {
		return func() room.Deck { return words.New() }
	}
	return nil // StartServer defaults to the canary deck
}

// endpoint returns the URL to drive and a cleanup func, booting a server when
// none was supplied.
func (c *commonFlags) endpoint(ctx context.Context, s *genpb.MatchSettings, log *slog.Logger) (string, func(), error) {
	if c.url != "" {
		return c.url, func() {}, nil
	}
	srv, err := StartServer(ctx, ServerOptions{
		Settings: s,
		NewDeck:  c.newDeck(),
		Logger:   log,
	})
	if err != nil {
		return "", nil, err
	}
	fmt.Printf("server listening on %s\n", srv.Addr)
	return srv.URL, func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Close(shutdown); err != nil {
			fmt.Fprintf(os.Stderr, "server shutdown: %v\n", err)
		}
	}, nil
}

// ---------------------------------------------------------------------------
// play
// ---------------------------------------------------------------------------

func cmdPlay(args []string) error {
	fs := flag.NewFlagSet("play", flag.ExitOnError)
	var c commonFlags
	c.bind(fs)
	players := fs.Int("players", 3, "seats to fill (3..10)")
	dropImposter := fs.Bool("drop-imposter", false, "disconnect the imposter for good once the words are dealt")
	dropMidTurn := fs.Duration("drop-midturn", 0, "make one guest drop this far into its own drawing turn")
	dropAtVote := fs.Duration("drop-imposter-at-vote", 0, "make the imposter fall silent and drop this far into the voting window, then return")
	rejoinAfter := fs.Duration("rejoin-after", 500*time.Millisecond, "how long that guest stays dark before reclaiming its seat")
	clampProbe := fs.Bool("clamp-probe", true, "send an over-wide brush on the first stroke and check the server clamps it")
	timeout := fs.Duration("timeout", 0, "hard bound on the match; 0 derives one from the settings")
	if err := fs.Parse(args); err != nil {
		return err
	}

	settings, err := c.settings()
	if err != nil {
		return err
	}
	log := c.logger()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	url, cleanup, err := c.endpoint(ctx, settings, log)
	if err != nil {
		return err
	}
	defer cleanup()

	plan := MatchPlan{
		Name:         fmt.Sprintf("%d-player", *players),
		Players:      *players,
		Settings:     settings,
		Strategy:     c.strategy,
		Draw:         DrawPlan{ClampProbe: *clampProbe},
		DropImposter: *dropImposter,
		Timeout:      *timeout,
		Logger:       log,
		Seed:         c.seed,
	}
	if *dropMidTurn > 0 {
		plan.DropMidTurn = &DropPlan{OnMyTurnAfter: *dropMidTurn, RejoinAfter: *rejoinAfter}
		plan.DropMidTurnSeat = 1
	}
	if *dropAtVote > 0 {
		plan.DropImposterAtVote = &DropPlan{OnDiscussionAfter: *dropAtVote, RejoinAfter: *rejoinAfter}
	}

	res, err := RunMatch(ctx, url, plan)
	if res != nil {
		fmt.Print(res.Summary())
		if c.verbose {
			printBots(res)
		}
	}
	if err != nil {
		return err
	}
	if !res.OK() {
		return errors.New("the match did not come back clean")
	}
	return nil
}

func printBots(res *MatchResult) {
	for _, rep := range res.Reports {
		fmt.Printf("  %-8s %-16s word=%-24q frames=%-4d strokes=%-3d errors=%d\n",
			rep.Name, rep.PlayerID, rep.Word, rep.Frames, rep.Strokes, len(rep.Errors))
	}
}

// ---------------------------------------------------------------------------
// suite
// ---------------------------------------------------------------------------

func cmdSuite(args []string) error {
	fs := flag.NewFlagSet("suite", flag.ExitOnError)
	var c commonFlags
	c.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	settings, err := c.settings()
	if err != nil {
		return err
	}
	log := c.logger()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	url, cleanup, err := c.endpoint(ctx, settings, log)
	if err != nil {
		return err
	}
	defer cleanup()

	plans := []MatchPlan{
		{
			Name: "3-player group win", Players: 3, Strategy: "gang",
			Draw: DrawPlan{ClampProbe: true},
		},
		{
			Name: "6-player imposter win", Players: 6, Strategy: "skip",
		},
		{
			Name: "10-player imposter win", Players: 10, Strategy: "skip",
		},
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []*MatchResult
		failed  int
	)
	for _, p := range plans {
		p.Settings = settings
		p.Logger = log
		p.Seed = c.seed
		wg.Add(1)
		go func(p MatchPlan) {
			defer wg.Done()
			res, err := RunMatch(ctx, url, p)
			mu.Lock()
			defer mu.Unlock()
			if res != nil {
				results = append(results, res)
			}
			if err != nil {
				failed++
				fmt.Fprintf(os.Stderr, "%s: %v\n", p.Name, err)
				return
			}
			if !res.OK() {
				failed++
			}
		}(p)
	}
	wg.Wait()

	for _, res := range results {
		fmt.Print(res.Summary())
		if c.verbose {
			printBots(res)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d matches did not come back clean", failed, len(plans))
	}
	return nil
}

// ---------------------------------------------------------------------------
// serve
// ---------------------------------------------------------------------------

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var c commonFlags
	c.bind(fs)
	addr := fs.String("addr", "127.0.0.1:0", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	settings, err := c.settings()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := StartServer(ctx, ServerOptions{
		Settings: settings,
		NewDeck:  c.newDeck(),
		Logger:   c.logger(),
		Addr:     *addr,
	})
	if err != nil {
		return err
	}
	fmt.Printf("bot server listening on %s\n  ws endpoint %s\n", srv.Addr, srv.URL)
	<-ctx.Done()

	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Close(shutdown)
}
