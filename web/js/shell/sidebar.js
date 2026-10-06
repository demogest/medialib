// The navigation sidebar. What people come for is at the top: Home, then each library. Cloud storage shows up once
// there is a store to browse. Background work is a small status chip that appears while something runs, not a
// section of its own. On a phone it is a bottom tab bar: Home, Library, Search, (Storage), Settings.
import { h, $$ } from '../lib/dom.js';
import { icon, logo } from '../lib/icons.js';
import { href, navigate, parseHash } from '../lib/router.js';
import { on, requestLibraryAction, running, state } from '../lib/state.js';
import { store } from '../lib/store.js';

// Every place there is, for the palette and the Alt+number shortcuts.
export const SECTIONS = [
  { id: 'home', label: 'Home', icon: 'home', hint: 'Recently played, recently added and your libraries' },
  { id: 'library', label: 'Library', icon: 'library', hint: 'Browse your videos' },
  { id: 'storage', label: 'Storage', icon: 'storage', hint: 'Browse and manage buckets' },
  { id: 'activity', label: 'Activity', icon: 'activity', hint: 'Scans, copies and moves' },
  { id: 'settings', label: 'Settings', icon: 'settings', hint: 'Players, indexing, updates and appearance' },
  { id: 'connections', label: 'Connections', icon: 'plug', hint: 'S3 stores and their credentials' },
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
  const tab = (id, label, ic, extra = {}) => h('a.nav-item', { href: href(id), dataset: { view: id }, ...extra }, icon(ic), h('span.nav-label', label));

  const libs = h('div.side-list', { id: 'side-libs' });
  const conns = h('div.side-list', { id: 'side-conns' });
  const cloud = h('div', { hidden: true },
    h('div.side-group', h('span.nav-label', 'Cloud storage'),
      h('a.mini', { href: href('connections'), 'aria-label': 'Connections', title: 'Add or edit connections' }, icon('plus', 'sm'))),
    conns);
  const themeBtn = h('button.icon-btn.theme-btn', { type: 'button', onclick: toggleTheme });
  const updateBtn = h('a.nav-item.update-pill', { href: href('settings', 'about'), hidden: true });
  const busy = h('a.busy-chip', { href: href('activity'), hidden: true, title: 'Activity' });
  const storageTab = tab('storage', 'Storage', 'storage', { class: 'mobile-only', hidden: true });

  root.append(
    h('div.app-head',
      h('a.brand', { href: href('home'), title: 'Media Library' }, h('span.brand-mark', logo()), h('span.brand-name', 'Media Library')),
      themeBtn,
      h('button.icon-btn.collapse', { type: 'button', 'aria-label': 'Collapse sidebar', title: 'Collapse sidebar (Ctrl+B)', onclick: toggleSidebar }, icon('chevron-left', 'sm'))),
    h('button.side-search', { type: 'button', onclick: onSearch, title: 'Search everything (Ctrl+K)' },
      icon('search', 'sm'), h('span.nav-label', 'Search'), h('kbd.kbd-hint', 'Ctrl K')),
    h('nav.side-nav', { 'aria-label': 'Sections' },
      tab('home', 'Home', 'home', { title: 'Home  (Alt+1)' }),
      tab('library', 'Library', 'library', { class: 'mobile-only' }),
      h('button.nav-item.mobile-only', { type: 'button', onclick: onSearch }, icon('search'), h('span.nav-label', 'Search')),
      storageTab),
    h('div.side-scroll',
      h('div.side-group', h('span.nav-label', 'Libraries'), h('span.mini-group',
        h('button.mini', { type: 'button', 'aria-label': 'Manage libraries', title: 'Rename, rescan or remove libraries', onclick: () => openLibraryAction('manage') }, icon('sliders', 'sm')),
        h('button.mini', { type: 'button', 'aria-label': 'Add a library', title: 'Add a library', onclick: () => openLibraryAction('add') }, icon('plus', 'sm')))),
      libs,
      cloud),
    h('div.app-foot', busy, updateBtn, tab('settings', 'Settings', 'settings', { title: 'Settings  (Alt+5)' })),
  );
  const expand = h('button.icon-btn.expand', { type: 'button', 'aria-label': 'Expand sidebar', title: 'Expand sidebar (Ctrl+B)', onclick: toggleSidebar }, icon('chevron-right', 'sm'));
  root.append(expand);

  const paintTheme = () => {
    themeBtn.replaceChildren(icon(isDark() ? 'sun' : 'moon', 'sm'));
    themeBtn.title = isDark() ? 'Light mode' : 'Dark mode';
    themeBtn.setAttribute('aria-label', themeBtn.title);
  };
  paintTheme();
  document.addEventListener('themechange', paintTheme);
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', paintTheme);

  const paintLists = () => {
    libs.replaceChildren(...(state.libs.length ? state.libs.map(l => {
      const j = state.jobs[l.id], scanning = running(j);
      const pct = scanning && j.total ? Math.round(100 * j.done / j.total) : null;
      return h('a.side-link', { href: href('library', l.id), dataset: { lib: l.id }, title: l.name },
        icon(l.type === 'local' ? 'folder' : 'cloud', 'sm'), h('span.nav-label.grow', l.name),
        scanning ? h('span.spin', { title: pct == null ? 'Scanning…' : `Scanning ${pct}%` })
          : l.items ? h('span.side-count.nav-label', l.items > 999 ? Math.round(l.items / 100) / 10 + 'k' : String(l.items)) : null);
    }) : [h('button.side-empty.nav-label', { type: 'button', onclick: () => openLibraryAction('add') }, icon('plus', 'sm'), 'Add your first library')]));
    conns.replaceChildren(...state.connections.map(c =>
      h('a.side-link', { href: href('storage', c.id), dataset: { conn: c.id }, title: `${c.name}  ${c.endpoint || ''}` },
        icon('server', 'sm'), h('span.nav-label.grow', c.name))));
    cloud.hidden = !state.connections.length;
    storageTab.hidden = !state.connections.length;
    paintActive();
  };
  // Work in the background: a chip that says what runs (one scan by name), and leads to Activity.
  const paintBusy = () => {
    const jobs = state.libs.filter(l => running(state.jobs[l.id]));
    const tasks = state.tasks.filter(t => t.state === 'running');
    const n = jobs.length + tasks.length;
    busy.hidden = !n;
    if (n) {
      const j = jobs.length ? state.jobs[jobs[0].id] : null;
      const pct = j && j.total ? Math.round(100 * j.done / j.total) : null;
      const label = n > 1 ? `${n} things running` : j ? `Scanning ${jobs[0].name}${pct == null ? '…' : ` · ${pct}%`}` : tasks[0].title;
      busy.title = label;
      busy.replaceChildren(h('span.spin'), h('span.nav-label.grow', label));
    }
    paintLists();
  };
  const paintUpdate = () => {
    const u = state.update, show = !!u && (u.available || u.ready);
    updateBtn.hidden = !show;
    if (!show) return;
    const label = u.ready ? 'Restart to update' : u.state === 'downloading' ? `Downloading ${u.latest}…` : `Update to ${u.latest}`;
    updateBtn.title = `${label}: medialib ${u.current} → ${u.latest}`;
    updateBtn.replaceChildren(icon(u.ready ? 'refresh' : 'download'), h('span.nav-label', label));
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
  on('libraries', paintLists); on('connections', paintLists); on('activity', paintBusy); on('update', paintUpdate);
  paintLists();
  return { paintActive };
}

