// The observable store: one plain state object, one subscribe, and one reducer
// keyed by event case.
//
// The `switch` in `reduce` is the client-side drift detector
// (IMPLEMENTATION_PLAN.md §4.3). `body` is `ServerEventBody` — the generated
// oneof with the `{ case: undefined }` member removed — so its `default` arm
// assigns to `never`. Add a variant to game.proto and this file stops
// compiling until somebody decides what the client does with it.

import { ErrorCode, Phase } from "../../gen/verso/v1/game_pb.js";
import type { PlayerInfo, Snapshot } from "../../gen/verso/v1/game_pb.js";
import { ASSIGN_DURATION_MS, RESOLVE_DURATION_MS } from "../net/protocol.js";
import type { ServerEventBody, ServerFrame } from "../net/protocol.js";
import { joinUrlFor, screenFor } from "./routes.js";
import { defaultSettings, initialState } from "./types.js";
import type { GameState, VoteChoice } from "./types.js";

/** Half of a round trip, capped: the lead time a deadline is aged by. */
const MAX_LATENCY_LEAD_MS = 250;

type Listener = (state: GameState) => void;

export class GameStore {
  private state: GameState = initialState();
  private readonly listeners = new Set<Listener>();
  private readonly now: () => number;
  private latencyLead = 0;

  constructor(now: () => number = () => performance.now()) {
    this.now = now;
  }

  // ---- reading ----------------------------------------------------------

  getState(): GameState {
    return this.state;
  }

  /** Fires immediately with the current state. Returns an unsubscribe. */
  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    fn(this.state);
    return () => {
      this.listeners.delete(fn);
    };
  }

  // ---- writing ----------------------------------------------------------

  /**
   * Applies one decoded frame. Called by the session wiring, not by the UI.
   *
   * A reducer arm that returns the state it was handed is declaring that this
   * event carries no `GameState`, and referential equality is how it says so.
   * Publishing that to every listener anyway is what made a stroke batch — up
   * to 20 a second, and the artist receives its own echo — rerun the screen
   * renderer and rebuild every roster row on the same main thread that is
   * rasterising the ink. The store owns its own notification semantics, so the
   * decision belongs here rather than in a special case at the call site.
   *
   * Note this is a *reference* test, not a deep compare: every arm that
   * changes anything already builds a new object, so a real update can never
   * be swallowed by it.
   */
  apply(frame: ServerFrame): void {
    const next = this.reduce(this.state, frame.body);
    if (next === this.state) return;
    this.commit(next);
  }

  /** Merges connection facts the socket owns and the reducer cannot see. */
  patch(partial: Partial<GameState>): void {
    this.commit({ ...this.state, ...partial });
  }

  /** Half the smoothed RTT, used to age every deadline the server sends. */
  setLatency(rttMs: number): void {
    this.latencyLead = Math.min(rttMs / 2, MAX_LATENCY_LEAD_MS);
  }

  private commit(next: GameState): void {
    const screen = screenFor(next);
    this.state = next.screen === screen ? next : { ...next, screen };
    for (const fn of this.listeners) fn(this.state);
  }

  // ---- the reducer ------------------------------------------------------

  private reduce(s: GameState, body: ServerEventBody): GameState {
    switch (body.case) {
      case "joined": {
        const v = body.value;
        return {
          ...s,
          selfId: v.playerId,
          roomCode: v.roomCode,
          isHost: v.isHost,
          joinUrl: joinUrlFor(v.roomCode),
          graceSeconds: 0,
          failure: null,
          lastError: null,
          lastErrorCode: null,
        };
      }

      case "lobbyState": {
        const v = body.value;
        const base = s.phase === Phase.LOBBY ? s : clearMatch(s);
        return {
          ...base,
          roomCode: v.roomCode,
          joinUrl: joinUrlFor(v.roomCode),
          players: [...v.players],
          settings: v.settings ?? base.settings,
          phase: v.phase,
          minPlayers: v.minPlayers,
          maxPlayers: v.maxPlayers,
          canStart: v.canStart,
          ...selfFacts(v.players, s.selfId),
        };
      }

      case "settingsChanged":
        return { ...s, settings: body.value.settings ?? defaultSettings() };

      case "roundStarted": {
        const v = body.value;
        return {
          ...clearRound(s),
          round: v.round,
          totalRounds: v.totalRounds,
          turnOrder: [...v.turnOrder],
          turnIndex: 0,
          activeCount: v.activeCount,
        };
      }

      case "turnStarted": {
        const v = body.value;
        return {
          ...s,
          round: v.round,
          turnIndex: v.turnIndex,
          artistId: v.artistId,
          nextArtistId: "",
          durationMs: v.durationMs,
          deadline: this.deadlineFrom(v.remainingMs),
        };
      }

      case "phaseChanged": {
        const v = body.value;
        // ASSIGNING opens every round, not just the match, and the two need
        // very different clears. `round` is what tells them apart: the reveal
        // that deals round n runs while the server's counter is still n-1, so
        // 0 is the match opening and anything else is a round boundary.
        //
        // Wiping the match at a round boundary would be a live bug, not just
        // waste: clearMatch resets youAreEliminated, so an eliminated player
        // would be handed the drawing screen back for the rest of the match.
        const base =
          v.phase === Phase.LOBBY || (v.phase === Phase.ASSIGNING && v.round === 0)
            ? clearMatch(s)
            : v.phase === Phase.ASSIGNING
              ? clearRoundBoundary(s)
              : s;
        const leavingTurnSequence = v.phase !== Phase.DRAWING && v.phase !== Phase.INTERMISSION;
        return {
          ...base,
          phase: v.phase,
          round: v.round,
          durationMs: v.durationMs,
          deadline: this.deadlineFrom(v.remainingMs),
          turnOrder: leavingTurnSequence ? [] : base.turnOrder,
          turnIndex: leavingTurnSequence ? 0 : base.turnIndex,
          artistId: v.phase === Phase.DRAWING ? base.artistId : "",
          nextArtistId: v.nextArtistId,
        };
      }

      // Stroke frames carry no game state. The canvas engine consumes them
      // straight off the socket (see main.ts), so there is nothing to reduce
      // here and nothing to store — a parallel copy in the store was pure
      // per-message allocation with no reader.
      case "strokeBegan":
      case "strokePoints":
      case "strokeEnded":
        return s;

      case "voteCastCount": {
        const v = body.value;
        return { ...s, round: v.round, votesCast: v.votesCast, activeCount: v.activeCount };
      }

      case "voteAccepted": {
        const v = body.value;
        const choice: VoteChoice = v.skip
          ? { case: "skip" }
          : { case: "candidateId", value: v.candidateId };
        return { ...s, youHaveVoted: true, yourVote: choice, busy: false };
      }

      case "voteTally":
        return { ...s, tally: body.value, activeCount: body.value.activeCount };

      case "playerEliminated": {
        const v = body.value;
        if (!v.eliminated) return { ...s, elimination: v };
        return {
          ...s,
          elimination: v,
          players: s.players.map((p) => (p.id === v.playerId ? { ...p, eliminated: true } : p)),
          youAreEliminated: s.youAreEliminated || v.playerId === s.selfId,
        };
      }

      case "spectatorInfo":
        return { ...s, spectator: body.value };

      case "matchEnded": {
        const v = body.value;
        const done = new Set(v.reveals.filter((r) => r.eliminated).map((r) => r.playerId));
        return {
          ...s,
          matchEnd: v,
          phase: Phase.ENDED,
          deadline: null,
          durationMs: 0,
          players: s.players.map((p) => (done.has(p.id) ? { ...p, eliminated: true } : p)),
        };
      }

      case "playerPresence": {
        const v = body.value;
        const info = v.player;
        if (info === undefined) return s;
        const players = mergePlayer(s.players, info);
        return {
          ...s,
          players,
          ...selfFacts(players, s.selfId),
        };
      }

      case "yourWord":
        return { ...s, word: body.value.word };

      case "snapshot":
        return this.fromSnapshot(s, body.value);

      case "error": {
        const v = body.value;
        // A kick is the one error that invalidates everything else on screen.
        // The seat is gone, so leaving the roster and phase in place would sit
        // the removed player in a lobby that no longer contains them; reset to
        // the initial state, which routes to home on selfId === "", and carry
        // only the reason across so home can render it.
        if (v.code === ErrorCode.KICKED) {
          return {
            ...initialState(),
            connection: s.connection,
            rttMs: s.rttMs,
            lastError: v.message,
            lastErrorCode: ErrorCode.KICKED,
          };
        }
        return {
          ...s,
          busy: false,
          lastError: v.message,
          lastErrorCode: v.code === ErrorCode.UNSPECIFIED ? null : v.code,
        };
      }

      default: {
        // Exhaustive: a new ServerEvent variant lands here and fails to build.
        const unreachable: never = body;
        void unreachable;
        return s;
      }
    }
  }

  // ---- reducer helpers --------------------------------------------------

  private fromSnapshot(s: GameState, v: Snapshot): GameState {
    const players = [...v.players];
    return {
      ...s,
      selfId: v.playerId,
      roomCode: v.roomCode,
      joinUrl: joinUrlFor(v.roomCode),
      phase: v.phase,
      round: v.round,
      totalRounds: v.totalRounds,
      settings: v.settings ?? s.settings,
      players,
      turnOrder: [...v.turnOrder],
      turnIndex: v.turnIndex,
      artistId: v.artistId,
      nextArtistId: v.nextArtistId,
      deadline: this.deadlineFrom(v.remainingMs),
      // Snapshot reports the time left but not the phase's full length, so the
      // denominator is reconstructed from the settings the same snapshot
      // carries. Never shorter than what remains.
      durationMs: Math.max(v.remainingMs, phaseLength(v)),
      word: v.yourWord,
      youHaveVoted: v.youHaveVoted,
      // A vote is anonymous: the server confirms THAT you voted, never again
      // WHAT you voted, so a resync cannot restore the choice itself.
      yourVote: v.youHaveVoted ? s.yourVote : null,
      votesCast: v.votesCast,
      activeCount: v.activeCount,
      ...selfFacts(players, v.playerId),
    };
  }

  /**
   * Turns the server's relative "milliseconds left" into a monotonic local
   * deadline, aged by half the measured round trip because the value was true
   * when the server sent it, not when we parsed it.
   */
  private deadlineFrom(remainingMs: number): number | null {
    if (remainingMs <= 0) return null;
    return this.now() + remainingMs - this.latencyLead;
  }

}

function selfFacts(
  players: readonly PlayerInfo[],
  selfId: string,
): Pick<GameState, "isHost" | "youAreEliminated"> {
  const me = players.find((p) => p.id === selfId);
  return {
    isHost: me?.isHost ?? false,
    youAreEliminated: me?.eliminated ?? false,
  };
}

/**
 * Applies one presence update. A presence carrying `isHost` is also the host
 * migration announcement, and the server only sends the promoted player — so
 * the old host's flag is cleared here rather than waited on.
 */
function mergePlayer(players: readonly PlayerInfo[], info: PlayerInfo): PlayerInfo[] {
  const cleared = info.isHost
    ? players.map((p) => (p.id === info.id || !p.isHost ? p : { ...p, isHost: false }))
    : players;
  const idx = cleared.findIndex((p) => p.id === info.id);
  if (idx === -1) return [...cleared, info].sort((a, b) => a.seat - b.seat);
  const next = [...cleared];
  next[idx] = info;
  return next;
}

/** Clears what belongs to one round. Survives: roster, settings, my word. */
function clearRound(s: GameState): GameState {
  return { ...s, tally: null, elimination: null, votesCast: 0, youHaveVoted: false, yourVote: null };
}

/**
 * Clears what one round leaves behind at the boundary between two of them.
 *
 * Between clearRound and clearMatch. The word goes — a fresh pair is dealt
 * every round, and holding the old one on screen while the reveal loads reads
 * as the new word for a frame. The turn sequence goes, because the next round
 * reshuffles it. Elimination, the roster and the round counter all stay: they
 * are properties of the match, and the match is still running.
 */
function clearRoundBoundary(s: GameState): GameState {
  return {
    ...clearRound(s),
    turnOrder: [],
    turnIndex: 0,
    artistId: "",
    nextArtistId: "",
    word: "",
  };
}

/** Clears everything that belongs to one match. */
function clearMatch(s: GameState): GameState {
  return {
    ...clearRound(s),
    round: 0,
    turnOrder: [],
    turnIndex: 0,
    artistId: "",
    nextArtistId: "",
    deadline: null,
    durationMs: 0,
    word: "",
    youAreEliminated: false,
    activeCount: 0,
    spectator: null,
    matchEnd: null,
  };
}

function phaseLength(v: Snapshot): number {
  switch (v.phase) {
    case Phase.ASSIGNING:
      return ASSIGN_DURATION_MS;
    case Phase.DRAWING:
      return (v.settings?.drawSeconds ?? 0) * 1000;
    case Phase.INTERMISSION:
      return (v.settings?.intermissionSeconds ?? 0) * 1000;
    case Phase.DISCUSSION:
      return (v.settings?.discussSeconds ?? 0) * 1000;
    case Phase.RESOLVING:
      return RESOLVE_DURATION_MS;
    default:
      return 0;
  }
}
