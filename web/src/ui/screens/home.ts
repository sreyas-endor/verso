import type { Avatar } from "../../../gen/verso/v1/game_pb.js";
import { avatar } from "../avatar.js";
import { AVATAR_CATALOG } from "../avatars/catalog.js";
import { LIMITS, type ScreenCtx } from "../context.js";
import { Disposers, el, setText } from "../dom.js";
import {
  codeFromLocation,
  parseRoomCode,
  rememberAvatar,
  rememberName,
  rememberedAvatar,
  rememberedName,
} from "../roomCode.js";

let d: Disposers | null = null;

export function mount(root: HTMLElement, ctx: ScreenCtx): void {
  d = new Disposers();
  const dd = d;

  const name = el("input", {
    type: "text",
    id: "vs-name",
    maxlength: LIMITS.maxNameLength,
    placeholder: "Your name",
    autocomplete: "nickname",
    value: rememberedName(),
  }) as HTMLInputElement;
  name.value = rememberedName();

  const code = el("input", {
    type: "text",
    id: "vs-code",
    class: "code-input",
    maxlength: LIMITS.codeLength,
    placeholder: "CODE",
    autocapitalize: "characters",
    autocomplete: "off",
    spellcheck: "false",
    "aria-label": `Room code, ${LIMITS.codeLength} characters`,
  }) as HTMLInputElement;
  code.value = codeFromLocation();

  const create = el("button", { type: "button", class: "btn btn-primary btn-block", text: "Create a room" }) as HTMLButtonElement;
  const join = el("button", { type: "button", class: "btn", text: "Join" }) as HTMLButtonElement;
  const err = el("p", { class: "blocker", role: "alert" });
  err.hidden = true;

  // Explains the greyed-out Create button. Colour alone must not carry it.
  const createHint = el("p", { class: "hint", text: "Clear the room code below to create a new room instead." });
  createHint.hidden = true;

  // The avatar picker: one large face with an arrow either side.
  //
  // Preselected on purpose. An avatar belongs to a seat and cannot be changed
  // once one exists, so this is the only place to choose — but making it a gate
  // would cost every player a click and break the name-then-Enter path that
  // gets a room open in one keystroke. The remembered face means the common
  // case is no presses at all.
  //
  // A cycler rather than a grid of ten: at grid size the creatures are 46px of
  // mush, and the whole point of drawing them was that somebody looks at them.
  // The cost is that reaching a specific face takes up to five presses, which
  // is what the dots are for — without a position readout a cycler feels
  // endless and never tells you that you have now seen all of them.
  let chosen: Avatar = rememberedAvatar();
  let index = Math.max(0, AVATAR_CATALOG.findIndex((e) => e.value === chosen));

  const face = el("div", { class: "face-chip" });
  // Announced rather than silent: for a screen reader the arrows are the only
  // evidence anything changed, and "Next face" does not say which face.
  const faceName = el("div", { class: "face-name", "aria-live": "polite" });
  const dots = el(
    "div",
    { class: "face-dots", "aria-hidden": "true" },
    ...AVATAR_CATALOG.map(() => el("span", { class: "face-dot" })),
  );

  const arrow = (label: string, glyph: string, step: number) => {
    const b = el(
      "button",
      { type: "button", class: "face-arrow", "aria-label": label, text: glyph },
    ) as HTMLButtonElement;
    dd.on(b, "click", () => show(index + step));
    return b;
  };
  const prevFace = arrow("Previous face", "\u2039", -1);
  const nextFace = arrow("Next face", "\u203a", 1);

  function show(to: number): void {
    const n = AVATAR_CATALOG.length;
    index = ((to % n) + n) % n;
    const entry = AVATAR_CATALOG[index];
    if (entry === undefined) return;
    chosen = entry.value;
    face.replaceChildren(avatar("", entry.value, "lg"));
    setText(faceName, entry.label);
    for (let i = 0; i < dots.children.length; i++) {
      dots.children[i]?.classList.toggle("face-dot-on", i === index);
    }
    rememberAvatar(chosen);
  }

  const picker = el(
    "div",
    { class: "facepick", role: "group", "aria-label": "Pick a face" },
    el("div", { class: "facepick-row" }, prevFace, face, nextFace),
    faceName,
    dots,
  );

  // The arrows are two tab stops, so a keyboard player who has landed on either
  // one can keep going with the arrow keys instead of alternating Tab and Space.
  dd.on(picker, "keydown", (e) => {
    if (e.key === "ArrowRight") show(index + 1);
    else if (e.key === "ArrowLeft") show(index - 1);
    else return;
    e.preventDefault();
  });

  const nameOk = () => name.value.trim().length > 0;

  // A typed code means the player means to JOIN. Creating a room here would
  // silently strand whoever gave them that code, so block it outright.
  let busy = false;
  const syncCreate = () => {
    const joining = parseRoomCode(code.value).length > 0;
    create.disabled = busy || joining;
    createHint.hidden = !joining;
  };

  const fail = (message: string, focus: HTMLInputElement) => {
    err.hidden = false;
    setText(err, message);
    focus.focus();
  };

  dd.on(create, "click", () => {
    if (create.disabled) return;
    const n = name.value.trim();
    if (!n) return fail("Type a name first — the others need to know who you are.", name);
    err.hidden = true;
    rememberName(n);
    ctx.actions.createRoom(n, chosen);
  });

  const submitJoin = () => {
    const n = name.value.trim();
    if (!n) return fail("Type a name first — the others need to know who you are.", name);
    const c = parseRoomCode(code.value);
    if (c.length !== LIMITS.codeLength) {
      return fail(`A room code is ${LIMITS.codeLength} characters. You can paste the whole link instead.`, code);
    }
    err.hidden = true;
    code.value = c;
    rememberName(n);
    ctx.actions.joinRoom(c, n, chosen);
  };

  dd.on(join, "click", submitJoin);
  dd.on(code, "keydown", (e) => {
    if (e.key === "Enter") submitJoin();
  });
  dd.on(name, "keydown", (e) => {
    if (e.key === "Enter") code.value.trim() ? submitJoin() : create.click();
  });
  // Uppercase as you type, and swallow a pasted share link.
  dd.on(code, "input", () => {
    const next = parseRoomCode(code.value);
    if (next !== code.value) code.value = next;
    syncCreate();
  });

  const view = el(
    "div",
    { class: "home" },
    el("h1", { class: "home-title" }, el("span", { text: "Ver" }), "so"),
    el("p", { class: "home-tag", text: "One of you has a different word. Draw. Argue. Vote." }),
    el(
      "section",
      { class: "card" },
      el("label", { class: "field", for: "vs-name" }, el("span", { text: "Display name" }), name),
      el("div", { class: "field" }, el("span", { text: "Pick a face" }), picker),
      el("div", { style: "height:.8rem" }),
      create,
      createHint,
      el("div", { class: "home-or" }, el("span", { text: "OR JOIN A ROOM" })),
      el(
        "label",
        { class: "field", for: "vs-code" },
        el("span", { text: "Room code or share link" }),
        el("div", { class: "row" }, el("div", { class: "grow" }, code), join),
      ),
      err,
    ),
    el(
      "section",
      { class: "card" },
      el("div", { class: "card-title", text: "How it works" }),
      el(
        "ol",
        { class: "how" },
        el("li", { text: "Everyone gets a secret word. One player's word is different — and nobody is told who." }),
        el("li", { text: "Take turns adding a quick freehand clue to one shared canvas. No letters, no erasing." }),
        el("li", { text: "Talk it over, then vote in secret. Whoever has the most votes is eliminated; a tie is safe." }),
        el("li", { text: "The group wins by eliminating the imposter. The imposter wins by surviving." }),
      ),
      el("div", { style: "height:.7rem" }),
      el("p", {
        class: "note",
        text: "There is no built-in voice or text chat. Get everyone on a call before you start.",
      }),
    ),
  );

  root.appendChild(view);
  dd.add(() => view.remove());

  const unsub = ctx.subscribe((s) => {
    busy = s.busy;
    join.disabled = s.busy;
    syncCreate();
    if (s.lastError) {
      err.hidden = false;
      setText(err, s.lastError);
    }
  });
  dd.add(unsub);

  syncCreate();
  show(index);

  if (name.value && code.value) join.focus();
  else if (name.value) create.focus();
  else name.focus();

  ctx.announce(nameOk() ? "Home. Create a room or join one with a code." : "Home. Enter your name to begin.");
}

export function unmount(): void {
  d?.dispose();
  d = null;
}
