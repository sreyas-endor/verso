// Game-data colour, as opposed to theme colour. Palette indices are wire
// values, so this must stay in the same order as canvas/palette.ts.

/** Pen inks. Must stay exactly PaletteSize (12) long — index is the wire value. */
export const PEN_INKS: readonly string[] = [
  "#14161f", // ink
  "#6c7391", // slate
  "#8b5a2b", // brown
  "#ef4c4c", // red
  "#ff7a1a", // orange
  "#ffb020", // amber
  "#2fbf71", // green
  "#0f9b8e", // teal
  "#4f7cff", // blue
  "#1b3fa0", // navy
  "#9b5de5", // purple
  "#ff5fa2", // pink
];

/** Brush nibs, in wire width units (clamped server-side to 1..32). */
export const NIB_WIDTHS: readonly number[] = [3, 8, 16, 28];

/**
 * Ten hues, one per seat in a full room.
 *
 * These used to be the avatar itself and are now its ring. That is not a
 * demotion: two players may choose the same creature, so the portrait alone
 * cannot separate a ten-seat roster and the hue is the half that always can.
 */
const AVATAR_HUES: readonly string[] = [
  "#4f7cff", "#ef4c4c", "#2fbf71", "#ff8a1f", "#7a5cff",
  "#0fb5ba", "#e05bc8", "#d99b00", "#5d6a8c", "#3fae4a",
];

function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

/** Deterministic per-player avatar colour. Same id ⇒ same hue on every client. */
export function avatarColor(playerId: string): string {
  const hue = AVATAR_HUES[hash(playerId) % AVATAR_HUES.length];
  return hue ?? "#4f7cff";
}
