import type { PlayerInfo } from "../../gen/verso/v1/game_pb.js";
import { el } from "./dom.js";
import { inTurnOrder } from "./turnOrder.js";
import type { ViewState } from "./context.js";

/** Everyone the tally is waiting on, in this round's drawing order. */
export function ballotSeats(s: ViewState): PlayerInfo[] {
  return inTurnOrder(s.players, s.turnOrder).filter((p) => p.connected && !p.eliminated);
}

/**
 * The ballot, seat by seat, ticked where a vote has landed.
 *
 * A bare "4 of 6 votes in" tells a room that it is waiting without telling it
 * whom to wait for, and a vote cannot be taken back once cast — so the only
 * thing left worth saying while the window runs is which seats still hold it
 * up. That is publishable: THAT a seat locked in is public, WHAT they chose
 * never is (DESIGN.md:65), and nothing here reads a candidate or a tally.
 */
export function ballotRoster(s: ViewState): HTMLElement {
  const seats = ballotSeats(s);
  // Own row: trust youHaveVoted as well as the roster flag. Both come from the
  // same cast — the unicast VoteAccepted and the presence broadcast behind it —
  // and a voter watching their own row still read "still to vote" for the gap
  // between the two frames.
  const cast = (p: PlayerInfo): boolean => p.voted || (p.id === s.selfId && s.youHaveVoted);
  const waiting = seats.filter((p) => !cast(p));
  return el(
    "div",
    { class: "ballot" },
    el(
      "ul",
      { class: "ballotlist" },
      ...seats.map((p) =>
        el(
          "li",
          { class: cast(p) ? "ballotrow ballotrow-in" : "ballotrow" },
          // The tick is drawn on a box that is always there, so a row does not
          // shift sideways at the moment its vote lands.
          el("span", { class: "ballotmark", "aria-hidden": "true", text: "✓" }),
          el("span", { class: "grow", text: p.name }),
          el("span", { class: "sr-only", text: cast(p) ? "— voted" : "— still to vote" }),
        ),
      ),
    ),
    el("p", {
      class: "hint",
      text:
        waiting.length === 0
          ? "Everyone has voted."
          : waiting.length === 1
            ? `Waiting on ${waiting[0]?.name ?? "one player"}.`
            : `Waiting on ${waiting.length} players.`,
    }),
  );
}
