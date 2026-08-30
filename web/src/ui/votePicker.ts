import type { VoteChoice, ViewState } from "./context.js";
import { avatar } from "./avatar.js";
import { ballotRoster, ballotSeats } from "./ballot.js";
import { Disposers, el, fill, setText } from "./dom.js";

export interface VotePickerView {
  root: HTMLElement;
  update(s: ViewState): void;
  dispose(): void;
}

function sameChoice(a: VoteChoice | null, b: VoteChoice | null): boolean {
  if (a === null || b === null) return a === b;
  if (a.case === "skip") return b.case === "skip";
  return b.case === "candidateId" && a.value === b.value;
}

/**
 * A vote is anonymous and irreversible (DESIGN.md:49), so a single mis-tap is
 * unrecoverable. Selecting a card only arms the vote; a separate confirm sends
 * it, and the warning is on screen before the confirming tap, not after.
 * Arming a different card re-arms `pending` directly (no need to back out of
 * one choice before making another), so the only way out of the confirm step
 * is forward, into "Lock it in".
 */
export function votePicker(onCast: (choice: VoteChoice) => void): VotePickerView {
  const d = new Disposers();
  let pending: VoteChoice | null = null;

  const grid = el("div", { class: "votegrid", role: "group", "aria-label": "Who is the imposter?" });
  const confirmText = el("p", {});
  const confirmBtn = el("button", { type: "button", class: "btn btn-danger btn-sm", text: "Lock it in" }) as HTMLButtonElement;
  const confirmBox = el("div", { class: "confirmbox" }, confirmText, el("div", { class: "row" }, confirmBtn));
  confirmBox.hidden = true;

  const lead = el("p", { class: "hint" });
  const locked = el("div", {});
  locked.hidden = true;

  d.on(confirmBtn, "click", () => {
    if (!pending) return;
    onCast(pending);
    pending = null;
  });

  function paint(s: ViewState): void {
    const done = s.youHaveVoted;
    // The ballot is in this round's drawing order, not seat order
    // (DESIGN.md:60). A voter is picking one of the drawings they have just
    // watched appear, and the thing they remember about it is when it appeared;
    // the roster beside them is in the same order, so a card and a row can be
    // matched up by eye.
    const candidates = ballotSeats(s);

    grid.hidden = done;
    confirmBox.hidden = done || pending === null;
    locked.hidden = !done;
    lead.hidden = done;

    if (done) {
      const mine = s.yourVote;
      const who = mine === null
        ? "your vote"
        : mine.case === "skip"
          ? "Skip"
          : s.players.find((p) => p.id === mine.value)?.name ?? "that player";
      fill(
        locked,
        el("p", {}, el("span", { class: "badge badge-ready", text: "VOTE LOCKED" })),
        el("p", { text: mine === null ? "Your vote is in." : `You voted for ${who}. That cannot be changed.` }),
        ballotRoster(s),
        el("p", {
          class: "hint",
          text: "Nobody will ever be told who voted for whom.",
        }),
      );
      return;
    }

    setText(lead, "One vote each. Anonymous, and it cannot be taken back.");

    const cards = candidates.map((p) => {
      const armed = sameChoice(pending, { case: "candidateId", value: p.id });
      // The tick says only THAT this seat has locked in (DESIGN.md:65). It sits
      // on the card rather than only in the roster so the two questions a voter
      // is holding at once — who to vote for, and who the room is still waiting
      // on — are answered in the same place. It never disables the card: having
      // voted has no bearing on being a candidate.
      const b = el(
        "button",
        {
          type: "button",
          class: p.voted ? "votecard votecard-in" : "votecard",
          "aria-pressed": String(armed),
          "aria-label": `${p.name}${p.id === s.selfId ? ", you" : ""}, ${p.voted ? "voted" : "still to vote"}`,
        },
        avatar(p.id, p.avatar),
        el("span", { class: "grow", text: p.name }),
        p.id === s.selfId ? el("span", { class: "badge badge-you", text: "YOU" }) : null,
        el("span", { class: "ballotmark", "aria-hidden": "true", text: "✓" }),
      ) as HTMLButtonElement;
      b.addEventListener("click", () => {
        pending = { case: "candidateId", value: p.id };
        paint(s);
      });
      return b;
    });

    const skipArmed = pending?.case === "skip";
    const skip = el(
      "button",
      { type: "button", class: "votecard votecard-skip", "aria-pressed": String(skipArmed) },
      el("span", { class: "avatar", "aria-hidden": "true", text: "—" }),
      el("span", { class: "grow", text: "Skip — eliminate nobody" }),
    ) as HTMLButtonElement;
    skip.addEventListener("click", () => {
      pending = { case: "skip" };
      paint(s);
    });

    fill(grid, ...cards, skip);

    const armed = pending;
    if (armed) {
      if (armed.case === "skip") {
        setText(confirmText, "Lock in Skip? Your vote is anonymous and final.");
      } else {
        const name = s.players.find((p) => p.id === armed.value)?.name ?? "that player";
        setText(confirmText, `Vote to eliminate ${name}? Your vote is anonymous and final.`);
      }
    }
  }

  const counter = el("p", { class: "hint", role: "status" });

  const root = el(
    "section",
    { class: "card col-right" },
    el("div", { class: "card-title", text: "Your vote" }),
    lead,
    grid,
    confirmBox,
    locked,
    counter,
  );

  return {
    root,
    update(s) {
      paint(s);
      setText(counter, `${s.votesCast} of ${s.activeCount} votes in.`);
    },
    dispose() {
      d.dispose();
    },
  };
}
