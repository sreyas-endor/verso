import { LIMITS, type ScreenCtx, type ViewState } from "../context.js";
import { Disposers, el, setText } from "../dom.js";
import { playerList } from "../playerList.js";
import { settingsPanel } from "../settingsPanel.js";

let d: Disposers | null = null;

/** Spells out *why* Start is unavailable — a greyed button explains nothing. */
function blockReason(s: ViewState): string {
  const seated = s.players.filter((p) => p.connected).length;
  if (seated < LIMITS.minPlayers) {
    const need = LIMITS.minPlayers - seated;
    return `Waiting for ${need} more player${need === 1 ? "" : "s"} — a match needs at least ${LIMITS.minPlayers}.`;
  }
  const notReady = s.players.filter((p) => p.connected && !p.ready && !p.isHost);
  if (notReady.length > 0) {
    const names = notReady.map((p) => p.name).join(", ");
    return `Waiting on ${names} to tick Ready.`;
  }
  if (!s.isHost) return "Only the host can start the match.";
  return "";
}

export function mount(root: HTMLElement, ctx: ScreenCtx): void {
  d = new Disposers();
  const dd = d;

  const roster = playerList("Players", {
    showReady: true,
    // No confirmation step: a lobby kick is not a ban, so the worst a misclick
    // costs is asking that player to rejoin. The toast is what makes a misclick
    // visible, since the row simply disappears.
    onKick: (p) => {
      ctx.actions.kickPlayer(p.id);
      ctx.toast(`Removed ${p.name}. They can rejoin with the room code.`);
    },
  });
  const settings = settingsPanel((next) => ctx.actions.updateSettings(next));
  dd.add(() => settings.dispose());

  const codeBox = el("div", { class: "roomcode", "aria-label": "Room code" });
  const urlBox = el("div", { class: "shareurl" });
  const copyBtn = el("button", { type: "button", class: "btn btn-sm", text: "Copy link" }) as HTMLButtonElement;

  dd.on(copyBtn, "click", () => {
    const url = ctx.state().joinUrl;
    const done = () => {
      setText(copyBtn, "Copied!");
      dd.timeout(1600, () => setText(copyBtn, "Copy link"));
    };
    // Clipboard is unavailable on plain-http LAN origins; fall back to select.
    navigator.clipboard?.writeText(url).then(done, () => {
      const range = document.createRange();
      range.selectNodeContents(urlBox);
      const sel = window.getSelection();
      sel?.removeAllRanges();
      sel?.addRange(range);
      setText(copyBtn, "Press ⌘C / Ctrl+C");
      dd.timeout(2600, () => setText(copyBtn, "Copy link"));
    });
  });

  const readyBtn = el("button", { type: "button", class: "btn btn-block", text: "I'm ready" }) as HTMLButtonElement;
  dd.on(readyBtn, "click", () => {
    const me = ctx.state().players.find((p) => p.id === ctx.state().selfId);
    ctx.actions.setReady(!(me?.ready ?? false));
  });

  const startBtn = el("button", { type: "button", class: "btn btn-primary btn-block", text: "Start match" }) as HTMLButtonElement;
  dd.on(startBtn, "click", () => ctx.actions.startMatch());
  const blocker = el("p", { class: "blocker", role: "status" });

  const share = el(
    "section",
    { class: "card" },
    el("div", { class: "card-title", text: "Invite your friends" }),
    codeBox,
    el("p", { class: "hint", style: "text-align:center", text: "Read the code out — or send the link." }),
    el("div", { style: "height:.6rem" }),
    urlBox,
    el("div", { style: "height:.6rem" }),
    el("div", { class: "row" }, el("div", { class: "grow" }), copyBtn),
  );

  const controls = el(
    "section",
    { class: "card" },
    readyBtn,
    el("div", { style: "height:.6rem" }),
    startBtn,
    el("div", { style: "height:.5rem" }),
    blocker,
  );

  const note = el("p", {
    class: "note",
    text: "No voice or text chat in here. Get everyone onto a call first — the whole game is the argument.",
  });

  const main = el("div", { class: "col-main" }, share, controls, note);
  const view = el("div", { class: "cols" }, roster.root, main, settings.root);
  root.appendChild(view);
  dd.add(() => view.remove());

  let announcedFull = false;

  const render = (s: ViewState) => {
    roster.update(s);
    settings.update(s.settings, s.isHost);
    setText(codeBox, s.roomCode || "—");
    setText(urlBox, s.joinUrl);

    const me = s.players.find((p) => p.id === s.selfId);
    const ready = me?.ready ?? false;
    setText(readyBtn, ready ? "Ready — tap to unready" : "I'm ready");
    readyBtn.className = ready ? "btn btn-good btn-block" : "btn btn-block";
    readyBtn.setAttribute("aria-pressed", String(ready));

    const reason = blockReason(s);
    startBtn.disabled = s.busy || !s.isHost || !s.canStart;
    startBtn.hidden = !s.isHost;
    blocker.hidden = reason === "";
    setText(blocker, reason);

    const full = s.players.length >= LIMITS.maxPlayers;
    if (full && !announcedFull) {
      announcedFull = true;
      ctx.toast(`Room is full at ${LIMITS.maxPlayers} players.`);
    } else if (!full) {
      announcedFull = false;
    }
  };

  render(ctx.state());
  dd.add(ctx.subscribe(render));
  ctx.announce(`Lobby. Room code ${ctx.state().roomCode.split("").join(" ")}.`);
}

export function unmount(): void {
  d?.dispose();
  d = null;
}
