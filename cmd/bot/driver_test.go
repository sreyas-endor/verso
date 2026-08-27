package main

// driver_test.go — milestone 9's acceptance criterion: 3-, 6- and 10-player
// matches running end to end in CI, with no browser and no manual step.
//
// Everything here drives the real binary's own machinery: registry, transport,
// room actor, real WebSockets over a real loopback listener on an ephemeral
// port, binary protobuf frames. The only substitution is the word deck, and
// that is so the leak search can tell a secret from a coincidence (deck.go).
//
// Runtime. These are wall-clock tests, because the phase machine is a wall
// clock: DESIGN.md's floors are 5 s of drawing per player and a 30 s discussion
// window, and room.GraceWindow is a fixed 60 s. The bounds are therefore
// arithmetic, not guesses — see defaultTimeout in table.go. The tests are
// parallel and share one server, so the suite costs about as long as its
// slowest member rather than the sum. -short skips the two longest.
//
//	go test -race ./cmd/bot/            # about 70 s wall
//	go test -race -short ./cmd/bot/     # about 60 s wall

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/room"
)

// testServer is the one in-process server every test in this file drives. One
// server, many rooms, exactly as a real deployment runs.
var testServer *LocalServer

func TestMain(m *testing.M) {
	ctx, cancel := context.WithCancel(context.Background())

	srv, err := StartServer(ctx, ServerOptions{
		Settings: room.DefaultSettings(),
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		cancel()
		panic("bot: could not start the test server: " + err.Error())
	}
	testServer = srv

	code := m.Run()

	shutdown, stop := context.WithTimeout(context.Background(), 15*time.Second)
	err = srv.Close(shutdown)
	stop()
	cancel()
	if err != nil && code == 0 {
		panic("bot: server shutdown: " + err.Error())
	}
	os.Exit(code)
}

// fastSettings is the shortest legal match: DESIGN.md's own floors, so the
// tests are as quick as the rules permit and not one millisecond quicker.
func fastSettings(rounds int32) *genpb.MatchSettings {
	return &genpb.MatchSettings{
		Difficulty:          genpb.Difficulty_DIFFICULTY_MEDIUM,
		MaxRounds:           rounds,
		DrawSeconds:         room.MinDrawSeconds,
		DiscussSeconds:      room.MinDiscussSeconds,
		IntermissionSeconds: room.MinIntermissionSeconds,
	}
}

// play runs one plan and fails the test on anything the harness flagged.
func play(t *testing.T, plan MatchPlan) *MatchResult {
	t.Helper()
	if plan.Logger == nil {
		plan.Logger = slog.New(slog.DiscardHandler)
	}
	res, err := RunMatch(t.Context(), testServer.URL, plan)
	if res != nil {
		t.Log("\n" + res.Summary())
	}
	if err != nil {
		t.Fatalf("%s: %v", plan.Name, err)
	}
	if len(res.Leaks) > 0 {
		t.Errorf("%s: %d secret leaks reached a socket", plan.Name, len(res.Leaks))
	}
	for _, v := range res.Violations {
		t.Errorf("%s: %s", plan.Name, v)
	}
	return res
}

// requireEnd asserts the headline result.
func requireEnd(t *testing.T, res *MatchResult, winner genpb.WinnerSide, reason genpb.MatchEndReason) {
	t.Helper()
	if res.Winner != winner {
		t.Errorf("winner = %s, want %s", res.Winner, winner)
	}
	if res.Reason != reason {
		t.Errorf("reason = %s, want %s", res.Reason, reason)
	}
}

// everySocketSaw asserts a frame type reached every bot that finished. A canary
// that never saw a frame type asserts nothing about it.
func everySocketSaw(t *testing.T, res *MatchResult, frame string) {
	t.Helper()
	for _, rep := range res.Reports {
		if rep.Ended == nil {
			continue // a socket that was scripted to vanish
		}
		if rep.FrameCounts[frame] == 0 {
			t.Errorf("%s never received a %s frame", rep.Name, frame)
		}
	}
}

// ---------------------------------------------------------------------------
// The three player counts DESIGN.md:229 asks for
// ---------------------------------------------------------------------------

// TestThreePlayerMatchGroupWin is the group's win condition: the table converges
// on the imposter, a strict majority of 2 of 3 removes them, and the match ends
// immediately (DESIGN.md:63).
func TestThreePlayerMatchGroupWin(t *testing.T) {
	t.Parallel()
	res := play(t, MatchPlan{
		Name:     "3-player group win",
		Players:  3,
		Settings: fastSettings(1),
		Strategy: "gang",
		// Every bot opens with an absurd brush width and checks the broadcast
		// came back clamped (internal/room/strokes.go, authority 2), and every
		// bot provokes the three mid-match rejections so Error.message is on
		// the wire and gets scanned like anything else.
		Draw:    DrawPlan{ClampProbe: true},
		Provoke: true,
		// And when it is over, the host sends Rematch and the room has to come
		// back as a startable three-seat lobby (DESIGN.md:81). RunMatch fails
		// the run if it does not.
		Rematch: true,
	})
	requireEnd(t, res,
		genpb.WinnerSide_WINNER_SIDE_GROUP,
		genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_ELIMINATED)

	if res.RoundsPlayed != 1 {
		t.Errorf("rounds played = %d, want 1", res.RoundsPlayed)
	}
	everySocketSaw(t, res, "MatchEnded")
	everySocketSaw(t, res, "VoteTally")
	everySocketSaw(t, res, "StrokeBegan")
	everySocketSaw(t, res, "YourWord")

	// The imposter went out, so nobody is a spectator who needs telling who the
	// imposter was (DESIGN.md:67 applies only to a NON-imposter elimination).
	for _, rep := range res.Reports {
		if rep.Spectator != nil {
			t.Errorf("%s received SpectatorInfo on a group win", rep.Name)
		}
	}
	if res.Strokes == 0 {
		t.Error("no strokes were committed: nobody actually drew")
	}

	// The server clamped an over-wide brush and said so on the wire. Counting
	// the confirmations keeps the assertion from passing by never running.
	probes := 0
	for _, rep := range res.Reports {
		probes += rep.ClampProbes
	}
	if probes == 0 {
		t.Error("the width clamp was never actually probed")
	}

	// The three provocations, each from a different handler: the artist gate in
	// internal/room/strokes.go, the host check in internal/room/room.go, and
	// the one-vote rule in internal/room/vote.go. Two of those build the
	// rejection with the sending player in hand, which is exactly why the leak
	// scan has to see them.
	codes := map[string]int{}
	snapshots := 0
	for _, rep := range res.Reports {
		for c, n := range rep.ErrorCodes {
			codes[c] += n
		}
		if !rep.Dropped {
			snapshots += rep.FrameCounts["Snapshot"]
		}
	}
	for _, want := range []string{"ERROR_CODE_NOT_ARTIST", "ERROR_CODE_NOT_HOST"} {
		if codes[want] == 0 {
			t.Errorf("no socket received %s: that rejection path was never exercised", want)
		}
	}
	// The second vote is refused, but by which rule depends on whether the
	// window was still open when it landed: a vote that arrives after the last
	// voter has closed the window early is WRONG_PHASE rather than
	// ALREADY_VOTED. Both come out of internal/room/vote.go's onCastVote with
	// the sending player in scope, which is the leak surface being exercised;
	// the irreversibility rule itself is pinned deterministically in
	// TestASilentPlayerLetsTheDiscussionTimerExpire, where the window cannot
	// close early.
	if codes["ERROR_CODE_ALREADY_VOTED"]+codes["ERROR_CODE_WRONG_PHASE"] == 0 {
		t.Error("a second vote was never refused")
	}
	if snapshots == 0 {
		t.Error("RequestSnapshot mid-match produced no Snapshot")
	}
	everySocketSaw(t, res, "Error")

	// Observed, not asserted. internal/room/api.go's BroadcastReply stamps the
	// commanding player's correlation id onto the frame every socket receives,
	// so a client that keys pending-request state on cid is handed other
	// players' ids. It is a design question for whoever owns the protocol, so
	// the harness reports the number instead of failing on it.
	foreign := 0
	sample := ""
	for _, rep := range res.Reports {
		foreign += rep.ForeignCids
		if sample == "" {
			sample = rep.ForeignCidSample
		}
	}
	t.Logf("frames carrying another player's correlation id: %d (e.g. %s)", foreign, sample)
}

// TestSixPlayerMatchImposterWin is the other win path: nobody reaches a strict
// majority, so nobody is eliminated and the imposter survives the configured
// final round (DESIGN.md:59, DESIGN.md:71).
func TestSixPlayerMatchImposterWin(t *testing.T) {
	t.Parallel()
	res := play(t, MatchPlan{
		Name:     "6-player imposter win",
		Players:  6,
		Settings: fastSettings(1),
		Strategy: "skip",
	})
	requireEnd(t, res,
		genpb.WinnerSide_WINNER_SIDE_IMPOSTER,
		genpb.MatchEndReason_MATCH_END_REASON_FINAL_ROUND_SURVIVED)

	// Six players, one drawing turn each, one round.
	for _, rep := range res.Reports {
		if got := rep.FrameCounts["TurnStarted"]; got != 6 {
			t.Errorf("%s saw %d TurnStarted frames, want one per player", rep.Name, got)
		}
	}
	everySocketSaw(t, res, "MatchEnded")
}

// TestTenPlayerMatchImposterWin is the count that cannot be playtested by hand,
// which is the entire reason milestone 9 exists.
func TestTenPlayerMatchImposterWin(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("ten drawing turns at the 5 s floor is a minute of wall clock")
	}
	res := play(t, MatchPlan{
		Name:     "10-player imposter win",
		Players:  10,
		Settings: fastSettings(1),
		Strategy: "self", // voting for yourself is legal and cannot reach a majority
	})
	requireEnd(t, res,
		genpb.WinnerSide_WINNER_SIDE_IMPOSTER,
		genpb.MatchEndReason_MATCH_END_REASON_FINAL_ROUND_SURVIVED)

	if got := len(res.Reports); got != 10 {
		t.Fatalf("%d bots reported, want 10", got)
	}
	// Ten seats, ten distinct ids, and exactly one minority word.
	seen := make(map[string]bool, 10)
	imposters := 0
	for _, rep := range res.Reports {
		if seen[rep.PlayerID] {
			t.Errorf("player id %q was issued twice", rep.PlayerID)
		}
		seen[rep.PlayerID] = true
		if rep.Word == res.ImposterWord {
			imposters++
		}
	}
	if imposters != 1 {
		t.Errorf("%d sockets were dealt the imposter word, want exactly 1", imposters)
	}
	// Every vote for oneself, so the tally is ten candidates with one vote each
	// and a threshold of six that nobody can reach.
	everySocketSaw(t, res, "VoteTally")
}

// ---------------------------------------------------------------------------
// The third path the brief asks for
// ---------------------------------------------------------------------------

// TestImposterDisconnectAwardsTheGroupTheMatch drives DESIGN.md:125. The
// imposter's socket vanishes for good as soon as the words are dealt; the match
// may not end on any headcount while their seat is merely dark, and when the
// 60 s grace window expires the group is awarded the win.
//
// Four rounds are configured deliberately: at the 5 s drawing floor, two
// remaining players get through a round in well under a minute, and the match
// must still be running when the grace window closes for this to be testing the
// disconnect path rather than the final-round path.
func TestImposterDisconnectAwardsTheGroupTheMatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("room.GraceWindow is a fixed 60 s and this test has to outlast it")
	}
	settings := fastSettings(room.MaxRounds)
	settings.DrawSeconds = 10

	res := play(t, MatchPlan{
		Name:         "3-player imposter disconnect",
		Players:      3,
		Settings:     settings,
		Strategy:     "skip",
		DropImposter: true,
	})
	requireEnd(t, res,
		genpb.WinnerSide_WINNER_SIDE_GROUP,
		genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_DISCONNECTED)

	// The verdict lands at grace expiry, not at socket close: a one-second blip
	// must not forfeit a match.
	if res.Duration < room.GraceWindow {
		t.Errorf("the match ended after %s, sooner than the %s grace window",
			res.Duration.Round(time.Second), room.GraceWindow)
	}
	// Two players remained active for most of the match. That must NOT have
	// ended it for the imposter, which is the rule collision the dark-imposter
	// guard in internal/room/end.go exists to resolve.
	if res.Reason == genpb.MatchEndReason_MATCH_END_REASON_TWO_PLAYERS_REMAIN {
		t.Error("the imposter was awarded the match for leaving")
	}
	for _, rep := range res.Reports {
		if rep.PlayerID == res.ImposterID {
			if rep.Ended != nil {
				t.Error("the disconnected imposter's socket received MatchEnded")
			}
			continue
		}
		if rep.Ended == nil {
			t.Errorf("%s never saw the match end", rep.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Reconnect, spectators, and the discussion timer
// ---------------------------------------------------------------------------

// TestDropMidTurnAndReclaimTheSeat is milestone 7 seen from the client side: a
// bot kills its socket in the middle of its own drawing turn, comes back with
// the seat token it was issued, and checks that the Snapshot it receives is the
// same seat, the same word and a canvas that only grew.
func TestDropMidTurnAndReclaimTheSeat(t *testing.T) {
	t.Parallel()
	res := play(t, MatchPlan{
		Name:     "3-player drop and reclaim",
		Players:  3,
		Settings: fastSettings(1),
		Strategy: "skip",
		// Far enough into the turn that some geometry has already been
		// committed, so the replayed canvas is not trivially empty.
		DropMidTurn:     &DropPlan{OnMyTurnAfter: 900 * time.Millisecond, RejoinAfter: 400 * time.Millisecond},
		DropMidTurnSeat: 1,
	})
	requireEnd(t, res,
		genpb.WinnerSide_WINNER_SIDE_IMPOSTER,
		genpb.MatchEndReason_MATCH_END_REASON_FINAL_ROUND_SURVIVED)

	var dropper *Report
	for i := range res.Reports {
		if res.Reports[i].Dropped {
			dropper = &res.Reports[i]
		}
	}
	if dropper == nil {
		t.Fatal("no bot dropped its socket: the scenario did not run")
	}
	if dropper.Resyncs == 0 {
		t.Fatal("the bot came back but never checked its state against what it held before the drop")
	}
	if got := dropper.FrameCounts["Joined"]; got < 2 {
		t.Errorf("the reclaiming bot received %d Joined frames, want at least 2", got)
	}
	if got := dropper.FrameCounts["Snapshot"]; got == 0 {
		t.Error("reclaiming a seat produced no Snapshot")
	}
	if dropper.Ended == nil {
		t.Error("the reclaimed seat did not see the match end")
	}
	// Everyone else must have been told the seat went dark and came back.
	for _, rep := range res.Reports {
		if rep.FrameCounts["PlayerPresence"] == 0 {
			t.Errorf("%s was never told about the disconnect", rep.Name)
		}
	}
}

// TestNonImposterEliminationMakesOneSpectator drives DESIGN.md:65 and :67: the
// group removes the wrong player, that player alone is privately told who the
// imposter is, was_imposter stays false, and the match continues into the next
// round.
func TestNonImposterEliminationMakesOneSpectator(t *testing.T) {
	t.Parallel()
	res := play(t, MatchPlan{
		Name:       "4-player wrong elimination",
		Players:    4,
		Settings:   fastSettings(2),
		Strategy:   "gang",
		GangTarget: "non-imposter",
		Provoke:    true,
	})
	requireEnd(t, res,
		genpb.WinnerSide_WINNER_SIDE_IMPOSTER,
		genpb.MatchEndReason_MATCH_END_REASON_FINAL_ROUND_SURVIVED)

	if res.RoundsPlayed != 2 {
		t.Errorf("rounds played = %d, want the match to have continued to 2", res.RoundsPlayed)
	}

	spectators := 0
	for _, rep := range res.Reports {
		if rep.Spectator == nil {
			continue
		}
		spectators++
		if rep.PlayerID == res.ImposterID {
			t.Error("the imposter was told who the imposter is")
		}
		if rep.Spectator.GetImposterPlayerId() != res.ImposterID {
			t.Errorf("SpectatorInfo named %q as the imposter, want %q",
				rep.Spectator.GetImposterPlayerId(), res.ImposterID)
		}
		if !rep.Eliminated {
			t.Errorf("%s received SpectatorInfo without being eliminated", rep.Name)
		}
	}
	if spectators != 1 {
		t.Errorf("%d sockets received SpectatorInfo, want exactly 1", spectators)
	}

	// was_imposter is false in every frame of a match the group did not win.
	for _, rep := range res.Reports {
		for _, ev := range rep.Transcript {
			if e := ev.GetPlayerEliminated(); e.GetWasImposter() {
				t.Errorf("%s was told was_imposter=true in a match that ended %s",
					rep.Name, res.Winner)
			}
		}
	}
}

// TestASilentPlayerLetsTheDiscussionTimerExpire is the never-vote strategy: the
// window cannot close early while one active player has not answered, so the
// combined discussion-and-decision timer has to run out. The silence is an
// abstention — it reaches neither skip_count nor any candidate's total
// (DESIGN.md:52).
func TestASilentPlayerLetsTheDiscussionTimerExpire(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("the discussion floor is 30 s and this test waits for all of it")
	}
	settings := fastSettings(1)
	res := play(t, MatchPlan{
		Name:     "3-player silent voter",
		Players:  3,
		Settings: settings,
		Strategy: "skip",
		Silent:   []int{2},
		// One player never answers, so the window cannot close early and a
		// second vote is guaranteed to land inside it. That makes
		// ALREADY_VOTED deterministic here and nowhere else.
		Provoke: true,
	})
	requireEnd(t, res,
		genpb.WinnerSide_WINNER_SIDE_IMPOSTER,
		genpb.MatchEndReason_MATCH_END_REASON_FINAL_ROUND_SURVIVED)

	floor := time.Duration(settings.GetDiscussSeconds()) * time.Second
	if res.Duration < floor {
		t.Errorf("the match took %s, less than the %s discussion window that had to expire",
			res.Duration.Round(time.Second), floor)
	}

	// Two of the three active players answered, both with Skip. The third said
	// nothing, and the tally must leave it out of every bucket rather than
	// rounding it up to a Skip.
	var tally *genpb.VoteTally
	for _, ev := range res.Reports[0].Transcript {
		if v := ev.GetVoteTally(); v != nil {
			tally = v
		}
	}
	if tally == nil {
		t.Fatal("no VoteTally reached the first socket")
	}
	if got, want := tally.GetSkipCount(), int32(2); got != want {
		t.Errorf("skip_count = %d, want %d: the absent vote was counted as a Skip", got, want)
	}
	if got, want := tally.GetActiveCount(), int32(3); got != want {
		t.Errorf("active_count = %d, want %d", got, want)
	}
	if len(tally.GetCounts()) != 0 {
		t.Errorf("the tally named %d candidates, want none", len(tally.GetCounts()))
	}

	// Exactly one socket never sent a vote, so exactly two got VoteAccepted.
	accepted := 0
	dupes := 0
	for _, rep := range res.Reports {
		accepted += rep.FrameCounts["VoteAccepted"]
		dupes += rep.ErrorCodes["ERROR_CODE_ALREADY_VOTED"]
	}
	if accepted != 2 {
		t.Errorf("%d votes were acknowledged, want 2", accepted)
	}
	// A vote is irreversible (DESIGN.md:49): both voters tried again inside the
	// open window and both were refused, with the first vote left standing —
	// which the tally above already proved, since it counted no candidate.
	if dupes != 2 {
		t.Errorf("%d second votes were refused with ALREADY_VOTED, want 2", dupes)
	}
}

// ---------------------------------------------------------------------------
// Harness self-checks
// ---------------------------------------------------------------------------

// TestTheLeakSearchActuallyFindsALeak proves the watchdog is not vacuous. A
// hand-built frame carrying another player's word must be caught by all three
// searches, including the squashed one that reassembles a secret split across
// two adjacent length-prefixed fields.
func TestTheLeakSearchActuallyFindsALeak(t *testing.T) {
	t.Parallel()

	const alpha = "VERSO-CANARY-ALPHA"
	w := NewWatchdog()
	w.RegisterWord("victim", alpha)
	w.RegisterWord("observer", "VERSO-CANARY-OMEGA")
	w.RegisterToken("victim", "0123456789abcdef0123456789abcdef")

	cases := []struct {
		name string
		ev   *genpb.ServerEvent
		want bool
	}{
		{
			name: "clean roster",
			ev: &genpb.ServerEvent{Evt: &genpb.ServerEvent_LobbyState{
				LobbyState: &genpb.LobbyState{Players: []*genpb.PlayerInfo{{Id: "victim", Name: "ada"}}},
			}},
		},
		{
			name: "word smuggled into a display name",
			ev: &genpb.ServerEvent{Evt: &genpb.ServerEvent_LobbyState{
				LobbyState: &genpb.LobbyState{Players: []*genpb.PlayerInfo{{Id: "victim", Name: "ada" + alpha}}},
			}},
			want: true,
		},
		{
			name: "word split across two adjacent string fields",
			ev: &genpb.ServerEvent{Evt: &genpb.ServerEvent_LobbyState{
				LobbyState: &genpb.LobbyState{Players: []*genpb.PlayerInfo{
					{Id: alpha[:9], Name: alpha[9:]},
				}},
			}},
			want: true,
		},
		{
			name: "seat token on somebody else's socket",
			ev: &genpb.ServerEvent{Evt: &genpb.ServerEvent_Error{
				Error: &genpb.Error{Message: "0123456789abcdef0123456789abcdef"},
			}},
			want: true,
		},
		{
			name: "the final reveal may carry every word",
			ev: &genpb.ServerEvent{Evt: &genpb.ServerEvent_MatchEnded{
				MatchEnded: &genpb.MatchEnded{ImposterWord: alpha},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := w.Inspect("observer", []string{"VERSO-CANARY-OMEGA"}, tc.ev)
			if (len(got) > 0) != tc.want {
				t.Fatalf("Inspect found %d leaks, want any = %v (%v)", len(got), tc.want, got)
			}
		})
	}
}

// TestTheHarnessRejectsAnImpossibleSeatCount keeps the driver honest about
// DESIGN.md:16 before it opens a socket.
func TestTheHarnessRejectsAnImpossibleSeatCount(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 2, 11} {
		if _, err := RunMatch(t.Context(), testServer.URL, MatchPlan{Players: n}); err == nil {
			t.Errorf("a %d-player plan was accepted", n)
		} else if !strings.Contains(err.Error(), "want 3..10") {
			t.Errorf("a %d-player plan failed with %v, want a seat-count complaint", n, err)
		}
	}
}
