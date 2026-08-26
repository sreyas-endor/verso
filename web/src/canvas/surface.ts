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

import { LOGICAL_H, LOGICAL_W } from "./grid.js";

/** 4096^2 device pixels. Safe on every current target including pre-18 iOS. */
const MAX_CANVAS_AREA = 16_777_216;
const MAX_CANVAS_DIM = 8192;

const STYLE_ID = "verso-canvas-style";

// The paper is the one surface in the app that never takes the page tint: it is
// white in both themes, and the PNG export composites onto the same white.
export const PAPER = "#ffffff";

const CSS = `
.verso-stage{position:relative;display:grid;place-items:center;width:100%;height:100%;min-width:0;min-height:0;}
.verso-pad{position:relative;width:100%;max-width:100%;max-height:100%;aspect-ratio:${LOGICAL_W}/${LOGICAL_H};
  background:${PAPER};border:2px solid var(--border-str,#c6cee0);border-radius:var(--radius,14px);
  overflow:hidden;touch-action:none;-webkit-user-select:none;user-select:none;-webkit-touch-callout:none;}
.verso-pad canvas{position:absolute;inset:0;width:100%;height:100%;display:block;touch-action:none;}
.verso-pad[data-drawing="true"]{cursor:crosshair;box-shadow:0 0 0 3px var(--accent-sf,#e8eeff);}
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
    this.observer = new ResizeObserver(() => this.resize());
    this.observer.observe(this.pad);
    this.watchDpr();
    this.resize();
  }

  unmount(): void {
    this.observer?.disconnect();
    this.observer = null;
    this.mq?.removeEventListener("change", this.onDprChange);
    this.mq = null;
    this.onResize = () => {};
    this.stage.remove();
  }

  private onDprChange = (): void => {
    this.watchDpr();
    this.resize();
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
    // exceeded, so neither may be left to chance.
    const area = LOGICAL_W * LOGICAL_H * scale * scale;
    if (area > MAX_CANVAS_AREA) scale *= Math.sqrt(MAX_CANVAS_AREA / area);
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

    if (changed) this.onResize();
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
