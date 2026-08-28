// The ejection cinematic: five acts of rose-petal dissolve over one player's
// portrait, played once per vote ejection.
//
// It is mounted beside the screen root rather than inside the result screen,
// because it outlives one. The run is 7.8 s and PHASE_RESOLVING is 8 s, so the
// screen underneath can be swapped for the final reveal while the overlay is
// still up — an overlay owned by a screen would be torn out mid-animation.
//
// Nothing here decides that an ejection happened. It reacts to the authoritative
// PlayerEliminated the room already broadcasts, reads the phase clock and never
// writes to it, and cannot delay afterResolve. There is no new protocol event,
// no server-side animation timer and no new phase behind any of this.

import type { ViewState } from "./context.js";
import { avatar } from "./avatar.js";
import { el, svgEl } from "./dom.js";
import { verdict } from "./verdict.js";

/**
 * Total run, and the value every duration in components.css is a fraction of.
 *
 * Sized by the verdict rather than by the animation. The line has to be READ,
 * so it holds fully opaque for five seconds, and 7.8 s is that hold plus the
 * shortest build-up that still lands it — inside the 8 s ResolveDuration
 * (internal/room/api.go:120) with 200 ms to spare. The only thing that could
 * overlap the next screen is the last 300 ms, which is a fade to nothing over
 * an overlay that never takes pointer events.
 */
const RUN_MS = 7800;
/**
 * The reduced-motion run: the same beats, none of the movement, and the same
 * five-second verdict. A reader who cannot take fast motion is the last person
 * who should be given less time to read.
 */
const CALM_MS = 7500;

/**
 * Enough petals to read as a dissolve rather than a puff, few enough to stay
 * one burst of compositor layers that appear and retire together.
 */
const PETAL_COUNT = 36;
/**
 * The ambient drift that carries acts 1 to 3. Without it the held beat reads as
 * a stall rather than a held breath.
 */
const MOTE_COUNT = 12;

/** One rose petal: cupped, wide at the top, pointed where it left the flower. */
const PETAL_D = "M12 22.5C2.6 16.4 2.6 6.2 8.4 2.4 10.8.8 13.2.8 15.6 2.4c5.8 3.8 5.8 14-3.6 20.1z";

export interface CinematicView {
  /** Append once, near the top of the app. Fixed, and inert until it plays. */
  readonly root: HTMLElement;
  /** Feed it every published state. It decides for itself whether to run. */
  update(s: ViewState): void;
}

export function cinematic(): CinematicView {
  const scrim = el("div", { class: "cin-scrim" });
  const motes = el("div", { class: "cin-motes" }, ...motesFor(MOTE_COUNT));
  const portrait = el("div", { class: "cin-portrait" });
  const petals = el("div", { class: "cin-petals" }, ...petalsFor(PETAL_COUNT));
  const nameLine = el("div", { class: "cin-name" });
  const verdictLine = el("div", { class: "cin-verdict" });

  const root = el(
    "div",
    { class: "cin", "aria-hidden": "true" },
    scrim,
    motes,
    el(
      "div",
      { class: "cin-stage" },
      el(
        "div",
        { class: "cin-figure" },
        el("div", { class: "cin-ring" }),
        el("div", { class: "cin-ring cin-ring-2" }),
        portrait,
        petals,
      ),
      nameLine,
      verdictLine,
    ),
  );
  root.hidden = true;

  // The last ejection this client watched. Not a boolean: a rematch resets the
  // store, so the counter can go backwards as well as forwards, and any change
  // at all is a fresh ejection.
  let seen = 0;
  let timer = 0;

  const finish = () => {
    root.classList.remove("cin-play", "cin-calm");
    // Emptied as well as hidden. A finished overlay that is still in the tree
    // with pointer-events on it silently eats the first click on the screen
    // underneath, and that screen is a vote or a Rematch button.
    root.hidden = true;
    portrait.replaceChildren();
  };

  const play = (s: ViewState, playerId: string, revealed: boolean, wasImposter: boolean) => {
    const gone = s.players.find((p) => p.id === playerId);
    if (gone === undefined) return;

    portrait.replaceChildren(avatar(gone.id, gone.avatar, "lg"));
    nameLine.textContent = gone.name;
    verdictLine.textContent = verdict(s.settings.imposterCount, revealed, wasImposter);

    // Deliberately silent for a screen reader. The result screen already
    // announces this ejection once through the same live region, and the whole
    // point of a live region is that it is not said twice.
    const calm = prefersReducedMotion();
    root.classList.remove("cin-play", "cin-calm");
    root.hidden = false;
    // Reading offsetWidth is what makes a replay restart rather than continue.
    // It is one forced layout per ejection, at most once every eight seconds.
    void root.offsetWidth;
    root.classList.add(calm ? "cin-calm" : "cin-play");

    globalThis.clearTimeout(timer);
    timer = globalThis.setTimeout(finish, (calm ? CALM_MS : RUN_MS) + 120);
  };

  return {
    root,

    update(s) {
      if (s.eliminationSeq === seen) return;
      seen = s.eliminationSeq;
      // Zero is the initial state, which a kick resets us to. Nobody was
      // ejected on the way there.
      if (s.eliminationSeq === 0) return;
      const ev = s.elimination;
      if (!ev || !ev.eliminated) return;
      play(s, ev.playerId, ev.alignmentRevealed, ev.wasImposter);
    },
  };
}

function prefersReducedMotion(): boolean {
  return globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
}

/**
 * Builds the petals once, at construction.
 *
 * Every value a petal needs is written here as a custom property, so the
 * animation itself reads nothing and computes nothing: the compositor runs the
 * whole burst without waking the main thread. Nothing in this file runs per
 * frame, and no element is created while an ejection is playing.
 *
 * Seven depth bands. A far petal is small, dim, slow and does not travel; a
 * near one is large, bright and thrown wide. That is what stops thirty-six of
 * them reading as flat confetti.
 */
function petalsFor(count: number): HTMLElement[] {
  // The golden angle, so no two petals leave along the same line.
  const golden = Math.PI * (3 - Math.sqrt(5));
  const out: HTMLElement[] = [];
  for (let i = 0; i < count; i++) {
    const angle = i * golden + 0.6;
    const depth = (i % 7) / 6;
    const distance = 74 + depth * 104 + ((i * 13) % 19);
    const size = 8 + Math.round(depth * 11) + (i % 3);
    const node = el("span", { class: "petal" }, petalSvg(i));
    setAll(node, {
      "--px": `${(Math.cos(angle) * distance).toFixed(1)}px`,
      // Biased downward: petals fall as well as scatter.
      "--py": `${(Math.sin(angle) * distance * 0.72 + 52).toFixed(1)}px`,
      "--pr": `${(i % 2 ? 1 : -1) * (60 + ((i * 37) % 130))}deg`,
      // Unitless. The stylesheet multiplies these by 1ms, which is what lets
      // one variable retime the whole run without rebuilding an element.
      "--pd": String(780 + Math.round(depth * 240) + ((i * 29) % 130)),
      "--pt": String(2400 + ((i * 17) % 220)),
      "--po": (0.55 + depth * 0.45).toFixed(2),
      "--pw": `${size}px`,
      "--ph": `${Math.round(size * 1.4)}px`,
    });
    out.push(node);
  }
  return out;
}

function motesFor(count: number): HTMLElement[] {
  const out: HTMLElement[] = [];
  for (let i = 0; i < count; i++) {
    const size = 7 + (i % 4) * 3;
    const node = el("span", { class: "mote" }, petalSvg(i));
    setAll(node, {
      "--ml": `${(4 + ((i * 8.3) % 92)).toFixed(1)}%`,
      "--mdx": `${((i * 53) % 90) - 45}px`,
      "--mr": `${(i % 2 ? 1 : -1) * (120 + ((i * 41) % 180))}deg`,
      "--md": String(5200 + ((i * 311) % 1900)),
      "--mt": String(200 + ((i * 227) % 2400)),
      "--mo": (0.1 + (i % 5) * 0.05).toFixed(2),
      "--mw": `${size}px`,
    });
    out.push(node);
  }
  return out;
}

function petalSvg(i: number): SVGElement {
  return svgEl(
    "svg",
    { viewBox: "0 0 24 24", "aria-hidden": "true", focusable: "false" },
    svgEl("path", { class: `petal-ink-${(i % 4) + 1}`, d: PETAL_D }),
  );
}

function setAll(node: HTMLElement, props: Record<string, string>): void {
  for (const [k, v] of Object.entries(props)) node.style.setProperty(k, v);
}
