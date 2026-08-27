import type { PlayerInfo } from "../../gen/verso/v1/game_pb.js";

/**
 * Puts players in this round's drawing order.
 *
 * One rule, two readers — the roster and the ballot — because they must agree.
 * The room votes on the drawings in the order it watched them appear
 * (DESIGN.md:60): "the third one" is how a drawing gets referred to out loud,
 * and it only means anything while the third row is still the third row. The
 * server therefore keeps `turn_order` on the wire for the whole round, vote
 * included, and every list that shows players during a round sorts by it.
 *
 * Anyone the order does not name — eliminated, or seated after the round began —
 * keeps their seat position and follows. With no order published (the lobby, the
 * final reveal) this is plain seat order, which is the order `players` already
 * arrives in: `Array.prototype.sort` is stable, so equal ranks do not move.
 */
export function inTurnOrder(players: readonly PlayerInfo[], turnOrder: readonly string[]): PlayerInfo[] {
  const seats = [...players];
  if (turnOrder.length === 0) return seats;
  const at = new Map(turnOrder.map((id, i) => [id, i]));
  return seats.sort(
    (a, b) => (at.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (at.get(b.id) ?? Number.MAX_SAFE_INTEGER),
  );
}
