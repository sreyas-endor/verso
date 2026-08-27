import type { PlayerInfo } from "../../gen/verso/v1/game_pb.js";
import { avatar } from "./avatar.js";
import { el, fill } from "./dom.js";
import { markerGlyph } from "./paintress.js";
import { avatarColor } from "./palette.js";
import { inTurnOrder } from "./turnOrder.js";
import type { ViewState } from "./context.js";

export interface PlayerListOptions {
  /**
   * Order the roster by this round's turn order and draw the turn track. Every
   * screen with a round in progress asks for it, the vote and the result
   * included: the rail is how the room keeps hold of which drawing was which
   * (DESIGN.md:60), so it must not disappear at the moment they argue about it.
   */
  showTurnQueue?: boolean;
  /** Show ready checkmarks (lobby only). */
  showReady?: boolean;
  /**
   * Render a remove button on every row but the viewer's own, for the host
   * only. Lobby only: the server refuses a kick in any other phase, so
   * offering the control there would be a button that cannot work.
   */
  onKick?: (player: PlayerInfo) => void;
}

export interface PlayerListView {
  root: HTMLElement;
  update(s: ViewState): void;
}

/** Where one player stands in this round's running order. */
type TurnState = "drew" | "now" | "next" | "waiting" | "offTrack";

interface Seat {
  readonly player: PlayerInfo;
  readonly state: TurnState;
  /** Turns that must finish before this player's own starts. */
  readonly away: number;
}

/**
 * Splits the roster into the players this round's turn order covers — in that
 * order — and everyone else, after them.
 *
 * Two separate things are decided here and only one of them is the track.
 *
 * The ORDER follows `turnOrder` wherever there is one, which is the whole round
 * including the vote (DESIGN.md:60) — see inTurnOrder. Nothing may reorder the
 * roster under a room that is arguing about "the third drawing".
 *
 * The TRACK — nodes, subtitles, the pen — says where the round has got to. It
 * lasts as long as the order does, because a rail that vanishes the moment the
 * last turn ends reads as the column being replaced rather than as the round
 * moving on. Once the server stops naming an artist the round has drawn its
 * last, so the cursor sits past the end: every node filled, and no pen, since
 * there is nobody left to hold it.
 */
function order(s: ViewState, opts: PlayerListOptions): { track: Seat[]; rest: Seat[] } {
  const flat = (): { track: Seat[]; rest: Seat[] } => ({
    track: [],
    rest: inTurnOrder(s.players, s.turnOrder).map((player) => ({
      player,
      state: "offTrack",
      away: 0,
    })),
  });
  if (opts.showTurnQueue !== true || s.turnOrder.length === 0) return flat();

  const focus = s.artistId !== "" ? s.artistId : s.nextArtistId;

  // Trust the id the server named over the index that came with it: the index
  // arrives on TurnStarted only, so after a bare PhaseChanged it is one turn
  // stale, while the id is always current. With no id named at all — the whole
  // discussion and result — nothing is pending, so the cursor goes off the end
  // and the track reads as finished.
  const found = focus === "" ? -1 : s.turnOrder.indexOf(focus);
  const cursor = found >= 0 ? found : focus === "" ? s.turnOrder.length : s.turnIndex;
  const live = s.artistId !== "";

  const unplaced = new Map(s.players.map((p) => [p.id, p]));
  const track: Seat[] = [];
  s.turnOrder.forEach((id, at) => {
    const player = unplaced.get(id);
    if (player === undefined) return;
    unplaced.delete(id);
    const state: TurnState =
      at < cursor ? "drew" : at === cursor ? (live ? "now" : "next") : "waiting";
    track.push({ player, state, away: at - cursor });
  });
  if (track.length === 0) return flat();

  // Eliminated players, and anyone seated after the round began: no node, and
  // below the track rather than interrupting it.
  const rest = s.players
    .filter((p) => unplaced.has(p.id))
    .map((player): Seat => ({ player, state: "offTrack", away: 0 }));
  return { track, rest };
}

function marks(p: PlayerInfo, s: ViewState, opts: PlayerListOptions): HTMLElement[] {
  const out: HTMLElement[] = [];
  if (p.id === s.selfId) out.push(el("span", { class: "badge badge-you", text: "YOU" }));
  if (p.isHost) out.push(el("span", { class: "badge badge-host", text: "HOST" }));
  if (opts.showReady && p.ready && !p.isHost) {
    out.push(el("span", { class: "badge badge-ready", text: "READY" }));
  }
  if (p.eliminated) out.push(el("span", { class: "badge badge-out", text: "OUT" }));
  return out;
}

function subtitle(seat: Seat, opts: PlayerListOptions): string {
  const p = seat.player;
  if (!p.connected) return "offline — seat held";
  if (p.eliminated) return "spectating";
  if (!opts.showTurnQueue) return "";
  switch (seat.state) {
    case "now":
      return "drawing now";
    case "next":
      return "drawing next";
    case "drew":
      // Kept to one word: the column is narrow, and a row carrying HOST or YOU
      // as well ellipsises anything longer. The filled node carries the rest.
      return "drew";
    case "waiting":
      // One turn away reads the same whether that turn is running or still in
      // its handoff: one turn happens, then yours.
      return seat.away === 1 ? "up next" : `in ${seat.away} turns`;
    case "offTrack":
      return "";
  }
}

function row(seat: Seat, s: ViewState, opts: PlayerListOptions): HTMLElement {
  const p = seat.player;
  const sub = subtitle(seat, opts);
  const classes = ["pitem"];
  if (seat.state === "now") classes.push("pitem-artist");
  if (seat.state === "next") classes.push("pitem-next");
  if (seat.state === "drew") classes.push("pitem-drew");
  if (p.eliminated) classes.push("pitem-out");
  if (!p.connected) classes.push("pitem-off");
  return el(
    "li",
    { class: classes.join(" ") },
    seat.state === "offTrack" ? null : el("span", { class: "pnode", "aria-hidden": "true" }),
    avatar(p.id, p.name),
    el(
      "div",
      { class: "grow" },
      el("div", { class: "pitem-name", text: p.name }),
      sub ? el("div", { class: "pitem-sub", text: sub }) : null,
    ),
    el("span", { class: "pitem-marks" }, ...marks(p, s, opts)),
    kickButton(p, s, opts),
  );
}

/**
 * The host's remove control. Absent for everyone else, and for the host's own
 * row: a host leaves by closing the tab, which migrates the role, and the
 * server refuses a self-kick anyway.
 */
function kickButton(p: PlayerInfo, s: ViewState, opts: PlayerListOptions): HTMLElement | null {
  if (!opts.onKick || !s.isHost || p.id === s.selfId) return null;
  const b = el("button", {
    class: "pitem-kick",
    type: "button",
    title: `Remove ${p.name}`,
    "aria-label": `Remove ${p.name} from the room`,
    text: "\u00d7",
  });
  b.addEventListener("click", () => opts.onKick?.(p));
  return b;
}

/** Left column of every in-match screen, and the lobby roster. */
export function playerList(title: string, opts: PlayerListOptions = {}): PlayerListView {
  const heading = el("div", { class: "card-title", text: title });
  const track = el("ul", { class: "plist plist-track" });
  const rest = el("ul", { class: "plist plist-rest" });

  // The pen exists only where there is a track to travel along. It parks on the
  // current node, which is why that node hides beneath it.
  const baton = opts.showTurnQueue
    ? el("div", { class: "baton", "aria-hidden": "true", hidden: true }, markerGlyph())
    : null;
  const body = baton
    ? el("div", { class: "plist-rail" }, track, rest, baton)
    : el("div", {}, track, rest);
  const root = el("section", { class: "card col-left" }, heading, body);

  let batonAt = "";

  /** Slides the pen to the penned row. Measures only when the row changes. */
  const moveBaton = (id: string, rows: HTMLElement[], at: number) => {
    if (!baton) return;
    const target = at < 0 ? undefined : rows[at];
    if (!target || id === "") {
      baton.hidden = true;
      batonAt = "";
      return;
    }
    if (id === batonAt && !baton.hidden) return;

    const first = baton.hidden;
    baton.hidden = false;
    baton.style.setProperty("--nib", avatarColor(id));
    const y = target.offsetTop + target.offsetHeight / 2 - baton.offsetHeight / 2;
    baton.style.transform = `translateY(${Math.round(y)}px) rotate(-13deg)`;
    batonAt = id;
    // First placement should not fly in from the top of the list.
    if (first) requestAnimationFrame(() => baton.classList.add("baton-warm"));
  };

  return {
    root,
    update(s) {
      heading.textContent = `${title} · ${s.players.length}`;
      const { track: onTrack, rest: offTrack } = order(s, opts);

      const trackRows = onTrack.map((seat) => row(seat, s, opts));
      fill(track, ...trackRows);
      fill(rest, ...offTrack.map((seat) => row(seat, s, opts)));
      track.hidden = trackRows.length === 0;
      rest.hidden = offTrack.length === 0;

      const pennedAt = onTrack.findIndex((seat) => seat.state === "now" || seat.state === "next");
      moveBaton(pennedAt < 0 ? "" : onTrack[pennedAt]?.player.id ?? "", trackRows, pennedAt);
    },
  };
}
