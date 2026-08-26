import { el, toggle } from "./dom.js";
import type { CanvasHandle } from "./context.js";

export interface StageView {
  root: HTMLElement;
  /** Dashed outline when nobody can draw, so "locked" is not colour-only. */
  setLocked(locked: boolean): void;
  dispose(): void;
}

/**
 * Host element for the shared canvas. Pure white paper — it never inherits the
 * page tint — with `touch-action: none` scoped to exactly this box so the rest
 * of the page keeps pinch-zoom (WCAG 2.0 SC 1.4.4).
 */
export function stage(canvas: CanvasHandle, label: string): StageView {
  const root = el("div", { class: "stage", role: "img", "aria-label": label });
  canvas.attach(root);
  return {
    root,
    setLocked(locked) {
      toggle(root, "stage-locked", locked);
    },
    dispose() {
      canvas.detach();
    },
  };
}
