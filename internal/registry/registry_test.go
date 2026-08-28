package registry

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	mrand "math/rand/v2"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/room"
)

type testDeck struct{}

func (testDeck) Pair(genpb.Difficulty, *mrand.Rand, []string) (string, string) { return "CAT", "DOG" }

func testConfig(cfg Config) Config {
	cfg.NewDeck = func() room.Deck { return testDeck{} }
	cfg.Logger = slog.New(slog.DiscardHandler)
	return cfg
}

// ---------------------------------------------------------------------------
// Join codes
// ---------------------------------------------------------------------------

func TestRandomCodeUsesTheUnambiguousAlphabet(t *testing.T) {
	// O/0 and I/1/L are the pairs people mis-transcribe when a code is read
	// aloud across a room. None of them may ever appear in a generated code.
	const banned = "O0I1L"

	seen := make(map[string]int)
	for range 20000 {
		code := randomCode()
		if len(code) != CodeLen {
			t.Fatalf("code %q has length %d, want %d", code, len(code), CodeLen)
		}
		for _, r := range code {
			if strings.ContainsRune(banned, r) {
				t.Fatalf("code %q contains ambiguous character %q", code, r)
			}
			if !strings.ContainsRune(Alphabet, r) {
				t.Fatalf("code %q contains out-of-alphabet character %q", code, r)
			}
		}
		seen[code]++
	}
	if len(seen) < 19000 {
		t.Fatalf("only %d distinct codes in 20000 draws; the generator is skewed", len(seen))
	}

	// The alphabet itself must not readmit an ambiguous pair by accident.
	for _, r := range banned {
		if strings.ContainsRune(Alphabet, r) {
			t.Fatalf("Alphabet contains the ambiguous character %q", r)
		}
	}
}

func TestGeneratedCodesNormaliseToThemselves(t *testing.T) {
	// A code the server prints must survive being typed back in unchanged, or
	// the room is unreachable by the only route a player has.
	for range 2000 {
		code := randomCode()
		if got := NormalizeCode(code); got != code {
			t.Fatalf("NormalizeCode(%q) = %q", code, got)
		}
		if got := NormalizeCode(" " + strings.ToLower(code) + " "); got != code {
			t.Fatalf("NormalizeCode of the lower-cased, padded form = %q, want %q", got, code)
		}
	}
}

func TestNormalizeCode(t *testing.T) {
	cases := map[string]string{
		"abcde":   "ABCDE",
		" AB-CDE": "ABCDE",
		"ab cd e": "ABCDE",
		// Out-of-alphabet characters are dropped, never substituted: guessing
		// would send a typo confidently into the wrong room.
		"ABOIL": "AB",
		"":      "",
	}
	for in, want := range cases {
		if got := NormalizeCode(in); got != want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAllocCodeRetriesOnCollision loads the table until roughly one draw in a
// hundred collides, then allocates twenty thousand codes.
//
// Not one of them may be a code already in the table. With ~0.9% of the space
// occupied, ~175 of those first draws collide, so the only way the assertion
// below can hold is if the retry loop ran — statistically it must have run
// about that many times.
func TestAllocCodeRetriesOnCollision(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a large table")
	}
	const occupied = 250_000
	const draws = 20_000

	g := &Registry{rooms: make(map[string]*entry, occupied)}
	for len(g.rooms) < occupied {
		g.rooms[randomCode()] = nil
	}

	for range draws {
		g.mu.Lock()
		code, err := g.allocCodeLocked()
		if err != nil {
			g.mu.Unlock()
			t.Fatalf("allocCodeLocked failed with %d of ~28.6M codes taken: %v", occupied, err)
		}
		if _, taken := g.rooms[code]; taken {
			g.mu.Unlock()
			t.Fatalf("allocCodeLocked returned %q, which is already in use", code)
		}
		// Claim it, so later draws must dodge it too.
		g.rooms[code] = nil
		g.mu.Unlock()
	}
	expectedRetries := float64(draws) * float64(occupied) / (31.0 * 31 * 31 * 31 * 31)
	t.Logf("%d allocations against a %.2f%% full table, ~%.0f of them collided on the "+
		"first draw and were retried",
		draws, 100*float64(occupied)/(31*31*31*31*31), expectedRetries)
}

// ---------------------------------------------------------------------------
// Caps and lifecycle
// ---------------------------------------------------------------------------

func TestLiveRoomCapIsEnforced(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	g := New(ctx, testConfig(Config{MaxRooms: 3}))
	t.Cleanup(func() { _ = g.Close(context.Background()) })

	for i := range 3 {
		if _, err := g.Create("host", genpb.Avatar_AVATAR_BEETLE); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := g.Create("host", genpb.Avatar_AVATAR_BEETLE); err != ErrTooManyRooms {
		t.Fatalf("the fourth create gave %v, want ErrTooManyRooms", err)
	}
	if n := g.Count(); n != 3 {
		t.Fatalf("Count = %d, want 3", n)
	}
}

func TestCreateAfterCloseIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	g := New(ctx, testConfig(Config{}))
	if err := g.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Create("host", genpb.Avatar_AVATAR_BEETLE); err != ErrClosed {
		t.Fatalf("create after Close gave %v, want ErrClosed", err)
	}
	// Close is documented as safe to call more than once.
	if err := g.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestHoldReleaseAndLookupAccounting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	g := New(ctx, testConfig(Config{}))
	t.Cleanup(func() { _ = g.Close(context.Background()) })

	c, err := g.Create("host", genpb.Avatar_AVATAR_BEETLE)
	if err != nil {
		t.Fatal(err)
	}

	// Create leaves one reference held on the caller's behalf.
	g.mu.Lock()
	holds := g.rooms[c.Code].holds
	g.mu.Unlock()
	if holds != 1 {
		t.Fatalf("a fresh room has %d holds, want the caller's 1", holds)
	}

	if _, ok := g.Lookup(strings.ToLower(c.Code)); !ok {
		t.Fatal("Lookup does not normalise the code")
	}
	if _, ok := g.Lookup("ZZZZZ"); ok {
		t.Fatal("Lookup found a room that does not exist")
	}

	if !g.Hold(c.Code) {
		t.Fatal("Hold refused a live room")
	}
	if g.Hold("ZZZZZ") {
		t.Fatal("Hold succeeded on a room that does not exist")
	}
	g.Release(c.Code)
	g.Release(c.Code)
	// Release past zero must not underflow into a negative count, which would
	// make the room uncollectable.
	g.Release(c.Code)
	g.mu.Lock()
	holds, empty := g.rooms[c.Code].holds, g.rooms[c.Code].emptySince
	g.mu.Unlock()
	if holds != 0 {
		t.Fatalf("holds = %d after over-releasing, want 0", holds)
	}
	if empty.IsZero() {
		t.Fatal("the empty-grace clock did not start when the last hold went")
	}

	// Releasing an unknown code is a no-op, not a panic.
	g.Release("ZZZZZ")

	// A closing room is invisible to Lookup and to Hold, so a new socket can
	// never bind to a room whose actor is on its way out.
	g.RoomClosed(c.Code)
	if _, ok := g.Lookup(c.Code); ok {
		t.Fatal("Lookup returned a closing room")
	}
	if g.Hold(c.Code) {
		t.Fatal("Hold succeeded on a closing room")
	}
}

// ---------------------------------------------------------------------------
// Garbage collection
// ---------------------------------------------------------------------------

// TestGCCollectsAnEmptyRoomAndTheActorExits runs in virtual time, because the
// empty grace is floored at room.GraceWindow + 30 s and waiting that out for
// real would be a minute and a half of nothing.
//
// Count() reaching zero IS the goroutine-exit signal: the entry is removed by
// the goroutine that ran the actor, on the line after rm.Run returns.
func TestGCCollectsAnEmptyRoomAndTheActorExits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		g := New(ctx, testConfig(Config{SweepInterval: time.Second}))

		c, err := g.Create("host", genpb.Avatar_AVATAR_BEETLE)
		if err != nil {
			t.Fatal(err)
		}
		grace := g.cfg.EmptyGrace
		if grace < room.GraceWindow {
			t.Fatalf("EmptyGrace = %v, shorter than the reconnect window", grace)
		}

		// Still held by its creator: the collector must leave it alone however
		// long it waits, or a room dies in the gap before the host's socket
		// attaches.
		time.Sleep(grace * 2)
		synctest.Wait()
		if n := g.Count(); n != 1 {
			t.Fatalf("a held room was collected after %v", grace*2)
		}

		g.Release(c.Code)
		synctest.Wait()

		// Not yet: the disconnected players inside still have their window.
		time.Sleep(grace / 2)
		synctest.Wait()
		if n := g.Count(); n != 1 {
			t.Fatal("the room was collected before its empty grace expired")
		}

		time.Sleep(grace + 2*g.cfg.SweepInterval)
		synctest.Wait()
		if n := g.Count(); n != 0 {
			t.Fatalf("Count = %d after the empty grace expired; the room actor "+
				"never returned and its stroke log is still resident", n)
		}
		if _, ok := g.Lookup(c.Code); ok {
			t.Fatal("a collected room is still reachable by code")
		}

		cancel()
		synctest.Wait()
	})
}

// chanSession adapts a buffered channel to room.Session. The registry has no
// opinion about what a connection is; it only ever hands one to a room.
type chanSession struct{ ch chan *genpb.ServerEvent }

func (s *chanSession) Send(ev *genpb.ServerEvent) {
	select {
	case s.ch <- ev:
	default:
	}
}

func (s *chanSession) Close() {}

// TestGCCollectsARoomWhoseSeatsAllExpired is the other GC route: the room
// closes itself once its last seat times out, and the registry follows.
func TestGCCollectsARoomWhoseSeatsAllExpired(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		g := New(ctx, testConfig(Config{SweepInterval: time.Second}))

		c, err := g.Create("host", genpb.Avatar_AVATAR_BEETLE)
		if err != nil {
			t.Fatal(err)
		}
		// Generously buffered rather than drained by a goroutine: an unclosed
		// reader would still be parked when the bubble ends. One session per
		// channel and reused, because the room tells sockets apart by identity.
		out := &chanSession{ch: make(chan *genpb.ServerEvent, 4096)}
		if _, err := c.Room.Attach(c.HostToken, out); err != nil {
			t.Fatal(err)
		}

		playerID, _ := c.Room.HostSeat()
		c.Room.Detach(playerID, out)
		synctest.Wait()

		// Past the seat's grace window the lobby frees the seat outright; with
		// no seats left the room closes itself on its next liveness sweep.
		time.Sleep(room.GraceWindow + 3*room.SweepInterval)
		synctest.Wait()

		// The registry still holds the caller's reference, so this is entirely
		// the room's own decision to stop.
		if n := g.Count(); n != 0 {
			t.Fatalf("Count = %d; a room with no seats left did not close", n)
		}
		cancel()
		synctest.Wait()
	})
}

func TestHardTTLCollectsEvenABusyRoom(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		g := New(ctx, testConfig(Config{
			SweepInterval: time.Second,
			HardTTL:       2 * time.Minute,
		}))

		c, err := g.Create("host", genpb.Avatar_AVATAR_BEETLE)
		if err != nil {
			t.Fatal(err)
		}
		// Held the whole time — a party game left open overnight is a leak, not
		// a session.
		if !g.Hold(c.Code) {
			t.Fatal("Hold refused a fresh room")
		}

		time.Sleep(3 * time.Minute)
		synctest.Wait()
		if n := g.Count(); n != 0 {
			t.Fatalf("Count = %d past the hard TTL", n)
		}
		cancel()
		synctest.Wait()
	})
}

// TestCollectedRoomsLeaveNoGoroutines is the same property measured the blunt
// way, in real time, because runtime.NumGoroutine cannot see inside a synctest
// bubble's accounting the way this test wants to.
func TestCollectedRoomsLeaveNoGoroutines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Let anything left over from an earlier test settle first.
	for range 20 {
		if runtime.NumGoroutine() < 30 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := runtime.NumGoroutine()

	g := New(ctx, testConfig(Config{
		SweepInterval: 5 * time.Millisecond,
		HardTTL:       time.Millisecond,
	}))

	const rooms = 25
	for i := range rooms {
		if _, err := g.Create("host", genpb.Avatar_AVATAR_BEETLE); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if n := runtime.NumGoroutine(); n < before+rooms {
		t.Fatalf("%d rooms started but only %d new goroutines exist", rooms, n-before)
	}

	deadline := time.Now().Add(10 * time.Second)
	for g.Count() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := g.Count(); n != 0 {
		t.Fatalf("%d rooms survived the hard TTL", n)
	}
	if err := g.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancel()

	// The collector goroutine is gone too once Close returns.
	after := runtime.NumGoroutine()
	for range 200 {
		if after <= before+2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
		after = runtime.NumGoroutine()
	}
	if after > before+2 {
		t.Fatalf("goroutines went %d -> %d across %d created and collected rooms",
			before, after, rooms)
	}
}

func TestCloseStopsEveryRoom(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	g := New(ctx, testConfig(Config{SweepInterval: time.Hour}))
	for range 10 {
		if _, err := g.Create("host", genpb.Avatar_AVATAR_BEETLE); err != nil {
			t.Fatal(err)
		}
	}

	// Close must not report success while an actor is still starting up: the
	// WaitGroup count is taken under the same lock that publishes the shutdown
	// flag, precisely so this cannot race.
	done := make(chan error, 1)
	go func() { done <- g.Close(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return")
	}
	if n := g.Count(); n != 0 {
		t.Fatalf("%d rooms outlived Close", n)
	}
}

func TestEmptyGraceCoversTheReconnectWindow(t *testing.T) {
	// A room collected while a player is still inside their reconnect window is
	// the precise bug the grace window exists to prevent.
	cfg := Config{EmptyGrace: 1}.withDefaults()
	if cfg.EmptyGrace <= room.GraceWindow {
		t.Fatalf("EmptyGrace = %v, want comfortably more than room.GraceWindow (%v)",
			cfg.EmptyGrace, room.GraceWindow)
	}
}
