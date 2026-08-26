import { el, setText, toggle } from "./dom.js";

export interface TimerView {
  root: HTMLElement;
  /** `deadline` is a performance.now() reading; null renders a dash. */
  update(deadline: number | null, durationMs: number): void;
}

function format(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return m > 0 ? `${m}:${String(s).padStart(2, "0")}` : String(s);
}

/**
 * The loudest element after the canvas: a tabular countdown over a depleting
 * bar. Colour shifts at 33% and 10%, and the number is always readable on its
 * own so the state never depends on the colour.
 */
export function timer(label: string): TimerView {
  const num = el("div", { class: "timer-num", text: "–" });
  const fill = el("i", { class: "timer-fill" });
  const bar = el("div", { class: "timer-bar" }, fill);
  const root = el(
    "div",
    { class: "timer", role: "timer", "aria-label": label, "aria-live": "off" },
    num,
    bar,
  );

  let lastText = "";

  return {
    root,
    update(deadline, durationMs) {
      if (deadline === null || durationMs <= 0) {
        setText(num, "–");
        fill.style.width = "0%";
        toggle(root, "timer-warn", false);
        toggle(root, "timer-crit", false);
        return;
      }
      const left = Math.max(0, deadline - performance.now());
      const frac = Math.max(0, Math.min(1, left / durationMs));
      const text = format(left);
      if (text !== lastText) {
        lastText = text;
        setText(num, text);
        root.setAttribute("aria-label", `${label}: ${text} seconds left`);
      }
      fill.style.width = `${(frac * 100).toFixed(2)}%`;
      toggle(root, "timer-warn", frac < 0.33 && frac >= 0.1);
      toggle(root, "timer-crit", frac < 0.1);
    },
  };
}
