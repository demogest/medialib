// Interface text in the user's language. Every message lives in web/locales/<code>.json under a key, in ICU
// MessageFormat; en.json is the source and the fallback for anything not translated yet. The language is the one
// chosen in Settings, else the first of the browser's (or system's) languages there is a translation for.
// Loading waits at the top level, so every module that imports t() runs with the messages in place.
import { format, parse } from './messageformat.js';
import { LANGUAGES, SOURCE } from './languages.js';
import { store } from './store.js';

export { LANGUAGES };

const codes = LANGUAGES.map(([code]) => code);
const find = code => codes.find(c => c.toLowerCase() === String(code).toLowerCase());

/** The language to show: the saved choice, else the browser's preference, else English. */
function pick() {
  const saved = find(store.get('lang', ''));
  if (saved) return saved;
  const wanted = navigator.languages?.length ? navigator.languages : [navigator.language || SOURCE];
  for (const w of wanted) {
    const exact = find(w);
    if (exact) return exact;
    const base = String(w).split('-')[0].toLowerCase();
    const near = codes.find(c => c.split('-')[0].toLowerCase() === base);
    if (near) return near;
  }
  return SOURCE;
}

async function load(code) {
  try {
    const r = await fetch(new URL(`../../locales/${code}.json`, import.meta.url));
    return r.ok ? await r.json() : {};
  } catch {
    return {};
  }
}

/** The language the interface is showing. */
export const lang = pick();
/** The language chosen in Settings, or '' to follow the browser. */
export const chosen = find(store.get('lang', '')) || '';

const [source, local] = await Promise.all([load(SOURCE), lang === SOURCE ? null : load(lang)]);
const parsed = new Map();
document.documentElement.lang = lang;

function parts(key, values) {
  const msg = (local && local[key]) || source[key];
  if (msg == null) return [key];
  let p = parsed.get(key);
  if (!p) {
    try { p = parse(msg); } catch { p = [msg]; }
    parsed.set(key, p);
  }
  return format(p, values, lang);
}

/** t('library.items', { count: 3 }) -> '3 videos'. */
export function t(key, values) {
  return parts(key, values).join('');
}

/** Like t(), for a message with elements in it: tx('home.press', { keys: h('kbd', 'Ctrl K') }) -> [text, node, text]. */
export function tx(key, values) {
  return parts(key, values);
}

/** Show the interface in another language ('' follows the browser). The page reloads to redraw every screen. */
export function setLanguage(code) {
  store.set('lang', find(code) || '');
  location.reload();
}
