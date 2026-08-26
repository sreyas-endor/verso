package main

// watchdog.go — the client-side half of the secret-leak defense
// (IMPLEMENTATION_PLAN.md §1).
//
// The server has three defenses: the Broadcastable marker (compile time), the
// single viewFor reader (structural), and the canary test (empirical, but from
// inside package room). This is the fourth and the only one that looks at what
// actually came back down a socket.
//
// Every bot hands every frame it receives to Inspect before doing anything with
// it. The Watchdog knows every bot's word and seat token, so it can answer the
// question a single client cannot: "does this frame contain a secret that is
// not mine?"
//
// Scan surface. The frame is marshalled back to bytes and searched three ways:
//
//  1. the raw bytes, verbatim;
//  2. the raw bytes case-folded;
//  3. the raw bytes with every non-printable byte removed, which reassembles a
//     secret split across two adjacent length-prefixed fields — a plain
//     bytes.Contains cannot see "Cat" and "alog" in neighbouring string fields
//     because the tag and length bytes sit between them.
//
// Stroke coordinate arrays are excluded from the scan. They are packed zigzag
// varints, so a short word's bytes turn up in them by chance often enough to
// make the check flaky, and a word smuggled through a numeric array is not a
// failure mode any real leak has. Every string field is scanned. A secret of
// eight bytes or more is additionally scanned against the unmodified frame,
// coordinates included, because at that length a chance collision is no longer
// credible — which is why the driver deals a canary deck.

import (
	"bytes"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// fullScanMinLen is the secret length above which the raw frame, stroke
// coordinates included, is scanned as well.
const fullScanMinLen = 8

// Leak is one secret found where it does not belong.
type Leak struct {
	// Observer is the player whose socket the frame arrived on.
	Observer string
	// Frame names the ServerEvent variant that carried it.
	Frame string
	// Owner is the player the secret belongs to.
	Owner string
	// Kind is "word" or "seat_token".
	Kind string
	// How is which of the three searches found it.
	How string
}

func (l Leak) String() string {
	return fmt.Sprintf("%s frame reaching %s carried %s's %s (%s match)",
		l.Frame, l.Observer, l.Owner, l.Kind, l.How)
}

// Watchdog holds every bot's secrets and checks frames against all of them. It
// is shared by every bot at one table, so it is the one place in this package
// that needs a mutex.
type Watchdog struct {
	mu     sync.Mutex
	words  map[string]string // playerID -> assigned word
	tokens map[string]string // playerID -> seat token
	leaks  []Leak
}

func NewWatchdog() *Watchdog {
	return &Watchdog{
		words:  make(map[string]string),
		tokens: make(map[string]string),
	}
}

// RegisterToken records a bot's seat token. Called as soon as Joined arrives.
func (w *Watchdog) RegisterToken(playerID, token string) {
	if playerID == "" || token == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tokens[playerID] = token
}

// RegisterWord records a bot's assigned word. Called as soon as YourWord
// arrives.
func (w *Watchdog) RegisterWord(playerID, word string) {
	if playerID == "" || word == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.words[playerID] = word
}

// Words returns a copy of everything registered so far.
func (w *Watchdog) Words() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]string, len(w.words))
	for k, v := range w.words {
		out[k] = v
	}
	return out
}

// Leaks returns every leak recorded so far.
func (w *Watchdog) Leaks() []Leak {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Leak(nil), w.leaks...)
}

// Inspect checks one frame against every secret registered at this table and
// records — and returns — anything it should not contain.
//
// observer is the player whose socket received it; ownWord is that player's own
// word, which is legitimate in its own YourWord and Snapshot and in nothing
// else. MatchEnded is exempt from the word scan entirely: it is the final
// reveal, the one broadcast the protocol allows to carry every word, and it is
// only ever emitted in PHASE_ENDED (game.proto MatchEnded).
func (w *Watchdog) Inspect(observer, ownWord string, ev *genpb.ServerEvent) []Leak {
	if ev == nil {
		return nil
	}
	name := frameName(ev)

	strippedRaw, err := proto.Marshal(stripCoordinates(ev))
	if err != nil {
		return nil
	}
	fullRaw, err := proto.Marshal(ev)
	if err != nil {
		return nil
	}
	needles := scanTargets(strippedRaw)
	fullNeedles := scanTargets(fullRaw)

	w.mu.Lock()
	defer w.mu.Unlock()

	var found []Leak
	record := func(owner, kind, how string) {
		l := Leak{Observer: observer, Frame: name, Owner: owner, Kind: kind, How: how}
		w.leaks = append(w.leaks, l)
		found = append(found, l)
	}

	if _, exempt := ev.GetEvt().(*genpb.ServerEvent_MatchEnded); !exempt {
		for owner, word := range w.words {
			if owner == observer || word == ownWord {
				// Either the recipient's own secret, or a word they already
				// hold — two players sharing the common word are
				// indistinguishable and nothing has been disclosed.
				continue
			}
			if how := needles.find(word); how != "" {
				record(owner, "word", how)
				continue
			}
			if len(word) >= fullScanMinLen {
				if how := fullNeedles.find(word); how != "" {
					record(owner, "word", how+"/coords")
				}
			}
		}
	}

	// A seat token is a bearer credential for one seat. It is never legitimate
	// on anyone else's socket, in any frame, including the final reveal.
	for owner, token := range w.tokens {
		if owner == observer {
			continue
		}
		if how := fullNeedles.find(token); how != "" {
			record(owner, "seat_token", how)
		}
	}

	return found
}

// SweepAll re-runs the word scan over a recorded transcript with the complete
// set of secrets. The live Inspect can only test against what was registered by
// the time a frame arrived; this closes the early-match window.
func (w *Watchdog) SweepAll(observer, ownWord string, frames []*genpb.ServerEvent) []Leak {
	var out []Leak
	for _, ev := range frames {
		out = append(out, w.Inspect(observer, ownWord, ev)...)
	}
	return out
}

// scanner holds the three forms of one frame's bytes.
type scanner struct {
	raw    []byte
	folded []byte
	squash []byte
}

func scanTargets(raw []byte) scanner {
	return scanner{
		raw:    raw,
		folded: bytes.ToLower(raw),
		squash: squash(raw),
	}
}

// find reports which of the three searches located the needle, or "".
func (s scanner) find(needle string) string {
	if needle == "" {
		return ""
	}
	if bytes.Contains(s.raw, []byte(needle)) {
		return "exact"
	}
	if bytes.Contains(s.folded, []byte(strings.ToLower(needle))) {
		return "case-folded"
	}
	if n := squash([]byte(needle)); len(n) > 0 && bytes.Contains(s.squash, n) {
		return "squashed"
	}
	return ""
}

// squash drops every byte that is not printable ASCII. Protobuf field tags and
// length prefixes are almost always non-printable, so this rejoins a secret
// that was split across two adjacent fields.
func squash(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			out = append(out, c)
		}
	}
	return out
}

// stripCoordinates clones a frame with every stroke coordinate array emptied.
func stripCoordinates(ev *genpb.ServerEvent) *genpb.ServerEvent {
	c, ok := proto.Clone(ev).(*genpb.ServerEvent)
	if !ok {
		return ev
	}
	switch e := c.GetEvt().(type) {
	case *genpb.ServerEvent_StrokeBegan:
		e.StrokeBegan.Points = nil
	case *genpb.ServerEvent_StrokePoints:
		e.StrokePoints.Points = nil
	case *genpb.ServerEvent_StrokeEnded:
		e.StrokeEnded.Points = nil
	case *genpb.ServerEvent_Snapshot:
		for _, s := range e.Snapshot.GetStrokes() {
			s.Points = nil
		}
	}
	return c
}

// frameName is the ServerEvent variant's name, for messages and for the
// coverage guard that proves the harness actually exercised a frame type.
func frameName(ev *genpb.ServerEvent) string {
	switch ev.GetEvt().(type) {
	case *genpb.ServerEvent_LobbyState:
		return "LobbyState"
	case *genpb.ServerEvent_SettingsChanged:
		return "SettingsChanged"
	case *genpb.ServerEvent_RoundStarted:
		return "RoundStarted"
	case *genpb.ServerEvent_TurnStarted:
		return "TurnStarted"
	case *genpb.ServerEvent_StrokeBegan:
		return "StrokeBegan"
	case *genpb.ServerEvent_StrokePoints:
		return "StrokePoints"
	case *genpb.ServerEvent_StrokeEnded:
		return "StrokeEnded"
	case *genpb.ServerEvent_PhaseChanged:
		return "PhaseChanged"
	case *genpb.ServerEvent_VoteCastCount:
		return "VoteCastCount"
	case *genpb.ServerEvent_VoteTally:
		return "VoteTally"
	case *genpb.ServerEvent_PlayerEliminated:
		return "PlayerEliminated"
	case *genpb.ServerEvent_MatchEnded:
		return "MatchEnded"
	case *genpb.ServerEvent_PlayerPresence:
		return "PlayerPresence"
	case *genpb.ServerEvent_Error:
		return "Error"
	case *genpb.ServerEvent_Joined:
		return "Joined"
	case *genpb.ServerEvent_YourWord:
		return "YourWord"
	case *genpb.ServerEvent_Snapshot:
		return "Snapshot"
	case *genpb.ServerEvent_SpectatorInfo:
		return "SpectatorInfo"
	case *genpb.ServerEvent_VoteAccepted:
		return "VoteAccepted"
	case nil:
		return "unset"
	default:
		return "unknown"
	}
}
