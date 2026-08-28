import { Avatar } from "../../gen/verso/v1/game_pb.js";
import { AVATAR_CATALOG, FALLBACK_AVATAR } from "./avatars/catalog.js";
import { LIMITS } from "./context.js";

const CODE_RE = new RegExp(`[A-Z0-9]{${LIMITS.codeLength}}`);

/**
 * Accepts what people actually paste: a bare code, a code with spaces or
 * dashes, or the whole share link. Returns "" when nothing usable is in there.
 */
export function parseRoomCode(input: string): string {
  const cleaned = input.trim().toUpperCase();
  const hash = cleaned.lastIndexOf("#");
  const tail = hash >= 0 ? cleaned.slice(hash + 1) : cleaned;
  const alnum = tail.replace(/[^A-Z0-9]/g, "");
  if (alnum.length === LIMITS.codeLength) return alnum;
  const m = CODE_RE.exec(alnum);
  return m ? m[0] : alnum.slice(0, LIMITS.codeLength);
}

/** Room code carried in the page fragment, e.g. https://host/#ABCDE. */
export function codeFromLocation(): string {
  return parseRoomCode(window.location.hash.replace(/^#/, ""));
}

const NAME_KEY = "verso.displayName";

export function rememberedName(): string {
  try {
    return (window.localStorage.getItem(NAME_KEY) ?? "").slice(0, LIMITS.maxNameLength);
  } catch {
    return "";
  }
}

export function rememberName(name: string): void {
  try {
    window.localStorage.setItem(NAME_KEY, name);
  } catch {
    /* private mode; the field just won't prefill next time */
  }
}

const AVATAR_KEY = "verso.avatar";

/**
 * The portrait to preselect on the home screen.
 *
 * A first visit gets a RANDOM one rather than always the first in the catalog.
 * Ten people opening the game for the first time should not all arrive as the
 * beetle, and a preselected face is the whole reason Create still works on one
 * keystroke — so it has to be a face worth keeping, not a placeholder.
 *
 * Never returns UNSPECIFIED. The picker's job is to make sure the client never
 * sends one, so the server's fallback stays a defence against old builds rather
 * than a value this build relies on.
 */
export function rememberedAvatar(): Avatar {
  try {
    const stored = Number(window.localStorage.getItem(AVATAR_KEY));
    if (AVATAR_CATALOG.some((e) => e.value === stored)) return stored as Avatar;
  } catch {
    /* private mode; fall through to a random one */
  }
  const pick = AVATAR_CATALOG[Math.floor(Math.random() * AVATAR_CATALOG.length)];
  return pick?.value ?? FALLBACK_AVATAR;
}

export function rememberAvatar(value: Avatar): void {
  try {
    window.localStorage.setItem(AVATAR_KEY, String(value));
  } catch {
    /* private mode; the picker just won't preselect this next time */
  }
}
