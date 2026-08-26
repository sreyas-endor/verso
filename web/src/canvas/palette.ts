// palette.ts — the fixed, server-authoritative ink palette.
//
// The wire carries a palette *index*, never a CSS colour string
// (IMPLEMENTATION_PLAN.md §4.7), so a client cannot inject an arbitrary colour
// and every viewer renders the same canvas. PALETTE_SIZE must stay in lockstep
// with room.PaletteSize (internal/room/api.go:54); the server rejects an
// out-of-range index rather than clamping it.
//
// Chosen to read as ink on white paper and to stay separable under the common
// colour-vision deficiencies: no two entries share both hue family and
// lightness, and the red/green pair is split by ~20 points of lightness. Every
// swatch still ships with a name so the UI can label it — nothing in this game
// may be conveyed by colour alone.

export const PALETTE_SIZE = 12;

interface Ink {
  readonly css: string;
  readonly name: string;
}

const INKS: readonly Ink[] = [
  { css: "#14161f", name: "Ink" },
  { css: "#6c7391", name: "Slate" },
  { css: "#8b5a2b", name: "Brown" },
  { css: "#ef4c4c", name: "Red" },
  { css: "#ff7a1a", name: "Orange" },
  { css: "#ffb020", name: "Amber" },
  { css: "#2fbf71", name: "Green" },
  { css: "#0f9b8e", name: "Teal" },
  { css: "#4f7cff", name: "Blue" },
  { css: "#1b3fa0", name: "Navy" },
  { css: "#9b5de5", name: "Purple" },
  { css: "#ff5fa2", name: "Pink" },
];

const FALLBACK: Ink = INKS[0] as Ink;

function ink(index: number): Ink {
  return INKS[index] ?? FALLBACK;
}

/** CSS colour for a palette index. Out-of-range falls back to index 0. */
export function paletteCss(index: number): string {
  return ink(index).css;
}

/** Human-readable swatch name, for `aria-label` on the UI's colour buttons. */
export function paletteName(index: number): string {
  return ink(index).name;
}

/** Every legal index, in order — for building the swatch row. */
export function paletteIndices(): number[] {
  return INKS.map((_, i) => i);
}

export function isValidColorIndex(index: number): boolean {
  return Number.isInteger(index) && index >= 0 && index < PALETTE_SIZE;
}
