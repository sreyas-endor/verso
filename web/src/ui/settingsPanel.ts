import { create } from "@bufbuild/protobuf";
import {
  Difficulty,
  EliminationResults,
  type MatchSettings,
  MatchSettingsSchema,
  PenRule,
} from "../../gen/verso/v1/game_pb.js";
import { LIMITS, RECOMMENDED } from "./context.js";
import { Disposers, el, setText } from "./dom.js";

export interface SettingsPanelView {
  root: HTMLElement;
  /**
   * `seated` is the roster size, which the imposter warning needs and no other
   * control does: two imposters at three players is legal, startable, and
   * unwinnable for the group (MULTIPLE_IMPOSTERS.md, "Three-player warning").
   */
  update(settings: MatchSettings, editable: boolean, seated: number): void;
  dispose(): void;
}

/**
 * What a valid elimination tells the room. Reveal is the base game; Hidden
 * withholds the alignment on every result the match survives, which makes a
 * wrong vote and a right one look identical from the outside.
 */
const RESULT_MODES: ReadonlyArray<[EliminationResults, string, string]> = [
  [EliminationResults.REVEAL, "Reveal", "After a vote, the room is told whether they got an imposter."],
  [EliminationResults.HIDDEN, "Hidden", "The room is told only who went. Nobody learns which side they were on."],
];

/** The warning MULTIPLE_IMPOSTERS.md requires, verbatim in substance. */
const THREE_PLAYER_WARNING =
  "Two imposters with three players is an experimental, unwinnable group " +
  "configuration. The imposter side wins when two players remain.";

const DIFFICULTIES: ReadonlyArray<[Difficulty, string]> = [
  [Difficulty.EASY, "Easy"],
  [Difficulty.MEDIUM, "Medium"],
  [Difficulty.HARD, "Hard"],
];

/**
 * The pen handicap, as a fixed three-way choice rather than a stroke count to
 * tune: the whole point is a rule everybody at the table can hold in their head
 * while they judge a drawing. The hint under the control carries the rule in
 * full, so no other screen has to explain it before the match starts.
 */
const PEN_RULES: ReadonlyArray<[PenRule, string, string]> = [
  [PenRule.FREE, "Free", "Draw as much as the clock allows."],
  [PenRule.ONE_LINE, "One line", "One unbroken stroke. Lift the pen and your turn's drawing is done."],
  [PenRule.MAX_FIVE, "Max 5", "Five strokes for the whole turn. Spend them well."],
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
    penRule: RECOMMENDED.penRule,
    maxRounds: RECOMMENDED.maxRounds,
    drawSeconds: RECOMMENDED.drawSeconds,
    discussSeconds: RECOMMENDED.discussSeconds,
    intermissionSeconds: RECOMMENDED.intermissionSeconds,
    imposterCount: RECOMMENDED.imposterCount,
    eliminationResults: RECOMMENDED.eliminationResults,
  });
  let editable = false;

  const emit = (patch: Partial<MatchSettings>) => {
    if (!editable) return;
    onChange(create(MatchSettingsSchema, {
      difficulty: patch.difficulty ?? current.difficulty,
      penRule: patch.penRule ?? current.penRule,
      maxRounds: patch.maxRounds ?? current.maxRounds,
      drawSeconds: patch.drawSeconds ?? current.drawSeconds,
      discussSeconds: patch.discussSeconds ?? current.discussSeconds,
      intermissionSeconds: patch.intermissionSeconds ?? current.intermissionSeconds,
      imposterCount: patch.imposterCount ?? current.imposterCount,
      eliminationResults: patch.eliminationResults ?? current.eliminationResults,
    }));
  };

  // Imposters — a stepper, because the range is 1..2 and the whole control is
  // two values. Never disabled at low player counts: the server permits every
  // combination and the warning below carries the consequence
  // (MULTIPLE_IMPOSTERS.md, "Match Settings").
  const impVal = el("span", { class: "stepper-val", text: "1" });
  const impMinus = el("button", { type: "button", class: "btn btn-sm", "aria-label": "Fewer imposters", text: "−" }) as HTMLButtonElement;
  const impPlus = el("button", { type: "button", class: "btn btn-sm", "aria-label": "More imposters", text: "+" }) as HTMLButtonElement;
  d.on(impMinus, "click", () => emit({ imposterCount: Math.max(LIMITS.minImposters, current.imposterCount - 1) }));
  d.on(impPlus, "click", () => emit({ imposterCount: Math.min(LIMITS.maxImposters, current.imposterCount + 1) }));
  const impStepper = el(
    "div",
    { class: "stepper", role: "group", "aria-label": "Number of imposters" },
    impMinus, impVal, impPlus,
  );
  const impHint = el("p", { class: "hint", style: "margin:.35rem 0 0" });
  // role="alert" rather than a plain paragraph: the host may be looking at the
  // player list when the roster drops to three, and a warning that only ever
  // appears silently is one they will start the match without having read.
  const impWarn = el("p", { class: "warn", role: "alert", text: THREE_PLAYER_WARNING });
  impWarn.hidden = true;

  // Elimination results — segmented, like the pen rule it sits beside.
  const resultBtns = RESULT_MODES.map(([value, text]) => {
    const b = el("button", { type: "button", "aria-pressed": "false", text }) as HTMLButtonElement;
    d.on(b, "click", () => emit({ eliminationResults: value }));
    return b;
  });
  const resultSeg = el("div", { class: "seg", role: "group", "aria-label": "Elimination results" }, ...resultBtns);
  const resultHint = el("p", { class: "hint", style: "margin:.35rem 0 0", text: RESULT_MODES[0]?.[2] ?? "" });

  // Pen rule — segmented control, above Difficulty because it changes how the
  // game plays more than the deck does.
  const penBtns = PEN_RULES.map(([value, text]) => {
    const b = el("button", { type: "button", "aria-pressed": "false", text }) as HTMLButtonElement;
    d.on(b, "click", () => emit({ penRule: value }));
    return b;
  });
  const penSeg = el("div", { class: "seg", role: "group", "aria-label": "Pen rule" }, ...penBtns);
  const penHint = el("p", { class: "hint", style: "margin:.35rem 0 0", text: PEN_RULES[0]?.[2] ?? "" });

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

  const resultVal = el("span", { class: "setting-val", text: "Reveal" });
  const penVal = el("span", { class: "setting-val", text: "Free" });
  const diffVal = el("span", { class: "setting-val", text: "Medium" });
  const drawVal = el("span", { class: "setting-val", text: "15s" });
  const discussVal = el("span", { class: "setting-val", text: "120s" });
  const intermissionVal = el("span", { class: "setting-val", text: "10s" });
  const hostNote = el("p", { class: "hint", text: "Only the host can change these." });

  // The hint rides inside the setting block, under the control it explains.
  const penSetting = labelled("Pen rule", "Free", penSeg, penVal);
  penSetting.appendChild(penHint);

  const imposterSetting = labelled("Imposters", "1", impStepper, null);
  imposterSetting.appendChild(impHint);
  imposterSetting.appendChild(impWarn);

  const resultSetting = labelled("Elimination results", "Reveal", resultSeg, resultVal);
  resultSetting.appendChild(resultHint);

  const root = el(
    "section",
    { class: "card col-right" },
    el("div", { class: "card-title", text: "Match settings" }),
    imposterSetting,
    resultSetting,
    penSetting,
    labelled("Difficulty", "Medium", seg, diffVal),
    labelled("Rounds", "2", stepper, null),
    labelled("Drawing turn", "15s", drawRange, drawVal),
    labelled("Discussion", "120s", discussRange, discussVal),
    labelled("Between turns", "10s", intermissionRange, intermissionVal),
    hostNote,
  );

  return {
    root,
    update(settings, canEdit, seated) {
      current = settings;
      editable = canEdit;

      const imposters = Math.max(LIMITS.minImposters, settings.imposterCount);
      setText(impVal, String(imposters));
      impMinus.disabled = !canEdit || imposters <= LIMITS.minImposters;
      impPlus.disabled = !canEdit || imposters >= LIMITS.maxImposters;
      setText(
        impHint,
        imposters === 1
          ? "One player holds the odd word."
          : "Two players hold the SAME odd word. Neither is told they are an imposter, " +
            "and neither is told who the other one is.",
      );
      // Shown to everybody, not only the host: the players about to be dealt
      // into an unwinnable match are the ones who most want to see it.
      impWarn.hidden = !(imposters > 1 && seated > 0 && seated <= LIMITS.unwinnableTwoImposterPlayers);

      resultBtns.forEach((b, i) => {
        const [value] = RESULT_MODES[i] ?? [EliminationResults.UNSPECIFIED, "", ""];
        b.setAttribute("aria-pressed", String(settings.eliminationResults === value));
        b.disabled = !canEdit;
      });
      // An unspecified mode is REVEAL to the server, so it reads as REVEAL here.
      const mode = RESULT_MODES.find(([v]) => v === settings.eliminationResults) ?? RESULT_MODES[0];
      setText(resultVal, mode ? mode[1] : "Reveal");
      setText(resultHint, mode ? mode[2] : "");
      penBtns.forEach((b, i) => {
        const [value] = PEN_RULES[i] ?? [PenRule.UNSPECIFIED, "", ""];
        b.setAttribute("aria-pressed", String(settings.penRule === value));
        b.disabled = !canEdit;
      });
      // An unspecified rule is FREE to the server, so it reads as FREE here.
      const rule = PEN_RULES.find(([v]) => v === settings.penRule) ?? PEN_RULES[0];
      setText(penVal, rule ? rule[1] : "Free");
      setText(penHint, rule ? rule[2] : "");
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
