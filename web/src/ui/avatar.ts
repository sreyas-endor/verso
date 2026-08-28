import { Avatar } from "../../gen/verso/v1/game_pb.js";
import { avatarEntry } from "./avatars/catalog.js";
import { el, svgEl } from "./dom.js";
import { avatarColor } from "./palette.js";

export type AvatarSize = "sm" | "md" | "lg";

/**
 * One seat's identity chip: the creature they chose, on paper, ringed in the
 * hue derived from their player id.
 *
 * The creature is the choice and the ring is the identity. Duplicate choices
 * are allowed — the picker does not discourage them — so at 1.6rem the portrait
 * alone cannot separate a ten-seat roster and the deterministic hue is what
 * does. That is also why the hue moved from the fill to the ring rather than
 * being dropped when the initials went.
 *
 * Neither half carries meaning on its own. The display name sits beside every
 * one of these, which is why the whole chip stays aria-hidden.
 */
export function avatar(playerId: string, value: Avatar, size: AvatarSize = "md"): HTMLElement {
  const cls = size === "md" ? "avatar" : `avatar avatar-${size}`;
  const chip = el(
    "span",
    { class: cls, "aria-hidden": "true" },
    svgEl(
      "svg",
      { class: "av-art", viewBox: "0 0 64 64", focusable: "false" },
      ...avatarEntry(value).art(),
    ),
  );
  // An empty id is the home-screen picker, where no seat exists yet and there
  // is no hue to derive. It keeps the neutral ring from the stylesheet, so ten
  // options do not all appear to belong to the same player.
  if (playerId !== "") chip.style.setProperty("--ring", avatarColor(playerId));
  return chip;
}

/** The value to pass for a seat the caller could not resolve. */
export const NO_AVATAR = Avatar.UNSPECIFIED;
