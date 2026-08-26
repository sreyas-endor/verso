import { create } from "@bufbuild/protobuf";
import { Difficulty, type MatchSettings, MatchSettingsSchema } from "../../gen/verso/v1/game_pb.js";
import { LIMITS, RECOMMENDED } from "./context.js";
import { Disposers, el, setText } from "./dom.js";

export interface SettingsPanelView {
  root: HTMLElement;
  update(settings: MatchSettings, editable: boolean): void;
  dispose(): void;
}

const DIFFICULTIES: ReadonlyArray<[Difficulty, string]> = [
  [Difficulty.EASY, "Easy"],
  [Difficulty.MEDIUM, "Medium"],
  [Difficulty.HARD, "Hard"],
];

function labelled(head: string, recommended: string, control: HTMLElement, value: HTMLElement | null): HTMLElement {
  return el(
    "div",
    { class: "setting" },
    el(
      "div",
      { class: "setting-head" },
      el("span", { text: head }),
      el("span", { class: "setting-rec", text: `rec. ${recommended}` }),
      value,
    ),
    control,
  );
}

/**
 * Host-only match configuration. Non-hosts get the identical panel with every
 * control disabled, so a spectating player still watches the values move.
 */
export function settingsPanel(onChange: (next: MatchSettings) => void): SettingsPanelView {
  const d = new Disposers();
  let current: MatchSettings = create(MatchSettingsSchema, {
    difficulty: RECOMMENDED.difficulty,
    maxRounds: RECOMMENDED.maxRounds,
    drawSeconds: RECOMMENDED.drawSeconds,
    discussSeconds: RECOMMENDED.discussSeconds,
    intermissionSeconds: RECOMMENDED.intermissionSeconds,
  });
  let editable = false;

  const emit = (patch: Partial<MatchSettings>) => {
    if (!editable) return;
    onChange(create(MatchSettingsSchema, {
      difficulty: patch.difficulty ?? current.difficulty,
      maxRounds: patch.maxRounds ?? current.maxRounds,
      drawSeconds: patch.drawSeconds ?? current.drawSeconds,
      discussSeconds: patch.discussSeconds ?? current.discussSeconds,
      intermissionSeconds: patch.intermissionSeconds ?? current.intermissionSeconds,
    }));
  };

  // Difficulty — segmented control.
  const diffBtns = DIFFICULTIES.map(([value, text]) => {
    const b = el("button", { type: "button", "aria-pressed": "false", text }) as HTMLButtonElement;
    d.on(b, "click", () => emit({ difficulty: value }));
    return b;
  });
  const seg = el("div", { class: "seg", role: "group", "aria-label": "Word-pair difficulty" }, ...diffBtns);

  // Rounds — stepper, because the range is 1..4.
  const roundsVal = el("span", { class: "stepper-val", text: "2" });
  const minus = el("button", { type: "button", class: "btn btn-sm", "aria-label": "Fewer rounds", text: "−" }) as HTMLButtonElement;
  const plus = el("button", { type: "button", class: "btn btn-sm", "aria-label": "More rounds", text: "+" }) as HTMLButtonElement;
  d.on(minus, "click", () => emit({ maxRounds: Math.max(LIMITS.minRounds, current.maxRounds - 1) }));
  d.on(plus, "click", () => emit({ maxRounds: Math.min(LIMITS.maxRounds, current.maxRounds + 1) }));
  const stepper = el(
    "div",
    { class: "stepper", role: "group", "aria-label": "Maximum rounds" },
    minus, roundsVal, plus,
  );

  // Two time sliders.
  const drawRange = el("input", {
    type: "range", min: LIMITS.minDrawSeconds, max: LIMITS.maxDrawSeconds, step: 1,
    "aria-label": "Drawing time per turn in seconds",
  }) as HTMLInputElement;
  const discussRange = el("input", {
    type: "range", min: LIMITS.minDiscussSeconds, max: LIMITS.maxDiscussSeconds, step: 5,
    "aria-label": "Discussion and decision time in seconds",
  }) as HTMLInputElement;
  const intermissionRange = el("input", {
    type: "range", min: LIMITS.minIntermissionSeconds, max: LIMITS.maxIntermissionSeconds, step: 1,
    "aria-label": "Handoff time between turns in seconds",
  }) as HTMLInputElement;
  d.on(drawRange, "input", () => emit({ drawSeconds: Number(drawRange.value) }));
  d.on(discussRange, "input", () => emit({ discussSeconds: Number(discussRange.value) }));
  d.on(intermissionRange, "input", () => emit({ intermissionSeconds: Number(intermissionRange.value) }));

  const diffVal = el("span", { class: "setting-val", text: "Medium" });
  const drawVal = el("span", { class: "setting-val", text: "15s" });
  const discussVal = el("span", { class: "setting-val", text: "120s" });
  const intermissionVal = el("span", { class: "setting-val", text: "10s" });
  const hostNote = el("p", { class: "hint", text: "Only the host can change these." });

  const root = el(
    "section",
    { class: "card col-right" },
    el("div", { class: "card-title", text: "Match settings" }),
    labelled("Difficulty", "Medium", seg, diffVal),
    labelled("Rounds", "2", stepper, null),
    labelled("Drawing turn", "15s", drawRange, drawVal),
    labelled("Discussion", "120s", discussRange, discussVal),
    labelled("Between turns", "10s", intermissionRange, intermissionVal),
    hostNote,
  );

  return {
    root,
    update(settings, canEdit) {
      current = settings;
      editable = canEdit;
      diffBtns.forEach((b, i) => {
        const [value] = DIFFICULTIES[i] ?? [Difficulty.UNSPECIFIED, ""];
        b.setAttribute("aria-pressed", String(settings.difficulty === value));
        b.disabled = !canEdit;
      });
      const named = DIFFICULTIES.find(([v]) => v === settings.difficulty);
      setText(diffVal, named ? named[1] : "—");
      setText(roundsVal, String(settings.maxRounds));
      setText(drawVal, `${settings.drawSeconds}s`);
      setText(discussVal, `${settings.discussSeconds}s`);
      setText(intermissionVal, `${settings.intermissionSeconds}s`);
      if (document.activeElement !== drawRange) drawRange.value = String(settings.drawSeconds);
      if (document.activeElement !== discussRange) discussRange.value = String(settings.discussSeconds);
      if (document.activeElement !== intermissionRange) intermissionRange.value = String(settings.intermissionSeconds);
      drawRange.disabled = !canEdit;
      discussRange.disabled = !canEdit;
      intermissionRange.disabled = !canEdit;
      minus.disabled = !canEdit || settings.maxRounds <= LIMITS.minRounds;
      plus.disabled = !canEdit || settings.maxRounds >= LIMITS.maxRounds;
      hostNote.hidden = canEdit;
    },
    dispose() {
      d.dispose();
    },
  };
}
