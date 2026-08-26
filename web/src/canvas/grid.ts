// grid.ts — the coordinate contract, the brush bounds, and RDP simplification.
//
// Two coordinate spaces, and only two:
//
//   logical  1024x768 floating point. Everything renders here; the CSS
//            letterbox and the DPR transform map it to device pixels.
//   grid     4096x3072 signed int16. Everything on the wire lives here — a
//            quarter-unit of logical precision. Signed on purpose: a stroke
//            dragged past the canvas edge survives the round trip instead of
//            being chopped off, which is why the server's ValidPoints accepts
//            any coordinate inside the int16 range rather than clamping to the
//            grid rectangle (internal/room/api.go:838).
//
// Every constant here mirrors one the server enforces. Keeping the client
// inside the same bounds is not a substitute for the server's authority — it
// just means the artist's local ink matches what the server will accept, so
// there is nothing to reconcile in the common case.

export const LOGICAL_W = 1024;
export const LOGICAL_H = 768;

/** grid units per logical unit. GRID_W === LOGICAL_W * GRID_SCALE. */
export const GRID_SCALE = 4;
export const GRID_W = LOGICAL_W * GRID_SCALE;
export const GRID_H = LOGICAL_H * GRID_SCALE;

export const COORD_MIN = -32768;
export const COORD_MAX = 32767;

export const MIN_WIDTH = 1;
export const MAX_WIDTH = 32;
export const DEFAULT_WIDTH = 6;

export const MAX_POINTS_PER_STROKE = 1200;
export const MAX_POINTS_PER_TURN = 4000;

/** Outbound point batching window, matching room.StrokeBatchWindow. */
export const STROKE_BATCH_MS = 50;

/** RDP tolerance, in logical units, applied once on pointerup. */
export const SIMPLIFY_TOLERANCE = 0.6;

export function clampWidth(w: number): number {
  if (!Number.isFinite(w)) return MIN_WIDTH;
  return Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, Math.round(w)));
}

/** Unchecked read. The callers below all walk indices they just bounded. */
function at(a: readonly number[], i: number): number {
  return a[i] as number;
}

function clampCoord(v: number): number {
  if (!Number.isFinite(v)) return 0;
  return Math.min(COORD_MAX, Math.max(COORD_MIN, Math.round(v)));
}

/** logical -> grid, in place over a fresh array. Length is preserved. */
export function toGrid(logical: readonly number[]): number[] {
  const out = new Array<number>(logical.length);
  for (let i = 0; i < logical.length; i++) {
    out[i] = clampCoord(at(logical, i) * GRID_SCALE);
  }
  return out;
}

/** grid -> logical. Tolerates an odd-length array by dropping the tail. */
export function fromGrid(grid: readonly number[]): number[] {
  const n = grid.length - (grid.length % 2);
  const out = new Array<number>(n);
  for (let i = 0; i < n; i++) {
    out[i] = at(grid, i) / GRID_SCALE;
  }
  return out;
}

// ---------------------------------------------------------------------------
// Ramer-Douglas-Peucker, ported to flat interleaved arrays.
//
// Adapted from simplify-js by Vladimir Agafonkin (BSD-2-Clause). Vendored as
// ~30 lines rather than added as a dependency: the package has been dormant
// since 2020 and this is the whole of it. Run once on pointerup, never in the
// hot path (IMPLEMENTATION_PLAN.md §4.7).
// ---------------------------------------------------------------------------

function sqDist(p: readonly number[], i: number, j: number): number {
  const dx = at(p, i) - at(p, j);
  const dy = at(p, i + 1) - at(p, j + 1);
  return dx * dx + dy * dy;
}

function sqSegDist(p: readonly number[], i: number, a: number, b: number): number {
  let x = at(p, a);
  let y = at(p, a + 1);
  let dx = at(p, b) - x;
  let dy = at(p, b + 1) - y;

  if (dx !== 0 || dy !== 0) {
    const t = ((at(p, i) - x) * dx + (at(p, i + 1) - y) * dy) / (dx * dx + dy * dy);
    if (t > 1) {
      x = at(p, b);
      y = at(p, b + 1);
    } else if (t > 0) {
      x += dx * t;
      y += dy * t;
    }
  }

  dx = at(p, i) - x;
  dy = at(p, i + 1) - y;
  return dx * dx + dy * dy;
}

/** Cheap pre-pass: drop points closer together than the tolerance. */
function radialDistance(p: readonly number[], sqTol: number): number[] {
  const out: number[] = [at(p, 0), at(p, 1)];
  let anchor = 0;
  let i = 2;
  for (; i < p.length; i += 2) {
    if (sqDist(p, i, anchor) > sqTol) {
      out.push(at(p, i), at(p, i + 1));
      anchor = i;
    }
  }
  if (anchor !== p.length - 2) {
    out.push(at(p, p.length - 2), at(p, p.length - 1));
  }
  return out;
}

/** Douglas-Peucker proper, iterative so a long stroke cannot blow the stack. */
function douglasPeucker(p: readonly number[], sqTol: number): number[] {
  const last = p.length - 2;
  const keep = new Uint8Array(p.length / 2);
  keep[0] = 1;
  keep[last / 2] = 1;

  const stack: number[] = [0, last];
  while (stack.length > 0) {
    const b = stack.pop() as number;
    const a = stack.pop() as number;
    let maxDist = sqTol;
    let index = -1;
    for (let i = a + 2; i < b; i += 2) {
      const d = sqSegDist(p, i, a, b);
      if (d > maxDist) {
        index = i;
        maxDist = d;
      }
    }
    if (index !== -1) {
      keep[index / 2] = 1;
      stack.push(a, index, index, b);
    }
  }

  const out: number[] = [];
  for (let i = 0; i < keep.length; i++) {
    if (keep[i] === 1) out.push(at(p, i * 2), at(p, i * 2 + 1));
  }
  return out;
}

/**
 * Simplify a flat interleaved polyline. Returns a new array; never grows the
 * input, which matters because the server rejects a "simplification" longer
 * than what it already holds (internal/room/strokes.go:127).
 */
export function simplify(points: readonly number[], tolerance = SIMPLIFY_TOLERANCE): number[] {
  if (points.length <= 4) return points.slice();
  const sqTol = tolerance * tolerance;
  return douglasPeucker(radialDistance(points, sqTol), sqTol);
}
