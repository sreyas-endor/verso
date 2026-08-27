// Public surface of the UI layer. The wiring agent needs exactly this file:
// mount the chrome once, then hand each screen module a root element and a
// ScreenCtx as the router changes ViewState.screen.

import * as discussion from "./screens/discussion.js";
import * as drawing from "./screens/drawing.js";
import * as intermission from "./screens/intermission.js";
import * as final from "./screens/final.js";
import * as home from "./screens/home.js";
import * as lobby from "./screens/lobby.js";
import * as result from "./screens/result.js";
import * as word from "./screens/word.js";
import type { Screen, ScreenName } from "./context.js";

export const screens: Record<ScreenName, Screen> = {
  home,
  lobby,
  word,
  intermission,
  drawing,
  discussion,
  result,
  final,
};

export { createChrome } from "./chrome.js";
export type { Chrome } from "./chrome.js";
export { LIMITS, RECOMMENDED } from "./context.js";
export type {
  Actions,
  CanvasHandle,
  ConnectionStatus,
  Screen,
  ScreenCtx,
  ScreenName,
  SoundToggle,
  ViewState,
  VoteChoice,
} from "./context.js";
export { NIB_WIDTHS, PEN_INKS, avatarColor, initials } from "./palette.js";
export { codeFromLocation, parseRoomCode, rememberName, rememberedName } from "./roomCode.js";
