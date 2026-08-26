import { MatchEndReason, WinnerSide } from "../../../gen/verso/v1/game_pb.js";
import type { ScreenCtx, ViewState } from "../context.js";
import { avatar } from "../avatar.js";
import { Disposers, el, fill, setText } from "../dom.js";
import { playerList } from "../playerList.js";
import { stage } from "../stage.js";

let d: Disposers | null = null;

const REASON: Record<number, string> = {
  [MatchEndReason.IMPOSTER_ELIMINATED]: "The group voted out the imposter.",
  [MatchEndReason.FINAL_ROUND_SURVIVED]: "The imposter survived the final round.",
  [MatchEndReason.TWO_PLAYERS_REMAIN]: "Only two active players were left.",
  [MatchEndReason.IMPOSTER_DISCONNECTED]: "The imposter left the room.",
  [MatchEndReason.ABANDONED]: "The match was abandoned.",
};

export function mount(root: HTMLElement, ctx: ScreenCtx): void {
  d = new Disposers();
  const dd = d;

  const roster = playerList("Players");
  const board = stage(ctx.canvas, "Final canvas");
  ctx.canvas.setInteractive(false);
  board.setLocked(true);
  dd.add(() => board.dispose());

  const banner = el("section", { class: "card winner" });
  const words = el("section", { class: "card" });
  const list = el("ul", { class: "reveals" });
  const revealCard = el(
    "section",
    { class: "card" },
    el("div", { class: "card-title", text: "Everybody's word" }),
    list,
  );

  const saveBtn = el("button", { type: "button", class: "btn", text: "Save canvas as PNG" }) as HTMLButtonElement;
  const againBtn = el("button", { type: "button", class: "btn btn-primary", text: "Play again" }) as HTMLButtonElement;

  dd.on(saveBtn, "click", () => {
    saveBtn.disabled = true;
    setText(saveBtn, "Rendering…");
    ctx.canvas.savePng().then(
      () => {
        saveBtn.disabled = false;
        setText(saveBtn, "Save canvas as PNG");
      },
      () => {
        saveBtn.disabled = false;
        setText(saveBtn, "Save canvas as PNG");
        ctx.toast("Could not save the canvas on this device.", "error");
      },
    );
  });
  dd.on(againBtn, "click", () => {
    againBtn.disabled = true;
    ctx.actions.rematch();
  });

  const actions = el("section", { class: "card row row-wrap" }, saveBtn, el("span", { class: "grow" }), againBtn);

  const main = el("div", { class: "col-main" }, banner, words, board.root, actions);
  const right = el("div", { class: "col-right stack" }, revealCard);
  const view = el("div", { class: "cols" }, roster.root, main, right);
  root.appendChild(view);
  dd.add(() => view.remove());

  let announced = false;

  const render = (s: ViewState) => {
    roster.update(s);
    againBtn.disabled = s.busy;

    const m = s.matchEnd;
    if (!m) {
      fill(banner, el("p", { class: "muted", text: "Waiting for the final result…" }));
      return;
    }

    const groupWon = m.winner === WinnerSide.GROUP;
    banner.className = `card winner ${groupWon ? "winner-group" : "winner-imposter"}`;
    fill(
      banner,
      el("div", { class: "winner-side", text: groupWon ? "The group wins" : "The imposter wins" }),
      el("p", { class: "winner-why", text: REASON[m.reason] ?? "The match is over." }),
      el("p", { class: "hint", text: `${m.roundsPlayed} round${m.roundsPlayed === 1 ? "" : "s"} played.` }),
    );

    const imposter = m.reveals.find((r) => r.wasImposter);
    fill(
      words,
      el("div", { class: "card-title", text: "The pair" }),
      el(
        "div",
        { class: "wordpair" },
        el(
          "div",
          { class: "wordbox" },
          el("div", { class: "wordbox-label", text: "Everyone else had" }),
          el("div", { class: "wordbox-value", text: m.commonWord }),
        ),
        el(
          "div",
          { class: "wordbox wordbox-imposter" },
          el("div", { class: "wordbox-label", text: "The imposter had" }),
          el("div", { class: "wordbox-value", text: m.imposterWord }),
        ),
      ),
      el("div", { style: "height:.6rem" }),
      el(
        "p",
        { class: "row" },
        avatar(m.imposterPlayerId, imposter?.name ?? "?"),
        el("span", {}, el("strong", { text: imposter?.name ?? "Someone" }), " was the imposter."),
      ),
    );

    fill(
      list,
      ...m.reveals.map((r) =>
        el(
          "li",
          { class: r.wasImposter ? "reveal-row reveal-row-imposter" : "reveal-row" },
          avatar(r.playerId, r.name),
          el(
            "div",
            { class: "grow" },
            el("div", { class: "pitem-name", text: r.name }),
            el("div", { class: "pitem-sub", text: r.wasImposter ? "imposter" : r.eliminated ? "eliminated" : "survived" }),
          ),
          el("span", { class: "reveal-word-cell", text: r.word }),
        )
      ),
    );

    if (!announced) {
      announced = true;
      ctx.announce(
        `${groupWon ? "The group wins." : "The imposter wins."} ` +
        `${imposter?.name ?? "The imposter"} had ${m.imposterWord}; everyone else had ${m.commonWord}.`,
      );
    }
  };

  render(ctx.state());
  dd.add(ctx.subscribe(render));
}

export function unmount(): void {
  d?.dispose();
  d = null;
}
