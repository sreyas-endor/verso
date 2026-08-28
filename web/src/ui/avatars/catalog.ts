import { svgEl } from "../dom.js";
import { Avatar } from "../../../gen/verso/v1/game_pb.js";

/**
 * The ten creature portraits, drawn in code.
 *
 * They are shapes rather than image assets so they inherit the paper-and-marker
 * palette from CSS and re-ink themselves when the theme flips — an exported PNG
 * or a flat <img> would carry baked-in colour that fights the rest of the app,
 * and would need a second copy at every size the picker, roster and cinematic
 * ask for.
 *
 * The order here is the enum's order, and the enum values go on the wire: a seat
 * stores a number, so shuffling entries would silently repaint every saved
 * player. Add to the end, never in the middle.
 *
 * `art()` builds new nodes on every call. The same creature is on screen in
 * several places at once, and a shared node array cannot be appended to two
 * parents — the second append would steal the shapes out of the first.
 */
export interface AvatarEntry {
  readonly value: Avatar;
  readonly label: string;
  readonly art: () => SVGElement[];
}

function shoulder(cls: string): SVGElement {
  return svgEl("path", { class: cls, d: "M9 64c1.5-10 10-16 23-16s21.5 6 23 16z" });
}

/**
 * Hoisted out of the array because `avatarEntry` must hand back a real entry for
 * UNSPECIFIED and for values a newer server knows about, and indexing the array
 * for it would only get the compiler an `AvatarEntry | undefined`.
 */
const BEETLE: AvatarEntry = {
  value: Avatar.BEETLE,
  label: "Masked beetle",
  art: () => [
    shoulder("a-cloth"),
    svgEl("path", { class: "a-none", d: "M24 15C21 9 17 7 14 8" }),
    svgEl("path", { class: "a-none", d: "M40 15C43 9 47 7 50 8" }),
    svgEl("circle", { class: "a-ink", cx: 14, cy: 8, r: 2 }),
    svgEl("circle", { class: "a-ink", cx: 50, cy: 8, r: 2 }),
    svgEl("ellipse", { class: "a-shell", cx: 32, cy: 30, rx: 17, ry: 16 }),
    svgEl("path", { class: "a-none", d: "M32 15v9" }),
    svgEl("rect", { class: "a-stone", x: 14, y: 25, width: 36, height: 10, rx: 5 }),
    svgEl("circle", { class: "a-ink", cx: 24, cy: 30, r: 3 }),
    svgEl("circle", { class: "a-ink", cx: 40, cy: 30, r: 3 }),
    svgEl("circle", { class: "a-paper ns", cx: 25.3, cy: 28.8, r: 1 }),
    svgEl("circle", { class: "a-paper ns", cx: 41.3, cy: 28.8, r: 1 }),
    svgEl("path", { class: "a-none", d: "M26 41q6 5 12 0" }),
  ],
};

export const AVATAR_CATALOG: readonly AvatarEntry[] = [
  BEETLE,
  {
    value: Avatar.COURIER,
    label: "Long-eared courier",
    art: () => [
      shoulder("a-rust"),
      svgEl("ellipse", {
        class: "a-fur",
        cx: 20,
        cy: 13,
        rx: 5,
        ry: 12,
        transform: "rotate(-12 20 13)",
      }),
      svgEl("path", { class: "a-fur", d: "M42 23c-2-8 0-15 5-17 4-1 6 2 5 6-1 5-4 8-6 11z" }),
      svgEl("ellipse", { class: "a-fur", cx: 32, cy: 31, rx: 15, ry: 14 }),
      svgEl("path", { class: "a-cloth", d: "M18 26a14 13 0 0 1 28 0z" }),
      svgEl("path", { class: "a-none", d: "M15 26h34" }),
      svgEl("circle", { class: "a-ink", cx: 26, cy: 32, r: 2.4 }),
      svgEl("circle", { class: "a-ink", cx: 38, cy: 32, r: 2.4 }),
      svgEl("path", { class: "a-blush", d: "M32 36.4l2.6 2.4h-5.2z" }),
      svgEl("path", { class: "a-none", d: "M36 40q5 0 8-2" }),
      svgEl("path", { class: "a-none", d: "M28 40q-5 0-8-2" }),
      svgEl("path", { class: "a-none", d: "M21 51l12 13" }),
    ],
  },
  {
    value: Avatar.LANTERN,
    label: "Lantern carrier",
    art: () => [
      svgEl("path", { class: "a-cloth", d: "M32 9c12 0 20 10 20 23v18H12V32C12 19 20 9 32 9z" }),
      svgEl("ellipse", { class: "a-ink", cx: 32, cy: 32, rx: 11.5, ry: 11.5 }),
      svgEl("circle", { class: "a-brass ns", cx: 27.6, cy: 31, r: 2.1 }),
      svgEl("circle", { class: "a-brass ns", cx: 36.4, cy: 31, r: 2.1 }),
      svgEl("circle", { class: "a-brass ns", cx: 53, cy: 45, r: 10, opacity: ".26" }),
      svgEl("path", { class: "a-none", d: "M49 38q4-6 8 0" }),
      svgEl("rect", { class: "a-brass", x: 48, y: 40, width: 10, height: 11, rx: 2.5 }),
      svgEl("circle", { class: "a-paper ns", cx: 53, cy: 45.5, r: 2.6 }),
    ],
  },
  {
    value: Avatar.GEARSMITH,
    label: "Tiny gearsmith",
    art: () => [
      shoulder("a-leaf"),
      svgEl("circle", { class: "a-brass", cx: 53, cy: 55, r: 10 }),
      svgEl("circle", { class: "a-paper", cx: 53, cy: 55, r: 3.6 }),
      svgEl("path", { class: "a-none", d: "M53 45v3M63 55h-3M53 65v-3M43 55h3" }),
      svgEl("path", { class: "a-fur", d: "M22 19q4-11 10-9 8 2 10 9z" }),
      svgEl("circle", { class: "a-skin", cx: 32, cy: 31, r: 15 }),
      svgEl("path", { class: "a-none", d: "M17 23h30" }),
      svgEl("circle", { class: "a-stone", cx: 24, cy: 21, r: 5.5 }),
      svgEl("circle", { class: "a-stone", cx: 40, cy: 21, r: 5.5 }),
      svgEl("circle", { class: "a-ink", cx: 27, cy: 33, r: 2.2 }),
      svgEl("circle", { class: "a-ink", cx: 38, cy: 33, r: 2.2 }),
      svgEl("path", { class: "a-none", d: "M28 39q4 3.5 8 0" }),
      svgEl("path", { class: "a-none", d: "M24 52v7M40 52v7" }),
    ],
  },
  {
    value: Avatar.SCOUT,
    label: "Mushroom-capped scout",
    art: () => [
      shoulder("a-leaf"),
      svgEl("ellipse", { class: "a-skin", cx: 32, cy: 38, rx: 12, ry: 11 }),
      svgEl("circle", { class: "a-ink", cx: 27, cy: 38, r: 2.2 }),
      svgEl("circle", { class: "a-ink", cx: 37, cy: 38, r: 2.2 }),
      svgEl("path", { class: "a-none", d: "M29 43.5q3 3 6 0" }),
      svgEl("path", { class: "a-rust", d: "M6 29C6 16 17 8 32 8s26 8 26 21c-8 5-44 5-52 0z" }),
      svgEl("ellipse", { class: "a-paper ns", cx: 20, cy: 20, rx: 4.6, ry: 3.4 }),
      svgEl("ellipse", { class: "a-paper ns", cx: 41, cy: 16, rx: 5.4, ry: 3.9 }),
      svgEl("ellipse", { class: "a-paper ns", cx: 49, cy: 24, rx: 3.4, ry: 2.5 }),
      svgEl("path", { class: "a-cloth", d: "M21 46q11 6 22 0l2 6q-13 7-26 0z" }),
    ],
  },
  {
    value: Avatar.MOTH,
    label: "Moth herald",
    art: () => [
      svgEl("path", { class: "a-cloth", d: "M31 34C17 27 5 34 7 47c2 11 15 13 24 4z" }),
      svgEl("path", { class: "a-cloth", d: "M33 34c14-7 26 0 24 13-2 11-15 13-24 4z" }),
      svgEl("path", { class: "a-none", d: "M13 41q6 1 9 5M51 41q-6 1-9 5" }),
      svgEl("path", { class: "a-none", d: "M25 17C21 9 16 6 12 7" }),
      svgEl("path", { class: "a-none", d: "M39 17c4-8 9-11 13-10" }),
      svgEl("path", { class: "a-none", d: "M20 11l-3-3M16 9l-2-4M44 11l3-3M48 9l2-4" }),
      svgEl("circle", { class: "a-fur", cx: 32, cy: 29, r: 13 }),
      svgEl("path", { class: "a-none", d: "M20 39q3 4.5 6 0 3 4.5 6 0 3 4.5 6 0 3 4.5 6 0" }),
      svgEl("ellipse", { class: "a-ink", cx: 26.5, cy: 29, rx: 3.4, ry: 4.2 }),
      svgEl("ellipse", { class: "a-ink", cx: 37.5, cy: 29, rx: 3.4, ry: 4.2 }),
      svgEl("circle", { class: "a-paper ns", cx: 25.4, cy: 27.3, r: 1.1 }),
      svgEl("circle", { class: "a-paper ns", cx: 36.4, cy: 27.3, r: 1.1 }),
    ],
  },
  {
    value: Avatar.MASON,
    label: "Pebble mason",
    art: () => [
      shoulder("a-cloth"),
      svgEl("path", { class: "a-none", d: "M45 62L57 41" }),
      svgEl("rect", {
        class: "a-stone",
        x: 49,
        y: 32,
        width: 14,
        height: 8,
        rx: 2.4,
        transform: "rotate(-28 56 36)",
      }),
      svgEl("path", {
        class: "a-stone",
        d: "M16 25q0-9 9-10l14-1q9-1 10 8l1 12q1 9-8 10l-13 1q-9 1-10-8z",
      }),
      svgEl("path", { class: "a-none", d: "M44 21l-3 5 3 3" }),
      svgEl("path", { class: "a-none", d: "M21 28q6-4 12-1M37 27q5-2 8 1" }),
      svgEl("circle", { class: "a-ink", cx: 26, cy: 34, r: 2.2 }),
      svgEl("circle", { class: "a-ink", cx: 39, cy: 33, r: 2.2 }),
      svgEl("path", { class: "a-none", d: "M26 42q7 3 12-1" }),
    ],
  },
  {
    value: Avatar.PIPER,
    label: "Reed piper",
    art: () => [
      shoulder("a-cloth"),
      svgEl("rect", { class: "a-fur", x: 43, y: 39, width: 5, height: 20, rx: 2.5 }),
      svgEl("rect", { class: "a-fur", x: 49, y: 43, width: 5, height: 16, rx: 2.5 }),
      svgEl("rect", { class: "a-fur", x: 55, y: 47, width: 5, height: 12, rx: 2.5 }),
      svgEl("path", { class: "a-none", d: "M42 52h19" }),
      svgEl("rect", { class: "a-leaf", x: 26, y: 32, width: 11, height: 18, rx: 5.5 }),
      svgEl("ellipse", { class: "a-leaf", cx: 30, cy: 24, rx: 12, ry: 11 }),
      svgEl("path", { class: "a-leaf", d: "M27 13q2-9 7-6.5 3 1.5 1 6.5z" }),
      svgEl("path", { class: "a-brass", d: "M41 24l9 3.5-9 4z" }),
      svgEl("circle", { class: "a-ink", cx: 34, cy: 23, r: 2.5 }),
      svgEl("circle", { class: "a-paper ns", cx: 35, cy: 22, r: 1 }),
    ],
  },
  {
    value: Avatar.COOK,
    label: "Kettle cook",
    art: () => [
      shoulder("a-rust"),
      svgEl("path", { class: "a-none", d: "M48 62L57 40" }),
      svgEl("circle", { class: "a-stone", cx: 58, cy: 36, r: 5.5 }),
      svgEl("ellipse", { class: "a-skin", cx: 32, cy: 38, rx: 13, ry: 12 }),
      svgEl("circle", { class: "a-blush ns", cx: 22, cy: 41, r: 3.2 }),
      svgEl("circle", { class: "a-blush ns", cx: 42, cy: 41, r: 3.2 }),
      svgEl("path", { class: "a-none", d: "M25 37q3-3.5 6 0M35 37q3-3.5 6 0" }),
      svgEl("path", { class: "a-none", d: "M28 43q4 4.5 8 0" }),
      svgEl("path", { class: "a-none", d: "M22 14q10-10 20 0" }),
      svgEl("rect", { class: "a-stone", x: 16, y: 14, width: 32, height: 15, rx: 3 }),
      svgEl("path", { class: "a-none", d: "M13 29h38" }),
    ],
  },
  {
    value: Avatar.CARTOGRAPHER,
    label: "Snail cartographer",
    art: () => [
      svgEl("circle", { class: "a-rust", cx: 42, cy: 28, r: 16 }),
      svgEl("path", {
        class: "a-none",
        d: "M42 28a4.5 4.5 0 1 1 4.5-4.5 9 9 0 1 1-9 9 13.5 13.5 0 1 1 13.5-13.5",
      }),
      svgEl("path", { class: "a-leaf", d: "M6 51q0-17 16-20l9-1 2 23z" }),
      svgEl("path", { class: "a-leaf", d: "M4 49h36q2 7-5 8H10q-6 0-6-8z" }),
      svgEl("path", { class: "a-none", d: "M12 33V21M20 32V25" }),
      svgEl("circle", { class: "a-ink", cx: 12, cy: 19, r: 2.4 }),
      svgEl("circle", { class: "a-ink", cx: 20, cy: 23, r: 2.4 }),
      svgEl("rect", { class: "a-paper", x: 5, y: 42, width: 21, height: 7, rx: 3.5 }),
      svgEl("path", { class: "a-none", d: "M14 42v7" }),
    ],
  },
];

export const FALLBACK_AVATAR = Avatar.BEETLE;

const BY_VALUE = new Map<Avatar, AvatarEntry>(
  AVATAR_CATALOG.map((e): [Avatar, AvatarEntry] => [e.value, e]),
);

/**
 * Mirrors the server's normalization: UNSPECIFIED and anything this build has
 * not heard of resolve to the fallback, so no seat ever renders faceless.
 */
export function avatarEntry(value: Avatar): AvatarEntry {
  return BY_VALUE.get(value) ?? BEETLE;
}
