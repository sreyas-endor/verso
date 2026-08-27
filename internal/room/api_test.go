package room

// api_test.go — the exported surface the transport package actually calls, and
// the two small pieces of the secret-leak defense that live outside the phase
// machine: the audience markers on every event wrapper, and Player redaction.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// TestExportedSeatLifecycle drives Run, Seat, Attach and Detach — the four
// entry points transport uses — rather than the unexported forms every other
// test in this package reaches for.
func TestExportedSeatLifecycle(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	r := New("APIX", "host", Options{Deck: pairDeck{"CAT", "DOG"}, Logger: discardLogger()})
	hostID, hostTok := r.HostSeat()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	s0 := newSmokeSock()
	if id, err := r.Attach(hostTok, s0); err != nil || id != hostID {
		t.Fatalf("Attach = %q, %v", id, err)
	}
	if _, err := r.Attach("nope", newSmokeSock()); !errors.Is(err, ErrBadSeat) {
		t.Fatalf("Attach with a bad token = %v, want ErrBadSeat", err)
	}

	s1 := newSmokeSock()
	p1, tok1, err := r.Seat("bee", s1)
	if err != nil {
		t.Fatalf("Seat: %v", err)
	}
	if p1 == "" || tok1 == "" {
		t.Fatal("Seat returned an empty identity")
	}

	if got := r.Player(p1); got == nil || got.ID != p1 {
		t.Fatalf("Player(%q) = %v", p1, got)
	}
	if got := r.Player("missing"); got != nil {
		t.Fatalf("Player of an unseated id = %v, want nil", got)
	}
	if got := len(r.Players()); got != 2 {
		t.Fatalf("Players() = %d seats, want 2", got)
	}
	if got := r.Phase(); got != genpb.Phase_PHASE_LOBBY {
		t.Fatalf("Phase() = %v, want LOBBY", got)
	}
	if got := r.Round(); got != 0 {
		t.Fatalf("Round() = %d before the first round, want 0", got)
	}
	if got := r.RemainingMS(); got != 0 {
		t.Fatalf("RemainingMS() = %d with no phase armed, want 0", got)
	}
	if got := r.Host(); got == nil || got.ID != hostID {
		t.Fatalf("Host() = %v, want the creator", got)
	}
	if got := r.Settings(); got.GetMaxRounds() != DefaultRounds {
		t.Fatalf("Settings() = %v, want the defaults", got)
	}
	if got := r.ActiveCount(); got != 2 {
		t.Fatalf("ActiveCount() = %d, want 2", got)
	}
	if got := len(r.ActivePlayers()); got != 2 {
		t.Fatalf("ActivePlayers() = %d, want 2", got)
	}

	r.Detach(p1, s1)
	if got := r.ActiveCount(); got != 1 {
		t.Fatalf("ActiveCount() = %d after a detach, want 1", got)
	}
	if got := len(r.Players()); got != 2 {
		t.Fatalf("Players() = %d: a detached seat is retained, want 2", got)
	}

	// Once Run returns, every entry point reports ErrClosed instead of blocking
	// forever on a select nobody is servicing.
	cancel()
	<-done
	if _, _, err := r.Seat("late", newSmokeSock()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Seat after close = %v, want ErrClosed", err)
	}
	if _, err := r.Attach(tok1, newSmokeSock()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Attach after close = %v, want ErrClosed", err)
	}
	r.Detach(p1, nil) // must not block or panic
	r.Submit(Command{PlayerID: p1, Cmd: &genpb.ClientCommand{}})
}

// TestSubmitIgnoresAnEmptyEnvelopeAndAnUnseatedSender — handle's two guards.
// Neither may reach a command handler.
func TestSubmitIgnoresAnEmptyEnvelopeAndAnUnseatedSender(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(2, 5, 30), 6001)
		defer h.stop()
		h.discard()

		h.r.Submit(Command{PlayerID: h.ids[0], Out: h.socks[0], Cmd: nil})
		h.r.Submit(Command{PlayerID: h.ids[0], Out: h.socks[0],
			Cmd: &genpb.ClientCommand{Cid: "empty"}})
		h.r.Submit(Command{PlayerID: "ffffffffffffffff", Out: h.socks[0],
			Cmd: &genpb.ClientCommand{Cmd: &genpb.ClientCommand_StartMatch{
				StartMatch: &genpb.StartMatch{}}}})
		synctest.Wait()

		// The empty oneof is a client this build does not understand; it gets a
		// rejection, not silence. The unseated sender gets nothing at all.
		codes := errorCodes(h.drain(0))
		if len(codes) != 1 || codes[0] != genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND {
			t.Fatalf("errors = %v, want exactly one INVALID_COMMAND", codes)
		}
		if got := h.phase(); got != genpb.Phase_PHASE_LOBBY {
			t.Fatalf("an unseated sender started the match: phase = %v", got)
		}
	})
}

// TestJoinResyncsAndChecksTheProtocolVersion — onJoin is a resync, never a
// second way to establish identity.
func TestJoinResyncsAndChecksTheProtocolVersion(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(2, 5, 30), 6002)
		defer h.stop()
		h.discard()

		h.send(1, &genpb.ClientCommand{Cid: "j1", Cmd: &genpb.ClientCommand_Join{
			Join: &genpb.JoinRoom{ProtocolVersion: ProtocolVersion + 1}}})
		synctest.Wait()
		if !hasErrorCode(h.drain(1), genpb.ErrorCode_ERROR_CODE_PROTOCOL_VERSION) {
			t.Fatal("a mismatched protocol version was accepted")
		}

		h.send(1, &genpb.ClientCommand{Cid: "j2", Cmd: &genpb.ClientCommand_Join{
			Join: &genpb.JoinRoom{ProtocolVersion: ProtocolVersion, DisplayName: "ignored"}}})
		synctest.Wait()
		evs := h.drain(1)
		joined := lastJoined(evs)
		if joined == nil {
			t.Fatal("no Joined on resync")
		}
		if joined.GetPlayerId() != h.ids[1] || joined.GetSeatToken() != h.toks[1] {
			t.Fatal("the resync re-derived identity from the frame body")
		}
		if got := joined.GetProtocolVersion(); got != ProtocolVersion {
			t.Fatalf("Joined.protocol_version = %d, want %d", got, ProtocolVersion)
		}
		if lastSnapshot(evs) == nil {
			t.Fatal("a resync did not deliver a Snapshot")
		}
		// A version of 0 means "unset" and is accepted.
		h.send(1, &genpb.ClientCommand{Cmd: &genpb.ClientCommand_Join{Join: &genpb.JoinRoom{}}})
		synctest.Wait()
		if hasErrorCode(h.drain(1), genpb.ErrorCode_ERROR_CODE_PROTOCOL_VERSION) {
			t.Fatal("an unset protocol version was rejected")
		}
	})
}

// TestPlayerNeverRendersItsWord — IMPLEMENTATION_PLAN.md §1: not in a payload,
// and not in a log line either. LogValue and String are the two ways a *Player
// can reach a logger.
func TestPlayerNeverRendersItsWord(t *testing.T) {
	t.Parallel()
	const secret = "PANGOLIN_SECRET_WORD"

	p := &Player{
		ID:         "abc123",
		Name:       "someone",
		SeatToken:  "tok-should-not-appear",
		Seat:       4,
		word:       secret,
		Connected:  true,
		Eliminated: true,
		IsHost:     true,
	}

	if got := p.String(); strings.Contains(got, secret) {
		t.Fatalf("Player.String() leaked the word: %s", got)
	} else if !strings.Contains(got, "[redacted]") {
		t.Fatalf("Player.String() = %s, want a redaction marker", got)
	}

	rendered := fmt.Sprint(p.LogValue())
	if strings.Contains(rendered, secret) {
		t.Fatalf("Player.LogValue() leaked the word: %s", rendered)
	}
	if !strings.Contains(rendered, "[redacted]") {
		t.Fatalf("Player.LogValue() = %s, want a redaction marker", rendered)
	}

	// A nil receiver is a real case: logging happens on paths where the seat
	// may already be gone.
	var nilp *Player
	if got := nilp.String(); got != "player{nil}" {
		t.Fatalf("nil Player.String() = %q", got)
	}
	if got := fmt.Sprint(nilp.LogValue()); !strings.Contains(got, "<nil>") {
		t.Fatalf("nil Player.LogValue() = %q", got)
	}
	if nilp.Active() {
		t.Fatal("a nil Player is active")
	}
}

// TestEventAudienceInventory — every wrapper the room can put on a socket, its
// kind, and which side of the broadcast line it sits on. The five unicast-only
// wrappers are the whole of defense 1 (IMPLEMENTATION_PLAN.md §4.2); this table
// is what notices if one of them quietly grows a broadcastSafe marker.
func TestEventAudienceInventory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ev        Event
		kind      EventKind
		broadcast bool
	}{
		{EvLobbyState{&genpb.LobbyState{}}, KindLobbyState, true},
		{EvSettingsChanged{&genpb.SettingsChanged{}}, KindSettingsChanged, true},
		{EvRoundStarted{&genpb.RoundStarted{}}, KindRoundStarted, true},
		{EvTurnStarted{&genpb.TurnStarted{}}, KindTurnStarted, true},
		{EvStrokeBegan{&genpb.StrokeBegan{}}, KindStrokeBegan, true},
		{EvStrokePoints{&genpb.StrokePoints{}}, KindStrokePoints, true},
		{EvStrokeEnded{&genpb.StrokeEnded{}}, KindStrokeEnded, true},
		{EvPhaseChanged{&genpb.PhaseChanged{}}, KindPhaseChanged, true},
		{EvVoteCastCount{&genpb.VoteCastCount{}}, KindVoteCastCount, true},
		{EvVoteTally{&genpb.VoteTally{}}, KindVoteTally, true},
		{EvPlayerEliminated{&genpb.PlayerEliminated{}}, KindPlayerEliminated, true},
		{EvMatchEnded{&genpb.MatchEnded{}}, KindMatchEnded, true},
		{EvPlayerPresence{&genpb.PlayerPresence{}}, KindPlayerPresence, true},
		{EvError{&genpb.Error{}}, KindError, true},

		// The five that must never be broadcastable.
		{EvJoined{&genpb.Joined{}}, KindJoined, false},
		{EvYourWord{&genpb.YourWord{}}, KindYourWord, false},
		{EvSnapshot{&genpb.Snapshot{}}, KindSnapshot, false},
		{EvSpectatorInfo{&genpb.SpectatorInfo{}}, KindSpectatorInfo, false},
		{EvVoteAccepted{&genpb.VoteAccepted{}}, KindVoteAccepted, false},
	}
	if len(cases) != int(KindVoteAccepted) {
		t.Fatalf("the inventory covers %d wrappers but there are %d kinds",
			len(cases), KindVoteAccepted)
	}
	seen := map[EventKind]bool{}
	for _, tc := range cases {
		name := tc.kind.String()
		if seen[tc.kind] {
			t.Fatalf("%s appears twice in the inventory", name)
		}
		seen[tc.kind] = true

		if got := KindOf(tc.ev); got != tc.kind {
			t.Errorf("%s: KindOf = %v", name, got)
		}
		if got := IsBroadcastable(tc.ev); got != tc.broadcast {
			t.Errorf("%s: IsBroadcastable = %v, want %v", name, got, tc.broadcast)
		}
		env := tc.ev.Envelope("cid-42")
		if env == nil || env.GetCid() != "cid-42" {
			t.Errorf("%s: envelope = %v, want cid echoed", name, env)
		}
		if env.GetEvt() == nil {
			t.Errorf("%s: envelope carries no payload", name)
		}
	}
	if got := EventKind(-1).String(); got != "Unknown" {
		t.Errorf("EventKind(-1).String() = %q", got)
	}
	if got := EventKind(999).String(); got != "Unknown" {
		t.Errorf("EventKind(999).String() = %q", got)
	}
	if got := KindUnspecified.String(); got != "Unspecified" {
		t.Errorf("KindUnspecified.String() = %q", got)
	}
}

// TestErrorCodeForEverySentinel — transport maps room errors onto the wire enum
// through this one function, so an unmapped sentinel becomes an UNSPECIFIED
// error a client cannot act on.
func TestErrorCodeForEverySentinel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want genpb.ErrorCode
	}{
		{nil, genpb.ErrorCode_ERROR_CODE_UNSPECIFIED},
		{ErrRoomFull, genpb.ErrorCode_ERROR_CODE_ROOM_FULL},
		{ErrMatchInProgress, genpb.ErrorCode_ERROR_CODE_MATCH_IN_PROGRESS},
		{ErrBadSeat, genpb.ErrorCode_ERROR_CODE_BAD_SEAT},
		{ErrNotHost, genpb.ErrorCode_ERROR_CODE_NOT_HOST},
		{ErrNotArtist, genpb.ErrorCode_ERROR_CODE_NOT_ARTIST},
		{ErrAlreadyVoted, genpb.ErrorCode_ERROR_CODE_ALREADY_VOTED},
		{ErrNotEnoughPlayers, genpb.ErrorCode_ERROR_CODE_NOT_ENOUGH_PLAYERS},
		{ErrWrongPhase, genpb.ErrorCode_ERROR_CODE_WRONG_PHASE},
		{ErrNotActive, genpb.ErrorCode_ERROR_CODE_NOT_ACTIVE},
		{ErrInvalidCommand, genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND},
		{ErrClosed, genpb.ErrorCode_ERROR_CODE_UNSPECIFIED},
		{errors.New("something internal"), genpb.ErrorCode_ERROR_CODE_UNSPECIFIED},
		// Wrapping must survive: transport annotates errors on the way out.
		{fmt.Errorf("attach: %w", ErrBadSeat), genpb.ErrorCode_ERROR_CODE_BAD_SEAT},
	}
	for _, tc := range cases {
		if got := ErrorCodeFor(tc.err); got != tc.want {
			t.Errorf("ErrorCodeFor(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	// An internal error must not put its message shape into a machine-readable
	// field, and no sentinel may name a word or a token.
	for _, err := range []error{ErrRoomFull, ErrBadSeat, ErrNotArtist, ErrAlreadyVoted} {
		if strings.Contains(err.Error(), "word") {
			t.Errorf("sentinel %q mentions a word", err)
		}
	}
}

// TestStrokeInputValidators — the pure guards the stroke layer is built on.
func TestStrokeInputValidators(t *testing.T) {
	t.Parallel()

	widths := []struct{ in, want int32 }{
		{-5, MinStrokeWidth}, {0, MinStrokeWidth}, {1, 1},
		{16, 16}, {32, MaxStrokeWidth}, {33, MaxStrokeWidth}, {99999, MaxStrokeWidth},
	}
	for _, tc := range widths {
		if got := ClampWidth(tc.in); got != tc.want {
			t.Errorf("ClampWidth(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}

	for _, i := range []int32{-1, PaletteSize, PaletteSize + 1, 1 << 20} {
		if ValidColorIndex(i) {
			t.Errorf("ValidColorIndex(%d) = true", i)
		}
	}
	for i := int32(0); i < PaletteSize; i++ {
		if !ValidColorIndex(i) {
			t.Errorf("ValidColorIndex(%d) = false", i)
		}
	}

	points := []struct {
		name string
		pts  []int32
		ok   bool
	}{
		{"empty", nil, false},
		{"odd_length", []int32{1, 2, 3}, false},
		{"one_point", []int32{0, 0}, true},
		{"negative_is_legal", []int32{-1, -1}, true},
		{"at_int16_min", []int32{CoordMin, CoordMin}, true},
		{"at_int16_max", []int32{CoordMax, CoordMax}, true},
		{"below_int16", []int32{CoordMin - 1, 0}, false},
		{"above_int16", []int32{0, CoordMax + 1}, false},
		{"outside_the_grid_is_legal", []int32{GridWidth + 500, GridHeight + 500}, true},
		{"at_the_per_message_cap", make([]int32, 2*MaxPointsPerStroke), true},
		{"one_over_the_cap", make([]int32, 2*MaxPointsPerStroke+2), false},
	}
	for _, tc := range points {
		if got := ValidPoints(tc.pts); got != tc.ok {
			t.Errorf("ValidPoints(%s) = %v, want %v", tc.name, got, tc.ok)
		}
	}
}

// TestDisplayNameTruncated — a name is the one free-form string a client
// controls, and it goes in the public roster.
func TestDisplayNameTruncated(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", MaxDisplayNameLen*3)
	if got := truncateName(long); len([]rune(got)) != MaxDisplayNameLen {
		t.Fatalf("truncateName kept %d runes, want %d", len([]rune(got)), MaxDisplayNameLen)
	}
	// Runes, not bytes: a multi-byte name must not be cut mid-character.
	multi := strings.Repeat("é", MaxDisplayNameLen+5)
	got := truncateName(multi)
	if len([]rune(got)) != MaxDisplayNameLen {
		t.Fatalf("truncateName kept %d runes of a multi-byte name", len([]rune(got)))
	}
	if !strings.HasPrefix(multi, got) {
		t.Fatal("truncateName cut a rune in half")
	}
	if got := truncateName("bee"); got != "bee" {
		t.Fatalf("truncateName(%q) = %q", "bee", got)
	}
}

// TestDefaultSettingsAreTheRecommendedGame — DESIGN.md:224.
func TestDefaultSettingsAreTheRecommendedGame(t *testing.T) {
	t.Parallel()
	s := DefaultSettings()
	if s.GetDifficulty() != genpb.Difficulty_DIFFICULTY_MEDIUM {
		t.Errorf("difficulty = %v, want MEDIUM", s.GetDifficulty())
	}
	if s.GetMaxRounds() != 2 || s.GetDrawSeconds() != 15 || s.GetDiscussSeconds() != 120 {
		t.Errorf("defaults = %v, want 2 rounds / 15 s / 120 s", s)
	}
	if DefaultSettings() == s {
		t.Error("DefaultSettings returns a shared struct callers could mutate")
	}
	if GraceWindow != 60*time.Second {
		t.Errorf("GraceWindow = %v, want the resolved 60 s", GraceWindow)
	}
}
