import type { ScreenCtx, ViewState } from "../context.js";
import { Disposers, el, setText } from "../dom.js";
import { timer } from "../timer.js";

let d: Disposers | null = null;

const MAY_NOT = [
  "Say, spell, translate or rhyme with your word.",
  "Name its category, or define it out loud.",
  "Name the thing you drew, if that gives your word away.",
  "Mime it, or hint at it sideways.",
];
const MAY = [
  "“That shape is being read too specifically.”",
  "“I was building on the clue before mine.”",
  "“Why is nobody talking about that mark?”",
  "“Two of these clues do not fit together.”",
];

function ruleList(cls: string, lead: string, items: readonly string[]): HTMLElement {
  return el(
    "ul",
    { class: `rules ${cls}` },
    ...items.map((t) =>
      el("li", {}, el("b", { text: lead, "aria-hidden": "true" }), el("span", { text: t }))
    ),
  );
}

export function mount(root: HTMLElement, ctx: ScreenCtx): void {
  d = new Disposers();
  const dd = d;

  const wordText = el("div", { class: "reveal-word" });
  const shown = el("div", {}, wordText, el("p", { class: "hint", text: "Only you can see this." }));

  const clock = timer("Time until the first round");

  const view = el(
    "div",
    { class: "reveal" },
    el(
      "section",
      { class: "card reveal-card" },
      el("div", { class: "card-title", text: "Your secret word" }),
      shown,
    ),
    el(
      "section",
      { class: "card" },
      el("div", { class: "card-title", text: "Talking about it" }),
      el("p", { class: "muted", text: "One of you was handed a different word. Nobody is told who." }),
      el("h3", { text: "You may not" }),
      ruleList("rules-no", "No", MAY_NOT),
      el("div", { style: "height:.6rem" }),
      el("h3", { text: "You may" }),
      ruleList("rules-yes", "Yes", MAY),
    ),
    el(
      "section",
      { class: "card row" },
      el("div", { class: "grow" }, el("div", { class: "card-title", text: "First round starts in" }),
        el("p", { class: "hint", text: "Your word stays available in the panel on the right all match." })),
      clock.root,
    ),
  );

  root.appendChild(view);
  dd.add(() => view.remove());

  const render = (s: ViewState) => {
    setText(wordText, s.word || "…");
    clock.update(s.deadline, s.durationMs);
  };

  render(ctx.state());
  dd.add(ctx.subscribe(render));
  dd.raf(() => {
    const s = ctx.state();
    clock.update(s.deadline, s.durationMs);
  });
  ctx.announce("Your private word is on screen. Only you can see it.");
}

export function unmount(): void {
  d?.dispose();
  d = null;
}
