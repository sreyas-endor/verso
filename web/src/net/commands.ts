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
  KickPlayerSchema,
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
import type { Avatar, MatchSettings } from "../../gen/verso/v1/game_pb.js";
import {
  MAX_DISCUSS_SECONDS,
  MAX_DRAW_SECONDS,
  MAX_IMPOSTERS,
  MAX_INTERMISSION_SECONDS,
  MAX_ROUNDS,
  MAX_STROKE_WIDTH,
  MIN_DISCUSS_SECONDS,
  MIN_DRAW_SECONDS,
  MIN_IMPOSTERS,
  MIN_INTERMISSION_SECONDS,
  MIN_ROUNDS,
  MIN_STROKE_WIDTH,
  PROTOCOL_VERSION,
  clamp,
} from "./protocol.js";
import type { ClientCommandBody } from "./protocol.js";

/**
 * Every host-configurable setting, derived from the message rather than listed.
 *
 * Derived on purpose. This used to be a hand-written subset, and because a
 * subset is structurally satisfied by the whole, passing a real MatchSettings
 * to it compiled fine while `toSettings` quietly dropped the two fields the
 * subset had never heard of. Add a field to MatchSettings now and toSettings
 * stops compiling until somebody decides what to do with it.
 */
export type SettingsInit = Omit<MatchSettings, "$typeName" | "$unknown">;

export function joinRoom(init: {
  roomCode: string;
  displayName: string;
  seatToken?: string;
  avatar: Avatar;
}): ClientCommandBody {
  return {
    case: "join",
    value: create(JoinRoomSchema, {
      roomCode: init.roomCode,
      displayName: init.displayName,
      seatToken: init.seatToken ?? "",
      // Sent on every join and read by the server on exactly one of them: a
      // fresh seat. A reclaim ignores it, the same as it ignores the name, so
      // this is never how a portrait changes.
      avatar: init.avatar,
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

/**
 * Remove another player's seat. Host only, lobby only; the server enforces
 * both, and refuses the host's own id.
 */
export function kickPlayer(targetPlayerId: string): ClientCommandBody {
  return { case: "kick", value: create(KickPlayerSchema, { targetPlayerId }) };
}

// Every field the server reads has to be set here. A missing one is not a
// no-op: the wire default for a proto3 scalar is 0, the room reads 0 as "not
// specified" (internal/room/api.go:830) and substitutes its own default, so a
// dropped field silently resets that setting on every change to any other one.
//
// `next` is annotated rather than inferred, and that annotation is the whole
// defence: SettingsInit requires every field, so leaving one out is a compile
// error instead of a setting that will not stick. Imposters and elimination
// results were both missing here for exactly as long as nothing enforced it.
export function toSettings(init: SettingsInit): MatchSettings {
  const next: SettingsInit = {
    difficulty: init.difficulty,
    // No range to clamp — an enum the room does not know becomes its default
    // there, the same as any other unset field.
    penRule: init.penRule,
    eliminationResults: init.eliminationResults,
    imposterCount: clamp(Math.round(init.imposterCount), MIN_IMPOSTERS, MAX_IMPOSTERS),
    maxRounds: clamp(Math.round(init.maxRounds), MIN_ROUNDS, MAX_ROUNDS),
    drawSeconds: clamp(Math.round(init.drawSeconds), MIN_DRAW_SECONDS, MAX_DRAW_SECONDS),
    discussSeconds: clamp(Math.round(init.discussSeconds), MIN_DISCUSS_SECONDS, MAX_DISCUSS_SECONDS),
    intermissionSeconds: clamp(
      Math.round(init.intermissionSeconds),
      MIN_INTERMISSION_SECONDS,
      MAX_INTERMISSION_SECONDS,
    ),
  };
  return create(MatchSettingsSchema, next);
}
