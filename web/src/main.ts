import "@fontsource-variable/nunito";
import "./styles/index.css";

import { Phase } from "../gen/verso/v1/game_pb.js";
import { createAudio, createAudioDriver } from "./audio/index.js";
import { CanvasEngine, LOGICAL_W, paint, renderPng, savePng, type ExportStroke } from "./canvas/index.js";
import { castVote, kickPlayer, rematch, requestSnapshot, setReady, startMatch, strokeBegin, strokeEnd, strokePoints, updateSettings } from "./net/commands.js";
import { VersoSocket } from "./net/socket.js";
import { GameStore } from "./state/store.js";
import { setDeepLink } from "./state/routes.js";
import { cinematic } from "./ui/cinematic.js";
import { createChrome, screens, type Actions, type CanvasHandle, type ScreenCtx, type ViewState } from "./ui/index.js";

const root = document.querySelector<HTMLElement>("#app");
if (!root) throw new Error("Verso could not find its app root");

const store = new GameStore();
const socket = new VersoSocket();
const audio = createAudio();
const engine = new CanvasEngine({
  outbound: {
    strokeBegin: (colorIndex, width, points) => socket.send(strokeBegin({ colorIndex, width, points })),
    strokePoints: (points) => socket.send(strokePoints(points)),
    strokeEnd: (points) => socket.send(strokeEnd(points)),
    // Not socket.request(requestSnapshot(...)): the socket owns `resyncing`
    // and the only bounded retry policy, so the engine's own sequence-gap
    // detector enters through it rather than firing a bare request that
    // nothing would follow up on if the answer were dropped.
    requestSnapshot: () => socket.resync(),
  },
});

/**
 * Finished canvases, keyed by round.
 *
 * Every round wipes the paper, so without this the final reveal could only ever
 * show the last round's drawing — MatchEnded carries no strokes, and the engine
 * holds exactly one canvas. A round is archived at the moment the server
 * announces the next one's word reveal, which is the last instant its vectors
 * still exist.
 *
 * Vectors, not bitmaps: a round is a few hundred stroke records, and the reveal
 * repaints them at whatever size it needs.
 */
const roundCanvases = new Map<number, ExportStroke[]>();

let canvasMounted = false;
const canvas: CanvasHandle = {
  attach(host) {
    if (!canvasMounted) {
      engine.mount(host);
      canvasMounted = true;
      return;
    }
    host.append(engine.element);
  },
  detach() {
    engine.setDrawingEnabled(false);
  },
  setInteractive(on) {
    engine.setDrawingEnabled(on);
  },
  setAcceptingNewStrokes(on) {
    engine.setAcceptingNewStrokes(on);
  },
  setColorIndex(index) {
    engine.setColorIndex(index);
  },
  setWidth(width) {
    engine.setWidth(width);
  },
  setStrokeLimit(limit) {
    engine.setStrokeLimit(limit);
  },
  strokeBudget() {
    return engine.strokeBudget();
  },
  async savePng() {
    await engine.downloadPng();
  },
  archivedRounds() {
    return [...roundCanvases.keys()].sort((a, b) => a - b);
  },
  paintRound(round, target) {
    const ctx = target.getContext("2d");
    if (!ctx) return;
    // paint() draws on the logical 1024x768 grid, so the scale is whatever
    // shrinks that onto this particular target — a thumbnail and the promoted
    // canvas run the identical code path at different scales.
    paint(ctx, roundCanvases.get(round) ?? [], target.width / LOGICAL_W);
  },
  async savePngForRound(round) {
    const blob = await renderPng(roundCanvases.get(round) ?? []);
    await savePng(blob, `verso-canvas-round-${round}`);
  },
};

/**
 * Keep the canvas the round just finished with.
 *
 * Called on the PHASE_ASSIGNING that opens the NEXT round, which is both the
 * last moment the vectors exist and the same transition that clears them — so
 * the archive can never be a frame late. `round` is the server's counter, which
 * during that reveal still names the round being left behind.
 *
 * An empty round is still archived: "nobody drew anything" is a real thing for
 * the reveal to show, and a missing entry would read as a gap in the match.
 */
function archiveRound(round: number): void {
  if (round < 1) return;
  roundCanvases.set(round, engine.committedStrokes());
}

const actions: Actions = {
  createRoom(displayName, avatar) {
    socket.connect({ roomCode: "", displayName, avatar });
  },
  joinRoom(roomCode, displayName, avatar) {
    socket.connect({ roomCode, displayName, avatar });
  },
  setReady(ready) {
    socket.request(setReady(ready));
  },
  updateSettings(settings) {
    socket.request(updateSettings(settings));
  },
  startMatch() {
    socket.request(startMatch());
  },
  castVote(choice) {
    socket.request(castVote(choice.case === "skip" ? { skip: true } : { candidateId: choice.value }));
  },
  rematch() {
    socket.request(rematch());
  },
  kickPlayer(playerId) {
    socket.request(kickPlayer(playerId));
  },
  requestSnapshot() {
    socket.request(requestSnapshot(0));
  },
};

const chrome = createChrome(root, {
  enabled: () => audio.enabled(),
  setEnabled(on) {
    audio.setEnabled(on);
    // The click that enabled it is also the gesture that unlocks the audio
    // context, so this is the first moment a cue can be heard — say so, or
    // "on" is indistinguishable from broken.
    if (on) audio.play("soundOn");
  },
});
// Mounted once, beside the screen root rather than inside it. An ejection runs
// for 4.7 s and PHASE_RESOLVING is 8 s, so the screen underneath can be swapped
// for the final reveal while the petals are still falling — an overlay owned by
// a screen would be torn out mid-animation.
const ejection = cinematic();
root.appendChild(ejection.root);

let mounted: keyof typeof screens | null = null;
const ctx: ScreenCtx = {
  state: () => store.getState(),
  subscribe: (fn) => store.subscribe(fn),
  actions,
  canvas,
  announce: (message) => chrome.announce(message),
  toast: (message, kind) => chrome.toast(message, kind),
};

function render(state: ViewState): void {
  chrome.render(state);
  ejection.update(state);
  // Leave an incoming fragment alone until a room has actually been joined:
  // the home screen reads it to prefill a deep-link join code.
  if (state.roomCode !== "") setDeepLink(state.roomCode);
  if (state.screen === mounted) return;
  if (mounted !== null) screens[mounted].unmount();
  mounted = state.screen;
  screens[mounted].mount(chrome.screenRoot, ctx);
}

store.subscribe(render);
// Cues are driven from the store, not from the screens: the transitions that
// matter most — one artist handing over to the next, drawing giving way to
// voting — straddle a screen unmount, and a per-screen hook would either miss
// them or re-fire on every remount.
store.subscribe(createAudioDriver(audio));
socket.onState((state) => {
  const connection = state.status === "open"
    ? "connected"
    : state.status === "closed"
      ? (socket.currentSeat() ? "dropped" : "offline")
      : state.status === "reconnecting"
        ? "reconnecting"
        : "connecting";
  store.setLatency(state.rttMs);
  store.patch({ connection, failure: state.failure, rttMs: state.rttMs });
});
socket.onFrame((frame) => {
  // The vector engine consumes the raw authoritative protobuf events. The
  // store independently records the same events for UI state and resync.
  switch (frame.body.case) {
    case "strokeBegan": engine.applyStrokeBegan(frame.body.value); break;
    case "strokePoints": engine.applyStrokePoints(frame.body.value); break;
    case "strokeEnded": engine.applyStrokeEnded(frame.body.value); break;
    case "snapshot": engine.replay(frame.body.value.strokes, frame.body.value.seq); break;
    case "phaseChanged":
      switch (frame.body.value.phase) {
        case Phase.ASSIGNING:
          // ASSIGNING opens every round, and the canvas it is about to clear is
          // the finished evidence of the round just played. Keep it before
          // resetting — after engine.reset() those vectors are gone. `round` is
          // 0 for the reveal that opens a match, which archiveRound ignores.
          archiveRound(frame.body.value.round);
          engine.reset();
          break;
        case Phase.LOBBY:
          // A rematch. Nothing from the last match belongs to the next one.
          roundCanvases.clear();
          engine.reset();
          break;
        case Phase.ENDED:
          // The final round has no next reveal to be archived by, and the
          // reveal screen wants every round through one code path — so catch
          // the last one here. The canvas is deliberately NOT reset: the match
          // is over and nothing else will draw on it.
          archiveRound(frame.body.value.round);
          break;
        default:
          break;
      }
      break;
  }
  store.apply(frame);
});
