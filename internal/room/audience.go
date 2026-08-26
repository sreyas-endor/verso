// Package room owns a single match: one goroutine, all state, no mutexes.
//
// This file is the type-level half of the secret-leak defense described in
// IMPLEMENTATION_PLAN.md §4.2. Every payload the room can put on a socket is
// wrapped in a thin value type declared here. The wrappers for room-wide events
// carry an unexported broadcastSafe() marker; the wrappers for the five
// player-private events deliberately do not, so
//
//	r.Broadcast(EvYourWord{...})
//
// does not compile:
//
//	cannot use EvYourWord{…} as Broadcastable value in argument to Broadcast:
//	  EvYourWord does not implement Broadcastable (missing method broadcastSafe)
//
// The wrappers exist because methods cannot be attached to types from another
// package, and — more importantly — because methods are invisible to every code
// generator. protoc-gen-go reads struct fields only, so re-running
// `buf generate` can never erase this defense.
package room

import (
	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// EventKind is a stable label for an event wrapper, for logs, metrics and
// tests. It is not on the wire; the oneof field number is.
type EventKind int

// Event kinds, broadcast half first.
const (
	KindUnspecified EventKind = iota
	KindLobbyState
	KindSettingsChanged
	KindRoundStarted
	KindTurnStarted
	KindStrokeBegan
	KindStrokePoints
	KindStrokeEnded
	KindPhaseChanged
	KindVoteCastCount
	KindVoteTally
	KindPlayerEliminated
	KindMatchEnded
	KindPlayerPresence
	KindError

	// Unicast only.
	KindJoined
	KindYourWord
	KindSnapshot
	KindSpectatorInfo
	KindVoteAccepted
)

var kindNames = [...]string{
	KindUnspecified:      "Unspecified",
	KindLobbyState:       "LobbyState",
	KindSettingsChanged:  "SettingsChanged",
	KindRoundStarted:     "RoundStarted",
	KindTurnStarted:      "TurnStarted",
	KindStrokeBegan:      "StrokeBegan",
	KindStrokePoints:     "StrokePoints",
	KindStrokeEnded:      "StrokeEnded",
	KindPhaseChanged:     "PhaseChanged",
	KindVoteCastCount:    "VoteCastCount",
	KindVoteTally:        "VoteTally",
	KindPlayerEliminated: "PlayerEliminated",
	KindMatchEnded:       "MatchEnded",
	KindPlayerPresence:   "PlayerPresence",
	KindError:            "Error",
	KindJoined:           "Joined",
	KindYourWord:         "YourWord",
	KindSnapshot:         "Snapshot",
	KindSpectatorInfo:    "SpectatorInfo",
	KindVoteAccepted:     "VoteAccepted",
}

func (k EventKind) String() string {
	if k < 0 || int(k) >= len(kindNames) {
		return "Unknown"
	}
	return kindNames[k]
}

// Event is any payload the room can deliver to a socket.
//
// eventKind is unexported on purpose: only this package can produce an Event,
// so no caller elsewhere in the tree can hand the room an arbitrary frame.
type Event interface {
	// Envelope wraps the payload in a ServerEvent, echoing the correlation id
	// of the command that caused it. Pass "" for a spontaneous event.
	Envelope(cid string) *genpb.ServerEvent
	eventKind() EventKind
}

// Broadcastable is an Event that every socket in the room may see.
//
// broadcastSafe is unexported, so no other package can opt a type in, and
// within this package adding the marker is a deliberate one-line act that shows
// up in review right next to the payload it blesses.
type Broadcastable interface {
	Event
	broadcastSafe()
}

// KindOf reports the kind of an event without exposing the marker method.
func KindOf(e Event) EventKind { return e.eventKind() }

// IsBroadcastable reports whether e carries the broadcast marker. Used by the
// canary test (IMPLEMENTATION_PLAN.md §6, step 10) to enumerate every frame that
// could reach a socket other than its subject's.
func IsBroadcastable(e Event) bool {
	_, ok := e.(Broadcastable)
	return ok
}

// ---------------------------------------------------------------------------
// Broadcast half. Each of these has broadcastSafe(); none of them has any field
// that could carry a player's word, except MatchEnded, which is emitted only
// once the match is over and the reveal is the point (DESIGN.md:75).
// ---------------------------------------------------------------------------

// EvLobbyState is the lobby roster and settings.
type EvLobbyState struct{ *genpb.LobbyState }

func (e EvLobbyState) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_LobbyState{LobbyState: e.LobbyState}}
}
func (EvLobbyState) eventKind() EventKind { return KindLobbyState }
func (EvLobbyState) broadcastSafe()       {}

// EvSettingsChanged announces clamped host settings.
type EvSettingsChanged struct{ *genpb.SettingsChanged }

func (e EvSettingsChanged) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_SettingsChanged{SettingsChanged: e.SettingsChanged}}
}
func (EvSettingsChanged) eventKind() EventKind { return KindSettingsChanged }
func (EvSettingsChanged) broadcastSafe()       {}

// EvRoundStarted opens a round and publishes the reshuffled turn order.
type EvRoundStarted struct{ *genpb.RoundStarted }

func (e EvRoundStarted) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_RoundStarted{RoundStarted: e.RoundStarted}}
}
func (EvRoundStarted) eventKind() EventKind { return KindRoundStarted }
func (EvRoundStarted) broadcastSafe()       {}

// EvTurnStarted names the current artist and starts their clock.
type EvTurnStarted struct{ *genpb.TurnStarted }

func (e EvTurnStarted) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_TurnStarted{TurnStarted: e.TurnStarted}}
}
func (EvTurnStarted) eventKind() EventKind { return KindTurnStarted }
func (EvTurnStarted) broadcastSafe()       {}

// EvStrokeBegan opens a stroke on every viewer's overlay layer.
type EvStrokeBegan struct{ *genpb.StrokeBegan }

func (e EvStrokeBegan) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_StrokeBegan{StrokeBegan: e.StrokeBegan}}
}
func (EvStrokeBegan) eventKind() EventKind { return KindStrokeBegan }
func (EvStrokeBegan) broadcastSafe()       {}

// EvStrokePoints appends to the open stroke. The room fills in StrokeId and Seq
// itself; the client-supplied values on the inbound command are discarded.
type EvStrokePoints struct{ *genpb.StrokePoints }

func (e EvStrokePoints) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_StrokePoints{StrokePoints: e.StrokePoints}}
}
func (EvStrokePoints) eventKind() EventKind { return KindStrokePoints }
func (EvStrokePoints) broadcastSafe()       {}

// EvStrokeEnded commits a stroke to the base layer.
type EvStrokeEnded struct{ *genpb.StrokeEnded }

func (e EvStrokeEnded) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_StrokeEnded{StrokeEnded: e.StrokeEnded}}
}
func (EvStrokeEnded) eventKind() EventKind { return KindStrokeEnded }
func (EvStrokeEnded) broadcastSafe()       {}

// EvPhaseChanged carries the authoritative clock for the new phase.
type EvPhaseChanged struct{ *genpb.PhaseChanged }

func (e EvPhaseChanged) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_PhaseChanged{PhaseChanged: e.PhaseChanged}}
}
func (EvPhaseChanged) eventKind() EventKind { return KindPhaseChanged }
func (EvPhaseChanged) broadcastSafe()       {}

// EvVoteCastCount is how many players have voted — never which ones.
type EvVoteCastCount struct{ *genpb.VoteCastCount }

func (e EvVoteCastCount) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_VoteCastCount{VoteCastCount: e.VoteCastCount}}
}
func (EvVoteCastCount) eventKind() EventKind { return KindVoteCastCount }
func (EvVoteCastCount) broadcastSafe()       {}

// EvVoteTally is the aggregate result. DESIGN.md:56 forbids ever publishing the
// voter-to-candidate mapping that produced it.
type EvVoteTally struct{ *genpb.VoteTally }

func (e EvVoteTally) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_VoteTally{VoteTally: e.VoteTally}}
}
func (EvVoteTally) eventKind() EventKind { return KindVoteTally }
func (EvVoteTally) broadcastSafe()       {}

// EvPlayerEliminated is the outcome of the tally, including "nobody".
type EvPlayerEliminated struct{ *genpb.PlayerEliminated }

func (e EvPlayerEliminated) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_PlayerEliminated{PlayerEliminated: e.PlayerEliminated}}
}
func (EvPlayerEliminated) eventKind() EventKind { return KindPlayerEliminated }
func (EvPlayerEliminated) broadcastSafe()       {}

// EvMatchEnded is the final reveal. It is the one broadcast that legitimately
// carries every player's word, and it is valid only in PHASE_ENDED
// (DESIGN.md:75). Build it in exactly one place, after the match is decided.
type EvMatchEnded struct{ *genpb.MatchEnded }

func (e EvMatchEnded) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_MatchEnded{MatchEnded: e.MatchEnded}}
}
func (EvMatchEnded) eventKind() EventKind { return KindMatchEnded }
func (EvMatchEnded) broadcastSafe()       {}

// EvPlayerPresence reports a connection-state or host change.
type EvPlayerPresence struct{ *genpb.PlayerPresence }

func (e EvPlayerPresence) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_PlayerPresence{PlayerPresence: e.PlayerPresence}}
}
func (EvPlayerPresence) eventKind() EventKind { return KindPlayerPresence }
func (EvPlayerPresence) broadcastSafe()       {}

// EvError reports a rejected command. Marked broadcast-safe because a room-wide
// shutdown notice uses it; most sends are still unicast via SendTo.
type EvError struct{ *genpb.Error }

func (e EvError) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_Error{Error: e.Error}}
}
func (EvError) eventKind() EventKind { return KindError }
func (EvError) broadcastSafe()       {}

// ---------------------------------------------------------------------------
// Unicast half. NONE of the five wrappers below has a broadcastSafe() method,
// and none may ever be given one. Each omission is load-bearing.
// ---------------------------------------------------------------------------

// EvJoined hands a player their seat token.
//
// DELIBERATELY NOT Broadcastable: the seat token is a bearer credential for one
// seat. Broadcasting it would let any player in the room steal that seat — and
// with it, that player's word.
type EvJoined struct{ *genpb.Joined }

func (e EvJoined) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_Joined{Joined: e.Joined}}
}
func (EvJoined) eventKind() EventKind { return KindJoined }

// EvYourWord carries a player's secret word.
//
// DELIBERATELY NOT Broadcastable. This is the single message the whole audience
// -typing scheme exists for (IMPLEMENTATION_PLAN.md §1). Adding broadcastSafe()
// here would silently ruin every match the server ever runs. Do not add it, do
// not embed *genpb.YourWord in a broadcast-safe wrapper, and do not add a word
// field to any type above.
type EvYourWord struct{ *genpb.YourWord }

func (e EvYourWord) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_YourWord{YourWord: e.YourWord}}
}
func (EvYourWord) eventKind() EventKind { return KindYourWord }

// EvSnapshot is a full resync for one player.
//
// DELIBERATELY NOT Broadcastable: Snapshot.YourWord is the recipient's own
// secret. It is built only by Room.viewFor, which is the only function in this
// package that reads Player.word.
type EvSnapshot struct{ *genpb.Snapshot }

func (e EvSnapshot) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_Snapshot{Snapshot: e.Snapshot}}
}
func (EvSnapshot) eventKind() EventKind { return KindSnapshot }

// EvSpectatorInfo tells one just-eliminated non-imposter who the imposter is
// (DESIGN.md:67).
//
// DELIBERATELY NOT Broadcastable: broadcasting the imposter's identity mid-match
// ends the game instantly and invisibly.
type EvSpectatorInfo struct{ *genpb.SpectatorInfo }

func (e EvSpectatorInfo) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_SpectatorInfo{SpectatorInfo: e.SpectatorInfo}}
}
func (EvSpectatorInfo) eventKind() EventKind { return KindSpectatorInfo }

// EvVoteAccepted privately confirms one voter's own choice.
//
// DELIBERATELY NOT Broadcastable: it names a voter and their candidate, which is
// precisely the pairing DESIGN.md:56 forbids disclosing to anyone else.
type EvVoteAccepted struct{ *genpb.VoteAccepted }

func (e EvVoteAccepted) Envelope(cid string) *genpb.ServerEvent {
	return &genpb.ServerEvent{Cid: cid, Evt: &genpb.ServerEvent_VoteAccepted{VoteAccepted: e.VoteAccepted}}
}
func (EvVoteAccepted) eventKind() EventKind { return KindVoteAccepted }

// Compile-time inventory. The first block must stay exhaustive over the
// broadcast half; the second asserts the private five are Events and says
// nothing more, because "does not implement Broadcastable" cannot be asserted
// positively in Go — that is what the milestone-3 non-compilation test and the
// milestone-10 canary test are for.
var (
	_ Broadcastable = EvLobbyState{}
	_ Broadcastable = EvSettingsChanged{}
	_ Broadcastable = EvRoundStarted{}
	_ Broadcastable = EvTurnStarted{}
	_ Broadcastable = EvStrokeBegan{}
	_ Broadcastable = EvStrokePoints{}
	_ Broadcastable = EvStrokeEnded{}
	_ Broadcastable = EvPhaseChanged{}
	_ Broadcastable = EvVoteCastCount{}
	_ Broadcastable = EvVoteTally{}
	_ Broadcastable = EvPlayerEliminated{}
	_ Broadcastable = EvMatchEnded{}
	_ Broadcastable = EvPlayerPresence{}
	_ Broadcastable = EvError{}

	_ Event = EvJoined{}
	_ Event = EvYourWord{}
	_ Event = EvSnapshot{}
	_ Event = EvSpectatorInfo{}
	_ Event = EvVoteAccepted{}
)
