import { el } from "./dom.js";
import { avatarColor, initials } from "./palette.js";

export type AvatarSize = "sm" | "md" | "lg";

/**
 * Deterministic identity chip. The initials carry the identity, the colour only
 * reinforces it — nothing here is conveyed by colour alone.
 */
export function avatar(playerId: string, name: string, size: AvatarSize = "md"): HTMLElement {
  const cls = size === "md" ? "avatar" : `avatar avatar-${size}`;
  return el("span", {
    class: cls,
    style: `background:${avatarColor(playerId)}`,
    "aria-hidden": "true",
    text: initials(name),
  });
}
