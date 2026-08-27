import type { ConnectionStatus, SoundToggle, ViewState } from "./context.js";
import { Disposers, el, fill, setText, toggle } from "./dom.js";

export interface Chrome {
  /** Screens are mounted into this element by the router. */
  screenRoot: HTMLElement;
  render(s: ViewState): void;
  announce(message: string): void;
  toast(message: string, kind?: "info" | "error"): void;
  destroy(): void;
}

const CONN_LABEL: Record<ConnectionStatus, string> = {
  offline: "Offline",
  connecting: "Connecting",
  connected: "Connected",
  reconnecting: "Reconnecting",
  dropped: "Disconnected",
};

/**
 * The persistent frame: room code, headcount, connection state, a calm
 * reconnect banner (never a modal — the match carries on without you), a toast
 * stack for server errors, the mute control for the turn cues, and the polite
 * live region every screen announces phase and turn changes through.
 *
 * `sound` is optional: with no sound layer wired the control is simply absent,
 * rather than present and lying.
 */
export function createChrome(root: HTMLElement, sound?: SoundToggle): Chrome {
  const d = new Disposers();

  const code = el("span", { class: "appbar-code" });
  const meta = el("span", { class: "appbar-meta" });
  const connDot = el("span", { class: "conn-dot" });
  const connText = el("span", {});
  const conn = el("span", { class: "conn conn-connecting" }, connDot, connText);

  // Mute, not volume: the cues are already levelled against each other, and a
  // slider is one more thing to mis-set before a turn starts.
  const soundBtn = el(
    "button",
    { class: "appbar-sound", type: "button" },
    el("span", { class: "appbar-sound-glyph", "aria-hidden": "true", text: "\u266a" }),
    el("span", { class: "sr-only", text: "Turn sounds" }),
  );
  const syncSound = () => {
    const on = sound?.enabled() ?? false;
    soundBtn.setAttribute("aria-pressed", on ? "true" : "false");
    soundBtn.title = on ? "Turn sounds on — click to mute" : "Turn sounds muted — click to unmute";
    toggle(soundBtn, "appbar-sound-off", !on);
  };
  if (sound === undefined) {
    soundBtn.hidden = true;
  } else {
    syncSound();
    d.on(soundBtn, "click", () => {
      sound.setEnabled(!sound.enabled());
      syncSound();
    });
  }

  const bar = el(
    "header",
    { class: "appbar" },
    el("span", { class: "appbar-brand" }, el("span", { text: "Ver" }), "so"),
    code,
    meta,
    el("span", { class: "grow" }),
    soundBtn,
    conn,
  );

  const banner = el("div", { class: "banner", role: "status" });
  banner.hidden = true;

  const screenRoot = el("main", { class: "screen" });
  const toasts = el("div", { class: "toasts" });
  const live = el("div", { class: "sr-only", "aria-live": "polite", "aria-atomic": "true" });

  const app = el("div", { class: "app" }, bar, banner, screenRoot, live);
  root.appendChild(app);
  root.appendChild(toasts);

  let lastAnnounced = "";

  return {
    screenRoot,

    render(s) {
      code.textContent = s.roomCode || "—";
      code.hidden = s.roomCode === "";
      const seated = s.players.length;
      const online = s.players.filter((p) => p.connected).length;
      meta.textContent = seated === 0 ? "" : online === seated
        ? `${seated} player${seated === 1 ? "" : "s"}`
        : `${online}/${seated} online`;

      conn.className = `conn conn-${s.connection}`;
      setText(connText, CONN_LABEL[s.connection]);

      if (s.connection === "reconnecting") {
        banner.hidden = false;
        banner.className = "banner";
        const left = s.graceSeconds > 0 ? ` Your seat is held for ${s.graceSeconds}s.` : "";
        setText(banner, `⟳ Reconnecting — the match is carrying on without you.${left}`);
      } else if (s.connection === "dropped") {
        banner.hidden = false;
        banner.className = "banner banner-bad";
        setText(banner, "Disconnected. Reload the page to rejoin — your seat may still be held.");
      } else {
        banner.hidden = true;
      }
    },

    announce(message) {
      if (message === lastAnnounced) return;
      lastAnnounced = message;
      // Swap the node so screen readers re-announce an identical-looking update.
      fill(live, el("p", { text: message }));
    },

    toast(message, kind = "info") {
      const node = el("div", { class: kind === "error" ? "toast toast-error" : "toast", role: "alert" },
        el("span", { class: "toast-dot", "aria-hidden": "true" }),
        el("span", { class: "grow", text: message }),
      );
      toasts.appendChild(node);
      d.timeout(5000, () => node.remove());
    },

    destroy() {
      d.dispose();
      app.remove();
      toasts.remove();
    },
  };
}
