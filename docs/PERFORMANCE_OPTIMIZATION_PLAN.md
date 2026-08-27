# Verso — Performance and Latency Optimization Plan

**Status:** implementation specification  
**Date:** 2026-08-27  
**Scope:** smoother local drawing, lower spectator latency, and safer server CPU/memory use.  
**Out of scope:** changing game rules, adding a framework, replacing Canvas2D, or changing the wire protocol unless a named step requires it.

## 1. Executive order

Implement and measure in this order:

1. Stop publishing unchanged UI state for stroke events.
2. Make a pointer-down immediately leave a provisional local mark.
3. Make failed canvas resync recover automatically.
4. Close superseded and kicked WebSocket connections.
5. Bound expensive inbound requests and malformed payloads.
6. Profile before pursuing allocation, replay, or broadcast-encoding optimizations.

The first item is the expected high-impact drawing improvement. The second and third improve perceived responsiveness and reliability. The server work protects the deployed service under connection churn and adversarial or accidental load; it is not expected to improve a normal local stroke directly.

## 2. Current architecture and where time is spent

### 2.1 Local artist path

```text
PointerEvent
  -> PointerInput.onMove()
  -> CanvasEngine.extend()
  -> dirty flag
  -> next requestAnimationFrame()
  -> StrokePen.flush() on overlay canvas

in parallel:
  -> 50 ms batch timer
  -> WebSocket StrokePoints
  -> server
  -> echoed StrokePoints
```

Local ink is correctly predicted and does not wait for the server. It does, however, wait for the next animation frame, which adds between zero and one display interval (up to 16.7 ms at 60 Hz).

### 2.2 Viewer path

```text
Artist batch wait (0–50 ms)
  -> WebSocket/server/WebSocket
  -> CanvasEngine.applyStrokePoints()
  -> remote playback queue (currently 50 ms target)
  -> animation frames that drain queue to overlay
```

The second 50 ms window is intentional visual smoothing. It improves continuous-looking remote motion but adds arrival-to-pixel delay. Treat it as a product latency budget, not a server stall.

### 2.3 The primary client coupling

`web/src/main.ts` sends each incoming stroke event to `CanvasEngine` and then unconditionally calls `store.apply(frame)`.

`web/src/state/store.ts` intentionally returns the *same* `GameState` object for `strokeBegan`, `strokePoints`, and `strokeEnded`, because those messages carry no UI state. But `GameStore.commit()` still notifies every subscriber. The drawing screen then reruns its screen renderer and `playerList.update()` destroys and recreates roster rows.

At the current `STROKE_BATCH_MS = 50`, this can happen about 20 times per second while the main thread is also receiving pointer input and rasterizing canvas segments. The artist receives its own echo, so this is not a spectator-only cost.

## 3. Non-negotiable constraints

All changes must preserve these properties:

- The local artist sees prediction immediately; authoritative server geometry remains the final committed version.
- A local stroke must never be replayed after reconnect into another player's turn.
- The server remains authoritative for artist identity, color validity, width, point counts, and ordering.
- Only the active artist can create a stroke.
- One room actor goroutine owns room state. Do not introduce concurrent room mutation.
- Slow client delivery remains non-blocking for the room actor.
- Private messages, especially words and seat tokens, must never become broadcast cache entries or log fields.
- Keep the two-canvas model: immutable/committed base plus one live overlay. Do not redraw every historical stroke on every input event.
- Do not move live artist rendering into a worker. The additional cross-thread hop is counter to the input-to-pixel goal.
- Do not lower the 50 ms send interval without coordinating `DefaultCommandRate` and its tests. A 25 ms interval can exceed the current sustained 40-command/sec allowance once begin/end messages are included.

## 4. Client implementation plan

### C1. Suppress store publication when state is unchanged

**Priority:** P0  
**Expected benefit:** highest general rendering improvement, especially on mobile and in a 10-player room.  
**Files:** `web/src/state/store.ts`; verify wiring in `web/src/main.ts`.

#### Change

Change `GameStore.apply()` so it does not call `commit()` if `reduce()` returns the exact same object reference:

```ts
apply(frame: ServerFrame): void {
  const next = this.reduce(this.state, frame.body);
  if (next === this.state) return;
  this.commit(next);
}
```

This is preferable to special-casing stroke variants in `main.ts`: the store owns its own notification semantics, and referential equality already expresses its no-change contract.

Do not skip `CanvasEngine.applyStrokeBegan`, `applyStrokePoints`, or `applyStrokeEnded`. `main.ts` must continue routing raw stroke events to the engine before the store decision.

#### Why it is safe

- The reducer already declares stroke messages to have no `GameState` effect.
- Canvas rendering happens outside the store.
- Timer updates use their own screen rAF loop.
- `createAudioDriver` compares actual state facts; it has no need for no-op stroke notifications.

#### Acceptance checks

- Add or extend a store unit test if web test infrastructure is introduced; otherwise manually assert that a `strokePoints` frame retains the existing state object and sends no listener notification.
- During an active stroke, the Chrome Performance trace must show no `chrome.render`, drawing-screen render, or `playerList.update` call caused by `strokePoints`.
- Verify normal updates still occur for turn changes, player presence, votes, snapshots, connection status, and errors.

### C2. Draw the initial local dot synchronously

**Priority:** P1  
**Expected benefit:** high perceived latency improvement for taps and very slow starts.  
**Files:** `web/src/canvas/engine.ts`, `web/src/canvas/geometry.ts`.

#### Current behavior

`CanvasEngine.begin()` creates a `StrokePen` and starts rAF but leaves `dirty` false. `StrokePen.flush()` initializes its cursor for a one-point stroke but emits no pixel. A tap can therefore wait for the server's `StrokeEnded` echo before it becomes visible.

#### Change

Make a provisional `StrokePen` render its first point as a round dot, then call it synchronously from `CanvasEngine.begin()` before `outbound.strokeBegin()`.

The least invasive shape is:

1. Teach `StrokePen.flush()` to emit the width-matched dot when `provisional === true`, it has initialized its first point, and the stroke has only one point.
2. In `CanvasEngine.begin()`, call `live.pen.flush(rec.pts)` immediately after creating `live`.
3. Leave the mark on the overlay only. Later local segments naturally overdraw it, and the server-authoritative finish path continues to clear overlay and render final geometry to base.

Do not add the point to the base layer early. The base must contain only committed server geometry.

#### Acceptance checks

- A quick mouse click, tap, or Pencil tap shows a visible dot without server round-trip delay.
- A slow start transitions smoothly from dot to line, with no double-opacity appearance or gap.
- The committed final image matches the current server-authoritative behavior.
- Test mouse, touch, pen, stroke cancellation, phase expiry mid-stroke, and delayed/missing `StrokeEnded`.

### C3. Make remote playback timing independent and measurable

**Priority:** P2  
**Expected benefit:** 20–40 ms lower viewer lag on a healthy connection; not an FPS optimization.  
**Files:** `web/src/canvas/engine.ts`.

#### Change

Keep `STROKE_BATCH_MS` as the network-send contract. Replace the implicit coupling:

```ts
const REMOTE_PLAYBACK_MS = STROKE_BATCH_MS;
```

with a separately named playback target. Start with a 1–2 frame target (for example, 24–32 ms) only after measuring at 60 Hz and 120 Hz.

An optional follow-up is a bounded adaptive target:

- minimum: one display frame;
- normal: 24–32 ms on low-jitter connections;
- maximum: current 50 ms when packet jitter is observed;
- backlog escape remains immediate when `MAX_PLAYBACK_LAG_PAIRS` is exceeded.

Do not reduce send batching to 25 ms as part of this change. That is a transport-rate decision and must be tested against the command limiter.

#### Acceptance checks

- Measure remote frame arrival to first rendered segment under 0, 20, and 50 ms simulated jitter at 30, 80, and 150 ms RTT.
- Confirm lower latency does not make normal strokes visibly jump between 50 ms batches.
- Confirm a stalled/backgrounded tab still drains backlog rapidly rather than replaying stale ink slowly.

### C4. Retry a failed canvas resync

**Priority:** P0 reliability  
**Expected benefit:** prevents a viewer from appearing permanently frozen after a gap plus dropped snapshot.  
**Files:** `web/src/net/socket.ts`, `web/src/canvas/engine.ts`; test with transport/room harnesses where feasible.

#### Current behavior

Socket sequencing sets `resyncing = true` after a gap and drops further stroke events. It only clears that state when `snapshot` arrives. The server deliberately drops an event if a slow client's bounded outbound queue is full. Therefore, a dropped snapshot can leave the client ignoring stroke traffic for the rest of the phase.

#### Change

Add a bounded retry timer owned by `VersoSocket`:

- start it when `requestResync()` first transitions to resyncing;
- request a new snapshot after a timeout if no snapshot arrived;
- use bounded exponential backoff with jitter;
- cancel it in `resetSync()`, socket teardown, and on snapshot arrival;
- never issue parallel snapshot requests;
- cap attempts, then reconnect or expose a recoverable failure according to existing connection policy.

The canvas engine's separate sequence-gap cooldown should not cause competing uncontrolled requests. Keep a single owner for automatic retry policy; preferably the socket, because it owns `resyncing`.

#### Acceptance checks

- Force a sequence gap, intentionally drop the first snapshot, and confirm canvas events resume after a retry.
- Verify duplicate snapshots and late events do not create duplicate strokes.
- Verify snapshot retry traffic remains bounded during a continuing slow-client overload.

### C5. Coalesce resize work and budget backing-store memory

**Priority:** P1 memory safety; P3 resize smoothness  
**Files:** `web/src/canvas/surface.ts`, `web/src/canvas/engine.ts`.

#### Current behavior

`ResizeObserver` immediately calls `resize()`. When dimensions change, each base and overlay backing store is reallocated and `CanvasEngine.redrawAll()` synchronously rerenders the entire committed log.

The existing 16,777,216-pixel limit applies to each canvas independently. Two maximum RGBA backing stores require about 128 MiB before browser compositor/GPU copies.

#### Change

1. Schedule resize evaluation at most once per animation frame. Coalesce repeated `ResizeObserver` and DPR-change signals.
2. Preserve atomic visual behavior: resize both layers, restore transforms, then call one redraw before presenting the new backing stores. Do not leave an active canvas blank between frames.
3. Replace the per-layer-only cap with an explicit total live-surface budget. Begin with a mobile-safe raw bitmap budget of 64 MiB for both layers combined, subject to profiling.
4. Derive effective DPR from that total budget rather than blindly accepting the browser DPR. Keep logical coordinates unchanged.

#### Acceptance checks

- A dense canvas resized continuously gets at most one `redrawAll()` per visual frame, not one per observer callback.
- Phone rotation and monitor/DPR changes never leave a blank paper or skewed coordinates.
- Record base/overlay dimensions and estimated raw bytes in debug builds.
- Validate line sharpness at 1×, 2×, and fractional DPR.

### C6. Defer hot-path allocation changes until profiling proves need

**Priority:** P3  
**Files:** `web/src/canvas/input.ts`, `web/src/canvas/engine.ts`, `web/src/canvas/grid.ts`, `web/src/net/commands.ts`.

Each input update may allocate an input array, accepted-point array, grid array, and protobuf-owned points array. Remote playback also uses `splice(0, n)`, which shifts the remaining queue.

Do not replace ordinary arrays with a complicated typed-array pool before profiles show GC in missed frames. If warranted:

- append directly to the persistent logical stroke and a reusable outbound grid buffer;
- use a queue read index instead of front-splicing remote points;
- clear/reuse only buffers that are no longer referenced by protobuf encoding or WebSocket send;
- retain current point-cap and server-authority semantics.

## 5. Server implementation plan

### S1. Close displaced and kicked connections

**Priority:** P0  
**Expected benefit:** protects Cloud Run request slots, goroutines, queue buffers, and room capacity during reconnect churn.  
**Files:** `internal/room/reconnect.go`, `internal/room/api.go`, `internal/transport/conn.go`; coordinate with the in-progress Kick feature.

#### Current behavior

When a seat reconnects, the room replaces `Player.outbound` and best-effort sends an error to the old channel. The old WebSocket can remain open and responsive to pings indefinitely, even though it will receive no further room events. A kicked connection similarly relies too much on voluntary client close.

#### Required design

Do not let the room package import transport. Define a small room-owned outbound-session interface or data type that transport's `conn` implements/provides. It must support:

- non-blocking enqueue of a server event;
- ordered “close after queued terminal event” behavior;
- identity comparison so a late old-socket detach cannot detach the replacement session.

The transport write pump must serialize the terminal error before closing, with a bounded write deadline. The room actor must only request closure; it must never block waiting for it.

Do not call the connection context cancel immediately after placing the terminal event on the old outbound queue: that races the writer and can suppress the explanation.

#### Acceptance checks

- Reclaim a seat repeatedly and assert the old connection closes after receiving `BAD_SEAT`.
- Kick a lobby client and assert it receives `KICKED`, closes, cannot reclaim its old token, and does not consume a live connection slot.
- Verify old `detach` callbacks cannot mark the newly attached seat disconnected.
- Run existing reconnect, kick, ordering, and race tests.

### S2. Bound inbound resource consumption at the transport edge

**Priority:** P0  
**Expected benefit:** reduces allocation/CPU amplification before work enters rooms.  
**Files:** `internal/transport/server.go`, `internal/transport/conn.go`, transport boundary tests.

#### Change

1. Reduce the WebSocket read limit after verifying exact maximum command encoding. A 16 KiB limit is a reasonable target: one legal 1,200-pair stroke contains 2,400 `sint32` values, each capped to the signed-16-bit range. Benchmark and test before finalizing the constant.
2. Enforce explicit maximum lengths for correlation ID, raw display name, room code, seat token, and player ID. Sanitizing/truncating a decoded string is not a resource limit.
3. Reject excessive point-array counts in boundary validation before `Room.Submit`.
4. Decode with protobuf unknown-field discard enabled, if supported by the generated/protobuf version in use.
5. Return the existing safe invalid-command response; never echo an unbounded client-provided ID or payload in a log/error.

Protocol limits need documentation and tests. Clients should receive an invalid-command error for violations, not a server panic or a silent resource spike.

### S3. Add a dedicated snapshot request limiter

**Priority:** P0  
**Expected benefit:** blocks authenticated CPU/GC/bandwidth amplification.  
**Files:** `internal/transport/conn.go`, `internal/transport/ratelimit.go`, `internal/room/room.go`, tests.

`RequestSnapshot` currently shares the generic command limiter. A full room snapshot can traverse and encode a large drawing log, so one authenticated connection can request it repeatedly.

Add a per-connection snapshot bucket. Starting policy:

- burst: 2;
- refill: 1 request per second;
- do not block the required initial join/reconnect snapshot;
- use a standard rate-limited error for explicit excess requests.

Keep the client retry policy under this rate. A resync retry should not become a self-inflicted denial of service.

### S4. Share broadcast encoding after measuring

**Priority:** P1  
**Expected benefit:** lower protobuf CPU and allocation for a full room.  
**Files:** `internal/room/api.go`, `internal/transport/conn.go`.

Currently each connection's write pump marshals the same public `ServerEvent` separately. For a room of ten, one stroke broadcast produces ten equivalent encodes.

After C1/S1/S2/S3 benchmarks exist, introduce an immutable shared encoded-frame representation:

- public broadcast frames may be encoded once and referenced by recipient queues;
- recipient-specific replies, snapshots, `Joined`, `YourWord`, and errors must remain individually encoded;
- no mutable protobuf message or private event may enter the shared cache;
- preserve per-connection bounded queues and write pumps;
- do not move blocking encoding work into an unbounded room-actor hot path.

The design must be reviewed for audience typing and secret-canary compliance before merge.

### S5. Deployment headroom and Go memory budget

**Priority:** P1 after load testing  
**Files:** `Makefile`, deployment configuration, deployment documentation.

Cloud Run's request concurrency should leave headroom above the application WebSocket cap for health checks, reconnects, and handshakes. Enforce the application cap inside the registration lock, not only with a pre-upgrade read.

Set `GOMEMLIMIT` below the 2 GiB container limit after measuring the application's real heap and native/socket overhead. Start testing near 1.7 GiB; do not treat that value as final without sustained load data.

Do not change compression policy: current disabled WebSocket compression avoids large per-connection flate state and is a deliberate memory trade.

### S6. Profile-first server follow-ups

Potential but deferred work:

- reduce point-slice copies across stroke admission, broadcast, and commit only with explicit immutable ownership;
- share immutable word-pair/deck data across rooms while retaining per-room draw state;
- rate-limit slow-client drop logs and close persistently slow sessions;
- cache or frame-slice large final-canvas rendering on the client.

Do not introduce `sync.Pool`, delta snapshots, timer wheels, WebSocket compression, or multi-instance room distribution without profiling and a separate correctness design.

## 6. Instrumentation and benchmarks

### 6.1 Client diagnostic mode

Add a development-only, fixed-size ring buffer or once-per-second summary. Do not call `performance.mark()` for every pointer sample in production.

Capture:

- pointer event to `CanvasEngine.extend`;
- rAF timing and `StrokePen.flush` duration;
- remote frame arrival to playback drain;
- resize/replay duration and stroke/point counts;
- backing-store dimensions and estimated bytes;
- store publication counts by event type;
- `longtask` and, where available, long-animation-frame entries.

Use a production build and test:

1. a 15-second local continuous stroke;
2. a remote viewer at 0/80/150 ms RTT and 0/20/50 ms jitter;
3. a dense snapshot plus window resize and mobile rotation;
4. a 10-player room with 4× CPU throttling.

Targets:

- local pointer-to-paint p95 no worse than one display frame;
- no sustained frame above 50 ms while drawing;
- zero UI/store publications for all three stroke event types;
- remote arrival-to-render consistent with the chosen playback target;
- no permanent freeze after a dropped resync snapshot.

### 6.2 Server benchmarks

Add benchmarks and load scenarios before/alongside S2–S5:

- protobuf decode of legal, malformed, and oversized frames;
- stroke begin/points/end for one and ten recipients;
- snapshots at empty, ordinary, and maximum practical canvas sizes;
- snapshot requests at 1, 10, and 40 per second;
- 20 rooms × 10 clients with one active artist per room sending 20 batches/sec;
- reconnect churn, kicked sessions, and deliberately slow readers.

Record command-to-write p50/p95/p99, queue depth/drops, snapshots bytes/sec, allocs/op, live heap, GC CPU/pause, goroutines, connection count, and registry lock contention.

## 7. Validation matrix

Every implementation phase must retain:

```sh
go test ./... -count=1
go test ./... -race
cd web && npm run typecheck && npm run build
```

The working tree currently contains unrelated Kick-feature changes. Do not overwrite or fold those edits into this optimization work. Resolve its existing TypeScript build errors independently before using the web build as a performance baseline.

In addition:

- Preserve secret-canary and broadcast audience tests after S4.
- Add targeted transport boundary tests for every new size/rate limit.
- Add reconnect/kick tests for session closure ordering.
- Manually test mouse, touch, and Pencil input for C2.
- Use browser traces to demonstrate C1 before declaring it complete.

## 8. Explicitly rejected shortcuts

- **“Redraw only the canvas” by bypassing all game events:** wrong. Only stroke events should skip general UI publication; phase, player, vote, and connection events still update UI.
- **Full canvas redraw per rAF:** wrong. Incremental overlay rendering is already the correct hot path.
- **Move all Canvas2D rendering to OffscreenCanvas:** wrong for live artist input latency; retain its export-only use.
- **Send every pointer event:** wrong under current server rate limits and unnecessary for smooth local prediction.
- **Close a displaced socket by immediately cancelling it:** wrong because it can discard the ordered terminal error.
- **Share encoded private events:** forbidden; only proven broadcast-safe public frames can be shared.

## 9. Completion definition

The optimization pass is complete when:

1. C1, C2, C4, S1, S2, and S3 are implemented and validated.
2. A production browser trace demonstrates removal of stroke-driven roster/UI work.
3. A reconnect test demonstrates recovery from a dropped snapshot.
4. A reconnect/kick load test demonstrates old sessions close and connection capacity remains available.
5. C3, C5, S4, and S5 have explicit benchmark evidence supporting implementation, deferral, or revised targets.
6. All functional, race, security/canary, and web build checks pass.

---

## 10. Implementation record

**Date:** 2026-08-27. Machine for every number below: Apple M4 Pro, `darwin/arm64`, `go test -bench -benchmem`. Cloud Run runs 4 vCPU of a different generation, so treat these as ratios between code paths, not as absolute deployed latency.

### 10.1 Implemented

| Item | Where |
| --- | --- |
| C1 suppress no-op store publication | `web/src/state/store.ts` (`apply` returns on referential equality) |
| C2 synchronous first dot | `web/src/canvas/geometry.ts` (`StrokePen.flush`), `web/src/canvas/engine.ts` (`begin`) |
| C3 playback target decoupled | `web/src/canvas/engine.ts` (`REMOTE_PLAYBACK_MS` is now its own constant) — value unchanged, see §10.3 |
| C4 bounded resync retry | `web/src/net/socket.ts`, `web/src/main.ts` (the engine's gap detector now enters through `VersoSocket.resync`) |
| C5 coalesced resize, total surface budget | `web/src/canvas/surface.ts` |
| S1 displaced and kicked sessions close | `internal/room/api.go` (`Session`), `internal/room/reconnect.go`, `internal/transport/conn.go` (`flushAndClose`) |
| S2 inbound bounds | `internal/transport/server.go` (read limit, length constants), `internal/transport/conn.go` (`validate`, `DiscardUnknown`) |
| S3 snapshot limiter | `internal/transport/server.go`, `internal/transport/conn.go` |
| S5 connection cap, deployment headroom | `internal/transport/server.go` (`add` enforces under the lock), `Makefile` |

Tests added: `internal/room/session_test.go`, `internal/transport/session_test.go`, `internal/transport/limits_test.go`. Benchmarks added: `internal/room/bench_test.go`, `internal/transport/bench_test.go`.

`room.Session` replaced `chan<- *genpb.ServerEvent` throughout the room's seat API. The channel is still the queue inside `transport.conn`; what the room now holds is a two-method interface it can also ask to close. Identity comparison — which is what keeps a displaced socket's late `Detach` from unseating its replacement — moved from channel equality to interface equality, so implementations must be pointer types, and the interface says so.

### 10.2 Measured

Decode, the work S2 bounds (`internal/transport`):

```text
DecodeCommand/typical                176 ns/op     232 B/op    4 allocs/op
DecodeCommand/maximum                7.2 µs/op   9,896 B/op    4 allocs/op
DecodeCommand/padded-to-read-limit   7.1 µs/op   9,896 B/op    4 allocs/op
DecodeMalformed                       50 ns/op      80 B/op    1 allocs/op
Validate                             3.0 ns/op       0 B/op    0 allocs/op
```

The largest legal command encodes to **7,276 bytes** (`TestTheReadLimitFitsTheLargestLegalCommand`), so the 16 KiB read limit carries 2.25x headroom and the old 64 KiB carried 9x. The padded case matters most: with `DiscardUnknown` a frame inflated to the read limit costs the same allocation as an unpadded one, so padding buys an attacker scan time only — and the new limit caps that scan at a quarter of what it was. `Validate` at 3 ns and zero allocations means S2's length checks are free relative to the decode they guard.

Broadcast, the question S4 asks (`internal/room`):

```text
StrokePointsBroadcast/1-recipient     125 ns/op    360 B/op    6 allocs/op
StrokePointsBroadcast/10-recipients   199 ns/op    360 B/op    6 allocs/op
MarshalStrokePoints                   164 ns/op     24 B/op    1 allocs/op
```

Snapshot, the cost S3 bounds:

```text
SnapshotBuild/empty        253 ns/op    1,144 B/op   13 allocs/op
SnapshotBuild/ordinary     283 ns/op    1,312 B/op   13 allocs/op
SnapshotBuild/full         393 ns/op    2,288 B/op   13 allocs/op
SnapshotMarshal/empty      833 ns/op      352 B/op    1 allocs/op
SnapshotMarshal/ordinary   5.6 µs/op    2,304 B/op    1 allocs/op
SnapshotMarshal/full        28 µs/op   13,568 B/op    1 allocs/op
```

`full` is the per-turn ceiling: `MaxPointsPerTurn` pairs over `MaxStrokesPerTurn` strokes, 13.5 KB on the wire.

### 10.3 Decisions these numbers support

**S4 — share broadcast encoding: DEFER.** The prediction held qualitatively. Room-side delivery costs 8 ns per extra recipient (one non-blocking channel send), while each recipient's write pump then spends 164 ns marshalling the same bytes — so per stroke batch a room of ten pays ~1.64 µs of redundant encode against ~200 ns of room work, and the redundant half is indeed the larger term. It is also negligible in absolute terms. At the §6.2 load scenario — 20 rooms, 10 clients each, 20 batches a second — the redundant encoding totals **656 µs of CPU per wall-clock second, about 0.07% of one core**. S4 asks for an immutable shared encoded-frame representation, a second audience-typing rule, and a re-review of the secret-canary defense, to recover that. The complexity is not repaid. Revisit only if a profile under real load shows `proto.Marshal` in the write pumps as a material share, which these numbers say it will not be.

**S5 — GOMEMLIMIT: NOT YET SETTLED, deployed as a starting point.** `Makefile` now sets `GOMEMLIMIT=1700MiB` under the 2 GiB container limit, and passes `-max-conns=180` under `--concurrency=200`. The connection-cap half is settled and needs no measurement: the application cap belongs below the platform's, and `Server.add` now enforces it inside the registration lock rather than only at the racy pre-upgrade read. The memory half is explicitly the value S5 nominated for testing and has not been checked against sustained load; the outstanding measurement is live heap and GC CPU under 20 rooms x 10 clients, which is a load-generation exercise this pass did not run. `Makefile` says so at the flag.

**C3 — lower the remote playback target: NOT YET, decoupled.** `REMOTE_PLAYBACK_MS` is now an independent constant with its own contract, so the value can move without touching the wire-side `STROKE_BATCH_MS`. The value stays at 50 ms because the evidence C3 asks for is a browser measurement at 60 Hz and 120 Hz under 0/20/50 ms of jitter, which no server-side benchmark can stand in for. The decoupling is the part that can be done without the display.

**C5 — total surface budget: IMPLEMENTED at 64 MiB, pending device profiling.** This one did not need a benchmark to justify, only arithmetic: the previous per-canvas area cap permitted two 16.7-megapixel backing stores, ~128 MiB of RGBA before compositor copies, on a phone. 64 MiB across both layers allows 8.4 megapixels each, which still covers a full-width pad at DPR 2 on every phone-sized viewport, so the budget binds only on large high-DPR displays and only by fractions of a device pixel. `Surface.metrics()` reports realised dimensions, effective ratio and estimated bytes so a profiling session can state the number instead of inferring it.

### 10.4 Not done

- **§6.1 client diagnostic mode.** No development-only ring buffer or per-second summary was added. `Surface.metrics()` covers the backing-store line item; the rest — pointer-to-`extend` latency, `StrokePen.flush` duration, remote arrival-to-drain, store publication counts by event type — still has to come from Chrome traces by hand. This is the gap that keeps §9 item 2 (a production trace demonstrating C1) from being closeable inside the repository.
- **C6, S6.** Deferred as specified: no allocation-pooling, replay-index or slice-ownership work, and no `sync.Pool`, delta snapshots or timer wheels.
- **Browser-side acceptance checks.** C2's input matrix (mouse, touch, Pencil, cancellation, phase expiry mid-stroke, delayed `StrokeEnded`), C5's rotation and fractional-DPR checks, and the C1 trace are all manual and were not run here.
