// Typed builders for every `ClientCommand` variant.
//
// These return a `ClientCommandBody` — the `{ case, value }` member of the
// envelope's oneof — which is what `VersoSocket.send` and `.request` accept.
// Nothing here talks to the network, so a command can be built and asserted on
// without a socket.
//
// Ranges are clamped to the values the server enforces (protocol.ts mirrors
// internal/room/api.go). Clamping locally is a courtesy that keeps the UI from
// showing a number the server would silently change; the server clamps anyway.

import { create } from "@bufbuild/protobuf";

import {
  CastVoteSchema,
  JoinRoomSchema,
  MatchSettingsSchema,
  RematchSchema,
  RequestSnapshotSchema,
  SetReadySchema,
  StartMatchSchema,
  StrokeBeginSchema,
  StrokeEndSchema,
  StrokePointsSchema,
  UpdateSettingsSchema,
} from "../../gen/verso/v1/game_pb.js";
import type { Difficulty, MatchSettings } from "../../gen/verso/v1/game_pb.js";
import {
  MAX_DISCUSS_SECONDS,
  MAX_DRAW_SECONDS,
  MAX_ROUNDS,
  MAX_STROKE_WIDTH,
  MIN_DISCUSS_SECONDS,
  MIN_DRAW_SECONDS,
  MIN_ROUNDS,
  MIN_STROKE_WIDTH,
  PROTOCOL_VERSION,
  clamp,
} from "./protocol.js";
import type { ClientCommandBody } from "./protocol.js";

/** The four host-configurable settings, as plain numbers. */
export interface SettingsInit {
  difficulty: Difficulty;
  maxRounds: number;
  drawSeconds: number;
  discussSeconds: number;
}

export function joinRoom(init: {
  roomCode: string;
  displayName: string;
  seatToken?: string;
}): ClientCommandBody {
  return {
    case: "join",
    value: create(JoinRoomSchema, {
      roomCode: init.roomCode,
      displayName: init.displayName,
      seatToken: init.seatToken ?? "",
      protocolVersion: PROTOCOL_VERSION,
    }),
  };
}

export function setReady(ready: boolean): ClientCommandBody {
  return { case: "setReady", value: create(SetReadySchema, { ready }) };
}

export function updateSettings(init: SettingsInit): ClientCommandBody {
  return {
    case: "updateSettings",
    value: create(UpdateSettingsSchema, { settings: toSettings(init) }),
  };
}

export function startMatch(): ClientCommandBody {
  return { case: "startMatch", value: create(StartMatchSchema) };
}

export function strokeBegin(init: {
  colorIndex: number;
  width: number;
  points: readonly number[];
}): ClientCommandBody {
  return {
    case: "strokeBegin",
    value: create(StrokeBeginSchema, {
      colorIndex: init.colorIndex,
      width: clamp(Math.round(init.width), MIN_STROKE_WIDTH, MAX_STROKE_WIDTH),
      points: [...init.points],
    }),
  };
}

/**
 * Appends to the artist's open stroke. `stroke_id` and `seq` are deliberately
 * left at zero: the server resolves the target itself and overwrites both on
 * the event it broadcasts. A client that filled them in would be ignored.
 */
export function strokePoints(points: readonly number[]): ClientCommandBody {
  return { case: "strokePoints", value: create(StrokePointsSchema, { points: [...points] }) };
}

/**
 * Closes the open stroke. `points` is the RDP-simplified replacement for the
 * whole stroke; omit it (or pass an empty array) to keep what was streamed.
 */
export function strokeEnd(points?: readonly number[]): ClientCommandBody {
  return { case: "strokeEnd", value: create(StrokeEndSchema, { points: points ? [...points] : [] }) };
}

/** One anonymous, irreversible vote: a candidate id, or Skip. */
export function castVote(choice: { candidateId: string } | { skip: true }): ClientCommandBody {
  const value =
    "skip" in choice
      ? create(CastVoteSchema, { choice: { case: "skip", value: true } })
      : create(CastVoteSchema, { choice: { case: "candidateId", value: choice.candidateId } });
  return { case: "castVote", value };
}

export function requestSnapshot(haveSeq: number): ClientCommandBody {
  return { case: "requestSnapshot", value: create(RequestSnapshotSchema, { haveSeq }) };
}

export function rematch(): ClientCommandBody {
  return { case: "rematch", value: create(RematchSchema) };
}

export function toSettings(init: SettingsInit): MatchSettings {
  return create(MatchSettingsSchema, {
    difficulty: init.difficulty,
    maxRounds: clamp(Math.round(init.maxRounds), MIN_ROUNDS, MAX_ROUNDS),
    drawSeconds: clamp(Math.round(init.drawSeconds), MIN_DRAW_SECONDS, MAX_DRAW_SECONDS),
    discussSeconds: clamp(Math.round(init.discussSeconds), MIN_DISCUSS_SECONDS, MAX_DISCUSS_SECONDS),
  });
}
