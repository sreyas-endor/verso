import type { PlayerInfo } from "../../gen/verso/v1/game_pb.js";
import { avatar } from "./avatar.js";
import { el, fill } from "./dom.js";
import { markerGlyph } from "./paintress.js";
import { avatarColor } from "./palette.js";
import type { ViewState } from "./context.js";

export interface PlayerListOptions {
  /** Highlight the current artist and show their turn position. */
  showTurnQueue?: boolean;
  /** Show ready checkmarks (lobby only). */
  showReady?: boolean;
}

export interface PlayerListView {
  root: HTMLElement;
  update(s: ViewState): void;
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

function subtitle(p: PlayerInfo, s: ViewState, opts: PlayerListOptions): string {
  if (!p.connected) return "offline — seat held";
  if (p.eliminated) return "spectating";
  if (!opts.showTurnQueue) return "";
  if (p.id === s.artistId) return "drawing now";
  const at = s.turnOrder.indexOf(p.id);
  if (at < 0 || at <= s.turnIndex) return "";
  const away = at - s.turnIndex;
  return away === 1 ? "up next" : `in ${away} turns`;
}

/** Left column of every in-match screen, and the lobby roster. */
export function playerList(title: string, opts: PlayerListOptions = {}): PlayerListView {
  const list = el("ul", { class: "plist" });
  const heading = el("div", { class: "card-title", text: title });

  // The baton only exists where there is a turn order to hand off along.
  const baton = opts.showTurnQueue
    ? el("div", { class: "baton", "aria-hidden": "true", hidden: true }, markerGlyph())
    : null;
  const body = baton
    ? el("div", { class: "plist-rail" }, list, baton)
    : list;
  const root = el("section", { class: "card col-left" }, heading, body);

  let batonAt = "";

  /** Slides the marker to the drawing player's row. Measures only on change. */
  const moveBaton = (s: ViewState, rows: HTMLElement[]) => {
    if (!baton) return;
    const at = s.players.findIndex((p) => p.id === s.artistId);
    const row = at < 0 ? undefined : rows[at];
    if (!row) {
      baton.hidden = true;
      batonAt = "";
      return;
    }
    if (s.artistId === batonAt && !baton.hidden) return;

    const first = baton.hidden;
    baton.hidden = false;
    baton.style.setProperty("--nib", avatarColor(s.artistId));
    const y = row.offsetTop + row.offsetHeight / 2 - baton.offsetHeight / 2;
    baton.style.transform = `translateY(${Math.round(y)}px) rotate(-13deg)`;
    batonAt = s.artistId;
    // First placement should not fly in from the top of the list.
    if (first) requestAnimationFrame(() => baton.classList.add("baton-warm"));
  };

  return {
    root,
    update(s) {
      heading.textContent = `${title} · ${s.players.length}`;
      const items = s.players.map((p) => {
        const sub = subtitle(p, s, opts);
        const classes = ["pitem"];
        if (opts.showTurnQueue && p.id === s.artistId) classes.push("pitem-artist");
        if (p.eliminated) classes.push("pitem-out");
        if (!p.connected) classes.push("pitem-off");
        return el(
          "li",
          { class: classes.join(" ") },
          avatar(p.id, p.name),
          el(
            "div",
            { class: "grow" },
            el("div", { class: "pitem-name", text: p.name }),
            sub ? el("div", { class: "pitem-sub", text: sub }) : null,
          ),
          el("span", { class: "pitem-marks" }, ...marks(p, s, opts)),
        );
      });
      fill(list, ...items);
      moveBaton(s, items);
    },
  };
}
