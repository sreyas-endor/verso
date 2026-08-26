// The client state, as one plain object.
//
// Field names deliberately match `web/src/ui/context.ts` (`ViewState`) so the
// wiring layer is a spread rather than a translation table. Message types come
// straight from the generated code — there is no parallel hierarchy of view
// models to drift out of step with the wire.
//
// Everything here is read-only by convention: the store replaces the whole
// object on every change and never mutates one it has already published.

import { Difficulty, Phase } from "../../gen/verso/v1/game_pb.js";
import { MatchSettingsSchema } from "../../gen/verso/v1/game_pb.js";
import type {
  ErrorCode,
  MatchEnded,
  MatchSettings,
  PlayerEliminated,
  PlayerInfo,
  SpectatorInfo,
  VoteTally,
} from "../../gen/verso/v1/game_pb.js";
import { create } from "@bufbuild/protobuf";
import {
  DEFAULT_DISCUSS_SECONDS,
  DEFAULT_DRAW_SECONDS,
  DEFAULT_INTERMISSION_SECONDS,
  DEFAULT_ROUNDS,
  GRACE_SECONDS,
  MAX_PLAYERS,
  MIN_PLAYERS,
} from "../net/protocol.js";
import type { ConnectionFailure } from "../net/socket.js";

export type { ConnectionFailure };
/** UI-facing connection states; the socket's transport states stay private. */
export type ConnectionStatus = "offline" | "connecting" | "connected" | "reconnecting" | "dropped";

/** The seven screens of DESIGN.md:183, as a name. */
export type ScreenName = "home" | "lobby" | "word" | "intermission" | "drawing" | "discussion" | "result" | "final";

/**
 * A vote the local player has locked in. Anonymous and irreversible
 * (DESIGN.md:49), so there is no "change" transition — only null to set.
 */
export type VoteChoice = { case: "candidateId"; value: string } | { case: "skip" };

/** One committed stroke on the shared canvas. Carries no artist id by design. */
export interface StrokeRecord {
  readonly strokeId: number;
  readonly colorIndex: number;
  readonly width: number;
  /** Flat interleaved x,y pairs on the 4096x3072 signed wire grid. */
  readonly points: readonly number[];
}

/** The in-progress stroke. There is at most one room-wide. */
export interface OpenStroke extends StrokeRecord {
  readonly mine: boolean;
}

/**
 * What the canvas engine consumes. Delivered on its own channel, not through
 * the state subscription: at 20 messages a second a stroke must not force the
 * whole UI to re-render.
 */
export type StrokeEvent =
  | { kind: "begin"; strokeId: number; colorIndex: number; width: number; points: readonly number[]; mine: boolean }
  | { kind: "points"; strokeId: number; points: readonly number[]; mine: boolean }
  /** `points` non-null is an RDP replacement: replace the stroke, do not append. */
  | { kind: "end"; strokeId: number; points: readonly number[] | null; mine: boolean }
  /** Full redraw: a Snapshot replay, a new match, or a return to the lobby. */
  | { kind: "reset"; strokes: readonly StrokeRecord[] };

export interface GameState {
  // -- connection ---------------------------------------------------------
  readonly connection: ConnectionStatus;
  /** Set only when the socket has stopped trying. */
  readonly failure: ConnectionFailure | null;
  /** Seconds left in *our own* reconnect grace window; 0 while connected. */
  readonly graceSeconds: number;
  /** Smoothed round-trip time in ms; 0 before the first sample. */
  readonly rttMs: number;
  /** A correlated command is in flight. Primary buttons disable on this. */
  readonly busy: boolean;
  readonly lastError: string | null;
  readonly lastErrorCode: ErrorCode | null;

  // -- identity -----------------------------------------------------------
  readonly screen: ScreenName;
  readonly selfId: string;
  readonly roomCode: string;
  readonly isHost: boolean;
  /** Absolute URL that lands a friend straight in this room. */
  readonly joinUrl: string;

  // -- match --------------------------------------------------------------
  readonly phase: Phase;
  readonly round: number;
  readonly totalRounds: number;
  readonly settings: MatchSettings;

  readonly players: PlayerInfo[];
  readonly minPlayers: number;
  readonly maxPlayers: number;
  /** The server's own verdict from LobbyState. */
  readonly canStart: boolean;

  // -- drawing ------------------------------------------------------------
  readonly turnOrder: string[];
  readonly turnIndex: number;
  readonly artistId: string;
  /** Artist announced for the current intermission; blank when voting is next. */
  readonly nextArtistId: string;

  /**
   * `performance.now()` reading at which the running turn or phase expires, or
   * null when nothing is running.
   *
   * The wire carries relative milliseconds only — there is not one absolute
   * timestamp in game.proto — so this is derived, monotonic, and immune to the
   * device clock being wrong. The client never decides that a phase ended: it
   * clamps the display at zero and waits for the server to say so.
   */
  readonly deadline: number | null;
  /** Full length of the running turn or phase, in ms. 0 when untimed. */
  readonly durationMs: number;

  // -- the secret ---------------------------------------------------------
  /** The local player's own word. No other player's word is ever in here. */
  readonly word: string;
  readonly youAreEliminated: boolean;
  readonly youHaveVoted: boolean;
  /**
   * The local player's own choice, when this client is the one that cast it.
   * A reconnect cannot restore it: the server confirms *that* you voted, never
   * again *what* you voted (DESIGN.md:56).
   */
  readonly yourVote: VoteChoice | null;
  readonly votesCast: number;
  readonly activeCount: number;

  // -- results ------------------------------------------------------------
  readonly tally: VoteTally | null;
  readonly elimination: PlayerEliminated | null;
  /** Populated only for a player who has been eliminated themselves. */
  readonly spectator: SpectatorInfo | null;
  readonly matchEnd: MatchEnded | null;
}

export function defaultSettings(): MatchSettings {
  return create(MatchSettingsSchema, {
    difficulty: Difficulty.MEDIUM,
    maxRounds: DEFAULT_ROUNDS,
    drawSeconds: DEFAULT_DRAW_SECONDS,
    discussSeconds: DEFAULT_DISCUSS_SECONDS,
    intermissionSeconds: DEFAULT_INTERMISSION_SECONDS,
  });
}

export function initialState(): GameState {
  return {
    connection: "offline",
    failure: null,
    graceSeconds: 0,
    rttMs: 0,
    busy: false,
    lastError: null,
    lastErrorCode: null,

    screen: "home",
    selfId: "",
    roomCode: "",
    isHost: false,
    joinUrl: "",

    phase: Phase.LOBBY,
    round: 0,
    totalRounds: DEFAULT_ROUNDS,
    settings: defaultSettings(),

    players: [],
    minPlayers: MIN_PLAYERS,
    maxPlayers: MAX_PLAYERS,
    canStart: false,

    turnOrder: [],
    turnIndex: 0,
    artistId: "",
    nextArtistId: "",

    deadline: null,
    durationMs: 0,

    word: "",
    youAreEliminated: false,
    youHaveVoted: false,
    yourVote: null,
    votesCast: 0,
    activeCount: 0,

    tally: null,
    elimination: null,
    spectator: null,
    matchEnd: null,
  };
}

/** The full grace window, for a UI that wants to show a proportion. */
export const GRACE_WINDOW_SECONDS = GRACE_SECONDS;
