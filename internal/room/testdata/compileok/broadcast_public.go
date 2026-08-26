// This package MUST compile.
//
// It is the positive control for TestBroadcastOfASecretDoesNotCompile: without
// it, the negative fixture next door would still "pass" if it failed to build
// for some unrelated reason — a renamed constructor, a moved import path, a
// broken generated file. Broadcasting a public event has to keep working for
// the non-compilation of the secret-bearing ones to mean anything.
package main

import (
	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/room"
)

func main() {
	r := room.New("TEST", "host", room.Options{})
	r.Broadcast(room.EvLobbyState{LobbyState: &genpb.LobbyState{}})
	r.Broadcast(room.EvMatchEnded{MatchEnded: &genpb.MatchEnded{}})
	r.Broadcast(room.EvStrokePoints{StrokePoints: &genpb.StrokePoints{}})
}
