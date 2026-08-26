// The WebSocket client: one socket, binary protobuf frames, automatic
// reconnection into the same seat, and `seq` gap detection.
//
// This module owns everything about *the connection* and nothing about the
// game. It hands decoded frames to subscribers in arrival order and only reads
// what it needs to stay connected and in sync:
//
//   joined  -> remember the seat token, so a socket drop can be reclaimed
//   error   -> when it answers our own join, decide retry vs. give up
//   seq     -> a gap means ask for a whole Snapshot, never patch (§4.7)
//
// The seat token lives in `sessionStorage`, never `localStorage`: two tabs are
// two players, and a shared seat would have them fight over one room seat.

import { ErrorCode, Phase } from "../../gen/verso/v1/game_pb.js";
import { buildCommand, decodeServerEvent, encodeCommand } from "./codec.js";
import * as cmd from "./commands.js";
import { isSequenced, narrowErrorCode, narrowPhase } from "./protocol.js";
import type { ClientCommandBody, ServerEventOf, ServerFrame } from "./protocol.js";

export type ConnectionStatus =
  /** No socket and no intent yet. */
  | "idle"
  /** First attempt for the current intent. */
  | "connecting"
  /** Socket open and the seat granted. */
  | "open"
  /** Was connected, lost it, backing off before the next attempt. */
  | "reconnecting"
  /** Given up: either the caller asked, or the server refused for good. */
  | "closed";

/** Why the socket stopped trying. `null` while it is still trying. */
export interface ConnectionFailure {
  readonly code: ErrorCode;
  readonly message: string;
}

export interface ConnectionState {
  readonly status: ConnectionStatus;
  /** Consecutive failed attempts. Back to 0 once a seat is granted. */
  readonly attempt: number;
  /** Milliseconds until the next attempt; 0 when not waiting on one. */
  readonly retryInMs: number;
  readonly failure: ConnectionFailure | null;
  /** Smoothed round-trip time in ms; 0 before the first sample. */
  readonly rttMs: number;
}

/** What to join. An empty `roomCode` asks the server to mint a new room. */
export interface JoinIntent {
  readonly roomCode: string;
  readonly displayName: string;
}

export interface SeatRecord {
  readonly roomCode: string;
  readonly playerId: string;
  readonly seatToken: string;
}

export interface SocketOptions {
  /** Defaults to `/ws` on the page's own origin. */
  url?: string;
  /** Monotonic clock. Defaults to `performance.now`. */
  now?: () => number;
  /** Where the seat token is kept. Defaults to `sessionStorage`. */
  storage?: Storage | null;
  /** Diagnostics sink. Never receives a word or a seat token. */
  log?: (message: string, detail?: unknown) => void;
}

const SEAT_KEY = "verso.seat";

// Backoff: exponential, capped, half-jittered. The floor keeps a server that
// accepts TCP and immediately closes from becoming a spin loop.
const BACKOFF_BASE_MS = 400;
const BACKOFF_FACTOR = 1.8;
const BACKOFF_CAP_MS = 10_000;

/** How long a resync request waits before another one is allowed. */
const RESYNC_COOLDOWN_MS = 2_000;

/** Correlation entries older than this stop being usable as RTT samples. */
const RTT_TTL_MS = 30_000;

/**
 * Join failures that mean "do not reconnect". Everything else is transient: a
 * dropped socket, a rate limit, a server that is briefly unreachable.
 */
const FATAL_JOIN_CODES: ReadonlySet<ErrorCode> = new Set([
  ErrorCode.ROOM_FULL,
  ErrorCode.MATCH_IN_PROGRESS,
  ErrorCode.PROTOCOL_VERSION,
]);

type SequencedEvent = ServerEventOf<"strokeBegan" | "strokePoints" | "strokeEnded">;
type FrameListener = (frame: ServerFrame) => void;
type StateListener = (state: ConnectionState) => void;

export class VersoSocket {
  private readonly url: string;
  private readonly now: () => number;
  private readonly storage: Storage | null;
  private readonly log: (message: string, detail?: unknown) => void;

  private ws: WebSocket | null = null;
  private intent: JoinIntent | null = null;
  private seat: SeatRecord | null = null;

  private status: ConnectionStatus = "idle";
  private attempt = 0;
  private failure: ConnectionFailure | null = null;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private retryAt = 0;

  /** cid of the join frame this socket is waiting on, or "". */
  private joinCid = "";
  /** One token-less retry per socket, so a stale token cannot loop. */
  private triedFreshJoin = false;

  private cidCounter = 0;
  private readonly pending = new Map<string, number>();
  private rtt = 0;

  /** Highest applied stroke seq, or null when there is no baseline yet. */
  private lastSeq: number | null = null;
  /** Stroke ids begun and not yet ended: the second gap detector. */
  private readonly openStrokeIds = new Set<number>();
  private resyncAt = 0;
  private resyncing = false;

  private readonly frameListeners = new Set<FrameListener>();
  private readonly stateListeners = new Set<StateListener>();
  private readonly onOnline = () => {
    // The OS says the network is back; do not sit out the rest of the backoff.
    if (this.status === "reconnecting") this.retryNow();
  };

  constructor(options: SocketOptions = {}) {
    this.url = options.url ?? defaultUrl();
    this.now = options.now ?? (() => performance.now());
    this.storage = options.storage !== undefined ? options.storage : safeSessionStorage();
    this.log = options.log ?? (() => {});
    this.seat = this.loadSeat();
    globalThis.addEventListener?.("online", this.onOnline);
  }

  // ------------------------------------------------------------------------
  // subscriptions
  // ------------------------------------------------------------------------

  /** Every decoded frame, in arrival order. Returns an unsubscribe. */
  onFrame(fn: FrameListener): () => void {
    this.frameListeners.add(fn);
    return () => {
      this.frameListeners.delete(fn);
    };
  }

  /** Connection lifecycle. Fires immediately with the current state. */
  onState(fn: StateListener): () => void {
    this.stateListeners.add(fn);
    fn(this.state());
    return () => {
      this.stateListeners.delete(fn);
    };
  }

  state(): ConnectionState {
    const retryInMs =
      this.retryTimer === null ? 0 : Math.max(0, Math.round(this.retryAt - this.now()));
    return {
      status: this.status,
      attempt: this.attempt,
      retryInMs,
      failure: this.failure,
      rttMs: Math.round(this.rtt),
    };
  }

  /** The seat this socket holds, or null before the server grants one. */
  currentSeat(): SeatRecord | null {
    return this.seat;
  }

  // ------------------------------------------------------------------------
  // lifecycle
  // ------------------------------------------------------------------------

  /**
   * Connects and joins. Safe to call again to change rooms: the old socket is
   * torn down first. A stored seat is reused only for the same room code.
   */
  connect(intent: JoinIntent): void {
    this.teardown();
    this.intent = intent;
    if (intent.roomCode === "" || (this.seat !== null && this.seat.roomCode !== intent.roomCode)) {
      // Creating a room, or joining a different one: nothing to reclaim.
      this.clearSeat();
    }
    this.attempt = 0;
    this.failure = null;
    this.resetSync();
    this.openSocket("connecting");
  }

  /** Reconnects immediately, cancelling any pending backoff. */
  retryNow(): void {
    if (this.intent === null) return;
    this.clearRetryTimer();
    this.failure = null;
    this.openSocket(this.seat === null ? "connecting" : "reconnecting");
  }

  /** Closes for good, forgets the seat, and stops reconnecting. */
  disconnect(): void {
    this.teardown();
    this.clearSeat();
    this.intent = null;
    this.resetSync();
    this.setStatus("closed");
  }

  /** Releases the `online` listener. Call from a page teardown, if ever. */
  dispose(): void {
    this.disconnect();
    globalThis.removeEventListener?.("online", this.onOnline);
    this.frameListeners.clear();
    this.stateListeners.clear();
  }

  // ------------------------------------------------------------------------
  // sending
  // ------------------------------------------------------------------------

  /**
   * Fire-and-forget: no correlation id, so no reply is matched to it and no
   * RTT sample is taken. This is the path for stroke traffic.
   *
   * Returns false when the socket is not open. Frames are deliberately not
   * queued for a later flush — replaying ink after a reconnect would draw it
   * into somebody else's turn.
   */
  send(body: ClientCommandBody): boolean {
    return this.write(body, "");
  }

  /**
   * Sends with a correlation id and returns it. The id comes back on the
   * `ServerEvent` this command produced, which is how an `error` is tied to
   * the command that caused it, and how RTT is measured.
   */
  request(body: ClientCommandBody): string {
    const cid = this.nextCid();
    this.pending.set(cid, this.now());
    this.prunePending();
    this.write(body, cid);
    return cid;
  }

  // ------------------------------------------------------------------------
  // internals: socket
  // ------------------------------------------------------------------------

  private openSocket(status: ConnectionStatus): void {
    if (this.intent === null) return;
    this.closeWs();

    let ws: WebSocket;
    try {
      ws = new WebSocket(this.url);
    } catch (err) {
      this.log("websocket construction failed", err);
      this.scheduleRetry();
      return;
    }
    // Before anything else. The browser default is "blob", which forces an
    // async hop on every frame and is undocumented in protobuf-es.
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    this.setStatus(status);

    ws.onopen = () => {
      if (this.ws !== ws) return;
      this.triedFreshJoin = false;
      this.sendJoin();
    };
    ws.onmessage = (ev: MessageEvent<unknown>) => {
      if (this.ws !== ws) return;
      const { data } = ev;
      if (!(data instanceof ArrayBuffer)) {
        // A text frame is a confused peer: the protocol is binary only.
        this.log("non-binary frame ignored");
        return;
      }
      const frame = decodeServerEvent(data);
      if (frame === null) {
        // Unparseable, or an envelope with an unset oneof. Neither can be
        // acted on, and neither should reach a switch downstream.
        this.log("undecodable frame ignored");
        return;
      }
      this.handleFrame(frame);
    };
    ws.onerror = () => {
      this.log("websocket error");
    };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      if (this.status === "closed") return;
      this.scheduleRetry();
    };
  }

  private sendJoin(): void {
    if (this.intent === null) return;
    const seat = this.seat;
    this.joinCid = this.request(
      cmd.joinRoom({
        // Reconnecting always names the room: transport refuses a seat token
        // without one.
        roomCode: seat?.roomCode ?? this.intent.roomCode,
        displayName: this.intent.displayName,
        seatToken: seat?.seatToken ?? "",
      }),
    );
  }

  private write(body: ClientCommandBody, cid: string): boolean {
    const ws = this.ws;
    if (ws === null || ws.readyState !== WebSocket.OPEN) return false;
    try {
      ws.send(encodeCommand(buildCommand(body, cid)));
      return true;
    } catch (err) {
      this.log("send failed", err);
      return false;
    }
  }

  private scheduleRetry(): void {
    if (this.intent === null || this.failure !== null) {
      this.setStatus("closed");
      return;
    }
    this.attempt += 1;
    const ceiling = Math.min(
      BACKOFF_CAP_MS,
      BACKOFF_BASE_MS * BACKOFF_FACTOR ** (this.attempt - 1),
    );
    // Half-jitter: never shorter than half the ceiling, never longer than it.
    const delay = ceiling / 2 + Math.random() * (ceiling / 2);
    this.retryAt = this.now() + delay;
    this.clearRetryTimer();
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      this.openSocket("reconnecting");
    }, delay);
    this.setStatus("reconnecting");
    this.emitState();
  }

  private clearRetryTimer(): void {
    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
  }

  private closeWs(): void {
    const ws = this.ws;
    this.ws = null;
    if (ws === null) return;
    ws.onopen = null;
    ws.onmessage = null;
    ws.onerror = null;
    ws.onclose = null;
    try {
      ws.close();
    } catch {
      // Already closing.
    }
  }

  private teardown(): void {
    this.clearRetryTimer();
    this.closeWs();
    this.pending.clear();
    this.joinCid = "";
  }

  private setStatus(next: ConnectionStatus): void {
    if (this.status === next) return;
    this.status = next;
    this.emitState();
  }

  private emitState(): void {
    const snapshot = this.state();
    for (const fn of this.stateListeners) fn(snapshot);
  }

  // ------------------------------------------------------------------------
  // internals: frames
  // ------------------------------------------------------------------------

  private handleFrame(frame: ServerFrame): void {
    this.sampleRtt(frame.cid);
    const body = frame.body;

    switch (body.case) {
      case "joined": {
        const j = body.value;
        this.seat = { roomCode: j.roomCode, playerId: j.playerId, seatToken: j.seatToken };
        this.saveSeat(this.seat);
        if (this.intent !== null && this.intent.roomCode !== j.roomCode) {
          // The server minted the code for us.
          this.intent = { roomCode: j.roomCode, displayName: this.intent.displayName };
        }
        this.attempt = 0;
        this.failure = null;
        this.joinCid = "";
        this.setStatus("open");
        this.emitState();
        break;
      }
      case "error":
        if (frame.cid !== "" && frame.cid === this.joinCid) {
          if (this.handleJoinError(body.value.code, body.value.message)) return;
        }
        break;
      case "snapshot":
        // A Snapshot is the whole truth: it re-baselines the sequence and
        // closes any resync in flight.
        this.lastSeq = body.value.seq;
        this.openStrokeIds.clear();
        this.resyncing = false;
        break;
      case "phaseChanged":
      case "lobbyState":
        this.maybeResetBaseline(body.value.phase);
        break;
      default:
        if (isSequenced(body) && !this.acceptSequenced(body)) return;
        break;
    }

    for (const fn of this.frameListeners) fn(frame);
  }

  /**
   * Decides what to do about an `error` that answers our own join. Returns
   * true when the frame was consumed and must not be forwarded.
   */
  private handleJoinError(rawCode: number, message: string): boolean {
    const code = narrowErrorCode(rawCode);
    this.joinCid = "";

    const usedToken = this.seat !== null;
    const staleSeat =
      code === ErrorCode.BAD_SEAT || (usedToken && code === ErrorCode.ROOM_NOT_FOUND);
    if (staleSeat && !this.triedFreshJoin && this.intent !== null && this.intent.displayName !== "") {
      // The grace window ran out, or the room outlived our seat. Drop the token
      // and take a new one on this same socket: transport allows a second join
      // because the first never bound a seat.
      this.triedFreshJoin = true;
      const roomCode = this.seat?.roomCode ?? this.intent.roomCode;
      this.clearSeat();
      this.resetSync();
      this.intent = { roomCode, displayName: this.intent.displayName };
      this.sendJoin();
      return true;
    }

    if (FATAL_JOIN_CODES.has(code) || staleSeat) {
      this.failure = { code, message };
      this.clearRetryTimer();
      this.closeWs();
      this.setStatus("closed");
      this.emitState();
      return false;
    }
    // Anything else — rate limited, a transient refusal — rides the normal
    // backoff once the socket closes.
    return false;
  }

  /** Gap detection. Returns false when the frame must be dropped. */
  private acceptSequenced(body: SequencedEvent): boolean {
    if (this.resyncing) return false;

    const { seq, strokeId } = body.value;

    if (this.lastSeq !== null && seq !== this.lastSeq + 1) {
      this.log("stroke seq gap", { expected: this.lastSeq + 1, got: seq });
      this.requestResync();
      return false;
    }

    // Second detector: geometry for a stroke we never saw begin means the
    // opening frame was lost even though the sequence looked contiguous, which
    // is what a lost frame looks like on the first event after a reset.
    if (body.case === "strokeBegan") {
      this.openStrokeIds.add(strokeId);
    } else if (!this.openStrokeIds.has(strokeId)) {
      this.log("stroke geometry for an unknown stroke", { strokeId });
      this.lastSeq = seq;
      this.requestResync();
      return false;
    }
    if (body.case === "strokeEnded") this.openStrokeIds.delete(strokeId);

    this.lastSeq = seq;
    return true;
  }

  private requestResync(): void {
    const now = this.now();
    if (this.resyncing && now - this.resyncAt < RESYNC_COOLDOWN_MS) return;
    this.resyncing = true;
    this.resyncAt = now;
    // have_seq is advisory; the server always answers with the whole state.
    this.request(cmd.requestSnapshot(this.lastSeq ?? 0));
  }

  private maybeResetBaseline(rawPhase: number): void {
    // Dealing a match and returning to the lobby both empty the server's
    // stroke log without emitting a stroke event. Drop the baseline so the
    // next stroke re-establishes it instead of reporting a phantom gap.
    const phase = narrowPhase(rawPhase);
    if (phase === Phase.LOBBY || phase === Phase.ASSIGNING) this.resetSync();
  }

  private resetSync(): void {
    this.lastSeq = null;
    this.openStrokeIds.clear();
    this.resyncing = false;
  }

  // ------------------------------------------------------------------------
  // internals: correlation and RTT
  // ------------------------------------------------------------------------

  private nextCid(): string {
    this.cidCounter += 1;
    return `c${this.cidCounter.toString(36)}`;
  }

  private sampleRtt(cid: string): void {
    if (cid === "") return;
    const sentAt = this.pending.get(cid);
    if (sentAt === undefined) return;
    // Only the first reply to a command is a round trip; the server may emit
    // several events under one cid, and the later ones are queued work.
    this.pending.delete(cid);
    const sample = this.now() - sentAt;
    if (sample < 0 || sample > RTT_TTL_MS) return;
    this.rtt = this.rtt === 0 ? sample : this.rtt * 0.8 + sample * 0.2;
  }

  private prunePending(): void {
    if (this.pending.size < 64) return;
    const cutoff = this.now() - RTT_TTL_MS;
    for (const [cid, at] of this.pending) {
      if (at < cutoff) this.pending.delete(cid);
    }
  }

  // ------------------------------------------------------------------------
  // internals: seat persistence
  // ------------------------------------------------------------------------

  private loadSeat(): SeatRecord | null {
    let raw: string | null = null;
    try {
      raw = this.storage?.getItem(SEAT_KEY) ?? null;
    } catch {
      return null;
    }
    if (raw === null) return null;
    try {
      const parsed: unknown = JSON.parse(raw);
      if (
        typeof parsed === "object" &&
        parsed !== null &&
        typeof (parsed as SeatRecord).roomCode === "string" &&
        typeof (parsed as SeatRecord).playerId === "string" &&
        typeof (parsed as SeatRecord).seatToken === "string"
      ) {
        return parsed as SeatRecord;
      }
    } catch {
      // Corrupt entry; treat it as absent.
    }
    return null;
  }

  private saveSeat(seat: SeatRecord): void {
    try {
      this.storage?.setItem(SEAT_KEY, JSON.stringify(seat));
    } catch {
      // Storage disabled or full: reconnect degrades to a fresh join.
    }
  }

  private clearSeat(): void {
    this.seat = null;
    try {
      this.storage?.removeItem(SEAT_KEY);
    } catch {
      // Nothing to do.
    }
  }
}

// --------------------------------------------------------------------------
// module helpers
// --------------------------------------------------------------------------

function defaultUrl(): string {
  const loc = globalThis.location as Location | undefined;
  const scheme = loc?.protocol === "https:" ? "wss:" : "ws:";
  const host = loc?.host ?? "localhost:8080";
  return `${scheme}//${host}/ws`;
}

function safeSessionStorage(): Storage | null {
  try {
    return globalThis.sessionStorage ?? null;
  } catch {
    // Blocked by a privacy setting.
    return null;
  }
}
