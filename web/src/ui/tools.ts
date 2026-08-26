import type { CanvasHandle } from "./context.js";
import { Disposers, el } from "./dom.js";
import { NIB_WIDTHS, PEN_INKS } from "./palette.js";

export interface ToolsView {
  root: HTMLElement;
  /** Only the current artist may draw; everyone else sees the row inert. */
  setEnabled(on: boolean): void;
  dispose(): void;
}

const INK_NAMES = [
  "ink", "slate", "brown", "red", "orange", "amber",
  "green", "teal", "blue", "navy", "purple", "pink",
];
const NIB_NAMES = ["thin", "medium", "thick", "extra thick"];

/**
 * Colour swatches and brush sizes. There is deliberately no eraser, no undo and
 * no text tool — DESIGN.md:87 forbids them, so the UI must not hint they exist.
 */
export function tools(canvas: CanvasHandle): ToolsView {
  const d = new Disposers();
  const swatchEls: HTMLButtonElement[] = [];
  const nibEls: HTMLButtonElement[] = [];
  let colorIndex = 0;
  let widthIndex = 1;

  const paintSelection = () => {
    swatchEls.forEach((b, i) => b.setAttribute("aria-pressed", String(i === colorIndex)));
    nibEls.forEach((b, i) => b.setAttribute("aria-pressed", String(i === widthIndex)));
  };

  const swatches = el("div", { class: "swatches", role: "group", "aria-label": "Pen colour" });
  PEN_INKS.forEach((hex, i) => {
    const b = el("button", {
      type: "button",
      class: "swatch",
      style: `background:${hex}`,
      "aria-pressed": "false",
      "aria-label": `Pen colour ${INK_NAMES[i] ?? String(i + 1)}`,
    });
    d.on(b, "click", () => {
      colorIndex = i;
      canvas.setColorIndex(i);
      paintSelection();
    });
    swatchEls.push(b);
    swatches.appendChild(b);
  });

  const nibs = el("div", { class: "nibs", role: "group", "aria-label": "Brush size" });
  NIB_WIDTHS.forEach((w, i) => {
    const dot = el("span", {
      class: "nib-dot",
      style: `width:${Math.round(w / 2) + 3}px;height:${Math.round(w / 2) + 3}px`,
    });
    const b = el(
      "button",
      {
        type: "button",
        class: "nib",
        "aria-pressed": "false",
        "aria-label": `Brush size ${NIB_NAMES[i] ?? String(i + 1)}`,
      },
      dot,
    );
    d.on(b, "click", () => {
      widthIndex = i;
      canvas.setWidth(w);
      paintSelection();
    });
    nibEls.push(b);
    nibs.appendChild(b);
  });

  const root = el(
    "section",
    { class: "card tools", "aria-disabled": "true" },
    el("div", { class: "card-title", text: "Your pen" }),
    swatches,
    el("div", { style: "height:.6rem" }),
    nibs,
  );

  paintSelection();
  canvas.setColorIndex(colorIndex);
  canvas.setWidth(NIB_WIDTHS[widthIndex] ?? 8);

  return {
    root,
    setEnabled(on) {
      root.setAttribute("aria-disabled", String(!on));
      for (const b of swatchEls) b.disabled = !on;
      for (const b of nibEls) b.disabled = !on;
    },
    dispose() {
      d.dispose();
    },
  };
}
