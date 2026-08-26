# Imposter Drawing Game — Design Plan

## Goal

Build a standalone, real-time browser party game for 3–10 players.

At the start of a match, one player receives a private word that is related to, but different from, the word assigned to every other player. Nobody is told whether they are the imposter. Players add fast visual clues to a single shared canvas, discuss without revealing their words, and vote anonymously to eliminate the suspected imposter.

The group wins by eliminating the imposter. The imposter wins by surviving the final configured round or by remaining when only two active players are left.

## Match Setup

- A host creates a room and shares a link or short join code.
- The host also plays normally.
- Players join with a display name and signal readiness.
- A match requires 3–10 players.
- The host configures:
  - Word-pair difficulty: Easy, Medium, or Hard.
  - Maximum rounds: 1–4; recommended default is 2.
  - Drawing time per turn: 5–60 seconds; recommended default is 15 seconds.
  - Discussion-and-decision time: 30–180 seconds; recommended default is 120 seconds.
- The server randomly selects a word pair from the chosen difficulty deck.
- The server randomly chooses which side of that pair is the common word.
- Exactly one player receives the other word; all remaining players receive the common word.
- No player is told their role, the full pair, or anyone else's word.

## Game Flow

### 1. Private Word Reveal

Each player privately sees their word before the first round. The word remains available to that player during the match.

### 2. Drawing Phase

- Each active player draws once per round.
- Turn order is shuffled independently at the start of every round.
- A player receives the configured drawing time, 15 seconds by default.
- All players can watch the drawing live.
- The interface identifies the current artist while they draw.
- The completed canvas does not retain artist labels, colors, or other attribution.
- Every turn adds to the same persistent shared canvas.

### 3. Discussion and Decision Phase

After all active players have drawn:

- A combined discussion-and-decision timer begins.
- Players discuss the visible canvas externally; version one has no built-in voice or text chat.
- Every active player may cast exactly one anonymous, irreversible vote.
- Valid vote choices are any active player, the voter themself, or `Skip`.
- The match ends the phase early if every active player has submitted a vote.
- A player who does not vote before time expires **abstains**. An abstention is not a `Skip`: it is counted in no bucket and adds weight to no option. `Skip` is a deliberate answer, and only a player who chooses it casts it.

### 4. Vote Result

- Reveal only aggregate vote totals per candidate and `Skip`.
- Never reveal who voted for whom, or who submitted early.
- Elimination is a **plurality with `Skip` on the ballot**. A candidate is eliminated only when their total is strictly greater than every other candidate's total **and** strictly greater than the `Skip` total.
- Any tie for first place eliminates nobody, including a tie with `Skip`. Three votes for Bob against three `Skip`s is a tie; four against three eliminates Bob.
- When nobody wins outright, nobody is eliminated and the next round begins.
- Because abstentions are counted nowhere, the totals need not sum to the number of active players.

### 5. End Conditions

- If the eliminated player is the imposter, the group wins immediately.
- If the eliminated player is not the imposter:
  - Active players are told only that a non-imposter was eliminated.
  - The eliminated player becomes a silent spectator.
  - The eliminated player privately learns the real imposter's identity.
  - The next round begins unless another end condition applies.
- The imposter wins if they remain active after the configured final round.
- The imposter also wins if only two active players remain.

### 6. Final Reveal

The result screen shows:

- The winning side.
- The common word and the imposter word.
- Each player's assigned word.
- The imposter's identity.
- The final shared canvas.
- A replay option that returns players to the lobby.

## Canvas Rules

The canvas is append-only evidence, not a sabotage tool.

- No erasing, undoing, or intentionally covering existing marks.
- No words, letters, numbers, arrows, emojis, or conventional symbols.
- Drawings must be freehand visual clues.
- Players may draw an ambiguous clue that fits more than one possible word.
- The game should prevent erase tools and text tools; intentional overpainting remains a player-conduct rule.

## Discussion Rules

Players may defend their drawing choices, question suspicious marks, and accuse or support other players. They may not reveal private-word information directly or indirectly.

Forbidden examples include:

- Saying, spelling, translating, or rhyming with the secret word.
- Naming the secret word's category or giving an obvious verbal definition.
- Naming the object they drew if that directly explains their word.
- Mimicking or using other indirect verbal clues for the word.

Allowed discussion includes statements such as:

- “That shape is being interpreted too specifically.”
- “I was building on the previous clue.”
- “Why are we ignoring the drawing added before mine?”
- “Two of these clues do not fit together.”

Because version one has no built-in voice or text chat, these rules are enforced socially by the players.

## Disconnect Rules

### Active players

A player is **active** only while they are connected, present in the room, and not eliminated. A disconnected player is **not** active: they are excluded from the strict-majority denominator, from the turn order, and from the "only two active players remain" count, for as long as they stay disconnected. Their seat, word, and match state are still retained so they can rejoin.

This is deliberate. A disconnected player cannot vote, so leaving them in the denominator would hold the voting window open on somebody who has left.

- The server retains a disconnected player's assigned word, match state, and room seat so they can reconnect.
- A disconnected non-imposter misses any drawing turn that occurs while absent.
- A disconnected player's missing vote is an abstention, like any other.
- On reconnect, a non-imposter returns as an active player if they have not been eliminated.
- If the imposter disconnects, retain their seat during the reconnect grace
  window. If they do not return before it expires, end the match and award a
  group win.
- The game can show a generic disconnect message, but the outcome intentionally gives players indirect information about the disconnected player's role.

## Word-Pair Difficulty

Difficulty is based on visual overlap, not word obscurity. Each pair should be easy to draw literally in a short turn and should not depend on text or symbols.

### Easy

Pairs have a clear visual difference. Players can still make generic drawings to avoid giving away their word immediately.

| Common/imposter pair examples |
| --- |
| Cat / Dog |
| Pizza / Burger |
| Train / Bus |
| Bicycle / Motorcycle |
| Shark / Dolphin |
| Apple / Orange |
| Doctor / Firefighter |
| Guitar / Piano |

### Medium

Pairs share several visual clues. Players need multiple drawings and discussion to distinguish the two concepts.

| Common/imposter pair examples |
| --- |
| Beach / Desert |
| Forest / Jungle |
| Mountain / Volcano |
| Hospital / School |
| Castle / Palace |
| Swimming / Surfing |
| Camera / Phone |
| Coffee / Tea |

### Hard

Pairs have substantial overlap and reward deliberately ambiguous drawings. These should be play-tested carefully so they remain solvable.

| Common/imposter pair examples |
| --- |
| Library / Bookstore |
| Restaurant / Café |
| Camping / Hiking |
| Airport / Train Station |
| River / Lake |
| Rain / Snow |
| Circus / Carnival |
| Museum / Art Gallery |

### Word-Deck Curation Rules

- Include each pair in both directions through random role assignment; players must not know which word is common.
- Avoid words that require text, flags, logos, or specialist knowledge to communicate.
- Avoid pairs that are either nearly identical or unrelated.
- Test every pair with short drawing turns before adding it to the production deck.
- Remove pairs that consistently reveal the imposter after one drawing or remain unsolvable after the maximum round count.

## Browser Experience

### Screens

1. **Home** — Create room or join with code/link.
2. **Lobby** — Player list, readiness, host settings, share controls, and start button.
3. **Private word reveal** — Show only the player's assigned word and a reminder not to reveal it.
4. **Drawing round** — Shared canvas, active artist, turn queue, and timer.
5. **Discussion and decision** — Shared canvas, countdown, and private vote picker.
6. **Vote result** — Anonymous aggregate tally and elimination/no-elimination outcome.
7. **Final reveal** — Winner, assignments, words, canvas, and replay.

### Usability Requirements

- Support mouse, trackpad, and touch drawing.
- Make the active timer and current artist obvious.
- Keep the shared canvas readable at the 10-player maximum.
- Avoid preserving permanent visual author attribution after a drawing turn.
- Preserve reconnectable room and match state on the server.

## Technical Architecture

- Use an authoritative game server for private word assignment, timers, room state, voting, elimination, and win conditions.
- Send each client only the data it is permitted to see.
- Synchronize canvas strokes in real time.
- Persist enough room state for reconnecting players to resume a match safely.
- Treat all final vote resolution and role checks as server-side operations.

## Initial Balance Defaults

| Setting | Default | Allowed range |
| --- | ---: | ---: |
| Players | 3–10 | 3–10 |
| Difficulty | Medium | Easy, Medium, Hard |
| Maximum rounds | 2 | 1–4 |
| Drawing turn | 15 seconds | 5–60 seconds |
| Discussion and decision | 120 seconds | 30–180 seconds |

With 10 players at the default configuration, two rounds take approximately nine minutes before result screens:

`(10 players × 15 seconds drawing + 120 seconds discussion/decision) × 2 rounds`

## Validation Plan

1. Play-test with 3, 6, and 10 players.
2. Test all difficulty decks using the default two-round configuration.
3. Measure how often the imposter is correctly identified after each round.
4. Watch for unreadable canvases, persistent vote stalemates, and word pairs that are trivial or impossible.
5. Adjust curated pairs and preset recommendations before expanding scope.

## Out of Scope for Version One

- Persistent accounts, scoreboards, ranks, achievements, or matchmaking.
- Built-in voice chat or text chat.
- Automated detection of spoken hints, written symbols, or intentional overpainting.
- Spectator interaction after elimination.
- Multiple imposters or additional roles.
