import "@fontsource-variable/nunito";
import "./styles/index.css";

import { Phase } from "../gen/verso/v1/game_pb.js";
import { CanvasEngine } from "./canvas/index.js";
import { castVote, rematch, requestSnapshot, setReady, startMatch, strokeBegin, strokeEnd, strokePoints, updateSettings } from "./net/commands.js";
import { VersoSocket } from "./net/socket.js";
import { GameStore } from "./state/store.js";
import { setDeepLink } from "./state/routes.js";
import { createChrome, screens, type Actions, type CanvasHandle, type ScreenCtx, type ViewState } from "./ui/index.js";

const root = document.querySelector<HTMLElement>("#app");
if (!root) throw new Error("Verso could not find its app root");

const store = new GameStore();
const socket = new VersoSocket();
const engine = new CanvasEngine({
  outbound: {
    strokeBegin: (colorIndex, width, points) => socket.send(strokeBegin({ colorIndex, width, points })),
    strokePoints: (points) => socket.send(strokePoints(points)),
    strokeEnd: (points) => socket.send(strokeEnd(points)),
    requestSnapshot: (haveSeq) => socket.request(requestSnapshot(haveSeq)),
  },
});

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
  setColorIndex(index) {
    engine.setColorIndex(index);
  },
  setWidth(width) {
    engine.setWidth(width);
  },
  async savePng() {
    await engine.downloadPng();
  },
};

const actions: Actions = {
  createRoom(displayName) {
    socket.connect({ roomCode: "", displayName });
  },
  joinRoom(roomCode, displayName) {
    socket.connect({ roomCode, displayName });
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
  requestSnapshot() {
    socket.request(requestSnapshot(0));
  },
};

const chrome = createChrome(root);
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
  // Leave an incoming fragment alone until a room has actually been joined:
  // the home screen reads it to prefill a deep-link join code.
  if (state.roomCode !== "") setDeepLink(state.roomCode);
  if (state.screen === mounted) return;
  if (mounted !== null) screens[mounted].unmount();
  mounted = state.screen;
  screens[mounted].mount(chrome.screenRoot, ctx);
}

store.subscribe(render);
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
      if (frame.body.value.phase === Phase.LOBBY || frame.body.value.phase === Phase.ASSIGNING) {
        engine.reset();
      }
      break;
  }
  store.apply(frame);
});
