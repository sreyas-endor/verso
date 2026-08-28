# Verso — Multiple Imposters Design

This document extends the base match rules in
[`DESIGN.md`](./DESIGN.md). It adds a host-configurable number of imposters
and an elimination-result visibility setting. Unless this document explicitly
says otherwise, the base design remains unchanged.

## Goal

Let a host make larger games more paranoid without turning the imposters into a
coordinating team. Two players can hold the odd word, but neither is told they
are an imposter or who the other imposter is.

## Match Settings

The lobby exposes two new settings:

| Setting | Default | Options |
| --- | --- | --- |
| Imposters | 1 | 1, 2 |
| Elimination results | Reveal | Reveal, Hidden |

Both values are visible in the lobby and lock when the host starts the match.
They apply to every round in that match.

`Imposters: 1` preserves the existing one-imposter experience. `Imposters: 2`
is permitted for every supported player count; the game does not disable the
setting at low counts.

### Three-player warning

With three players and two imposters, the group cannot win under the base
two-active-player end condition. Eliminating either imposter leaves two active
players, which immediately awards the match to the imposter side before the
second can be removed.

The host may still start this configuration. The lobby must show a clear
warning before the match starts:

> Two imposters with three players is an experimental, unwinnable group
> configuration. The imposter side wins when two players remain.

Four- and five-player two-imposter matches are valid, but deliberately much
less forgiving than larger games. They require the group to avoid incorrect
eliminations and to catch both imposters before the round limit.

## Role Assignment

At match start, the server selects the configured number of distinct imposter
seats uniformly at random. The selected seats remain imposters for the whole
match; only their words change between rounds.

For each round:

1. The server chooses a word pair exactly as in the base design.
2. It chooses one word as the common word.
3. Every imposter receives the other word.
4. Every non-imposter receives the common word.

No player's private reveal identifies their role, names the alternate word, or
identifies another player's assignment. In particular, two imposters do not
receive each other's identity and must not be given a private team channel.
They may independently infer that a player with compatible clues holds the
same word, just as any other player may infer from the shared canvas.

The imposter count must not be exposed in a player's private word reveal. It
is a public lobby setting, so players already know whether the match has one
or two imposters.

## Drawing, Discussion, and Voting

The number of imposters changes no turn mechanics:

- Every active player still draws once per round on the shared anonymous
  canvas.
- The selected pen rule still applies equally to every artist.
- Discussion remains external and must not disclose private-word information.
- Each active player still has one anonymous, irreversible vote for an active
  player, themself, or `Skip`.
- Plurality, abstention, and `Skip` resolution are unchanged.

The imposters share no special drawing tools, extra time, votes, or ability to
communicate. Their only common advantage is having the same alternate word.

## Elimination Results

`Elimination results` controls what active players learn after a valid
elimination. Vote totals are always aggregate-only; the toggle does not make
individual votes visible.

### Reveal

The public result identifies the eliminated player's alignment:

- If a non-imposter was eliminated, announce that a non-imposter was
  eliminated.
- If an imposter was eliminated and others remain, announce that an imposter
  was eliminated and that the match continues.
- If the last imposter was eliminated, end the match with a group win.

In a two-imposter match, catching the first imposter therefore confirms that
the second remains. The UI need not repeat an exact remaining count because it
is implied by the public `Imposters` setting.

### Hidden

The public result announces only that the named player was eliminated. It does
not say whether they were an imposter or a non-imposter. If at least one
imposter remains, the next round begins normally.

When the last imposter is eliminated, the match ends with the normal group-win
result. The winner necessarily learns the group succeeded, but no intermediate
alignment result is revealed.

## Win Conditions

With one imposter, win conditions retain their base meaning. With two:

- The group wins immediately only when **both** imposters have been
  eliminated.
- The imposter side wins if at least one imposter remains active after the
  configured final round.
- The imposter side wins if only two active players remain.

An imposter who disconnects and does not return before the existing reconnect
grace period retains the base-design outcome: the match ends and the group
wins. This prevents a hidden imposter from indefinitely stalling the match and
does not require the room to infer which disconnected player held which role.

## Eliminated-player Spectator View

Every eliminated player becomes a silent spectator, regardless of the
`Elimination results` setting. On elimination, the server privately grants
that spectator a complete behind-the-scenes view:

- identities of every imposter;
- the common and imposter word for every round already dealt;
- each player's word assignment for every round already dealt;
- each canvas already completed; and
- the same word and assignment information for every later round as soon as
  it is dealt.

This information is private to that spectator. It must never be included in
the active-player room state or in any broadcast frame.

Spectators cannot draw, vote, re-enter the active roster, speak in the
discussion, DM active players, or signal private information by any other
means. Since the game uses Discord or another external voice service, this is
a social rule rather than a technical guarantee.

## Final Reveal

The final reveal replaces the singular imposter identity with the full list of
imposters. It continues to show every round's pair, each player's word
assignment, and the round-canvas filmstrip.

## Server and Client Requirements

- Persist the selected imposter count and elimination-result setting in match
  state from lobby creation through final reveal.
- Select distinct imposter seats once at match start.
- Send each active player only their own word, never the full assignment
  table.
- Build a separate spectator view that contains the full role and word data
  only after that recipient has been eliminated.
- Resolve the group win only after all selected imposter seats have been
  eliminated.
- Apply the public-result visibility setting consistently to every
  non-terminal elimination.
- Prevent the host from changing either new setting after match start.

## Validation Plan

1. Play one- and two-imposter matches at every player count from 3 to 10.
2. Confirm the three-player/two-imposter warning appears, does not block match
   creation, and accurately describes the resulting automatic imposter win.
3. Confirm both imposters receive the same alternate word every round and
   neither receives the other imposter's identity.
4. Confirm catching one imposter in a two-imposter match continues the match;
   catching the second ends it with a group win.
5. Verify Reveal and Hidden results contain the intended public information
   and never expose individual ballots.
6. Verify an eliminated player sees all existing and future word assignments,
   while an active player's client never receives another player's word.
7. Test the spectator view across reconnects and across every round boundary.
8. Play-test four- and five-player two-imposter matches to measure whether the
   configuration remains enjoyably chaotic rather than arbitrary.
