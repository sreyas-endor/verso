// engine.ts — the canvas engine the rest of the client talks to.
//
// Two layers, one open stroke, and one rule that shapes the whole file:
// THE ARTIST'S OWN INK NEVER ROUND-TRIPS. It is rendered from local points the
// moment the pointer moves. Viewers get the ~150 ms path (50 ms batch + RTT +
// a frame); the artist gets none of it, because the perceptible threshold for
// direct manipulation is ~2.4 ms and no network reaches it.
//
// The server is still authoritative, so a local stroke is reconciled rather
// than trusted. While the artist draws, the engine also accumulates the
// geometry the server echoes back — post-clamp width, post-cap points — and on
// StrokeEnded it clears the overlay and commits THAT to the base layer. If the
// server clamped the width or cut the stroke off at the per-turn point cap, the
// commit is what the room actually holds, and it matches every viewer exactly.
// A stroke that came from someone else is already authoritative on the overlay,
// so it takes the documented fast path: one drawImage, no re-render.
//
// Divergence recovery: every stroke event carries a room-monotonic seq. A gap
// means a frame was dropped, and the answer is a full RequestSnapshot rather
// than any incremental catch-up machinery (IMPLEMENTATION_PLAN.md §4.6).

import type { Stroke, StrokeBegan, StrokeEnded, StrokePoints } from "../../gen/verso/v1/game_pb.js";
import { renderPng, savePng, type ExportStroke, type SaveOutcome } from "./export.js";
import { StrokePen, renderStroke } from "./geometry.js";
import {
  DEFAULT_WIDTH,
  MAX_POINTS_PER_STROKE,
  MAX_POINTS_PER_TURN,
  STROKE_BATCH_MS,
  clampWidth,
  fromGrid,
  simplify,
  toGrid,
} from "./grid.js";
import { PointerInput, type PointerSink, scaledWidth } from "./input.js";
import { isValidColorIndex } from "./palette.js";
import { Surface } from "./surface.js";

/** What the engine sends. The net layer wraps these in ClientCommand. */
export interface CanvasOutbound {
  /** @param points one interleaved x,y pair on the 4096x3072 signed grid. */
  strokeBegin(colorIndex: number, width: number, points: number[]): void;
  /** @param points a 50 ms batch of interleaved grid pairs. */
  strokePoints(points: number[]): void;
  /** @param points the RDP-simplified whole stroke, or [] to keep what was streamed. */
  strokeEnd(points: number[]): void;
  /**
   * "My canvas is wrong, send me everything."
   *
   * @param haveSeq the last seq applied, advisory only — the answer is always
   *   the complete state, and the implementation is free to supply its own.
   *   The net layer owns retry policy for this one; the engine asks once per
   *   gap and does not follow up (PERFORMANCE_OPTIMIZATION_PLAN.md C4).
   */
  requestSnapshot(haveSeq: number): void;
}

export interface CanvasEngineOptions {
  outbound: CanvasOutbound;
  /** Fired when the committed stroke count changes — enables the export button. */
  onInkChanged?: (strokeCount: number) => void;
  /** Fired when the local pointer starts and stops a stroke. */
  onDrawingChanged?: (drawing: boolean) => void;
}

interface StrokeRec {
  id: number;
  colorIndex: number;
  width: number;
  pts: number[];
  /** Set only for a stroke committed by a snapshot that is still open server-side. */
  tailPen?: StrokePen | undefined;
}

interface Live {
  rec: StrokeRec;
  pen: StrokePen;
  /** True when this is the local artist's own prediction. */
  local: boolean;
  /** True once the server's StrokeBegan echo assigned the real stroke id. */
  adopted: boolean;
  /** Local strokes only: the geometry the server acknowledged. */
  auth: number[];
  /** Local strokes only: the pointer is up, waiting for StrokeEnded. */
  ended: boolean;
  /**
   * Remote strokes only: points that have arrived but are not on screen yet.
   * The frame loop drains this a few points at a time so a batch becomes
   * motion instead of a jump. Always empty for a local stroke, whose ink is
   * drawn from the pointer and never queued.
   */
  queue: number[];
}

/** Id held by a local stroke until the server's echo names it. */
const UNADOPTED = -1;
/** How long a local stroke waits for a StrokeEnded before it is discarded. */
const COMMIT_TIMEOUT_MS = 5_000;
/** Minimum spacing between two RequestSnapshot commands. */
const GAP_COOLDOWN_MS = 1_000;
/**
 * How long a viewer takes to play out the ink it is holding. Points arrive in
 * STROKE_BATCH_MS clumps, and drawing each clump the instant it lands makes a
 * remote stroke advance in visible steps — on a LAN the steps at least arrive
 * on a metronome, but over a real network the jitter between them is what
 * reads as "laggy", far more than the latency itself. Draining over roughly a
 * batch's worth of frames converts that into continuous motion, at the cost of
 * being one batch behind. That trade is only ever made for someone else's
 * stroke; see the file header on why the artist's own ink is never delayed.
 *
 * This is a PLAYBACK target and STROKE_BATCH_MS is a WIRE contract; they are
 * deliberately not the same constant even though they currently hold the same
 * number. Lowering this one buys a viewer 20-40 ms and costs nothing on the
 * network, but it trades against how continuous a batch looks when it is
 * spread over fewer frames, so it is a measurement (60 Hz and 120 Hz, at 0/20
 * /50 ms of jitter) rather than a guess — PERFORMANCE_OPTIMIZATION_PLAN.md C3.
 * Lowering STROKE_BATCH_MS instead is a different decision entirely and has to
 * be argued against transport.DefaultCommandRate.
 */
const REMOTE_PLAYBACK_MS = 50;
/**
 * Backlog past which the buffer is emptied in one frame. Smoothing is worth a
 * batch of delay and nothing more: after a stall the viewer should catch up,
 * not replay the stall in slow motion.
 */
const MAX_PLAYBACK_LAG_PAIRS = 120;

function appendAll(dst: number[], src: readonly number[]): void {
  for (let i = 0; i < src.length; i++) dst.push(src[i] as number);
}

export class CanvasEngine implements PointerSink {
  private readonly surface = new Surface();
  private readonly input: PointerInput;
  private readonly outbound: CanvasOutbound;
  private readonly onInkChanged: ((n: number) => void) | undefined;
  private readonly onDrawingChanged: ((drawing: boolean) => void) | undefined;

  private log: StrokeRec[] = [];
  private byId = new Map<number, StrokeRec>();
  private live: Live | null = null;

  private lastSeq = 0;
  private gapPending = false;

  private colorIndex = 0;
  private width = DEFAULT_WIDTH;
  private enabled = false;
  private pointsThisTurn = 0;
  /**
   * Per-turn stroke ceiling from the match's pen rule (ONE_LINE = 1,
   * MAX_FIVE = 5) and how much of it this turn has spent.
   *
   * Infinity, not the server's FREE ceiling of 128: the room enforces that one
   * as anti-abuse, and mirroring a number the point budget above reaches first
   * would only give the client a second cap to keep in sync. The server is the
   * authority either way — this gate exists so the artist is not laying down
   * ink that the room will silently drop.
   */
  private strokeLimit = Number.POSITIVE_INFINITY;
  private strokesThisTurn = 0;

  private pending: number[] = [];
  private batchTimer: number | null = null;
  private commitTimer: number | null = null;
  private raf: number | null = null;
  private dirty = false;
  private mounted = false;

  constructor(options: CanvasEngineOptions) {
    this.outbound = options.outbound;
    this.onInkChanged = options.onInkChanged;
    this.onDrawingChanged = options.onDrawingChanged;
    this.input = new PointerInput(this.surface, this);
  }

  // -------------------------------------------------------------------------
  // Lifecycle
  // -------------------------------------------------------------------------

  mount(container: HTMLElement): void {
    if (this.mounted) return;
    this.mounted = true;
    this.surface.mount(container, () => this.redrawAll());
    this.input.attach();
  }

  unmount(): void {
    if (!this.mounted) return;
    this.mounted = false;
    this.input.detach();
    this.stopRaf();
    this.stopBatch();
    this.clearCommitTimer();
    this.surface.unmount();
  }

  /** The element to place in the layout. Sizes itself to its container. */
  get element(): HTMLElement {
    return this.surface.stage;
  }

  /** The letterboxed paper itself, for the UI to anchor overlays to. */
  get padElement(): HTMLElement {
    return this.surface.pad;
  }

  get strokeCount(): number {
    return this.log.length;
  }

  // -------------------------------------------------------------------------
  // Brush and gating
  // -------------------------------------------------------------------------

  setColorIndex(index: number): void {
    if (isValidColorIndex(index)) this.colorIndex = index;
  }

  getColorIndex(): number {
    return this.colorIndex;
  }

  setWidth(width: number): void {
    this.width = clampWidth(width);
  }

  getWidth(): number {
    return this.width;
  }

  /**
   * Only the current artist draws. Enabling starts a fresh per-turn point and
   * stroke budget; disabling closes any stroke still under the pointer, which
   * is what makes a turn that expires mid-stroke end cleanly on both sides.
   */
  setDrawingEnabled(enabled: boolean): void {
    if (enabled && !this.enabled) {
      this.pointsThisTurn = 0;
      this.strokesThisTurn = 0;
    }
    this.enabled = enabled;
    this.surface.setDrawingAffordance(enabled);
    this.input.setEnabled(enabled);
  }

  get drawingEnabled(): boolean {
    return this.enabled;
  }

  /**
   * The pen rule's per-turn stroke ceiling. A courtesy gate, not enforcement:
   * internal/room/strokes.go drops a StrokeBegin past its own ceiling, so a
   * client that never called this still cannot draw more than the room allows.
   * Anything that is not a finite count means unlimited.
   */
  setStrokeLimit(limit: number): void {
    this.strokeLimit = Number.isFinite(limit) ? Math.max(0, Math.floor(limit)) : Number.POSITIVE_INFINITY;
  }

  /**
   * The stroke budget as the UI draws it. `used` counts strokes started this
   * turn — including the one under the pointer, which `penDown` separates out
   * so the gauge can show it as in progress rather than already spent.
   */
  strokeBudget(): { limit: number; used: number; penDown: boolean } {
    const live = this.live;
    return {
      limit: this.strokeLimit,
      used: this.strokesThisTurn,
      penDown: live !== null && live.local && !live.ended,
    };
  }

  // -------------------------------------------------------------------------
  // PointerSink — the local artist's path. Nothing here waits on the network.
  // -------------------------------------------------------------------------

  begin(x: number, y: number, widthScale: number): void {
    if (!this.enabled) return;
    if (this.pointsThisTurn >= MAX_POINTS_PER_TURN) return;
    // Out of strokes locks the pen for the rest of the turn; it never ends the
    // turn early, so the clock simply runs out with the canvas as it stands.
    if (this.strokesThisTurn >= this.strokeLimit) return;
    this.forceCommitLive();

    const width = scaledWidth(this.width, widthScale);
    const rec: StrokeRec = { id: UNADOPTED, colorIndex: this.colorIndex, width, pts: [x, y] };
    const live: Live = {
      rec,
      pen: new StrokePen(this.surface.overlayCtx, rec.colorIndex, rec.width, true),
      local: true,
      adopted: false,
      auth: [],
      ended: false,
      queue: [],
    };
    this.live = live;
    this.pointsThisTurn++;
    this.strokesThisTurn++;

    // Synchronously, before the socket and before the frame loop. The rAF path
    // only paints on a `dirty` flag that the first point does not set, so
    // without this a tap has nothing on screen until StrokeEnded comes back
    // over the network — the one place the artist's ink was still round
    // -tripping. Provisional, so it lands on the overlay only; the base layer
    // keeps holding committed server geometry and nothing else.
    live.pen.flush(rec.pts);

    this.outbound.strokeBegin(rec.colorIndex, width, toGrid(rec.pts));
    this.startBatch();
    this.startRaf();
    this.onDrawingChanged?.(true);
  }

  extend(points: number[]): void {
    const live = this.live;
    if (!live || !live.local || live.ended) return;

    const accepted: number[] = [];
    for (let i = 0; i + 1 < points.length; i += 2) {
      if (live.rec.pts.length >= MAX_POINTS_PER_STROKE * 2) break;
      if (this.pointsThisTurn >= MAX_POINTS_PER_TURN) break;
      const x = points[i] as number;
      const y = points[i + 1] as number;
      live.rec.pts.push(x, y);
      accepted.push(x, y);
      this.pointsThisTurn++;
    }
    if (accepted.length === 0) return;

    appendAll(this.pending, toGrid(accepted));
    this.dirty = true;
  }

  end(): void {
    const live = this.live;
    if (!live || !live.local || live.ended) return;
    live.ended = true;

    // Draw whatever the last frame did not, then stop the frame loop: the
    // geometry cannot change again.
    live.pen.flush(live.rec.pts);
    this.dirty = false;
    this.stopRaf();

    this.flushBatch();
    this.stopBatch();

    // The one place RDP runs. Never in the hot path, and never sent when it
    // failed to remove anything — the server rejects a replacement longer than
    // what it already holds, so a no-op costs bytes for nothing.
    const simplified = simplify(live.rec.pts);
    const shrank = simplified.length < live.rec.pts.length;
    this.outbound.strokeEnd(shrank ? toGrid(simplified) : []);

    this.clearCommitTimer();
    this.commitTimer = window.setTimeout(() => {
      this.commitTimer = null;
      // No StrokeEnded came back, so the server never took this stroke: drop
      // the prediction rather than leaving ink nobody else can see.
      if (this.live?.local && this.live.ended) {
        this.surface.clearOverlay();
        this.live = null;
      }
    }, COMMIT_TIMEOUT_MS);

    this.onDrawingChanged?.(false);
  }

  // -------------------------------------------------------------------------
  // Server events
  // -------------------------------------------------------------------------

  applyStrokeBegan(ev: StrokeBegan): void {
    if (!this.acceptSeq(ev.seq)) return;
    const live = this.live;

    if (live && live.local && !live.adopted) {
      live.adopted = true;
      live.rec.id = ev.strokeId;
      live.auth = fromGrid(ev.points);
      // The server clamps width and validates the colour index. It should agree
      // with what was sent, because the same bounds are applied locally — but if
      // it does not, the server wins and the overlay is re-cut to match.
      if (ev.width !== live.rec.width || ev.colorIndex !== live.rec.colorIndex) {
        live.rec.width = ev.width;
        live.rec.colorIndex = ev.colorIndex;
        this.surface.clearOverlay();
        live.pen = new StrokePen(this.surface.overlayCtx, ev.colorIndex, ev.width, true);
        live.pen.flush(live.rec.pts);
      }
      return;
    }

    // Someone else's stroke. There is exactly one artist, so anything still
    // open belongs to a turn that has already moved on.
    this.forceCommitLive();
    const rec: StrokeRec = {
      id: ev.strokeId,
      colorIndex: ev.colorIndex,
      width: ev.width,
      pts: fromGrid(ev.points),
    };
    const pen = new StrokePen(this.surface.overlayCtx, rec.colorIndex, rec.width, false);
    pen.flush(rec.pts);
    this.live = { rec, pen, local: false, adopted: true, auth: [], ended: false, queue: [] };
    // A remote stroke needs the frame loop too, to drain its playback buffer.
    // The local path starts it in begin().
    this.startRaf();
  }

  applyStrokePoints(ev: StrokePoints): void {
    if (!this.acceptSeq(ev.seq)) return;
    const live = this.live;

    if (live && live.adopted && live.rec.id === ev.strokeId) {
      if (live.local) {
        // Already on screen. Record what the server took so the commit can be
        // reconciled against it.
        appendAll(live.auth, fromGrid(ev.points));
        return;
      }
      // Queued, not drawn: the frame loop pays this out over the next few
      // frames so the stroke moves instead of stepping.
      appendAll(live.queue, fromGrid(ev.points));
      return;
    }

    // A Snapshot bakes the currently open stroke into the stroke log, so a
    // client that reconnected mid-turn keeps receiving points for a stroke it
    // already treats as committed. Append them to the base layer.
    const rec = this.byId.get(ev.strokeId);
    if (!rec) return;
    if (!rec.tailPen) {
      rec.tailPen = new StrokePen(this.surface.baseCtx, rec.colorIndex, rec.width, false);
    }
    appendAll(rec.pts, fromGrid(ev.points));
    rec.tailPen.flush(rec.pts);
  }

  applyStrokeEnded(ev: StrokeEnded): void {
    if (!this.acceptSeq(ev.seq)) return;
    const live = this.live;

    const isOurs =
      (live?.adopted === true && live.rec.id === ev.strokeId) ||
      // Our own stroke whose StrokeBegin the server never acknowledged: there
      // is nothing else it could be closing.
      (live?.local === true && !live.adopted && live.ended);
    if (live && isOurs) {
      this.finishLive(ev.points);
      return;
    }

    const rec = this.byId.get(ev.strokeId);
    if (!rec) return;
    if (ev.points.length > 0) {
      // A replacement is not an append: the old ink is already on the base
      // layer and there is no way to subtract it, so the canvas is rebuilt.
      rec.pts = fromGrid(ev.points);
      rec.tailPen = undefined;
      this.redrawAll();
      return;
    }
    if (rec.tailPen) {
      rec.tailPen.end(rec.pts);
      rec.tailPen = undefined;
    }
  }

  /** Snapshot: the whole stroke log in one message, replayed without flicker. */
  replay(strokes: readonly Stroke[], seq: number): void {
    // A snapshot supersedes anything in flight, but the server still holds our
    // open stroke, so close it properly instead of abandoning it.
    this.input.abort();
    this.stopRaf();
    this.stopBatch();
    this.clearCommitTimer();
    this.pending = [];
    this.live = null;
    this.dirty = false;

    this.log = strokes.map((s) => ({
      id: s.strokeId,
      colorIndex: s.colorIndex,
      width: s.width,
      pts: fromGrid(s.points),
    }));
    this.byId = new Map(this.log.map((r) => [r.id, r]));
    this.lastSeq = seq;
    this.redrawAll();
    this.onInkChanged?.(this.log.length);
  }

  /**
   * Clear the paper for a new match. `seq` is deliberately left alone: the room
   * keeps it monotonic for its whole life, so a rematch is not a gap and must
   * not be mistaken for one (internal/room/phase.go:245).
   */
  reset(): void {
    this.input.abort();
    this.stopRaf();
    this.stopBatch();
    this.clearCommitTimer();
    this.pending = [];
    this.live = null;
    this.dirty = false;
    this.log = [];
    this.byId.clear();
    this.pointsThisTurn = 0;
    this.strokesThisTurn = 0;
    this.surface.clearBase();
    this.surface.clearOverlay();
    this.onInkChanged?.(0);
  }

  // -------------------------------------------------------------------------
  // Export
  // -------------------------------------------------------------------------

  /**
   * A detached copy of the committed vectors — everything on the canvas right
   * now, minus any stroke still in flight.
   *
   * This is how a finished round survives the wipe that opens the next one. The
   * points are copied, not aliased: reset() clears the log, and an archived
   * round has to outlive it.
   */
  committedStrokes(): ExportStroke[] {
    return this.log.map((r) => ({
      colorIndex: r.colorIndex,
      width: r.width,
      points: [...r.pts],
    }));
  }

  /** Re-render the committed vectors at 2x and encode a PNG off the main thread. */
  exportPng(scale = 2): Promise<Blob> {
    return renderPng(this.committedStrokes(), scale);
  }

  /** Export, then hand it to the share sheet or a download. */
  async downloadPng(baseName = "verso-canvas"): Promise<SaveOutcome> {
    return savePng(await this.exportPng(), baseName);
  }

  // -------------------------------------------------------------------------
  // Internals
  // -------------------------------------------------------------------------

  private finishLive(gridPoints: readonly number[]): void {
    const live = this.live;
    if (!live) return;
    this.clearCommitTimer();
    this.flushPlayback(live);
    this.stopRaf();
    this.dirty = false;

    if (gridPoints.length === 0 && !live.local) {
      // The streamed points stand and the overlay already holds exactly them:
      // one bitmap copy, no geometry re-run.
      live.pen.end(live.rec.pts);
      this.surface.compositeOverlay();
      this.commitRec(live.rec);
      this.live = null;
      return;
    }

    const final = gridPoints.length > 0 ? fromGrid(gridPoints) : live.auth;
    this.surface.clearOverlay();
    live.rec.pts = final;
    if (final.length > 0) {
      renderStroke(this.surface.baseCtx, final, live.rec.colorIndex, live.rec.width);
      this.commitRec(live.rec);
    }
    this.live = null;
  }

  /** Close whatever is live without a StrokeEnded. Defensive; normally a no-op. */
  private forceCommitLive(): void {
    const live = this.live;
    if (!live) return;
    this.flushPlayback(live);
    this.stopRaf();
    this.dirty = false;
    this.surface.clearOverlay();
    const final = live.local ? live.auth : live.rec.pts;
    if (final.length > 0 && live.adopted) {
      live.rec.pts = final;
      renderStroke(this.surface.baseCtx, final, live.rec.colorIndex, live.rec.width);
      this.commitRec(live.rec);
    }
    this.live = null;
  }

  private commitRec(rec: StrokeRec): void {
    if (rec.id < 0 || this.byId.has(rec.id)) return;
    this.log.push(rec);
    this.byId.set(rec.id, rec);
    this.onInkChanged?.(this.log.length);
  }

  private redrawAll(): void {
    this.surface.clearBase();
    this.surface.clearOverlay();
    for (const rec of this.log) {
      rec.tailPen = undefined;
      renderStroke(this.surface.baseCtx, rec.pts, rec.colorIndex, rec.width);
    }
    const live = this.live;
    if (live) {
      live.pen = new StrokePen(this.surface.overlayCtx, live.rec.colorIndex, live.rec.width, live.local);
      live.pen.flush(live.rec.pts);
    }
  }

  /**
   * The room's seq is strictly monotonic for the room's whole life, so it is
   * both the gap detector and the duplicate filter. The duplicate half matters:
   * a Snapshot bakes the currently open stroke into the log, and any stroke
   * event still in flight when it was built would otherwise be applied twice.
   *
   * A gap is not recoverable incrementally by design — the answer is one full
   * RequestSnapshot (IMPLEMENTATION_PLAN.md §4.6). The event is still applied,
   * because a slightly wrong canvas beats a frozen one for the ~1 RTT until the
   * snapshot lands.
   */
  private acceptSeq(seq: number): boolean {
    if (this.lastSeq > 0) {
      if (seq <= this.lastSeq) return false;
      if (seq > this.lastSeq + 1) this.requestSnapshot();
    }
    this.lastSeq = seq;
    return true;
  }

  private requestSnapshot(): void {
    if (this.gapPending) return;
    this.gapPending = true;
    this.outbound.requestSnapshot(this.lastSeq);
    window.setTimeout(() => {
      this.gapPending = false;
    }, GAP_COOLDOWN_MS);
  }

  private startBatch(): void {
    if (this.batchTimer !== null) return;
    this.batchTimer = window.setInterval(() => this.flushBatch(), STROKE_BATCH_MS);
  }

  private flushBatch(): void {
    if (this.pending.length === 0) return;
    const points = this.pending;
    this.pending = [];
    this.outbound.strokePoints(points);
  }

  private stopBatch(): void {
    if (this.batchTimer === null) return;
    window.clearInterval(this.batchTimer);
    this.batchTimer = null;
  }

  private startRaf(): void {
    if (this.raf !== null) return;
    let last = 0;
    const tick = (now: number): void => {
      this.raf = window.requestAnimationFrame(tick);
      const live = this.live;
      if (!live) return;

      if (live.local) {
        if (!this.dirty) return;
        this.dirty = false;
        live.pen.flush(live.rec.pts);
        return;
      }

      // Clamped because a backgrounded tab resumes with an enormous delta, and
      // that must not be read as "the buffer is hours behind".
      const dt = last === 0 ? 16 : Math.min(now - last, 100);
      last = now;
      this.drainPlayback(live, dt);
    };
    this.raf = window.requestAnimationFrame(tick);
  }

  /**
   * Move part of a remote stroke's buffer onto the canvas. The share taken is
   * proportional to how much is waiting, which makes it self-correcting: a
   * jitter burst leaves a deeper buffer and so drains faster, and the steady
   * state settles at whatever rate the sender is actually drawing.
   */
  private drainPlayback(live: Live, dt: number): void {
    const queued = live.queue.length >> 1;
    if (queued === 0) return;

    const pairs =
      queued > MAX_PLAYBACK_LAG_PAIRS
        ? queued
        : Math.min(queued, Math.max(1, Math.ceil((queued * dt) / REMOTE_PLAYBACK_MS)));

    appendAll(live.rec.pts, live.queue.splice(0, pairs * 2));
    live.pen.flush(live.rec.pts);
  }

  /**
   * Put every buffered point on the canvas at once. Called when a stroke is
   * closing: smoothing the tail of a stroke that is already over would just be
   * lag, and dropping it would lose ink the server has committed.
   */
  private flushPlayback(live: Live): void {
    if (live.queue.length === 0) return;
    appendAll(live.rec.pts, live.queue);
    live.queue.length = 0;
  }

  private stopRaf(): void {
    if (this.raf === null) return;
    window.cancelAnimationFrame(this.raf);
    this.raf = null;
  }

  private clearCommitTimer(): void {
    if (this.commitTimer === null) return;
    window.clearTimeout(this.commitTimer);
    this.commitTimer = null;
  }
}
