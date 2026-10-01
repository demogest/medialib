// Application shell: navigation rail, theme, routing.
import { h, $ } from './lib/dom.js';
import { icon, logo } from './lib/icons.js';
import { href, startRouter } from './lib/router.js';
import { on, state, startWatcher, loadConnections, loadLibraries, loadPlayers } from './lib/state.js';
import { store } from './lib/store.js';
import { toastError } from './lib/ui.js';

const SECTIONS = [
  { id: 'library', label: 'Library', icon: 'library' },
  { id: 'storage', label: 'Storage', icon: 'storage' },
  { id: 'connections', label: 'Connect', icon: 'plug' },
  { id: 'activity', label: 'Activity', icon: 'activity' },
];

const VIEWS = {
  library: () => import('./views/library.js'),
  storage: () => import('./views/storage.js'),
  connections: () => import('./views/connections.js'),
  activity: () => import('./views/activity.js'),
  settings: () => import('./views/settings.js'),
};

function buildRail() {
  const rail = $('#rail');
  const link = s => h('a.rail-item', { href: href(s.id), dataset: { view: s.id }, 'aria-label': s.label }, icon(s.icon), h('span', s.label), s.id === 'activity' ? h('span.count', { id: 'activity-count', hidden: true }) : null);
  const themeBtn = h('button.rail-item.rail-extra', { type: 'button', id: 'theme', 'aria-label': 'Toggle dark mode', onclick: toggleTheme });
  rail.append(h('div.logo', logo()), ...SECTIONS.map(link), h('div.grow'),
    themeBtn, h('a.rail-item', { href: href('settings'), dataset: { view: 'settings' }, 'aria-label': 'Settings' }, icon('settings'), h('span', 'Settings')));
  paintTheme();
}

const isDark = () => document.documentElement.dataset.theme === 'dark'
  || (!document.documentElement.dataset.theme && matchMedia('(prefers-color-scheme: dark)').matches);
function paintTheme() {
  const b = $('#theme');
  b.replaceChildren(icon(isDark() ? 'sun' : 'moon'), h('span', isDark() ? 'Light' : 'Dark'));
}
function toggleTheme() {
  const next = isDark() ? 'light' : 'dark';
  document.documentElement.dataset.theme = next;
  store.set('theme', next);
  paintTheme();
}

function paintActive(view) {
  for (const a of document.querySelectorAll('.rail-item[data-view]')) {
    if (a.dataset.view === view) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
  }
}

function paintBadge() {
  const n = state.tasks.filter(t => t.state === 'running').length + Object.values(state.jobs).filter(j => ['waiting', 'listing', 'indexing'].includes(j.state)).length;
  const badge = $('#activity-count');
  badge.hidden = !n;
  badge.textContent = n;
}

async function boot() {
  buildRail();
  on('activity', paintBadge);
  try {
    await Promise.all([loadLibraries(), loadConnections(), loadPlayers()]);
  } catch (e) {
    toastError('Could not reach the server', e);
  }
  startWatcher();
  await startRouter({
    root: $('#view'), views: VIEWS, fallback: state.libs.length || !state.connections.length ? 'library' : 'storage',
    onChange: paintActive,
  });
}

boot();
