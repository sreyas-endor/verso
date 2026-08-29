import { PenRule } from "../../../gen/verso/v1/game_pb.js";
import type { ScreenCtx, ViewState } from "../context.js";
import { Disposers, el, fill, setText } from "../dom.js";
import { NO_AVATAR, avatar } from "../avatar.js";
import { paintress } from "../paintress.js";
import { avatarColor } from "../palette.js";
import { playerList } from "../playerList.js";
import { spectatorPanel } from "../spectatorPanel.js";
import { stage } from "../stage.js";
import { timer } from "../timer.js";
import { tools } from "../tools.js";
import { wordPanel } from "../wordPanel.js";

let d: Disposers | null = null;

/**
 * The pen rule, everywhere it has to be said out loud.
 *
 * A handicap nobody can see is a handicap nobody can read into a vote, so the
 * chip rides beside the status-card title for the watchers as well as for the
 * artist, and the announcement carries it too — a screen reader user is being
 * handed the same restriction.
 */
function ruleChip(rule: PenRule): string {
  if (rule === PenRule.ONE_LINE) return "One line";
  if (rule === PenRule.MAX_FIVE) return "Max 5 strokes";
  return "";
}

/**
 * The per-turn stroke ceiling the canvas gates on. Infinity under FREE: the
 * room's own anti-abuse cap still applies, and the client has no business
 * holding a second copy of a number it does not enforce.
 */
function strokeLimit(rule: PenRule): number {
  if (rule === PenRule.ONE_LINE) return 1;
  if (rule === PenRule.MAX_FIVE) return 5;
  return Number.POSITIVE_INFINITY;
}

/** Spoken to the artist on the turn they get it, in the lobby's own words. */
function ruleSpoken(rule: PenRule): string {
  if (rule === PenRule.ONE_LINE) return " One unbroken stroke. Lift the pen and your turn's drawing is done.";
  if (rule === PenRule.MAX_FIVE) return " Five strokes for the whole turn. Spend them well.";
  return "";
}

/**
 * Cap on the half-RTT the pen closes early by, matching the one the store ages
 * its deadlines with. A connection bad enough to exceed it has bigger problems
 * than a lost stroke tail, and locking the pen a full second before the buzzer
 * would be worse than the thing it prevents.
 */
const MAX_START_LEAD_MS = 250;

function turnsAway(s: ViewState): number {
  const at = s.turnOrder.indexOf(s.selfId);
  if (at < 0 || at <= s.turnIndex) return -1;
  return at - s.turnIndex;
}

export function mount(root: HTMLElement, ctx: ScreenCtx): void {
  d = new Disposers();
  const dd = d;

  const roster = playerList("Players", { showTurnQueue: true });
  const board = stage(ctx.canvas, "Shared drawing canvas");
  // The canvas handle outlives this screen — leave input off on the way out.
  dd.add(() => ctx.canvas.setInteractive(false));
  dd.add(() => board.dispose());
  const pens = tools(ctx.canvas);
  dd.add(() => pens.dispose());
  // Decoration, not a status light: she paints at the same tempo all turn, and
  // stands on the floor strip below the stage rather than over the canvas.
  const paint = paintress();
  const word = wordPanel(true);
  const clock = timer("Time left in this turn");

  const kicker = el("div", { class: "phasehead-kicker" });
  const title = el("div", { class: "phasehead-title" });
  const head = el(
    "section",
    { class: "card phasehead" },
    el("div", { class: "phasehead-main" }, kicker, title),
    clock.root,
  );

  const statusTitle = el("div", { class: "card-title" });
  const statusBody = el("div", {});
  const status = el("section", { class: "card" }, statusTitle, statusBody);

  /** The title, with the rule chip pushed to its right when there is one. */
  const setStatusTitle = (text: string, chip: string) => {
    statusTitle.className = chip ? "card-title with-chip" : "card-title";
    fill(statusTitle, el("span", { text }), chip ? el("span", { class: "rulechip", text: chip }) : null);
  };

  // The watcher's copy of the rule. The pen card only ever speaks to the artist,
  // so under a non-free rule everyone else gets this card instead — built once
  // and refilled, because it changes at most once a match.
  const ruleTicks = el("span", { class: "gauge-ticks" });
  const ruleCount = el("b");
  const ruleOf = el("span");
  const ruleGauge = el("div", { class: "gauge" }, ruleTicks, el("span", { class: "gauge-label" }, ruleCount, ruleOf));
  const ruleHint = el("p", { class: "hint" });
  const ruleCard = el(
    "section",
    { class: "card" },
    el("div", { class: "card-title", text: "The rule this match" }),
    ruleGauge,
    ruleHint,
  );
  let ruleCardFor: PenRule | null = null;
  const fillRuleCard = (rule: PenRule) => {
    if (ruleCardFor === rule) return;
    ruleCardFor = rule;
    const oneLine = rule === PenRule.ONE_LINE;
    const ticks = oneLine ? 1 : 5;
    ruleGauge.className = oneLine ? "gauge oneline" : "gauge";
    fill(ruleTicks, ...Array.from({ length: ticks }, () => el("i", { class: "gauge-tick" })));
    setText(ruleCount, oneLine ? "One line" : "Max 5");
    setText(ruleOf, oneLine ? "nobody lifts the pen" : "five strokes each");
    setText(ruleHint, oneLine
      ? "Every artist gets a single unbroken stroke. Judge the drawing accordingly."
      : "Every artist gets five strokes for the whole turn. Judge the drawing accordingly.");
  };

  const main = el("div", { class: "col-main" }, head, board.root, paint.root);
  // Compact: this column sits beside a live canvas, and a spectator watching
  // the drawing wants the names to hand, not the whole word table. The full
  // one is on the discussion and result screens, where there is room to read.
  const dossier = spectatorPanel("You are out. Watch what they draw knowing this:", true);

  const right = el("div", { class: "col-right stack" }, status, dossier.root, pens.root, word.root);
  const view = el("div", { class: "cols" }, roster.root, main, right);
  root.appendChild(view);
  dd.add(() => view.remove());

  // On a phone the columns stack, and the right rail lands below a ~270px
  // canvas plus the paintress strip — roughly 760px down a ~734px viewport.
  // The artist would have to scroll away from their own drawing to change
  // pen. Above the breakpoint the node is never moved, so the desktop rail
  // is exactly what it was.
  const narrow = window.matchMedia("(max-width: 640px)");
  const placePens = () => {
    const host = narrow.matches ? main : right;
    if (pens.root.parentNode === host) return;
    if (host === main) main.insertBefore(pens.root, paint.root);
    else right.insertBefore(pens.root, word.root);
  };
  placePens();
  narrow.addEventListener("change", placePens);
  dd.add(() => narrow.removeEventListener("change", placePens));

  let lastArtist = "";
  let lastRound = -1;
  // What the frame loop needs to know to poll the budget without re-reading the
  // whole snapshot for it.
  let liveRule: PenRule = PenRule.FREE;
  let liveArtist = false;

  /**
   * Open or close the pen against the turn's own clock rather than against the
   * announcement that it ran out.
   *
   * PhaseChanged only says the turn is over one one-way latency after it was,
   * and until it lands PointerInput keeps taking points that the room refuses
   * (artistGate) — the artist then watches the tail of their own line vanish
   * when StrokeEnded re-cuts the overlay to the geometry the room kept. So the
   * pen closes on the predicted deadline instead, and the StrokeEnd that closes
   * it lands inside room.TurnGrace, which exists to catch exactly that.
   *
   * Starting a stroke shuts off a half-RTT earlier still: a StrokeBegin sent
   * later than that cannot reach the room before the deadline, and the room has
   * no grace for a NEW stroke — only for finishing the open one.
   *
   * `s.deadline` is already aged by the other half of the round trip by the
   * store, so it is this client's best estimate of the room's own instant.
   */
  const applyPenGates = (s: ViewState, iAmArtist: boolean) => {
    if (!iAmArtist) {
      ctx.canvas.setInteractive(false);
      return;
    }
    // A drawing turn always has its timer armed, so a null deadline here is not
    // "untimed" — it is a turn whose clock the room already reports as spent,
    // which is what a reconnect landing inside the grace window sees.
    const left = s.deadline === null ? 0 : s.deadline - performance.now();
    ctx.canvas.setInteractive(left > 0);
    ctx.canvas.setAcceptingNewStrokes(left > Math.min(s.rttMs / 2, MAX_START_LEAD_MS));
  };

  /** Push the canvas's own count into the gauge. Cheap enough for every frame. */
  const paintBudget = () => {
    if (!liveArtist || liveRule === PenRule.FREE) {
      pens.setBudget(PenRule.FREE, 0, false);
      return;
    }
    const budget = ctx.canvas.strokeBudget();
    pens.setBudget(liveRule, budget.used, budget.penDown);
  };

  const render = (s: ViewState) => {
    roster.update(s);
    word.update(s.word);

    const artist = s.players.find((p) => p.id === s.artistId);
    const artistName = artist?.name ?? "Someone";
    const iAmArtist = s.artistId === s.selfId && !s.youAreEliminated;
    if (s.artistId) paint.setInk(avatarColor(s.artistId));

    setText(kicker, `Round ${s.round} of ${s.totalRounds} · turn ${s.turnIndex + 1} of ${s.turnOrder.length}`);
    fill(
      title,
      iAmArtist ? el("em", { text: "Your turn — draw!" }) : el("em", { text: artistName }),
      iAmArtist ? "" : " is drawing",
    );

    // An unknown rule is FREE to the server, so it is FREE here too.
    const rule = s.settings.penRule;
    const chip = ruleChip(rule);
    liveRule = chip ? rule : PenRule.FREE;
    liveArtist = iAmArtist;

    pens.setEnabled(iAmArtist);
    ctx.canvas.setStrokeLimit(strokeLimit(rule));
    applyPenGates(s, iAmArtist);
    board.setLocked(!iAmArtist);
    paintBudget();

    const wantsRuleCard = Boolean(chip) && !iAmArtist && !s.youAreEliminated;
    if (wantsRuleCard) {
      fillRuleCard(rule);
      // The rack sits above the word panel in the rail — except on a phone,
      // where placePens() has moved it out of this column entirely and the
      // word panel is the next fixed landmark to sit in front of.
      if (ruleCard.parentNode !== right) {
        right.insertBefore(ruleCard, pens.root.parentNode === right ? pens.root : word.root);
      }
    } else {
      ruleCard.remove();
    }

    dossier.update(s);
    if (s.youAreEliminated) {
      status.className = "card spectator";
      setStatusTitle("Spectating", "");
      fill(
        statusBody,
        el("p", { text: "You were eliminated. You can still watch every stroke, but you no longer draw or vote." }),
      );
      pens.root.hidden = true;
    } else {
      status.className = "card";
      pens.root.hidden = false;
      if (iAmArtist) {
        setStatusTitle("You are drawing", chip);
        fill(
          statusBody,
          el("p", { text: "Freehand clues only. No letters, numbers, arrows or symbols." }),
          el("p", { class: "hint", text: "There is no eraser and no undo — every mark stays on the canvas." }),
        );
      } else {
        const away = turnsAway(s);
        setStatusTitle("Watching", chip);
        fill(
          statusBody,
          el("p", { class: "row" }, avatar(s.artistId, artist?.avatar ?? NO_AVATAR, "sm"), el("span", { text: `${artistName} is drawing.` })),
          away === 1
            ? el("p", { class: "badge badge-you", text: "YOUR TURN IS NEXT" })
            : away > 1
              ? el("p", { class: "hint", text: `Your turn is in ${away} turns.` })
              : el("p", { class: "hint", text: "You have already drawn this round." }),
        );
      }
    }

    clock.update(s.deadline, s.durationMs);

    if (s.artistId !== lastArtist || s.round !== lastRound) {
      lastArtist = s.artistId;
      lastRound = s.round;
      ctx.announce(
        iAmArtist
          ? `Round ${s.round}. Your turn to draw.${ruleSpoken(rule)}`
          : `Round ${s.round}. ${artistName} is drawing.`,
      );
    }
  };

  render(ctx.state());
  dd.add(ctx.subscribe(render));
  dd.raf(() => {
    const s = ctx.state();
    clock.update(s.deadline, s.durationMs);
    // The buzzer is a moment, not an event: nothing in the store fires when the
    // clock reaches zero, so the pen has to be closed from the same loop that
    // draws the countdown.
    if (liveArtist) applyPenGates(s, true);
    // A stroke starting or ending is a local event the store never republishes,
    // so the gauge rides the clock's loop. setBudget() drops a repeat before it
    // reaches the DOM, and under FREE nothing is read at all.
    if (liveArtist && liveRule !== PenRule.FREE) paintBudget();
  });
}

export function unmount(): void {
  d?.dispose();
  d = null;
}
