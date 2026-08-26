// The shared canvas, as data: an append-only log plus the one open stroke.
//
// Stroke traffic runs at ~20 messages a second, so it deliberately does NOT go
// through the state subscription — republishing the whole state object twenty
// times a second to re-render a player list would be absurd. The canvas engine
// subscribes here instead and gets incremental events; anything that needs the
// whole picture (a resize, a late mount, the PNG export) reads `all()`.

import type { OpenStroke, StrokeEvent, StrokeRecord } from "./types.js";

type Listener = (event: StrokeEvent) => void;

export class StrokeLog {
  private committed: StrokeRecord[] = [];
  private openStroke: OpenStroke | null = null;
  private readonly listeners = new Set<Listener>();

  /** Every committed stroke, oldest first. Read-only. */
  all(): readonly StrokeRecord[] {
    return this.committed;
  }

  /** The stroke currently being drawn, or null. */
  open(): OpenStroke | null {
    return this.openStroke;
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  }

  /** Replaces the whole canvas: a Snapshot replay, a new match, a rematch. */
  reset(strokes: readonly StrokeRecord[]): void {
    this.committed = strokes.map(normalize);
    this.openStroke = null;
    this.emit({ kind: "reset", strokes: this.committed });
  }

  begin(stroke: StrokeRecord, mine: boolean): void {
    // A begin with something still open means the previous stroke's end was
    // lost. Commit what we have rather than dropping it: the canvas is
    // append-only evidence and a half-stroke is better than a hole.
    this.commitOpen();
    this.openStroke = { ...normalize(stroke), mine };
    this.emit({
      kind: "begin",
      strokeId: stroke.strokeId,
      colorIndex: stroke.colorIndex,
      width: stroke.width,
      points: this.openStroke.points,
      mine,
    });
  }

  extend(strokeId: number, points: readonly number[], mine: boolean): void {
    const open = this.openStroke;
    if (open === null || open.strokeId !== strokeId) return;
    const merged = [...open.points, ...points];
    this.openStroke = { ...open, points: merged };
    this.emit({ kind: "points", strokeId, points: [...points], mine });
  }

  /** `points` is the RDP replacement for the whole stroke, or null to keep. */
  end(strokeId: number, points: readonly number[] | null, mine: boolean): void {
    const open = this.openStroke;
    if (open === null || open.strokeId !== strokeId) return;
    const final = points === null ? open.points : [...points];
    this.committed = [...this.committed, { ...open, points: final }];
    this.openStroke = null;
    this.emit({ kind: "end", strokeId, points, mine });
  }

  private commitOpen(): void {
    const open = this.openStroke;
    if (open === null) return;
    this.committed = [...this.committed, normalize(open)];
    this.openStroke = null;
  }

  private emit(event: StrokeEvent): void {
    for (const fn of this.listeners) fn(event);
  }
}

function normalize(s: StrokeRecord): StrokeRecord {
  return {
    strokeId: s.strokeId,
    colorIndex: s.colorIndex,
    width: s.width,
    points: [...s.points],
  };
}
