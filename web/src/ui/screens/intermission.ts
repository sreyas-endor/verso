import type { ScreenCtx, ViewState } from "../context.js";
import { Disposers, el, fill, setText } from "../dom.js";
import { avatar } from "../avatar.js";
import { playerList } from "../playerList.js";
import { stage } from "../stage.js";
import { timer } from "../timer.js";

let d: Disposers | null = null;

export function mount(root: HTMLElement, ctx: ScreenCtx): void {
  d = new Disposers();
  const dd = d;
  const roster = playerList("Players", { showTurnQueue: true });
  const board = stage(ctx.canvas, "Shared drawing canvas, locked during the handoff");
  ctx.canvas.setInteractive(false);
  board.setLocked(true);
  dd.add(() => board.dispose());
  const badge = el("div", { class: "handoff-badge" });
  const title = el("h1", { class: "handoff-title" });
  const detail = el("div", { class: "handoff-detail" });
  const clock = timer("Time until the next activity");
  const overlay = el(
    "section",
    { class: "handoff-overlay", role: "status", "aria-live": "polite" },
    badge,
    title,
    detail,
    el("div", { class: "handoff-clock" }, clock.root),
  );
  const main = el("div", { class: "col-main" }, el("div", { class: "handoff-canvas" }, board.root, overlay));
  const view = el("div", { class: "cols" }, roster.root, main);
  root.appendChild(view);
  dd.add(() => view.remove());

  let announced = "";
  const render = (s: ViewState) => {
    roster.update(s);
    const artist = s.players.find((p) => p.id === s.nextArtistId);
    if (artist) {
      setText(badge, "NEXT UP");
      setText(title, `${artist.name} is drawing next`);
      fill(detail, avatar(artist.id, artist.name, "lg"), el("span", { text: "Get ready for their clue." }));
    } else {
      setText(badge, "ROUND COMPLETE");
      setText(title, "Voting opens next");
      setText(detail, "Look over the canvas and decide who feels out of place.");
    }
    clock.update(s.deadline, s.durationMs);
    const message = artist ? `${artist.name} draws next.` : "Voting opens next.";
    if (message !== announced) {
      announced = message;
      ctx.announce(message);
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
