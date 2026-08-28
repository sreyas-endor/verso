import type { ScreenCtx, ViewState } from "../context.js";
import { NO_AVATAR, avatar } from "../avatar.js";
import { Disposers, el, fill, setText } from "../dom.js";
import { playerList } from "../playerList.js";
import { spectatorPanel } from "../spectatorPanel.js";
import { stage } from "../stage.js";
import { tallyChart } from "../tally.js";
import { timer } from "../timer.js";
import { verdict } from "../verdict.js";
import { wordPanel } from "../wordPanel.js";

let d: Disposers | null = null;

export function mount(root: HTMLElement, ctx: ScreenCtx): void {
  d = new Disposers();
  const dd = d;

  const roster = playerList("Players", { showTurnQueue: true });
  const board = stage(ctx.canvas, "Canvas so far");
  ctx.canvas.setInteractive(false);
  board.setLocked(true);
  dd.add(() => board.dispose());

  const chart = tallyChart();
  const word = wordPanel();
  // What actually comes next is the new round's word reveal, not a drawing
  // turn — the round deals a fresh pair on a blank canvas before anybody draws.
  const clock = timer("Time until the next word");
  // Only on the rounds that have a next one. On the final round this screen is
  // followed by the reveal, and promising a new word would be a lie.
  const nextNote = el("p", { class: "hint" });

  const kicker = el("div", { class: "phasehead-kicker" });
  const title = el("div", { class: "phasehead-title" });
  const head = el(
    "section",
    { class: "card phasehead" },
    el("div", { class: "phasehead-main" }, kicker, title, nextNote),
    clock.root,
  );

  const outcome = el("section", { class: "card" });
  const dossier = spectatorPanel("You are out of the game. Here is the match as it really is.");

  const main = el("div", { class: "col-main" }, head, outcome, board.root);
  const right = el("div", { class: "col-right stack" }, chart.root, dossier.root, word.root);
  const view = el("div", { class: "cols" }, roster.root, main, right);
  root.appendChild(view);
  dd.add(() => view.remove());

  let announced = "";

  const render = (s: ViewState) => {
    roster.update(s);
    word.update(s.word);
    chart.update(s);
    setText(kicker, `Round ${s.round} of ${s.totalRounds} · result`);
    setText(
      nextNote,
      s.round < s.totalRounds
        ? `Round ${s.round + 1} deals a new word on a blank canvas.`
        : "",
    );

    const ev = s.elimination;
    const gone = ev?.eliminated ? s.players.find((p) => p.id === ev.playerId) : undefined;
    const goneName = gone?.name ?? "";

    if (!ev || !ev.eliminated) {
      fill(title, el("em", { text: "Nobody was eliminated" }));
      fill(
        outcome,
        el("div", { class: "card-title", text: "Tie for most votes" }),
        el("p", { text: "No single player had the most votes, so everyone stays in and the next round begins." }),
      );
    } else {
      fill(title, el("em", { text: goneName }), " was eliminated");
      fill(
        outcome,
        el("div", { class: "card-title", text: "Eliminated" }),
        el(
          "p",
          { class: "row" },
          avatar(ev.playerId, gone?.avatar ?? NO_AVATAR),
          el("span", { text: `${goneName} was voted out.` }),
        ),
        el("p", { class: "muted", text: verdict(s.settings.imposterCount, ev.alignmentRevealed, ev.wasImposter) }),
      );
    }

    // Private, and only ever for the player who was just eliminated.
    dossier.update(s);

    clock.update(s.deadline, s.durationMs);

    const key = `${s.round}:${ev?.playerId ?? ""}:${ev?.eliminated ?? false}`;
    if (key !== announced) {
      announced = key;
      ctx.announce(
        !ev || !ev.eliminated
          ? "The vote was tied. Nobody was eliminated."
          : `${goneName} was eliminated.`,
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
