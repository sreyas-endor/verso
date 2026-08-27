package main

// table.go — the match driver.
//
// A table seats N bots in one room, walks them from the lobby to the final
// reveal, and then cross-examines what every socket actually received. The bots
// themselves are ordinary clients; everything omniscient lives here, because a
// player who could work out who the imposter is would be a bug rather than a
// feature.
//
// What the table knows that no bot does: every word. It uses that for exactly
// two things — pointing the gang-up strategy at the imposter, and re-sweeping
// every recorded transcript at the end with the complete secret set, which
// closes the window in which an early frame arrived before the last bot had
// registered its word.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	mrand "math/rand/v2"

	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/room"
)

// MatchPlan describes one scripted match.
type MatchPlan struct {
	// Name labels the run in reports.
	Name string
	// Players is the seat count, 3..10.
	Players int
	// Settings is what the host requests. The server clamps it.
	Settings *genpb.MatchSettings
	// Strategy is the vote strategy every bot uses: random, skip, self, gang,
	// silent. "gang" points every bot at the imposter, which is how the group
	// win is driven.
	Strategy string
	// GangTarget selects who the "gang" strategy converges on: "imposter"
	// (default) drives the group win, "non-imposter" drives the branch where a
	// spectator privately learns the imposter's identity and the match continues
	// (DESIGN.md:67).
	GangTarget string
	// Silent, when non-empty, overrides Strategy for the bots at these indices
	// with NeverVote, so the discussion timer has to expire on its own.
	Silent []int
	// Draw is the drawing plan for every bot.
	Draw DrawPlan
	// DropImposter disconnects the imposter's socket for good as soon as the
	// words are dealt (DESIGN.md:125).
	DropImposter bool
	// DropMidTurn, when set, scripts the bot at index DropMidTurnSeat to kill
	// its socket during its own drawing turn and reclaim the seat afterwards.
	DropMidTurn *DropPlan
	// DropMidTurnSeat is the seat index the drop plan applies to. Seat 0 is the
	// host, so 1 is the usual choice.
	DropMidTurnSeat int
	// DropImposterAtVote scripts the imposter — whichever seat that turns out to
	// be — to fall silent and drop this far into the voting window, then come
	// back. See DropPlan.OnDiscussionAfter for what it reproduces.
	DropImposterAtVote *DropPlan
	// Rematch sends Rematch once the match is over and checks the room comes
	// back as a startable lobby with every seat still in it (DESIGN.md:81).
	Rematch bool
	// Provoke makes every bot send a few commands it knows will be refused, so
	// the Error frame is on the wire and gets scanned like every other frame.
	Provoke bool
	// Timeout bounds the whole match. Zero picks a bound from the settings.
	Timeout time.Duration
	// Logger receives table lines. Nil discards.
	Logger *slog.Logger
	// Seed makes a run reproducible on the client side. The server has its own
	// generator.
	Seed uint64
}

// MatchResult is everything the table observed.
type MatchResult struct {
	Plan         MatchPlan
	Code         string
	Reports      []Report
	Winner       genpb.WinnerSide
	Reason       genpb.MatchEndReason
	RoundsPlayed int32
	// CommonWord and ImposterWord are the FINAL round's pair. Rounds carries
	// every one of them, oldest first.
	CommonWord   string
	ImposterWord string
	Rounds       []*genpb.RoundWords
	ImposterID   string
	ImposterName string
	Words        map[string]string
	Duration     time.Duration
	Frames       int
	Strokes      int
	FrameTypes   map[string]int
	Violations   []string
	Leaks        []Leak
}

// OK reports whether the match ended cleanly with nothing to answer for.
func (r *MatchResult) OK() bool {
	return len(r.Violations) == 0 && len(r.Leaks) == 0 &&
		r.Reason != genpb.MatchEndReason_MATCH_END_REASON_UNSPECIFIED
}

// Summary is a one-block human-readable account.
func (r *MatchResult) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d players, room %s, %s in %s\n",
		r.Plan.Name, r.Plan.Players, r.Code,
		strings.TrimPrefix(r.Winner.String(), "WINNER_SIDE_"), r.Duration.Round(time.Millisecond))
	fmt.Fprintf(&b, "  reason        %s after %d round(s)\n",
		strings.TrimPrefix(r.Reason.String(), "MATCH_END_REASON_"), r.RoundsPlayed)
	fmt.Fprintf(&b, "  imposter      %s (%s), the same seat every round\n", r.ImposterName, r.ImposterID)
	// One pair per round: the deal is per round, and printing only the last
	// would hide three quarters of a four-round match.
	for _, rw := range r.Rounds {
		fmt.Fprintf(&b, "    round %d     imposter %q, everyone else %q\n",
			rw.GetRound(), rw.GetImposterWord(), rw.GetCommonWord())
	}
	fmt.Fprintf(&b, "  traffic       %d frames, %d strokes committed, %d frame types\n",
		r.Frames, r.Strokes, len(r.FrameTypes))
	types := make([]string, 0, len(r.FrameTypes))
	for k := range r.FrameTypes {
		types = append(types, k)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Fprintf(&b, "                  %-18s %d\n", t, r.FrameTypes[t])
	}
	if len(r.Leaks) > 0 {
		fmt.Fprintf(&b, "  LEAKS         %d\n", len(r.Leaks))
		for _, l := range r.Leaks {
			fmt.Fprintf(&b, "                  %s\n", l)
		}
	}
	if len(r.Violations) > 0 {
		fmt.Fprintf(&b, "  VIOLATIONS    %d\n", len(r.Violations))
		for _, v := range r.Violations {
			fmt.Fprintf(&b, "                  %s\n", v)
		}
	}
	return b.String()
}

// target is a race-free box for the id the gang-up strategy votes for. Bots
// read it from their own goroutines; the table writes it once.
type target struct{ v atomic.Pointer[string] }

func (t *target) set(id string) { t.v.Store(&id) }
func (t *target) get() string {
	if p := t.v.Load(); p != nil {
		return *p
	}
	return ""
}

// RunMatch plays one scripted match against the server at url.
func RunMatch(ctx context.Context, url string, plan MatchPlan) (*MatchResult, error) {
	if plan.Players < room.MinPlayers || plan.Players > room.MaxPlayers {
		return nil, fmt.Errorf("%d players, want %d..%d", plan.Players, room.MinPlayers, room.MaxPlayers)
	}
	log := plan.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	settings := room.ClampSettings(plan.Settings)
	timeout := plan.Timeout
	if timeout == 0 {
		timeout = defaultTimeout(plan.Players, settings, plan.DropImposter)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	seed := plan.Seed
	if seed == 0 {
		seed = mrand.Uint64()
	}
	watch := NewWatchdog()
	tgt := &target{}
	started := time.Now()

	silent := make(map[int]bool, len(plan.Silent))
	for _, i := range plan.Silent {
		silent[i] = true
	}

	bots := make([]*Bot, plan.Players)
	defer func() {
		for _, b := range bots {
			if b != nil {
				b.Stop()
			}
		}
	}()

	newCfg := func(i int, code string) BotConfig {
		strategy := plan.Strategy
		if silent[i] {
			strategy = "silent"
		}
		vs, err := StrategyByName(strategy, tgt.get)
		if err != nil {
			vs = Random()
		}
		cfg := BotConfig{
			Name:     fmt.Sprintf("bot-%02d", i),
			URL:      url,
			RoomCode: code,
			Vote:     vs,
			Draw:     plan.Draw,
			Provoke:  plan.Provoke,
			Watch:    watch,
			Log:      log,
			Rand:     mrand.New(mrand.NewPCG(seed, uint64(i)+1)),
		}
		if plan.DropMidTurn != nil && i == plan.DropMidTurnSeat {
			cfg.Drop = plan.DropMidTurn
		}
		return cfg
	}

	// The host creates the room by joining with no code.
	host := NewBot(newCfg(0, ""))
	bots[0] = host
	if err := host.Start(ctx); err != nil {
		return nil, err
	}
	if err := waitClosed(ctx, host.Joined(), "the host to be seated"); err != nil {
		return nil, err
	}
	code := host.RoomCode()
	if code == "" {
		return nil, errors.New("the host was seated without a room code")
	}
	if !host.IsHost() {
		return nil, errors.New("the room's creator was not made host")
	}
	log.Info("room created", "code", code, "players", plan.Players)

	host.UpdateSettings(settings)

	for i := 1; i < plan.Players; i++ {
		g := NewBot(newCfg(i, code))
		bots[i] = g
		if err := g.Start(ctx); err != nil {
			return nil, err
		}
		if err := waitClosed(ctx, g.Joined(), "a guest to be seated"); err != nil {
			return nil, err
		}
	}

	for _, b := range bots {
		b.SetReady(true)
	}
	if err := waitUntil(ctx, 25*time.Millisecond, "every seat to be ready", host.CanStart); err != nil {
		return nil, err
	}

	host.StartMatch()

	for _, b := range bots {
		if err := waitClosed(ctx, b.HasWord(), "the words to be dealt"); err != nil {
			return nil, err
		}
	}

	// One resync from a phase that has no strokes and no votes in it. The
	// answer is always the complete state, never a delta
	// (IMPLEMENTATION_PLAN.md §4.6), and the bot checks the Snapshot it gets
	// back names it and carries its own word and nobody else's.
	host.RequestSnapshot()

	words := make(map[string]string, plan.Players)
	for _, b := range bots {
		words[b.PlayerID()] = b.Word()
	}
	imposterID, common, imposter, err := findImposter(words)
	if err != nil {
		return nil, err
	}
	switch plan.GangTarget {
	case "", "imposter":
		tgt.set(imposterID)
	case "non-imposter":
		for _, b := range bots {
			if b.PlayerID() != imposterID {
				tgt.set(b.PlayerID())
				break
			}
		}
	default:
		return nil, fmt.Errorf("unknown gang target %q (imposter, non-imposter)", plan.GangTarget)
	}
	log.Info("words dealt", "imposter", imposterID, "gang target", tgt.get())

	// Bots that will still be here at the end. The imposter's socket is about to
	// vanish for good in the disconnect scenario, so it never sees MatchEnded.
	finishers := make([]*Bot, 0, len(bots))
	for _, b := range bots {
		if plan.DropImposter && b.PlayerID() == imposterID {
			continue
		}
		finishers = append(finishers, b)
	}

	if plan.DropImposterAtVote != nil {
		for _, b := range bots {
			if b.PlayerID() == imposterID {
				log.Info("scripting the imposter to drop during the voting window")
				b.ScriptDrop(*plan.DropImposterAtVote, true)
			}
		}
	}

	if plan.DropImposter {
		for _, b := range bots {
			if b.PlayerID() == imposterID {
				log.Info("dropping the imposter for good", "player", imposterID)
				b.DropSocket(0)
			}
		}
	}

	for _, b := range finishers {
		if err := waitClosed(ctx, b.Ended(), "the match to end for "+b.Name()); err != nil {
			return nil, fmt.Errorf("%w (room %s)", err, code)
		}
	}

	if plan.Rematch {
		if err := runRematch(ctx, bots, host, plan, log); err != nil {
			return nil, err
		}
	}

	res := &MatchResult{
		Plan:       plan,
		Code:       code,
		Words:      words,
		ImposterID: imposterID,
		Duration:   time.Since(started),
		FrameTypes: map[string]int{},
	}

	var canonical *genpb.MatchEnded
	for _, b := range bots {
		rep := b.Snapshot()
		res.Reports = append(res.Reports, rep)
		res.Frames += rep.Frames
		res.Strokes += rep.Strokes
		for k, v := range rep.FrameCounts {
			res.FrameTypes[k] += v
		}
		res.Violations = append(res.Violations, rep.Violations...)
		if rep.RunErr != nil {
			res.Violations = append(res.Violations, rep.Name+": "+rep.RunErr.Error())
		}
		if rep.Ended == nil {
			continue
		}
		if canonical == nil {
			canonical = rep.Ended
			continue
		}
		if !proto.Equal(canonical, rep.Ended) {
			res.Violations = append(res.Violations,
				fmt.Sprintf("%s received a different MatchEnded from the other sockets", rep.Name))
		}
	}
	if canonical == nil {
		return res, errors.New("no socket received MatchEnded")
	}

	res.Winner = canonical.GetWinner()
	res.Reason = canonical.GetReason()
	res.RoundsPlayed = canonical.GetRoundsPlayed()
	res.CommonWord = canonical.GetCommonWord()
	res.ImposterWord = canonical.GetImposterWord()
	res.Rounds = canonical.GetRounds()

	if canonical.GetImposterPlayerId() != imposterID {
		res.Violations = append(res.Violations, fmt.Sprintf(
			"the final reveal names %q as the imposter, but the minority word was dealt to %q",
			canonical.GetImposterPlayerId(), imposterID))
	}
	// `common` and `imposter` were captured from the sockets at the FIRST deal,
	// before round 1. Every round deals a fresh pair, so the headline on
	// MatchEnded is the final round's and cannot be compared against them —
	// rounds[0] is the one that has to agree. A single-round match makes the
	// two the same thing.
	firstCommon, firstImposter := canonical.GetCommonWord(), canonical.GetImposterWord()
	if rs := canonical.GetRounds(); len(rs) > 0 {
		firstCommon, firstImposter = rs[0].GetCommonWord(), rs[0].GetImposterWord()
	}
	if firstCommon != common || firstImposter != imposter {
		res.Violations = append(res.Violations, fmt.Sprintf(
			"round 1 of the final reveal says common=%q imposter=%q, but the sockets were dealt common=%q imposter=%q",
			firstCommon, firstImposter, common, imposter))
	}
	if n := len(canonical.GetReveals()); n != plan.Players {
		res.Violations = append(res.Violations,
			fmt.Sprintf("the final reveal has %d rows for %d players", n, plan.Players))
	}
	for _, rv := range canonical.GetReveals() {
		if rv.GetPlayerId() == imposterID {
			res.ImposterName = rv.GetName()
		}
	}

	// The complete sweep. Inspect ran on every frame as it arrived, but only
	// against the secrets registered by that moment; this re-runs it now that
	// every word is known.
	for _, rep := range res.Reports {
		if leaks := watch.SweepAll(rep.PlayerID, rep.Words, rep.Transcript); len(leaks) > 0 {
			res.Leaks = append(res.Leaks, leaks...)
		}
	}
	res.Leaks = dedupeLeaks(res.Leaks)
	for _, l := range res.Leaks {
		res.Violations = append(res.Violations, "SECRET LEAK: "+l.String())
	}

	return res, nil
}

// runRematch returns the room to the lobby and checks it is playable again.
//
// This is not decoration. resetToLobby has to collect any seat that went dark
// during the match, because a seat that can never be Ready again makes every
// future rematch in that room unstartable — so "the lobby is startable" is the
// assertion that matters, not "the phase changed".
func runRematch(ctx context.Context, bots []*Bot, host *Bot, plan MatchPlan, log *slog.Logger) error {
	host.Rematch()
	if err := waitUntil(ctx, 25*time.Millisecond, "the room to return to the lobby", func() bool {
		st := host.State()
		return st.Phase == genpb.Phase_PHASE_LOBBY && st.Players == plan.Players
	}); err != nil {
		return err
	}
	for _, b := range bots {
		if st := b.State(); st.Eliminated {
			return fmt.Errorf("%s is still eliminated after the rematch", b.Name())
		}
		b.SetReady(true)
	}
	if err := waitUntil(ctx, 25*time.Millisecond, "the rematched lobby to become startable", host.CanStart); err != nil {
		return err
	}
	log.Info("rematch lobby is startable again", "players", plan.Players)
	return nil
}

// findImposter reduces the dealt words to the one seat that differs.
func findImposter(words map[string]string) (id, common, imposter string, err error) {
	counts := make(map[string]int, 2)
	for _, w := range words {
		counts[w]++
	}
	if len(counts) != 2 {
		return "", "", "", fmt.Errorf("%d distinct words were dealt, want exactly 2", len(counts))
	}
	for w, n := range counts {
		if n == 1 {
			imposter = w
		} else {
			common = w
		}
	}
	if imposter == "" || common == "" {
		return "", "", "", fmt.Errorf("the deal was %v, which has no single minority word", counts)
	}
	for pid, w := range words {
		if w == imposter {
			id = pid
		}
	}
	return id, common, imposter, nil
}

func dedupeLeaks(in []Leak) []Leak {
	seen := make(map[Leak]bool, len(in))
	out := make([]Leak, 0, len(in))
	for _, l := range in {
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// defaultTimeout is a generous bound derived from the settings, so a plan does
// not have to state one. Every term is a real cost: the private-word reveal,
// the intermission handoff before each drawing turn AND before the voting
// window, one drawing turn per player per round, the discussion window, the
// vote-result screen, and — when the imposter is scripted to vanish — the 60 s
// reconnect grace window that has to expire before the group can be awarded
// the match.
func defaultTimeout(players int, s *genpb.MatchSettings, dropImposter bool) time.Duration {
	rounds := time.Duration(s.GetMaxRounds())
	// players handoffs, one per turn, plus one more before voting opens.
	intermissions := time.Duration(players+1) *
		time.Duration(s.GetIntermissionSeconds()) * time.Second
	perRound := time.Duration(players)*time.Duration(s.GetDrawSeconds())*time.Second +
		intermissions +
		time.Duration(s.GetDiscussSeconds())*time.Second +
		room.ResolveDuration
	total := room.AssignDuration + rounds*perRound
	if dropImposter {
		total += room.GraceWindow
	}
	// Handshakes, scheduling and a healthy margin.
	return total + 45*time.Second
}

// waitClosed blocks until c closes or ctx expires.
func waitClosed(ctx context.Context, c <-chan struct{}, what string) error {
	select {
	case <-c:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for %s: %w", what, ctx.Err())
	}
}

// waitUntil polls cond until it is true or ctx expires.
func waitUntil(ctx context.Context, every time.Duration, what string, cond func() bool) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if cond() {
			return nil
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s: %w", what, ctx.Err())
		}
	}
}
