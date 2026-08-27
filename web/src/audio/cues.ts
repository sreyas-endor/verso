// The cue catalogue: one entry per thing a player has to notice without
// looking at the screen.
//
// Every pitch is drawn from one C major pentatonic scale. That is not
// decoration — handoffs are short, so two cues will overlap, and a pentatonic
// scale has no interval in it that sounds like a mistake when they do.
//
// Meaning is carried by pitch, length and level, in that order:
//
//   - Your own turn rises to the top of the scale and is the loudest thing the
//     game plays. It is the one cue that must cut through a room of people
//     talking over each other.
//   - Somebody else's turn is a single mid note. It fires up to ten times a
//     round, so it has to stay in the background.
//   - The handoff sits an octave below everything else, quiet enough to read
//     as punctuation rather than an event.
//
// Anything added here should be placed on that scale of urgency first, and
// given a pitch second.

import type { Note } from "./synth.js";

const G4 = 392.0;
const C5 = 523.25;
const E5 = 659.25;
const G5 = 783.99;
const A5 = 880.0;
const C6 = 1046.5;
const E6 = 1318.51;
const D6 = 1174.66;

export type CueName =
  /** You are the artist now. The one cue that has to carry across a room. */
  | "yourTurn"
  /** Somebody else took the pen. */
  | "turnChange"
  /** The handoff named you as the next artist. */
  | "youAreNext"
  /** A handoff that is not about you. */
  | "handoff"
  /** Voting is open. */
  | "discussion"
  /** The tally is in. */
  | "resolve"
  /** The match ended on your side. */
  | "win"
  /** The match ended on the other side. */
  | "loss"
  /** Words have been dealt. */
  | "matchStart"
  /** Acknowledges the sound toggle, so "on" is audibly different from broken. */
  | "soundOn";

export interface Cue {
  readonly notes: readonly Note[];
  /** Peak level before the master gain, 0..1. */
  readonly level: number;
}

export const CUES: Record<CueName, Cue> = {
  // Four notes climbing the scale: unmistakable, and unlike anything else here.
  yourTurn: {
    level: 0.95,
    notes: [
      { freq: C5, at: 0 },
      { freq: E5, at: 0.075 },
      { freq: G5, at: 0.15 },
      { freq: C6, at: 0.225, dur: 0.55 },
    ],
  },

  turnChange: {
    level: 0.4,
    notes: [{ freq: E5, at: 0, dur: 0.22 }],
  },

  youAreNext: {
    level: 0.62,
    notes: [
      { freq: G5, at: 0 },
      { freq: C6, at: 0.1, dur: 0.34 },
    ],
  },

  handoff: {
    level: 0.26,
    notes: [{ freq: G4, at: 0, dur: 0.2 }],
  },

  // A chord rather than a sequence: the room stops and opens up.
  discussion: {
    level: 0.5,
    notes: [
      { freq: C5, at: 0, dur: 0.85 },
      { freq: E5, at: 0, dur: 0.85, gain: 0.8 },
      { freq: G5, at: 0, dur: 0.85, gain: 0.65 },
    ],
  },

  resolve: {
    level: 0.55,
    notes: [
      { freq: A5, at: 0 },
      { freq: E5, at: 0.12, dur: 0.45 },
    ],
  },

  win: {
    level: 0.8,
    notes: [
      { freq: C5, at: 0 },
      { freq: E5, at: 0.08 },
      { freq: G5, at: 0.16 },
      { freq: C6, at: 0.24 },
      { freq: E6, at: 0.32, dur: 0.7 },
    ],
  },

  // The same scale walked back down. Deliberately not dissonant: the imposter
  // getting caught is a good ending for most of the room.
  loss: {
    level: 0.55,
    notes: [
      { freq: G5, at: 0 },
      { freq: E5, at: 0.12 },
      { freq: C5, at: 0.24 },
      { freq: G4, at: 0.36, dur: 0.75 },
    ],
  },

  matchStart: {
    level: 0.5,
    notes: [
      { freq: G4, at: 0 },
      { freq: C5, at: 0.1, dur: 0.4 },
    ],
  },

  soundOn: {
    level: 0.5,
    notes: [
      { freq: C5, at: 0 },
      { freq: G5, at: 0.08, dur: 0.3 },
    ],
  },
};

// ---------------------------------------------------------------------------
// Countdowns
//
// Built rather than tabulated, because their length is not fixed: the handoff
// countdown is as long as the handoff allows, down to the three-second minimum
// intermission. Every tick of a run is scheduled inside one cue, so the audio
// clock spaces them rather than setTimeout, which drifts.
//
// Nothing cancels a run once it is scheduled. Both are bounded by a
// server-timed phase, and the only way either phase ends early is a socket
// dropping — a stray tick into the next screen is not worth tracking live
// audio nodes to prevent.
// ---------------------------------------------------------------------------

/** Ticking into your own turn: the last tick lands one second before the pen. */
export function preTurnCountdown(ticks: number): Cue {
  return {
    level: 0.62,
    notes: run(ticks, A5, C6),
  };
}

/** Ticking your own turn out. Higher and drier than the handoff countdown. */
export function endOfTurnCountdown(ticks: number): Cue {
  return {
    level: 0.5,
    notes: run(ticks, D6, D6),
  };
}

function run(ticks: number, freq: number, lastFreq: number): Note[] {
  const notes: Note[] = [];
  for (let i = 0; i < ticks; i++) {
    const last = i === ticks - 1;
    notes.push({
      freq: last ? lastFreq : freq,
      at: i,
      dur: last ? 0.16 : 0.1,
      // The last tick is the one that means "now".
      gain: last ? 1.35 : 1,
    });
  }
  return notes;
}
