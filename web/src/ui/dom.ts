// Tiny DOM helpers. Text only — nothing in this module ever touches innerHTML,
// because display names and room codes are attacker-controlled strings.

export type Attrs = Record<string, string | number | boolean | null | undefined>;
export type Child = Node | string | number | null | undefined | false;

export function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs?: Attrs,
  ...children: Child[]
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === null || v === undefined || v === false) continue;
      if (k === "class") node.className = String(v);
      else if (k === "text") node.textContent = String(v);
      else if (k === "style") node.setAttribute("style", String(v));
      else if (v === true) node.setAttribute(k, "");
      else node.setAttribute(k, String(v));
    }
  }
  append(node, children);
  return node;
}

const SVG_NS = "http://www.w3.org/2000/svg";

/**
 * SVG sibling of `el`. Needed because createElement puts nodes in the HTML
 * namespace, where <circle> and friends render as nothing. Attributes only —
 * decorative sprites are the sole caller, so there is no text or user data.
 */
export function svgEl<K extends keyof SVGElementTagNameMap>(
  tag: K,
  attrs?: Attrs,
  ...children: SVGElement[]
): SVGElementTagNameMap[K] {
  const node = document.createElementNS(SVG_NS, tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === null || v === undefined || v === false) continue;
      node.setAttribute(k, v === true ? "" : String(v));
    }
  }
  for (const c of children) node.appendChild(c);
  return node;
}

export function append(parent: Node, children: Child[]): void {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    parent.appendChild(typeof c === "object" ? c : document.createTextNode(String(c)));
  }
}

export function clear(node: Node): void {
  while (node.firstChild) node.removeChild(node.firstChild);
}

export function fill(parent: Node, ...children: Child[]): void {
  clear(parent);
  append(parent, children);
}

/** Sets textContent only when it actually changed, so caret/selection survive. */
export function setText(node: HTMLElement, value: string): void {
  if (node.textContent !== value) node.textContent = value;
}

export function toggle(node: HTMLElement, cls: string, on: boolean): void {
  node.classList.toggle(cls, on);
}

/** Collects teardown functions so a screen's unmount() is a single call. */
export class Disposers {
  private fns: Array<() => void> = [];

  add(fn: () => void): void {
    this.fns.push(fn);
  }

  on<K extends keyof HTMLElementEventMap>(
    target: HTMLElement,
    type: K,
    handler: (ev: HTMLElementEventMap[K]) => void,
    options?: AddEventListenerOptions,
  ): void {
    target.addEventListener(type, handler as EventListener, options);
    this.fns.push(() => target.removeEventListener(type, handler as EventListener, options));
  }

  interval(ms: number, fn: () => void): void {
    const id = window.setInterval(fn, ms);
    this.fns.push(() => window.clearInterval(id));
  }

  timeout(ms: number, fn: () => void): void {
    const id = window.setTimeout(fn, ms);
    this.fns.push(() => window.clearTimeout(id));
  }

  raf(fn: (t: number) => void): void {
    let id = 0;
    const loop = (t: number) => {
      fn(t);
      id = window.requestAnimationFrame(loop);
    };
    id = window.requestAnimationFrame(loop);
    this.fns.push(() => window.cancelAnimationFrame(id));
  }

  dispose(): void {
    for (let i = this.fns.length - 1; i >= 0; i--) this.fns[i]?.();
    this.fns = [];
  }
}
