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
  const wordTitle = el("div", { class: "card-title", text: "Your secret word" });
  const clockTitle = el("div", { class: "card-title", text: "Round starts in" });
  // Rounds after the first deal a brand new pair on a blank canvas, and a
  // player who assumes their old word carries over will draw the wrong thing.
  const freshNote = el("p", { class: "reveal-fresh" });

  // The reveal that opens round n runs while the server's counter still reads
  // n-1, so the round about to start is always round + 1.
  const nextRound = (s: ViewState): number => s.round + 1;
  const clock = timer("Time until the round starts");

  const view = el(
    "div",
    { class: "reveal" },
    el("section", { class: "card reveal-card" }, wordTitle, shown, freshNote),
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
      el("div", { class: "grow" }, clockTitle,
        el("p", { class: "hint", text: "Your word stays available in the panel on the right all round." })),
      clock.root,
    ),
  );

  root.appendChild(view);
  dd.add(() => view.remove());

  let announcedFor = -1;

  const render = (s: ViewState) => {
    const n = nextRound(s);
    const first = n <= 1;
    setText(wordText, s.word || "…");
    setText(wordTitle, first ? "Your secret word" : `Your secret word — round ${n}`);
    setText(clockTitle, first ? "First round starts in" : `Round ${n} starts in`);
    setText(
      freshNote,
      first ? "" : "A new pair, on a blank canvas. Last round's word is finished with.",
    );
    clock.update(s.deadline, s.durationMs);

    // Re-announced every round: the word actually changed, and a screen reader
    // that only heard about round 1 would be reading a stale one.
    if (s.word !== "" && announcedFor !== n) {
      announcedFor = n;
      ctx.announce(
        first
          ? "Your private word is on screen. Only you can see it."
          : `Round ${n}. You have a new private word on screen. Only you can see it.`,
      );
    }
  };

  render(ctx.state());
  dd.add(ctx.subscribe(render));
  dd.raf(() => {
    const s = ctx.state();
    clock.update(s.deadline, s.durationMs);
  });
}

export function unmount(): void {
  d?.dispose();
  d = null;
}
