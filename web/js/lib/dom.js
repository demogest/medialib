// Tiny DOM helpers: h('button.btn.primary', { onclick }, 'Save'), plus a few shortcuts.
export const $ = (selector, root = document) => root.querySelector(selector);
export const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

const SVG_NS = 'http://www.w3.org/2000/svg';

/** h('tag.class#id', props, ...children). props: on* handlers, class, style (object or string), dataset, aria-*, or any attribute/property. */
export function h(spec, props, ...children) {
  const m = /^([a-z][a-z0-9-]*)?((?:[.#][\w-]+)*)$/i.exec(spec);
  const tag = (m && m[1]) || 'div';
  const el = document.createElement(tag);
  for (const part of (m && m[2] ? m[2].match(/[.#][\w-]+/g) : []) || []) {
    if (part[0] === '.') el.classList.add(part.slice(1)); else el.id = part.slice(1);
  }
  if (props && (typeof props !== 'object' || props instanceof Node || Array.isArray(props))) { children.unshift(props); props = null; }
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2).toLowerCase(), v);
    else if (k === 'class') el.className += (el.className ? ' ' : '') + v;
    else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else if (k === 'value' || k === 'checked' || k === 'disabled' || k === 'hidden' || k === 'selected') el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  append(el, children);
  return el;
}

export function append(el, children) {
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

/** Replace all children of el. */
export function fill(el, ...children) {
  el.replaceChildren();
  return append(el, children);
}

/** Build an element from trusted markup (our own icon SVGs). */
export function svg(markup) {
  const t = document.createElement('template');
  t.innerHTML = markup.trim();
  return t.content.firstElementChild;
}
export { SVG_NS };

export function debounce(fn, ms) {
  let t;
  const wrapped = (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
  wrapped.cancel = () => clearTimeout(t);
  return wrapped;
}

/** Run fn on every animation frame at most once for bursts of calls. */
export function raf(fn) {
  let queued = false;
  return (...a) => { if (queued) return; queued = true; requestAnimationFrame(() => { queued = false; fn(...a); }); };
}

/** Collect listeners/observers/timers of a view so destroy() can drop them all at once. */
export class Scope {
  constructor() { this.cleanups = []; }
  on(target, type, fn, opts) { target.addEventListener(type, fn, opts); this.cleanups.push(() => target.removeEventListener(type, fn, opts)); return this; }
  add(fn) { this.cleanups.push(fn); return this; }
  interval(fn, ms) { const id = setInterval(fn, ms); this.cleanups.push(() => clearInterval(id)); return id; }
  dispose() { for (const c of this.cleanups.splice(0)) { try { c(); } catch { /* ignore */ } } }
}
