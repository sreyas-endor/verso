import type { VoteTally } from "../../gen/verso/v1/game_pb.js";
import type { ViewState } from "./context.js";
import { NO_AVATAR, avatar } from "./avatar.js";
import { el, fill } from "./dom.js";

export interface TallyView {
  root: HTMLElement;
  update(s: ViewState): void;
}

function row(
  key: string,
  label: HTMLElement,
  votes: number,
  max: number,
  cls: string,
): HTMLElement {
  const pct = max > 0 ? Math.round((votes / max) * 100) : 0;
  return el(
    "div",
    { class: `tallyrow ${cls}`, "data-key": key },
    el("div", { class: "tallyrow-label" }, label),
    el("div", { class: "tallyrow-count", text: `${votes}` }),
    el("div", { class: "tallybar" }, el("i", { style: `width:${pct}%` })),
  );
}

/**
 * Aggregate totals only. Who voted for whom is never on the wire and never
 * rendered — DESIGN.md:56.
 */
export function tallyChart(): TallyView {
  const body = el("div", { class: "tally" });
  const caption = el("p", { class: "hint" });
  const root = el(
    "section",
    { class: "card" },
    el("div", { class: "card-title", text: "The vote" }),
    body,
    caption,
  );

  return {
    root,
    update(s) {
      const t: VoteTally | null = s.tally;
      if (!t) {
        fill(body, el("p", { class: "muted", text: "Counting…" }));
        caption.textContent = "";
        return;
      }
      const max = Math.max(1, t.skipCount, ...t.counts.map((c) => c.votes));
      const eliminatedId = s.elimination?.eliminated ? s.elimination.playerId : "";

      const skipLabel = () =>
        el(
          "span",
          { class: "row" },
          el("span", { class: "avatar avatar-sm", "aria-hidden": "true", style: "background:var(--ink-3)", text: "—" }),
          el("span", { text: "Skip" }),
        );

      // Skip is sorted in with the candidates rather than pinned to the bottom:
      // it competes for first place, and a Skip that outpolls everybody is the
      // reason nobody was eliminated. Pinning it last would hide that.
      const rows = [
        ...t.counts.map((c) => ({ key: c.candidateId, votes: c.votes, skip: false })),
        { key: "__skip", votes: t.skipCount, skip: true },
      ]
        .sort((a, b) => b.votes - a.votes)
        .map((r) => {
          if (r.skip) return row(r.key, skipLabel(), r.votes, max, "tallyrow-skip");
          const p = s.players.find((x) => x.id === r.key);
          const name = p?.name ?? "Unknown";
          const label = el(
            "span",
            { class: "row" },
            avatar(r.key, p?.avatar ?? NO_AVATAR, "sm"),
            el("span", { text: name }),
            r.key === eliminatedId ? el("span", { class: "badge badge-out", text: "OUT" }) : null,
          );
          return row(r.key, label, r.votes, max, r.key === eliminatedId ? "tallyrow-win" : "");
        });

      fill(body, ...rows);
      caption.textContent =
        "Most votes wins, and Skip is on the ballot. A tie eliminates nobody, and players who never voted count for neither side.";
    },
  };
}
