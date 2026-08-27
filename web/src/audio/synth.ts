// The sound engine: a struck-mallet voice made of oscillators, plus the
// AudioContext lifecycle around it.
//
// Verso ships no audio files. Every cue in the game is three sine partials
// under a decay envelope, assembled at play time. Two reasons it is built this
// way rather than from samples. The Go binary embeds web/dist wholesale
// (cmd/verso/static.go:18), so an asset is bytes in every deployed image and a
// fetch on a cold start; and a synthesised note is retunable by editing one
// number, which matters more here than fidelity does, because these cues only
// have to be *discriminable* — "it is my turn" must never be mistaken for
// "somebody else's turn" by a player who is looking out of the window.

/** One struck note within a cue. */
export interface Note {
  /** Pitch, in Hz. */
  readonly freq: number;
  /** Onset, in seconds from the start of the cue. */
  readonly at: number;
  /** Full ring-out, in seconds. The perceived length is roughly half of it. */
  readonly dur?: number;
  /** Level relative to the cue's own level; 1 is the reference. */
  readonly gain?: number;
}

/** Headroom for a cue whose notes all land at once, e.g. the vote chord. */
const MASTER_GAIN = 0.45;
/** Long enough to avoid a click, short enough to still read as a strike. */
const ATTACK_S = 0.006;
const DEFAULT_DUR_S = 0.26;
/** Fundamental, octave, twelfth — enough overtone to read as struck wood. */
const PARTIALS: ReadonlyArray<readonly [number, number]> = [
  [1, 1],
  [2, 0.22],
  [3, 0.07],
];

export interface Engine {
  /** Schedules one cue. A silent no-op while the context is not running. */
  play(notes: readonly Note[], level: number): void;
  /**
   * Builds the context, or resumes a suspended one. Must be called from inside
   * a real user gesture the first time, or the browser keeps it suspended.
   */
  resume(): void;
  /** True once the context exists and is actually running. */
  ready(): boolean;
  /** Releases the hardware. A later resume() builds a fresh context. */
  close(): void;
}

export function createEngine(): Engine {
  let ctx: AudioContext | null = null;
  let master: GainNode | null = null;

  const build = (): boolean => {
    if (ctx !== null) return true;
    // Absent in non-browser contexts, and in a couple of privacy modes.
    const Ctor = globalThis.AudioContext;
    if (typeof Ctor !== "function") return false;
    try {
      // "interactive" asks the platform for the shortest buffer it will give
      // us: these cues are reactions to a deadline, so latency beats power.
      ctx = new Ctor({ latencyHint: "interactive" });
      master = ctx.createGain();
      master.gain.value = MASTER_GAIN;
      master.connect(ctx.destination);
      return true;
    } catch {
      ctx = null;
      master = null;
      return false;
    }
  };

  return {
    play(notes, level) {
      // A suspended context does not advance currentTime, so scheduling into
      // one banks up every missed cue and fires them together on resume. Drop
      // them instead: a stale "your turn" is worse than silence.
      if (ctx === null || master === null || ctx.state !== "running") return;
      const t0 = ctx.currentTime + 0.01;
      for (const n of notes) strike(ctx, master, n, t0, level);
    },

    resume() {
      if (!build() || ctx === null) return;
      if (ctx.state === "running") return;
      void ctx.resume().catch(() => {
        /* Still locked; the next gesture tries again. */
      });
    },

    ready() {
      return ctx !== null && ctx.state === "running";
    },

    close() {
      const dying = ctx;
      ctx = null;
      master = null;
      void dying?.close().catch(() => {
        /* Already gone. */
      });
    },
  };
}

function strike(ctx: AudioContext, dest: AudioNode, n: Note, t0: number, level: number): void {
  const t = t0 + n.at;
  const dur = n.dur ?? DEFAULT_DUR_S;
  const peak = Math.max(0.0001, level * (n.gain ?? 1));

  const env = ctx.createGain();
  env.gain.setValueAtTime(0, t);
  env.gain.linearRampToValueAtTime(peak, t + ATTACK_S);
  // Exponential, because a linear fade on a mallet sounds like a fault.
  env.gain.exponentialRampToValueAtTime(peak * 0.001, t + dur);
  env.connect(dest);

  const voices: Array<{ osc: OscillatorNode; mix: GainNode }> = [];
  for (const [mult, share] of PARTIALS) {
    const freq = n.freq * mult;
    // Past Nyquist an oscillator aliases into audible garbage. Cheaper to
    // drop the partial than to filter it.
    if (freq >= ctx.sampleRate / 2) continue;
    const osc = ctx.createOscillator();
    osc.type = "sine";
    osc.frequency.setValueAtTime(freq, t);
    const mix = ctx.createGain();
    mix.gain.value = share;
    osc.connect(mix);
    mix.connect(env);
    osc.start(t);
    osc.stop(t + dur + 0.02);
    voices.push({ osc, mix });
  }
  const last = voices[voices.length - 1];
  if (last === undefined) {
    env.disconnect();
    return;
  }
  // All the partials stop together, so tearing the whole strike down on the
  // last one is enough to keep a long match from accumulating a graph.
  last.osc.onended = () => {
    for (const v of voices) {
      v.osc.disconnect();
      v.mix.disconnect();
    }
    env.disconnect();
  };
}
