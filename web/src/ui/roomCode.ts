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
