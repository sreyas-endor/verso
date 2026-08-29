# Verso — Mobile / Touch Responsiveness Plan

**Status:** implemented — all of P0.1–P2.8. `npm run typecheck` and `npm run build` pass; **no manual phone or browser QA has been run.** §8 is the implementation record, including four deviations from the proposed code and three things it did not fix.
**Date:** 2026-08-29 (proposed), 2026-08-29 (implemented)
**Baseline:** `cd web && npm run build` passes clean at `9fc1f5a` (121 modules, `dist/assets/index-DNTWfXLL.css` 37.91 kB). Every measurement below is against that tree.
**Scope:** the phone experience — portrait and landscape, coarse pointer, viewports 360–430 CSS px wide.
**Explicit non-goal:** changing the desktop/laptop layout or experience. The user's report is that desktop "is really good"; §5 states exactly what guarantees it does not move.
**Also out of scope:** the Go server, the wire protocol, the `1024×768` logical grid (§2.1 explains why that constant is *not* the problem), the game rules, and any new framework or CSS library. The app stays hand-written TypeScript + plain CSS.

---

## 1. What already works, so the plan does not re-litigate it

The codebase is not naïve about mobile. Before listing failures, the prior decisions this plan deliberately keeps:

- `web/index.html:5` has a real viewport meta (`width=device-width, initial-scale=1`) and does **not** disable zoom. No `user-scalable=no`, no `maximum-scale`. Pinch-zoom works.
- `web/src/styles/layout.css:78` already stacks the three-column grid into a single column below 900 px, with `.col-main` (canvas) promoted to `order: 1`. The comment even says "Phone-first: canvas leads".
- `web/src/styles/components.css:71` already un-hides the host's kick control for `@media (hover: none)`, so it is not hover-only on touch.
- `touch-action: none` is scoped to the canvas box only, never page-wide (`web/src/ui/stage.ts:11` documents this against WCAG 2.0 SC 1.4.4), and `web/src/canvas/input.ts` implements the full iOS pointer-event contract from `docs/IMPLEMENTATION_PLAN.md:337`.
- Nothing anywhere uses `100vh`. The layout is flow-based, so the classic iOS Safari "URL bar eats the last 100 px" trap does not exist here. Confirmed: `grep -n "vh\b" web/src/styles/*.css` returns only `clamp()` upper bounds on margins and font sizes.
- `Surface.resize()` (`web/src/canvas/surface.ts:303`) is already DPR-correct, area-capped and rAF-coalesced, and `MAX_SURFACE_BYTES` (`web/src/canvas/surface.ts:49`) was explicitly budgeted for phones.

**One prior decision this plan must not contradict without saying so.** `docs/IMPLEMENTATION_PLAN.md:275` states: *"Use 1024×768 (4:3) — 4:3 wastes far less screen in portrait for a phone-heavy party game."* That reasoning is correct and this plan agrees with it. The problems in §2.1 and §2.2 are **not** caused by the logical grid's shape; they are caused by the CSS box that letterboxes it having no height bound at all. No change to `LOGICAL_W` / `LOGICAL_H` (`web/src/canvas/grid.ts:19`) is proposed.

---

## 2. Problems found

Line references are `path:line` from the repo root. Arithmetic uses the app's real values: `body` is `font-size: 16px` (`web/src/styles/base.css:10`), so `1rem = 16px`; `.screen` padding is `clamp(1rem, 3vw, 1.8rem)` (`web/src/styles/layout.css:61`), which at every phone width resolves to the `1rem` floor because `3vw` at 430 px is only 12.9 px.

### 2.1 P0 — In landscape, the canvas is one and a half screens tall

`web/src/styles/components.css:105`

```css
.stage {
  ...
  aspect-ratio: 4 / 3;
  width: 100%;
  touch-action: none;
}
```

`.stage` is bound on **width only**. Its height is whatever `width × 3/4` produces. Nothing — not `.stage`, not `.verso-stage` (`web/src/canvas/surface.ts:74`), not `.verso-pad` (`web/src/canvas/surface.ts:75`), not `.cols` (`web/src/styles/layout.css:65`) — ever compares that height to the viewport.

On a phone rotated to landscape, `web/src/styles/layout.css:78` still fires (844 ≤ 900), so `.cols` collapses to a single full-width column and the canvas takes the whole 844 px:

| Viewport | `.cols` width | `.stage` box | canvas height ÷ viewport height |
| --- | --- | --- | --- |
| 360 × 800 portrait | 328 px | 328 × 246 | **30.8 %** |
| 390 × 844 portrait | 358 px | 358 × 268.5 | **31.8 %** |
| 428 × 926 portrait | 396 px | 396 × 297 | **32.1 %** |
| 844 × 390 landscape | 793.4 px | 793.4 × **595** | **152.6 %** |

At 844 × 390 the drawing is 595 px tall inside a 390 px viewport. The player cannot see the whole shared canvas at once — which for a deduction game whose entire premise is "everyone sees the same picture" is the worst possible failure. Combined with §2.3 they cannot even scroll it into view with a finger on it.

**Why desktop is unaffected:** on a laptop the same rule produces a canvas roughly `1500 × 0.55 × 0.75 ≈ 600 px` tall inside a 800–1200 px viewport, and the three-column grid (`web/src/styles/layout.css:67`) caps `.col-main` well below the full width. Width-bounded is the *right* rule when the viewport is landscape and roomy. It is wrong only when the container is portrait-narrow or the viewport is short.

### 2.2 P0 — The artist's pen rack is below the fold, on a timed turn

`web/src/ui/screens/drawing.ts:128` and `web/src/ui/screens/drawing.ts:134`

```ts
const main  = el("div", { class: "col-main" }, head, board.root, paint.root);
const right = el("div", { class: "col-right stack" }, status, dossier.root, pens.root, word.root);
const view  = el("div", { class: "cols" }, roster.root, main, right);
```

`pens.root` — the twelve-marker rack and the four nib buttons, the artist's only tools — is the **third** child of `.col-right`. Under `web/src/styles/layout.css:81` the phone order is `.col-main` (1), `.col-right` (2), `.col-left` (3), so the page reads top-to-bottom: phase header → canvas → paintress → status card → dossier → pen rack → word panel → roster.

Measured offset to the top of the pen rack at 390 × 844:

| Element | Source | Height |
| --- | --- | --- |
| `.appbar` (`.5rem` padding × 2 + 29.6 px sound button + 2 px border) | `web/src/styles/layout.css:4`, `:27` | 47.6 |
| `.screen` padding-top | `web/src/styles/layout.css:61` | 16 |
| `.phasehead` card — wraps to two rows, see below | `web/src/styles/components.css:92` | ≈ 155 |
| `.col-main` gap | `web/src/ui/screens/drawing.ts:128` + `.col-main{gap:.8rem}` `web/src/styles/layout.css:72` | 12.8 |
| `.stage` | §2.1 | 268.5 |
| `.col-main` gap | | 12.8 |
| `.easel` (`4.6rem` − `.6rem` negative top margin) | `web/src/styles/components.css:362`, `:315` | 64 |
| `.cols` gap | `web/src/styles/layout.css:68` | 14.4 |
| status card (title + two paragraphs at 318.8 px inner width) | `web/src/ui/screens/drawing.ts:90` | ≈ 157 |
| `.stack` gap | `web/src/styles/base.css:46` | 11.2 |
| **top of `.tools`** | | **≈ 759 px** |

iOS Safari's visible viewport on a 390 × 844 device is ~734 px with toolbars shown. The rack begins **below every pixel of it**. At rest the artist sees a canvas and no pen. Changing colour costs a scroll away from the drawing, mid-turn, against a countdown they also scrolled off screen.

`.phasehead` wrapping is itself part of the cost: `.phasehead-main` is `flex: 1 1 240px` (`web/src/styles/components.css:93`) and `.timer` is `min-width: 7rem` (`web/src/styles/components.css:76`); `240 + 112 + 16 = 368 px` exceeds the 318.8 px available inside the card at 390 px, so the timer drops to a second row and the header costs ~155 px instead of ~100 px.

**Why desktop is unaffected:** above 900 px the rack sits in the right rail *beside* the canvas, permanently visible. That is the layout the user called good.

### 2.3 P0 — The canvas is an unscrollable dead zone for everyone who is not drawing

`web/src/styles/components.css:107`, `web/src/styles/components.css:109`, `web/src/canvas/surface.ts:77`, `web/src/canvas/surface.ts:78`

```css
.stage { ... touch-action: none; }
.stage > canvas { ... touch-action: none; }
.verso-pad { ... touch-action:none; ... }
.verso-pad canvas { ... touch-action:none; }
```

All four declarations are **unconditional**. `touch-action: none` tells the browser to send no gesture to the scroller, ever, for that box — regardless of whether this client is allowed to draw.

The screens where nobody local can draw are the majority of the match:

- `web/src/ui/screens/discussion.ts:18` — `setInteractive(false)`, `setLocked(true)`
- `web/src/ui/screens/intermission.ts:15` — same
- `web/src/ui/screens/result.ts` — same, via `board.setLocked(true)`
- `web/src/ui/screens/drawing.ts:166` — every non-artist, every turn, which is *n−1* of the *n* players

On all of those, a 246–297 px-tall band across the full width of a phone silently swallows every vertical swipe. The player's thumb naturally lands in the middle of the screen, which is exactly where the canvas is (§2.2: the stage spans roughly y = 231–500 of a ~734 px window). They swipe, nothing moves, and the roster and vote picker below appear unreachable.

The JS side is already conditional — `PointerInput.onTouchMove` only calls `preventDefault()` when a stroke is live (`web/src/canvas/input.ts:145`) — so this is purely the static CSS being broader than the behaviour it protects.

**Why desktop is unaffected:** `touch-action` governs touch and pen gestures. A mouse wheel and a scrollbar are unaffected by it, so on a laptop this declaration is invisible.

### 2.4 P1 — Touch targets below the 44 px minimum

Every value computed from `1rem = 16px` and `line-height: 1.45` (`web/src/styles/base.css:11`). WCAG 2.5.5 / the iOS and Material guidance all land at 44–48 px.

| Control | Source | Rendered size | Notes |
| --- | --- | --- | --- |
| Host's remove-player `×` | `web/src/styles/components.css:60` | **24 × 24** | Destructive, and `web/src/styles/components.css:71` makes it *permanently visible* on touch, so it sits 24 px from a row a thumb also wants to read |
| Nib (brush size) buttons | `web/src/styles/components.css:167` | height **36.8** | Four of them, used every turn |
| Appbar mute button | `web/src/styles/layout.css:27` | **29.6 × 29.6** | |
| `.btn-sm` | `web/src/styles/base.css:63` | height **37.2** | Used for the vote confirm (`web/src/ui/votePicker.ts:32`), lobby "Copy link" (`web/src/ui/screens/lobby.ts:43`), and all four settings steppers (`web/src/ui/settingsPanel.ts:108`, `:109`, `:153`, `:154`) where the glyph is a single `−`/`+` ≈ 38 px wide |
| `.seg > button` | `web/src/styles/base.css:111` | height **37.6** | Difficulty, pen rule, result mode |
| `.face-arrow` | `web/src/styles/components.css:433` | **41.6 × 41.6** | Marginal |
| `.swatch` at 360 px wide | `web/src/styles/components.css:116`, `:124` | **39.7 × 76.4**, 6.4 px apart | Tall enough; narrow and tightly packed. At 390 px it is 44.7 px and fine |

The plain `.btn` is *not* a problem — `.7rem` padding + 23.2 px line box + 4 px border = **49.6 px** (`web/src/styles/base.css:51`). The pattern is that every *small* variant was sized for a mouse cursor and never revisited for a thumb.

**Why desktop is unaffected:** a mouse pointer is precise to one pixel. 24 px is a comfortable mouse target and an unusable thumb target.

### 2.5 P1 — Sticky `:hover` makes the vote picker lie about which card is armed

`web/src/styles/components.css:220`

```css
.votecard:hover:not(:disabled)  { background: var(--accent-sf); }
.votecard[aria-pressed="true"]  { border-color: var(--accent); background: var(--accent-sf); }
```

The armed state and the hover state are **the same background colour**. The only difference is a 2 px border.

On iOS and Android a tapped element retains `:hover` until the user taps elsewhere. So after arming card A and then arming card B, card A keeps `--accent-sf` from sticky hover while card B has it from `aria-pressed`. Two cards look selected in a UI whose own documentation (`web/src/ui/votePicker.ts:18`) says *"a single mis-tap is unrecoverable"* and whose entire confirm step exists to prevent exactly that ambiguity.

None of the five `:hover` rules in the codebase is wrapped in `@media (hover: hover)`: `web/src/styles/base.css:59`, `:72`, `:73`, `:74`; `web/src/styles/layout.css:34`; `web/src/styles/components.css:67`, `:220`, `:438`. The vote picker is the one where it changes the meaning of the screen rather than only its polish.

**Why desktop is unaffected:** a real pointer leaves, so `:hover` clears the moment the cursor moves off.

### 2.6 P1 — The handoff overlay overflows the canvas it is centred in

`web/src/styles/screens.css:156` / `web/src/ui/screens/intermission.ts:22`

`.handoff-overlay` is `position: absolute; inset: 0` over `.handoff-canvas`, so its height *is* `.stage`'s height — 246 px at 360 px wide, 268.5 px at 390 px. Its `padding: clamp(1rem, 4vw, 2.5rem)` resolves to the 1 rem floor on phones, leaving 214 px / 236.5 px of content room.

Its children need, at 360 px:

| Child | Source | Height |
| --- | --- | --- |
| `.handoff-badge` (`.78rem` × 1.45 + `.35rem` padding × 2) | `web/src/styles/screens.css:157` | 29.3 |
| `.handoff-title` margins (`1rem` + `.65rem`) | `web/src/styles/screens.css:158` | 26.4 |
| `.handoff-title` text — `max-width: 14ch` at 26.4 px, `line-height: .95`; "Alexandra is drawing next" wraps to 2–3 lines | `web/src/styles/screens.css:158` | 50–75 |
| `.handoff-detail` (`avatar-lg` 48 px + `.4rem` padding × 2) | `web/src/styles/screens.css:159`, `web/src/styles/components.css:17` | 60.8 |
| `.handoff-clock` (`1.25rem` margin + `.6rem` padding × 2 + timer 44.8) | `web/src/styles/screens.css:160`, `web/src/styles/components.css:76` | 84 |
| **total** | | **≈ 289 px vs 214 px available** |

`.handoff-overlay` is `justify-content: center` with no `overflow` rule, and `.handoff-canvas` (`web/src/styles/screens.css:154`) does not clip, so the badge escapes upward over the phase header and the clock escapes downward over the roster. The timer — the one thing the handoff screen exists to show — is the piece that lands outside the frame.

**Why desktop is unaffected:** the same overlay gets a ~600 px-tall stage, roughly double what it needs.

### 2.7 P2 — The appbar can push the page horizontally at 360 px

`web/src/styles/layout.css:4`

```css
.appbar { display: flex; align-items: center; gap: .8rem; padding: .5rem clamp(.7rem, 3vw, 1.5rem); }
```

No `flex-wrap`, and none of `.appbar-code` (`web/src/styles/layout.css:14`), `.appbar-meta` (`:19`) or `.conn` (`:42`) carries `min-width: 0`, a truncation rule, or `flex-shrink` guidance. `web/src/styles/layout.css:84` drops `.appbar-brand` below 900 px, which buys ~60 px, but the worst case is still tight: at 360 px, a 6-character room code chip (~80 px) + `"10/11 online"` (~85 px) + the 29.6 px mute button + `"Reconnecting"` with its dot (~100 px) + four 12.8 px gaps + 22.4 px of padding ≈ **368 px**. Past the viewport, so the whole document gains a horizontal scroll and the sticky header shifts out from under the content when the user pans.

This is a narrow worst case — a full room *and* a reconnect *and* the smallest common phone — but it is reachable, and the failure mode (the entire page scrolls sideways) is disproportionate.

### 2.8 P2 — No safe-area handling, and the meta tag does not ask for one

`web/index.html:5` — `content="width=device-width, initial-scale=1"`, with no `viewport-fit=cover`. `grep -n "env(safe-area" web/src/styles/*.css` returns nothing.

The current state is **not broken**: with the default `viewport-fit=auto`, iOS insets the whole layout viewport inside the safe area, so nothing is clipped by the notch or the home indicator. The cost is cosmetic and specific:

- The `.appbar`'s translucent background (`web/src/styles/layout.css:8`) and the `body` dot-grid (`web/src/styles/base.css:6`) both stop at the inset. In portrait the status-bar strip above the header renders as a flat band; in landscape both side gutters do.
- `.toasts` is `position: fixed; bottom: 1.2rem` (`web/src/styles/layout.css:88`). Measured from the safe box this clears the home indicator, but 19.2 px is inside the region iOS Safari's collapsed bottom toolbar overlays, so a server-error toast can land behind browser chrome.

The important part of this finding is the **ordering constraint**: adding `viewport-fit=cover` without simultaneously adding `env(safe-area-inset-*)` padding to `.appbar`, `.screen`, `.banner` and `.toasts` would actively regress the app — content would slide under the notch and the home indicator. These two changes ship together or not at all.

### 2.9 P2 — Nothing renders at exactly 4:3, and it is slightly worse the smaller the box

`web/src/styles/components.css:99` gives `.stage` `aspect-ratio: 4/3` and `border: 4px`, under a global `box-sizing: border-box` (`web/src/styles/base.css:1`). `aspect-ratio` therefore applies to the **border box**, so the content box is `(W − 8) × (0.75W − 8)` — never 4:3. `.verso-pad` then adds its own 2 px border (`web/src/canvas/surface.ts:76`) with `width: 100%` and `max-height: 100%`, so its height clamps and the ratio is violated rather than the width being recomputed:

| Viewport | drawable pad | ratio (want 1.3333) | error |
| --- | --- | --- | --- |
| 360 × 800 | 320 × 238 | 1.3445 | 0.84 % |
| 390 × 844 | 350 × 260.5 | 1.3436 | 0.77 % |
| 428 × 926 | 388 × 289 | 1.3426 | 0.70 % |
| 844 × 390 landscape | 785.4 × 587 | 1.3379 | 0.34 % |

The error is a constant 8 px absorbed by a shrinking box, so it grows as the viewport narrows. There is **no coordinate bug**: `toLogicalX` and `toLogicalY` (`web/src/canvas/surface.ts:84`, `:89`) divide by `rect.width` and `rect.height` independently, so a tap always maps to the logical point under the finger. The consequence is only that everyone's drawing is uniformly stretched ~0.8 % vertically, and by slightly different amounts on differently-shaped screens. This is cosmetic and pre-existing; it is listed because §3.1 has to touch these exact declarations anyway and can fix it for free.

---

## 3. Proposed design

Every rule below is **additive** and lives inside a media query that a desktop browser does not match, except where explicitly flagged. §5 states the guarantee.

Two new breakpoints, both narrower than the existing 900 px rule so they compose rather than conflict:

```css
/* Phone-shaped viewport. */
@media (max-width: 640px) { ... }

/* Short viewport — a phone in landscape, and nothing a laptop reaches. */
@media (max-height: 560px) { ... }

/* Thumb, not cursor. Orthogonal to width. */
@media (pointer: coarse) { ... }
```

`(max-height: 560px)` is chosen because the tallest phone in landscape is 430 px and the shortest laptop viewport in normal use is ~600 px. `(pointer: coarse)` never matches a mouse or trackpad.

### 3.1 Bound the canvas by height as well as width — fixes 2.1, and 2.9 for free

Introduce a single custom property that the media queries set and `.stage` consumes.

**In `web/src/styles/components.css`, change `.stage` to:**

```css
.stage {
  background: #fffefb;
  border: 4px solid var(--ink);
  border-radius: calc(var(--radius) + 2px);
  box-shadow: 5px 6px 0 rgba(38,50,56,.14);
  overflow: hidden;
  box-sizing: content-box;          /* NEW — see below */
  aspect-ratio: 4 / 3;
  width: min(100%, calc(var(--stage-max-h, 100000px) * 4 / 3));
  max-height: var(--stage-max-h, none);
  margin-inline: auto;
  touch-action: none;
}
```

`--stage-max-h` is undefined by default, so `width` resolves to `min(100%, 133333px)` = `100%` and `max-height` to `none` — **byte-for-byte the current desktop behaviour**. The media queries then define it:

```css
@media (max-width: 640px)  { .stage { --stage-max-h: 46svh; } }
@media (max-height: 560px) { .stage { --stage-max-h: 72svh; } }
```

Because `width` is already clamped to `--stage-max-h × 4/3`, `max-height` never actually binds — the aspect ratio holds exactly and the box simply centres itself with `margin-inline: auto`. This is the important property: it *narrows* the canvas rather than squashing it.

Resulting sizes:

| Viewport | before | after | canvas ÷ viewport height |
| --- | --- | --- | --- |
| 390 × 844 portrait | 358 × 268.5 | 358 × 268.5 (unchanged — `46svh` of 844 ≈ 388 px, so width still binds) | 31.8 % |
| 360 × 800 portrait | 328 × 246 | 328 × 246 (unchanged) | 30.8 % |
| 844 × 390 landscape | 793.4 × **595** | **374 × 280.5** | **72 %** |

Portrait is untouched; landscape stops being broken. `svh` (small viewport height) is used rather than `vh` deliberately: `svh` is the height with browser toolbars *shown*, so the canvas never exceeds the visible area even before the user scrolls. `svh` has been supported in Safari since 15.4 and Chrome since 108.

Use `--stage-max-h` in `web/src/styles/screens.css` for the filmstrip too — `.filmstrip-wrap > .stage` (`web/src/styles/screens.css:117`) inherits the same rule automatically, and `.filmstrip-thumb .stage` (`:131`) is fixed at `5.5rem` wide so `min()` leaves it alone.

**The `box-sizing: content-box` line fixes 2.9.** With it, `aspect-ratio` applies to `.stage`'s content box, so the box the pad fills is exactly 4:3. The pad's own 2 px border must then stop taking layout space — change `web/src/canvas/surface.ts:76` from `border:2px solid var(--border-str,#c6cee0)` to `box-shadow:inset 0 0 0 2px var(--border-str,#c6cee0)`, which is visually equivalent (the border is decorative and mostly hidden under `.stage`'s own 4 px ink frame) and occupies no layout. `.verso-pad`'s `width:100%` then produces an exactly-4:3 pad, and `max-height:100%` never clamps. This is the one change in this plan that also alters desktop — by 0.35 % of vertical scale, in the direction of correctness. Flagged explicitly rather than buried; if the reviewer would rather not touch desktop rendering at all, drop the `box-sizing` line and the pad `box-shadow` change and accept 2.9 as permanent. **Recommendation: take the fix.** A shared-canvas game should render the shared canvas at the ratio it claims.

**Rejected alternative:** changing `LOGICAL_W`/`LOGICAL_H` to something portrait-shaped, or making the grid orientation-dependent. Rejected because the grid is the coordinate contract every client and the server agree on (`web/src/canvas/grid.ts:19`, mirrored server-side per the comment at `:14`), because `docs/IMPLEMENTATION_PLAN.md:275` already reasoned this through correctly, and because two clients on differently-shaped grids would no longer see the same picture — which is the product.

**Also rejected:** giving `Surface` orientation-awareness. `Surface.resize()` (`web/src/canvas/surface.ts:303`) reads its container and nothing else, which is correct separation. The container's size is a layout question and belongs in CSS. `ResizeObserver` on `.pad` (`web/src/canvas/surface.ts:243`) already picks up every change these media queries cause, including rotation, with no code change.

### 3.2 Move the pen rack under the canvas on phones — fixes 2.2

**This is the one architectural decision in the plan, so it is stated plainly.**

The rack cannot be moved with CSS alone. `order` reorders siblings within a flex container; `pens.root` is a grandchild of `.cols`, inside `.col-right`. Moving it into `.col-main` unconditionally would change the desktop layout, which is forbidden. The three options:

1. **Reparent under a `matchMedia` listener.** ~12 lines in `web/src/ui/screens/drawing.ts`.
2. **Build a bottom-sheet / drawer for the tools.** New component, new open/closed state, new focus management, new `aria-expanded` wiring, and it hides the palette behind a tap.
3. **Duplicate the rack into both columns and hide one.** Two live `ToolsView` instances with independent selection state that must be kept in sync. Rejected outright — `tools()` (`web/src/ui/tools.ts:42`) owns `colorIndex`/`widthIndex` as closure state and pushes them into the engine on click.

**Recommendation: option 1.** It is the smallest change, it reuses a pattern the codebase already uses in this exact file (`right.insertBefore(ruleCard, pens.root)` at `web/src/ui/screens/drawing.ts:218`, and `root.insertBefore(gauge, swatches)` at `web/src/ui/tools.ts:201`), and above the breakpoint the node is never touched, so desktop is provably unchanged.

**In `web/src/ui/screens/drawing.ts`, after `root.appendChild(view)` (line 136):**

```ts
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
```

Inserting before `paint.root` puts the rack between the canvas and the paintress strip. New offset to the top of the rack at 390 × 844: `47.6 + 16 + 155 + 12.8 + 268.5 + 12.8 ≈ 513 px`, with the rack's ~250 px running to ~763 px. The canvas *and* the swatches are both on screen at once; only the nib row needs a short scroll.

**Two supporting changes, both phone-only:**

- **Hide the paintress on phones.** `web/src/styles/components.css:361` already shrinks `.easel` to `4.6rem` below 900 px; below 640 px, set `.easel { display: none; }`. She is explicitly decoration (`web/src/styles/components.css:309`: *"Ambient decoration on a floor strip below the stage"*), and 64 px of decoration between the canvas and the tools is the wrong trade on a 390 px-wide screen. This preserves the rule from `~/.claude/projects/-Users-ss-Code-verso/memory/verso-motion-off-canvas.md` — motion stays off the bitmap — because removing her removes motion rather than moving it onto the canvas.
- **Stop `.phasehead` wrapping.** Below 640 px, `.phasehead-main { flex-basis: 100%; }` is wrong (it forces the wrap); instead reduce the timer's footprint: `.phasehead { gap: .6rem; } .phasehead-main { flex-basis: 160px; } .timer { min-width: 5.5rem; flex-direction: row; align-items: center; gap: .5rem; } .timer-bar { flex: 1 1 auto; }`. `160 + 88 + 9.6 = 258 px` fits the 318.8 px inner width, so the header stays one row and drops from ~155 px to ~100 px. That is another 55 px of canvas-and-tools visible at once.

### 3.3 Make `touch-action` conditional on being able to draw — fixes 2.3

The hook already exists and is already correct. `CanvasEngine.setDrawingEnabled()` calls `surface.setDrawingAffordance(enabled)` (`web/src/canvas/engine.ts:261`), which writes `pad.dataset.drawing` (`web/src/canvas/surface.ts:418`). `[data-drawing="true"]` means precisely *"this client may put ink down right now"*, and it is driven from `setInteractive` through `web/src/main.ts:61`.

**In `web/src/canvas/surface.ts:75`–`78`, change the default and add the armed case:**

```
.verso-pad{...touch-action:pan-y;...}
.verso-pad canvas{...touch-action:pan-y;...}
.verso-pad[data-drawing="true"],
.verso-pad[data-drawing="true"] canvas{touch-action:none;}
```

**And in `web/src/styles/components.css:107`, `:109`,** mirror it against `.stage-locked` (toggled by `web/src/ui/stage.ts:23`, and set by discussion/intermission/result):

```css
.stage        { touch-action: none; }
.stage-locked, .stage-locked > canvas { touch-action: pan-y; }
```

`pan-y` allows vertical page scroll while still suppressing horizontal pan, pinch and double-tap-zoom over the canvas.

**Two constraints this respects, both from the existing docs:**

- `docs/IMPLEMENTATION_PLAN.md:347` warns that `touch-action: manipulation` suppresses `pointercancel` on iOS (WebKit 240917) and produces stuck strokes. `pan-y` is a different value and is not implicated — but more decisively, `pan-y` is only ever in effect when the client *cannot start a stroke at all*, so there is no live stroke for a missing `pointercancel` to strand.
- `web/src/canvas/input.ts:145`'s non-passive `touchmove` guard is unchanged and still calls `preventDefault()` whenever a stroke is active, so the artist's own drawing is unaffected on every path.

**Acceptance risk to check by hand:** the transition instant. When a turn starts, `setDrawingEnabled(true)` flips the attribute mid-gesture-window. Verify that a scroll already in flight when the artist's turn begins does not leave a half-scrolled page with a stuck pointer. Expected to be fine — `setEnabled(false)` calls `abort()` (`web/src/canvas/input.ts:77`) on the way out — but it is exactly the kind of thing only a device reproduces.

### 3.4 One coarse-pointer block for touch targets — fixes 2.4

A single additive block. `(pointer: coarse)` is false for every mouse and trackpad, so this is unreachable from a laptop.

```css
@media (pointer: coarse) {
  .pitem-kick  { width: 2.75rem; height: 2.75rem; font-size: 1.25rem; }
  .nib         { height: 2.75rem; }
  .appbar-sound{ width: 2.75rem; height: 2.75rem; }
  .appbar-sound-glyph { font-size: 1.25rem; }
  .btn-sm      { padding: .7rem 1rem; }
  .seg > button{ padding: .8rem .4rem; }
  .face-arrow  { width: 2.75rem; height: 2.75rem; }
  .swatches    { gap: .85rem .55rem; }
}
```

`2.75rem = 44 px`. `.btn-sm` at `.7rem` vertical padding gives `20.4 + 22.4 + 4 = 46.8 px`; `.seg > button` gives `23.2 + 25.6 = 48.8 px`.

`.pitem-kick` at 44 px needs a check the others do not: `.pitem` is `align-items: center` with `.35rem` vertical padding (`web/src/styles/components.css:44`) around a `2.1rem` avatar, so the row is currently ~50 px and a 44 px button fits without growing it. Confirm during QA that the roster does not gain height.

The `.swatches` gap bump is the minimum that keeps an armed swatch's `outline: 3px` at `outline-offset: 3px` (`web/src/styles/components.css:149`) from overlapping its neighbour: 8.8 px of gap against 6 px of outline per side.

### 3.5 Gate every `:hover` behind `@media (hover: hover)` — fixes 2.5

Wrap all eight hover rules. The vote picker is the one that changes behaviour; the rest are consistency, and doing them together avoids a codebase where hover-gating is a per-rule judgement call.

- `web/src/styles/base.css:59`, `:72`, `:73`, `:74` — `.btn` family
- `web/src/styles/layout.css:34` — `.appbar-sound`
- `web/src/styles/components.css:67` — `.pitem-kick`
- `web/src/styles/components.css:220` — `.votecard` ← **the one that matters**
- `web/src/styles/components.css:438` — `.face-arrow`

Leave `web/src/styles/components.css:66` (`.pitem:hover .pitem-kick`) alone — it is already correctly paired with the `@media (hover: none)` override at `:71`.

**Additionally**, make the armed vote state distinguishable by more than a border, so a stale hover cannot impersonate it even if one slips through. Change `web/src/styles/components.css:221` to add a second signal already used elsewhere in the app's vocabulary:

```css
.votecard[aria-pressed="true"] {
  border-color: var(--accent);
  background: var(--accent-sf);
  box-shadow: 3px 3px 0 rgba(239,93,90,.22);
}
```

This is desktop-visible. It is a small addition to a state that already exists, in the app's existing offset-shadow idiom (`web/src/styles/components.css:50` uses the same treatment for `.pitem-artist`), and it makes an irreversible action legible on both pointer types. **Recommendation: take it.**

### 3.6 Give the handoff overlay a compact phone layout — fixes 2.6

Below 640 px, cut the three fixed costs and let it scroll rather than escape:

```css
@media (max-width: 640px) {
  .handoff-overlay { padding: .6rem; overflow: hidden; }
  .handoff-title   { margin: .5rem 0 .4rem; font-size: clamp(1.2rem, 6vw, 1.65rem); max-width: 18ch; }
  .handoff-detail  { gap: .45rem; padding: .3rem .5rem; }
  .handoff-detail .avatar-lg { width: 2.1rem; height: 2.1rem; font-size: .8rem; }
  .handoff-clock   { margin-top: .55rem; padding: .4rem; }
}
```

Recomputed at 360 px against 246 − 19.2 = 226.8 px available: badge 29.3 + title margins 14.4 + title ~46 (2 lines at 21.6 px, `line-height: .95`, `18ch`) + detail 43.6 + clock 8.8 + 12.8 + 44.8 ≈ **200 px**. Fits, with 27 px of slack for a long name. `overflow: hidden` is the backstop for a pathological name — the badge and title are the load-bearing content and they are first in the flow, so what gets cut is the least important thing rather than the clock.

If §3.1's `46svh` cap ever binds on a very short portrait phone the overlay shrinks with it, so this should be re-checked at 360 × 640 during QA.

### 3.7 Let the appbar wrap — fixes 2.7

`web/src/styles/layout.css:4`, additive:

```css
@media (max-width: 640px) {
  .appbar      { flex-wrap: wrap; row-gap: .35rem; gap: .55rem; }
  .appbar-meta { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .conn        { flex: none; }
}
```

Wrapping is preferred over truncating the connection label: `"Reconnecting"` and `"Disconnected"` are the two states a player most needs to read in full, and `web/src/ui/chrome.ts:102` already renders a full explanatory `.banner` below the bar for both — so the bar wrapping to two rows in exactly those states is consistent with what the app already does there.

### 3.8 Adopt `viewport-fit=cover` with insets, as one change — fixes 2.8

Both halves ship together or neither does.

**`web/index.html:5`:**

```html
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
```

**`web/src/styles/layout.css`,** unconditional (the `env()` values are `0px` on every device without insets, including every desktop browser, so this is a no-op off-phone):

```css
.appbar { padding-top:  calc(.5rem + env(safe-area-inset-top));
          padding-left: calc(clamp(.7rem, 3vw, 1.5rem) + env(safe-area-inset-left));
          padding-right:calc(clamp(.7rem, 3vw, 1.5rem) + env(safe-area-inset-right)); }
.banner { padding-left: calc(clamp(.7rem, 3vw, 1.5rem) + env(safe-area-inset-left));
          padding-right:calc(clamp(.7rem, 3vw, 1.5rem) + env(safe-area-inset-right)); }
.screen { padding: clamp(1rem, 3vw, 1.8rem);
          padding-left:   calc(clamp(1rem, 3vw, 1.8rem) + env(safe-area-inset-left));
          padding-right:  calc(clamp(1rem, 3vw, 1.8rem) + env(safe-area-inset-right));
          padding-bottom: calc(clamp(1rem, 3vw, 1.8rem) + env(safe-area-inset-bottom)); }
.toasts { bottom: calc(1.2rem + env(safe-area-inset-bottom)); }
```

Landscape is the case that makes this worth doing: with `viewport-fit=cover` the `body` dot-grid and the appbar's translucent background finally extend edge to edge, and the `env(safe-area-inset-left/right)` padding keeps the room code and the canvas out from under the notch.

`.cin` (`web/src/styles/components.css:467`) is `position: fixed; inset: 0` and deliberately full-bleed — the ejection cinematic *should* cover the notch. Leave it alone.

### 3.9 Decisions deliberately not taken

- **No drawer, sheet, tab bar or hamburger.** Every phone problem above is a sizing or ordering problem, and each has a fix that costs fewer than twenty lines. A drawer would add state, focus management and animation to a codebase whose whole premise is a small `el()` helper and plain CSS.
- **The roster stays where it is on phones** — last, under the word panel. It is genuinely the least urgent thing on a drawing screen, and `web/src/ui/playerList.ts` already condenses the turn state into the `.plist-rail` track with `"up next"` / `"in N turns"` subtitles (`web/src/ui/playerList.ts:135`), which is exactly what `docs/DESIGN.md:236` asks for. If phone QA shows players still cannot answer *"when am I up?"*, the follow-up is a compact turn strip in the phase header, not a roster drawer. Deliberately deferred rather than designed speculatively.
- **No orientation-lock prompt.** §3.1 makes landscape work; telling players to rotate their phone back is worse than supporting the orientation they chose.
- **No change to `MAX_SURFACE_BYTES`** (`web/src/canvas/surface.ts:49`). §3.1 makes the canvas *smaller* in landscape and identical in portrait, so the byte budget only gets easier.

---

## 4. Prioritised implementation order

Each step is independently shippable and independently revertable.

| | Item | Files | Fixes |
| --- | --- | --- | --- |
| **P0.1** | Height-bound the stage | `web/src/styles/components.css:99`, `web/src/canvas/surface.ts:76` | 2.1, 2.9 |
| **P0.2** | Conditional `touch-action` | `web/src/canvas/surface.ts:75`, `web/src/styles/components.css:107` | 2.3 |
| **P0.3** | Pen rack under the canvas on phones (+ hide paintress, unwrap phase header) | `web/src/ui/screens/drawing.ts:136`, `web/src/styles/components.css:361`, `:92` | 2.2 |
| **P1.4** | Coarse-pointer target sizes | `web/src/styles/components.css`, `base.css`, `layout.css` | 2.4 |
| **P1.5** | Hover gating + vote-armed shadow | 8 rules across all three stylesheets | 2.5 |
| **P1.6** | Handoff overlay compaction | `web/src/styles/screens.css:156` | 2.6 |
| **P2.7** | Appbar wrap | `web/src/styles/layout.css:4` | 2.7 |
| **P2.8** | `viewport-fit=cover` + `env()` insets | `web/index.html:5`, `web/src/styles/layout.css` | 2.8 |

P0.1 and P0.2 are pure CSS, touch no TypeScript, and together turn the two states where the app is unusable on a phone (landscape, and any non-artist screen) into merely imperfect ones. If only one thing ships, ship those two.

P0.3 is the only step with a TypeScript change and the only one carrying a real design decision (§3.2). It should be reviewed on its own merits even if P0.1/P0.2 are approved on sight.

P2.8 must ship as a single commit — the meta tag and the `env()` padding are one change, per §2.8.

---

## 5. What does not change for desktop, and why that is guaranteed

The user's position is that the desktop layout is good. The mechanism that protects it:

1. **Every new rule lives in a query a laptop does not match.** `(max-width: 640px)` — a laptop viewport is ≥ 1024 px. `(max-height: 560px)` — a laptop viewport is ≥ 600 px, and a browser window deliberately dragged that short is already outside the design's target. `(pointer: coarse)` — a mouse and a trackpad both report `fine`. `(hover: hover)` — §3.5 *adds* a condition that desktop satisfies, so those rules keep applying exactly as they do today.
2. **The existing 900 px and 1100 px breakpoints are not touched.** `web/src/styles/layout.css:74` and `:78` keep their current selectors and declarations. The new breakpoints are strictly narrower, so at 641–900 px the behaviour is byte-identical to today.
3. **`--stage-max-h` is undefined outside the two new queries.** `min(100%, calc(var(--stage-max-h, 100000px) * 4/3))` collapses to `100%` and `max-height: var(--stage-max-h, none)` to `none`. The computed desktop values are unchanged, not merely similar.
4. **The one DOM change is gated on `matchMedia("(max-width: 640px)")` and is a no-op above it.** `placePens()` returns early when the node is already in `right`, which is where it is constructed (`web/src/ui/screens/drawing.ts:134`). On a desktop the listener fires zero times and `insertBefore` is never called.
5. **No shared token, no `body` rule, no `.card`/`.btn`/`.cols` base declaration is modified.** Nothing in `web/src/styles/tokens.css` changes.

**The three exceptions, stated rather than hidden.** These are visible on desktop and each is an explicit recommendation, not a side effect:

- §3.1's `box-sizing: content-box` on `.stage` plus the `.verso-pad` border→inset-shadow swap. Changes the rendered canvas ratio by 0.35 % on a desktop viewport, toward exactly 4:3. Droppable if the reviewer prefers zero desktop delta.
- §3.5's `box-shadow` on `.votecard[aria-pressed="true"]`. Adds a second, non-colour signal to an armed vote on both pointer types.
- §3.8's `env(safe-area-inset-*)`. Computes to `0px` on every desktop browser — technically a change to the declarations, provably not a change to the pixels.

---

## 6. Acceptance checks

### 6.1 Mechanical

- `cd web && npm run typecheck` passes.
- `cd web && npm run build` passes. Baseline for comparison: 121 modules, CSS 37.91 kB / 8.64 kB gzip, JS 180.33 kB / 56.65 kB gzip. The JS delta from §3.2 should be well under 1 kB; a larger jump means something unintended was pulled in.
- `grep -rn "user-scalable\|maximum-scale" web/index.html` stays empty — zoom must remain available (WCAG 2.0 SC 1.4.4, and `web/src/ui/stage.ts:11` depends on it).

### 6.2 Desktop non-regression — run first, before any phone check

At **1440 × 900** and **1280 × 800**, with a mouse:

- Lobby, drawing, discussion, result and final screens are pixel-identical to the pre-change build, except for the three §5 exceptions. Screenshot-diff each screen if practical.
- The three-column grid still reads players | canvas | phase panel; the pen rack is still in the right rail on the drawing screen.
- Hover feedback still appears on `.btn`, `.votecard`, `.face-arrow`, `.appbar-sound` and `.pitem-kick`.
- At **1000 × 800** and **950 × 800** the existing 1100 px two-column and 900 px stacked layouts behave exactly as before.
- At **700 × 800** — between the old 900 px breakpoint and the new 640 px one — nothing from §3 has engaged yet.

### 6.3 Phone, portrait

Emulated at **360 × 800**, **390 × 844**, **428 × 926**; then at least one real device, ideally one iOS Safari and one Android Chrome, since §3.3 and §3.8 are both platform-behaviour-dependent.

- **Drawing screen, as the artist:** the canvas and the twelve swatches are both visible without scrolling. Timer visible. Tapping a swatch changes the pen without the page jumping.
- **Drawing screen, as a watcher:** a vertical swipe starting *on the canvas* scrolls the page. This is the §3.3 check and the single most important phone regression test.
- **Discussion screen:** same swipe test; then vote — arm one candidate, arm a different one, confirm only the second reads as armed (§2.5). Tap "Lock it in" and confirm the 44 px target is comfortable.
- **Intermission/handoff:** the badge, the name, the avatar and the countdown clock are all inside the canvas frame with nothing clipped or spilling. Test with the longest name the server allows (`LIMITS.maxNameLength`).
- **Lobby:** the settings steppers (`−`/`+`), the difficulty/pen-rule/result segmented controls and "Copy link" are all comfortably tappable one-handed. Host: the roster's remove `×` is 44 px and is not accidentally hit while reading a row.
- **Appbar:** join a full room and force a reconnect (kill the network briefly). The bar wraps to two rows rather than producing a horizontal page scroll. Confirm `document.documentElement.scrollWidth === document.documentElement.clientWidth` on every screen.
- **Notched device:** nothing sits under the notch or the home indicator; the appbar background and the dot-grid reach the physical screen edges; a toast is fully readable above the bottom toolbar.

### 6.4 Phone, landscape — the §3.1 check

Emulated at **800 × 360**, **844 × 390**, **926 × 428**; plus a real rotation, not just a resize.

- The entire canvas is visible without scrolling on the drawing, discussion, intermission and result screens.
- Rotating mid-stroke does not strand a live stroke. `pointercancel` fires on rotation (`docs/IMPLEMENTATION_PLAN.md:340`) and `web/src/canvas/input.ts:57` handles it; confirm the stroke closes cleanly and the canvas redraws at the new size with no ink lost. This exercises `Surface.resize()`'s coalescing (`web/src/canvas/surface.ts:277`) and the dot-only resize path from `docs/REMOTE_DOT_LATENCY_PLAN.md` §5 simultaneously.
- Rotating while the browser toolbars are collapsed and again while expanded — `svh` should keep the canvas inside the visible area in both.
- No horizontal page scroll at any of the three widths.

### 6.5 Cross-client consistency — do not skip

Two devices of different shapes (one phone portrait, one laptop) in the same room, drawing the same round:

- A stroke drawn on the phone lands at the same place on the laptop's canvas and vice versa. §3.1 changes only the CSS box, and `toLogicalX`/`toLogicalY` (`web/src/canvas/surface.ts:84`) normalise by the measured rect, so this should hold — but it is the property the whole game depends on, and it is worth confirming by eye after any change to the stage's sizing.
- With §3.1's `box-sizing` fix taken, both clients render the drawing at the same 4:3 ratio rather than at slightly different stretches.

### 6.6 Accessibility, unchanged by this plan but re-checked because layout moved

- `prefers-reduced-motion` still flattens everything (`web/src/styles/base.css:141`).
- Tab order on the drawing screen after §3.2's reparent: the rack now precedes the status card in the DOM on phones. Confirm the order still reads sensibly and that nothing is reachable only by scrolling.
- The `.pitem-kick` focus-visible outline (`web/src/styles/components.css:68`) is not clipped by the larger button.

---

## 7. Open questions for review

1. **§3.1's `box-sizing` fix** — take the 0.35 % desktop ratio change to get an exactly-4:3 shared canvas, or keep desktop at zero delta and accept 2.9 permanently? Recommended: take it.
2. **`46svh` in portrait** — chosen so it never binds on any current phone (it is a guard against very short portrait viewports, e.g. a 360 × 640 device or a phone with a large keyboard accessory bar). Is a cap that is inert on all listed test devices worth having? Recommended: yes, as a cheap floor; it costs one declaration.
3. **§3.2's `matchMedia` reparent** — the only TypeScript change in the plan. If a reviewer would rather ship zero JS for this, the fallback is the duplicate-rack approach, which §3.2 rejects on state-synchronisation grounds. There is no CSS-only option.
4. **Hiding the paintress below 640 px** (§3.2) — she is deliberate, documented decoration and someone chose to draw her. Confirm with whoever owns that decision before removing her from the phone layout. The 64 px she costs sits exactly between the canvas and the tools, which is why she is on the list at all.

---

## 8. Implementation record

**Date:** 2026-08-29, against `9fc1f5a` plus the uncommitted `internal/words/words.go` edit that predates this work and is unrelated to it. Everything below is what the code now does, not what §3 proposed — where the two differ, §8.3 says so.

### 8.1 Implemented

Every item in §4, in the order §4 gives.

| | Item | Where | Fixes |
| --- | --- | --- | --- |
| **P0.1** | Height-bound the stage, `content-box`, pad border → inset shadow | `web/src/styles/components.css` (`.stage`), `web/src/styles/screens.css` (`.filmstrip-thumb .stage`), `web/src/canvas/surface.ts` (`PAD_RULE`) | 2.1, 2.9 |
| **P0.2** | `touch-action` conditional on being able to draw | `web/src/canvas/surface.ts` (`.verso-pad`, `[data-drawing="true"]`), `web/src/styles/components.css` (`.stage-locked`) | 2.3 |
| **P0.3** | Pen rack under the canvas on phones; paintress hidden; phase header unwrapped | `web/src/ui/screens/drawing.ts` (`placePens`), `web/src/styles/components.css` (`.easel`, `.phasehead`/`.timer` at 640px) | 2.2 |
| **P1.4** | Coarse-pointer target sizes | `web/src/styles/components.css` (one `@media (pointer: coarse)` block at the foot of the file) | 2.4 |
| **P1.5** | Hover gating, all eight rules, plus the vote-armed shadow | `web/src/styles/base.css`, `layout.css`, `components.css` | 2.5 |
| **P1.6** | Handoff overlay compaction | `web/src/styles/screens.css` | 2.6 |
| **P2.7** | Appbar wrap | `web/src/styles/layout.css` | 2.7 |
| **P2.8** | `viewport-fit=cover` **and** `env()` insets, together | `web/index.html`, `web/src/styles/layout.css` (`.appbar`, `.banner`, `.screen`, `.toasts`) | 2.8 |

Nothing was skipped. All four §7 open questions were resolved to the recommendation the plan itself gives: the `box-sizing` fix taken (Q1), the `46svh` portrait floor kept (Q2), the `matchMedia` reparent taken over the duplicate-rack fallback (Q3), and the paintress hidden below 640px (Q4). **Q4 is the one that is a taste decision rather than a technical one, and §7.4 asks for the owner's confirmation before removing her.** It is implemented per §3.2's recommendation and is one line — `@media (max-width: 640px) { .easel { display: none; } }` in `web/src/styles/components.css` — so it is trivially revertable if that confirmation does not come.

The three §5 exceptions — deliberately visible on desktop — all shipped: `content-box` on `.stage` with the pad's border swapped for an inset shadow, the `box-shadow` on `.votecard[aria-pressed="true"]`, and the `env()` insets (which compute to `0px` off-device).

### 8.2 Mechanical checks (§6.1)

- `cd web && npm run typecheck` — **passes**, clean.
- `cd web && npm run build` — **passes**. 121 modules, unchanged from the baseline.
  - CSS `37.91 kB / 8.64 kB gzip` → `40.03 kB / 9.08 kB gzip` (+2.12 kB raw).
  - JS `180.33 kB / 56.65 kB gzip` → `180.73 kB / 56.77 kB gzip`. **+0.40 kB**, inside the "well under 1 kB" bound §6.1 sets for §3.2's `matchMedia` block, so nothing unintended was pulled in.
- `grep -rn "user-scalable\|maximum-scale" web/index.html` — still empty. The meta tag gained only `viewport-fit=cover`; pinch-zoom is untouched.

### 8.3 Deviations from the proposed code

Four, all forced by re-checking §3's CSS against the cascade rather than by preference.

1. **`.stage`'s width formula subtracts the frame.** §3.1 proposes `box-sizing: content-box` together with `width: min(100%, …)`. Under `content-box` the `width` property sizes the *content* box, so `100%` plus an 8px border makes the border box 8px wider than its container — an 8px horizontal overflow on every viewport, desktop included, which would have broken §5's guarantee and re-created §2.7's failure mode everywhere. The rule now carries `--stage-frame: 4px`, uses it in the `border` shorthand, and subtracts it back out: `width: min(100% - var(--stage-frame) * 2, calc(var(--stage-max-h, 100000px) * 4 / 3))`. `--stage-max-h` still defaults to undefined, so the desktop computed width is `100% - 8px` content + 8px border = exactly the container, as before.
   - Consequently `web/src/styles/screens.css:131` sets `--stage-frame: 2.5px` instead of `border-width: 2.5px`, so the filmstrip thumbnail subtracts its own frame rather than the default one and stays exactly `5.5rem` wide.
2. **The pad's drawing-affordance shadow composes.** §3.1 swaps `.verso-pad`'s border for `box-shadow: inset 0 0 0 2px …`, but `.verso-pad[data-drawing="true"]` already declares its own `box-shadow` for the accent glow, which would have replaced the new hairline outright the moment a turn started. Both are now emitted from one `PAD_RULE` constant: `box-shadow: ${PAD_RULE}, 0 0 0 3px var(--accent-sf)`.
3. **`drawing.ts`'s rule-card insertion is now phone-safe.** `right.insertBefore(ruleCard, pens.root)` at the old `:218` throws `NotFoundError` once `placePens()` has moved `pens.root` into `.col-main` — and its guard (`!iAmArtist` under a non-FREE rule) is exactly the common phone case. It now targets `pens.root` when the rack is still in the rail and `word.root` when it is not, which preserves the intended "rule card above the pen card" order on desktop. §3.2 did not flag this; it is a direct consequence of the reparent and would have been a hard crash on the drawing screen.
4. **§3.4's coarse-pointer block is one block, not three.** It sits at the foot of `web/src/styles/components.css` rather than being split across `components.css`, `base.css` and `layout.css` as §4's file column implies. `components.css` is imported last (`web/src/styles/index.css`), so it overrides `.btn-sm` and `.seg > button` from `base.css` and `.appbar-sound` from `layout.css` on source order alone, with no specificity raised anywhere.

### 8.4 Not fixed, and known to be not fixed

Three things this pass leaves alone. None is a regression from the pre-change build; the first is pre-existing, the other two are cosmetic side effects of P0.1 that only exist in landscape.

- **The final screen's filmstrip thumbnails keep `touch-action: none`.** `web/src/ui/screens/final.ts:178` builds them as a bare `.stage` without `.stage-locked`, so P0.2's override does not reach them and a swipe that starts on one of the 88px thumbnails still scrolls neither the strip nor the page. §2.3 enumerated the drawing, discussion, intermission and result screens and did not list this one, and adding `.stage-locked` there would make the thumbnail border dashed on desktop. Left for a follow-up rather than fixed off-plan.
- **`.handoff-overlay` still spans the full column, not the canvas.** It is `position: absolute; inset: 0` over `.handoff-canvas` (`web/src/ui/screens/intermission.ts:30`), which is a full-width block. Now that P0.1 narrows and centres the stage in a short viewport, the overlay's `rgba(255,253,247,.22)` wash extends a little past the ink frame on both sides in landscape. Its children are centred, so the badge, name, avatar and clock still sit over the paper. Worth an eye during §6.4.
- **The paintress's floor strip is wider than the canvas between 641px and 900px in landscape.** She is hidden below 640px but not by the `(max-height: 560px)` query, so on a landscape phone the dashed strip under `.easel::before` runs the full column while the stage above it is narrower. Also cosmetic, also for §6.4.

### 8.5 What has NOT been verified

**Everything in §6.2 through §6.6.** No browser was opened, no device was used, no screenshot was taken and no screenshot diff was run. The claims this record makes are limited to what typecheck, the production build and reading the cascade can establish:

- Desktop non-regression (§6.2) is argued from §5's mechanism — every new rule is inside `(max-width: 640px)`, `(max-height: 560px)`, `(pointer: coarse)` or `(hover: hover)`, and the three stated exceptions — and from the computed-value reasoning in §5.3 and §8.3. It has **not** been observed at 1440×900 or anywhere else.
- The §3.3 acceptance risk — a scroll in flight when `setDrawingEnabled(true)` flips `data-drawing` mid-gesture — is exactly the kind of thing only a device reproduces, and no device has run it.
- The §3.6 arithmetic that the compacted handoff overlay fits in ~200px, and the §3.2 arithmetic that the rack now starts ~513px down, are recomputations of numbers this plan already stated. Neither has been measured against a rendered page, and neither accounts for a real font's line-breaking.
- Cross-client consistency (§6.5) is unexercised. `toLogicalX`/`toLogicalY` normalise by the measured rect and were not touched, so it should hold, but "should" is the operative word.
- iOS-specific behaviour — `svh` under collapsed versus expanded toolbars, `viewport-fit=cover` insets, `pan-y` over a canvas — is platform behaviour that a headless build cannot exercise at all.
