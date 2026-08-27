import type { ScreenCtx, ViewState } from "../context.js";
import { avatar } from "../avatar.js";
import { Disposers, el, fill, setText } from "../dom.js";
import { playerList } from "../playerList.js";
import { stage } from "../stage.js";
import { tallyChart } from "../tally.js";
import { timer } from "../timer.js";
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
  const privateCard = el("section", { class: "card spectator" });
  privateCard.hidden = true;

  const main = el("div", { class: "col-main" }, head, outcome, board.root);
  const right = el("div", { class: "col-right stack" }, chart.root, privateCard, word.root);
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
    const itWasMe = ev?.eliminated === true && ev.playerId === s.selfId;

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
          avatar(ev.playerId, goneName),
          el("span", { text: `${goneName} was voted out.` }),
        ),
        // The group is told only that a non-imposter went. DESIGN.md:65.
        el("p", {
          class: "muted",
          text: ev.wasImposter
            ? "They were the imposter. The group wins."
            : "They were not the imposter. Somebody here is still holding a different word.",
        }),
      );
    }

    // Private, and only ever for the player who was just eliminated.
    if (s.youAreEliminated && s.spectator) {
      privateCard.hidden = false;
      fill(
        privateCard,
        el("div", { class: "card-title", text: "For your eyes only" }),
        el("p", { text: itWasMe ? "You are out of the game." : "You are spectating." }),
        el("p", {}, el("span", { text: "The imposter is " }), el("strong", { text: s.spectator.imposterName }), "."),
        el("p", { class: "hint", text: "Keep it to yourself. You watch from here — you no longer draw or vote." }),
      );
    } else {
      privateCard.hidden = true;
    }

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
