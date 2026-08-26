import { el, svgEl } from "./dom.js";

/**
 * Decorative sprites, in the paper-and-marker language of the rest of the app.
 *
 * Nothing here reads game state beyond a colour hint, and nothing renders inside
 * the canvas box — a mark composited over the shared drawing would read as a
 * stroke the artist made, which actively misleads in a deduction game. The
 * paintress therefore stands on the floor *below* the stage and paints upward at
 * it from outside the frame.
 */

export interface PaintressView {
  root: HTMLElement;
  /** Tints her marker. Purely cosmetic — she is not a "who is drawing" signal. */
  setInk(hex: string): void;
}

/** Her marker, and the stroke marks that puff off the nib, follow this colour. */
const DEFAULT_INK = "var(--accent)";

function paintressSvg(): SVGSVGElement {
  const arm = svgEl(
    "g",
    { class: "pt-arm" },
    svgEl("path", { class: "pt-sleeve-out", d: "M74 78L100 51" }),
    svgEl("path", { class: "pt-sleeve-in", d: "M74 78L100 51" }),
    svgEl("circle", { class: "pt-skin", cx: 102, cy: 48, r: 7.5 }),
    svgEl(
      "g",
      { transform: "rotate(22 102 48)" },
      svgEl("rect", { class: "pt-pen", x: 97.5, y: 20, width: 9.5, height: 28, rx: 4.5 }),
      svgEl("rect", { class: "pt-nib", x: 99.5, y: 12, width: 5.5, height: 8, rx: 2.5 }),
    ),
    svgEl("path", { class: "pt-mark", d: "M116 22q5 3 8 0" }),
    svgEl("path", { class: "pt-mark pt-mark-2", d: "M112 30q6 1 10-2" }),
    svgEl("path", { class: "pt-mark pt-mark-3", d: "M118 38q4 3 7 2" }),
  );

  const body = svgEl(
    "g",
    { class: "pt-bob" },
    svgEl("circle", { class: "pt-hair", cx: 28, cy: 30, r: 12 }),
    svgEl("path", { class: "pt-hair", d: "M22 40c-9 4-12 13-8 20 3 6 9 8 14 7" }),
    svgEl("rect", { class: "pt-smock", x: 28, y: 60, width: 52, height: 52, rx: 20 }),
    svgEl("circle", { class: "pt-skin", cx: 54, cy: 40, r: 21 }),
    svgEl("ellipse", { class: "pt-beret", cx: 55, cy: 24, rx: 25, ry: 10.5 }),
    svgEl("circle", { class: "pt-beret", cx: 55, cy: 13.5, r: 4.5 }),
    svgEl("rect", { class: "pt-eye", x: 63, y: 38, width: 4.6, height: 6.6, rx: 2.2 }),
    svgEl("circle", { class: "pt-cheek", cx: 70, cy: 49, r: 4 }),
    svgEl("path", { class: "pt-smile", d: "M61 50q5.5 4.5 10-.5" }),
    arm,
  );

  return svgEl(
    "svg",
    { class: "pt", viewBox: "0 0 128 118", "aria-hidden": "true", focusable: "false" },
    svgEl("ellipse", { class: "pt-shadow", cx: 54, cy: 112, rx: 33, ry: 5 }),
    body,
  );
}

/**
 * The floor strip that goes directly under the stage. Ambient only: she paints
 * at the same calm tempo whether or not anyone is actually drawing.
 */
export function paintress(): PaintressView {
  const svg = paintressSvg();
  const root = el("div", { class: "easel", "aria-hidden": "true" }, svg);
  root.style.setProperty("--nib", DEFAULT_INK);
  return {
    root,
    setInk(hex) {
      root.style.setProperty("--nib", hex);
    },
  };
}

/** A single marker, for the roster rail. Same drawing language, no character. */
export function markerGlyph(): SVGSVGElement {
  return svgEl(
    "svg",
    { class: "baton-pen", viewBox: "0 0 26 44", "aria-hidden": "true", focusable: "false" },
    svgEl("rect", { class: "pt-pen", x: 3, y: 3, width: 20, height: 29, rx: 8 }),
    svgEl("rect", { class: "pt-nib", x: 8.5, y: 31, width: 9, height: 10, rx: 4 }),
    svgEl("path", { class: "pt-cap", d: "M3.6 13h18.8" }),
  );
}
