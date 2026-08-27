// The contract between the UI layer and everything else. The state/net agent
// implements ViewState + Actions; the canvas agent implements CanvasHandle.
// Nothing under web/src/ui/ imports from those modules directly, so the three
// halves can be built in parallel and adapted at wiring time.

import { PenRule } from "../../gen/verso/v1/game_pb.js";
import type {
  Difficulty,
  ErrorCode,
  MatchEnded,
  MatchSettings,
  Phase,
  PlayerEliminated,
  PlayerInfo,
  SpectatorInfo,
  VoteTally,
} from "../../gen/verso/v1/game_pb.js";

export type ScreenName =
  | "home"
  | "lobby"
  | "word"
  | "intermission"
  | "drawing"
  | "discussion"
  | "result"
  | "final";

export type ConnectionStatus = "offline" | "connecting" | "connected" | "reconnecting" | "dropped";

/** A vote the local player has locked in. `null` until they choose. */
export type VoteChoice = { case: "candidateId"; value: string } | { case: "skip" };

/**
 * Everything the seven screens read. One immutable snapshot per notification —
 * screens never mutate it and never hold a reference across renders.
 */
export interface ViewState {
  screen: ScreenName;
  connection: ConnectionStatus;
  /** Seconds left in the reconnect grace window, or 0 when not reconnecting. */
  graceSeconds: number;

  selfId: string;
  roomCode: string;
  isHost: boolean;
  /** Absolute URL a friend can open to land straight in this room. */
  joinUrl: string;

  phase: Phase;
  round: number;
  totalRounds: number;
  settings: MatchSettings;

  players: PlayerInfo[];
  minPlayers: number;
  maxPlayers: number;
  /** Server's own verdict from LobbyState. The UI additionally explains why not. */
  canStart: boolean;

  turnOrder: string[];
  turnIndex: number;
  artistId: string;
  nextArtistId: string;

  /**
   * `performance.now()` reading at which the current turn or phase expires, or
   * null when nothing is running. Never an absolute wall-clock time: the wire
   * carries relative milliseconds only.
   */
  deadline: number | null;
  /** Full length of the running turn or phase, in ms. 0 when untimed. */
  durationMs: number;

  /** The local player's own secret word. Never another player's. */
  word: string;
  youAreEliminated: boolean;
  youHaveVoted: boolean;
  yourVote: VoteChoice | null;
  votesCast: number;
  activeCount: number;

  /** Latest tally, cleared when a new round starts. */
  tally: VoteTally | null;
  /** Latest elimination outcome, cleared when a new round starts. */
  elimination: PlayerEliminated | null;
  /** Only ever populated for a player who has been eliminated themselves. */
  spectator: SpectatorInfo | null;
  matchEnd: MatchEnded | null;

  /** A command is in flight; primary buttons disable themselves. */
  busy: boolean;
  /** Last server Error rendered inline on home/lobby. Toasts handle the rest. */
  lastError: string | null;
  /** The code behind `lastError`, for the few screens that branch on it. */
  lastErrorCode: ErrorCode | null;
}

export interface Actions {
  createRoom(displayName: string): void;
  joinRoom(roomCode: string, displayName: string): void;
  setReady(ready: boolean): void;
  updateSettings(settings: MatchSettings): void;
  startMatch(): void;
  castVote(choice: VoteChoice): void;
  rematch(): void;
  /** Host only, lobby only. The server rejects it from anyone else. */
  kickPlayer(playerId: string): void;
  requestSnapshot(): void;
}

/** The canvas engine, as the UI sees it. */
export interface CanvasHandle {
  /** Move the live canvas element into `host`. Idempotent. */
  attach(host: HTMLElement): void;
  /** Detach without destroying — strokes survive across screen changes. */
  detach(): void;
  /** Enable pointer input. Only ever true for the current artist. */
  setInteractive(on: boolean): void;
  setColorIndex(index: number): void;
  setWidth(width: number): void;
  /**
   * The per-turn stroke ceiling for the current artist's turn, from the match's
   * pen rule. Unlimited is `Infinity`; the count itself resets wherever the
   * per-turn point budget does, so this is a setting and never a lifecycle.
   */
  setStrokeLimit(limit: number): void;
  /**
   * The state of that ceiling right now: the limit, how much of it is gone, and
   * whether a stroke is open under the pointer. Read cheaply, per frame, by the
   * drawing screen — a stroke starting or ending is a purely local event that
   * the store never republishes, so the gauge polls rather than subscribes.
   */
  strokeBudget(): { limit: number; used: number; penDown: boolean };
  /** Re-renders the vectors at 2x and hands the viewer a PNG. */
  savePng(): Promise<void>;

  /**
   * Round numbers that have a finished canvas kept, oldest first.
   *
   * Every round wipes the canvas, so the reveal would otherwise only ever show
   * the last one. Each round's vectors are kept as it ends and repainted on
   * demand — no bitmap is held per round.
   *
   * A player who reconnected mid-match is missing the rounds they were away
   * for: the archive is built from frames this client saw, and a Snapshot only
   * carries the round in progress.
   */
  archivedRounds(): readonly number[];
  /**
   * Repaint one archived round into `target`, sizing it to the canvas element's
   * own width and height. A round with nothing kept for it paints blank paper
   * rather than throwing, so the reveal never has a hole in it.
   */
  paintRound(round: number, target: HTMLCanvasElement): void;
  /** Re-render one archived round at 2x and hand the viewer a PNG. */
  savePngForRound(round: number): Promise<void>;
}

/**
 * The sound layer, as the UI sees it. Arranged like CanvasHandle: the app
 * chrome renders a control for it without importing the implementation, so
 * nothing under web/src/ui/ knows a cue name or an AudioContext exists.
 */
export interface SoundToggle {
  enabled(): boolean;
  setEnabled(on: boolean): void;
}

export interface ScreenCtx {
  state(): ViewState;
  /** Returns an unsubscribe function. Screens must call it in unmount(). */
  subscribe(fn: (s: ViewState) => void): () => void;
  actions: Actions;
  canvas: CanvasHandle;
  /** Routed to the aria-live="polite" region in the app chrome. */
  announce(message: string): void;
  toast(message: string, kind?: "info" | "error"): void;
}

export interface Screen {
  mount(root: HTMLElement, ctx: ScreenCtx): void;
  unmount(): void;
}

/** DESIGN.md:224 — the recommended defaults, marked in the lobby sliders. */
export const RECOMMENDED = {
  difficulty: 2 as Difficulty,
  penRule: PenRule.FREE,
  maxRounds: 2,
  drawSeconds: 15,
  discussSeconds: 120,
  intermissionSeconds: 10,
} as const;

export const LIMITS = {
  minPlayers: 3,
  maxPlayers: 10,
  minRounds: 1,
  maxRounds: 4,
  minDrawSeconds: 5,
  maxDrawSeconds: 60,
  minDiscussSeconds: 30,
  maxDiscussSeconds: 180,
  minIntermissionSeconds: 3,
  maxIntermissionSeconds: 30,
  codeLength: 5,
  maxNameLength: 24,
} as const;
