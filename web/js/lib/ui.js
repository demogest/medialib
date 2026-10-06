// UI kit: toasts, modal dialogs, confirm/prompt, popup menus.
import { h, $ } from './dom.js';
import { icon } from './icons.js';
import { t } from './i18n.js';

/** Where floating things go: an open modal dialog lives in the browser's top layer, so anything appended to <body> would hide behind it. */
const topHost = () => [...document.querySelectorAll('dialog[open]')].pop() || document.body;

// ---------------------------------------------------------------- toast
let toastHost;
export function toast(message, { kind = 'info', action, ms } = {}) {
  toastHost ||= h('div.toasts', { role: 'status', 'aria-live': 'polite' });
  if (toastHost.parentNode !== topHost()) topHost().append(toastHost);
  const el = h('div.toast', { class: kind }, kind === 'ok' ? icon('check', 'sm') : kind === 'error' ? icon('alert', 'sm') : null, h('span', message));
  if (action) el.append(h('button.act', { type: 'button', onclick: () => { action.run(); el.remove(); } }, action.label));
  el.append(h('button.icon-btn.small.toast-x', { type: 'button', 'aria-label': t('common.close'), onclick: () => el.remove() }, icon('x', 'sm')));
  toastHost.append(el);
  // It stays while the pointer or the keyboard is on it, so there is time to read it and press its button.
  const wait = ms ?? (kind === 'error' ? 8000 : 3500);
  let timer = setTimeout(() => el.remove(), wait);
  const hold = () => clearTimeout(timer);
  const go = () => { clearTimeout(timer); if (!el.matches(':hover, :focus-within')) timer = setTimeout(() => el.remove(), 2000); };
  el.addEventListener('pointerenter', hold); el.addEventListener('focusin', hold);
  el.addEventListener('pointerleave', go); el.addEventListener('focusout', go);
  return el;
}
export const toastError = (message, e) => toast(e ? t('ui.errorReason', { message, reason: e.message || e }) : message, { kind: 'error' });

// ---------------------------------------------------------------- modal
/** modal({ title, body, actions: [{ label, primary, danger, value, keepOpen, onClick }], size }) -> { el, close(value), closed: Promise }. */
export function modal({ title, body, actions = [], size = '', onClose, dismissible = true }) {
  let resolve;
  const closed = new Promise(r => { resolve = r; });
  const foot = actions.length ? h('div.dlg-foot') : null;
  const dlg = h('dialog', { class: size, 'aria-label': title },
    h('div.dlg-head', h('h2', title), dismissible ? h('button.icon-btn', { type: 'button', 'aria-label': t('common.close'), onclick: () => close(undefined) }, icon('x')) : null),
    h('div.dlg-body', body),
    foot);
  const api = { el: dlg, body: $('.dlg-body', dlg), foot, close, closed, setBusy };
  function close(value) {
    if (!dlg.isConnected) return;
    dlg.close();
    dlg.remove();
    onClose && onClose(value);
    resolve(value);
  }
  function setBusy(busy) {
    for (const b of dlg.querySelectorAll('.dlg-foot button')) b.disabled = busy;
  }
  for (const a of actions) {
    const b = h('button.btn', {
      type: 'button', class: [a.primary && 'primary', a.danger && 'danger solid', a.left && 'left'].filter(Boolean).join(' '),
      onclick: async () => {
        if (a.onClick) {
          setBusy(true);
          try { if ((await a.onClick(api)) === false) return; } finally { setBusy(false); }
        }
        if (!a.keepOpen) close(a.value);
      },
    }, a.label);
    a.el = b;
    foot.append(b);
  }
  dlg.addEventListener('cancel', e => { if (!dismissible) e.preventDefault(); else { e.preventDefault(); close(undefined); } });
  dlg.addEventListener('mousedown', e => { if (dismissible && e.target === dlg) close(undefined); });
  document.body.append(dlg);
  dlg.showModal();
  const first = dlg.querySelector('[autofocus], input:not([type=hidden]), textarea, select');
  (first || dlg.querySelector('.dlg-foot .primary')) ?.focus();
  return api;
}

export function confirmDialog({ title, message, detail, confirm = t('common.ok'), danger = false, cancel = t('common.cancel') }) {
  const m = modal({
    title, size: 'narrow',
    body: [h('p', message), detail ? h('p.muted', detail) : null],
    actions: [{ label: cancel, value: false }, { label: confirm, primary: !danger, danger, value: true }],
  });
  return m.closed.then(v => v === true);
}

/** Ask for one line of text. validate(value) may return an error message. Resolves to the text, or null when cancelled. */
export function promptDialog({ title, label, value = '', placeholder = '', confirm = t('common.ok'), hint, validate, mono = false }) {
  const input = h('input.input', { type: 'text', value, placeholder, autocomplete: 'off', spellcheck: 'false', class: mono ? 'mono' : '' });
  const error = h('p.form-error', { hidden: true });
  const submit = api => {
    const text = input.value.trim();
    const problem = validate && validate(text);
    if (problem) { error.textContent = problem; error.hidden = false; input.focus(); return false; }
    api.close(text);
    return false;
  };
  const m = modal({
    title, size: 'narrow',
    body: h('div', error, h('div.field', label ? h('label', label) : null, input, hint ? h('p.hint', hint) : null)),
    actions: [{ label: t('common.cancel'), value: null }, { label: confirm, primary: true, keepOpen: true, onClick: submit }],
  });
  input.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); submit(m); } });
  const dot = value.lastIndexOf('.');
  input.focus();
  input.setSelectionRange(0, dot > 0 ? dot : value.length);  // select the name, not the extension
  return m.closed.then(v => (v === undefined ? null : v));
}

// ---------------------------------------------------------------- popup menu
let openMenu = null;
export function closeMenu() { if (openMenu) openMenu.close(); }

/** showMenu({ anchor | x,y, items, align }): items are { label, icon, onClick, danger, disabled, check, kb } | { sep: true } | { head } | { node }. */
export function showMenu({ anchor, x, y, items, align = 'left', onClose }) {
  closeMenu();
  const el = h('div.menu', { role: 'menu' });
  const buttons = [];
  for (const it of items) {
    if (!it) continue;
    if (it.sep) { el.append(h('div.menu-sep')); continue; }
    if (it.head) { el.append(h('div.menu-head', it.head)); continue; }
    if (it.node) { el.append(h('div.menu-custom', it.node)); continue; }
    const b = h('button.menu-item', {
      type: 'button', role: 'menuitem', class: [it.danger && 'danger', it.check && 'check'].filter(Boolean).join(' '), disabled: it.disabled,
      onclick: () => { close(); it.onClick && it.onClick(); },
    }, it.icon ? icon(it.icon) : h('span', { style: { width: '16px' } }), h('span', it.label), it.kb ? h('span.kb', it.kb) : null);
    if (it.check === false) b.classList.remove('check');
    buttons.push(b);
    el.append(b);
  }
  topHost().append(el);
  const r = anchor ? anchor.getBoundingClientRect() : { left: x, right: x, top: y, bottom: y, width: 0 };
  const w = el.offsetWidth, hgt = el.offsetHeight, m = 8;
  let left = align === 'right' ? r.right - w : r.left;
  let top = r.bottom + 4;
  if (top + hgt > innerHeight - m) top = Math.max(m, (anchor ? r.top - 4 : innerHeight - m) - hgt);
  left = Math.max(m, Math.min(left, innerWidth - w - m));
  el.style.left = left + 'px';
  el.style.top = top + 'px';
  const onDown = e => { if (!el.contains(e.target)) close(); };
  const onKey = e => {
    if (e.key === 'Escape') { e.preventDefault(); close(); anchor && anchor.focus && anchor.focus(); return; }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const live = buttons.filter(b => !b.disabled), i = live.indexOf(document.activeElement);
    live[(i + (e.key === 'ArrowDown' ? 1 : -1) + live.length) % live.length]?.focus();
  };
  setTimeout(() => { document.addEventListener('pointerdown', onDown, true); document.addEventListener('keydown', onKey, true); });
  window.addEventListener('resize', close, { once: true });
  function close() {
    if (openMenu !== handle) return;
    openMenu = null;
    el.remove();
    document.removeEventListener('pointerdown', onDown, true);
    document.removeEventListener('keydown', onKey, true);
    onClose && onClose();
  }
  const handle = { close, el };
  openMenu = handle;
  if (anchor) (buttons.find(b => !b.disabled) || el).focus?.();
  return handle;
}

/** Right-click (and long-press free) context menu helper: contextMenu(event, items). */
export function contextMenu(e, items) {
  e.preventDefault();
  return showMenu({ x: e.clientX, y: e.clientY, items });
}

/** Where the browser may not write to the clipboard (a page over plain http on the network): the text, selected, to copy by hand. */
export function copyByHand(text) {
  const input = h('input.input.mono', { type: 'text', value: text, readOnly: true, spellcheck: 'false' });
  modal({ title: t('ui.copyByHandTitle'), size: 'narrow', body: [h('p', t('ui.copyByHandText')), input], actions: [{ label: t('common.done'), primary: true }] });
  input.focus();
  input.select();
}
