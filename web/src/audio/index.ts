// Turn audio: the public surface of the sound layer, and the driver that turns
// store updates into cues.
//
// Wired once in main.ts. Nothing under web/src/ui/ imports this module — the
// sound toggle in the app chrome is handed a SoundToggle from the UI contract
// instead, the same way the canvas is handed a CanvasHandle.

import { CUES, endOfTurnCountdown, preTurnCountdown, type Cue, type CueName } from "./cues.js";
import { createEngine, type Engine } from "./synth.js";
import { cueFor, tickPlanFor, type AudioFacts, type TickPlan } from "./transitions.js";

export type { AudioFacts, CueName };

const PREF_KEY = "verso.sound";

/**
 * A tick that arrives this far from where it was scheduled is discarded. A
 * backgrounded tab throttles timers to once a minute or worse, so without this
 * a player who switched tabs mid-turn gets the countdown for a turn that ended
 * long ago.
 */
const TICK_TOLERANCE_MS = 900;

export interface Audio {
  /** Plays a cue now. Silent when muted, or before the first user gesture. */
  play(cue: CueName): void;
  /** Plays a run of one-second ticks counting into, or out of, your turn. */
  playTicks(kind: TickPlan["kind"], ticks: number): void;
  enabled(): boolean;
  /** Persists the choice. Turning it on from a click also unlocks the context. */
  setEnabled(on: boolean): void;
  /**
   * Resumes the audio context. Called for you on the first gesture anywhere in
   * the page; exposed because the sound toggle is itself such a gesture.
   */
  unlock(): void;
  dispose(): void;
}

export function createAudio(engine: Engine = createEngine()): Audio {
  let on = storedPreference();
  // Browsers will not start an audio context outside a user gesture, so the
  // first one anywhere in the page is claimed for it. By the time a match
  // starts, everyone has clicked Create or Join, so in practice the unlock has
  // already happened well before the first cue.
  const gestures = ["pointerdown", "keydown", "touchend"] as const;
  const onGesture = () => {
    unlock();
  };
  const arm = () => {
    for (const type of gestures) {
      globalThis.addEventListener?.(type, onGesture, { capture: true, passive: true });
    }
  };
  const disarm = () => {
    for (const type of gestures) {
      globalThis.removeEventListener?.(type, onGesture, { capture: true });
    }
  };
  const sound = (c: Cue) => {
    engine.play(c.notes, c.level);
  };
  const unlock = () => {
    if (!on) return;
    engine.resume();
    if (engine.ready()) disarm();
  };
  if (on) arm();

  return {
    play(cue) {
      if (!on) return;
      sound(CUES[cue]);
    },

    playTicks(kind, ticks) {
      if (!on) return;
      sound(kind === "preTurn" ? preTurnCountdown(ticks) : endOfTurnCountdown(ticks));
    },

    enabled() {
      return on;
    },

    setEnabled(next) {
      if (next === on) return;
      on = next;
      store(next);
      if (!next) {
        disarm();
        // Release the hardware rather than leaving a muted context running for
        // the rest of the match.
        engine.close();
        return;
      }
      arm();
      unlock();
    },

    unlock,

    dispose() {
      disarm();
      engine.close();
    },
  };
}

/**
 * Turns a stream of states into cues. Feed it every state the store publishes;
 * it plays at most one cue per change and schedules the end-of-turn countdown.
 *
 * The first state is only ever recorded, never sounded. A page load and a
 * resync both arrive as a large state change that would otherwise announce a
 * turn the player has been watching for ten seconds.
 */
export function createAudioDriver(audio: Audio): (s: AudioFacts) => void {
  let prev: AudioFacts | null = null;
  let latest: AudioFacts | null = null;
  let pending: TickPlan | null = null;
  let timer = 0;

  const cancel = () => {
    if (timer !== 0) {
      globalThis.clearTimeout(timer);
      timer = 0;
    }
    pending = null;
  };

  const fire = () => {
    timer = 0;
    const due = pending;
    pending = null;
    // Re-derive from the live state rather than trusting the timer: the turn
    // may have ended early, or the tab may have been asleep.
    if (due === null || latest === null) return;
    const still = tickPlanFor(latest);
    if (still === null || still.kind !== due.kind) return;
    if (Math.abs(performance.now() - due.at) > TICK_TOLERANCE_MS) return;
    audio.playTicks(due.kind, due.ticks);
  };

  const schedule = (s: AudioFacts) => {
    const next = tickPlanFor(s);
    if (next === null) {
      cancel();
      return;
    }
    // The same countdown arriving again — a vote count, a presence change, a
    // roster update — must not restart it.
    if (pending !== null && pending.kind === next.kind && Math.abs(next.at - pending.at) < 50) {
      return;
    }
    cancel();
    const delay = next.at - performance.now();
    // Already inside the window: the ticks would be a lie about how much time
    // is left, so this one is skipped entirely rather than started late.
    if (delay <= 0) return;
    pending = next;
    timer = globalThis.setTimeout(fire, delay);
  };

  return (s) => {
    latest = s;
    if (prev !== null) {
      const cue = cueFor(prev, s);
      if (cue !== null) audio.play(cue);
    }
    prev = s;
    schedule(s);
  };
}

function storedPreference(): boolean {
  try {
    // Default on: a player who cannot see the screen when their turn starts is
    // the whole reason this layer exists.
    return globalThis.localStorage?.getItem(PREF_KEY) !== "off";
  } catch {
    return true;
  }
}

function store(on: boolean): void {
  try {
    globalThis.localStorage?.setItem(PREF_KEY, on ? "on" : "off");
  } catch {
    /* Private mode; the choice just will not survive a reload. */
  }
}
