// The seven screens of DESIGN.md:183, derived from the phase and the local
// player's own status. There is no router: a screen is a pure function of
// state, so it can never disagree with the state the screen is rendering.
//
// Eliminated players are not routed anywhere separate. They are silent
// spectators (DESIGN.md:66): same screens, same canvas, no vote picker.

import { Phase } from "../../gen/verso/v1/game_pb.js";
import { ROOM_CODE_LENGTH, normalizeRoomCode } from "../net/protocol.js";
import type { GameState, ScreenName } from "./types.js";

/** Which screen this state shows. Total over `Phase`, including UNSPECIFIED. */
export function screenFor(state: Pick<GameState, "selfId" | "phase">): ScreenName {
  // No seat yet means the join form, whatever the socket is doing.
  if (state.selfId === "") return "home";
  switch (state.phase) {
    case Phase.ASSIGNING:
      return "word";
    case Phase.DRAWING:
      return "drawing";
    case Phase.INTERMISSION:
      return "intermission";
    case Phase.DISCUSSION:
      return "discussion";
    case Phase.RESOLVING:
      return "result";
    case Phase.ENDED:
      return "final";
    case Phase.LOBBY:
    case Phase.UNSPECIFIED:
      return "lobby";
    default: {
      // An open enum value this build has no name for was narrowed to
      // UNSPECIFIED at the socket boundary, so this is unreachable. Degrade to
      // the lobby rather than showing nothing.
      return "lobby";
    }
  }
}

// --------------------------------------------------------------------------
// Deep links: /#ROOMCODE prefills the join code (IMPLEMENTATION_PLAN.md §4.1,
// and the SPA fallback in cmd/verso/static.go serves index.html for it).
// --------------------------------------------------------------------------

/** The room code in the current URL fragment, or "" when there is none. */
export function deepLinkCode(): string {
  const hash = globalThis.location?.hash ?? "";
  return normalizeRoomCode(hash.startsWith("#") ? hash.slice(1) : hash);
}

/** An absolute URL that lands a friend straight in this room. */
export function joinUrlFor(roomCode: string): string {
  const loc = globalThis.location as Location | undefined;
  if (loc === undefined) return roomCode === "" ? "" : `/#${roomCode}`;
  const base = `${loc.origin}${loc.pathname}${loc.search}`;
  return roomCode === "" ? base : `${base}#${roomCode}`;
}

/**
 * Writes the room code into the fragment without adding a history entry, so
 * the browser Back button leaves the game instead of walking room codes.
 */
export function setDeepLink(roomCode: string): void {
  const loc = globalThis.location as Location | undefined;
  if (loc === undefined) return;
  const next = roomCode === "" ? `${loc.pathname}${loc.search}` : joinUrlFor(roomCode);
  if (loc.href === next) return;
  try {
    globalThis.history?.replaceState(null, "", next);
  } catch {
    loc.hash = roomCode;
  }
}

/** Fires when the user edits the fragment or follows a second deep link. */
export function onDeepLinkChange(fn: (roomCode: string) => void): () => void {
  const handler = () => {
    fn(deepLinkCode());
  };
  globalThis.addEventListener?.("hashchange", handler);
  return () => {
    globalThis.removeEventListener?.("hashchange", handler);
  };
}

/** True when `code` is a syntactically complete join code. */
export function isCompleteRoomCode(code: string): boolean {
  return normalizeRoomCode(code).length === ROOM_CODE_LENGTH;
}
