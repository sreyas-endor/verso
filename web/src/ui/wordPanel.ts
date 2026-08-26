import { el, setText } from "./dom.js";

export interface WordPanelView {
  root: HTMLElement;
  update(word: string): void;
}

/**
 * The player's own word stays reachable for the whole match (DESIGN.md:31) but
 * starts collapsed, because on a shared couch the person beside you can read it.
 */
export function wordPanel(alwaysVisible = false): WordPanelView {
  const value = el("div", { class: "wordpanel-value", text: "" });
  const body = el(
    "div",
    {},
    value,
    el("div", { class: "wordpanel-note", text: "Yours alone. Never say it, spell it, or rhyme with it." }),
  );
  const root = alwaysVisible
    ? el("section", { class: "card wordpanel wordpanel-visible" }, el("div", { class: "wordpanel-label", text: "Your word" }), body)
    : el("details", { class: "card wordpanel" }, el("summary", {}, el("span", { text: "Your word — tap to show" })), body);

  return {
    root,
    update(word) {
      setText(value, word);
      root.hidden = word === "";
    },
  };
}
