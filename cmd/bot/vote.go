package main

// vote.go — pluggable voting strategies.
//
// A strategy is asked once per discussion window and answers with a CastVote or
// nil. nil means "cast nothing at all", which is how the never-vote strategy
// forces the combined discussion-and-decision timer to run to expiry. Those
// silences are abstentions, not Skips: they appear in no bucket of the tally
// (DESIGN.md:52).
//
// Strategies see only what a real client sees: the roster, who is still active,
// and their own id. Nothing here knows who the imposter is. GangUp is handed a
// target by the table, which does know — that omniscience belongs to the test
// director, not to a player.

import (
	"fmt"
	"sort"

	mrand "math/rand/v2"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
)

// View is what a strategy is allowed to know.
type View struct {
	Me      string
	Round   int32
	Active  []string // active player ids, excluding nobody — Me is included
	Rand    *mrand.Rand
	Roster  map[string]*genpb.PlayerInfo
	Attempt int // 0 on the first try, 1+ after the server refused the last one
}

// Others is Active without Me, in a stable order.
func (v View) Others() []string {
	out := make([]string, 0, len(v.Active))
	for _, id := range v.Active {
		if id != v.Me {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// VoteStrategy decides one vote per discussion window.
type VoteStrategy interface {
	// Name identifies the strategy in reports.
	Name() string
	// Vote returns the command to send, or nil to cast nothing.
	Vote(View) *genpb.CastVote
}

func voteFor(id string) *genpb.CastVote {
	return &genpb.CastVote{Choice: &genpb.CastVote_CandidateId{CandidateId: id}}
}

func voteSkip() *genpb.CastVote {
	return &genpb.CastVote{Choice: &genpb.CastVote_Skip{Skip: true}}
}

// --- random ---------------------------------------------------------------

type randomVoter struct{}

// Random votes for an arbitrary other active player, or Skip when it is the
// last one standing with nobody to accuse.
func Random() VoteStrategy { return randomVoter{} }

func (randomVoter) Name() string { return "random" }

func (randomVoter) Vote(v View) *genpb.CastVote {
	others := v.Others()
	if len(others) == 0 || v.Rand == nil {
		return voteSkip()
	}
	return voteFor(others[v.Rand.IntN(len(others))])
}

// --- always skip ----------------------------------------------------------

type skipVoter struct{}

// AlwaysSkip never accuses anybody. A whole table of these reaches no strict
// majority, so nobody is eliminated and the imposter survives to the final round
// (DESIGN.md:59, DESIGN.md:71).
func AlwaysSkip() VoteStrategy { return skipVoter{} }

func (skipVoter) Name() string { return "skip" }

func (skipVoter) Vote(View) *genpb.CastVote { return voteSkip() }

// --- always self ----------------------------------------------------------

type selfVoter struct{}

// AlwaysSelf votes for itself, which DESIGN.md:50 explicitly permits. With
// three or more players no candidate can reach a strict majority this way, so
// it is the other route to a stalemate.
func AlwaysSelf() VoteStrategy { return selfVoter{} }

func (selfVoter) Name() string { return "self" }

func (selfVoter) Vote(v View) *genpb.CastVote { return voteFor(v.Me) }

// --- gang up --------------------------------------------------------------

type gangVoter struct {
	target func() string
	label  string
}

// GangUp votes for whoever target names. The function is resolved at vote time,
// not at construction, because the table only learns who the imposter is after
// the words have been dealt.
//
// A target that is empty, gone, or the voter themself degrades to Skip rather
// than to an invalid command.
func GangUp(label string, target func() string) VoteStrategy {
	return gangVoter{target: target, label: label}
}

func (g gangVoter) Name() string { return "gang-up:" + g.label }

func (g gangVoter) Vote(v View) *genpb.CastVote {
	if g.target == nil {
		return voteSkip()
	}
	id := g.target()
	if id == "" {
		return voteSkip()
	}
	for _, a := range v.Active {
		if a == id {
			return voteFor(id)
		}
	}
	// The target is no longer an active candidate — eliminated already, or
	// their socket dropped out of the denominator.
	return voteSkip()
}

// --- never vote -----------------------------------------------------------

type silentVoter struct{}

// NeverVote casts nothing. It exists to prove the discussion timer, not the
// early-close path: a table containing one of these cannot close the window
// early, so the combined timer must expire on its own — and the absent vote
// must show up in neither skip_count nor any candidate's total.
func NeverVote() VoteStrategy { return silentVoter{} }

func (silentVoter) Name() string { return "never-vote" }

func (silentVoter) Vote(View) *genpb.CastVote { return nil }

// StrategyByName builds a strategy from a CLI flag value. gangTarget is only
// consulted by "gang".
func StrategyByName(name string, gangTarget func() string) (VoteStrategy, error) {
	switch name {
	case "random", "":
		return Random(), nil
	case "skip":
		return AlwaysSkip(), nil
	case "self":
		return AlwaysSelf(), nil
	case "gang":
		return GangUp("imposter", gangTarget), nil
	case "silent", "never":
		return NeverVote(), nil
	default:
		return nil, fmt.Errorf("unknown vote strategy %q (random, skip, self, gang, silent)", name)
	}
}
