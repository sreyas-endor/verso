import { MatchEndReason, WinnerSide } from "../../../gen/verso/v1/game_pb.js";
import type { ScreenCtx, ViewState } from "../context.js";
import { avatar } from "../avatar.js";
import { Disposers, el, fill, setText } from "../dom.js";
import { playerList } from "../playerList.js";

let d: Disposers | null = null;

/**
 * The headline under the winner. Keyed by reason, and by whether the match had
 * more than one imposter — "the group voted out the imposter" is the wrong
 * sentence for a match where they had to catch both.
 */
function reasonText(reason: MatchEndReason, many: boolean): string {
  switch (reason) {
    case MatchEndReason.IMPOSTER_ELIMINATED:
      return many ? "The group voted out every imposter." : "The group voted out the imposter.";
    case MatchEndReason.FINAL_ROUND_SURVIVED:
      return many
        ? "At least one imposter survived the final round."
        : "The imposter survived the final round.";
    case MatchEndReason.TWO_PLAYERS_REMAIN:
      return "Only two active players were left.";
    case MatchEndReason.IMPOSTER_DISCONNECTED:
      return many ? "An imposter left the room." : "The imposter left the room.";
    case MatchEndReason.ABANDONED:
      return "The match was abandoned.";
    default:
      return "The match is over.";
  }
}

/**
 * Backing-store size for a repainted round. The canvas is 4:3 on a logical
 * 1024x768 grid; 1024 wide keeps the promoted view sharp on a 2x display
 * without holding a full-resolution bitmap per round.
 */
const PROMOTED_W = 1024;
const THUMB_W = 320;

function paper(width: number, className: string): HTMLCanvasElement {
  const c = el("canvas", { class: className }) as HTMLCanvasElement;
  c.width = width;
  c.height = Math.round((width * 3) / 4);
  return c;
}

export function mount(root: HTMLElement, ctx: ScreenCtx): void {
  d = new Disposers();
  const dd = d;

  const roster = playerList("Players");

  // The live engine surface is not on this screen at all — every round,
  // including the last, is repainted from its archive. Disarm it anyway: the
  // match is over and nothing may add ink to it.
  ctx.canvas.setInteractive(false);

  const banner = el("section", { class: "card winner" });
  const words = el("section", { class: "card" });
  const list = el("ul", { class: "reveals" });
  const revealCard = el(
    "section",
    { class: "card" },
    el("div", { class: "card-title", text: "Everybody's word" }),
    list,
  );

  // ---- the filmstrip -------------------------------------------------------
  //
  // Every round wipes the canvas, so the match ends with one finished drawing
  // per round rather than a single accumulated sheet. One is shown full size
  // and the rest sit under it as thumbnails: the whole match stays visible at a
  // glance, and adding a fourth round costs a scroll rather than shrinking the
  // three already there.
  //
  // The promoted round is not "the current canvas" — every round, the last one
  // included, is repainted from its archived vectors, so there is one code path
  // and no special case for the round that happened to be live at the end.
  const promoted = paper(PROMOTED_W, "");
  const stageBox = el("div", { class: "stage stage-locked", role: "img" }, promoted);
  const stageCaption = el("p", { class: "filmstrip-now" });
  const strip = el("div", { class: "filmstrip", role: "tablist", "aria-label": "Rounds" });
  const board = el("section", { class: "filmstrip-wrap" }, stageBox, stageCaption, strip);

  const saveBtn = el("button", { type: "button", class: "btn", text: "Save canvas as PNG" }) as HTMLButtonElement;
  const againBtn = el("button", { type: "button", class: "btn btn-primary", text: "Play again" }) as HTMLButtonElement;

  // Which round the big canvas is showing. Null until the first render picks
  // the last round played.
  let shown: number | null = null;

  const setSaveLabel = (): void => {
    setText(saveBtn, shown === null ? "Save canvas as PNG" : `Save round ${shown} as PNG`);
  };

  dd.on(saveBtn, "click", () => {
    if (shown === null) return;
    const round = shown;
    saveBtn.disabled = true;
    setText(saveBtn, "Rendering…");
    ctx.canvas.savePngForRound(round).then(
      () => {
        saveBtn.disabled = false;
        setSaveLabel();
      },
      () => {
        saveBtn.disabled = false;
        setSaveLabel();
        ctx.toast("Could not save the canvas on this device.", "error");
      },
    );
  });
  dd.on(againBtn, "click", () => {
    againBtn.disabled = true;
    ctx.actions.rematch();
  });

  const actions = el("section", { class: "card row row-wrap" }, saveBtn, el("span", { class: "grow" }), againBtn);

  const main = el("div", { class: "col-main" }, banner, words, board, actions);
  const right = el("div", { class: "col-right stack" }, revealCard);
  const view = el("div", { class: "cols" }, roster.root, main, right);
  root.appendChild(view);
  dd.add(() => view.remove());

  let announced = false;
  // Thumbnails are painted once. Repainting them on every store notification
  // would redraw every stroke of every round for a screen that never changes.
  let stripBuiltFor = -1;

  /** Round numbers to show, oldest first. */
  const roundsOf = (s: ViewState): number[] => {
    const fromReveal = s.matchEnd?.rounds.map((r) => r.round) ?? [];
    if (fromReveal.length > 0) return fromReveal;
    // A match that ended before any round completed still has a canvas to show.
    return [...ctx.canvas.archivedRounds()];
  };

  const pairFor = (s: ViewState, round: number): string => {
    const rw = s.matchEnd?.rounds.find((r) => r.round === round);
    return rw ? `${rw.commonWord} / ${rw.imposterWord}` : "";
  };

  const promote = (s: ViewState, round: number): void => {
    shown = round;
    ctx.canvas.paintRound(round, promoted);
    const pair = pairFor(s, round);
    setText(stageCaption, pair === "" ? `Round ${round}` : `Round ${round} — ${pair}`);
    stageBox.setAttribute("aria-label", `The canvas from round ${round}`);
    setSaveLabel();
    for (const btn of strip.querySelectorAll<HTMLButtonElement>("[data-round]")) {
      btn.setAttribute("aria-selected", String(Number(btn.dataset["round"]) === round));
    }
  };

  const buildStrip = (s: ViewState, rounds: number[]): void => {
    fill(strip);
    // One round is not a strip — the big canvas already is the whole match.
    if (rounds.length < 2) {
      strip.hidden = true;
      return;
    }
    strip.hidden = false;
    for (const round of rounds) {
      const thumb = paper(THUMB_W, "");
      ctx.canvas.paintRound(round, thumb);
      const btn = el(
        "button",
        {
          type: "button",
          class: "filmstrip-thumb",
          role: "tab",
          "data-round": String(round),
          title: pairFor(s, round),
          "aria-label": `Round ${round}`,
        },
        el("span", { class: "stage" }, thumb),
        el("span", { class: "filmstrip-cap", text: `R${round}` }),
      );
      dd.on(btn, "click", () => promote(ctx.state(), round));
      strip.append(btn);
    }
  };

  const render = (s: ViewState) => {
    roster.update(s);
    againBtn.disabled = s.busy;

    const m = s.matchEnd;
    if (!m) {
      fill(banner, el("p", { class: "muted", text: "Waiting for the final result…" }));
      return;
    }

    // Every imposter, in the order the server pinned them. The reveal rows are
    // the source rather than imposter_player_ids so a name comes with each id;
    // the two are cross-checked by the server's own tests, not here.
    const imposters = m.reveals.filter((r) => r.wasImposter);
    const many = imposters.length > 1;

    const groupWon = m.winner === WinnerSide.GROUP;
    banner.className = `card winner ${groupWon ? "winner-group" : "winner-imposter"}`;
    fill(
      banner,
      el("div", {
        class: "winner-side",
        text: groupWon ? "The group wins" : many ? "The imposters win" : "The imposter wins",
      }),
      el("p", { class: "winner-why", text: reasonText(m.reason, many) }),
      el("p", { class: "hint", text: `${m.roundsPlayed} round${m.roundsPlayed === 1 ? "" : "s"} played.` }),
    );

    const rounds = roundsOf(s);
    if (stripBuiltFor !== rounds.length) {
      stripBuiltFor = rounds.length;
      buildStrip(s, rounds);
      // Open on the round the match ended in — the one everybody was just
      // looking at, and the one the result is about.
      promote(s, rounds[rounds.length - 1] ?? 1);
    }

    fill(
      words,
      el("div", {
        class: "card-title",
        text: m.rounds.length > 1 ? `The pairs — one per round` : "The pair",
      }),
      ...m.rounds.map((rw) =>
        el(
          "div",
          { class: rw.round === m.roundsPlayed ? "roundpair roundpair-final" : "roundpair" },
          el("span", { class: "roundpair-n", text: String(rw.round) }),
          el(
            "div",
            { class: "wordpair grow" },
            el(
              "div",
              { class: "wordbox" },
              el("div", { class: "wordbox-label", text: "Everyone else had" }),
              el("div", { class: "wordbox-value", text: rw.commonWord }),
            ),
            el(
              "div",
              { class: "wordbox wordbox-imposter" },
              el("div", { class: "wordbox-label", text: "The imposter had" }),
              el("div", { class: "wordbox-value", text: rw.imposterWord }),
            ),
          ),
        ),
      ),
      el("div", { style: "height:.6rem" }),
      // One row per imposter. They were pinned to those seats for the whole
      // match and were never told about each other, which is worth saying out
      // loud here — it is the thing everybody wants to argue about afterwards.
      el(
        "div",
        { class: "stack-tight" },
        ...(imposters.length === 0
          ? [el("p", { class: "muted", text: "Nobody was dealt the odd word." })]
          : imposters.map((r) =>
              el(
                "p",
                { class: "row" },
                avatar(r.playerId, r.avatar),
                el(
                  "span",
                  {},
                  el("strong", { text: r.name }),
                  many
                    ? ` was an imposter${m.rounds.length > 1 ? " in every round" : ""}.`
                    : ` was the imposter${m.rounds.length > 1 ? " in every round" : ""}.`,
                ),
              ),
            )),
        many
          ? el("p", {
              class: "hint",
              text: "They held the same word all match and were never told about each other.",
            })
          : null,
      ),
    );

    // One row per player, with a cell per round. A blank cell is a round that
    // player had already been eliminated out of — distinct from holding the
    // common word, which the server sends as the word itself.
    const multi = m.rounds.length > 1;
    fill(
      list,
      ...m.reveals.map((r) =>
        el(
          "li",
          { class: r.wasImposter ? "reveal-row reveal-row-imposter" : "reveal-row" },
          avatar(r.playerId, r.avatar),
          el(
            "div",
            { class: "grow" },
            el("div", { class: "pitem-name", text: r.name }),
            el("div", {
              class: "pitem-sub",
              text: r.wasImposter
                ? r.eliminated ? "imposter — caught" : "imposter"
                : r.eliminated ? "eliminated" : "survived",
            }),
          ),
          multi
            ? el(
                "span",
                { class: "reveal-words" },
                ...r.words.map((w, i) =>
                  el("span", {
                    class: w === "" ? "reveal-word-cell reveal-word-gone" : "reveal-word-cell",
                    title: `Round ${m.rounds[i]?.round ?? i + 1}`,
                    text: w === "" ? "—" : w,
                  }),
                ),
              )
            : el("span", { class: "reveal-word-cell", text: r.word }),
        )
      ),
    );

    if (!announced) {
      announced = true;
      const pairs = m.rounds
        .map((rw) => `round ${rw.round}, ${rw.imposterWord} against ${rw.commonWord}`)
        .join("; ");
      const names = imposters.map((r) => r.name);
      const who = names.length === 0
        ? "Nobody was the imposter."
        : names.length === 1
          ? `${names[0]} was the imposter.`
          : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]} were the imposters.`;
      ctx.announce(
        `${groupWon ? "The group wins." : many ? "The imposters win." : "The imposter wins."} ` +
        `${who} ` +
        (pairs === "" ? "" : `The pairs were: ${pairs}.`),
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
