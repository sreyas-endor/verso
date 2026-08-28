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
- **The host may remove another player from the lobby.** Only from the lobby, and never themself. Past the lobby a seat holds a word, a place in the turn order and a place in the vote denominator; removing one would have to decide what happens when the target is the imposter, and no answer to that exists which does not tell the room who they were. A host who wants somebody out of a running match ends it and removes them before the rematch.
- Removal is not a ban. The removed player's seat and reconnect token are destroyed, so they cannot reclaim that seat, but the room code still works and they may take a fresh one. There is no account to ban and no address worth banning on a shared network.
- The host configures:
  - Word-pair difficulty: Easy, Medium, or Hard.
  - Maximum rounds: 1–4; recommended default is 2.
  - Drawing time per turn: 5–60 seconds; recommended default is 15 seconds.
  - Discussion-and-decision time: 30–180 seconds; recommended default is 120 seconds.
- The server randomly selects a word pair from the chosen difficulty deck, **once per round**.
- The server randomly chooses which side of that pair is the common word.
- Exactly one player receives the other word; all remaining players receive the common word.
- **The imposter is chosen once, at the start of the match, and keeps that role for every round.** The words change; who holds the odd one does not.
- Each round's pair must come from a cluster no earlier round in the match used, so no word is ever dealt twice. A player whose word repeated while the pairing moved would have learned they hold the common one.
- No player is told their role, the full pair, or anyone else's word.

## Game Flow

### 1. Private Word Reveal

Each player privately sees their word before **every** round, not only the first, because every round deals a new pair. The word remains available to that player for the rest of that round.

Rounds after the first say so explicitly: the reveal names the round and states that the pair is new and the canvas is blank. A player who assumes their previous word carried over will draw the wrong thing.

### 2. Drawing Phase

- Each active player draws once per round.
- Turn order is shuffled independently at the start of every round.
- A player receives the configured drawing time, 15 seconds by default.
- All players can watch the drawing live.
- The interface identifies the current artist while they draw.
- The completed canvas does not retain artist labels, colors, or other attribution.
- Every turn of one round adds to the same shared canvas. **The canvas is wiped between rounds**: each round is a fresh pair on blank paper, and is argued on its own evidence.
- Each round's finished canvas is kept for the final reveal.

### 3. Discussion and Decision Phase

After all active players have drawn:

- A combined discussion-and-decision timer begins.
- Players discuss the visible canvas externally; version one has no built-in voice or text chat.
- Every active player may cast exactly one anonymous, irreversible vote.
- Valid vote choices are any active player, the voter themself, or `Skip`.
- The match ends the phase early if every active player has submitted a vote.
- A player who does not vote before time expires **abstains**. An abstention is not a `Skip`: it is counted in no bucket and adds weight to no option. `Skip` is a deliberate answer, and only a player who chooses it casts it.
- The roster and the ballot keep **this round's drawing order** through the vote and the result. Players argue about the drawings in the order they appeared — "the third one" is how a drawing gets referred to — so nothing reorders between the last turn and the elimination. The reshuffle belongs to the start of the next round, not the end of this one.

### 4. Vote Result

- Reveal only aggregate vote totals per candidate and `Skip`.
- Never reveal who voted for whom. The roster may show that a seat has locked in a vote this round, so the room can see voting is live, but never which candidate (or `Skip`) they chose.
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
- **Every round's pair** — the common word and the imposter word for each round played, in order.
- **Each player's word for each round.** A round a player had already been eliminated out of shows a blank, which is distinct from having held the common word.
- The imposter's identity, which is the same player for the whole match.
- **Every round's canvas**, as a filmstrip: the final round shown full size, the rest as thumbnails beneath it, any of which can be promoted. The PNG export saves the promoted round.
- A replay option that returns players to the lobby.

A player who was disconnected across a round boundary is missing that round's thumbnail. The archive is built from the frames that client saw, and a reconnect snapshot carries only the round in progress.

## Canvas Rules

The canvas is append-only evidence, not a sabotage tool.

- No erasing, undoing, or intentionally covering existing marks.
- No words, letters, numbers, arrows, emojis, or conventional symbols.
- Drawings must be freehand visual clues.
- Players may draw an ambiguous clue that fits more than one possible word.
- The game should prevent erase tools and text tools; intentional overpainting remains a player-conduct rule.

### Pen Rules

The host may hand every artist the same handicap on how the turn may be spent. It constrains the pen, never the word, so it is not a second difficulty knob — a harder pen makes every clue sparser, which is precisely what makes an imposter easier to spot and a nervous informed player easier to mistake for one.

| Pen rule | The artist gets |
| --- | --- |
| **Free** (default) | As much drawing as the clock allows. |
| **One line** | One unbroken stroke. Lifting the pen finishes their drawing. |
| **Max 5** | Five strokes for the whole turn. |

A spent budget locks the pen for the rest of the turn; it never ends the turn early. Pointer cancellation is routine on touch devices — an app switch, a rotation, or a palm resting on the screen all fire it — and none of those should cost a player their turn. The rule is enforced on the server as a per-turn stroke ceiling; the client's gauge is a courtesy so nobody draws ink that will be dropped.

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

Pairs are not authored one at a time. Each deck is a list of **clusters** of five mutually confusable words, and the server draws a pair by taking two members of one cluster. A cluster of five yields ten pairs, so the same authoring effort produces roughly ten times the deck.

A cluster belongs to exactly one difficulty, and that is what keeps the host's choice honest: all five words must be confusable *at that tier*, so no combination of them can come out easier or harder than the tier selected. A cluster whose fifth word is confusable with three members but obvious against the fourth is miscalibrated even though four of its ten pairs look fine.

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
- Every cluster holds exactly five words, and **all ten of its combinations** must sit at the cluster's tier. Calibrate the set, not the individual words.
- No word appears in two clusters of the same deck: a repeated word leaks from one match into the next. Words shared *within* a cluster are not a leak — that is how a cluster works.
- No pair is duplicated in either direction, within or across decks.
- Test every pair with short drawing turns before adding it to the production deck.
- Remove pairs that consistently reveal the imposter after one drawing or remain unsolvable after the maximum round count. If only one combination of a cluster is bad, replace the word it depends on rather than dropping the cluster.

## Browser Experience

### Screens

1. **Home** — Create room or join with code/link.
2. **Lobby** — Player list, readiness, host settings, share controls, and start button. The host's roster rows carry a remove control; nobody else's do.
3. **Private word reveal** — Show only the player's assigned word and a reminder not to reveal it.
4. **Drawing round** — Shared canvas, active artist, turn queue, and timer.
5. **Discussion and decision** — Shared canvas, countdown, and private vote picker.
6. **Vote result** — Anonymous aggregate tally and elimination/no-elimination outcome.
7. **Final reveal** — Winner, assignments, words, canvas, and replay.

### Usability Requirements

- Support mouse, trackpad, and touch drawing.
- Make the active timer and current artist obvious.
- Announce a turn audibly as well as visually, so a player who is looking away
  still knows the pen has reached them.
- Order the roster by the running order while a round is in progress, and mark
  each player as having drawn, drawing, or waiting. A player should be able to
  answer "when am I up?" without counting.
- Carry that state during the handoff too, when there is no live artist. The
  handoff is when the question is asked most.
- Keep the shared canvas readable at the 10-player maximum.
- Avoid preserving permanent visual author attribution after a drawing turn.
- Preserve reconnectable room and match state on the server.

### Turn Audio

Drawing turns are short and the game is played with everyone talking at once, so
turn changes are carried by sound as well as by the screen.

- Every cue is synthesised in the browser. The game ships no audio files.
- Cues react to server events only. The client never sounds a transition it
  decided on its own, for the same reason it never decides a phase has ended.
- One cue per event at most. Two sounds landing together are heard as a third,
  unfamiliar sound, so where an event could justify two the more urgent one
  wins.
- The cues, in order of how much they interrupt:

  | Event | Cue |
  | --- | --- |
  | You are the artist now | Loudest in the game, and unlike any other cue |
  | Your own turn starts in five seconds | A tick a second, ending one second before the pen |
  | Three seconds of your own turn left | A tick a second, drier and higher |
  | The handoff named you as next artist | Two rising notes |
  | The match ended | A win or a loss flourish, per your own side |
  | Voting opened | One chord |
  | The tally arrived | Two falling notes |
  | Words were dealt, at the start of every round | Two rising notes, quiet |
  | Another player took the pen | One mid note, quiet |
  | A handoff that is not about you | One low note, quietest |

- Turn cues are on by default and muted from one control in the app bar. The
  choice persists per browser.
- The handoff counts down out loud for the player about to draw. A single
  announcement at the top of a ten-second handoff is missed, and missing the
  start of your own turn costs you the turn.
- Both countdowns are for one player only — the one who can act on them. Ten
  players hearing ten run-ups a round is noise, and a player watching somebody
  else draw has nothing to do when that turn expires.
- A handoff too short to hold the full run-up gets a shorter one rather than a
  late one, and drops the spoken announcement so the two do not collide.
- Cues keep playing in a backgrounded tab — being told it is your turn while
  reading something else is the point — but a tick that arrives late, because
  the browser throttled its timer, is dropped rather than played against a
  deadline that has already passed.

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
| Pen rule | Free | Free, One line, Max 5 |
| Maximum rounds | 2 | 1–4 |
| Drawing turn | 15 seconds | 5–60 seconds |
| Discussion and decision | 120 seconds | 30–180 seconds |

With 10 players at the default configuration, two rounds take approximately nine minutes before result screens:

`(6 seconds word reveal + 10 players × 15 seconds drawing + 120 seconds discussion/decision) × 2 rounds`

The word reveal is per round rather than per match, so raising the round ceiling costs 6 seconds a round on top of the drawing and discussion time. At the 4-round maximum that is 24 seconds of the total, which is why it was not given a host-facing setting.

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
