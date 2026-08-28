package room

// harness_test.go — the shared driver for milestones 5, 6 and 7.
//
// Every test in this package runs a real room actor with no network, no
// browser and no clock: testing/synctest gives each test its own bubble with
// its own fake clock, so a ten-minute match with 10 players resolves in
// microseconds of wall time and every deadline lands on an exact instant.
//
// The harness only ever reaches room state through smokeGet, which hops onto
// the actor goroutine the way Seat and Detach do. A test that read r.phase
// directly would be a data race, and the whole point of the actor design is
// that -race can prove there is none.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"sort"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// pairDeck is a fixed deck. The pair is constant so a test can name the two
// words, but which SIDE becomes the common word is still drawn from r.rnd —
// that coin flip is part of what these tests exercise (DESIGN.md:33).
type pairDeck struct{ a, b string }

func (d pairDeck) Pair(genpb.Difficulty, *mrand.Rand, []string) (string, string) { return d.a, d.b }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// mkSettings builds a clamped MatchSettings without repeating the struct literal
// in thirty places.
func mkSettings(rounds, draw, discuss int32) *genpb.MatchSettings {
	return &genpb.MatchSettings{
		Difficulty:     genpb.Difficulty_DIFFICULTY_MEDIUM,
		MaxRounds:      rounds,
		DrawSeconds:    draw,
		DiscussSeconds: discuss,
	}
}

// harness is one running room plus one fake socket per seat.
type harness struct {
	t     *testing.T
	r     *Room
	ids   []string
	toks  []string
	socks []*smokeSock

	set    *genpb.MatchSettings // the clamped settings the room is actually using
	cancel context.CancelFunc
}

// newHarness seats n players in a running room, all ready, still in the lobby,
// dealing from a deck whose pair never changes.
//
// Must be called from inside a synctest bubble.
func newHarness(t *testing.T, n int, s *genpb.MatchSettings, seed uint64) *harness {
	t.Helper()
	return newHarnessWithDeck(t, n, s, seed, pairDeck{"CAT", "DOG"})
}

// newHarnessWithDeck is newHarness with the deck supplied. Every round draws
// again, so a test about what changes between rounds needs a deck that does.
// Seats are taken one virtual second apart so JoinedAt is a strict order and
// host migration has something real to sort by.
//
// Must be called from inside a synctest bubble.
func newHarnessWithDeck(t *testing.T, n int, s *genpb.MatchSettings, seed uint64, deck Deck) *harness {
	t.Helper()
	if n < 1 || n > MaxPlayers {
		t.Fatalf("harness: %d players is outside 1..%d", n, MaxPlayers)
	}
	ctx, cancel := context.WithCancel(context.Background())

	r := New("TEST", "host", genpb.Avatar_AVATAR_BEETLE, Options{
		Deck:     deck,
		Rand:     mrand.New(mrand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		Settings: s,
		Logger:   discardLogger(),
	})
	hostID, hostTok := r.HostSeat()
	go r.run(ctx)

	h := &harness{t: t, r: r, set: ClampSettings(s), cancel: cancel}

	s0 := newSmokeSock()
	if _, err := r.attach(hostTok, s0); err != nil {
		t.Fatalf("harness: attach host: %v", err)
	}
	h.ids = append(h.ids, hostID)
	h.toks = append(h.toks, hostTok)
	h.socks = append(h.socks, s0)

	for i := 1; i < n; i++ {
		// One virtual second between seats: JoinedAt is the host-migration
		// ordering key, and every seat taken in the same instant would make
		// "longest connected" indistinguishable from "lowest seat number".
		h.advance(time.Second)
		sk := newSmokeSock()
		id, tok, err := r.seat(fmt.Sprintf("p%d", i), genpb.Avatar_AVATAR_COURIER, sk)
		if err != nil {
			t.Fatalf("harness: seat %d: %v", i, err)
		}
		h.ids = append(h.ids, id)
		h.toks = append(h.toks, tok)
		h.socks = append(h.socks, sk)
	}

	for i := range h.ids {
		h.send(i, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}})
	}
	synctest.Wait()
	return h
}

// stop cancels the actor. Call it with defer from inside the bubble: a cleanup
// registered on the outer *testing.T would run after the bubble is gone.
func (h *harness) stop() {
	h.cancel()
	synctest.Wait()
}

// ---------------------------------------------------------------------------
// driving
// ---------------------------------------------------------------------------

func (h *harness) send(i int, c *genpb.ClientCommand) {
	h.r.Submit(Command{PlayerID: h.ids[i], Out: h.socks[i], Cmd: c})
}

// advance moves the fake clock by d and waits for the actor to settle.
func (h *harness) advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

func (h *harness) start() {
	h.t.Helper()
	h.send(0, &genpb.ClientCommand{
		Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}}})
	synctest.Wait()
	if got := h.phase(); got != genpb.Phase_PHASE_ASSIGNING {
		h.t.Fatalf("start: phase = %v, want ASSIGNING", got)
	}
}

func (h *harness) vote(i int, candidateID string) {
	h.send(i, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_CastVote{
		CastVote: &genpb.CastVote{Choice: &genpb.CastVote_CandidateId{CandidateId: candidateID}}}})
}

func (h *harness) voteSkip(i int) {
	h.send(i, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_CastVote{
		CastVote: &genpb.CastVote{Choice: &genpb.CastVote_Skip{Skip: true}}}})
}

// skipAll makes every still-active seat vote Skip, which closes the window
// early with nobody eliminated.
func (h *harness) skipAll() {
	for i := range h.ids {
		if h.active(i) {
			h.voteSkip(i)
		}
	}
	synctest.Wait()
}

func (h *harness) drawDur() time.Duration {
	return time.Duration(h.set.GetDrawSeconds()) * time.Second
}

func (h *harness) discussDur() time.Duration {
	return time.Duration(h.set.GetDiscussSeconds()) * time.Second
}

func (h *harness) intermissionDur() time.Duration {
	return time.Duration(h.set.GetIntermissionSeconds()) * time.Second
}

// toDiscussion runs the clock forward through assignment and every drawing
// turn of the current round, stopping the moment the voting window opens.
//
// Every drawing turn — and the voting window itself — is preceded by a
// PHASE_INTERMISSION handoff (phase.go, beginIntermission), so this walks the
// clock rather than assuming which phase follows which.
func (h *harness) toDiscussion() {
	h.t.Helper()
	for range 6 * MaxPlayers {
		switch h.phase() {
		case genpb.Phase_PHASE_DISCUSSION:
			return
		case genpb.Phase_PHASE_ASSIGNING:
			h.advance(AssignDuration)
		case genpb.Phase_PHASE_INTERMISSION:
			h.advance(h.intermissionDur())
		case genpb.Phase_PHASE_DRAWING:
			h.advance(h.drawDur())
		default:
			h.t.Fatalf("toDiscussion: stuck in %v", h.phase())
		}
	}
	h.t.Fatalf("toDiscussion: never opened, phase = %v", h.phase())
}

// toDrawing runs the clock forward to the next live drawing turn, through
// assignment and the intermission handoff. It fails rather than walking past
// the turn, so a caller that meant "the Nth turn" cannot silently get the
// voting window instead.
func (h *harness) toDrawing() {
	h.t.Helper()
	for range 4 {
		switch h.phase() {
		case genpb.Phase_PHASE_DRAWING:
			return
		case genpb.Phase_PHASE_ASSIGNING:
			h.advance(AssignDuration)
		case genpb.Phase_PHASE_INTERMISSION:
			h.advance(h.intermissionDur())
		default:
			h.t.Fatalf("toDrawing: stuck in %v", h.phase())
		}
	}
	h.t.Fatalf("toDrawing: never opened, phase = %v", h.phase())
}

// nextTurn ends the live drawing turn and lands on the following one, crossing
// the intermission between them.
func (h *harness) nextTurn() {
	h.t.Helper()
	if got := h.phase(); got != genpb.Phase_PHASE_DRAWING {
		h.t.Fatalf("nextTurn: phase = %v, want DRAWING", got)
	}
	h.advance(h.drawDur())
	h.toDrawing()
}

// nextRound leaves the vote-result screen and re-enters the voting window of
// the following round.
func (h *harness) nextRound() {
	h.t.Helper()
	if got := h.phase(); got != genpb.Phase_PHASE_RESOLVING {
		h.t.Fatalf("nextRound: phase = %v, want RESOLVING", got)
	}
	h.advance(ResolveDuration)
	h.toDiscussion()
}

// ---------------------------------------------------------------------------
// reading state — always through the actor goroutine
// ---------------------------------------------------------------------------

// strokeCount and seq read the canvas log. A round boundary empties the first
// and must leave the second alone: seq is monotonic for the life of the room,
// so a client cannot mistake a wipe for a dropped frame.
func (h *harness) strokeCount() int {
	return smokeGet(h.r, func(r *Room) int { return len(r.strokes) })
}
func (h *harness) seq() int32 { return smokeGet(h.r, func(r *Room) int32 { return r.seq }) }

func (h *harness) phase() genpb.Phase  { return smokePhase(h.r) }
func (h *harness) artist() string      { return smokeArtist(h.r) }
func (h *harness) imposter() string    { return smokeImposter(h.r) }
func (h *harness) imposters() []string { return smokeImposters(h.r) }
func (h *harness) round() int32        { return smokeGet(h.r, func(r *Room) int32 { return r.round }) }
func (h *harness) activeCount() int {
	return smokeGet(h.r, func(r *Room) int { return r.ActiveCount() })
}
func (h *harness) remainingMS() int32 {
	return smokeGet(h.r, func(r *Room) int32 { return r.RemainingMS() })
}
func (h *harness) seatCount() int        { return smokeGet(h.r, func(r *Room) int { return len(r.players) }) }
func (h *harness) eliminated(i int) bool { return smokeEliminated(h.r, h.ids[i]) }
func (h *harness) word(i int) string     { return smokeWordOf(h.r, h.ids[i]) }

func (h *harness) active(i int) bool {
	return smokeGet(h.r, func(r *Room) bool {
		p := r.byID[h.ids[i]]
		return p != nil && p.Active()
	})
}

func (h *harness) hostIdx() int {
	id := smokeGet(h.r, func(r *Room) string {
		if p := r.Host(); p != nil {
			return p.ID
		}
		return ""
	})
	return h.indexOf(id)
}

func (h *harness) outcome() (genpb.WinnerSide, genpb.MatchEndReason) { return smokeOutcome(h.r) }

// indexOf maps a player id back to its harness index, or -1.
func (h *harness) indexOf(id string) int {
	for i, v := range h.ids {
		if v == id {
			return i
		}
	}
	return -1
}

// artistIdx is the harness index of the current artist, or -1.
func (h *harness) artistIdx() int { return h.indexOf(h.artist()) }

// imposterIdx is the harness index of the dealt imposter, or -1. Single
// imposter only — see smokeImposter.
func (h *harness) imposterIdx() int { return h.indexOf(h.imposter()) }

// imposterIdxs is every dealt imposter's harness index, in seat order.
func (h *harness) imposterIdxs() []int {
	ids := h.imposters()
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		out = append(out, h.indexOf(id))
	}
	return out
}

// anyIdxExcept returns the lowest active index not in skip.
func (h *harness) anyIdxExcept(skip ...int) int {
	h.t.Helper()
	for i := range h.ids {
		if !h.active(i) {
			continue
		}
		blocked := false
		for _, s := range skip {
			if s == i {
				blocked = true
			}
		}
		if !blocked {
			return i
		}
	}
	h.t.Fatal("anyIdxExcept: no candidate left")
	return -1
}

// ---------------------------------------------------------------------------
// reading the wire
// ---------------------------------------------------------------------------

func (h *harness) drain(i int) []*genpb.ServerEvent { return h.socks[i].drain() }

// drainAll empties every socket and returns the frames per seat index.
func (h *harness) drainAll() [][]*genpb.ServerEvent {
	out := make([][]*genpb.ServerEvent, len(h.socks))
	for i := range h.socks {
		out[i] = h.socks[i].drain()
	}
	return out
}

// discard throws away everything currently queued, so a later drain sees only
// frames caused by what the test does next.
func (h *harness) discard() {
	for i := range h.socks {
		h.socks[i].drain()
	}
}

func lastTally(evs []*genpb.ServerEvent) *genpb.VoteTally {
	var out *genpb.VoteTally
	for _, e := range evs {
		if v := e.GetVoteTally(); v != nil {
			out = v
		}
	}
	return out
}

func lastEliminated(evs []*genpb.ServerEvent) *genpb.PlayerEliminated {
	var out *genpb.PlayerEliminated
	for _, e := range evs {
		if v := e.GetPlayerEliminated(); v != nil {
			out = v
		}
	}
	return out
}

func lastMatchEnded(evs []*genpb.ServerEvent) *genpb.MatchEnded {
	var out *genpb.MatchEnded
	for _, e := range evs {
		if v := e.GetMatchEnded(); v != nil {
			out = v
		}
	}
	return out
}

func lastSnapshot(evs []*genpb.ServerEvent) *genpb.Snapshot {
	var out *genpb.Snapshot
	for _, e := range evs {
		if v := e.GetSnapshot(); v != nil {
			out = v
		}
	}
	return out
}

func lastLobbyState(evs []*genpb.ServerEvent) *genpb.LobbyState {
	var out *genpb.LobbyState
	for _, e := range evs {
		if v := e.GetLobbyState(); v != nil {
			out = v
		}
	}
	return out
}

func lastJoined(evs []*genpb.ServerEvent) *genpb.Joined {
	var out *genpb.Joined
	for _, e := range evs {
		if v := e.GetJoined(); v != nil {
			out = v
		}
	}
	return out
}

func allSpectatorInfo(evs []*genpb.ServerEvent) []*genpb.SpectatorInfo {
	var out []*genpb.SpectatorInfo
	for _, e := range evs {
		if v := e.GetSpectatorInfo(); v != nil {
			out = append(out, v)
		}
	}
	return out
}

func allRoundStarted(evs []*genpb.ServerEvent) []*genpb.RoundStarted {
	var out []*genpb.RoundStarted
	for _, e := range evs {
		if v := e.GetRoundStarted(); v != nil {
			out = append(out, v)
		}
	}
	return out
}

func allTurnStarted(evs []*genpb.ServerEvent) []*genpb.TurnStarted {
	var out []*genpb.TurnStarted
	for _, e := range evs {
		if v := e.GetTurnStarted(); v != nil {
			out = append(out, v)
		}
	}
	return out
}

// phaseTrail is the sequence of phases announced on a socket, which is the
// only view of the phase machine a client ever gets.
func phaseTrail(evs []*genpb.ServerEvent) []genpb.Phase {
	var out []genpb.Phase
	for _, e := range evs {
		if v := e.GetPhaseChanged(); v != nil {
			out = append(out, v.GetPhase())
		}
	}
	return out
}

func errorCodes(evs []*genpb.ServerEvent) []genpb.ErrorCode {
	var out []genpb.ErrorCode
	for _, e := range evs {
		if v := e.GetError(); v != nil {
			out = append(out, v.GetCode())
		}
	}
	return out
}

func hasErrorCode(evs []*genpb.ServerEvent, want genpb.ErrorCode) bool {
	for _, c := range errorCodes(evs) {
		if c == want {
			return true
		}
	}
	return false
}

func countVotes(t *genpb.VoteTally, candidateID string) int32 {
	for _, c := range t.GetCounts() {
		if c.GetCandidateId() == candidateID {
			return c.GetVotes()
		}
	}
	return 0
}

// fieldNames lists a message's declared fields, sorted. Used by the tests that
// assert a payload exposes aggregates and nothing else.
func fieldNames(m proto.Message) []string {
	fs := m.ProtoReflect().Descriptor().Fields()
	out := make([]string, 0, fs.Len())
	for i := range fs.Len() {
		out = append(out, string(fs.Get(i).Name()))
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
