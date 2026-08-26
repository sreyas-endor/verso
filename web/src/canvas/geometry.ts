// geometry.ts — midpoint-quadratic smoothing, drawn incrementally.
//
// Extending a stroke costs one quadraticCurveTo per new sample. The polyline is
// never re-resolved, which is what makes per-frame work proportional to the
// points that arrived this frame rather than to everything already on the
// canvas (IMPLEMENTATION_PLAN.md §4.7).
//
// Width is velocity-derived, never pressure-derived (§7): mouse-down reports
// exactly 0.5 and iOS non-Pencil reports a hard 0, so pressure carries no
// signal. "Velocity" here is measured as the spacing between consecutive
// midpoints, which is a pure function of the geometry — so the artist, every
// viewer, and the PNG export all compute the same widths from the same points
// without a single extra byte on the wire. The wire carries one nominal width
// per stroke and the taper modulates it inside a narrow band.
//
// The taper is smoothed with an EMA and quantised, so consecutive segments that
// land in the same bucket accumulate into one path and cost one stroke() call.

import { paletteCss } from "./palette.js";

type AnyCtx = CanvasRenderingContext2D | OffscreenCanvasRenderingContext2D;

/** Spacing, in logical units, at which the taper reaches its thick end. */
const SLOW_SPACING = 1.2;
/** Spacing at which it reaches its thin end. */
const FAST_SPACING = 10;
const MAX_MULT = 1.1;
const MIN_MULT = 0.82;
const EMA = 0.35;
/** Quantisation steps per unit of multiplier. */
const BUCKETS = 40;

function at(a: readonly number[], i: number): number {
  return a[i] as number;
}

/**
 * An incremental renderer for exactly one stroke.
 *
 * `flush` may be called any number of times as points arrive; each call emits
 * only the geometry that became resolvable since the last one. `end` closes the
 * stroke off at its final point.
 */
export class StrokePen {
  private readonly ctx: AnyCtx;
  private readonly css: string;
  private readonly base: number;
  private readonly provisional: boolean;

  /** Points whose curve segment has already been emitted. */
  private consumed = 0;
  /** Path cursor: the midpoint the last segment ended on. */
  private cx = 0;
  private cy = 0;
  private mult = 1;
  private bucket = -1;
  private open = false;
  private started = false;

  /**
   * @param provisional draw a straight tail to the newest point on every flush.
   *   The midpoint scheme otherwise lags one sample, which is invisible for a
   *   viewer receiving 50 ms batches but not for the artist's own hand. The tail
   *   is overdrawn by real geometry on the next flush, so it is only ever
   *   enabled on the overlay layer, which is discarded rather than composited.
   */
  constructor(ctx: AnyCtx, colorIndex: number, width: number, provisional = false) {
    this.ctx = ctx;
    this.css = paletteCss(colorIndex);
    this.base = width;
    this.provisional = provisional;
  }

  /** Emit every segment that `pts` (flat, logical) now makes resolvable. */
  flush(pts: readonly number[]): void {
    const n = pts.length >> 1;
    if (n === 0) return;

    if (!this.started) {
      this.cx = at(pts, 0);
      this.cy = at(pts, 1);
      this.started = true;
      this.consumed = 1;
    }

    let i = this.consumed;
    for (; i <= n - 2; i++) {
      const px = at(pts, i * 2);
      const py = at(pts, i * 2 + 1);
      const mx = (px + at(pts, i * 2 + 2)) / 2;
      const my = (py + at(pts, i * 2 + 3)) / 2;

      const b = this.taper(Math.hypot(mx - this.cx, my - this.cy));
      if (!this.open || b !== this.bucket) {
        this.strokePath();
        this.openPath(b);
      }
      this.ctx.quadraticCurveTo(px, py, mx, my);
      this.cx = mx;
      this.cy = my;
    }
    this.consumed = i;

    if (this.provisional && n >= 2 && this.consumed === n - 1) {
      this.strokePath();
      this.openPath(this.bucket < 0 ? this.taper(0) : this.bucket);
      this.ctx.lineTo(at(pts, n * 2 - 2), at(pts, n * 2 - 1));
      this.strokePath();
    }
  }

  /** Close the stroke at its last point. Idempotent geometry, safe to call once. */
  end(pts: readonly number[]): void {
    const n = pts.length >> 1;
    if (n === 0) return;
    if (n === 1) {
      this.dot(at(pts, 0), at(pts, 1));
      return;
    }
    this.flush(pts);
    if (!this.open) this.openPath(this.bucket < 0 ? this.taper(0) : this.bucket);
    this.ctx.lineTo(at(pts, n * 2 - 2), at(pts, n * 2 - 1));
    this.strokePath();
  }

  private taper(spacing: number): number {
    let raw: number;
    if (spacing <= SLOW_SPACING) {
      raw = MAX_MULT;
    } else if (spacing >= FAST_SPACING) {
      raw = MIN_MULT;
    } else {
      const t = (spacing - SLOW_SPACING) / (FAST_SPACING - SLOW_SPACING);
      raw = MAX_MULT + t * (MIN_MULT - MAX_MULT);
    }
    this.mult += (raw - this.mult) * EMA;
    return Math.round(this.mult * BUCKETS);
  }

  private openPath(bucket: number): void {
    this.ctx.beginPath();
    this.ctx.moveTo(this.cx, this.cy);
    this.bucket = bucket;
    this.open = true;
  }

  private strokePath(): void {
    if (!this.open) return;
    this.ctx.lineCap = "round";
    this.ctx.lineJoin = "round";
    this.ctx.strokeStyle = this.css;
    this.ctx.lineWidth = Math.max(0.1, (this.base * this.bucket) / BUCKETS);
    this.ctx.stroke();
    this.open = false;
  }

  private dot(x: number, y: number): void {
    this.ctx.beginPath();
    this.ctx.arc(x, y, Math.max(0.05, this.base / 2), 0, Math.PI * 2);
    this.ctx.fillStyle = this.css;
    this.ctx.fill();
  }
}

/** Draw a complete stroke in one pass. Used for commits, replays and export. */
export function renderStroke(
  ctx: AnyCtx,
  points: readonly number[],
  colorIndex: number,
  width: number,
): void {
  new StrokePen(ctx, colorIndex, width, false).end(points);
}
