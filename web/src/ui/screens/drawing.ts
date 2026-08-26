import type { ScreenCtx, ViewState } from "../context.js";
import { Disposers, el, fill, setText } from "../dom.js";
import { avatar } from "../avatar.js";
import { paintress } from "../paintress.js";
import { avatarColor } from "../palette.js";
import { playerList } from "../playerList.js";
import { stage } from "../stage.js";
import { timer } from "../timer.js";
import { tools } from "../tools.js";
import { wordPanel } from "../wordPanel.js";

let d: Disposers | null = null;

function turnsAway(s: ViewState): number {
  const at = s.turnOrder.indexOf(s.selfId);
  if (at < 0 || at <= s.turnIndex) return -1;
  return at - s.turnIndex;
}

export function mount(root: HTMLElement, ctx: ScreenCtx): void {
  d = new Disposers();
  const dd = d;

  const roster = playerList("Players", { showTurnQueue: true });
  const board = stage(ctx.canvas, "Shared drawing canvas");
  // The canvas handle outlives this screen — leave input off on the way out.
  dd.add(() => ctx.canvas.setInteractive(false));
  dd.add(() => board.dispose());
  const pens = tools(ctx.canvas);
  dd.add(() => pens.dispose());
  // Decoration, not a status light: she paints at the same tempo all turn, and
  // stands on the floor strip below the stage rather than over the canvas.
  const paint = paintress();
  const word = wordPanel(true);
  const clock = timer("Time left in this turn");

  const kicker = el("div", { class: "phasehead-kicker" });
  const title = el("div", { class: "phasehead-title" });
  const head = el(
    "section",
    { class: "card phasehead" },
    el("div", { class: "phasehead-main" }, kicker, title),
    clock.root,
  );

  const statusTitle = el("div", { class: "card-title" });
  const statusBody = el("div", {});
  const status = el("section", { class: "card" }, statusTitle, statusBody);

  const main = el("div", { class: "col-main" }, head, board.root, paint.root);
  const right = el("div", { class: "col-right stack" }, status, pens.root, word.root);
  const view = el("div", { class: "cols" }, roster.root, main, right);
  root.appendChild(view);
  dd.add(() => view.remove());

  let lastArtist = "";
  let lastRound = -1;

  const render = (s: ViewState) => {
    roster.update(s);
    word.update(s.word);

    const artist = s.players.find((p) => p.id === s.artistId);
    const artistName = artist?.name ?? "Someone";
    const iAmArtist = s.artistId === s.selfId && !s.youAreEliminated;
    if (s.artistId) paint.setInk(avatarColor(s.artistId));

    setText(kicker, `Round ${s.round} of ${s.totalRounds} · turn ${s.turnIndex + 1} of ${s.turnOrder.length}`);
    fill(
      title,
      iAmArtist ? el("em", { text: "Your turn — draw!" }) : el("em", { text: artistName }),
      iAmArtist ? "" : " is drawing",
    );

    pens.setEnabled(iAmArtist);
    ctx.canvas.setInteractive(iAmArtist);
    board.setLocked(!iAmArtist);

    if (s.youAreEliminated) {
      status.className = "card spectator";
      setText(statusTitle, "Spectating");
      fill(
        statusBody,
        el("p", { text: "You were eliminated. You can still watch every stroke, but you no longer draw or vote." }),
        s.spectator
          ? el("p", {}, el("span", { text: "The imposter is " }), el("strong", { text: s.spectator.imposterName }), ".")
          : null,
      );
      pens.root.hidden = true;
    } else {
      status.className = "card";
      pens.root.hidden = false;
      if (iAmArtist) {
        setText(statusTitle, "You are drawing");
        fill(
          statusBody,
          el("p", { text: "Freehand clues only. No letters, numbers, arrows or symbols." }),
          el("p", { class: "hint", text: "There is no eraser and no undo — every mark stays on the canvas." }),
        );
      } else {
        const away = turnsAway(s);
        setText(statusTitle, "Watching");
        fill(
          statusBody,
          el("p", { class: "row" }, avatar(s.artistId, artistName, "sm"), el("span", { text: `${artistName} is drawing.` })),
          away === 1
            ? el("p", { class: "badge badge-you", text: "YOUR TURN IS NEXT" })
            : away > 1
              ? el("p", { class: "hint", text: `Your turn is in ${away} turns.` })
              : el("p", { class: "hint", text: "You have already drawn this round." }),
        );
      }
    }

    clock.update(s.deadline, s.durationMs);

    if (s.artistId !== lastArtist || s.round !== lastRound) {
      lastArtist = s.artistId;
      lastRound = s.round;
      ctx.announce(
        iAmArtist
          ? `Round ${s.round}. Your turn to draw.`
          : `Round ${s.round}. ${artistName} is drawing.`,
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
