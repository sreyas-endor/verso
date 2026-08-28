package main

// bot.go — a headless protocol client (IMPLEMENTATION_PLAN.md §6, milestone 9).
//
// A Bot speaks the real wire protocol over a real WebSocket: binary protobuf
// frames, seat tokens, the lot. It knows nothing the server has not told it,
// which is the point — the harness exists to keep the protocol drivable without
// a browser, so anything a bot cannot work out from its own inbox is a protocol
// smell rather than a bot limitation.
//
// Concurrency. One goroutine owns all bot state, mirroring the room's own actor
// model, so there is no mutex here either. A reader goroutine per socket does
// nothing but decode frames into b.frames; every timer, command and directive
// lands in the same select. Fields that the table reads directly (id, word,
// token) are written once and published by closing a channel, which is a real
// happens-before edge rather than a hopeful one.
//
// The bot verifies as it goes. Two categories:
//
//   - Leaks, checked by the shared Watchdog: no frame may carry another
//     player's word or seat token.
//   - Conformance, checked here: monotonic stroke seq, a round number inside
//     the configured total, a majority threshold that matches its own
//     denominator, was_imposter only in the resolution that ends the match for
//     the group, a final reveal that agrees with the word this bot was dealt.
//
// Both record a violation string rather than panicking, so one match yields the
// complete list instead of the first item on it.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	mrand "math/rand/v2"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/room"
)

// botReadLimit must be generous: a Snapshot replays the entire stroke log in
// one frame, and the server puts no ceiling on that. See the report in the
// package doc of table.go.
const botReadLimit = 8 << 20

// writeTimeout bounds one outbound frame.
const writeTimeout = 10 * time.Second

// DropPlan makes a bot kill its own socket mid-turn, the way a phone locking
// its screen does, and optionally come back with its seat token.
type DropPlan struct {
	// OnMyTurnAfter is how far into this bot's own drawing turn to drop. Zero
	// disables that trigger.
	OnMyTurnAfter time.Duration
	// OnDiscussionAfter is how far into the voting window to drop. Zero
	// disables that trigger. Aimed at the imposter with NeverVote, it is the
	// deterministic reproduction of the final-round overrun described in the
	// package report: the denominator shrinking is itself an early-resolve
	// trigger (internal/room/reconnect.go, detachOnActor), so the tally then
	// runs at the one instant internal/room/end.go refuses to end on.
	OnDiscussionAfter time.Duration
	// RejoinAfter is how long to stay dark. Zero means never come back, which
	// is the imposter-disconnect path (DESIGN.md:125).
	RejoinAfter time.Duration
}

// BotConfig configures one bot.
type BotConfig struct {
	// Name is the display name. Truncated to room.MaxDisplayNameLen server-side.
	Name string
	// URL is the WebSocket endpoint, e.g. ws://127.0.0.1:8080/ws.
	URL string
	// RoomCode is the room to join. Empty creates a new room and makes this bot
	// the host.
	RoomCode string
	// Vote is the voting strategy. Nil means Random.
	Vote VoteStrategy
	// Draw is how much this bot draws on its turn.
	Draw DrawPlan
	// Watch is the shared leak oracle. Nil disables leak checking.
	Watch *Watchdog
	// Drop, when set, scripts a mid-turn disconnect.
	Drop *DropPlan
	// Provoke makes the bot send a handful of commands it knows will be
	// refused, once each, so the Error frame is actually on the wire and gets
	// scanned like every other frame. internal/room/vote.go and
	// internal/room/strokes.go both build a rejection with the sending player
	// in hand, which makes Error.message a live leak surface; a harness that
	// only ever sends legal traffic never looks at it.
	Provoke bool
	// Log receives lifecycle lines. Nil discards.
	Log *slog.Logger
	// Rand seeds this bot's drawing and voting. Nil means a fresh generator.
	Rand *mrand.Rand
}

// inFrame is one decoded frame, or the death of the socket that carried it.
type inFrame struct {
	gen int
	ev  *genpb.ServerEvent
	err error
}

// Bot is one headless player.
type Bot struct {
	cfg  BotConfig
	log  *slog.Logger
	rnd  *mrand.Rand
	vote VoteStrategy

	ctx    context.Context
	cancel context.CancelFunc

	ws  *websocket.Conn
	gen int

	frames chan inFrame
	ctrl   chan func()

	// cid is namespaced per bot so a correlation id that comes back on a frame
	// can be attributed. See foreignCids.
	cidPrefix string

	// Published state. Each field is written exactly once, by the actor, before
	// the matching channel is closed; the table reads it only after that close.
	// The mutable working copies below are actor-owned and never leave the
	// goroutine except through Do.
	pubPlayerID  string
	pubSeatToken string
	pubRoomCode  string
	pubIsHost    bool
	pubWord      string

	playerID  string
	seatToken string
	roomCode  string
	isHost    bool
	// word is the CURRENT round's word; wordRound is the round it was dealt
	// for. Every round deals a fresh pair (game.proto YourWord), so a changed
	// word is a violation only when the round did not move with it.
	word      string
	wordRound int32
	// ownWords is every word this bot has ever legitimately held. The leak
	// scanner needs the whole set, not just the current one: a transcript
	// swept at the end of a match still contains round 1's frames, and round
	// 1's word is not a leak in them.
	ownWords []string

	joinedC chan struct{}
	wordC   chan struct{}
	endC    chan struct{}
	doneC   chan struct{}

	joinedOnce sync.Once
	wordOnce   sync.Once
	endOnce    sync.Once

	// --- actor-owned ---
	phase       genpb.Phase
	round       int32
	totalRounds int32
	settings    *genpb.MatchSettings
	roster      map[string]*genpb.PlayerInfo
	seatOrder   []string
	turnOrder   []string
	artistID    string
	eliminated  bool
	canStart    bool

	lastSeq        int32
	seqSeen        bool
	strokesClosed  int
	clampProbeOpen bool
	firstStroke    bool

	voteCid      string
	awaitingVote bool
	votedRound   int32

	foreignCids      int
	foreignCidSample string

	provNotArtist bool
	provDupVote   bool
	provNotHost   bool
	provResync    bool
	clampProbes   int

	drawPen   *pen
	drawTimer *time.Ticker

	dropTimer   *time.Timer
	rejoinTimer *time.Timer
	dropArmed   bool
	dropUsed    bool
	rejoinAfter time.Duration
	stayDown    bool
	expect      *resyncExpect

	resyncs int
	ended   *genpb.MatchEnded

	// pluralityWinner is who the last VoteTally says should go: the candidate
	// strictly ahead of every other candidate AND of skip_count, or "" when the
	// lead is tied or Skip holds it. onPlayerEliminated checks the server's
	// answer against it, so the plurality rule is verified from the wire rather
	// than trusted (DESIGN.md:58).
	pluralityWinner string
	sawTally        bool

	// sawImposter records a PlayerEliminated{was_imposter:true}. With one
	// imposter it may only appear in the resolution that wins the match for the
	// group; with two, the first one caught sets it in a resolution the match
	// survives (MULTIPLE_IMPOSTERS.md, "Elimination Results").
	sawImposter bool
	// impostersCaught counts those frames, so a Reveal match can be checked
	// against the configured imposter count.
	impostersCaught int
	// hiddenDisclosure records a Hidden match disclosing an alignment before
	// MatchEnded arrived, which is only legal in the resolution that ends it.
	hiddenDisclosure bool
	spectator        *genpb.SpectatorInfo
	frameCounts      map[string]int
	transcript       []*genpb.ServerEvent
	errors           []*genpb.Error
	violations       []string
	leaks            []Leak
	runErr           error
}

// resyncExpect is the state a bot asserts it gets back after a reconnect.
type resyncExpect struct {
	playerID  string
	word      string
	roomCode  string
	round     int32
	strokes   int
	settings  *genpb.MatchSettings
	capturedT time.Time
}

// NewBot builds a bot. It does not touch the network.
func NewBot(cfg BotConfig) *Bot {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Vote == nil {
		cfg.Vote = Random()
	}
	rnd := cfg.Rand
	if rnd == nil {
		rnd = mrand.New(mrand.NewPCG(mrand.Uint64(), mrand.Uint64()))
	}
	return &Bot{
		cfg:         cfg,
		log:         cfg.Log.With("bot", cfg.Name),
		rnd:         rnd,
		vote:        cfg.Vote,
		frames:      make(chan inFrame, 256),
		ctrl:        make(chan func(), 8),
		joinedC:     make(chan struct{}),
		wordC:       make(chan struct{}),
		endC:        make(chan struct{}),
		doneC:       make(chan struct{}),
		cidPrefix:   cfg.Name + "/",
		roster:      make(map[string]*genpb.PlayerInfo),
		frameCounts: make(map[string]int),
		firstStroke: true,
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start dials the server and runs the bot until ctx is cancelled or the socket
// dies for good. It returns as soon as the connection is up; the caller waits
// on Joined for the seat.
func (b *Bot) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)
	if err := b.dial(); err != nil {
		b.cancel()
		close(b.doneC)
		return fmt.Errorf("%s: dial: %w", b.cfg.Name, err)
	}
	go b.run()
	return nil
}

// Stop closes the socket and waits for the bot to finish.
func (b *Bot) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
	<-b.doneC
}

// Done closes when the bot's loop has returned.
func (b *Bot) Done() <-chan struct{} { return b.doneC }

// Joined closes once the seat is granted.
func (b *Bot) Joined() <-chan struct{} { return b.joinedC }

// HasWord closes once this bot has been dealt its word.
func (b *Bot) HasWord() <-chan struct{} { return b.wordC }

// Ended closes once MatchEnded has arrived.
func (b *Bot) Ended() <-chan struct{} { return b.endC }

// PlayerID is valid after Joined has closed.
func (b *Bot) PlayerID() string { return b.pubPlayerID }

// SeatToken is valid after Joined has closed.
func (b *Bot) SeatToken() string { return b.pubSeatToken }

// RoomCode is valid after Joined has closed.
func (b *Bot) RoomCode() string { return b.pubRoomCode }

// IsHost is valid after Joined has closed. It is the flag from the first
// Joined; host migration is reported through PlayerPresence instead.
func (b *Bot) IsHost() bool { return b.pubIsHost }

// Word is valid after HasWord has closed.
func (b *Bot) Word() string { return b.pubWord }

// Name is the configured display name.
func (b *Bot) Name() string { return b.cfg.Name }

// Do runs fn on the bot's own goroutine and waits for it. It is the only safe
// way for the table to touch actor-owned state.
func (b *Bot) Do(fn func(*Bot)) bool {
	ack := make(chan struct{})
	select {
	case b.ctrl <- func() { fn(b); close(ack) }:
	case <-b.doneC:
		return false
	}
	select {
	case <-ack:
		return true
	case <-b.doneC:
		return false
	}
}

// Report is a bot's account of the match it just played.
type Report struct {
	Name     string
	PlayerID string
	Word     string
	// Words is every word this bot was dealt, oldest first — one per round.
	// Word is the last of them.
	Words       []string
	IsHost      bool
	Eliminated  bool
	Ended       *genpb.MatchEnded
	Spectator   *genpb.SpectatorInfo
	Frames      int
	FrameCounts map[string]int
	Strokes     int
	// Dropped is true if this bot killed its own socket at some point, and
	// Resyncs counts the reconnects whose Snapshot it checked against the state
	// it held before the drop.
	Dropped bool
	Resyncs int
	// ClampProbes counts the over-wide brushes this bot sent and saw come back
	// clamped, so the assertion cannot pass by never running.
	ClampProbes int
	// ErrorCodes counts the rejections this bot collected, by ErrorCode name.
	ErrorCodes map[string]int
	// ForeignCids counts frames that arrived carrying another player's
	// correlation id, with one example. See the note on BroadcastReply in the
	// package report: a room-wide event that answers one player's command is
	// stamped with that player's cid for everybody.
	ForeignCids      int
	ForeignCidSample string
	Errors           []string
	Violations       []string
	Leaks            []Leak
	RunErr           error
	Transcript       []*genpb.ServerEvent
}

// Snapshot collects this bot's report. Safe to call once the bot has stopped,
// and also while it is running.
func (b *Bot) Snapshot() Report {
	var rep Report
	ok := b.Do(func(b *Bot) { rep = b.reportLocked() })
	if !ok {
		// The bot is gone; its fields are no longer being written.
		rep = b.reportLocked()
	}
	return rep
}

func (b *Bot) reportLocked() Report {
	counts := make(map[string]int, len(b.frameCounts))
	for k, v := range b.frameCounts {
		counts[k] = v
	}
	errs := make([]string, 0, len(b.errors))
	codes := make(map[string]int, len(b.errors))
	for _, e := range b.errors {
		errs = append(errs, fmt.Sprintf("%s: %s", e.GetCode(), e.GetMessage()))
		codes[e.GetCode().String()]++
	}
	return Report{
		Name:             b.cfg.Name,
		PlayerID:         b.playerID,
		Word:             b.word,
		Words:            slices.Clone(b.ownWords),
		IsHost:           b.isHost,
		Eliminated:       b.eliminated,
		Ended:            b.ended,
		Spectator:        b.spectator,
		Frames:           len(b.transcript),
		FrameCounts:      counts,
		Strokes:          b.strokesClosed,
		Dropped:          b.dropUsed,
		Resyncs:          b.resyncs,
		ClampProbes:      b.clampProbes,
		ErrorCodes:       codes,
		ForeignCids:      b.foreignCids,
		ForeignCidSample: b.foreignCidSample,
		Errors:           errs,
		Violations:       append([]string(nil), b.violations...),
		Leaks:            append([]Leak(nil), b.leaks...),
		RunErr:           b.runErr,
		Transcript:       append([]*genpb.ServerEvent(nil), b.transcript...),
	}
}

// ---------------------------------------------------------------------------
// Directives the table can issue
// ---------------------------------------------------------------------------

// DropSocket kills this bot's connection now. rejoinAfter of zero means it
// never comes back.
func (b *Bot) DropSocket(rejoinAfter time.Duration) {
	b.Do(func(b *Bot) {
		b.rejoinAfter = rejoinAfter
		b.stayDown = rejoinAfter <= 0
		b.dropNow()
	})
}

// RequestSnapshot asks the server for a full resync. Exercises the recovery
// path a client is supposed to take on a stroke-seq gap.
func (b *Bot) RequestSnapshot() {
	b.Do(func(b *Bot) {
		b.send(&genpb.ClientCommand{
			Cid: b.cid("resync"),
			Cmd: &genpb.ClientCommand_RequestSnapshot{
				RequestSnapshot: &genpb.RequestSnapshot{HaveSeq: b.lastSeq},
			},
		})
	})
}

// SetReady sends SetReady.
func (b *Bot) SetReady(ready bool) {
	b.Do(func(b *Bot) {
		b.send(&genpb.ClientCommand{
			Cid: b.cid("ready"),
			Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: ready}},
		})
	})
}

// UpdateSettings sends UpdateSettings. Host only; the server rejects it from
// anyone else and the rejection is recorded like any other.
func (b *Bot) UpdateSettings(s *genpb.MatchSettings) {
	b.Do(func(b *Bot) {
		b.send(&genpb.ClientCommand{
			Cid: b.cid("settings"),
			Cmd: &genpb.ClientCommand_UpdateSettings{
				UpdateSettings: &genpb.UpdateSettings{Settings: s},
			},
		})
	})
}

// StartMatch sends StartMatch.
func (b *Bot) StartMatch() {
	b.Do(func(b *Bot) {
		b.send(&genpb.ClientCommand{
			Cid: b.cid("start"),
			Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}},
		})
	})
}

// Rematch sends Rematch. Host only.
func (b *Bot) Rematch() {
	b.Do(func(b *Bot) {
		b.send(&genpb.ClientCommand{
			Cid: b.cid("rematch"),
			Cmd: &genpb.ClientCommand_Rematch{Rematch: &genpb.Rematch{}},
		})
	})
}

// CanStart reports the last LobbyState.can_start this bot saw.
func (b *Bot) CanStart() bool {
	var ok bool
	b.Do(func(b *Bot) { ok = b.canStart })
	return ok
}

// BotState is a snapshot of where this bot thinks the match is.
type BotState struct {
	Phase      genpb.Phase
	Round      int32
	Players    int
	CanStart   bool
	Eliminated bool
}

// State reads the bot's current view of the room.
func (b *Bot) State() BotState {
	var st BotState
	b.Do(func(b *Bot) {
		st = BotState{
			Phase:      b.phase,
			Round:      b.round,
			Players:    len(b.roster),
			CanStart:   b.canStart,
			Eliminated: b.eliminated,
		}
	})
	return st
}

// ---------------------------------------------------------------------------
// The actor loop
// ---------------------------------------------------------------------------

func (b *Bot) run() {
	defer close(b.doneC)
	defer b.closeSocket(websocket.StatusNormalClosure, "done")

	b.drawTimer = time.NewTicker(room.StrokeBatchWindow)
	defer b.drawTimer.Stop()

	b.dropTimer = newIdleTimer()
	defer b.dropTimer.Stop()
	b.rejoinTimer = newIdleTimer()
	defer b.rejoinTimer.Stop()

	b.sendJoin()

	for {
		select {
		case <-b.ctx.Done():
			return

		case fn := <-b.ctrl:
			fn()

		case f := <-b.frames:
			if f.gen != b.gen {
				continue // a socket we already replaced
			}
			if f.err != nil {
				if !b.onSocketDown(f.err) {
					return
				}
				continue
			}
			b.onEvent(f.ev)

		case <-b.drawTimer.C:
			b.onDrawTick()

		case <-b.dropTimer.C:
			b.dropNow()

		case <-b.rejoinTimer.C:
			b.reconnect()
		}
	}
}

// newIdleTimer returns a stopped timer. Go 1.23 timers need no draining after
// Stop or Reset, so this is safe to re-arm from the select loop.
func newIdleTimer() *time.Timer {
	t := time.NewTimer(time.Hour)
	t.Stop()
	return t
}

func (b *Bot) dial() error {
	ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
	defer cancel()

	ws, _, err := websocket.Dial(ctx, b.cfg.URL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		return err
	}
	ws.SetReadLimit(botReadLimit)
	b.ws = ws
	b.gen++
	go b.readLoop(ws, b.gen)
	return nil
}

func (b *Bot) readLoop(ws *websocket.Conn, gen int) {
	for {
		typ, data, err := ws.Read(b.ctx)
		if err != nil {
			b.post(inFrame{gen: gen, err: err})
			return
		}
		if typ != websocket.MessageBinary {
			b.post(inFrame{gen: gen, err: errors.New("server sent a non-binary frame")})
			return
		}
		ev := &genpb.ServerEvent{}
		if err := proto.Unmarshal(data, ev); err != nil {
			b.post(inFrame{gen: gen, err: fmt.Errorf("decode: %w", err)})
			return
		}
		b.post(inFrame{gen: gen, ev: ev})
	}
}

func (b *Bot) post(f inFrame) {
	select {
	case b.frames <- f:
	case <-b.ctx.Done():
	}
}

func (b *Bot) send(cmd *genpb.ClientCommand) {
	if b.ws == nil {
		return
	}
	data, err := proto.Marshal(cmd)
	if err != nil {
		b.violate("could not marshal %T: %v", cmd.GetCmd(), err)
		return
	}
	ctx, cancel := context.WithTimeout(b.ctx, writeTimeout)
	err = b.ws.Write(ctx, websocket.MessageBinary, data)
	cancel()
	if err != nil && b.ctx.Err() == nil {
		b.log.Debug("write failed", "err", err)
	}
}

func (b *Bot) sendJoin() {
	j := &genpb.JoinRoom{
		RoomCode:        b.cfg.RoomCode,
		DisplayName:     b.cfg.Name,
		ProtocolVersion: room.ProtocolVersion,
	}
	if b.seatToken != "" {
		// A reclaim: the room code is required and the seated name wins, so the
		// display name is not resent.
		j.RoomCode = b.roomCode
		j.SeatToken = b.seatToken
		j.DisplayName = ""
	}
	b.send(&genpb.ClientCommand{Cid: b.cid("join"), Cmd: &genpb.ClientCommand_Join{Join: j}})
}

// closeSocket tears down the current connection, if any.
func (b *Bot) closeSocket(status websocket.StatusCode, reason string) {
	if b.ws == nil {
		return
	}
	if status == websocket.StatusAbnormalClosure {
		_ = b.ws.CloseNow()
	} else {
		_ = b.ws.Close(status, reason)
	}
	b.ws = nil
}

// onSocketDown handles the read side dying. It reports whether the bot should
// keep running.
func (b *Bot) onSocketDown(err error) bool {
	b.ws = nil
	b.drawPen = nil
	b.dropTimer.Stop()

	if b.ctx.Err() != nil {
		return false
	}
	if b.stayDown {
		b.log.Info("socket down, staying down")
		// Keep the loop alive so the table can still collect the transcript.
		return true
	}
	if b.rejoinAfter > 0 {
		b.log.Info("socket down, reconnecting", "after", b.rejoinAfter)
		b.rejoinTimer.Reset(b.rejoinAfter)
		return true
	}
	if b.ended != nil {
		return false
	}
	b.runErr = fmt.Errorf("socket closed before the match ended: %w", err)
	return false
}

// dropNow simulates the connection vanishing: no close handshake, no goodbye.
func (b *Bot) dropNow() {
	if b.ws == nil {
		return
	}
	b.dropUsed = true
	b.expect = &resyncExpect{
		playerID:  b.playerID,
		word:      b.word,
		roomCode:  b.roomCode,
		round:     b.round,
		strokes:   b.strokesClosed,
		settings:  b.settings,
		capturedT: time.Now(),
	}
	b.log.Info("dropping socket", "round", b.round, "phase", b.phase.String())
	b.closeSocket(websocket.StatusAbnormalClosure, "")
}

// reconnect redials and reclaims the seat with the token from the first Joined.
func (b *Bot) reconnect() {
	if b.ws != nil || b.ctx.Err() != nil {
		return
	}
	if err := b.dial(); err != nil {
		b.runErr = fmt.Errorf("reconnect dial: %w", err)
		b.cancel()
		return
	}
	b.rejoinAfter = 0
	b.log.Info("reconnecting with seat token")
	b.sendJoin()
}

// ---------------------------------------------------------------------------
// Event handling
// ---------------------------------------------------------------------------

func (b *Bot) onEvent(ev *genpb.ServerEvent) {
	name := frameName(ev)
	b.frameCounts[name]++
	b.transcript = append(b.transcript, ev)

	// A correlation id is documented as echoing the command that produced the
	// event (game.proto ServerEvent.cid). BroadcastReply stamps one player's cid
	// onto the frame every socket receives, so a client keyed on cid can be
	// handed somebody else's. Counted rather than failed: it is a design
	// question, not a bug this harness gets to rule on.
	if c := ev.GetCid(); c != "" && !strings.HasPrefix(c, b.cidPrefix) {
		b.foreignCids++
		if b.foreignCidSample == "" {
			b.foreignCidSample = name + " cid=" + c
		}
	}

	if b.cfg.Watch != nil {
		// The recipient's own word may arrive in the very frame being inspected,
		// before it has been recorded. Taking the hint from the frame keeps a
		// player's YourWord from reading as a leak of the identical word a
		// player holding the common word was dealt. This applies to every
		// round's deal, not just the first.
		own := b.ownWords
		switch e := ev.GetEvt().(type) {
		case *genpb.ServerEvent_YourWord:
			own = append(slices.Clone(own), e.YourWord.GetWord())
		case *genpb.ServerEvent_Snapshot:
			if e.Snapshot.GetPlayerId() == b.playerID {
				own = append(slices.Clone(own), e.Snapshot.GetYourWord())
			}
		}
		if found := b.cfg.Watch.Inspect(b.playerID, own, ev); len(found) > 0 {
			b.leaks = append(b.leaks, found...)
			for _, l := range found {
				b.violate("SECRET LEAK: %s", l)
			}
		}
	}

	switch e := ev.GetEvt().(type) {
	case *genpb.ServerEvent_Joined:
		b.onJoined(e.Joined)
	case *genpb.ServerEvent_YourWord:
		b.onYourWord(e.YourWord)
	case *genpb.ServerEvent_Snapshot:
		b.onSnapshot(e.Snapshot)
	case *genpb.ServerEvent_LobbyState:
		b.onLobbyState(e.LobbyState)
	case *genpb.ServerEvent_SettingsChanged:
		b.settings = e.SettingsChanged.GetSettings()
	case *genpb.ServerEvent_RoundStarted:
		b.onRoundStarted(e.RoundStarted)
	case *genpb.ServerEvent_TurnStarted:
		b.onTurnStarted(e.TurnStarted)
	case *genpb.ServerEvent_StrokeBegan:
		b.onStrokeBegan(e.StrokeBegan)
	case *genpb.ServerEvent_StrokePoints:
		b.checkSeq("StrokePoints", e.StrokePoints.GetSeq())
	case *genpb.ServerEvent_StrokeEnded:
		b.checkSeq("StrokeEnded", e.StrokeEnded.GetSeq())
		b.strokesClosed++
	case *genpb.ServerEvent_PhaseChanged:
		b.onPhaseChanged(e.PhaseChanged)
	case *genpb.ServerEvent_VoteCastCount:
		b.onVoteCastCount(e.VoteCastCount)
	case *genpb.ServerEvent_VoteTally:
		b.onVoteTally(e.VoteTally)
	case *genpb.ServerEvent_VoteAccepted:
		b.awaitingVote = false
		b.votedRound = e.VoteAccepted.GetRound()
		b.onVoteAccepted()
	case *genpb.ServerEvent_PlayerEliminated:
		b.onPlayerEliminated(e.PlayerEliminated)
	case *genpb.ServerEvent_PlayerPresence:
		b.onPresence(e.PlayerPresence)
	case *genpb.ServerEvent_SpectatorInfo:
		b.spectator = e.SpectatorInfo
	case *genpb.ServerEvent_MatchEnded:
		b.onMatchEnded(e.MatchEnded)
	case *genpb.ServerEvent_Error:
		b.onError(ev.GetCid(), e.Error)
	case nil:
		b.violate("received a ServerEvent with an unset evt oneof")
	default:
		b.violate("received an unknown ServerEvent variant %T", e)
	}
}

func (b *Bot) onJoined(j *genpb.Joined) {
	if v := j.GetProtocolVersion(); v != room.ProtocolVersion {
		b.violate("Joined.protocol_version = %d, want %d", v, room.ProtocolVersion)
	}
	if j.GetSeatToken() == "" {
		b.violate("Joined carried no seat token")
	}
	if b.playerID != "" && j.GetPlayerId() != b.playerID {
		b.violate("reclaimed the seat as %q, was %q", j.GetPlayerId(), b.playerID)
	}
	b.playerID = j.GetPlayerId()
	b.seatToken = j.GetSeatToken()
	b.roomCode = j.GetRoomCode()
	if !j.GetReconnected() {
		b.isHost = j.GetIsHost()
	}
	if b.cfg.Watch != nil {
		b.cfg.Watch.RegisterToken(b.playerID, b.seatToken)
	}
	b.joinedOnce.Do(func() {
		b.pubPlayerID, b.pubSeatToken = b.playerID, b.seatToken
		b.pubRoomCode, b.pubIsHost = b.roomCode, b.isHost
		close(b.joinedC)
	})
}

func (b *Bot) onYourWord(y *genpb.YourWord) {
	w := y.GetWord()
	if w == "" {
		b.violate("YourWord carried an empty word")
		return
	}
	round := y.GetRound()
	if round <= 0 {
		b.violate("YourWord carried round %d, want 1 or more", round)
	}
	// A new word is legitimate only as part of a new round's deal. Within one
	// round the word is fixed, and a second YourWord that changes it is the
	// bug this assertion exists to catch.
	if b.word != "" && b.word != w && round == b.wordRound {
		b.violate("word changed inside round %d from %q to %q", round, b.word, w)
	}
	if b.wordRound != 0 && round < b.wordRound {
		b.violate("YourWord went backwards: round %d after round %d", round, b.wordRound)
	}
	b.setWord(w, round)
	b.wordOnce.Do(func() {
		b.pubWord = w
		close(b.wordC)
	})
}

// setWord records a word the bot legitimately holds, for both the round
// bookkeeping and the leak scanner's exemption set.
func (b *Bot) setWord(w string, round int32) {
	b.word = w
	if round > b.wordRound {
		b.wordRound = round
	}
	if !slices.Contains(b.ownWords, w) {
		b.ownWords = append(b.ownWords, w)
	}
	if b.cfg.Watch != nil {
		b.cfg.Watch.RegisterWord(b.playerID, w)
	}
}

func (b *Bot) onSnapshot(s *genpb.Snapshot) {
	if s.GetPlayerId() != b.playerID {
		b.violate("Snapshot addressed to %q, but this bot is %q", s.GetPlayerId(), b.playerID)
	}
	if w := s.GetYourWord(); w != "" {
		// A Snapshot carries the word for the round it describes. It may
		// legitimately differ from the one in hand only when the snapshot is
		// for a later round, which is what a client that missed a whole
		// round's deal comes back to.
		if b.word != "" && w != b.word && s.GetRound() <= b.wordRound {
			b.violate("Snapshot.your_word = %q for round %d, want this bot's own %q",
				w, s.GetRound(), b.word)
		}
		// A Snapshot names the round in progress; the reveal that deals round n
		// runs while round is still n-1, so take the later of the two rather
		// than trusting either alone.
		b.setWord(w, max(s.GetRound(), b.wordRound))
		b.wordOnce.Do(func() {
			b.pubWord = w
			close(b.wordC)
		})
	}

	b.phase = s.GetPhase()
	b.round = s.GetRound()
	b.totalRounds = s.GetTotalRounds()
	b.settings = s.GetSettings()
	if s.GetYouAreEliminated() {
		// The grace-expiry path eliminates a seat without a PlayerEliminated
		// broadcast — the socket was gone to hear one. A reconnecting spectator
		// learns it here, and the dossier arrives right behind this Snapshot.
		b.eliminated = true
		b.becomeSpectator()
	}
	b.setRoster(s.GetPlayers())
	b.turnOrder = s.GetTurnOrder()
	b.artistID = s.GetArtistId()
	b.eliminated = s.GetYouAreEliminated()
	// The snapshot is the complete state, so it is also the new seq baseline:
	// anything missed while the socket was dark is in this frame, not a gap.
	b.lastSeq = s.GetSeq()
	b.seqSeen = true

	if b.expect != nil {
		b.verifyResync(s)
		b.expect = nil
	}
}

// verifyResync is the milestone-7 assertion, made from the client's side: a bot
// that dropped mid-turn and came back with its seat token must find the same
// seat, the word for whatever round it came back to, and — as long as it is
// still the same round — a canvas that only grew.
//
// Both the word and the canvas are scoped to the round now. A drop that spans a
// round boundary comes back to a different word and a blank sheet, and neither
// is a fault: the round it left no longer exists.
func (b *Bot) verifyResync(s *genpb.Snapshot) {
	e := b.expect
	sameRound := s.GetRound() == e.round
	if s.GetPlayerId() != e.playerID {
		b.violate("resync: player id %q, want %q", s.GetPlayerId(), e.playerID)
	}
	if e.word != "" && sameRound && s.GetYourWord() != e.word {
		b.violate("resync: word %q, want the word this seat held in round %d", s.GetYourWord(), e.round)
	}
	if s.GetRoomCode() != e.roomCode {
		b.violate("resync: room code %q, want %q", s.GetRoomCode(), e.roomCode)
	}
	if s.GetRound() < e.round {
		b.violate("resync: round went backwards, %d < %d", s.GetRound(), e.round)
	}
	if got := len(s.GetStrokes()); sameRound && got < e.strokes {
		b.violate("resync: canvas shrank inside round %d, %d strokes replayed but %d were committed before the drop",
			s.GetRound(), got, e.strokes)
	}
	if e.settings != nil && !proto.Equal(e.settings, s.GetSettings()) {
		b.violate("resync: settings changed across the reconnect")
	}
	if p := b.roster[b.playerID]; p != nil && !p.GetConnected() {
		b.violate("resync: the roster still shows this seat as disconnected")
	}
	b.resyncs++
	b.log.Info("resync verified",
		"gap", time.Since(e.capturedT).Round(time.Millisecond),
		"strokes", len(s.GetStrokes()))
}

func (b *Bot) onLobbyState(l *genpb.LobbyState) {
	b.phase = l.GetPhase()
	b.settings = l.GetSettings()
	b.canStart = l.GetCanStart()
	b.roomCode = l.GetRoomCode()
	b.setRoster(l.GetPlayers())
	if b.cfg.Provoke && !b.provNotHost && b.playerID != "" && !b.isHost {
		// Only the host may edit the settings. The room must answer NOT_HOST.
		b.provNotHost = true
		b.send(&genpb.ClientCommand{Cid: b.cid("provoke-not-host"),
			Cmd: &genpb.ClientCommand_UpdateSettings{UpdateSettings: &genpb.UpdateSettings{
				Settings: room.DefaultSettings(),
			}}})
	}
	if l.GetMinPlayers() != room.MinPlayers || l.GetMaxPlayers() != room.MaxPlayers {
		b.violate("LobbyState advertises %d..%d players, want %d..%d",
			l.GetMinPlayers(), l.GetMaxPlayers(), room.MinPlayers, room.MaxPlayers)
	}
}

func (b *Bot) onRoundStarted(r *genpb.RoundStarted) {
	b.round = r.GetRound()
	b.totalRounds = r.GetTotalRounds()
	b.turnOrder = r.GetTurnOrder()
	b.awaitingVote = false

	if r.GetRound() > r.GetTotalRounds() {
		b.violate("RoundStarted announced round %d of %d: the match is playing past its configured final round",
			r.GetRound(), r.GetTotalRounds())
	}
	if got, want := len(r.GetTurnOrder()), int(r.GetActiveCount()); got != want {
		b.violate("RoundStarted turn_order has %d entries but active_count is %d", got, want)
	}
	seen := make(map[string]bool, len(r.GetTurnOrder()))
	for _, id := range r.GetTurnOrder() {
		if seen[id] {
			b.violate("RoundStarted turn_order names %q twice", id)
		}
		seen[id] = true
	}
}

func (b *Bot) onTurnStarted(t *genpb.TurnStarted) {
	b.artistID = t.GetArtistId()
	b.round = t.GetRound()

	if i := int(t.GetTurnIndex()); i >= 0 && i < len(b.turnOrder) {
		if b.turnOrder[i] != t.GetArtistId() {
			b.violate("TurnStarted names artist %q at index %d, but turn_order has %q there",
				t.GetArtistId(), i, b.turnOrder[i])
		}
	}
	if t.GetRemainingMs() > t.GetDurationMs() {
		b.violate("TurnStarted remaining_ms %d exceeds duration_ms %d",
			t.GetRemainingMs(), t.GetDurationMs())
	}

	if t.GetArtistId() != b.playerID {
		b.drawPen = nil
		if b.cfg.Provoke && !b.provNotArtist {
			// A well-formed stroke from a player whose turn it is not. The room
			// must answer NOT_ARTIST and change nothing
			// (internal/room/strokes.go, authority 1).
			b.provNotArtist = true
			b.send(&genpb.ClientCommand{Cid: b.cid("provoke-not-artist"),
				Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
					ColorIndex: 0, Width: 4, Points: []int32{100, 100},
				}}})
		}
		return
	}
	// My turn.
	b.drawPen = newPen(b.cfg.Draw, b.rnd, b.firstStroke)
	b.log.Info("my turn to draw", "round", t.GetRound(), "ms", t.GetDurationMs())

	b.armDrop(func(d DropPlan) time.Duration { return d.OnMyTurnAfter })
}

// armDrop starts the scripted disconnect countdown, once per match.
func (b *Bot) armDrop(pick func(DropPlan) time.Duration) {
	p := b.cfg.Drop
	if p == nil || b.dropUsed || b.dropArmed {
		return
	}
	d := pick(*p)
	if d <= 0 {
		return
	}
	b.dropArmed = true
	b.rejoinAfter = p.RejoinAfter
	b.stayDown = p.RejoinAfter <= 0
	b.dropTimer.Reset(d)
}

// ScriptDrop installs a disconnect plan after the bot is already running, and
// optionally silences its vote. The table uses it once it knows which seat holds
// the imposter's word, which is not knowable before the deal.
func (b *Bot) ScriptDrop(plan DropPlan, silent bool) {
	b.Do(func(b *Bot) {
		b.cfg.Drop = &plan
		if silent {
			b.vote = NeverVote()
		}
		if b.phase == genpb.Phase_PHASE_DISCUSSION {
			b.armDrop(func(d DropPlan) time.Duration { return d.OnDiscussionAfter })
		}
	})
}

func (b *Bot) onStrokeBegan(s *genpb.StrokeBegan) {
	b.checkSeq("StrokeBegan", s.GetSeq())
	if w := s.GetWidth(); w < room.MinStrokeWidth || w > room.MaxStrokeWidth {
		b.violate("StrokeBegan width %d is outside the server's own clamp %d..%d",
			w, room.MinStrokeWidth, room.MaxStrokeWidth)
	}
	if c := s.GetColorIndex(); c < 0 || c >= room.PaletteSize {
		b.violate("StrokeBegan color_index %d is outside the palette", c)
	}
	if b.clampProbeOpen {
		b.clampProbeOpen = false
		if s.GetWidth() != room.MaxStrokeWidth {
			b.violate("width clamp probe: sent 9999, the broadcast came back as %d, want %d",
				s.GetWidth(), room.MaxStrokeWidth)
		} else {
			b.clampProbes++
			b.log.Info("width clamp confirmed on the wire", "width", s.GetWidth())
		}
	}
}

func (b *Bot) onPhaseChanged(p *genpb.PhaseChanged) {
	prev := b.phase
	b.phase = p.GetPhase()
	b.round = p.GetRound()

	if p.GetRemainingMs() > p.GetDurationMs() {
		b.violate("PhaseChanged remaining_ms %d exceeds duration_ms %d",
			p.GetRemainingMs(), p.GetDurationMs())
	}
	if p.GetPhase() != genpb.Phase_PHASE_DRAWING {
		b.drawPen = nil
	}
	if p.GetPhase() == genpb.Phase_PHASE_DISCUSSION && prev != genpb.Phase_PHASE_DISCUSSION {
		b.castVote(0)
		b.armDrop(func(d DropPlan) time.Duration { return d.OnDiscussionAfter })
	}
}

// castVote asks the strategy and sends its answer. attempt is 0 the first time
// and rises when the server refuses the previous choice.
func (b *Bot) castVote(attempt int) {
	if b.eliminated {
		return // silent spectator (DESIGN.md:66)
	}
	v := View{
		Me:      b.playerID,
		Round:   b.round,
		Active:  b.activeIDs(),
		Rand:    b.rnd,
		Roster:  b.roster,
		Attempt: attempt,
	}
	cv := b.vote.Vote(v)
	if cv == nil {
		b.log.Info("casting no vote at all", "round", b.round, "strategy", b.vote.Name())
		return
	}
	b.voteCid = b.cid(fmt.Sprintf("vote-%d-%d", b.round, attempt))
	b.awaitingVote = true
	b.send(&genpb.ClientCommand{
		Cid: b.voteCid,
		Cmd: &genpb.ClientCommand_CastVote{CastVote: cv},
	})
}

// onVoteAccepted runs the two mid-match provocations that need a vote to have
// landed first: a second vote, which must be refused because a vote is
// irreversible (DESIGN.md:49), and a full resync, which is the recovery path a
// client is told to take on a stroke-seq gap and is otherwise never exercised
// outside a reconnect.
func (b *Bot) onVoteAccepted() {
	if !b.cfg.Provoke {
		return
	}
	if !b.provDupVote {
		b.provDupVote = true
		b.send(&genpb.ClientCommand{Cid: b.cid("provoke-dup-vote"),
			Cmd: &genpb.ClientCommand_CastVote{CastVote: voteSkip()}})
	}
	if !b.provResync {
		b.provResync = true
		b.send(&genpb.ClientCommand{Cid: b.cid("provoke-resync"),
			Cmd: &genpb.ClientCommand_RequestSnapshot{
				RequestSnapshot: &genpb.RequestSnapshot{HaveSeq: b.lastSeq},
			}})
	}
}

func (b *Bot) onVoteCastCount(v *genpb.VoteCastCount) {
	if v.GetVotesCast() > v.GetActiveCount() {
		b.violate("VoteCastCount reports %d votes from %d active players",
			v.GetVotesCast(), v.GetActiveCount())
	}
}

func (b *Bot) onVoteTally(t *genpb.VoteTally) {
	active := t.GetActiveCount()
	skip := t.GetSkipCount()

	// A plurality has no threshold. The field is deprecated and must stay 0, or
	// a client that still reads it starts drawing a line that does not exist.
	if got := t.GetMajorityThreshold(); got != 0 {
		b.violate("VoteTally majority_threshold = %d, want 0 under a plurality", got)
	}

	// `<=`, not `==`: an abstention is counted in neither bucket, so the totals
	// need not reach active_count (DESIGN.md:52). Exceeding it is still a bug.
	total := skip
	for _, c := range t.GetCounts() {
		if c.GetVotes() <= 0 {
			b.violate("VoteTally lists candidate %q with %d votes", c.GetCandidateId(), c.GetVotes())
		}
		total += c.GetVotes()
	}
	if total > active {
		b.violate("VoteTally accounts for %d votes but only %d players were active",
			total, active)
	}

	// Recompute the outcome the rule demands. Strictly ahead of every other
	// candidate and strictly ahead of Skip; any tie for first eliminates
	// nobody, Skip included.
	most, leader, tied := int32(0), "", false
	for _, c := range t.GetCounts() {
		switch n := c.GetVotes(); {
		case n > most:
			most, leader, tied = n, c.GetCandidateId(), false
		case n == most:
			tied = true
		}
	}
	if tied || most <= skip {
		leader = ""
	}
	b.pluralityWinner = leader
	b.sawTally = true
}

func (b *Bot) onPlayerEliminated(e *genpb.PlayerEliminated) {
	// Cross-check the server against the tally it just published. Only when a
	// tally was actually seen: an elimination can also arrive on a path that
	// never held a vote.
	if b.sawTally {
		got := ""
		if e.GetEliminated() {
			got = e.GetPlayerId()
		}
		if got != b.pluralityWinner {
			b.violate("PlayerEliminated names %q but the tally's plurality winner was %q",
				got, b.pluralityWinner)
		}
		b.sawTally = false
	}
	if !e.GetEliminated() {
		if e.GetPlayerId() != "" {
			b.violate("PlayerEliminated says nobody went but names %q", e.GetPlayerId())
		}
		if e.GetWasImposter() {
			b.violate("PlayerEliminated says nobody went but was_imposter is true")
		}
		if e.GetAlignmentRevealed() {
			b.violate("PlayerEliminated says nobody went but alignment_revealed is true")
		}
		return
	}
	// A verdict may never ride on a flag the room did not license
	// (game.proto PlayerEliminated).
	if e.GetWasImposter() && !e.GetAlignmentRevealed() {
		b.violate("PlayerEliminated sets was_imposter with alignment_revealed clear")
	}
	// Under Hidden the only elimination allowed to disclose an alignment is the
	// one that ends the match, and MatchEnded arrives in the same resolution.
	if b.settings.GetEliminationResults() == genpb.EliminationResults_ELIMINATION_RESULTS_HIDDEN &&
		e.GetAlignmentRevealed() && b.ended == nil {
		b.hiddenDisclosure = true
	}
	if p := b.roster[e.GetPlayerId()]; p != nil {
		p.Eliminated = true
	}
	if e.GetPlayerId() == b.playerID {
		b.eliminated = true
		b.drawPen = nil
		b.becomeSpectator()
	}
	if e.GetWasImposter() {
		b.sawImposter = true
		b.impostersCaught++
	}
}

func (b *Bot) onPresence(p *genpb.PlayerPresence) {
	info := p.GetPlayer()
	if info == nil {
		b.violate("PlayerPresence carried no player")
		return
	}
	b.roster[info.GetId()] = info
	if info.GetId() == b.playerID {
		b.isHost = info.GetIsHost()
		b.eliminated = info.GetEliminated()
	}
	if info.GetConnected() && p.GetGraceSecondsRemaining() != 0 {
		b.violate("PlayerPresence reports %q connected with %d grace seconds left",
			info.GetId(), p.GetGraceSecondsRemaining())
	}
}

func (b *Bot) onMatchEnded(m *genpb.MatchEnded) {
	b.ended = m
	b.phase = genpb.Phase_PHASE_ENDED
	b.drawPen = nil

	if m.GetWinner() == genpb.WinnerSide_WINNER_SIDE_UNSPECIFIED &&
		m.GetReason() != genpb.MatchEndReason_MATCH_END_REASON_ABANDONED {
		b.violate("MatchEnded has no winner but reason is %s", m.GetReason())
	}
	if m.GetReason() == genpb.MatchEndReason_MATCH_END_REASON_UNSPECIFIED {
		b.violate("MatchEnded carried no reason")
	}
	if m.GetCommonWord() == "" || m.GetImposterWord() == "" {
		b.violate("MatchEnded did not reveal both words")
	}
	if m.GetCommonWord() == m.GetImposterWord() {
		b.violate("MatchEnded revealed the same word twice: %q", m.GetCommonWord())
	}

	// A Hidden match may only ever disclose an alignment in the resolution that
	// also ends it, and by then MatchEnded is publishing every alignment anyway
	// (MULTIPLE_IMPOSTERS.md, "Elimination Results"). hiddenDisclosure is set
	// only for a frame that arrived while b.ended was still nil.
	if b.hiddenDisclosure && m.GetWinner() != genpb.WinnerSide_WINNER_SIDE_GROUP {
		b.violate("Hidden elimination results disclosed an alignment in a match that ended %s",
			m.GetWinner())
	}

	// Catching every imposter is the group's only win by vote. Catching some of
	// them and then losing is legal with two, so the invariant is about the
	// count rather than about having seen one at all: a group win by vote must
	// have taken all of them out, and only IMPOSTER_ELIMINATED is that path.
	want := int(b.settings.GetImposterCount())
	if want == 0 {
		want = 1
	}
	if m.GetReason() == genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_ELIMINATED &&
		b.settings.GetEliminationResults() != genpb.EliminationResults_ELIMINATION_RESULTS_HIDDEN &&
		b.impostersCaught != want {
		b.violate("the group won by elimination having caught %d of %d imposters",
			b.impostersCaught, want)
	}
	if b.sawImposter && b.impostersCaught == want &&
		m.GetWinner() != genpb.WinnerSide_WINNER_SIDE_GROUP {
		b.violate("every imposter was eliminated but the match ended %s", m.GetWinner())
	}

	b.checkRoundWords(m)

	named := make(map[string]bool, len(m.GetImposterPlayerIds()))
	for _, id := range m.GetImposterPlayerIds() {
		named[id] = true
	}
	if len(named) != len(m.GetImposterPlayerIds()) {
		b.violate("imposter_player_ids repeats a seat: %v", m.GetImposterPlayerIds())
	}
	if len(named) != want {
		b.violate("imposter_player_ids names %d seats but the match was set to %d imposters",
			len(named), want)
	}

	imposters := 0
	mine := ""
	for _, rv := range m.GetReveals() {
		if rv.GetWasImposter() {
			imposters++
			if !named[rv.GetPlayerId()] {
				b.violate("reveal marks %q as an imposter but imposter_player_ids is %v",
					rv.GetPlayerId(), m.GetImposterPlayerIds())
			}
		} else if named[rv.GetPlayerId()] {
			b.violate("imposter_player_ids names %q but their reveal row is not marked",
				rv.GetPlayerId())
		}

		// PlayerReveal.word is the word from the LAST round this seat was dealt
		// into, which is the final round only for players who were still
		// standing. Someone eliminated in round 1 is legitimately revealed
		// holding round 1's word, so the headline pair is the wrong thing to
		// check it against — the per-round column is.
		last := ""
		for _, w := range rv.GetWords() {
			if w != "" {
				last = w
			}
		}
		if rv.GetWord() != last {
			b.violate("player %q is revealed holding %q, but their last per-round word is %q",
				rv.GetPlayerId(), rv.GetWord(), last)
		}
		if !rv.GetEliminated() && rv.GetWord() != m.GetCommonWord() && !rv.GetWasImposter() {
			b.violate("surviving player %q holds %q, which is not the final round's common word",
				rv.GetPlayerId(), rv.GetWord())
		}

		// Per-round column. Every entry must be one of that round's two words,
		// on the correct side of the pair, or blank for a round this seat had
		// already been eliminated out of.
		if got, want := len(rv.GetWords()), len(m.GetRounds()); got != want {
			b.violate("player %q has %d per-round words but the match played %d rounds",
				rv.GetPlayerId(), got, want)
		} else {
			for i, w := range rv.GetWords() {
				if w == "" {
					continue
				}
				rw := m.GetRounds()[i]
				want := rw.GetCommonWord()
				if rv.GetWasImposter() {
					want = rw.GetImposterWord()
				}
				if w != want {
					b.violate("player %q holds %q in round %d, want %q",
						rv.GetPlayerId(), w, rw.GetRound(), want)
				}
			}
		}

		if rv.GetPlayerId() == b.playerID {
			mine = rv.GetWord()
			b.checkOwnRoundWords(rv)
		}
	}
	if imposters != want {
		b.violate("MatchEnded marked %d players as imposters, want exactly %d", imposters, want)
	}
	if b.word != "" && mine != "" && mine != b.word {
		b.violate("the final reveal gives this bot %q, but it was dealt %q", mine, b.word)
	}
	if b.word != "" && mine == "" {
		b.violate("the final reveal has no row for this bot")
	}

	b.log.Info("match ended",
		"winner", m.GetWinner().String(), "reason", m.GetReason().String(),
		"rounds", m.GetRoundsPlayed())
	b.endOnce.Do(func() { close(b.endC) })
}

// checkRoundWords verifies MatchEnded.rounds against itself: one entry per
// round played, numbered in order, each a real pair, and no word reused across
// rounds. That last one is the round-independence guarantee — a player whose
// word repeats while the pairing moves has learned they hold the common word
// (internal/words: Deck.Pair avoid set).
func (b *Bot) checkRoundWords(m *genpb.MatchEnded) {
	rounds := m.GetRounds()
	if got, want := len(rounds), int(m.GetRoundsPlayed()); got != want {
		b.violate("MatchEnded carries %d round pairs but reports %d rounds played", got, want)
	}

	seen := make(map[string]int32, 2*len(rounds))
	for i, rw := range rounds {
		if got, want := rw.GetRound(), int32(i+1); got != want {
			b.violate("MatchEnded.rounds[%d] is numbered %d, want %d", i, got, want)
		}
		c, o := rw.GetCommonWord(), rw.GetImposterWord()
		if c == "" || o == "" {
			b.violate("round %d did not reveal both words", rw.GetRound())
			continue
		}
		if c == o {
			b.violate("round %d revealed the same word twice: %q", rw.GetRound(), c)
		}
		for _, w := range []string{c, o} {
			if prev, dup := seen[w]; dup {
				b.violate("%q was dealt in both round %d and round %d", w, prev, rw.GetRound())
			}
			seen[w] = rw.GetRound()
		}
	}

	// The headline pair must be the last round's: that is the one that was live
	// when the match ended.
	if n := len(rounds); n > 0 {
		last := rounds[n-1]
		if m.GetCommonWord() != last.GetCommonWord() || m.GetImposterWord() != last.GetImposterWord() {
			b.violate("MatchEnded headline pair does not match the final round's")
		}
	}
}

// checkOwnRoundWords cross-checks this bot's own reveal row against what it was
// actually dealt. Every word it received in a YourWord must appear in its row,
// and the row's last non-empty entry must be the word it is still holding.
func (b *Bot) checkOwnRoundWords(rv *genpb.PlayerReveal) {
	for _, w := range b.ownWords {
		if !slices.Contains(rv.GetWords(), w) {
			b.violate("this bot was dealt %q but its reveal row does not contain it", w)
		}
	}
	last := ""
	for _, w := range rv.GetWords() {
		if w != "" {
			last = w
		}
	}
	if b.word != "" && last != "" && last != b.word {
		b.violate("this bot's last per-round word is %q, but it is holding %q", last, b.word)
	}
}

func (b *Bot) onError(cid string, e *genpb.Error) {
	b.errors = append(b.errors, e)
	b.log.Debug("server refused a command", "cid", cid, "code", e.GetCode().String(), "msg", e.GetMessage())

	// A vote for somebody whose socket dropped in the same instant is refused,
	// because a disconnected player is not an eligible candidate. A real client
	// has to notice and fall back, so the bot does too.
	if b.awaitingVote && cid == b.voteCid {
		b.awaitingVote = false
		switch e.GetCode() {
		case genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND, genpb.ErrorCode_ERROR_CODE_NOT_ACTIVE:
			b.send(&genpb.ClientCommand{
				Cid: b.voteCid + "-skip",
				Cmd: &genpb.ClientCommand_CastVote{CastVote: voteSkip()},
			})
		}
	}
}

// checkSeq enforces the one ordering guarantee the stroke relay makes: seq is
// monotonic for the life of the room, and a gap means resync
// (game.proto StrokePoints).
func (b *Bot) checkSeq(what string, seq int32) {
	if !b.seqSeen {
		b.lastSeq = seq
		b.seqSeen = true
		return
	}
	switch {
	case seq == b.lastSeq+1:
	case seq <= b.lastSeq:
		b.violate("%s seq went backwards: %d after %d", what, seq, b.lastSeq)
	default:
		// A real gap. This is exactly the case the protocol tells a client to
		// recover from, so recover from it.
		b.log.Info("stroke seq gap, requesting a snapshot", "have", b.lastSeq, "got", seq)
		b.send(&genpb.ClientCommand{
			Cid: b.cid("resync-gap"),
			Cmd: &genpb.ClientCommand_RequestSnapshot{
				RequestSnapshot: &genpb.RequestSnapshot{HaveSeq: b.lastSeq},
			},
		})
	}
	b.lastSeq = seq
}

// ---------------------------------------------------------------------------
// Drawing
// ---------------------------------------------------------------------------

// onDrawTick fires every room.StrokeBatchWindow. It sends at most one stroke
// command, which is what makes the batch window a property of the harness
// rather than a comment in it.
func (b *Bot) onDrawTick() {
	if b.drawPen == nil || b.ws == nil {
		return
	}
	if b.phase != genpb.Phase_PHASE_DRAWING || b.artistID != b.playerID {
		b.drawPen = nil
		return
	}
	stage, pts, color, width := b.drawPen.step()
	switch stage {
	case stageBegin:
		if width > room.MaxStrokeWidth {
			b.clampProbeOpen = true
		}
		b.firstStroke = false
		b.send(&genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeBegin{
			StrokeBegin: &genpb.StrokeBegin{ColorIndex: color, Width: width, Points: pts},
		}})
	case stagePoints:
		b.send(&genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokePoints{
			// stroke_id and seq are deliberately left zero: the server resolves
			// the open stroke itself and overwrites both (game.proto
			// StrokePoints). A client that filled them in would be trusting
			// values it is told never to read back.
			StrokePoints: &genpb.StrokePoints{Points: pts},
		}})
	case stageEnd:
		b.send(&genpb.ClientCommand{Cmd: &genpb.ClientCommand_StrokeEnd{
			StrokeEnd: &genpb.StrokeEnd{Points: pts},
		}})
	case stageIdle:
		if b.drawPen.done() {
			b.drawPen = nil
		}
	}
}

// ---------------------------------------------------------------------------
// Roster
// ---------------------------------------------------------------------------

func (b *Bot) setRoster(players []*genpb.PlayerInfo) {
	b.roster = make(map[string]*genpb.PlayerInfo, len(players))
	b.seatOrder = b.seatOrder[:0]
	for _, p := range players {
		b.roster[p.GetId()] = p
		b.seatOrder = append(b.seatOrder, p.GetId())
		if p.GetId() == b.playerID {
			b.eliminated = p.GetEliminated()
			b.isHost = p.GetIsHost()
		}
	}
}

// activeIDs is this bot's own view of who may be voted for: connected, seated
// and not eliminated (DESIGN.md "Active players").
func (b *Bot) activeIDs() []string {
	out := make([]string, 0, len(b.seatOrder))
	for _, id := range b.seatOrder {
		p := b.roster[id]
		if p == nil || !p.GetConnected() || p.GetEliminated() {
			continue
		}
		out = append(out, id)
	}
	return out
}

// cid namespaces a correlation id to this bot.
func (b *Bot) cid(s string) string { return b.cidPrefix + s }

func (b *Bot) violate(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	b.violations = append(b.violations, b.cfg.Name+": "+msg)
	b.log.Error("protocol violation", "detail", msg)
}

// becomeSpectator tells the shared watchdog this seat is out, so the dossier
// that follows is judged as sanctioned rather than as a leak. Idempotent.
func (b *Bot) becomeSpectator() {
	if b.cfg.Watch != nil {
		b.cfg.Watch.RegisterSpectator(b.playerID)
	}
}
