# Verso — Implementation Plan

Implementation plan for the game specified in [`DESIGN.md`](./DESIGN.md).

**Status:** planning complete, no code written yet.
**Date:** 2026-08-26

The design doc is **accepted as-is**. Its known asymmetries are deliberate and must not be "fixed":

- Anonymous canvas with no author attribution (`DESIGN.md:40`) — intentional.
- 3-player matches end on a single wrong vote (`DESIGN.md:70`) — intentional.
- Vote breakdown is never revealed, only aggregates (`DESIGN.md:56`) — intentional.
- No built-in chat; discussion happens on an external voice call (`DESIGN.md:48`) — intentional for v1.

---

## 1. The one requirement that shapes everything

Each player holds a secret word. One player's word differs from everyone else's. **A player's word must never reach another player's client** — not in a message payload, not in a debug frame, not in a log line.

This is the only unrecoverable bug class in the project. A visual glitch is a bug; a leaked word silently ruins the match and nobody notices it happened. Every architectural choice below is downstream of making that leak hard to write and easy to test.

Three independent defenses, cheapest first:

1. **Type-level** — a secret-bearing message cannot be passed to `Broadcast` (§4.2). Fails at `go build`.
2. **Structural** — exactly one function builds a player's view, and it is the only place the word is read.
3. **Empirical** — a canary test asserts the string never appears in any broadcast frame's bytes (§6, step 10).

---

## 2. Tech stack

| Concern | Choice | Rationale |
|---|---|---|
| Language | **Go ≥ 1.26.6** | Stated preference. `1.26.6` is a **security floor**, see §3. |
| Transport | **`github.com/coder/websocket`** (ISC) | Concurrency-safe `Write`, full RFC 7692 deflate, actively maintained. |
| Concurrency | **One goroutine per room** (actor) | State owned by one goroutine ⇒ no mutexes, and a whole match is unit-testable. |
| Timers | **`time.Timer` in the room's `select`** | Plus `testing/synctest` for virtual-time tests. |
| Schema | **`.proto`** → `protoc-gen-go` + `protoc-gen-es`, driven by `buf` | The only mechanism that generates a real sum type in Go **and** a real discriminated union in TS from one source. |
| Wire format | **Protobuf binary** over plain WebSocket | Envelope + `oneof` + correlation id. |
| Frontend | **Vite + TypeScript**, no framework required | Served from `embed.FS`. |
| Canvas | **Canvas2D, two layers** | ~30 lines of smoothing, no drawing library. |
| Stroke coords | **1024×768 logical, ×4 signed int16 grid** | Signed so overshoot past the edge isn't clipped. |
| Stroke batching | **50 ms** | ~20 msg/s, ~10 kB/s per active room. |
| Persistence | **None. In-memory rooms.** | `DESIGN.md:204` needs reconnect across a *socket drop*, not a *server restart*. |
| Deploy | **Single static binary** | Frontend embedded. |

### Rejected, with reasons

| Rejected | Reason |
|---|---|
| **Nakama** | Requires Postgres/CockroachDB and a `.so` plugin pinned to an exact Go toolchain. Removes almost none of this work. Its real value (accounts, matchmaking, leaderboards, LiveOps) is out of scope. |
| **Colyseus** | Genuinely excellent — `StateView` + `clock` + `allowReconnection` map onto this spec almost line-by-line. Rejected only because it means switching languages to solve a problem that costs zero in Go, and its own docs warn `StateView` isn't optimized for large datasets — an append-only stroke log is the worst possible shape for a schema differ. **Pick this if the team ever refuses to write Go.** |
| **boardgame.io** | Best-in-class hidden-state support (`playerView` / `STRIP_SECRETS`), but **no server-side phase timers** (issues open for years; documented workaround is client timers) and npm frozen at `0.50.2` since 2022-11-10. |
| **Playroom / Rune** | No server authority over secrets. Playroom elects a *player's browser* as host. Disqualified, not merely awkward. |
| **pitaya** | Browser client (`pitaya-protocol-js`) untouched since 2018. |
| **centrifuge / Centrifugo** | Pub/sub transport. Adds a hop without removing the phase machine, which is the actual work. |
| **lance-gg / geckos.io / nengi** | Dormant, or built for action-game tick loops. |
| **ConnectRPC / gRPC-Web** | `connect-web` throws for anything other than `server_streaming`; browser bidi is structurally impossible over Fetch. gRPC-Web is officially feature-frozen and has publicly declined bidi. |
| **`gzuidhof/tygo`** | Emits `export type Event = any` for interface-typed fields — **silently erases the entire tagged union**. 0 commits in 90 days. |
| **`coder/guts`** | Better generator (correct `encoding/json` embedding), but also cannot generate discriminated unions. No Go→TS generator can — hence protobuf. |
| **`perfect-freehand`** | Healthy and good, but returns a closed polygon you must `fill()`, has **no incremental mode by design** (O(n²) per stroke), and its value is pressure-modulated width — and pressure is unusable here (§7). Adopt only as a deliberate *aesthetic* choice. |
| **Yjs / Automerge / Liveblocks** | Single writer + append-only + server sequencer = total order by construction. Figma has genuinely concurrent editing and still rejected CRDTs. |
| **Konva / Fabric / paper.js / rough.js** | Scene graphs with hit-testing and transforms, none of which this canvas has. |
| **tldraw internals** | Not MIT — `"SEE LICENSE IN LICENSE.md"`, SPDX `NOASSERTION`. Do not vendor. |
| **WebTransport / WebSocket-over-HTTP-2** | Go's RFC 8441 support is Proposal-Accepted but Backlog; the x/net attempt was abandoned. Treat as a **durable** constraint, not a temporary one. |

---

## 3. Go version floor

The toolchain on this machine is **go1.26.5**. Bump before writing transport code.

- **GO-2026-4870 / CVE-2026-32283** — an unauthenticated peer sending multiple TLS 1.3 post-handshake `KeyUpdate` records in one record can **deadlock the connection and hold it persistently**. Aimed squarely at long-lived connections. Fixed in **1.25.9 / 1.26.2**.
- **GO-2026-6089** — `ReadHeaderTimeout` not applied during the unencrypted HTTP/2 preface sniff. Fixed 1.25.13 / **1.26.6** / 1.27.0-rc.3.
- Plus two `x/net/http2` issues: CVE-2026-27141 (frames `0x0a`–`0x0f` panic a running server) and CVE-2026-33814 (`SETTINGS_MAX_FRAME_SIZE=0` → infinite CONTINUATION loop).

**Floor: Go 1.26.6.** Prefer 1.27.x — `encoding/json` v2 is the default there and roughly halves unmarshal cost at zero allocations, and Green Tea GC (default in 1.26) suits a many-small-objects workload.

Also: `golang.org/x/net/http2/h2c` is now formally deprecated. Use `Server.Protocols` + `SetUnencryptedHTTP2(true)`.

*Unresolved:* whether MadeYouReset (CVE-2025-8671) affects Go's HTTP/2. Zero hits in the Go vuln DB and zero in `golang/go` issues. Do not assume either way.

---

## 4. Architecture

### 4.1 Repo layout

```
verso/
  cmd/verso/main.go            # flags, http server, embed.FS mount
  proto/verso/v1/game.proto    # single source of truth
  internal/
    gen/                       # protoc-gen-go output (committed)
    transport/                 # coder/websocket, envelope codec, seat auth
    room/
      room.go                  # actor: inbox select loop
      phase.go                 # phase machine
      vote.go                  # strict majority, elimination
      view.go                  # viewFor(playerID) — the ONLY place a word is read
      strokes.go               # append-only log
    registry/                  # room codes, create/join, GC
    words/                     # decks, difficulty tiers
  web/
    src/                       # Vite + TS client
    gen/                       # protoc-gen-es output (committed)
  cmd/bot/                     # headless protocol client for playtesting
  docs/
    DESIGN.md
    IMPLEMENTATION_PLAN.md
```

### 4.2 Audience typing — the secret-leak defense

Two separate message types, never one type with a redacted projection. A field-level "redact for others" scheme cannot be enforced by either type system and will not survive codegen.

```go
type Event interface{ eventKind() EventKind }

type Broadcastable interface {
    Event
    broadcastSafe()   // unexported: no other package can opt a type in
}

func (RoundStarted) broadcastSafe() {}
func (StrokePoints) broadcastSafe() {}
// YourWord deliberately has NO broadcastSafe method.

func Broadcast(e Broadcastable)       {}  // room-wide
func SendTo(playerID string, e Event) {}  // unicast
```

`Broadcast(YourWord{...})` then fails to compile:

```
cannot use YourWord{…} as Broadcastable value in argument to Broadcast:
  YourWord does not implement Broadcastable (missing method broadcastSafe)
```

This survives regeneration because **methods are invisible to every code generator** — `protoc-gen-go`, `tygo`, and `guts` all read struct fields only. The markers live in hand-written Go beside the generated types.

> **Do not use embedding for the private variant.** `type Private struct { Public; Secret string }` makes `Private` a structural subtype of `Public` in the generated TypeScript, so TS will accept the private one anywhere the public one is expected — precisely the bug being prevented. Duplicate the fields or nest them.

In protobuf, make them sibling variants of the same `oneof`: `RoundStarted` (broadcast) and `YourWord` (unicast). The union stays closed and the secret has no home on a broadcastable type.

### 4.3 Protocol shape

Envelope + `oneof` + correlation id. Nakama (`rtapi/realtime.proto`, ~50 variants), LiveKit (`SignalRequest`/`SignalResponse`), Centrifugo, Zed and pitaya all independently converged on this. It is a convention, not a library — Google has explicitly declined to spec gRPC-over-WebSocket.

```proto
message ClientCommand {
  string cid = 1;                    // correlation id, echoed back
  oneof cmd {
    JoinRoom     join         = 2;
    SetReady     set_ready    = 3;
    StartMatch   start_match  = 4;
    StrokeBegin  stroke_begin = 5;
    StrokePoints stroke_points= 6;
    StrokeEnd    stroke_end   = 7;
    CastVote     cast_vote    = 8;
  }
}

message ServerEvent {
  string cid = 1;
  oneof evt {
    // broadcast
    LobbyState    lobby_state    = 2;
    RoundStarted  round_started  = 3;
    TurnStarted   turn_started   = 4;
    StrokeBegan   stroke_began   = 5;
    StrokePoints  stroke_points  = 6;
    StrokeEnded   stroke_ended   = 7;
    PhaseChanged  phase_changed  = 8;
    VoteTally     vote_tally     = 9;
    MatchEnded    match_ended    = 10;
    // unicast only
    YourWord      your_word      = 11;
    Snapshot      snapshot       = 12;
    SpectatorInfo spectator_info = 13;   // DESIGN.md:67 — eliminated player learns the imposter
  }
}
```

Rules:
- **`int32` / `sint32` only.** `int64`/`uint64` generate `bigint` in TS, which `JSON.stringify` throws on.
- Every exhaustive `switch` must handle the `{case: undefined}` member that `protoc-gen-es` emits. Validate once at the socket boundary, then narrow.
- Send **binary**. ProtoJSON is worst-of-both — larger than hand-rolled JSON *and* ~3.5× slower to parse.
- Client must set **`ws.binaryType = "arraybuffer"`**. The browser default is `"blob"`, which forces an async hop. Undocumented in protobuf-es.
- Handle unknown proto3 enum values, or a newer server breaks older clients.

**Drift detection.** A new `oneof` variant makes every `switch (e.evt.case)` with a `default: never` fail at `tsc --strict`. CI runs `buf generate` then `git diff --exit-code` so stale generated files also fail the build.

### 4.4 Room actor

One goroutine owns all room state. No mutexes anywhere.

```go
func (r *Room) run(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():      return
        case m := <-r.inbox:    r.handle(m)      // client commands
        case <-r.phaseTimer.C:  r.onDeadline()   // phase expiry
        case <-r.sweep.C:       r.checkLiveness() // shared ticker, see below
        }
    }
}
```

**No per-connection writer goroutine.** `coder/websocket`'s `Write` is concurrency-safe, so the room goroutine drains each client's bounded queue directly. Keep the bounded queue — it is what provides backpressure and the kick policy — and drop only the goroutine. This halves goroutine count and stack memory, and **is not available with `gorilla/websocket`**, where a writer goroutine is mandatory. It is the sharpest concrete reason for the transport choice.

**One shared ticker, not per-connection ping timers.** Centrifugo measured >50% CPU reduction replacing per-connection timers with a shared wheel. The room goroutine already owns timers; sweep liveness there.

**Slow clients:** bounded outbound queue, drop-or-kick on full. One slow client must never stall a room.

### 4.5 Phase machine

```
lobby → assigning → drawing(turn 1..n) → discussion → voting → resolving
                          ↑                                        │
                          └──────────── next round ────────────────┘
                                                                   ↓
                                                                 ended
```

- Turn order reshuffled at the start of every round (`DESIGN.md:36`).
- Discussion and voting share one combined timer (`DESIGN.md:46`).
- Phase ends early when every active player has voted (`DESIGN.md:52`).
- A missing vote is an **abstention**, counted in no bucket — never promoted to `Skip` (`DESIGN.md:52`).
- Elimination is a **plurality with `Skip` on the ballot**: strictly ahead of every other candidate and strictly ahead of `Skip` (`DESIGN.md:58`).
- Any tie for first ⇒ nobody eliminated, next round.
- Imposter eliminated ⇒ group wins. Non-imposter eliminated ⇒ they become a spectator and privately learn the imposter's identity (`DESIGN.md:67`).
- Imposter survives the final round, **or** only two active players remain ⇒ imposter wins.
- Imposter disconnects ⇒ match ends immediately, group wins (`DESIGN.md:119`).

Test this under **`testing/synctest`** before any client exists. A full 10-minute match runs through every phase in microseconds of virtual time — no `Clock` interface to thread through, since `synctest` went GA in Go 1.25.

### 4.6 Seats and reconnect

- Signed seat token binds a `playerID` to a new socket within a grace window.
- On reconnect: full `Snapshot` + replay the entire stroke log in one message. After RDP simplification, 40 strokes × ~40 points ≈ 1,600 points ≈ 15 kB JSON / 6.5 kB binary. **No incremental catch-up machinery, no vector clocks, no gap detection.**
- A disconnected non-imposter misses their turn and their vote becomes `Skip` (`DESIGN.md:116`).

**Open:** grace window duration.

**Resolved:** a disconnected-but-seated player is **not** active and is **excluded from the strict-majority denominator**, the turn order, and the two-players-remain count. `DESIGN.md` now defines "active" explicitly under Disconnect Rules. The seat, word, and match state are still retained for reconnect. Compute the denominator from currently-connected, non-eliminated players **at tally time**.

### 4.7 Canvas and stroke sync

**Rendering — two stacked Canvas2D layers:**
- **Base** — committed strokes. Written once per stroke end. **Never cleared.**
- **Overlay** — the single in-progress stroke. Cleared and redrawn each `rAF`.

On stroke end, `baseCtx.drawImage(overlay, 0, 0)` and clear the overlay. Because the canvas is append-only, per-frame work is **one stroke's geometry regardless of accumulated content** — which is what eliminates any case for SVG or WebGL.

Add `getContext('2d', { desynchronized: true })` and feature-detect via `ctx.getContextAttributes().desynchronized`. Free upside.

**Geometry — ~30 lines, no library.** Midpoint-quadratic smoothing, then `ctx.stroke()` with `lineJoin`/`lineCap: 'round'`. The browser rasterizes joins and caps faster than any JS can, and this is genuinely incremental — extend a stroke by appending one `quadraticCurveTo` rather than re-resolving a whole polygon.

**Simplification:** copy the ~25-line RDP function from `simplify-js` (BSD-2, dormant since 2020 — don't add the dependency). Run it **once on `pointerup`**, never in the hot path.

**Wire:** flat interleaved int arrays on a **4096×3072 signed int16** grid (a 1024×768 logical space at ¼-unit precision). Signed matters — scribble.rs's own source comment explains it is so strokes dragged past the canvas border aren't chopped off. Colors are **palette indices**, not strings: one `uint8`, server-authoritative, so a client can't inject arbitrary colors.

**Batch at 50 ms** (~20 msg/s, 3–6 points per message, ~10 kB/s egress per active room with 7 viewers). Per-message overhead dominates payload at every realistic sample rate, so message *rate* is the thing to optimize, not bytes. tldraw uses `COLLABORATIVE_MODE_FPS = 30`; Excalidraw uses `CURSOR_SYNC_TIMEOUT = 33`. Latency budget: 50 ms batch + 30–80 ms RTT + 16 ms render ≈ 100–150 ms, well inside the ~300 ms threshold below which spectators perceive no difference.

**Never round-trip the artist's own ink.** Render their stroke from local points immediately. The perceptible-latency threshold for direct manipulation is ~2.4 ms (Jota et al., CHI '13) — unreachable over a network. Viewers get the ~150 ms path; the artist must not.

**Server-side authority (all three, non-negotiable):**
1. Reject stroke events from anyone who is not the current artist.
2. Clamp brush width. scribble.rs's comment: *"prevent clients from lagging due to too thick lines."*
3. **Cap total points per turn.** An append-only log with no cap is a trivial memory-exhaustion vector.

**Sizing — fixed logical space + CSS letterbox.** This is settled by production practice: skribbl.io hardcodes 800×600, scribble.rs 1600×900. Use **1024×768 (4:3)** — 4:3 wastes far less screen in portrait for a phone-heavy party game. Excalidraw/tldraw/Figma use the opposite model (infinite scene + per-viewer camera), but they are explicitly *not* "everyone sees the same picture" products; a deduction game is.

```css
#stage { width:100%; height:100%; display:grid; place-items:center; background:#111; }
#pad   { aspect-ratio: 4/3; width:100%; max-width:100%; max-height:100%;
         background:#fff; touch-action:none; }
```

`aspect-ratio` + `max-width/max-height` in a centering grid *is* `object-fit: contain`, and `getBoundingClientRect().left/top` **is** the letterbox offset — no separate offset variable to get wrong.

**No CRDT.** The write set has a total order by construction: single writer enforced by the server, append-only so no operation can conflict, one WebSocket per client so TCP already guarantees ordering, and the server is the sequencer. Figma's own writeup is the authoritative statement of this reasoning — and Figma has *genuinely* concurrent editing.

One real risk without a CRDT: a dropped message silently diverges one viewer's canvas. Mitigate with a monotonic `seq` per stroke event and a full-state refetch on a gap (~15 kB). Excalidraw's belt-and-braces alternative is a periodic full resync (`SYNC_FULL_SCENE_INTERVAL_MS = 20000`). Either is a dozen lines.

---

## 5. Reference implementation to read

**`github.com/scribble-rs/scribble.rs`** — Go + vanilla JS, BSD-3, active (last push 2026-05-31). Closest match to these constraints by a wide margin. Read it before writing the stroke layer.

Worth copying: the signed-`int16` coordinate decision, the append-only `currentDrawing []any` log, `lobby.canDraw(player)` gating, server-side brush clamping, the fixed canvas + CSS aspect-lock.

**Do not copy:** it sends one WebSocket message per `pointermove`, unbatched (~5 kB/s upstream, ~55 kB/s egress at 11 viewers). It works in production, so this isn't fatal — but 50 ms batching is a 3–5× win for a dozen lines. Also skip its legacy dual touch+pointer handlers and hand-rolled pointer capture.

Also useful:
- **skribbl.io** — reverse-engineered protocol. Packet 19 = draw, flat 7-element int array `[tool, colorId, brushSize, x1, y1, x2, y2]`, palette indices, fixed 800×600, and reconnect via a full `drawCommands` log in packet 10. This design, already shipped at scale. *(Third-party reverse engineering, not official docs.)*
- **`sk89q/sketch`** (`resources/assets/js/pen.js`) — if binary stroke encoding is ever wanted, this is the pattern: opcode stream with stateful color/width and abs/rel delta switching (3 bytes per point in the common case).
- **`Skribblrs.io`** — instructive counter-example. Blind relay, zero server state, therefore **no reconnect replay at all**, raw normalized floats and full CSS color strings at 100 msg/s ≈ 10× the recommended traffic. Three anti-patterns in 130 lines.

---

## 6. Milestones

Ordered so the hard part is testable before any UI exists.

| # | Milestone | Done when |
|---|---|---|
| 1 | **Scaffold** — new git repo, Go ≥1.26.6, `buf`, Vite+TS, `embed.FS` | `go run ./cmd/verso` serves a built page |
| 2 | **`game.proto`** — envelopes, all message types, `buf generate` in the dev loop | Go + TS types generate; CI fails on stale output |
| 3 | **Audience types** — `Event` / `Broadcastable`, `Broadcast` / `SendTo` | A test asserting `Broadcast(YourWord{})` does **not** compile |
| 4 | **Room actor** — inbox, phase timer, bounded per-client queues, drop-or-kick | A room can be driven entirely by channel sends |
| 5 | **Phase machine** — every transition in `DESIGN.md:27` | Full match through all phases under `synctest` |
| 6 | **Game rules** — decks, assignment, votes, majority, elimination, win conditions | Table tests for 3/6/10 players incl. both win paths and the imposter-disconnect case |
| 7 | **Seats + reconnect** — signed token, grace window, snapshot + stroke replay | A client can drop mid-turn and rejoin with identical state |
| 8 | **Stroke relay** — append-only log, artist gating, width clamp, per-turn point cap | Non-artist stroke events rejected; cap enforced |
| 9 | **Bot harness** — headless Go protocol clients | 3-, 6-, and 10-player matches run end-to-end in CI |
| 10 | **Canary test** — seed `SECRET_CANARY`, assert absent from all broadcast bytes | Green, and wired into CI |
| 11 | **Client** — seven screens (`DESIGN.md:183`), two-layer canvas, Pointer Events | A real 3-player match playable in browsers |
| 12 | **Export + polish** — `OffscreenCanvas` worker, vector re-render at 2× | Final canvas exports as PNG without jank on Safari |
| 13 | **Playtest and curate decks** — `DESIGN.md:171`, `DESIGN.md:221` | Pairs that trivially reveal or never resolve are removed |

Milestones 4–10 are the bulk of the work — roughly 1,100–1,400 lines of production Go plus 400–600 of tests.

Milestone 9 is not optional and not late polish: **a 10-player game cannot be manually playtested**, and the bot harness constrains the protocol to be drivable without a browser. Build it before the client, not after.

---

## 7. Client gotcha checklist

Distilled from platform research; each of these is a real bug someone shipped.

**Input**
- **Pointer Events only.** W3C Recommendation as of Level 3 (June 2026). Drop mouse/touch fallbacks.
- **`getCoalescedEvents()`** — browsers throttle `pointermove` to ~1/frame, dropping 2–4 real samples on a 120–240 Hz digitizer. Guard for empty/1-element returns. **On iOS 18.2 coalesced events lack `pointerId` and `target`** — take identity from the *outer* event, geometry from the *inner*. *(Unverified whether Apple has fixed this; assume not.)*
- **Don't use `pointerrawupdate`** — Safari has never shipped it.
- **`setPointerCapture`** on `pointerdown`. Without it, dragging off-canvas stops delivering moves and `pointerup` never arrives — the stroke hangs live forever. Touch and pen get implicit capture; **mouse does not**. Always also terminate on **`lostpointercapture`** and **`pointercancel`** (fires on orientation change, app switch, Pencil palm rejection).
- **Ignore `pressure`.** Spec says `0.5` when down on hardware without pressure support, so mouse-down is *exactly* `0.5` — zero signal. iOS non-Pencil returns hard `0` (3D Touch was removed after the iPhone XS; Haptic Touch is a timer, not a sensor). Use **velocity-based** width instead. Gate any Pencil bonus hard: `pointerType === 'pen' && pressure > 0 && pressure !== 0.5`.
- **Use `event.buttons`** (bitmask) on move events, not `event.button` — `button === 0` is ambiguous with the uninitialized state.
- **Ship no palm rejection.** There is no API. `width`/`height` return 1 on iOS always. iOS gives it free and unavoidably via `requiresExclusiveTouchType`. A heuristic will reject legitimate input more often than it helps.

**Touch / scroll suppression**
- `touch-action: none` on the canvas is the load-bearing line. Scope it to the canvas — page-wide it disables pinch-zoom and violates WCAG 2.0 SC 1.4.4.
- **`touch-action: manipulation` suppresses `pointercancel` on iOS** (WebKit 240917) → stuck strokes. If it appears anywhere in the ancestor chain, expect them. Use `none`, or spell out `pan-x pan-y pinch-zoom`.
- **Attach non-passive listeners to the canvas element**, never `window`/`document`/`documentElement`/`body` — the DOM spec forces `passive: true` on exactly those four targets for `touchstart`/`touchmove`/`wheel`.
- Keep the non-passive `touchmove` guard: `preventDefault()` on `pointerdown` only started suppressing iOS scroll in **Safari 26.5**.
- `maximum-scale` / `user-scalable=no` have been ignored by iOS Safari since iOS 10 — Android only.

**DPR and sizing**
- **`setTransform`, not `scale`.** Assigning `canvas.width` resets context state, so `scale(dpr,dpr)` is safe right after a resize — but on the no-op path it **compounds**. `setTransform` is idempotent.
- **`getBoundingClientRect()`, not `clientWidth`** — the latter is specified to round to an integer, losing the true device-pixel edge.
- **`devicePixelContentBoxSize` is unavailable in Safari** (WebKit 219005, status NEW since 2020). The `dpr × getBoundingClientRect()` path is primary, not a fallback.
- **No `devicepixelratiochange` event exists.** Rebuild a `(resolution: Xdppx)` media query each time it stops matching. Page zoom and moving between monitors change DPR; pinch-zoom does not.
- **Clamp canvas size by *area*, not dimension.** iOS is 8192×8192 = 67.1M *device* pixels (16.7M before iOS 18); desktop WebKit 16384². Exceeding it **fails silently** in Chrome (blank canvas, no throw, no console message) and Safari; only Firefox throws.
- Safari 26.2 added **fractional** `PointerEvent` coordinates — before that iOS gave integer CSS pixels, i.e. visibly chunky strokes on high-DPR screens. Biggest stroke-quality change in years.
- iOS caps rendering near 60 Hz by default even on ProMotion. Don't design for 120.

**Export**
- **`toBlob('image/png')`, re-rendering the vectors at 2×.** Never `drawImage`-upscale the display bitmap — it resamples pixels already discarded, and `imageSmoothingQuality` can't recover them.
- **Do it in a worker via `OffscreenCanvas.convertToBlob()`.** This is the one real reason to use OffscreenCanvas: Firefox encodes `toBlob` on a background pool, Chrome on the **main thread** in idle chunks, and **WebKit synchronously on the calling thread** — so main-thread `toBlob` blocks on Safari. In a worker, `convertToBlob` is off-thread in all three. (Do *not* move live drawing to a worker — pointer events still arrive on the main thread, adding a latency hop to the one thing that can't afford one.)
- There is **no promise-based `toBlob`** — the spec IDL returns `undefined` and takes a callback. Wrap it. `convertToBlob` is the only promise variant.
- **The transparency trap:** `getContext('2d', {alpha:false})` gives opaque **black**, not white, and alpha-less formats composite onto black. Always `fillRect` white before export.
- Prefer `toBlob` over `toDataURL` — base64 is +33%, built synchronously, and a 3200×2400 PNG becomes a multi-MB string.
- `quality` is ignored for PNG. `image/webp` is unsupported in Safari's `toBlob` and **silently falls back to PNG** — check `blob.type`.
- If a logo or background image is ever composited in: set `img.crossOrigin='anonymous'` **before** `.src`. Without it the image loads and silently taints the canvas, and export then throws `SecurityError`.
- Delivery: prefer the share sheet on iOS (`navigator.canShare({files})` → offers "Save Image"); fall back to `<a download>` + object URL. Firefox never supports file sharing — always gate on `canShare`.

---

## 8. Open questions

| # | Question | Blocks | Recommendation |
|---|---|---|---|
| 1 | **Deploy target** — localhost-for-friends, or public URL? | Decides whether room GC, rate limiting, and abuse handling exist at all | Doesn't block milestones 1–10. Answer before 11. |
| 2 | **Reconnect grace window** duration | Milestone 7 | Start at 60 s; tune from playtests. |
| 3 | ~~Do disconnected-but-seated players count in the majority denominator?~~ **Decided** | Milestone 6 | **No.** Disconnected ⇒ not active ⇒ out of the denominator. `DESIGN.md` "Active players" is now normative. |
| 4 | **Host migration** — host leaves the lobby or mid-match | Milestone 4 | Undefined in the design doc. Simplest: promote the longest-connected active player. |
| 5 | **Disconnect mid-drawing-turn** — timer runs out, or skip immediately? | Milestone 5 | Undefined in the design doc. Recommend skip immediately. |
| 6 | **Verify deflate on real iOS Safari** | Before shipping | iOS Safari historically violated `client_no_context_takeover` (gorilla#731); unconfirmed for 2026, and all benchmarks were V8, not JavaScriptCore. |
| 7 | **Balance at high player counts** | Milestone 13 | Two rounds at 10 players is very likely imposter-favored. Accepted for v1; revisit with playtest data. |
| 8 | **Word-deck quality** | Milestone 13 | The actual product risk. No engineering fixes it. |

---

## 9. Out of scope

Everything in `DESIGN.md:229`, plus:

- Persistence across server restarts.
- Horizontal scaling, multi-node rooms, Redis.
- WebTransport or WebSocket-over-HTTP/2 — blocked upstream in Go, durably.
- Binary stroke encoding beyond protobuf (`sk89q/sketch` is the pattern if ever needed).
- Accounts, matchmaking, leaderboards. If these are ever genuinely wanted as a *product*, revisit Nakama then.
