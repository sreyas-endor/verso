// Which cue, if any, a state change is worth. Pure functions over two states,
// deliberately with no access to the engine, so the interesting half of the
// audio layer can be reasoned about — and later tested — without a browser.
//
// The client never decides that a phase ended (see GameState.deadline): every
// cue below is a reaction to something the server has already said. That is
// why this is a diff of two authoritative states rather than a set of hooks
// hung off the UI, which would fire on remount and go silent across a screen
// change.

import { Phase, WinnerSide } from "../../gen/verso/v1/game_pb.js";
import type { GameState } from "../state/types.js";
import { VOTE_TICKS, type CueName } from "./cues.js";

/** Exactly the state the cue rules read. */
export type AudioFacts = Pick<
  GameState,
  | "selfId"
  | "phase"
  | "artistId"
  | "nextArtistId"
  | "deadline"
  | "durationMs"
  | "matchEnd"
  | "youHaveVoted"
  | "youAreEliminated"
  | "eliminationSeq"
>;

/**
 * How long before your own turn *starts* the handoff countdown runs, in ticks
 * of one second. This is the cue that stops a player missing their own turn, so
 * it is the longest run-up in the game.
 */
export const PRE_TURN_TICKS = 5;

/** Ticks before your own turn *expires*, and how far ahead they start. */
export const END_OF_TURN_TICKS = 3;
export const END_OF_TURN_LEAD_MS = END_OF_TURN_TICKS * 1000;

/**
 * How long before voting closes the vote countdown starts.
 *
 * Ten seconds rather than the three a turn gets. A turn ending is a thing that
 * happens *to* the artist and needs no more than a warning; voting closing is
 * something a player still has to act on, and a vote takes reading the roster
 * and making up your mind. Ten seconds is enough to finish an argument and
 * still press a name.
 *
 * MIN_DISCUSS_SECONDS is 30 (web/src/net/protocol.ts:47), so this always fits
 * inside the phase. tickPlanFor still checks, because that bound belongs to
 * the settings validator and not to this file.
 */
export const VOTE_LEAD_MS = 10_000;

/**
 * Clear air a handoff needs between "you are next" and the countdown's first
 * tick for both to be worth playing. Below this the two collide and the
 * announcement is dropped, because the ticks say the same thing for longer.
 */
const ANNOUNCE_HEADROOM_MS = 1500;

/**
 * The cue for one state change, or null for silence.
 *
 * At most one cue per change, by design. Two sounds landing together is heard
 * as one unfamiliar sound, so where a change could justify two — a phase
 * boundary that also swaps the artist — the more urgent one wins and the other
 * is dropped.
 */
export function cueFor(prev: AudioFacts, next: AudioFacts): CueName | null {
  // Nothing to narrate until this client actually holds a seat.
  if (next.selfId === "") return null;

  // The end of a match arrives as MatchEnded, which sets the phase itself, and
  // may also arrive as a PhaseChanged — in either order. Keying off the payload
  // rather than the phase makes it fire exactly once, and only once the winning
  // side is actually known.
  if (prev.matchEnd === null && next.matchEnd !== null) return endCue(next);

  // Somebody was voted out. Keyed off the counter rather than off `elimination`
  // for the same reason the cinematic is: the payload sits in the state for the
  // whole of RESOLVING and survives a resync, and only this counter separates
  // watching it happen from being told it already had.
  //
  // It cannot collide with anything below. A PlayerEliminated arrives on its
  // own frame, changing no phase and no artist, so the rest of this function
  // would return null for it anyway.
  if (next.eliminationSeq !== prev.eliminationSeq && next.eliminationSeq > 0) return "ejection";

  if (next.phase !== prev.phase) {
    switch (next.phase) {
      case Phase.ASSIGNING:
        return "wordsDealt";
      case Phase.DISCUSSION:
        return "discussion";
      case Phase.RESOLVING:
        return "resolve";
      case Phase.INTERMISSION:
        return handoffCue(next);
      case Phase.DRAWING:
        // Entering DRAWING clears the artist (see the store's phaseChanged
        // case); the cue comes from the TurnStarted that follows, below.
        return turnCue(prev, next);
      case Phase.ENDED:
        // Waiting on MatchEnded for the side. Handled above.
        return null;
      case Phase.LOBBY:
      case Phase.UNSPECIFIED:
        return null;
      default:
        return null;
    }
  }

  if (next.phase === Phase.DRAWING) return turnCue(prev, next);
  if (next.phase === Phase.INTERMISSION && next.nextArtistId !== prev.nextArtistId) {
    return handoffCue(next);
  }
  return null;
}

/** A pending run of one-second ticks. */
export interface TickPlan {
  /** `performance.now()` reading at which the first tick sounds. */
  readonly at: number;
  /** Counting down to your turn starting, to it expiring, or to voting closing. */
  readonly kind: "preTurn" | "endOfTurn" | "vote";
  readonly ticks: number;
}

/**
 * The countdown this state calls for, or null for silence.
 *
 * Every countdown here is for players who can act on it, and silent for
 * everyone else. A run of ticks in ten pairs of ears, ten times a round, is
 * noise: a player watching somebody else draw has nothing to do when that turn
 * runs out. The two turn countdowns therefore narrow to one player.
 *
 * The vote countdown is the exception, and deliberately so — voting is the one
 * deadline every active player has to beat, so it plays for all of them at
 * once. It still narrows: a player who has already voted, or who was
 * eliminated and is only watching, has nothing left to do and hears nothing.
 * That makes it self-silencing, which is the property that keeps it from
 * becoming the noise the other two avoid by being personal.
 *
 * The three can never overlap. They belong to INTERMISSION, DRAWING and
 * DISCUSSION respectively, so one pending timer is still enough for all of
 * them.
 */
export function tickPlanFor(s: AudioFacts): TickPlan | null {
  if (s.deadline === null) return null;

  // Counting into your own turn. The whole point of this cue: an announcement
  // at the top of a ten-second handoff is easy to miss, so the last seconds
  // are counted out loud.
  if (s.phase === Phase.INTERMISSION) {
    if (s.nextArtistId === "" || s.nextArtistId !== s.selfId) return null;
    const ticks = Math.min(PRE_TURN_TICKS, Math.floor(s.durationMs / 1000));
    if (ticks <= 0) return null;
    return { kind: "preTurn", ticks, at: s.deadline - ticks * 1000 };
  }

  // Counting your own turn out.
  if (s.phase === Phase.DRAWING) {
    if (s.artistId === "" || s.artistId !== s.selfId) return null;
    return { kind: "endOfTurn", ticks: END_OF_TURN_TICKS, at: s.deadline - END_OF_TURN_LEAD_MS };
  }

  // Counting the vote out, for everyone still able to cast one. youHaveVoted
  // going true is what cancels a pending run — the driver re-derives this on
  // every state, and the vote acknowledgement is a state change like any
  // other.
  if (s.phase === Phase.DISCUSSION) {
    if (s.youAreEliminated || s.youHaveVoted) return null;
    if (s.durationMs <= VOTE_LEAD_MS) return null;
    return { kind: "vote", ticks: VOTE_TICKS, at: s.deadline - VOTE_LEAD_MS };
  }

  return null;
}

function turnCue(prev: AudioFacts, next: AudioFacts): CueName | null {
  // An unchanged artist is a state change about something else — a roster
  // update, a vote count, a resync that told us what we already knew.
  if (next.artistId === "" || next.artistId === prev.artistId) return null;
  return next.artistId === next.selfId ? "yourTurn" : "turnChange";
}

function handoffCue(next: AudioFacts): CueName | null {
  if (next.nextArtistId === "" || next.nextArtistId !== next.selfId) return "handoff";
  // On a short handoff the countdown starts the moment the handoff does, and
  // the announcement would land on top of its first tick. The ticks win: they
  // carry the same message across more of the window.
  const plan = tickPlanFor(next);
  if (plan !== null && next.durationMs - plan.ticks * 1000 < ANNOUNCE_HEADROOM_MS) return null;
  return "youAreNext";
}

function endCue(next: AudioFacts): CueName | null {
  const m = next.matchEnd;
  if (m === null) return null;
  if (m.winner === WinnerSide.UNSPECIFIED) return null;
  const iAmImposter = next.selfId !== "" && m.imposterPlayerIds.includes(next.selfId);
  const side = iAmImposter ? WinnerSide.IMPOSTER : WinnerSide.GROUP;
  return m.winner === side ? "win" : "loss";
}
