// input.ts — Pointer Events, and nothing else.
//
// Every rule in IMPLEMENTATION_PLAN.md §7 that concerns input is implemented
// here, and each one is a bug someone shipped:
//
//   - setPointerCapture on pointerdown. Touch and pen get implicit capture;
//     MOUSE DOES NOT. Without it, dragging off the canvas stops delivering
//     moves and pointerup never arrives, so the stroke hangs live forever.
//   - terminate on lostpointercapture and pointercancel as well as pointerup.
//     pointercancel fires on orientation change, app switch and Pencil palm
//     rejection; if any ancestor carries touch-action: manipulation, iOS
//     suppresses it entirely (WebKit 240917) and strokes stick.
//   - getCoalescedEvents, guarded for empty and 1-element returns. On iOS 18.2
//     the coalesced entries carry no pointerId and no target, so identity comes
//     from the OUTER event and only geometry comes from the inner one.
//   - no pointerrawupdate: Safari has never shipped it.
//   - pressure is ignored. The spec pins it to exactly 0.5 for hardware without
//     pressure support, and iOS returns a hard 0 for anything but a Pencil, so
//     it is zero signal. Width comes from velocity (geometry.ts). The one
//     Pencil concession is gated hard and applied once, at stroke start, to the
//     nominal width that goes on the wire — so it stays consistent for viewers.
//   - event.buttons (a bitmask) on move, never event.button, whose 0 is
//     ambiguous with the uninitialised state.
//   - no palm rejection. There is no API for it, width/height are always 1 on
//     iOS, and a heuristic rejects legitimate input more often than it helps.
//   - listeners go on the CANVAS element. window, document, documentElement and
//     body are the four targets the DOM spec forces passive:true on for
//     touchstart/touchmove/wheel, which would make the guard below a no-op.

import { clampWidth } from "./grid.js";
import { Surface, toLogicalX, toLogicalY } from "./surface.js";

export interface PointerSink {
  /** @param widthScale the gated Pencil bonus, 1 for everything else. */
  begin(x: number, y: number, widthScale: number): void;
  /** @param points flat interleaved logical coordinates, at least one pair. */
  extend(points: number[]): void;
  end(): void;
}

export class PointerInput {
  private readonly surface: Surface;
  private readonly sink: PointerSink;
  private enabled = false;
  private active: number | null = null;

  constructor(surface: Surface, sink: PointerSink) {
    this.surface = surface;
    this.sink = sink;
  }

  attach(): void {
    const el = this.surface.overlay;
    el.addEventListener("pointerdown", this.onDown);
    el.addEventListener("pointermove", this.onMove);
    el.addEventListener("pointerup", this.onUp);
    el.addEventListener("pointercancel", this.onUp);
    el.addEventListener("lostpointercapture", this.onUp);
    // preventDefault() on pointerdown only began suppressing iOS scroll in
    // Safari 26.5, so the explicit touchmove guard stays.
    el.addEventListener("touchmove", this.onTouchMove, { passive: false });
  }

  detach(): void {
    const el = this.surface.overlay;
    el.removeEventListener("pointerdown", this.onDown);
    el.removeEventListener("pointermove", this.onMove);
    el.removeEventListener("pointerup", this.onUp);
    el.removeEventListener("pointercancel", this.onUp);
    el.removeEventListener("lostpointercapture", this.onUp);
    el.removeEventListener("touchmove", this.onTouchMove);
    this.active = null;
  }

  setEnabled(enabled: boolean): void {
    this.enabled = enabled;
    if (!enabled) this.abort();
  }

  get drawing(): boolean {
    return this.active !== null;
  }

  /** Force the live stroke closed — turn ended, snapshot arrived, unmounted. */
  abort(): void {
    if (this.active === null) return;
    this.releaseActive();
    this.sink.end();
  }

  private releaseActive(): void {
    const id = this.active;
    this.active = null;
    if (id === null) return;
    if (this.surface.overlay.hasPointerCapture(id)) {
      this.surface.overlay.releasePointerCapture(id);
    }
  }

  private onDown = (e: PointerEvent): void => {
    if (!this.enabled || this.active !== null) return;
    // A mouse with no button down is a hover, not a stroke.
    if (e.pointerType === "mouse" && (e.buttons & 1) === 0) return;

    e.preventDefault();
    this.active = e.pointerId;
    try {
      this.surface.overlay.setPointerCapture(e.pointerId);
    } catch {
      // Capture can be refused if the pointer went away between dispatch and
      // handling. The lostpointercapture/pointercancel path still terminates.
    }

    const rect = this.surface.padRect();
    this.sink.begin(
      toLogicalX(rect, e.clientX),
      toLogicalY(rect, e.clientY),
      penWidthScale(e),
    );
  };

  private onMove = (e: PointerEvent): void => {
    if (this.active === null || e.pointerId !== this.active) return;
    // The button was released somewhere we never saw the up event.
    if ((e.buttons & 1) === 0) {
      this.finish();
      return;
    }
    e.preventDefault();

    const rect = this.surface.padRect();
    const points: number[] = [];
    for (const s of samples(e)) {
      points.push(toLogicalX(rect, s.clientX), toLogicalY(rect, s.clientY));
    }
    if (points.length > 0) this.sink.extend(points);
  };

  private onUp = (e: PointerEvent): void => {
    if (this.active === null || e.pointerId !== this.active) return;
    this.finish();
  };

  private onTouchMove = (e: TouchEvent): void => {
    if (this.active !== null) e.preventDefault();
  };

  private finish(): void {
    this.releaseActive();
    this.sink.end();
  }
}

/**
 * The samples for one move event: every coalesced sample when the browser has
 * them, otherwise the event itself. Guards both the empty and the 1-element
 * return, and reads nothing but geometry off the inner entries.
 */
function samples(e: PointerEvent): readonly { clientX: number; clientY: number }[] {
  if (typeof e.getCoalescedEvents === "function") {
    let list: readonly PointerEvent[] = [];
    try {
      list = e.getCoalescedEvents();
    } catch {
      list = [];
    }
    if (list.length > 0) return list;
  }
  return [e];
}

/**
 * The whole of the pressure story. Everything outside this gate is 1.
 *
 * `pressure > 0` rules out iOS non-Pencil, which reports a hard 0; `!== 0.5`
 * rules out the spec's fixed value for hardware with no pressure sensor. What
 * survives is a real Pencil reading, and it is applied once — to the nominal
 * width the server will echo to every viewer — rather than per sample, because
 * the wire carries one width per stroke.
 */
function penWidthScale(e: PointerEvent): number {
  if (e.pointerType !== "pen") return 1;
  if (e.pressure <= 0 || e.pressure === 0.5) return 1;
  return 0.7 + 0.6 * Math.min(1, e.pressure);
}

/** Apply the pen bonus to a nominal width, staying inside the server's bounds. */
export function scaledWidth(width: number, scale: number): number {
  return clampWidth(width * scale);
}
