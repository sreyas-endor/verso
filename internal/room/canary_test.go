package room

// canary_test.go — the empirical half of the secret-leak defense
// (IMPLEMENTATION_PLAN.md §1 defense 3, milestone 10) and the type-level half's
// non-compilation proof (defense 1, milestone 3).
//
// The type system already makes Broadcast(EvYourWord{...}) fail to compile and
// viewFor is the only function that reads Player.word. This file assumes both
// could be wrong tomorrow. It seeds two unmistakable sentinels, plays a complete
// match — lobby, assignment, every drawing turn, discussion, votes, an
// elimination, the spectator's private reveal, the final reveal — captures the
// RAW MARSHALLED BYTES of every frame that reached every socket, and searches
// those bytes.
//
// Byte-level and not field-level, on purpose. A field-level check only inspects
// the fields somebody remembered to list, so it cannot see a word smuggled
// through a display name, an error message, a log-shaped string, or a field
// added to game.proto next month. The search is also case-insensitive, and runs
// a second time over the frame with its non-printable bytes removed, so a word
// split across two adjacent fields by a tag and a length prefix cannot hide in
// the gap.

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	mrand "math/rand/v2"

	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// The sentinels. Long, uppercase, and structurally unlike anything else on the
// wire, so a single byte of either one in a frame is unambiguous evidence.
const (
	canaryAlpha = "SECRET_CANARY_ALPHA"
	canaryBeta  = "SECRET_CANARY_BETA"
)

// canaryDeck deals only the sentinels. assignWords still flips its own coin for
// which side is common, so the test must ask the room which is which rather
// than assume.
type canaryDeck struct{}

func (canaryDeck) Pair(genpb.Difficulty, *mrand.Rand, []string) (string, string) {
	return canaryAlpha, canaryBeta
}

// ---------------------------------------------------------------------------
// capture plumbing — deliberately independent of every other test file, so the
// most important test in the repo cannot be broken from outside itself
// ---------------------------------------------------------------------------

// cnryFrame is one delivered frame plus the exact bytes writePump would have
// put on the socket. The bytes are taken at harvest time, not at assert time:
// a ServerEvent can share submessages with live room state, so marshalling late
// would be marshalling something the client never saw.
type cnryFrame struct {
	ev  *genpb.ServerEvent
	raw []byte
}

type cnrySock struct {
	ch     chan *genpb.ServerEvent
	frames []cnryFrame
}

func newCnrySock() *cnrySock {
	return &cnrySock{ch: make(chan *genpb.ServerEvent, 16384)}
}

// Send and Close are the room's Session contract. Close is a no-op recorded
// nowhere: this test cares about what reached the wire, not about lifecycle.
func (s *cnrySock) Send(ev *genpb.ServerEvent) {
	select {
	case s.ch <- ev:
	default:
	}
}

func (s *cnrySock) Close() {}

// harvest drains the queue and freezes each frame's wire form. It must be
// called often: the queue is bounded, and a dropped frame is a frame this test
// never gets to examine.
func (s *cnrySock) harvest(t *testing.T) {
	t.Helper()
	for {
		select {
		case ev := <-s.ch:
			raw, err := proto.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal %T: %v", ev.GetEvt(), err)
			}
			s.frames = append(s.frames, cnryFrame{ev: ev, raw: raw})
		default:
			return
		}
	}
}

// cnryGet hops onto the room goroutine, which owns every field on Room.
func cnryGet[T any](r *Room, fn func(*Room) T) T {
	var v T
	if err := r.do(func() { v = fn(r) }); err != nil {
		panic(err)
	}
	return v
}

func cnryPhase(r *Room) genpb.Phase {
	return cnryGet(r, func(r *Room) genpb.Phase { return r.phase })
}

func cnryRound(r *Room) int32 {
	return cnryGet(r, func(r *Room) int32 { return r.round })
}

func cnryArtist(r *Room) string {
	return cnryGet(r, func(r *Room) string { return r.artistID })
}

func cnryImposter(r *Room) string {
	return cnryGet(r, func(r *Room) string {
		if len(r.imposterIDs) == 0 {
			return ""
		}
		return r.imposterIDs[0]
	})
}

func cnryWordOf(r *Room, id string) string {
	return cnryGet(r, func(r *Room) string {
		if p := r.byID[id]; p != nil {
			return p.word
		}
		return ""
	})
}

// cnrySquash removes every byte that is not printable ASCII.
//
// Protobuf writes a field as tag, length, payload. A word split across two
// adjacent string fields — "SECRET_CANARY_" in one and "ALPHA" in the next —
// is invisible to a plain substring search because the tag and length bytes sit
// between the halves. Those separators are not printable ASCII (a 19-byte
// payload has length 0x13), so dropping them puts the halves back together and
// the search sees the whole word.
func cnrySquash(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			out = append(out, c)
		}
	}
	return out
}

// cnryCarries reports whether raw contains word, case-insensitively, either
// literally or once the frame's non-printable separators are removed.
func cnryCarries(raw []byte, word string) bool {
	needle := bytes.ToLower([]byte(word))
	hay := bytes.ToLower(raw)
	if bytes.Contains(hay, needle) {
		return true
	}
	return bytes.Contains(cnrySquash(hay), needle)
}

// cnryDraw puts one real stroke on the canvas as the current artist, so
// StrokeBegan, StrokePoints and StrokeEnded are genuinely on the wire.
func cnryDraw(r *Room, id string, out Session) {
	r.Submit(Command{PlayerID: id, Out: out, Cmd: &genpb.ClientCommand{
		Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
			ColorIndex: 3, Width: 6, Points: []int32{10, 20, 30, 40},
		}}}})
	r.Submit(Command{PlayerID: id, Out: out, Cmd: &genpb.ClientCommand{
		Cmd: &genpb.ClientCommand_StrokePoints{StrokePoints: &genpb.StrokePoints{
			Points: []int32{50, 60, 70, 80},
		}}}})
	r.Submit(Command{PlayerID: id, Out: out, Cmd: &genpb.ClientCommand{
		Cmd: &genpb.ClientCommand_StrokeEnd{StrokeEnd: &genpb.StrokeEnd{}}}})
}

// cnryKind names a frame for the coverage guard at the end of the test.
func cnryKind(ev *genpb.ServerEvent) string {
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
	default:
		return "Unknown"
	}
}

// ---------------------------------------------------------------------------
// The canary
// ---------------------------------------------------------------------------

// TestCanaryCompleteMatch is milestone 10.
//
// It plays a full six-player, two-round match to the final reveal and asserts,
// over the raw bytes of every frame that reached every socket:
//
//  1. A sentinel may appear only in a frame addressed to its owner — that
//     player's own YourWord or Snapshot — or in MatchEnded, the one sanctioned
//     broadcast of every word (DESIGN.md:75).
//  2. No socket ever receives the other sentinel before MatchEnded.
//  3. No broadcast-typed frame carries either sentinel before MatchEnded.
//  4. The imposter's identity is marked only where it is legitimate: the
//     spectator's private unicast, and the final reveal.
//  5. A seat token issued to one player appears in no other player's frames.
func TestCanaryCompleteMatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const players = 6

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r := New("CNRY", "host", Options{
			Deck: canaryDeck{},
			Rand: mrand.New(mrand.NewPCG(21, 22)),
			Settings: &genpb.MatchSettings{
				MaxRounds: 2, DrawSeconds: 5, DiscussSeconds: 30,
			},
		})
		hostID, hostTok := r.HostSeat()
		go r.run(ctx)

		s0 := newCnrySock()
		if _, err := r.attach(hostTok, s0); err != nil {
			t.Fatal(err)
		}
		ids := []string{hostID}
		socks := []*cnrySock{s0}
		tokens := []string{hostTok}
		for i := 1; i < players; i++ {
			sk := newCnrySock()
			id, tok, err := r.seat("player", sk)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
			socks = append(socks, sk)
			tokens = append(tokens, tok)
		}

		harvest := func() {
			for _, s := range socks {
				s.harvest(t)
			}
		}
		idx := func(id string) int {
			for i, v := range ids {
				if v == id {
					return i
				}
			}
			t.Fatalf("unknown player %q", id)
			return -1
		}
		submit := func(i int, c *genpb.ClientCommand) {
			r.Submit(Command{PlayerID: ids[i], Out: socks[i], Cmd: c})
		}

		// A host settings change, so SettingsChanged is exercised too.
		submit(0, &genpb.ClientCommand{Cid: "cfg",
			Cmd: &genpb.ClientCommand_UpdateSettings{UpdateSettings: &genpb.UpdateSettings{
				Settings: &genpb.MatchSettings{MaxRounds: 2, DrawSeconds: 5, DiscussSeconds: 30},
			}}})
		// A rejected command, so an Error frame is exercised too. Its message is
		// a place a careless implementation could echo state into.
		submit(1, &genpb.ClientCommand{Cid: "nope",
			Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}}})
		synctest.Wait()
		harvest()

		for i := range ids {
			submit(i, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}})
		}
		synctest.Wait()
		harvest()

		submit(0, &genpb.ClientCommand{Cid: "go",
			Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}}})
		synctest.Wait()
		harvest()

		if ph := cnryPhase(r); ph != genpb.Phase_PHASE_ASSIGNING {
			t.Fatalf("phase after StartMatch = %v, want ASSIGNING", ph)
		}
		imposterID := cnryImposter(r)
		if imposterID == "" {
			t.Fatal("no imposter was dealt")
		}

		// The elimination target is a non-imposter, chosen so the match survives
		// round one and runs to the final round. Eliminating them produces the
		// PlayerEliminated broadcast and the SpectatorInfo unicast that tells
		// exactly one player who the imposter is (DESIGN.md:67).
		target := ""
		for _, id := range ids {
			if id != imposterID && id != hostID {
				target = id
				break
			}
		}
		// The seat that drops and returns mid-match, so PlayerPresence and a
		// reconnect Snapshot are genuinely on the wire. Neither the imposter nor
		// the elimination target, so neither drop changes the outcome.
		dropper := ""
		for _, id := range ids {
			if id != imposterID && id != target && id != hostID {
				dropper = id
				break
			}
		}
		if target == "" || dropper == "" {
			t.Fatal("could not pick a target and a dropper")
		}

		eliminated := false
		droppedOnce := false

		// Drive the match. Every iteration harvests first, so nothing is lost to
		// a bounded queue.
		for step := 0; step < 400; step++ {
			harvest()
			ph := cnryPhase(r)
			if ph == genpb.Phase_PHASE_ENDED {
				break
			}

			switch ph {
			case genpb.Phase_PHASE_DRAWING:
				artist := cnryArtist(r)
				if artist != "" {
					cnryDraw(r, artist, socks[idx(artist)])
					synctest.Wait()
					harvest()

					// Provoke rejections from everyone else, mid-match, while a
					// word is in scope in the handler that builds the message.
					// An Error string is a plausible place for a word to end up
					// by accident, and a canary that only ever sees the lobby's
					// errors would never look at one.
					for i, id := range ids {
						if id == artist {
							continue
						}
						submit(i, &genpb.ClientCommand{Cid: "wrongphase",
							Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{
								Choice: &genpb.CastVote_Skip{Skip: true}}}})
						submit(i, &genpb.ClientCommand{Cid: "notartist",
							Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
								ColorIndex: 1, Width: 4, Points: []int32{1, 2}}}})
						submit(i, &genpb.ClientCommand{Cid: "notyours",
							Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}}})
					}
					synctest.Wait()
					harvest()
				}
				// Drop and reclaim a seat in the middle of a live turn.
				if !droppedOnce && artist != dropper && cnryRound(r) == 1 {
					droppedOnce = true
					di := idx(dropper)
					r.detach(dropper, socks[di])
					synctest.Wait()
					harvest()
					if _, err := r.attach(tokens[di], socks[di]); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					harvest()
				}
				// Read through the actor: Room.Settings() returns a field the
				// room goroutine owns, with no synchronisation of its own.
				draw := cnryGet(r, func(r *Room) int32 { return r.settings.GetDrawSeconds() })
				time.Sleep(time.Duration(draw)*time.Second + time.Millisecond)
				synctest.Wait()

			case genpb.Phase_PHASE_DISCUSSION:
				if !eliminated {
					// Round one: a strict majority of six is four; all six vote
					// the same way, so the elimination is unambiguous.
					eliminated = true
					for i := range ids {
						submit(i, &genpb.ClientCommand{
							Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{
								Choice: &genpb.CastVote_CandidateId{CandidateId: target},
							}}})
					}
				} else {
					// Round two: everyone skips, so nobody goes and the imposter
					// survives the final round. The eliminated player votes too
					// and is refused — another error message built with a word
					// in scope, this time a spectator's own.
					for i := range ids {
						submit(i, &genpb.ClientCommand{Cid: "vote",
							Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{
								Choice: &genpb.CastVote_Skip{Skip: true},
							}}})
					}
					// A stroke in the wrong phase, rejected by the artist gate.
					submit(0, &genpb.ClientCommand{Cid: "nodraw",
						Cmd: &genpb.ClientCommand_StrokeBegin{StrokeBegin: &genpb.StrokeBegin{
							ColorIndex: 1, Width: 4, Points: []int32{1, 2}}}})
				}
				synctest.Wait()

			default:
				time.Sleep(time.Second)
				synctest.Wait()
			}
		}
		harvest()

		if ph := cnryPhase(r); ph != genpb.Phase_PHASE_ENDED {
			t.Fatalf("match never reached the final reveal, phase = %v", ph)
		}
		if !eliminated {
			t.Fatal("no elimination happened; the spectator path was never exercised")
		}

		// -------------------------------------------------------------------
		// Which sentinel belongs to whom.
		// -------------------------------------------------------------------
		own := make([]string, len(ids))
		for i, id := range ids {
			own[i] = cnryWordOf(r, id)
			if own[i] != canaryAlpha && own[i] != canaryBeta {
				t.Fatalf("player %d holds %q, which is neither sentinel", i, own[i])
			}
		}
		other := func(w string) string {
			if w == canaryAlpha {
				return canaryBeta
			}
			return canaryAlpha
		}
		if got := cnryWordOf(r, imposterID); got == cnryWordOf(r, ids[0]) && imposterID != ids[0] {
			t.Fatalf("the imposter holds the same word as a non-imposter (%q)", got)
		}

		// -------------------------------------------------------------------
		// Assertion 1 and 2 — per socket, over raw bytes.
		// -------------------------------------------------------------------
		total, tainted := 0, 0
		reveals := make([]int, len(ids)) // index of each socket's MatchEnded
		for i := range socks {
			reveals[i] = -1
		}

		for i, s := range socks {
			mine, theirs := own[i], other(own[i])
			for n, f := range s.frames {
				total++
				kind := cnryKind(f.ev)
				if kind == "MatchEnded" && reveals[i] < 0 {
					reveals[i] = n
				}

				hasMine := cnryCarries(f.raw, mine)
				hasTheirs := cnryCarries(f.raw, theirs)
				if !hasMine && !hasTheirs {
					continue
				}
				tainted++

				switch kind {
				case "MatchEnded":
					// The one sanctioned broadcast of every word, valid only in
					// PHASE_ENDED (DESIGN.md:75).
				case "YourWord":
					if w := f.ev.GetYourWord().GetWord(); w != mine {
						t.Errorf("player %d frame %d: YourWord carried %q, owns %q", i, n, w, mine)
					}
					if hasTheirs {
						t.Errorf("player %d frame %d: YourWord bytes contain the other sentinel", i, n)
					}
				case "Snapshot":
					if w := f.ev.GetSnapshot().GetYourWord(); w != mine {
						t.Errorf("player %d frame %d: Snapshot carried %q, owns %q", i, n, w, mine)
					}
					if hasTheirs {
						t.Errorf("player %d frame %d: Snapshot bytes contain the other sentinel", i, n)
					}
				case "SpectatorInfo":
					// The dossier of an eliminated player legitimately carries
					// BOTH sentinels: it publishes every seat's word for every
					// round dealt (MULTIPLE_IMPOSTERS.md, "Eliminated-player
					// Spectator View"). The thing that makes it safe is who
					// receives it, so that is what is asserted here.
					if ids[i] != target {
						t.Errorf("player %d frame %d: SpectatorInfo carrying words reached a "+
							"player who is not the eliminated spectator", i, n)
					}
					si := f.ev.GetSpectatorInfo()
					for _, rd := range si.GetRounds() {
						for _, a := range rd.GetAssignments() {
							if w := a.GetWord(); w != canaryAlpha && w != canaryBeta {
								t.Errorf("player %d frame %d: dossier assigns %q, neither sentinel",
									i, n, w)
							}
						}
					}
				default:
					t.Errorf("player %d frame %d: sentinel bytes in a %s frame — "+
						"this frame type must never carry a word (mine=%v theirs=%v)",
						i, n, kind, hasMine, hasTheirs)
				}
			}
			if reveals[i] < 0 {
				t.Errorf("player %d never received MatchEnded", i)
			}
		}

		// -------------------------------------------------------------------
		// Assertion 3 — nothing broadcast-typed carries a sentinel before the
		// reveal. Stated by frame TYPE rather than by socket, so it holds even
		// if a future change starts unicasting a broadcast-shaped payload.
		// -------------------------------------------------------------------
		unicastOnly := map[string]bool{
			"Joined": true, "YourWord": true, "Snapshot": true,
			"SpectatorInfo": true, "VoteAccepted": true,
		}
		for i, s := range socks {
			for n, f := range s.frames {
				kind := cnryKind(f.ev)
				if kind == "MatchEnded" || unicastOnly[kind] {
					continue
				}
				if cnryCarries(f.raw, canaryAlpha) || cnryCarries(f.raw, canaryBeta) {
					t.Errorf("player %d frame %d: broadcast-typed %s carries a sentinel", i, n, kind)
				}
			}
		}

		// -------------------------------------------------------------------
		// Assertion 4 — the imposter's identity is marked only where it is
		// legitimate. The imposter's player id is a public roster entry and
		// appears everywhere a non-imposter's does; what must not appear is any
		// field that says WHICH player is the imposter.
		// -------------------------------------------------------------------
		spectatorSockets := 0
		for i, s := range socks {
			for n, f := range s.frames {
				switch kind := cnryKind(f.ev); kind {
				case "SpectatorInfo":
					si := f.ev.GetSpectatorInfo()
					if ids[i] != target {
						t.Errorf("player %d frame %d: SpectatorInfo reached a player who is "+
							"not the eliminated spectator", i, n)
					}
					named := si.GetImposters()
					if len(named) != 1 || named[0].GetPlayerId() != imposterID {
						t.Errorf("player %d frame %d: dossier names %d imposters (%v), want just %q",
							i, n, len(named), cnryImposterIDs(si), imposterID)
					}
					if len(named) == 1 && named[0].GetName() == "" {
						t.Errorf("player %d frame %d: dossier carries no imposter name", i, n)
					}
					if len(si.GetRounds()) == 0 {
						t.Errorf("player %d frame %d: dossier carries no rounds", i, n)
					}
					if reveals[i] >= 0 && n > reveals[i] {
						t.Errorf("player %d frame %d: SpectatorInfo arrived after MatchEnded", i, n)
					}
					spectatorSockets++
				case "PlayerEliminated":
					pe := f.ev.GetPlayerEliminated()
					// Nobody eliminated in this match was the imposter, so
					// was_imposter must be false in every one of these frames.
					// The default settings are Reveal, so alignment_revealed is
					// set and the flag is a real "no", not a withheld one.
					if pe.GetEliminated() && !pe.GetAlignmentRevealed() {
						t.Errorf("player %d frame %d: alignment_revealed is clear under the "+
							"default Reveal setting", i, n)
					}
					if pe.GetWasImposter() {
						t.Errorf("player %d frame %d: PlayerEliminated.was_imposter is set "+
							"for a non-imposter elimination", i, n)
					}
					if pe.GetEliminated() && pe.GetPlayerId() == imposterID {
						t.Errorf("player %d frame %d: the imposter was eliminated, "+
							"which this match should not do", i, n)
					}
				case "MatchEnded":
					me := f.ev.GetMatchEnded()
					if got := me.GetImposterPlayerIds(); len(got) != 1 || got[0] != imposterID {
						t.Errorf("player %d frame %d: MatchEnded names imposters %v, want [%q]",
							i, n, got, imposterID)
					}
					if len(me.GetReveals()) != players {
						t.Errorf("player %d frame %d: %d reveals, want %d",
							i, n, len(me.GetReveals()), players)
					}
					marked := 0
					for _, rv := range me.GetReveals() {
						if rv.GetWasImposter() {
							marked++
							if rv.GetPlayerId() != imposterID {
								t.Errorf("player %d frame %d: reveal marks %q as the imposter",
									i, n, rv.GetPlayerId())
							}
						}
					}
					if marked != 1 {
						t.Errorf("player %d frame %d: %d reveals marked as the imposter, want 1",
							i, n, marked)
					}
				}
			}
		}
		if spectatorSockets == 0 {
			t.Error("no SpectatorInfo was ever delivered; the private imposter reveal " +
				"path was not exercised")
		}

		// -------------------------------------------------------------------
		// Assertion 5 — seat tokens. A token is a bearer credential for one
		// seat, and with it goes that seat's word.
		// -------------------------------------------------------------------
		for i, s := range socks {
			sawOwn := false
			for n, f := range s.frames {
				for j, tok := range tokens {
					if !cnryCarries(f.raw, tok) {
						continue
					}
					if i == j {
						sawOwn = true
						continue
					}
					t.Errorf("player %d frame %d (%s): carries player %d's seat token",
						i, n, cnryKind(f.ev), j)
				}
			}
			// Guard the guard: if no frame carried the recipient's own token
			// either, the loop above proved nothing about token handling.
			if !sawOwn {
				t.Errorf("player %d never received its own seat token; "+
					"the token assertion is vacuous", i)
			}
		}

		// -------------------------------------------------------------------
		// Guard the whole guard. A canary that never saw a frame type asserts
		// nothing about that frame type, and would go quietly on doing so.
		// -------------------------------------------------------------------
		seen := map[string]int{}
		for _, s := range socks {
			for _, f := range s.frames {
				seen[cnryKind(f.ev)]++
			}
		}
		for _, kind := range []string{
			"LobbyState", "SettingsChanged", "RoundStarted", "TurnStarted",
			"StrokeBegan", "StrokePoints", "StrokeEnded", "PhaseChanged",
			"VoteCastCount", "VoteTally", "PlayerEliminated", "MatchEnded",
			"PlayerPresence", "Error", "Joined", "YourWord", "Snapshot",
			"SpectatorInfo", "VoteAccepted",
		} {
			if seen[kind] == 0 {
				t.Errorf("no %s frame was captured; the canary did not exercise it", kind)
			}
		}
		if total == 0 {
			t.Fatal("no frames captured; the canary asserted nothing")
		}

		t.Logf("%d frames across %d sockets, %d carried sentinel bytes, "+
			"%d distinct frame types", total, len(socks), tainted, len(seen))

		cancel()
		synctest.Wait()
	})
}

// TestCanaryImposterRevealOnlyOnAGroupWin covers the one path the full-match
// canary deliberately does not take: the imposter IS voted out, so
// PlayerEliminated.was_imposter goes true.
//
// DESIGN.md:65 allows that bit to be set only in the same resolution that ends
// the match as a group win. Set it any earlier and the broadcast hands every
// remaining player the answer.
func TestCanaryImposterRevealOnlyOnAGroupWin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r := New("CNR2", "host", Options{
			Deck: canaryDeck{},
			Rand: mrand.New(mrand.NewPCG(7, 8)),
			Settings: &genpb.MatchSettings{
				MaxRounds: 2, DrawSeconds: 5, DiscussSeconds: 30,
			},
		})
		hostID, hostTok := r.HostSeat()
		go r.run(ctx)

		s0 := newCnrySock()
		if _, err := r.attach(hostTok, s0); err != nil {
			t.Fatal(err)
		}
		ids := []string{hostID}
		socks := []*cnrySock{s0}
		for i := 1; i < 5; i++ {
			sk := newCnrySock()
			id, _, err := r.seat("player", sk)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
			socks = append(socks, sk)
		}
		submit := func(i int, c *genpb.ClientCommand) {
			r.Submit(Command{PlayerID: ids[i], Out: socks[i], Cmd: c})
		}
		harvest := func() {
			for _, s := range socks {
				s.harvest(t)
			}
		}

		for i := range ids {
			submit(i, &genpb.ClientCommand{
				Cmd: &genpb.ClientCommand_SetReady{SetReady: &genpb.SetReady{Ready: true}}})
		}
		synctest.Wait()
		submit(0, &genpb.ClientCommand{
			Cmd: &genpb.ClientCommand_StartMatch{StartMatch: &genpb.StartMatch{}}})
		synctest.Wait()

		imposterID := cnryImposter(r)
		voted := false
		for step := 0; step < 400; step++ {
			harvest()
			ph := cnryPhase(r)
			if ph == genpb.Phase_PHASE_ENDED {
				break
			}
			switch ph {
			case genpb.Phase_PHASE_DISCUSSION:
				voted = true
				for i := range ids {
					submit(i, &genpb.ClientCommand{
						Cmd: &genpb.ClientCommand_CastVote{CastVote: &genpb.CastVote{
							Choice: &genpb.CastVote_CandidateId{CandidateId: imposterID},
						}}})
				}
				synctest.Wait()
			default:
				time.Sleep(time.Second)
				synctest.Wait()
			}
		}
		harvest()

		if !voted {
			t.Fatal("the voting window never opened")
		}
		if ph := cnryPhase(r); ph != genpb.Phase_PHASE_ENDED {
			t.Fatalf("phase = %v, want ENDED", ph)
		}

		for i, s := range socks {
			sawReveal, sawEnd := false, false
			for n, f := range s.frames {
				switch cnryKind(f.ev) {
				case "PlayerEliminated":
					pe := f.ev.GetPlayerEliminated()
					if !pe.GetWasImposter() {
						continue
					}
					sawReveal = true
					if pe.GetPlayerId() != imposterID {
						t.Errorf("player %d frame %d: was_imposter set on %q, imposter is %q",
							i, n, pe.GetPlayerId(), imposterID)
					}
				case "MatchEnded":
					sawEnd = true
					me := f.ev.GetMatchEnded()
					if me.GetWinner() != genpb.WinnerSide_WINNER_SIDE_GROUP ||
						me.GetReason() != genpb.MatchEndReason_MATCH_END_REASON_IMPOSTER_ELIMINATED {
						t.Errorf("player %d: eliminating the imposter gave %v/%v, want a group win",
							i, me.GetWinner(), me.GetReason())
					}
				case "SpectatorInfo":
					// The imposter went out, so nobody is a non-imposter
					// spectator and this unicast has no reason to exist.
					t.Errorf("player %d frame %d: SpectatorInfo sent although the "+
						"imposter was the one eliminated", i, n)
				}
			}
			if !sawReveal {
				t.Errorf("player %d: was_imposter never set although the imposter was voted out", i)
			}
			if !sawEnd {
				t.Errorf("player %d: no MatchEnded", i)
			}
		}

		cancel()
		synctest.Wait()
	})
}

// ---------------------------------------------------------------------------
// Defense 1 — the type-level half (IMPLEMENTATION_PLAN.md §6, milestone 3)
// ---------------------------------------------------------------------------

// TestBroadcastOfASecretDoesNotCompile asserts that handing Broadcast any of the
// five unicast-only wrappers is a BUILD error, and asserts on the compiler's own
// message.
//
// This is the cheapest of the three defenses and the only one that cannot be
// forgotten at runtime, so it is worth proving rather than assuming. The
// fixtures live under testdata/, which the go tool skips for wildcard patterns:
// `go build ./...` and `go vet ./...` never see the failing one.
func TestBroadcastOfASecretDoesNotCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles two packages")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("no go toolchain: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "out")

	// Positive control first. Without it, a renamed constructor or a broken
	// generated file would make the negative fixture "pass" for the wrong
	// reason, and this test would guard nothing forever after.
	ok, err := exec.Command("go", "build", "-o", bin, "./testdata/compileok").CombinedOutput()
	if err != nil {
		t.Fatalf("the positive control must build, but did not: %v\n%s", err, ok)
	}

	out, err := exec.Command("go", "build", "-o", bin, "./testdata/nocompile").CombinedOutput()
	if err == nil {
		t.Fatal("Broadcast accepted a unicast-only event: the compile-time half of " +
			"the secret-leak defense is GONE (IMPLEMENTATION_PLAN.md §4.2)")
	}
	got := string(out)

	// One error per wrapper, each naming the missing marker. Anything less means
	// a wrapper picked up a broadcastSafe method somewhere.
	for _, want := range []string{
		"room.EvYourWord does not implement room.Broadcastable (missing method broadcastSafe)",
		"room.EvSnapshot does not implement room.Broadcastable (missing method broadcastSafe)",
		"room.EvJoined does not implement room.Broadcastable (missing method broadcastSafe)",
		"room.EvSpectatorInfo does not implement room.Broadcastable (missing method broadcastSafe)",
		"room.EvVoteAccepted does not implement room.Broadcastable (missing method broadcastSafe)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("compiler did not say %q\nfull output:\n%s", want, got)
		}
	}
	t.Logf("go build rejected all five secret-bearing wrappers:\n%s", strings.TrimSpace(got))
}

// cnryImposterIDs flattens a dossier's imposter list for a failure message.
func cnryImposterIDs(si *genpb.SpectatorInfo) []string {
	out := make([]string, 0, len(si.GetImposters()))
	for _, im := range si.GetImposters() {
		out = append(out, im.GetPlayerId())
	}
	return out
}
