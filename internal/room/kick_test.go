package room

// kick_test.go — the host's lobby kick (reconnect.go, onKick).
//
// The kick is deliberately the narrowest possible feature: lobby only, where a
// seat holds nothing, so the whole operation is removeSeat. These tests pin
// that narrowness — every guard, and the two things the removal must actually
// accomplish (the target is told, and their token stops working).

import (
	"testing"
	"testing/synctest"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

func kickCmd(cid, targetID string) *genpb.ClientCommand {
	return &genpb.ClientCommand{
		Cid: cid,
		Cmd: &genpb.ClientCommand_Kick{Kick: &genpb.KickPlayer{TargetPlayerId: targetID}},
	}
}

// TestKickRemovesSeat is the happy path: the roster shrinks, the target is told
// why, and the surviving seats get a LobbyState that no longer lists them.
func TestKickRemovesSeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(1, 5, 30), 7)
		defer h.stop()
		h.discard()

		target := h.ids[2]
		h.send(0, kickCmd("k1", target))
		synctest.Wait()

		if got := h.seatCount(); got != 3 {
			t.Fatalf("seats = %d, want 3", got)
		}
		if h.indexOf(target) >= 0 && h.active(2) {
			t.Fatal("kicked player is still active")
		}

		// The target learns why rather than watching the room go quiet.
		if !hasErrorCode(h.drain(2), genpb.ErrorCode_ERROR_CODE_KICKED) {
			t.Fatal("target never received ERROR_CODE_KICKED")
		}

		// Everyone else sees the shrunk roster, correlated to the host's cid.
		ls := lastLobbyState(h.drain(1))
		if ls == nil {
			t.Fatal("no LobbyState after the kick")
		}
		if len(ls.GetPlayers()) != 3 {
			t.Fatalf("LobbyState lists %d players, want 3", len(ls.GetPlayers()))
		}
		for _, p := range ls.GetPlayers() {
			if p.GetId() == target {
				t.Fatal("kicked player still on the roster")
			}
		}
	})
}

// TestKickInvalidatesSeatToken is the difference between a kick and a nudge: the
// bearer token that would have reclaimed that seat has to stop working, or the
// target reconnects straight back into the room they were removed from.
func TestKickInvalidatesSeatToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(1, 5, 30), 11)
		defer h.stop()

		tok := h.toks[2]
		h.send(0, kickCmd("", h.ids[2]))
		synctest.Wait()

		sk := newSmokeSock()
		if _, err := h.r.attach(tok, sk); err != ErrBadSeat {
			t.Fatalf("attach with a kicked token: err = %v, want ErrBadSeat", err)
		}
	})
}

// TestKickIsNotABan states the limit out loud. There is no stable identity to
// ban on, so the room code keeps working; only the seat is gone.
func TestKickIsNotABan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(1, 5, 30), 13)
		defer h.stop()

		h.send(0, kickCmd("", h.ids[2]))
		synctest.Wait()

		sk := newSmokeSock()
		if _, _, err := h.r.seat("returning", genpb.Avatar_AVATAR_COURIER, sk); err != nil {
			t.Fatalf("fresh seat after a kick: %v", err)
		}
		if got := h.seatCount(); got != 4 {
			t.Fatalf("seats = %d, want 4 after the fresh join", got)
		}
	})
}

// TestKickRejected covers every guard in one table. Each case must leave the
// roster untouched — a rejected kick that still removed somebody is the worst
// possible failure here.
func TestKickRejected(t *testing.T) {
	cases := map[string]struct {
		from   int
		target func(h *harness) string
		want   genpb.ErrorCode
	}{
		"non-host": {
			from:   1,
			target: func(h *harness) string { return h.ids[2] },
			want:   genpb.ErrorCode_ERROR_CODE_NOT_HOST,
		},
		"host kicking themself": {
			from:   0,
			target: func(h *harness) string { return h.ids[0] },
			want:   genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND,
		},
		"unknown player": {
			from:   0,
			target: func(*harness) string { return "no-such-player" },
			want:   genpb.ErrorCode_ERROR_CODE_INVALID_COMMAND,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, 4, mkSettings(1, 5, 30), 17)
				defer h.stop()
				h.discard()

				h.send(tc.from, kickCmd("k", tc.target(h)))
				synctest.Wait()

				if !hasErrorCode(h.drain(tc.from), tc.want) {
					t.Fatalf("sender did not receive %v", tc.want)
				}
				if got := h.seatCount(); got != 4 {
					t.Fatalf("seats = %d, want 4 — a rejected kick removed somebody", got)
				}
			})
		})
	}
}

// TestKickRejectedOutsideLobby is the load-bearing guard. Past the lobby a seat
// holds a word, a place in the turn order and a place in the vote denominator;
// removing it would have to decide what happens when the target is the
// imposter, and that cannot be decided without telling the room who they are.
func TestKickRejectedOutsideLobby(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(1, 5, 30), 19)
		defer h.stop()
		h.start()
		h.discard()

		h.send(0, kickCmd("k", h.ids[2]))
		synctest.Wait()

		if !hasErrorCode(h.drain(0), genpb.ErrorCode_ERROR_CODE_WRONG_PHASE) {
			t.Fatal("host did not receive ERROR_CODE_WRONG_PHASE")
		}
		if got := h.seatCount(); got != 4 {
			t.Fatalf("seats = %d, want 4 — a kick landed mid-match", got)
		}
		if h.eliminated(2) {
			t.Fatal("target was eliminated by a rejected kick")
		}
	})
}

// TestKickRecomputesCanStart: dropping below MinPlayers has to close the start
// button, or the host is left looking at a control the server would refuse.
func TestKickRecomputesCanStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 3, mkSettings(1, 5, 30), 23)
		defer h.stop()
		h.discard()

		h.send(0, kickCmd("k", h.ids[2]))
		synctest.Wait()

		ls := lastLobbyState(h.drain(0))
		if ls == nil {
			t.Fatal("no LobbyState after the kick")
		}
		if ls.GetCanStart() {
			t.Fatalf("can_start is true with %d seats, want false", len(ls.GetPlayers()))
		}
	})
}

// TestKickDisconnectedSeat is the case a host actually reaches for: somebody
// closed the tab and their seat is sitting in the grace window, holding a slot
// in a 10-player room. Delivering to a socket that is already gone must be a
// no-op, not a panic.
func TestKickDisconnectedSeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(1, 5, 30), 29)
		defer h.stop()

		h.r.Detach(h.ids[2], h.socks[2])
		synctest.Wait()
		h.discard()

		h.send(0, kickCmd("k", h.ids[2]))
		synctest.Wait()

		if got := h.seatCount(); got != 3 {
			t.Fatalf("seats = %d, want 3", got)
		}
		if codes := errorCodes(h.drain(0)); len(codes) != 0 {
			t.Fatalf("host received %v, want no error", codes)
		}
	})
}
