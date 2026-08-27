import { PenRule } from "../../gen/verso/v1/game_pb.js";
import type { CanvasHandle } from "./context.js";
import { Disposers, el, fill, setText } from "./dom.js";
import { NIB_WIDTHS, PEN_INKS } from "./palette.js";

export interface ToolsView {
  root: HTMLElement;
  /** Only the current artist may draw; everyone else sees the row inert. */
  setEnabled(on: boolean): void;
  /**
   * The pen rule's stroke budget for this turn. `used` counts strokes started,
   * including the one still under the pointer — `penDown` separates that one
   * out, because a stroke in progress is not spent yet.
   *
   * Under FREE this renders nothing at all: the default game's pen card must
   * look exactly as it did before the rule existed. Cheap to call every frame;
   * repeats are dropped before they reach the DOM.
   */
  setBudget(rule: PenRule, used: number, penDown: boolean): void;
  dispose(): void;
}

/** Strokes a rule allows per turn. 0 means no ceiling worth drawing a gauge for. */
function ceilingFor(rule: PenRule): number {
  if (rule === PenRule.ONE_LINE) return 1;
  if (rule === PenRule.MAX_FIVE) return 5;
  return 0;
}

const INK_NAMES = [
  "ink", "slate", "brown", "red", "orange", "amber",
  "green", "teal", "blue", "navy", "purple", "pink",
];
const NIB_NAMES = ["thin", "medium", "thick", "extra thick"];

/**
 * Colour swatches and brush sizes. There is deliberately no eraser, no undo and
 * no text tool — DESIGN.md:87 forbids them, so the UI must not hint they exist.
 * The stroke gauge is a readout of what the pen rule allows, not a fourth tool:
 * it takes nothing back, which is why a spent tick stays on screen.
 */
export function tools(canvas: CanvasHandle): ToolsView {
  const d = new Disposers();
  const swatchEls: HTMLButtonElement[] = [];
  const nibEls: HTMLButtonElement[] = [];
  let colorIndex = 0;
  let widthIndex = 1;
  let enabled = false;
  /** The rule locked the pen for the rest of this turn. */
  let outOfInk = false;
  /** Last budget rendered, so the per-frame poll costs a string compare. */
  let budgetKey = "";

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

  // The gauge and the note bar live outside the tree until a rule asks for
  // them, so a FREE match never renders either one.
  const gaugeTicks = el("span", { class: "gauge-ticks", role: "img", "aria-label": "" });
  const gaugeCount = el("b");
  const gaugeOf = el("span");
  const gauge = el("div", { class: "gauge" }, gaugeTicks, el("span", { class: "gauge-label" }, gaugeCount, gaugeOf));
  const note = el("p", { class: "spent-note" });
  const tickEls: HTMLElement[] = [];

  /**
   * One place decides whether the rack is reachable. A non-artist and an artist
   * who has spent their budget both get the same inert card — there is nothing
   * left to choose a colour for, and a control that lies is worse than none.
   */
  const applyEnabled = () => {
    const on = enabled && !outOfInk;
    root.setAttribute("aria-disabled", String(!on));
    for (const b of swatchEls) b.disabled = !on;
    for (const b of nibEls) b.disabled = !on;
  };

  paintSelection();
  canvas.setColorIndex(colorIndex);
  canvas.setWidth(NIB_WIDTHS[widthIndex] ?? 8);

  return {
    root,
    setEnabled(on) {
      enabled = on;
      applyEnabled();
    },
    setBudget(rule, used, penDown) {
      const key = `${rule}:${used}:${penDown ? 1 : 0}`;
      if (key === budgetKey) return;
      budgetKey = key;

      const limit = ceilingFor(rule);
      if (limit === 0) {
        gauge.remove();
        note.remove();
        outOfInk = false;
        applyEnabled();
        return;
      }

      // The open stroke is not spent until the pen comes up: it is the live
      // tick, and the artist can still steer it.
      const spent = penDown ? Math.max(0, used - 1) : Math.min(used, limit);
      const left = Math.max(0, limit - spent);
      const oneLine = rule === PenRule.ONE_LINE;

      if (tickEls.length !== limit) {
        tickEls.length = 0;
        for (let i = 0; i < limit; i++) tickEls.push(el("i", { class: "gauge-tick" }));
        fill(gaugeTicks, ...tickEls);
      }
      tickEls.forEach((t, i) => {
        const cls = i < spent ? "gauge-tick spent" : i === spent && penDown ? "gauge-tick live" : "gauge-tick";
        if (t.className !== cls) t.className = cls;
      });

      let count: string;
      let of: string;
      let reading: string;
      if (oneLine && penDown) {
        count = "Pen down";
        of = "keep it down";
        reading = "Your one line is in progress";
      } else if (oneLine && left === 0) {
        count = "Line done";
        of = "pen up";
        reading = "Your line is finished";
      } else if (oneLine) {
        count = "One line";
        of = "one unbroken stroke";
        reading = "Your one line is still to draw";
      } else if (left === 0) {
        count = "Spent";
        of = `all ${limit} used`;
        reading = "No strokes left";
      } else {
        count = `${left} left`;
        of = `of ${limit} strokes`;
        reading = `${left} of ${limit} strokes left`;
      }
      setText(gaugeCount, count);
      setText(gaugeOf, of);
      if (gaugeTicks.getAttribute("aria-label") !== reading) gaugeTicks.setAttribute("aria-label", reading);
      const gaugeCls = `gauge${oneLine ? " oneline" : ""}${left === 0 ? " spent" : ""}`;
      if (gauge.className !== gaugeCls) gauge.className = gaugeCls;
      if (gauge.parentNode !== root) root.insertBefore(gauge, swatches);

      // A note bar only exists while it has something to say: mid-line, or once
      // the pen is locked. Everything in between is the ordinary pen card.
      const live = oneLine && penDown;
      const noteText = live
        ? "Your line is live. Lift and it's finished."
        : left > 0
          ? ""
          : oneLine
            ? "Pen lifted — your line is on the canvas for good."
            : "Out of strokes. The clock keeps running — sit on your hands.";
      if (noteText) {
        note.className = live ? "live-note" : "spent-note";
        setText(note, noteText);
        if (note.parentNode !== root) root.appendChild(note);
      } else {
        note.remove();
      }

      outOfInk = left === 0;
      applyEnabled();
    },
    dispose() {
      d.dispose();
    },
  };
}
