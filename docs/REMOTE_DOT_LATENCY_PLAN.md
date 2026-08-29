# Verso — Remote Dot Latency (C7 addendum)

**Status:** implemented (v3), `npm run typecheck` and `npm run build` pass; manual browser QA not yet run
**Date:** 2026-08-29
**Scope:** one client-side rendering gap for single-point ("dot") strokes as seen by everyone except the artist.
**Relationship to the existing plan:** this is an addendum to `docs/PERFORMANCE_OPTIMIZATION_PLAN.md`, in the same category as its `C2` item ("draw the initial local dot synchronously"). `C2` fixed the artist's own dot not appearing until a round trip; this fixes the mirror case — everyone *else's* dot not appearing until a round trip. It is numbered `C7` to slot after the existing `C1`–`C6` without renumbering them.
**Out of scope:** anything the parent plan already gates behind measurement (the `REMOTE_PLAYBACK_MS` smoothing window, remote-stroke curve fitting, allocation pooling) or already rejected (§8 of the parent plan). No wire protocol change, no game rule change.

## 1. Problem

**Symptom:** when the artist places a single tap with no drag — a dot, no line — every other client in the room sees nothing appear on the canvas until the full `StrokeBegan → StrokeEnded` round trip completes. Every other stroke shape does not have this gap, because remote viewers get progressive rendering from `StrokePoints` messages arriving while the artist is still drawing. A dot generates no such messages: `StrokeBegan` carries the one and only point, and `StrokeEnded` closes the stroke immediately after, with nothing in between. So for a dot specifically, the entire network round trip is exposed to a viewer with nothing masking it — unlike a drawn line, where the stroke's own duration hides the latency.

**Root cause, precisely:** `web/src/canvas/geometry.ts:96`

```ts
if (this.provisional && n === 1) {
  if (!this.dotted) {
    this.dot(at(pts, 0), at(pts, 1));
    this.dotted = true;
  }
  return;
}
```

`StrokePen.flush()` only draws the early single-point dot when `provisional === true`. `provisional` is `true` only for the local artist's own overlay pen (`web/src/canvas/engine.ts:314`, inside `begin()`). For a remote stroke, `applyStrokeBegan` constructs the pen with `provisional: false` (`web/src/canvas/engine.ts:428`) and calls `pen.flush(rec.pts)` with the server's one authoritative point (`web/src/canvas/engine.ts:429`) — but with `provisional` false, the guard above never fires, so `flush()` falls through to the incremental curve loop at `web/src/canvas/geometry.ts:104`, which needs `n >= 2` to emit anything, and draws nothing. The dot is not drawn until `applyStrokeEnded` eventually runs `finishLive()` → `live.pen.end(live.rec.pts)`; `end()`'s own handling of `n === 1` (`web/src/canvas/geometry.ts:134`) is unconditional and is the only place a remote dot ever appears.

**Why this is a bug, not a deliberate design choice:** the documented purpose of `provisional` (`web/src/canvas/geometry.ts:63`) is "this is a prediction that might be wrong and gets overdrawn later" — true for the local artist, whose geometry is not yet server-confirmed. But `applyStrokeBegan`'s remote branch is working from `ev.points`, the server's own echoed, already-authoritative event. Nothing is being predicted there. Gating the dot behind `provisional` conflates two different questions — "is this geometry a guess" and "is this the first point of any stroke" — and the remote path inherits a restriction it does not need, purely as a side effect of reusing the same flag.

## 2. Fix — v3, as implemented

**v1 (superseded) was unsafe.** The first version of this plan proposed widening `StrokePen.flush()`'s guard from `if (this.provisional && n === 1)` to `if (n === 1)` and stopping there. An external review (Cursor, `gpt-5.6-sol-high`, see §6) caught a real defect: `dot()` always paints at the stroke's full nominal `width` (`web/src/canvas/geometry.ts:177`), but the first real curve segment can render narrower — the velocity taper ranges from `MIN_MULT = 0.82` to `MAX_MULT = 1.1` and is EMA-smoothed from a starting multiplier of `1.0` (`web/src/canvas/geometry.ts:144`, `EMA = 0.35`). If a remote stroke's early dot is drawn at `StrokeBegan` and the stroke then grows into a fast-started line, the dot can end up very slightly wider than the segment that follows it — and because a finished remote stroke's overlay is composited straight into the base layer with no re-render (`web/src/canvas/engine.ts:588`, `compositeOverlay()`), that mismatch would be permanent rather than something a later redraw corrects. v1's `dotted` guard on `end()` prevented a double-fill for a stroke that *stays* one point, but did nothing for the transition to two-or-more points, which is exactly where the mismatch appears.

**v2 (superseded) fixed the transition but not the timing of it.** v2 proposed: track a `dotOnly` flag, and the instant a remote stroke's point count passed one, clear the overlay and rebuild the pen from the full point list. A second review round (§6, round 2) caught two more defects, both independently confirmed against the code before implementing:

- **A blank flicker.** A fresh `StrokePen`'s bare `flush()` renders nothing until *three* points exist — the midpoint scheme's loop bound is `n - 2` (`web/src/canvas/geometry.ts:104`). v2 would clear the dot as soon as a *second* point arrived, so the stroke would visibly go dot → blank → line, not dot → line. Confirmed by re-reading `flush()` directly.
- **`finishLive()` bypasses the fix entirely.** `finishLive()` calls `flushPlayback()` (`web/src/canvas/engine.ts:770`) before rendering, and `flushPlayback()` only moves queued points into `rec.pts` — it never calls `live.pen.flush()`. So a stroke that ends before the next animation frame processes `drainPlayback()` (a fast flick, easily faster than one frame) reaches `live.pen.end(live.rec.pts)` (`web/src/canvas/engine.ts:610`) on the *original*, still-tainted, dot-only pen, reproducing v1's exact mismatch. Confirmed by tracing `finishLive → flushPlayback → live.pen.end` directly; `flushPlayback` genuinely never touches `live.pen`.

**v3, as implemented, fixes both.** The mechanism is centralized in one helper, `resolveDotOnly(live, minPoints)` (`web/src/canvas/engine.ts`, added next to `drainPlayback`/`flushPlayback`), called from every site that could render a dot-only stroke's geometry, each with the minimum point count that site can safely act on:

1. `Live` gained a `dotOnly: boolean` field (`web/src/canvas/engine.ts:75`). It is set `true` only when a remote live stroke is created with exactly one point (`applyStrokeBegan`'s remote branch), and `false` unconditionally for a local stroke (whose commit path never reuses `live.pen` — see §3).
2. `StrokePen.flush()` (`web/src/canvas/geometry.ts:96`) and `StrokePen.end()` (`web/src/canvas/geometry.ts:139`) both widened from `if (this.provisional && n === 1)` to `if (n === 1)`, each still guarded by the existing `dotted` instance flag so neither call re-fills an already-drawn disc.
3. `resolveDotOnly(live, minPoints)`: if `live.dotOnly` and the stroke has fewer than `minPoints` points, it is a no-op — the dot stays exactly where it is. Otherwise it clears the overlay, replaces `live.pen` with a **fresh** `StrokePen`, and clears `dotOnly`. A fresh pen handed the full accumulated point list on its next call renders exactly what an incrementally-fed pen would have — this reuses the same recreate-pen pattern `redrawAll()` already uses in production for resize (`web/src/canvas/engine.ts:656`), not new geometry logic.
4. `drainPlayback()` calls `resolveDotOnly(live, 3)` right after appending newly-arrived points, *before* its own `live.pen.flush(...)` call — 3, not 2, specifically to avoid the blank-flicker defect above: at 3 points a fresh pen's `flush()` immediately renders a real segment in the same call that clears the dot, so there is no frame where nothing is visible.
5. `finishLive()`'s fast-path branch (the one that composites the overlay directly, `web/src/canvas/engine.ts:607`) calls `resolveDotOnly(live, 2)` immediately before `live.pen.end(...)` — 2 is sufficient here, unlike in `drainPlayback`, because `end()` (unlike bare `flush()`) does render a real final segment for exactly two points. This is the call that closes the gap `finishLive`/`flushPlayback` left open in v2.
6. `redrawAll()` (resize) needed one more adjustment: if `live.dotOnly` is still true and the stroke has fewer than 3 points at resize time, flushing the full accumulated point list to the freshly-recreated pen would render nothing (same underlying cause as defect 1) and silently erase the dot with no replacement. `redrawAll()` now flushes only the first point (`live.rec.pts.slice(0, 2)`) in that case, so the dot redraws correctly at the new scale instead of vanishing.
7. `forceCommitLive()` needed no change — confirmed it always clears the overlay and calls the free `renderStroke()` function with a brand-new pen, never touching `live.pen` at all, so it was never exposed to the tainted-pen defect in the first place.
8. The doc comment on `StrokePen`'s `provisional` constructor parameter (`web/src/canvas/geometry.ts:63`) was updated to stop describing the dot as provisional-only behavior. Only the incremental *tail-to-newest-point* behavior stays exclusive to `provisional`.

**Correction to a v1/v2 claim, confirmed by round 1's review and independently re-checked.** Earlier versions of this plan claimed the mid-turn `tailPen` path (`applyStrokePoints`) would "also now draw a dot... consistent with the same fix applying to another already-authoritative case." This is wrong: `applyStrokePoints` always appends incoming points to `rec.pts` *before* calling `rec.tailPen.flush(rec.pts)`, and `rec.pts` already holds at least one point from the snapshot that baked the open stroke in. `tailPen.flush()` can never actually observe `n === 1`. This fix is inert on that path — neither helping nor risking it — because that path never reaches the code being changed.

## 3. Why this should be safe

- Confined to `web/src/canvas/geometry.ts` and `web/src/canvas/engine.ts`, entirely inside `StrokePen` and the remote-stroke branches of `CanvasEngine`. No wire protocol, no game rule.
- Not a timing/tuning change. Unlike `REMOTE_PLAYBACK_MS` (parent plan, `C3`), there is no "how much delay is acceptable" judgment call requiring device measurement — a stroke's point count is observed exactly, not estimated.
- The geometry drawn is never new or guessed. `applyStrokeBegan`'s remote branch already holds the server-authoritative point, width, and color before this change.
- The "clear and rebuild from the full point list" mechanism is not a new risk surface — it is the exact pattern `redrawAll()` already uses in production for a different trigger (resize). v3 only adds two more triggers (the one-to-N-points transition in `drainPlayback`, and the same check in `finishLive`) for a code path that already exists and is already exercised.
- Confirmed against the code, not just asserted: `forceCommitLive()` and `finishLive()`'s non-fast-path branch both already render through a fresh `renderStroke()` call and were never exposed to this defect; only the one fast-path branch in `finishLive` needed an explicit fix.

## 4. Pushback on round 1's severity framing — still stands, refined with round 2's correction

Round 1's diagnosis was correct, but its framing ("can leave an oversized dot permanently attached," "not safe to implement") read more alarming than the actual magnitude, and this plan says so plainly rather than silently adopting the tone. Round 2 partially agreed and partially corrected the numbers — both are recorded here rather than picking the more convenient one:

- The dot-then-taper sequence being flagged **already ships today**, unmodified, for the local artist's own overlay: `begin()` draws the exact same `dot()` at nominal width, then subsequent points render through the identical taper logic once the stroke grows. This is `C2`, already implemented per `docs/PERFORMANCE_OPTIMIZATION_PLAN.md` §10.1. If the mismatch were visually significant, it would already be visible on every fast-started local stroke in production today.
- **Round 2's correction, accepted:** my original bound (`~6%`) only accounted for the EMA smoothing and skipped the actual bucket quantization. `taper()` returns `Math.round(this.mult * 40) / 40` (`web/src/canvas/geometry.ts:144`, `:155`), and running the EMA-smoothed multipliers through that rounding gives a quantized range of `[0.925, 1.025]` — so the worst-case gap between the dot's full nominal width and a fast first segment's rendered width is `7.5%`, not `~6%`. Round 2's arithmetic was more careful than mine; corrected here.
- Round 2 also pushed back that the local-artist precedent is "weaker than claimed," since local provisional rendering includes extra tail geometry and is only ever transient — it never becomes the permanent record the way a composited remote overlay does. That distinction is fair and is the actual reason v3 fixes the transition properly rather than resting on the precedent argument.
- None of this was ever an argument for shipping v1 or v2's incomplete mechanisms. It was, and remains, an argument for stating the true severity plainly: a real but small (`≤7.5%`), bounded, cosmetic mismatch — worth fixing because the fix is cheap and reuses an established pattern, not because the unfixed magnitude would have been a severe product problem.

## 5. Acceptance checks

- A remote single tap renders a dot immediately on `StrokeBegan`, not delayed until `StrokeEnded`.
- A remote multi-point stroke is visually unchanged at its start point — no double-dot artifact, no visible width seam, and no blank frame at the one-to-two-point transition.
- A stroke that ends abruptly right after gaining its second point — before the next animation frame runs `drainPlayback` — still renders correctly rather than reproducing the v1/v2 mismatch. This is the `finishLive`/`flushPlayback` case round 2 caught; it cannot be verified by typecheck or build, only by a timing-sensitive manual or scripted test (e.g. a very fast two-point flick).
- A stroke resized (window resize, mobile rotation) while still at one or two points redraws its dot correctly afterward, rather than vanishing.
- The local artist's own dot (already instant via the existing `provisional` path) is unaffected.
- The mid-turn resumed-stroke (`tailPen`) path is unaffected, per the corrected claim in §2.
- A stroke that grows from one point to many across several `StrokePoints` batches (not just one) still transitions cleanly.
- `cd web && npm run typecheck && npm run build` passes, per the parent plan's validation matrix (`docs/PERFORMANCE_OPTIMIZATION_PLAN.md` §7). **Done** — both pass clean as of this implementation.
- Manual browser QA (the timing-sensitive cases above) has **not** been run yet.

## 6. Review history

**Round 1 — Cursor (`gpt-5.6-sol-high`), 2026-08-29.** Reviewed v1 against the code, plus an independent subagent audit of `web/src/canvas/`, `web/src/net/`, `web/src/state/store.ts` for regressions in the already-shipped `C1`–`C5`/`S1`–`S3`/`S5` optimizations. Verdict: v1's root-cause diagnosis was accurate; v1's fix was incomplete (the multi-point transition defect) and its `tailPen` safety claim was wrong (corrected in §2). The audit also surfaced defects unrelated to C7 — most notably a missing `!live.ended` guard in `applyStrokeBegan`'s local-adoption check (`web/src/canvas/engine.ts:402`) that can let a stale, already-ended local stroke incorrectly adopt the next artist's unrelated `StrokeBegan`. That finding, plus two reconnect-path claims (client not treating `BAD_SEAT` as terminal; a kick/displacement error silently dropped under a full outbound queue), were independently spot-checked against the actual code and confirmed real, but are out of scope for this canvas-only plan and tracked separately, not folded in here.

**Round 2 — Cursor (`gpt-5.6-sol-high`), 2026-08-29.** Reviewed v2 fresh, asked explicitly to push back on §4 rather than just agree, and to hunt for edge cases across multi-batch growth, abort-mid-transition, and resize-versus-drain races. Verdict: v2 was "no-go as written" — found the blank-flicker defect and the `finishLive`/`flushPlayback` bypass described in §2, corrected the `7.5%` figure in §4, confirmed `forceCommitLive` was already safe, and confirmed the resize interaction needed the `redrawAll()` adjustment in §2 item 6. Both defects were independently re-verified by reading `drainPlayback`, `flushPlayback`, and `finishLive` directly before being accepted into v3 — not taken on the review's word alone.

## 7. Status

**v3 implemented** in `web/src/canvas/geometry.ts` and `web/src/canvas/engine.ts`. `npm run typecheck` and `npm run build` both pass. Manual browser QA for the timing-sensitive cases in §5 has not been run. No third review round has been requested; round 2's findings were independently verified against the code before implementing, which is why this proceeded straight to implementation rather than a round 3.
