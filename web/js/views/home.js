// Home: where the app opens. What you played lately and what is new, one click from playing, then your libraries.
// Before any library exists it is the first-run guide instead: the folders on this computer that hold videos (one
// click each), what covers need (ffmpeg), and cloud storage for those who keep media in a bucket.
import { h, fill } from '../lib/dom.js';
import { icon, logo } from '../lib/icons.js';
import { del, get, post } from '../lib/api.js';
import { ago, clock, num, plural, resLabel, stem } from '../lib/fmt.js';
import { href, navigate } from '../lib/router.js';
import { loadLibraries, loadSystem, on, pokeWatcher, running, state } from '../lib/state.js';
import { store } from '../lib/store.js';
import { contextMenu, toast, toastError } from '../lib/ui.js';
import { openLibraryAction } from '../shell/sidebar.js';
import { editConnection } from './connections.js';
import { playHere } from '../lib/handoff.js';

const thumb = it => `/thumbs/${it.lib}/${it.id}-${it.ver}-${it.cover ?? 0}.avif`;
const greeting = () => { const hr = new Date().getHours(); return hr < 5 ? 'Good evening' : hr < 12 ? 'Good morning' : hr < 18 ? 'Good afternoon' : 'Good evening'; };

export async function mount(root) {
  const inner = h('div.page-inner.home');
  root.append(h('div.page', inner));
  const offs = [];
  // The guide stays until it is dismissed, even once a first folder is added: there may be more to add.
  let guide = !state.libs.length;

  async function play(it) {
    try {
      const j = await post('/api/play', { lib: it.lib, ids: [it.id], player: store.get('player', state.defaultPlayer) });
      toast(`Opening in ${j.player}`, { kind: 'ok' });
      refreshFeed();
    } catch (e) {
      if (e.status === 403) { playHere(it.lib, it).catch(x => toastError('Could not play', x)); return; }
      toastError('Could not start the player', e);
    }
  }

  // ---------------------------------------------------------------- shelves
  // What arrived since the last visit is marked New. The mark stays for this visit; the next one starts from now.
  const seen = store.get('homeSeen', '');
  store.set('homeSeen', new Date().toISOString().replace(/\.\d+Z$/, 'Z'));
  const isNew = it => seen && (it.added || it.mtime) > seen;

  function card(it, sub) {
    const cover = h('div.shelf-cover', it.frames ? h('img', { src: thumb(it), alt: '', loading: 'lazy', decoding: 'async' })
      : h('div.ph', icon(it.kind === 'audio' ? 'music' : 'film', 'lg')));
    if (it.duration) cover.append(h('span.badge.br', clock(it.duration)));
    const res = resLabel(it.width, it.height);
    if (res) cover.append(h('span.badge.tl', res));
    if (sub === addedSub && isNew(it)) cover.append(h('span.badge.tr.new', 'New'));
    cover.append(h('span.play-fab', icon('play')));
    return h('button.shelf-card', { type: 'button', title: `${it.name}\n${it.lib_name}${it.dir ? ' › ' + it.dir : ''}`, onclick: () => play(it),
      oncontextmenu: e => contextMenu(e, [
        { label: 'Play', icon: 'play', onClick: () => play(it) },
        { label: 'Show in its folder', icon: 'folder', onClick: () => openLibraryAction('reveal', { lib: it.lib, dir: it.dir, id: it.id }) },
      ]) },
    cover, h('span.shelf-title', stem(it.name)), h('span.shelf-sub', sub(it)));
  }
  function shelf(title, items, sub, extra) {
    if (!items.length) return null;
    const track = h('div.shelf-track', items.map(it => card(it, sub)));
    const step = dir => track.scrollBy({ left: dir * track.clientWidth * 0.85, behavior: 'smooth' });
    return h('section.shelf',
      h('header.shelf-head', h('h2', title), extra,
        h('div.shelf-nav', h('button.icon-btn', { type: 'button', 'aria-label': 'Back', onclick: () => step(-1) }, icon('chevron-left', 'sm')),
          h('button.icon-btn', { type: 'button', 'aria-label': 'More', onclick: () => step(1) }, icon('chevron-right', 'sm')))),
      track);
  }

  // ---------------------------------------------------------------- libraries
  function tile(l) {
    const covers = l.covers || [];
    const art = h('div.tile-mosaic', { class: `n${Math.min(covers.length, 4)}` },
      covers.length ? covers.map(c => h('img', { src: `/thumbs/${l.id}/${c.id}-${c.ver}-${c.i}.avif`, alt: '', loading: 'lazy' }))
        : h('div.ph', icon(l.type === 'local' ? 'folder' : 'cloud', 'lg')));
    const j = state.jobs[l.id];
    const status = running(j) ? h('span.tile-status.busy', h('span.spin'), j.total ? `Scanning · ${Math.round(100 * j.done / j.total)}%` : 'Scanning…')
      : l.reachable === false ? h('span.tile-status.warn', icon('alert', 'sm'), l.type === 'local' ? 'Folder not found' : 'Connection missing')
        : !l.updated ? h('span.tile-status', 'Not scanned yet') : h('span.tile-status', `${plural(l.items, 'video')} · ${ago(l.updated)}`);
    return h('a.lib-tile', { href: href('library', l.id) }, art,
      h('div.tile-meta', h('span.tile-name', icon(l.type === 'local' ? 'folder' : 'cloud', 'sm'), l.name), status));
  }

  // ---------------------------------------------------------------- the page
  let feed = { played: [], added: [] };
  async function refreshFeed() {
    try { feed = await get('/api/home'); } catch { /* keep what is shown */ }
    if (!guide) render();
  }
  function render() {
    if (guide) return onboarding();
    const total = state.libs.reduce((a, l) => a + (l.items || 0), 0);
    const scans = state.libs.filter(l => running(state.jobs[l.id]));
    const clear = feed.played.length ? h('button.btn.ghost.small', { type: 'button', title: 'Forget what was played', onclick: async () => {
      try { await del('/api/history'); refreshFeed(); } catch (e) { toastError('Could not clear', e); }
    } }, 'Clear') : null;
    fill(inner,
      h('header.home-head',
        h('div', h('h1', greeting()), h('p.muted', state.libs.length ? `${plural(state.libs.length, 'library', 'libraries')} · ${plural(total, 'video')}` : '')),
        h('div.actions', h('button.btn', { type: 'button', onclick: () => openLibraryAction('add') }, icon('plus', 'sm'), 'Add a library'))),
      scans.length ? h('a.scan-banner', { href: href('library', scans[0].id) }, h('span.spin'),
        h('span.grow', `Making covers for ${scans[0].name}`, h('span.muted', ' · new videos appear as they are done')),
        progress(state.jobs[scans[0].id])) : null,
      shelf('Recently played', feed.played, it => `${it.lib_name} · ${ago(it.played)}`, clear),
      shelf('Recently added', feed.added, addedSub),
      h('section.shelf', h('header.shelf-head', h('h2', 'Libraries')),
        h('div.lib-tiles', state.libs.map(tile),
          h('button.lib-tile.add', { type: 'button', onclick: () => openLibraryAction('add') }, h('div.tile-mosaic', h('div.ph', icon('plus', 'lg'))),
            h('div.tile-meta', h('span.tile-name', 'Add a library'), h('span.tile-status', 'A folder, a NAS share or a bucket'))))),
      !feed.added.length && !scans.length && state.libs.every(l => !l.items) ? h('p.home-hint.muted', icon('info', 'sm'),
        'Open a library and choose ', h('strong', 'Scan for videos'), ' to make its covers.') : null);
  }
  function addedSub(it) { return `${it.lib_name} · ${ago(it.added || it.mtime)}`; }
  const progress = j => j && j.total ? h('progress', { max: j.total, value: j.done }) : null;

  // ---------------------------------------------------------------- first run
  let found = null, foundError = '';
  const added = new Set();
  async function onboarding() {
    const sys = state.system;
    const suggestions = h('div.ob-list');
    const paintFound = () => {
      if (found === null) { fill(suggestions, h('div.ob-row.muted', h('span.spin'), 'Looking for videos on this computer…')); return; }
      if (foundError) { fill(suggestions, h('div.ob-row.muted', foundError)); return; }
      if (!found.length) { fill(suggestions, h('div.ob-row.muted', icon('info', 'sm'), 'No videos in the usual places (Videos, Downloads, other disks). Choose the folder yourself.')); return; }
      fill(suggestions, found.map(f => {
        const done = added.has(f.path);
        return h('div.ob-row', h('span.ob-icon', icon('folder')),
          h('div.grow', h('div.ob-name', f.name), h('div.ob-path', f.path)),
          h('span.tag', f.more ? `${num(f.videos)}+ videos` : plural(f.videos, 'video')),
          done ? h('span.tag.ok', icon('check', 'sm'), 'Added') : h('button.btn.small.primary', { type: 'button', onclick: e => addFolder(f, e.currentTarget) }, 'Add'));
      }));
    };
    const ffmpegCard = sys && !sys.ffmpeg ? h('section.ob-card.warn',
      h('h2', icon('alert'), 'Covers need ffmpeg'),
      h('p', 'medialib makes its covers with ffmpeg, a free program. Install it, then check again:'),
      h('div.ob-cmd', h('code', installCommand(sys.platform)), h('button.btn.small', { type: 'button', onclick: () => navigator.clipboard.writeText(installCommand(sys.platform)).then(() => toast('Copied', { kind: 'ok' })) }, icon('copy', 'sm'), 'Copy')),
      h('div.ob-actions', h('button.btn', { type: 'button', onclick: async () => { await loadSystem(); render(); } }, icon('refresh', 'sm'), 'Check again'),
        h('a.btn.ghost', { href: href('settings') }, 'Choose where ffmpeg is…'))) : null;
    fill(inner, h('div.ob',
      h('div.ob-hero', h('div.welcome-mark', logo()), h('h1', added.size ? 'Nice. Add more, or start watching.' : 'Let’s set up your library'),
        h('p.lede', 'Media Library turns folders of videos into a wall of real keyframe covers, finds anything as you type, and plays it in the player you already use.')),
      h('section.ob-card',
        h('h2', icon('folder'), 'Where are your videos?'),
        sys && sys.can_edit === false ? h('p.muted', 'Libraries are added on the computer running medialib.') : [suggestions,
          h('div.ob-actions',
            h('button.btn', { type: 'button', onclick: () => openLibraryAction('add') }, icon('folder-plus', 'sm'), 'Choose a folder…'),
            h('button.btn.ghost', { type: 'button', onclick: async () => { const c = await editConnection(); if (c) navigate('storage', c.id); } }, icon('cloud', 'sm'), 'My videos are in cloud storage (S3)…'))]),
      ffmpegCard,
      added.size || state.libs.length ? h('div.ob-done', h('button.btn.primary.big', { type: 'button', onclick: async () => { guide = false; await loadLibraries(); refreshFeed(); } }, 'Start watching', icon('chevron-right', 'sm'))) : null,
      h('p.welcome-tip', h('kbd', 'Ctrl K'), ' searches everything, anywhere.')));
    paintFound();
    if (found === null && sys?.can_edit !== false) {
      try { found = (await get('/api/suggestions')).folders; } catch (e) { found = []; foundError = e.status === 403 ? 'Folders can be added on the computer running medialib.' : ''; }
      paintFound();
    }
  }
  async function addFolder(f, btn) {
    btn.disabled = true;
    try {
      const lib = await post('/api/libraries', { path: f.path, name: f.name });
      await post('/api/index', { id: lib.id });
      added.add(f.path);
      await loadLibraries();
      pokeWatcher();
      toast(`Added ${f.name}: making its covers`, { kind: 'ok' });
      onboarding();
    } catch (e) { btn.disabled = false; toastError('Could not add the folder', e); }
  }

  offs.push(on('libraries', () => { if (!guide) render(); }));
  // While a library scans, its new covers belong on the page: refresh now and then, and once it ends.
  let wasScanning = false, lastPull = 0;
  offs.push(on('activity', () => {
    const scanning = state.libs.some(l => running(state.jobs[l.id]));
    if (guide) { wasScanning = scanning; return; }
    if ((wasScanning && !scanning) || (scanning && Date.now() - lastPull > 8000)) { lastPull = Date.now(); loadLibraries().then(refreshFeed); }
    else render();
    wasScanning = scanning;
  }));
  if (!guide) await refreshFeed(); else await onboarding();
  return { destroy() { offs.forEach(f => f()); } };
}

function installCommand(platform = '') {
  if (platform.startsWith('windows')) return 'winget install Gyan.FFmpeg';
  if (platform.startsWith('darwin')) return 'brew install ffmpeg';
  return 'sudo apt install ffmpeg';
}
