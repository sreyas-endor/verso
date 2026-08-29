// surface.ts — the two stacked Canvas2D layers, their DOM, and their sizing.
//
//   base     committed strokes. Written once per stroke end. Never cleared
//            except on a full redraw (resize, snapshot replay, new match).
//   overlay  the single in-progress stroke. There is exactly one artist at a
//            time, so there is at most one open stroke room-wide.
//
// Sizing is a fixed 1024x768 logical space, CSS-letterboxed by `aspect-ratio`
// inside a centring grid. That is `object-fit: contain` spelled in layout, and
// it means getBoundingClientRect().left/top *is* the letterbox offset — there is
// no second offset variable to get wrong (IMPLEMENTATION_PLAN.md §4.7).
//
// Every DPR rule in §7 is load-bearing here:
//   - setTransform, never scale: scale() compounds on the no-op path.
//   - getBoundingClientRect(), never clientWidth: the latter rounds to an int
//     and loses the true device-pixel edge.
//   - devicePixelContentBoxSize does not exist in Safari, so dpr x rect is the
//     primary path, not a fallback.
//   - there is no devicepixelratiochange event; a (resolution: Xdppx) media
//     query is rebuilt each time it stops matching.
//   - the size cap is on AREA, not on either dimension. Exceeding it fails
//     silently in Chrome and Safari: blank canvas, no throw, no console message.
//   - the AREA cap is per canvas, and this surface has two of them. The budget
//     that actually matters is the pair's, so MAX_SURFACE_BYTES is what sets
//     the effective DPR; the browser's own devicePixelRatio is an input to that
//     decision, not the answer to it.

import { LOGICAL_H, LOGICAL_W } from "./grid.js";

/** 4096^2 device pixels. Safe on every current target including pre-18 iOS. */
const MAX_CANVAS_AREA = 16_777_216;
const MAX_CANVAS_DIM = 8192;

/** Layers whose backing stores are live at once: base and overlay. */
const LAYER_COUNT = 2;
/** RGBA. Browsers may keep a second copy for the compositor; this is the floor. */
const BYTES_PER_PIXEL = 4;
/**
 * Raw bitmap budget for BOTH layers together.
 *
 * The per-canvas area cap alone permits two 16.7-megapixel backing stores, or
 * ~128 MiB of RGBA before the compositor and GPU copies that sit behind them —
 * on a phone that is the whole tab budget spent on a canvas nobody asked to be
 * that sharp. 64 MiB is the mobile-safe starting point: at 4 bytes a pixel over
 * two layers it allows 8.4 megapixels each, which still covers a 4K-wide pad at
 * DPR 1 and a full-width pad at DPR 2 on every phone-sized viewport. Sharpness
 * is given up only past that, and only by fractions of a device pixel.
 */
const MAX_SURFACE_BYTES = 64 << 20;
/** The per-layer pixel ceiling the byte budget implies. */
const MAX_LAYER_PIXELS = MAX_SURFACE_BYTES / (LAYER_COUNT * BYTES_PER_PIXEL);

/** What the surface currently costs. Diagnostics only; safe to call anywhere. */
export interface SurfaceMetrics {
  /** Backing-store width in device pixels, per layer. */
  readonly width: number;
  /** Backing-store height in device pixels, per layer. */
  readonly height: number;
  /** The browser's reported ratio. */
  readonly devicePixelRatio: number;
  /** The ratio actually used, after the byte budget. Lower means downscaled. */
  readonly effectiveRatio: number;
  /** Estimated raw bytes for both layers. Excludes compositor copies. */
  readonly bytes: number;
}

const STYLE_ID = "verso-canvas-style";

// The paper is the one surface in the app that never takes the page tint: it is
// white in both themes, and the PNG export composites onto the same white.
export const PAPER = "#ffffff";

// The pad's own hairline is an inset shadow rather than a border: a border
// takes layout space, which would make the pad's aspect ratio unachievable
// inside a stage that is already exactly 4:3 and force `max-height` to clamp
// it. The rule is decorative and mostly hidden under the stage's ink frame.
//
// `touch-action` defaults to `pan-y` and only becomes `none` while this client
// may actually put ink down (`data-drawing`, written by setDrawingAffordance).
// For everyone who is not the artist — most players, most of the match — the
// canvas is a band across the middle of a phone that used to swallow every
// vertical swipe.
const PAD_RULE = "inset 0 0 0 2px var(--border-str,#c6cee0)";
const CSS = `
.verso-stage{position:relative;display:grid;place-items:center;width:100%;height:100%;min-width:0;min-height:0;}
.verso-pad{position:relative;width:100%;max-width:100%;max-height:100%;aspect-ratio:${LOGICAL_W}/${LOGICAL_H};
  background:${PAPER};box-shadow:${PAD_RULE};border-radius:var(--radius,14px);
  overflow:hidden;touch-action:pan-y;-webkit-user-select:none;user-select:none;-webkit-touch-callout:none;}
.verso-pad canvas{position:absolute;inset:0;width:100%;height:100%;display:block;touch-action:pan-y;}
.verso-pad[data-drawing="true"],
.verso-pad[data-drawing="true"] canvas{touch-action:none;}
.verso-pad[data-drawing="true"]{cursor:var(--verso-pen-cursor,crosshair);box-shadow:${PAD_RULE},0 0 0 3px var(--accent-sf,#e8eeff);}
.verso-pad:focus-visible{outline:2px solid var(--accent,#4f7cff);outline-offset:2px;}
@media (prefers-reduced-motion: reduce){.verso-pad{transition:none;}}
`;

export function toLogicalX(rect: DOMRect, clientX: number): number {
  if (rect.width <= 0) return 0;
  return ((clientX - rect.left) / rect.width) * LOGICAL_W;
}

export function toLogicalY(rect: DOMRect, clientY: number): number {
  if (rect.height <= 0) return 0;
  return ((clientY - rect.top) / rect.height) * LOGICAL_H;
}

function installStyle(): void {
  if (document.getElementById(STYLE_ID)) return;
  const el = document.createElement("style");
  el.id = STYLE_ID;
  el.textContent = CSS;
  document.head.appendChild(el);
}

function context(canvas: HTMLCanvasElement): CanvasRenderingContext2D {
  const ctx = canvas.getContext("2d", { desynchronized: true });
  if (!ctx) throw new Error("verso: 2D canvas context unavailable");
  return ctx;
}

// The pen cursor is drawn here rather than delegated to `cursor: crosshair`.
//
// The OS crosshair is not a safe default over this surface. What it looks like
// is the platform's and the user's choice, not ours: macOS draws a near-white
// cross with a hairline core, and Windows' Precision Select follows the pointer
// scheme, which is white-filled by default. PAPER is #ffffff in both themes, so
// on both a white cursor camouflages and the artist draws blind — observed on a
// Windows laptop and reproduced on macOS. Picking the other extreme does not
// help either: a solid black cross disappears into a #14161f stroke. A cursor
// legible on white paper AND on dark ink has to carry both tones itself, which
// is something no system cursor and no accessibility scheme will guarantee.
//
// Owning the image also lets it state the nib: the ring is the actual stroke
// diameter, so the weight of the line is visible before it is committed. That
// matters most under a pen rule, where a stroke cannot be taken back.
//
// This is a CSS cursor, not a mark on either bitmap. Nothing is composited over
// the paper — the canvas rect carries strokes and nothing else, or a decoration
// reads as ink — and the PNG export never sees it.

/** Halo tone, stroked underneath. Carries the cursor over ink. */
const CURSOR_HALO = "#ffffff";
const CURSOR_HALO_W = 3.5;
/** Core tone, stroked on top. Carries it over paper. Matches palette index 0. */
const CURSOR_CORE = "#14161f";
const CURSOR_CORE_W = 1.6;
/**
 * Ring radius floor in CSS px. Deliberately small: the thinnest nib really is
 * about two CSS pixels wide, and inflating the ring to a comfortable size would
 * make the two smallest nibs draw the same cursor. Visibility at that size is
 * the ticks' job, not the ring's.
 */
const CURSOR_MIN_RADIUS = 1.5;
/** Gap between ring and ticks, and the tick length itself, in CSS px. */
const CURSOR_GAP = 3;
const CURSOR_TICK = 5;
/**
 * Total edge length ceiling in CSS px.
 *
 * Chrome ignores a cursor image past 128x128 outright — no cursor, no warning,
 * the arrow stays — so the ring is clamped to fit well inside that rather than
 * risk the silent failure on a very large pad or a very thick nib.
 */
const CURSOR_MAX_SIZE = 96;

/**
 * A ring at the nib's diameter plus four approach ticks, each stroked twice:
 * a wide halo underneath, a narrow core on top.
 *
 * @param diameter the stroke width in CSS px, already mapped out of logical
 *   space — a cursor is sized in CSS pixels, so this cannot be a logical unit.
 */
function penCursorValue(diameter: number): string {
  const room = CURSOR_MAX_SIZE / 2 - CURSOR_GAP - CURSOR_TICK - 2;
  const r = Math.min(room, Math.max(CURSOR_MIN_RADIUS, diameter / 2));
  const half = Math.ceil(r + CURSOR_GAP + CURSOR_TICK + 2);
  const size = half * 2;
  const inner = r + CURSOR_GAP;
  const outer = inner + CURSOR_TICK;
  const near = (half - outer).toFixed(2);
  const nearIn = (half - inner).toFixed(2);
  const farIn = (half + inner).toFixed(2);
  const far = (half + outer).toFixed(2);
  const ticks =
    `M${half} ${near}V${nearIn}M${half} ${farIn}V${far}` +
    `M${near} ${half}H${nearIn}M${farIn} ${half}H${far}`;
  const shapes = `<circle cx="${half}" cy="${half}" r="${r.toFixed(2)}"/><path d="${ticks}"/>`;
  // width/height as well as viewBox: Firefox will not render an SVG cursor
  // sized only by its viewBox.
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="0 0 ${size} ${size}">` +
    `<g fill="none" stroke-linecap="round">` +
    `<g stroke="${CURSOR_HALO}" stroke-width="${CURSOR_HALO_W}" stroke-opacity=".95">${shapes}</g>` +
    `<g stroke="${CURSOR_CORE}" stroke-width="${CURSOR_CORE_W}">${shapes}</g>` +
    `</g></svg>`;
  // encodeURIComponent, not a raw string: the stroke colours carry '#', which
  // would truncate the data URI at a fragment. The trailing keyword is the
  // fallback for a browser that refuses the image outright.
  return `url("data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}") ${half} ${half}, crosshair`;
}

export class Surface {
  readonly stage: HTMLDivElement;
  readonly pad: HTMLDivElement;
  readonly base: HTMLCanvasElement;
  readonly overlay: HTMLCanvasElement;
  readonly baseCtx: CanvasRenderingContext2D;
  readonly overlayCtx: CanvasRenderingContext2D;
  /** True when the browser honoured the low-latency hint. Informational. */
  readonly desynchronized: boolean;

  private observer: ResizeObserver | null = null;
  private mq: MediaQueryList | null = null;
  private onResize: () => void = () => {};
  private lastW = 0;
  private lastH = 0;
  private resizeRaf: number | null = null;
  /** Nominal nib width in logical units, mirrored here to size the cursor. */
  private penWidth = 0;
  /** Last cursor value written, so a resize that changes nothing costs no DOM. */
  private penCursor = "";

  constructor() {
    installStyle();

    this.stage = document.createElement("div");
    this.stage.className = "verso-stage";

    this.pad = document.createElement("div");
    this.pad.className = "verso-pad";
    this.pad.dataset["drawing"] = "false";

    this.base = document.createElement("canvas");
    this.base.setAttribute("role", "img");
    this.base.setAttribute("aria-label", "Shared drawing canvas");

    this.overlay = document.createElement("canvas");
    // Purely decorative: it holds a copy of ink the base layer will own a
    // moment later, and screen readers get nothing from either bitmap.
    this.overlay.setAttribute("aria-hidden", "true");

    this.pad.append(this.base, this.overlay);
    this.stage.append(this.pad);

    this.baseCtx = context(this.base);
    this.overlayCtx = context(this.overlay);

    const attrs = this.baseCtx.getContextAttributes?.();
    this.desynchronized = attrs?.desynchronized === true;
  }

  mount(container: HTMLElement, onResize: () => void): void {
    this.onResize = onResize;
    container.append(this.stage);
    this.observer = new ResizeObserver(() => this.scheduleResize());
    this.observer.observe(this.pad);
    this.watchDpr();
    // The first one is synchronous: mounting a frame late means presenting an
    // unsized canvas, and there is nothing yet to coalesce with.
    this.resize();
  }

  unmount(): void {
    this.observer?.disconnect();
    this.observer = null;
    this.mq?.removeEventListener("change", this.onDprChange);
    this.mq = null;
    if (this.resizeRaf !== null) {
      cancelAnimationFrame(this.resizeRaf);
      this.resizeRaf = null;
    }
    this.onResize = () => {};
    this.stage.remove();
  }

  /**
   * Coalesce every resize signal down to at most one evaluation per frame.
   *
   * A drag on a window edge fires ResizeObserver many times between two paints,
   * and each one that changed the dimensions used to reallocate two backing
   * stores and re-render the entire committed stroke log synchronously. None of
   * that work but the last could ever be seen. Rotating a phone fires the
   * observer and the DPR media query together, which is the same waste twice.
   *
   * The resize itself stays atomic within its frame: both layers are resized,
   * both transforms are restored, and one redraw repaints the whole canvas
   * before the browser presents anything — so the paper is never handed to the
   * compositor blank.
   */
  private scheduleResize(): void {
    if (this.resizeRaf !== null) return;
    this.resizeRaf = requestAnimationFrame(() => {
      this.resizeRaf = null;
      this.resize();
    });
  }

  private onDprChange = (): void => {
    this.watchDpr();
    this.scheduleResize();
  };

  private watchDpr(): void {
    this.mq?.removeEventListener("change", this.onDprChange);
    const dpr = window.devicePixelRatio || 1;
    this.mq = window.matchMedia(`(resolution: ${dpr}dppx)`);
    this.mq.addEventListener("change", this.onDprChange);
  }

  /**
   * Re-measure and, if the backing store changed, resize both layers and ask
   * the owner to redraw. Assigning canvas.width resets all context state, so
   * the transform is re-applied unconditionally — setTransform is idempotent,
   * which is exactly why it is used instead of scale.
   */
  resize(): void {
    const rect = this.pad.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;

    const dpr = window.devicePixelRatio || 1;
    let scale = (dpr * rect.width) / LOGICAL_W;

    // Clamp by area first, then by either dimension. Both fail silently when
    // exceeded, so neither may be left to chance. The area ceiling is the
    // tighter of what the browser tolerates per canvas and what the two-layer
    // byte budget allows, so an honest DPR on a large display is downscaled to
    // fit rather than accepted and paid for twice.
    const areaCap = Math.min(MAX_CANVAS_AREA, MAX_LAYER_PIXELS);
    const area = LOGICAL_W * LOGICAL_H * scale * scale;
    if (area > areaCap) scale *= Math.sqrt(areaCap / area);
    scale = Math.min(scale, MAX_CANVAS_DIM / LOGICAL_W, MAX_CANVAS_DIM / LOGICAL_H);

    const w = Math.max(1, Math.round(LOGICAL_W * scale));
    const h = Math.max(1, Math.round(LOGICAL_H * scale));

    const changed = w !== this.lastW || h !== this.lastH;
    if (changed) {
      this.base.width = w;
      this.base.height = h;
      this.overlay.width = w;
      this.overlay.height = h;
      this.lastW = w;
      this.lastH = h;
    }

    // Use the realised integer sizes, not `scale`, so rounding cannot skew.
    this.baseCtx.setTransform(w / LOGICAL_W, 0, 0, h / LOGICAL_H, 0, 0);
    this.overlayCtx.setTransform(w / LOGICAL_W, 0, 0, h / LOGICAL_H, 0, 0);

    // The cursor is sized off the CSS box, not the backing store, so it is
    // refreshed on every measurement — a pad that changed width without
    // changing its device-pixel size still needs a new ring.
    this.applyPenCursor(rect.width);

    if (changed) this.onResize();
  }

  /**
   * Tell the surface what the nib is, in logical units, so the cursor ring can
   * show its true diameter. Safe to call before mount and safe to repeat.
   */
  setPenWidth(width: number): void {
    if (width === this.penWidth) return;
    this.penWidth = width;
    this.applyPenCursor(this.pad.getBoundingClientRect().width);
  }

  private applyPenCursor(cssWidth: number): void {
    if (cssWidth <= 0 || this.penWidth <= 0) return;
    const value = penCursorValue((this.penWidth * cssWidth) / LOGICAL_W);
    if (value === this.penCursor) return;
    this.penCursor = value;
    this.pad.style.setProperty("--verso-pen-cursor", value);
  }

  /**
   * What the two backing stores currently cost. Nothing reads this in the
   * running game; it exists so a profiling session can state the number rather
   * than infer it from a screenshot.
   */
  metrics(): SurfaceMetrics {
    const rect = this.pad.getBoundingClientRect();
    return {
      width: this.lastW,
      height: this.lastH,
      devicePixelRatio: window.devicePixelRatio || 1,
      effectiveRatio: rect.width > 0 ? this.lastW / rect.width : 0,
      bytes: this.lastW * this.lastH * BYTES_PER_PIXEL * LAYER_COUNT,
    };
  }

  /**
   * The letterbox offset is the pad's own rect origin, so a single
   * getBoundingClientRect() is the whole mapping. Read it once per event
   * handler and reuse it across a coalesced batch.
   */
  padRect(): DOMRect {
    return this.pad.getBoundingClientRect();
  }

  /** Client coordinates -> the 1024x768 logical space. */
  toLogical(clientX: number, clientY: number): { x: number; y: number } {
    const rect = this.padRect();
    return {
      x: toLogicalX(rect, clientX),
      y: toLogicalY(rect, clientY),
    };
  }

  clearBase(): void {
    this.clear(this.baseCtx, this.base);
  }

  clearOverlay(): void {
    this.clear(this.overlayCtx, this.overlay);
  }

  /**
   * The documented fast path for committing a stroke: one bitmap copy instead
   * of re-running the geometry. It must run with an identity transform, or the
   * device-pixel bitmap is scaled a second time by the DPR transform.
   */
  compositeOverlay(): void {
    this.baseCtx.save();
    this.baseCtx.setTransform(1, 0, 0, 1, 0, 0);
    this.baseCtx.drawImage(this.overlay, 0, 0);
    this.baseCtx.restore();
    this.clearOverlay();
  }

  setDrawingAffordance(enabled: boolean): void {
    this.pad.dataset["drawing"] = enabled ? "true" : "false";
  }

  private clear(ctx: CanvasRenderingContext2D, canvas: HTMLCanvasElement): void {
    ctx.save();
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    ctx.restore();
  }
}
