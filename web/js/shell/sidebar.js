// The navigation sidebar: sections, libraries, stores, activity badge and theme. Collapses to an icon rail (Ctrl+B).
import { h, $, $$ } from '../lib/dom.js';
import { icon, logo } from '../lib/icons.js';
import { href, navigate, parseHash } from '../lib/router.js';
import { on, requestLibraryAction, running, state } from '../lib/state.js';
import { store } from '../lib/store.js';

export const SECTIONS = [
  { id: 'library', label: 'Library', icon: 'library', hint: 'Browse your indexed media' },
  { id: 'storage', label: 'Storage', icon: 'storage', hint: 'Browse and manage buckets' },
  { id: 'connections', label: 'Connections', icon: 'plug', hint: 'S3 stores and credentials' },
  { id: 'activity', label: 'Activity', icon: 'activity', hint: 'Indexing, copies and moves' },
  { id: 'settings', label: 'Settings', icon: 'settings', hint: 'Players, tools and appearance' },
];

export const isDark = () => document.documentElement.dataset.theme === 'dark'
  || (!document.documentElement.dataset.theme && matchMedia('(prefers-color-scheme: dark)').matches);

export function setTheme(t) {
  if (t === 'system') { delete document.documentElement.dataset.theme; store.set('theme', 'system'); }
  else { document.documentElement.dataset.theme = t; store.set('theme', t); }
  document.dispatchEvent(new Event('themechange'));
}
export const toggleTheme = () => setTheme(isDark() ? 'light' : 'dark');

export function setCollapsed(on_) {
  document.documentElement.dataset.side = on_ ? 'collapsed' : 'open';
  store.set('sideCollapsed', on_ ? '1' : '0');
}
export const toggleSidebar = () => setCollapsed(document.documentElement.dataset.side !== 'collapsed');

/** Open one of the library dialogs from any page: go to the library view if needed, then ask it. */
export function openLibraryAction(name, arg) {
  // A file is shown in its own folder: go there from here, so the library view is mounted once, at the right place.
  if (name === 'reveal') navigate('library', arg.lib, ...(arg.dir ? arg.dir.split('/') : []));
  else if (parseHash().view !== 'library') navigate('library');
  requestLibraryAction(name, arg);
}

export function buildSidebar(root, { onSearch }) {
  setCollapsed(store.get('sideCollapsed', '0') === '1');
  const item = (s, n) => h('a.nav-item', { href: href(s.id), dataset: { view: s.id }, title: `${s.label}  (Alt+${n})` },
    icon(s.icon), h('span.nav-label', s.label),
    s.id === 'activity' ? h('span.count', { id: 'activity-count', hidden: true }) : null);

  const libs = h('div.side-list', { id: 'side-libs' });
  const conns = h('div.side-list', { id: 'side-conns' });
  const themeBtn = h('button.nav-item', { type: 'button', id: 'theme', onclick: toggleTheme });

  root.append(
    h('div.app-head',
      h('a.brand', { href: href('library'), title: 'Media Library' }, h('span.brand-mark', logo()), h('span.brand-name', 'Media Library')),
      h('button.icon-btn.collapse', { type: 'button', 'aria-label': 'Collapse sidebar', title: 'Collapse sidebar (Ctrl+B)', onclick: toggleSidebar }, icon('chevron-left', 'sm'))),
    h('button.side-search', { type: 'button', onclick: onSearch, title: 'Search or jump to… (Ctrl+K)' },
      icon('search', 'sm'), h('span.nav-label', 'Search or jump to…'), h('kbd.kbd-hint', 'Ctrl K')),
    h('nav.side-nav', { 'aria-label': 'Sections' }, SECTIONS.slice(0, 4).map((s, i) => item(s, i + 1))),
    h('div.side-scroll',
      h('div.side-group', h('span.nav-label', 'Libraries'), h('span.mini-group',
        h('button.mini', { type: 'button', 'aria-label': 'Manage libraries', title: 'Manage libraries: rename, re-index, remove', onclick: () => openLibraryAction('manage') }, icon('sliders', 'sm')),
        h('button.mini', { type: 'button', 'aria-label': 'Add a library', title: 'Add a library', onclick: () => openLibraryAction('add') }, icon('plus', 'sm')))),
      libs,
      h('div.side-group', h('span.nav-label', 'Stores'), h('button.mini', { type: 'button', 'aria-label': 'Add a connection', title: 'Add a connection', onclick: () => navigate('connections') }, icon('plus', 'sm'))),
      conns),
    h('div.app-foot', themeBtn, item(SECTIONS[4], 5)),
  );
  const expand = h('button.icon-btn.expand', { type: 'button', 'aria-label': 'Expand sidebar', title: 'Expand sidebar (Ctrl+B)', onclick: toggleSidebar }, icon('chevron-right', 'sm'));
  root.append(expand);

  const paintTheme = () => themeBtn.replaceChildren(icon(isDark() ? 'sun' : 'moon'), h('span.nav-label', isDark() ? 'Light mode' : 'Dark mode'));
  paintTheme();
  document.addEventListener('themechange', paintTheme);
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', paintTheme);

  const paintLists = () => {
    libs.replaceChildren(...(state.libs.length ? state.libs.map(l => {
      const j = state.jobs[l.id], busy = running(j);
      const pct = busy && j.total ? Math.round(100 * j.done / j.total) : null;
      return h('a.side-link', { href: href('library', l.id), dataset: { lib: l.id }, title: l.name },
        icon(l.type === 'local' ? 'folder' : 'cloud', 'sm'), h('span.nav-label.grow', l.name),
        busy ? h('span.spin', { title: pct == null ? 'Indexing…' : `Indexing ${pct}%` }) : null);
    }) : [h('span.side-empty.nav-label', 'No libraries yet')]));
    conns.replaceChildren(...(state.connections.length ? state.connections.map(c =>
      h('a.side-link', { href: href('storage', c.id), dataset: { conn: c.id }, title: `${c.name}  ${c.endpoint || ''}` },
        icon('server', 'sm'), h('span.nav-label.grow', c.name))) : [h('span.side-empty.nav-label', 'No connections yet')]));
    paintActive();
  };
  const paintBadge = () => {
    const n = state.tasks.filter(t => t.state === 'running').length + Object.values(state.jobs).filter(running).length;
    const b = $('#activity-count');
    b.hidden = !n; b.textContent = n;
    paintLists();
  };
  let view = '', parts = [];
  function paintActive(v = view, p = parts) {
    view = v; parts = p;
    for (const a of $$('.nav-item[data-view]', root)) {
      if (a.dataset.view === v) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    }
    for (const a of $$('.side-link', root)) {
      const on_ = (a.dataset.lib && v === 'library' && (p[0] || state.activeLib) === a.dataset.lib) || (a.dataset.conn && v === 'storage' && p[0] === a.dataset.conn);
      if (on_) a.setAttribute('aria-current', 'true'); else a.removeAttribute('aria-current');
    }
  }
  on('libraries', paintLists); on('connections', paintLists); on('activity', paintBadge);
  paintLists();
  return { paintActive };
}
