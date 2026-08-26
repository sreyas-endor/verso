// Protocol constants and the narrowed shapes of the two wire envelopes.
//
// Everything here is derived from `web/gen/verso/v1/game_pb.ts`, which is
// generated from `proto/verso/v1/game.proto`. Nothing in this file may drift
// from the server on its own: the constants mirror `internal/room/api.go` and
// the types are computed from the generated union, so adding a `oneof` variant
// to the proto changes `ServerEventBody` and breaks every exhaustive switch
// that consumes it (IMPLEMENTATION_PLAN.md §4.3, the drift detector).

import { isUnknownEnum } from "@bufbuild/protobuf";
import type { DescEnum } from "@bufbuild/protobuf";

import {
  Difficulty,
  DifficultySchema,
  ErrorCode,
  ErrorCodeSchema,
  MatchEndReason,
  MatchEndReasonSchema,
  Phase,
  PhaseSchema,
  WinnerSide,
  WinnerSideSchema,
} from "../../gen/verso/v1/game_pb.js";
import type { ClientCommand, ServerEvent } from "../../gen/verso/v1/game_pb.js";

// --------------------------------------------------------------------------
// Server mirrors. Each value restates a constant the server already enforces,
// so the UI can disable a control instead of waiting for an Error frame. The
// server is still the authority: it clamps or rejects regardless.
// --------------------------------------------------------------------------

/** Bumped in lockstep with `room.ProtocolVersion`. */
export const PROTOCOL_VERSION = 1;

export const MIN_PLAYERS = 3;
export const MAX_PLAYERS = 10;

export const MIN_ROUNDS = 1;
export const MAX_ROUNDS = 4;
export const DEFAULT_ROUNDS = 2;

export const MIN_DRAW_SECONDS = 5;
export const MAX_DRAW_SECONDS = 60;
export const DEFAULT_DRAW_SECONDS = 15;

export const MIN_DISCUSS_SECONDS = 30;
export const MAX_DISCUSS_SECONDS = 180;
export const DEFAULT_DISCUSS_SECONDS = 120;

export const MIN_INTERMISSION_SECONDS = 3;
export const MAX_INTERMISSION_SECONDS = 30;
export const DEFAULT_INTERMISSION_SECONDS = 10;

/** Wire coordinate space: 1024x768 logical at quarter-unit precision. */
export const GRID_WIDTH = 4096;
export const GRID_HEIGHT = 3072;
export const LOGICAL_WIDTH = 1024;
export const LOGICAL_HEIGHT = 768;
/** Signed int16 bounds. Off-grid overshoot is legal and deliberate. */
export const COORD_MIN = -32768;
export const COORD_MAX = 32767;

/** Colours travel as indices into a client-rendered palette of this size. */
export const PALETTE_SIZE = 12;
export const MIN_STROKE_WIDTH = 1;
export const MAX_STROKE_WIDTH = 32;
export const MAX_POINTS_PER_STROKE = 1200;

/** Outbound stroke batching window (IMPLEMENTATION_PLAN.md §4.7). */
export const STROKE_BATCH_MS = 50;

/** Reconnect grace window. The authoritative value arrives on `Joined`. */
export const GRACE_SECONDS = 60;

/**
 * The two fixed phase lengths the server owns (`room.AssignDuration` and
 * `room.ResolveDuration`). They are mirrored because `Snapshot` reports the
 * milliseconds left in a phase but not how long that phase was, and a
 * depleting bar needs the denominator.
 */
export const ASSIGN_DURATION_MS = 6_000;
export const RESOLVE_DURATION_MS = 8_000;

export const ROOM_CODE_LENGTH = 5;
/** O/0 and I/1/L are absent: codes get read aloud (registry.Alphabet). */
export const ROOM_CODE_ALPHABET = "ABCDEFGHJKMNPQRSTUVWXYZ23456789";
export const MAX_DISPLAY_NAME_LENGTH = 24;

// --------------------------------------------------------------------------
// Narrowed envelope unions
// --------------------------------------------------------------------------

/**
 * `ServerEvent.evt` with the `{ case: undefined }` member protoc-gen-es emits
 * removed. An envelope whose oneof is unset never reaches a consumer: the
 * socket drops it at the boundary (`decodeServerEvent`), so everything
 * downstream is a real variant with a non-null payload.
 */
export type ServerEventBody = Exclude<ServerEvent["evt"], { case: undefined }>;

export type ServerEventCase = ServerEventBody["case"];

/** The variant for one case, e.g. `ServerEventOf<"snapshot">`. */
export type ServerEventOf<C extends ServerEventCase> = Extract<ServerEventBody, { case: C }>;

/** The payload for one case, e.g. `ServerPayload<"snapshot">` is `Snapshot`. */
export type ServerPayload<C extends ServerEventCase> = ServerEventOf<C>["value"];

/** `ClientCommand.cmd` with the unset member removed. */
export type ClientCommandBody = Exclude<ClientCommand["cmd"], { case: undefined }>;

export type ClientCommandCase = ClientCommandBody["case"];

/**
 * A decoded frame: the correlation id plus a guaranteed-present body.
 * `cid` is `""` for a spontaneous event such as a phase expiry.
 */
export interface ServerFrame {
  readonly cid: string;
  readonly body: ServerEventBody;
}

/**
 * Every event case, as data. The `Record` is the second drift detector: a new
 * `oneof` variant makes this object literal fail `tsc` with a missing key,
 * even where no `switch` happens to be looking.
 */
const EVENT_CASE_TABLE: Record<ServerEventCase, true> = {
  lobbyState: true,
  settingsChanged: true,
  roundStarted: true,
  turnStarted: true,
  strokeBegan: true,
  strokePoints: true,
  strokeEnded: true,
  phaseChanged: true,
  voteCastCount: true,
  voteTally: true,
  playerEliminated: true,
  matchEnded: true,
  playerPresence: true,
  error: true,
  joined: true,
  yourWord: true,
  snapshot: true,
  spectatorInfo: true,
  voteAccepted: true,
};

export const EVENT_CASES: readonly ServerEventCase[] = Object.freeze(
  Object.keys(EVENT_CASE_TABLE) as ServerEventCase[],
);

/**
 * The three events that carry the monotonic `seq`. A gap in it is the one
 * failure a canvas without a CRDT cannot self-heal (IMPLEMENTATION_PLAN.md
 * §4.7), so the socket watches exactly these.
 */
export function isSequenced(
  body: ServerEventBody,
): body is ServerEventOf<"strokeBegan" | "strokePoints" | "strokeEnded"> {
  return body.case === "strokeBegan" || body.case === "strokePoints" || body.case === "strokeEnded";
}

// --------------------------------------------------------------------------
// Open-enum narrowing
//
// proto3 enums are open: a newer server may send a number this build has no
// name for, and the generated type lies about that. Every enum crossing the
// boundary goes through one of these, so an unknown value degrades to
// UNSPECIFIED instead of leaking a bogus number into a switch.
// --------------------------------------------------------------------------

function narrow<T extends number>(desc: DescEnum, value: number, fallback: T): T {
  return isUnknownEnum(desc, value) ? fallback : (value as T);
}

export function narrowPhase(v: number): Phase {
  return narrow(PhaseSchema, v, Phase.UNSPECIFIED);
}

export function narrowDifficulty(v: number): Difficulty {
  return narrow(DifficultySchema, v, Difficulty.UNSPECIFIED);
}

export function narrowWinnerSide(v: number): WinnerSide {
  return narrow(WinnerSideSchema, v, WinnerSide.UNSPECIFIED);
}

export function narrowMatchEndReason(v: number): MatchEndReason {
  return narrow(MatchEndReasonSchema, v, MatchEndReason.UNSPECIFIED);
}

export function narrowErrorCode(v: number): ErrorCode {
  return narrow(ErrorCodeSchema, v, ErrorCode.UNSPECIFIED);
}

// --------------------------------------------------------------------------
// Small shared helpers
// --------------------------------------------------------------------------

/** Uppercases and strips anything outside the join-code alphabet. */
export function normalizeRoomCode(raw: string): string {
  let out = "";
  for (const ch of raw.trim().toUpperCase()) {
    if (ROOM_CODE_ALPHABET.includes(ch)) out += ch;
  }
  return out.slice(0, ROOM_CODE_LENGTH);
}

/** Collapses whitespace and trims to the length the server would truncate to. */
export function normalizeDisplayName(raw: string): string {
  const collapsed = raw.replace(/\s+/g, " ").trim();
  return [...collapsed].slice(0, MAX_DISPLAY_NAME_LENGTH).join("");
}

export function clamp(v: number, lo: number, hi: number): number {
  return v < lo ? lo : v > hi ? hi : v;
}
