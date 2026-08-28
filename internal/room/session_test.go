package room

// session_test.go — the room's side of connection lifecycle
// (PERFORMANCE_OPTIMIZATION_PLAN.md S1).
//
// A room that only ever replaces a seat's outbound queue leaves the displaced
// socket open: it answers pings, holds a connection slot and a 64-frame queue,
// and receives nothing ever again, because the seat has moved. Under reconnect
// churn — a phone walking between wifi and cellular — that accumulates.
//
// What is asserted here is the ORDER, not just the fact. The terminal Error has
// to be on the session's queue before the close is requested, or the client is
// disconnected with no reason for it; and the close must not discard what is
// already queued, which is why Session.Close is a request and not a teardown.

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	mrand "math/rand/v2"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// sessRoom is a lobby room with the host attached, and nothing else.
func sessRoom(t *testing.T) (*Room, string, string, *smokeSock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := New("SESS", "host", genpb.Avatar_AVATAR_BEETLE, Options{
		Deck:   pairDeck{"CAT", "DOG"},
		Rand:   mrand.New(mrand.NewPCG(11, 13)),
		Logger: discardLogger(),
	})
	hostID, hostTok := r.HostSeat()
	go r.run(ctx)
	t.Cleanup(func() {
		cancel()
		synctest.Wait()
	})

	s0 := newSmokeSock()
	if _, err := r.attach(hostTok, s0); err != nil {
		t.Fatalf("attach host: %v", err)
	}
	synctest.Wait()
	return r, hostID, hostTok, s0
}

// terminalError returns the last Error on a socket's queue and how many frames
// were drained to reach the end.
func terminalError(s *smokeSock) (*genpb.Error, int) {
	var last *genpb.Error
	n := 0
	for {
		select {
		case ev := <-s.ch:
			n++
			if e := ev.GetError(); e != nil {
				last = e
			}
		default:
			return last, n
		}
	}
}

// TestReclaimingASeatClosesTheDisplacedSession is the reconnect half of S1.
func TestReclaimingASeatClosesTheDisplacedSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _, hostTok, old := sessRoom(t)
		old.drain()

		fresh := newSmokeSock()
		if _, err := r.attach(hostTok, fresh); err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		synctest.Wait()

		// The explanation went out BEFORE the close was asked for, and the
		// close did not take it with it: both are still observable here.
		got, _ := terminalError(old)
		if got == nil {
			t.Fatal("the displaced session was closed without being told why")
		}
		if got.GetCode() != genpb.ErrorCode_ERROR_CODE_BAD_SEAT {
			t.Fatalf("displaced session got %v, want BAD_SEAT", got.GetCode())
		}
		if !old.wasClosed() {
			t.Fatal("the displaced session was left open; it will never receive another event")
		}
		if fresh.wasClosed() {
			t.Fatal("the reconnecting session was closed")
		}
	})
}

// TestRepeatedReclaimsCloseEveryDisplacedSession is the churn case. Every
// superseded socket has to go, not just the first: a reconnect loop is exactly
// the load this exists to survive.
func TestRepeatedReclaimsCloseEveryDisplacedSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _, hostTok, first := sessRoom(t)

		socks := []*smokeSock{first}
		for range 8 {
			sk := newSmokeSock()
			if _, err := r.attach(hostTok, sk); err != nil {
				t.Fatalf("reclaim: %v", err)
			}
			synctest.Wait()
			socks = append(socks, sk)
		}

		for i, sk := range socks[:len(socks)-1] {
			if !sk.wasClosed() {
				t.Errorf("displaced session %d was left open", i)
			}
			if got, _ := terminalError(sk); got.GetCode() != genpb.ErrorCode_ERROR_CODE_BAD_SEAT {
				t.Errorf("displaced session %d got %v, want BAD_SEAT", i, got.GetCode())
			}
		}
		if live := socks[len(socks)-1]; live.wasClosed() {
			t.Fatal("the surviving session was closed")
		}
	})
}

// TestALateDetachCannotUnseatTheReplacement is the race S1 must not open.
//
// A displaced socket finishes its own teardown after the reconnect has already
// taken the seat, and transport calls Detach from there. Identity is the only
// thing that distinguishes the two, which is why room.Session has to be
// comparable and why transport passes the conn itself rather than its queue.
func TestALateDetachCannotUnseatTheReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, hostID, hostTok, old := sessRoom(t)

		fresh := newSmokeSock()
		if _, err := r.attach(hostTok, fresh); err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		synctest.Wait()

		// The old socket's read loop only now notices it is gone.
		r.detach(hostID, old)
		synctest.Wait()

		connected := smokeGet(r, func(r *Room) bool {
			p := r.byID[hostID]
			return p != nil && p.Connected
		})
		if !connected {
			t.Fatal("a late detach from the displaced socket knocked the live seat offline")
		}

		// And the seat is still reachable: events go to the new session.
		fresh.drain()
		r.Submit(Command{PlayerID: hostID, Out: fresh, Cmd: &genpb.ClientCommand{
			Cid: "rdy", Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}}})
		synctest.Wait()
		if _, n := terminalError(fresh); n == 0 {
			t.Fatal("the live session stopped receiving after the late detach")
		}
	})
}

// TestKickClosesTheTargetSession is the kick half of S1.
//
// The plan's acceptance check, in one test: the target is told, its socket is
// closed rather than left to close itself, and the token it held is dead.
func TestKickClosesTheTargetSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(1, 10, 30), 7)
		defer h.stop()

		target := h.socks[2]
		targetTok := h.toks[2]
		target.drain()

		h.send(0, &genpb.ClientCommand{Cid: "k", Cmd: &genpb.ClientCommand_Kick{
			Kick: &genpb.KickPlayer{TargetPlayerId: h.ids[2]}}})
		synctest.Wait()

		got, _ := terminalError(target)
		if got == nil {
			t.Fatal("the kicked session was closed without being told why")
		}
		if got.GetCode() != genpb.ErrorCode_ERROR_CODE_KICKED {
			t.Fatalf("kicked session got %v, want KICKED", got.GetCode())
		}
		if !target.wasClosed() {
			t.Fatal("the kicked session was left open, holding a connection slot in a room it is not in")
		}

		// The seat token died with the seat: a client that ignores the error
		// and reconnects cannot walk back in.
		if _, err := h.r.attach(targetTok, newSmokeSock()); err != ErrBadSeat {
			t.Fatalf("a kicked seat token gave %v, want ErrBadSeat", err)
		}

		// Nobody else was closed.
		for i, sk := range h.socks[:2] {
			if sk.wasClosed() {
				t.Errorf("session %d was closed by someone else's kick", i)
			}
		}
	})
}

// TestAnOrdinaryDetachDoesNotCloseTheSession draws the line the other way. A
// socket that dropped on its own is already gone; the room has nothing to close
// and must not pretend the seat was taken away.
func TestAnOrdinaryDetachDoesNotCloseTheSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, hostID, _, s0 := sessRoom(t)
		r.detach(hostID, s0)
		synctest.Wait()

		if s0.wasClosed() {
			t.Fatal("a plain detach asked the session to close")
		}
		if got, _ := terminalError(s0); got != nil {
			t.Fatalf("a plain detach sent the client an error: %v", got.GetCode())
		}

		// And the seat survives its grace window, as it must.
		time.Sleep(GraceWindow / 2)
		synctest.Wait()
		if _, err := r.attach(func() string { _, tok := r.HostSeat(); return tok }(), newSmokeSock()); err != nil {
			t.Fatalf("the seat did not survive a plain detach: %v", err)
		}
	})
}
