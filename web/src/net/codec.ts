// Binary protobuf codec. ProtoJSON is never produced or accepted: it is larger
// than hand-rolled JSON and roughly 3.5x slower to parse
// (IMPLEMENTATION_PLAN.md §4.3).

import { create, fromBinary, toBinary } from "@bufbuild/protobuf";

import { ClientCommandSchema, ServerEventSchema } from "../../gen/verso/v1/game_pb.js";
import type { ClientCommand } from "../../gen/verso/v1/game_pb.js";
import type { ClientCommandBody, ServerFrame } from "./protocol.js";

/** Builds one command envelope. `cid` may be empty for fire-and-forget. */
export function buildCommand(body: ClientCommandBody, cid: string): ClientCommand {
  return create(ClientCommandSchema, { cid, cmd: body });
}

export function encodeCommand(cmd: ClientCommand): Uint8Array {
  return toBinary(ClientCommandSchema, cmd);
}

/**
 * Decodes one inbound frame.
 *
 * Returns null for a frame that cannot be parsed and for one whose `evt` oneof
 * is unset — the `{ case: undefined }` member protoc-gen-es emits. Dropping it
 * here is what lets every downstream switch be exhaustive over real variants
 * only; validate once at the boundary, then narrow.
 */
export function decodeServerEvent(data: ArrayBuffer | Uint8Array): ServerFrame | null {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
  let ev;
  try {
    ev = fromBinary(ServerEventSchema, bytes);
  } catch {
    return null;
  }
  if (ev.evt.case === undefined) return null;
  return { cid: ev.cid, body: ev.evt };
}
