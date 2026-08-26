import { LIMITS, type ScreenCtx } from "../context.js";
import { Disposers, el, setText } from "../dom.js";
import { codeFromLocation, parseRoomCode, rememberName, rememberedName } from "../roomCode.js";

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
    ctx.actions.createRoom(n);
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
    ctx.actions.joinRoom(c, n);
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

  if (name.value && code.value) join.focus();
  else if (name.value) create.focus();
  else name.focus();

  ctx.announce(nameOk() ? "Home. Create a room or join one with a code." : "Home. Enter your name to begin.");
}

export function unmount(): void {
  d?.dispose();
  d = null;
}
