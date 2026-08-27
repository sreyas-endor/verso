// index.ts — the canvas engine's public surface.

export { CanvasEngine } from "./engine.js";
export type { CanvasEngineOptions, CanvasOutbound } from "./engine.js";
export { paint, renderPng, savePng } from "./export.js";
export type { ExportStroke, SaveOutcome } from "./export.js";
export {
  PALETTE_SIZE,
  isValidColorIndex,
  paletteCss,
  paletteIndices,
  paletteName,
} from "./palette.js";
export {
  DEFAULT_WIDTH,
  LOGICAL_H,
  LOGICAL_W,
  MAX_WIDTH,
  MIN_WIDTH,
  clampWidth,
} from "./grid.js";
