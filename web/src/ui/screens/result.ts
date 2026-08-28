import type { ScreenCtx, ViewState } from "../context.js";
import { avatar } from "../avatar.js";
import { Disposers, el, fill, setText } from "../dom.js";
import { playerList } from "../playerList.js";
import { spectatorPanel } from "../spectatorPanel.js";
import { stage } from "../stage.js";
import { tallyChart } from "../tally.js";
import { timer } from "../timer.js";
import { wordPanel } from "../wordPanel.js";

let d: Disposers | null = null;

/**
 * What the room is told about the player who just went
 * (MULTIPLE_IMPOSTERS.md, "Elimination Results").
 *
 * Under Hidden the answer is that there is no answer, and the copy has to say
 * so rather than defaulting to the reassuring half of the truth — a silent
 * "they were not the imposter" would be a lie the setting exists to prevent.
 *
 * Under Reveal with two imposters, catching one does not end the match, so the
 * line cannot promise a group win. It does not count the survivors either: the
 * doc leaves that implied by the public Imposters setting, and a count computed
 * here would be one more thing to get wrong across a reconnect.
 */
function verdict(s: ViewState, revealed: boolean, wasImposter: boolean): string {
  const many = s.settings.imposterCount > 1;
  if (!revealed) {
    return many
      ? "Which side they were on stays hidden. Somebody here is still holding a different word."
      : "Which side they were on stays hidden — that is how this match is set up.";
  }
  if (!wasImposter) {
    return many
      ? "They were not an imposter. Two people here are still holding a different word."
      : "They were not the imposter. Somebody here is still holding a different word.";
  }
  return many
    ? `That was an imposter. The group only wins once all ${s.settings.imposterCount} are out.`
    : "They were the imposter. The group wins.";
}

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
          avatar(ev.playerId, goneName),
          el("span", { text: `${goneName} was voted out.` }),
        ),
        el("p", { class: "muted", text: verdict(s, ev.alignmentRevealed, ev.wasImposter) }),
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
