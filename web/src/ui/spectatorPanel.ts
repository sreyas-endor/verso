import type { SpectatorInfo, SpectatorRound } from "../../gen/verso/v1/game_pb.js";
import type { ViewState } from "./context.js";
import { avatar } from "./avatar.js";
import { el, fill } from "./dom.js";

/**
 * The eliminated player's dossier, as one card
 * (MULTIPLE_IMPOSTERS.md, "Eliminated-player Spectator View").
 *
 * A spectator is out of the match for the rest of it, so this panel follows
 * them across every screen they still see — the word reveal, the drawing turns,
 * the discussion they cannot vote in, and the result. One component rather than
 * four copies of "the imposter is X", because with two imposters and a per-round
 * word table there is now enough here to get out of step.
 *
 * Everything it renders arrived on the wire in one `SpectatorInfo`, which the
 * server re-sends whole on every deal and every resync. There is nothing
 * accumulated on this side, so a reconnect cannot leave the panel half-built.
 *
 * `compact` drops the per-round table and keeps the imposter names, for the
 * screens where the panel is a sidebar next to a live canvas rather than the
 * thing being read.
 */
export interface SpectatorPanelView {
  root: HTMLElement;
  update(s: ViewState): void;
}

export function spectatorPanel(lead: string, compact = false): SpectatorPanelView {
  const root = el("section", { class: "card spectator" });
  root.hidden = true;

  let rendered = "";

  const render = (s: ViewState): void => {
    const info = s.spectator;
    if (!info || !s.youAreEliminated) {
      root.hidden = true;
      rendered = "";
      return;
    }
    root.hidden = false;

    // The dossier only ever grows, and only at a deal. Keying on the round
    // count plus the imposter count is enough to know nothing in it moved,
    // which keeps a per-frame render from rebuilding a table of every word in
    // the match on the same main thread that is rasterising ink.
    const key = `${info.imposters.length}:${info.rounds.length}:${lead}`;
    if (key === rendered) return;
    rendered = key;

    fill(
      root,
      el("div", { class: "card-title", text: "For your eyes only" }),
      el("p", { text: lead }),
      impostersRow(info),
      ...(compact ? [] : info.rounds.map((r) => roundBlock(r, s))),
      el("p", {
        class: "hint",
        text: compact
          ? "Keep it to yourself. You watch from here — you no longer draw or vote."
          : "Keep it to yourself. Saying any of this out loud ends the game for everybody.",
      }),
    );
  };

  return { root, update: render };
}

function impostersRow(info: SpectatorInfo): HTMLElement {
  const many = info.imposters.length > 1;
  return el(
    "div",
    { class: "dossier-imposters" },
    el("div", { class: "dossier-label", text: many ? "The imposters are" : "The imposter is" }),
    el(
      "div",
      { class: "row row-wrap" },
      ...info.imposters.map((im) =>
        el(
          "span",
          { class: "row dossier-who" },
          avatar(im.playerId, im.name, "sm"),
          el("strong", { text: im.name }),
        ),
      ),
    ),
  );
}

/**
 * One round: the pair, then every seat and the word it held. A seat that had
 * already been eliminated when this round was dealt is absent from
 * `assignments` rather than blank, so it simply does not appear.
 */
function roundBlock(r: SpectatorRound, s: ViewState): HTMLElement {
  const nameOf = (id: string): string => s.players.find((p) => p.id === id)?.name ?? "?";
  return el(
    "div",
    { class: "dossier-round" },
    el(
      "div",
      { class: "dossier-head" },
      el("span", { class: "dossier-n", text: `R${r.round}` }),
      el("span", { class: "dossier-pair" },
        el("span", { text: r.commonWord }),
        el("span", { class: "dossier-slash", text: " / " }),
        el("span", { class: "dossier-odd", text: r.imposterWord }),
      ),
    ),
    el(
      "ul",
      { class: "dossier-seats" },
      ...r.assignments.map((a) =>
        el(
          "li",
          { class: a.isImposter ? "dossier-seat dossier-seat-odd" : "dossier-seat" },
          el("span", { class: "dossier-seat-name", text: nameOf(a.playerId) }),
          el("span", { class: "dossier-seat-word", text: a.word }),
        ),
      ),
    ),
  );
}
