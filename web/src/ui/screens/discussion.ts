import type { ScreenCtx, ViewState } from "../context.js";
import { Disposers, el, fill, setText } from "../dom.js";
import { playerList } from "../playerList.js";
import { stage } from "../stage.js";
import { timer } from "../timer.js";
import { votePicker } from "../votePicker.js";
import { wordPanel } from "../wordPanel.js";

let d: Disposers | null = null;

export function mount(root: HTMLElement, ctx: ScreenCtx): void {
  d = new Disposers();
  const dd = d;

  const roster = playerList("Players", { showTurnQueue: true });
  const board = stage(ctx.canvas, "Finished canvas for this round");
  ctx.canvas.setInteractive(false);
  board.setLocked(true);
  dd.add(() => board.dispose());

  const picker = votePicker((choice) => ctx.actions.castVote(choice));
  dd.add(() => picker.dispose());
  const word = wordPanel();
  const clock = timer("Time left to discuss and vote");

  const kicker = el("div", { class: "phasehead-kicker" });
  const title = el("div", { class: "phasehead-title" });
  const head = el(
    "section",
    { class: "card phasehead" },
    el("div", { class: "phasehead-main" }, kicker, title),
    clock.root,
  );

  const spectator = el("section", { class: "card spectator" });
  spectator.hidden = true;

  const main = el("div", { class: "col-main" }, head, board.root);
  const right = el("div", { class: "col-right stack" }, picker.root, spectator, word.root);
  const view = el("div", { class: "cols" }, roster.root, main, right);
  root.appendChild(view);
  dd.add(() => view.remove());

  let announcedLock = false;

  const render = (s: ViewState) => {
    roster.update(s);
    word.update(s.word);
    setText(kicker, `Round ${s.round} of ${s.totalRounds}`);
    fill(title, el("em", { text: "Discuss" }), " — then vote in secret");

    if (s.youAreEliminated) {
      picker.root.hidden = true;
      spectator.hidden = false;
      fill(
        spectator,
        el("div", { class: "card-title", text: "Spectating" }),
        el("p", { text: "You were eliminated, so you no longer vote. The others are deciding." }),
        s.spectator
          ? el("p", {}, el("span", { text: "The imposter is " }), el("strong", { text: s.spectator.imposterName }), ".")
          : null,
        el("p", { class: "hint", text: `${s.votesCast} of ${s.activeCount} votes in.` }),
      );
    } else {
      spectator.hidden = true;
      picker.root.hidden = false;
      picker.update(s);
      if (s.youHaveVoted && !announcedLock) {
        announcedLock = true;
        ctx.announce("Your vote is locked in.");
      }
      if (!s.youHaveVoted) announcedLock = false;
    }

    clock.update(s.deadline, s.durationMs);
  };

  render(ctx.state());
  dd.add(ctx.subscribe(render));
  dd.raf(() => {
    const s = ctx.state();
    clock.update(s.deadline, s.durationMs);
  });
  ctx.announce(`Round ${ctx.state().round}. Discussion and voting has opened.`);
}

export function unmount(): void {
  d?.dispose();
  d = null;
}
