// Command palette (Ctrl+K): jump to any section, library or connection, run the common commands, and find media in every
// library by name or folder, in Chinese by pinyin too, and even with a typo. Enter on a file plays it in your player;
// Ctrl+Enter shows it in its folder.
import { h } from '../lib/dom.js';
import { icon } from '../lib/icons.js';
import { get, post } from '../lib/api.js';
import { navigate } from '../lib/router.js';
import { state } from '../lib/state.js';
import { store } from '../lib/store.js';
import { toast, toastError } from '../lib/ui.js';
import { SECTIONS, isDark, openLibraryAction, toggleSidebar, toggleTheme } from './sidebar.js';
import { clock } from '../lib/fmt.js';

function commands() {
  const out = [];
  SECTIONS.forEach((s, i) => out.push({ group: 'Go to', label: s.label, hint: s.hint, icon: s.icon, key: i < 4 ? `Alt+${i + 1}` : 'Alt+5', run: () => navigate(s.id) }));
  for (const l of state.libs) out.push({ group: 'Libraries', label: l.name, hint: l.type === 'local' ? l.path : `${l.bucket || ''}/${l.prefix || ''}`, icon: l.type === 'local' ? 'folder' : 'cloud', run: () => navigate('library', l.id) });
  for (const c of state.connections) out.push({ group: 'Stores', label: c.name, hint: c.endpoint, icon: 'server', run: () => navigate('storage', c.id) });
  out.push(
    { group: 'Actions', label: 'Add a library…', icon: 'plus', run: () => openLibraryAction('add') },
    { group: 'Actions', label: 'Manage libraries…', hint: 'Rename, re-index, convert or remove', icon: 'sliders', run: () => openLibraryAction('manage') },
    { group: 'Actions', label: 'Add a connection…', icon: 'plug', run: () => navigate('connections') },
    { group: 'Actions', label: isDark() ? 'Switch to light mode' : 'Switch to dark mode', icon: isDark() ? 'sun' : 'moon', run: toggleTheme },
    { group: 'Actions', label: 'Toggle sidebar', icon: 'menu', key: 'Ctrl+B', run: toggleSidebar },
    { group: 'Actions', label: 'Reload', icon: 'refresh', key: 'F5', run: () => location.reload() },
  );
  return out;
}

// Every query word must appear in the label or hint; earlier and label matches rank first.
function rank(cmd, words) {
  const l = cmd.label.toLowerCase(), t = `${l} ${(cmd.hint || '').toLowerCase()}`;
  let score = 0;
  for (const w of words) {
    if (!t.includes(w)) return -1;
    score += l.startsWith(w) ? 3 : l.includes(w) ? 2 : 1;
  }
  return score;
}

async function playMedia(m) {
  try {
    const j = await post('/api/play', { lib: m.lib, ids: [m.id], player: store.get('player', state.defaultPlayer) });
    toast(`Opening in ${j.player}`, { kind: 'ok' });
  } catch (e) {
    if (e.status === 403) { // another computer: the server cannot open a player on that screen
      window.open(`${location.origin}/media/${m.lib}/${m.id}/${encodeURIComponent(m.name)}`, '_blank');
      return;
    }
    toastError('Could not start the player', e);
  }
}

const mediaCommand = m => ({
  group: 'Media', label: m.name, hint: [m.libName, m.dir].filter(Boolean).join(' › '), icon: m.kind === 'audio' ? 'music' : 'film', media: m,
  thumb: m.frames ? `/thumbs/${m.lib}/${m.id}-${m.ver}-${m.cover ?? 0}.avif` : null, time: m.duration ? clock(m.duration) : '',
  run: () => playMedia(m),
  alt: () => openLibraryAction('reveal', { lib: m.lib, dir: m.dir, id: m.id }),
});

let open = null;
export function openPalette() {
  if (open) return open.close();
  const all = commands();
  let shown = [], sel = 0, found = [], total = 0, seq = 0;
  const input = h('input.pal-input', { placeholder: 'Search media, libraries and commands…', 'aria-label': 'Command palette', autocomplete: 'off', spellcheck: 'false' });
  const list = h('div.pal-list', { role: 'listbox' });
  const hintEnter = h('span', ' open');
  const hintAlt = h('span', { hidden: true }, h('kbd', 'Ctrl'), h('kbd', '↵'), ' show in folder');
  const dlg = h('div.pal-scrim', { onmousedown: e => { if (e.target === dlg) close(); } },
    h('div.pal', { role: 'dialog', 'aria-label': 'Command palette' },
      h('div.pal-top', icon('search'), input, h('kbd', 'Esc')), list,
      h('div.pal-foot', h('span', h('kbd', '↑'), h('kbd', '↓'), ' move'), h('span', h('kbd', '↵'), hintEnter), hintAlt, h('span', h('kbd', 'Esc'), ' close'))));

  function render() {
    const words = input.value.toLowerCase().split(/\s+/).filter(Boolean);
    const cmds = words.length ? all.map(c => [c, rank(c, words)]).filter(x => x[1] >= 0).sort((a, b) => b[1] - a[1]).map(x => x[0]) : all;
    shown = [...cmds, ...found.map(mediaCommand)];
    sel = Math.min(sel, Math.max(0, shown.length - 1));
    list.replaceChildren();
    if (!shown.length) { list.append(h('p.pal-empty', words.length ? `Nothing matches “${input.value}”.` : '')); return; }
    let group = '';
    shown.forEach((c, i) => {
      if ((!words.length || c.media) && c.group !== group) { group = c.group; list.append(h('div.pal-group', group, c.media && total > found.length ? h('span.pal-more', `${found.length} of ${total}`) : null)); }
      const lead = c.thumb ? h('span.pal-thumb', h('img', { src: c.thumb, alt: '', loading: 'lazy' })) : icon(c.icon);
      list.append(h('button.pal-item', { class: c.media ? 'media' : '', type: 'button', role: 'option', 'aria-selected': String(i === sel), dataset: { i }, onmousemove: () => { if (sel !== i) { sel = i; mark(); } }, onclick: e => ((e.ctrlKey || e.metaKey) && c.alt ? (close(), c.alt()) : run(c)) },
        lead, h('span.pal-label', c.label), c.hint ? h('span.pal-hint', c.hint) : null, c.time ? h('span.pal-time', c.time) : null, c.alt ? h('span.pal-reveal', { role: 'button', title: 'Show in its folder (Ctrl+Enter)', 'aria-label': 'Show in folder', onclick: e => { e.stopPropagation(); close(); c.alt(); } }, icon('folder', 'sm')) : null, c.key ? h('kbd', c.key) : null));
    });
    mark();
  }
  function mark() {
    for (const el of list.querySelectorAll('.pal-item')) el.setAttribute('aria-selected', String(+el.dataset.i === sel));
    list.querySelector(`.pal-item[data-i="${sel}"]`)?.scrollIntoView({ block: 'nearest' });
    const c = shown[sel];
    hintEnter.textContent = c && c.media ? ' play' : ' open';
    hintAlt.hidden = !(c && c.alt);
  }
  function run(c) { close(); c.run(); }
  function close() { clearTimeout(timer); seq++; dlg.remove(); open = null; prev?.focus?.(); }
  const prev = document.activeElement;

  // Media come from the server (/api/search): every library at once, in order of relevance.
  let timer = null;
  function lookup() {
    const q = input.value.trim(), mine = ++seq;
    if (!q) { found = []; total = 0; render(); return; }
    get('/api/search?limit=12&q=' + encodeURIComponent(q)).then(j => {
      if (mine !== seq) return;
      found = j.results || [];
      total = j.total || 0;
      render();
    }).catch(() => { if (mine === seq) { found = []; render(); } });
  }
  input.addEventListener('input', () => { sel = 0; clearTimeout(timer); timer = setTimeout(lookup, 110); render(); });
  input.addEventListener('keydown', e => {
    if (e.key === 'ArrowDown') { e.preventDefault(); sel = Math.min(shown.length - 1, sel + 1); mark(); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); sel = Math.max(0, sel - 1); mark(); }
    else if (e.key === 'Enter') { e.preventDefault(); if (e.repeat) return; // a held key must not start the player again and again
      const c = shown[sel]; if (c) { if ((e.ctrlKey || e.metaKey) && c.alt) { close(); c.alt(); } else run(c); } }
    else if (e.key === 'Escape') { e.preventDefault(); close(); }
  });
  render();
  document.body.append(dlg);
  input.focus();
  open = { close };
  return open;
}
