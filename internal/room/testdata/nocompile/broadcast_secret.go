// This package must NEVER compile.
//
// It is the fixture behind TestBroadcastOfASecretDoesNotCompile
// (internal/room/canary_test.go), which is defense 1 of
// IMPLEMENTATION_PLAN.md §1 — the type-level half of the secret-leak guard,
// milestone 3 in §6.
//
// Every call below hands Room.Broadcast one of the five unicast-only event
// wrappers. None of them carries the unexported broadcastSafe marker, so each
// line is a compile error, and the test asserts on the compiler's own words.
//
// It lives under testdata/ because the go tool ignores that directory for
// wildcard patterns: `go build ./...` and `go vet ./...` never see it, while
// the explicit package path the test uses still builds it.
package main

import (
	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/room"
)

func main() {
	r := room.New("TEST", "host", room.Options{})

	// The one this whole scheme exists for: a player's secret word.
	r.Broadcast(room.EvYourWord{YourWord: &genpb.YourWord{Word: "SECRET_CANARY_ALPHA"}})

	// Snapshot.your_word is the recipient's own secret.
	r.Broadcast(room.EvSnapshot{Snapshot: &genpb.Snapshot{}})

	// Joined.seat_token is a bearer credential for one seat.
	r.Broadcast(room.EvJoined{Joined: &genpb.Joined{}})

	// SpectatorInfo names the imposter.
	r.Broadcast(room.EvSpectatorInfo{SpectatorInfo: &genpb.SpectatorInfo{}})

	// VoteAccepted pairs a voter with their candidate.
	r.Broadcast(room.EvVoteAccepted{VoteAccepted: &genpb.VoteAccepted{}})
}
