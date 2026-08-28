package room

// avatar_test.go — the avatar is seat state.
//
// It is chosen once, when the seat is created, and never again. That single
// decision is what makes it survive a reconnect for free: attach does not read
// the avatar off the join frame, exactly as it does not read the display name,
// so there is no second writer to disagree with the first.
//
// The kick regression at the bottom lives here because this change touched the
// seat, and the kick is the one path that destroys one.

import (
	"context"
	"testing"
	"testing/synctest"

	mrand "math/rand/v2"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// avRoom is a lobby room with the host attached and nothing else, with the
// host's avatar under the test's control.
func avRoom(t *testing.T, hostAvatar genpb.Avatar) (*Room, string, string, *smokeSock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := New("AVTR", "host", hostAvatar, Options{
		Deck:   pairDeck{"CAT", "DOG"},
		Rand:   mrand.New(mrand.NewPCG(5, 7)),
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

// avatarOf reads a seat's stored avatar from the actor goroutine.
func avatarOf(r *Room, id string) genpb.Avatar {
	return smokeGet(r, func(r *Room) genpb.Avatar {
		p := r.byID[id]
		if p == nil {
			return genpb.Avatar_AVATAR_UNSPECIFIED
		}
		return p.Avatar
	})
}

// rosterAvatar finds a seat's avatar in the last LobbyState on a socket, which
// is the only view of the roster another player ever gets.
func rosterAvatar(evs []*genpb.ServerEvent, id string) (genpb.Avatar, bool) {
	ls := lastLobbyState(evs)
	if ls == nil {
		return genpb.Avatar_AVATAR_UNSPECIFIED, false
	}
	for _, pi := range ls.GetPlayers() {
		if pi.GetId() == id {
			return pi.GetAvatar(), true
		}
	}
	return genpb.Avatar_AVATAR_UNSPECIFIED, false
}

// TestSeatStoresAndPublishesTheAvatar is the whole feature in one pass: the
// choice reaches the seat, and it reaches every other client on the public
// roster. An avatar nobody else can see is a private preference, not a face.
func TestSeatStoresAndPublishesTheAvatar(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, hostID, _, s0 := avRoom(t, genpb.Avatar_AVATAR_MASON)
		s0.drain()

		id, _, err := r.seat("bee", genpb.Avatar_AVATAR_LANTERN, newSmokeSock())
		if err != nil {
			t.Fatalf("seat: %v", err)
		}
		synctest.Wait()

		if got := avatarOf(r, id); got != genpb.Avatar_AVATAR_LANTERN {
			t.Fatalf("seated avatar = %v, want LANTERN", got)
		}
		if got := avatarOf(r, hostID); got != genpb.Avatar_AVATAR_MASON {
			t.Fatalf("host avatar = %v, want MASON — New dropped the choice", got)
		}

		evs := s0.drain()
		got, ok := rosterAvatar(evs, id)
		if !ok {
			t.Fatal("the new seat never reached the host's roster")
		}
		if got != genpb.Avatar_AVATAR_LANTERN {
			t.Fatalf("roster avatar = %v, want LANTERN", got)
		}
		if got, _ := rosterAvatar(evs, hostID); got != genpb.Avatar_AVATAR_MASON {
			t.Fatalf("host's roster avatar = %v, want MASON", got)
		}
	})
}

// TestTwoSeatsMayShareAnAvatar states the deliberate absence of a rule. There is
// no per-room uniqueness check, because reserving avatars turns joining into a
// negotiation — a second player picking the beetle would have to be refused, or
// silently repainted, in the one moment they are least able to understand why.
func TestTwoSeatsMayShareAnAvatar(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, hostID, _, _ := avRoom(t, genpb.Avatar_AVATAR_PIPER)

		id, _, err := r.seat("twin", genpb.Avatar_AVATAR_PIPER, newSmokeSock())
		if err != nil {
			t.Fatalf("seat with the host's avatar: %v", err)
		}
		synctest.Wait()

		if got := avatarOf(r, id); got != genpb.Avatar_AVATAR_PIPER {
			t.Fatalf("second seat's avatar = %v, want PIPER", got)
		}
		if got := avatarOf(r, hostID); got != genpb.Avatar_AVATAR_PIPER {
			t.Fatalf("host's avatar = %v after a twin joined, want PIPER", got)
		}
		if got := smokeGet(r, func(r *Room) int { return len(r.players) }); got != 2 {
			t.Fatalf("seats = %d, want 2 — the duplicate was refused", got)
		}
	})
}

// TestAvatarFallsBackToBeetle pins the normalization. A seat stored with the
// zero value has no face for any client to draw, so an unstated or unknown
// choice becomes the designated fallback rather than a join failure — a client
// that omits the field, or one built against a longer enum than this server
// knows, still gets to play.
func TestAvatarFallsBackToBeetle(t *testing.T) {
	cases := map[string]struct {
		in   genpb.Avatar
		want genpb.Avatar
	}{
		"unspecified":  {genpb.Avatar_AVATAR_UNSPECIFIED, genpb.Avatar_AVATAR_BEETLE},
		"out of range": {genpb.Avatar(9999), genpb.Avatar_AVATAR_BEETLE},
		"negative":     {genpb.Avatar(-3), genpb.Avatar_AVATAR_BEETLE},
		"known value":  {genpb.Avatar_AVATAR_CARTOGRAPHER, genpb.Avatar_AVATAR_CARTOGRAPHER},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := normalizeAvatar(tc.in); got != tc.want {
				t.Fatalf("normalizeAvatar(%d) = %v, want %v", tc.in, got, tc.want)
			}

			synctest.Test(t, func(t *testing.T) {
				// Both writers, because there are exactly two: the host seat is
				// minted by New and every other seat by seatOnActor.
				r, hostID, _, _ := avRoom(t, tc.in)
				if got := avatarOf(r, hostID); got != tc.want {
					t.Fatalf("host seat stored %v, want %v", got, tc.want)
				}
				id, _, err := r.seat("bee", tc.in, newSmokeSock())
				if err != nil {
					t.Fatalf("seat: %v", err)
				}
				if got := avatarOf(r, id); got != tc.want {
					t.Fatalf("fresh seat stored %v, want %v", got, tc.want)
				}
			})
		})
	}
}

// TestReconnectKeepsTheSeatedAvatar is the reason the avatar is seat state at
// all. The join frame that reclaims a seat carries a display name and an avatar
// like any other, and attach reads neither: the room the rest of the table is
// looking at does not get repainted because somebody's client came back with a
// different local preference, or with none at all.
func TestReconnectKeepsTheSeatedAvatar(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _, _, s0 := avRoom(t, genpb.Avatar_AVATAR_BEETLE)

		first := newSmokeSock()
		id, tok, err := r.seat("bee", genpb.Avatar_AVATAR_SCOUT, first)
		if err != nil {
			t.Fatalf("seat: %v", err)
		}
		synctest.Wait()

		r.Detach(id, first)
		synctest.Wait()
		s0.drain()

		// A different avatar in the reclaiming frame has nowhere to land:
		// Attach takes a seat token and a socket, and that is the guarantee.
		if _, err := r.attach(tok, newSmokeSock()); err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		synctest.Wait()

		if got := avatarOf(r, id); got != genpb.Avatar_AVATAR_SCOUT {
			t.Fatalf("avatar after a reconnect = %v, want SCOUT", got)
		}
		if got, ok := rosterAvatar(s0.drain(), id); !ok || got != genpb.Avatar_AVATAR_SCOUT {
			t.Fatalf("roster avatar after a reconnect = %v (found %v), want SCOUT", got, ok)
		}
	})
}

// TestKickStillOnlyKicks is the regression guard on the path this change came
// closest to. A kick removes a lobby seat and tells its holder why; it is not an
// elimination, and it must never broadcast one — a room that saw PlayerEliminated
// for a kicked player would start showing them as voted out.
func TestKickStillOnlyKicks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 4, mkSettings(1, 5, 30), 31)
		defer h.stop()
		h.discard()

		h.send(0, kickCmd("k", h.ids[2]))
		synctest.Wait()

		all := h.drainAll()
		if !hasErrorCode(all[2], genpb.ErrorCode_ERROR_CODE_KICKED) {
			t.Fatal("target never received ERROR_CODE_KICKED")
		}
		for _, code := range errorCodes(all[2]) {
			if code != genpb.ErrorCode_ERROR_CODE_KICKED {
				t.Fatalf("target also received %v", code)
			}
		}
		for i, evs := range all {
			if lastEliminated(evs) != nil {
				t.Fatalf("seat %d received PlayerEliminated for a kick", i)
			}
		}
	})
}
