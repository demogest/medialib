// Home: where the app opens. What you played lately and what is new, one click from playing, then your libraries.
// Before any library exists it is the first-run guide instead: the folders on this computer that hold videos (one
// click each), what covers need (ffmpeg), and cloud storage for those who keep media in a bucket.
import { h, fill } from '../lib/dom.js';
import { icon, logo } from '../lib/icons.js';
import { del, get, post } from '../lib/api.js';
import { ago, clock, keys, resLabel, stem } from '../lib/fmt.js';
import { href, navigate } from '../lib/router.js';
import { loadLibraries, loadSystem, on, pokeWatcher, running, state } from '../lib/state.js';
import { store } from '../lib/store.js';
import { confirmDialog, contextMenu, toast, toastError } from '../lib/ui.js';
import { openLibraryAction } from '../shell/sidebar.js';
import { editConnection } from './connections.js';
import { playHere } from '../lib/handoff.js';
import { t, tx } from '../lib/i18n.js';

const thumb = it => `/thumbs/${it.lib}/${it.id}-${it.ver}-${it.cover ?? 0}.avif`;
const greeting = () => { const hr = new Date().getHours(); return hr < 5 ? t('home.goodEvening') : hr < 12 ? t('home.goodMorning') : hr < 18 ? t('home.goodAfternoon') : t('home.goodEvening'); };

export async function mount(root) {
  const inner = h('div.page-inner.home');
  root.append(h('div.page', inner));
  const offs = [];
  // The guide stays until it is dismissed, even once a first folder is added: there may be more to add.
  let guide = !state.libs.length;

  async function play(it) {
    try {
      const j = await post('/api/play', { lib: it.lib, ids: [it.id], player: store.get('player', state.defaultPlayer) });
      toast(t('palette.openingIn', { player: j.player }), { kind: 'ok' });
      refreshFeed();
    } catch (e) {
      if (e.status === 403) { playHere(it.lib, it).catch(x => toastError(t('home.playFailed'), x)); return; }
      toastError(t('palette.playerFailed'), e);
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
    if (sub === addedSub && isNew(it)) cover.append(h('span.badge.tr.new', t('home.new')));
    cover.append(h('span.play-fab', icon('play')));
    return h('button.shelf-card', { type: 'button', title: `${it.name}\n${it.lib_name}${it.dir ? ' › ' + it.dir : ''}`, onclick: () => play(it),
      oncontextmenu: e => contextMenu(e, [
        { label: t('common.play'), icon: 'play', onClick: () => play(it) },
        { label: t('home.showInFolder'), icon: 'folder', onClick: () => openLibraryAction('reveal', { lib: it.lib, dir: it.dir, id: it.id }) },
      ]) },
    cover, h('span.shelf-title', stem(it.name)), h('span.shelf-sub', sub(it)));
  }
  function shelf(title, items, sub, extra) {
    if (!items.length) return null;
    const track = h('div.shelf-track', items.map(it => card(it, sub)));
    const step = dir => track.scrollBy({ left: dir * track.clientWidth * 0.85, behavior: 'smooth' });
    return h('section.shelf',
      h('header.shelf-head', h('h2', title), extra,
        h('div.shelf-nav', h('button.icon-btn', { type: 'button', 'aria-label': t('common.back'), onclick: () => step(-1) }, icon('chevron-left', 'sm')),
          h('button.icon-btn', { type: 'button', 'aria-label': t('common.more'), onclick: () => step(1) }, icon('chevron-right', 'sm')))),
      track);
  }

  // ---------------------------------------------------------------- libraries
  function tile(l) {
    const covers = l.covers || [];
    const art = h('div.tile-mosaic', { class: `n${Math.min(covers.length, 4)}` },
      covers.length ? covers.map(c => h('img', { src: `/thumbs/${l.id}/${c.id}-${c.ver}-${c.i}.avif`, alt: '', loading: 'lazy' }))
        : h('div.ph', icon(l.type === 'local' ? 'folder' : 'cloud', 'lg')));
    const j = state.jobs[l.id];
    const status = running(j) ? h('span.tile-status.busy', h('span.spin'), j.total ? t('home.scanningPct', { pct: Math.round(100 * j.done / j.total) }) : t('nav.scanning'))
      : l.reachable === false ? h('span.tile-status.warn', icon('alert', 'sm'), l.type === 'local' ? t('home.folderNotFound') : t('home.connectionMissing'))
        : !l.updated ? h('span.tile-status', t('home.notScanned')) : h('span.tile-status', t('home.videosAgo', { count: l.items, when: ago(l.updated) }));
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
    const clear = feed.played.length ? h('button.btn.ghost.small', { type: 'button', title: t('home.forgetPlayed'), onclick: async () => {
      if (!await confirmDialog({ title: t('home.clearTitle'), message: t('home.clearMessage'), confirm: t('home.clear'), danger: true })) return;
      try { await del('/api/history'); refreshFeed(); } catch (e) { toastError(t('home.clearFailed'), e); }
    } }, t('home.clear')) : null;
    fill(inner,
      h('header.home-head',
        h('div', h('h1', greeting()), h('p.muted', state.libs.length ? t('home.summary', { libs: state.libs.length, videos: total }) : '')),
        h('div.actions', h('button.btn', { type: 'button', onclick: () => openLibraryAction('add') }, icon('plus', 'sm'), t('nav.addLibrary')))),
      scans.length ? h('a.scan-banner', { href: href('library', scans[0].id) }, h('span.spin'),
        h('span.grow', t('home.makingCovers', { name: scans[0].name }), h('span.muted', ' · ' + t('home.makingCoversNote'))),
        progress(state.jobs[scans[0].id])) : null,
      shelf(t('home.recentlyPlayed'), feed.played, it => `${it.lib_name} · ${ago(it.played)}`, clear),
      shelf(t('home.recentlyAdded'), feed.added, addedSub),
      h('section.shelf', h('header.shelf-head', h('h2', t('nav.libraries'))),
        h('div.lib-tiles', state.libs.map(tile),
          h('button.lib-tile.add', { type: 'button', onclick: () => openLibraryAction('add') }, h('div.tile-mosaic', h('div.ph', icon('plus', 'lg'))),
            h('div.tile-meta', h('span.tile-name', t('nav.addLibrary')), h('span.tile-status', t('home.addLibraryHint')))))),
      !feed.added.length && !scans.length && state.libs.every(l => !l.items) ? h('p.home-hint.muted', icon('info', 'sm'),
        tx('home.scanHint', { action: h('strong', t('home.scanForVideos')) })) : null);
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
      if (found === null) { fill(suggestions, h('div.ob-row.muted', h('span.spin'), t('home.looking'))); return; }
      if (foundError) { fill(suggestions, h('div.ob-row.muted', foundError)); return; }
      if (!found.length) { fill(suggestions, h('div.ob-row.muted', icon('info', 'sm'), t('home.noneFound'))); return; }
      fill(suggestions, found.map(f => {
        const done = added.has(f.path);
        return h('div.ob-row', h('span.ob-icon', icon('folder')),
          h('div.grow', h('div.ob-name', f.name), h('div.ob-path', f.path)),
          h('span.tag', f.more ? t('home.videosOrMore', { count: f.videos }) : t('home.videos', { count: f.videos })),
          done ? h('span.tag.ok', icon('check', 'sm'), t('home.added')) : h('button.btn.small.primary', { type: 'button', onclick: e => addFolder(f, e.currentTarget) }, t('common.add')));
      }));
    };
    const ffmpegCard = sys && !sys.ffmpeg ? h('section.ob-card.warn',
      h('h2', icon('alert'), t('home.needFfmpeg')),
      h('p', t('home.needFfmpegText')),
      h('div.ob-cmd', h('code', installCommand(sys.platform)), h('button.btn.small', { type: 'button', onclick: () => navigator.clipboard.writeText(installCommand(sys.platform)).then(() => toast(t('home.copied'), { kind: 'ok' })) }, icon('copy', 'sm'), t('common.copy'))),
      h('div.ob-actions', h('button.btn', { type: 'button', onclick: async () => { await loadSystem(); render(); } }, icon('refresh', 'sm'), t('home.checkAgain')),
        h('a.btn.ghost', { href: href('settings') }, t('home.chooseFfmpeg')))) : null;
    fill(inner, h('div.ob',
      h('div.ob-hero', h('div.welcome-mark', logo()), h('h1', added.size ? t('home.niceAddMore') : t('home.setUp')),
        h('p.lede', t('home.lede'))),
      h('section.ob-card',
        h('h2', icon('folder'), t('home.whereVideos')),
        sys && sys.can_edit === false ? h('p.muted', t('home.addedOnServer')) : [suggestions,
          h('div.ob-actions',
            h('button.btn', { type: 'button', onclick: () => openLibraryAction('add') }, icon('folder-plus', 'sm'), t('home.chooseFolder')),
            h('button.btn.ghost', { type: 'button', onclick: async () => { const c = await editConnection(); if (c) navigate('storage', c.id); } }, icon('cloud', 'sm'), t('home.inCloud')))]),
      ffmpegCard,
      added.size || state.libs.length ? h('div.ob-done', h('button.btn.primary.big', { type: 'button', onclick: async () => { guide = false; await loadLibraries(); refreshFeed(); } }, t('home.startWatching'), icon('chevron-right', 'sm'))) : null,
      h('p.welcome-tip', tx('home.tip', { keys: h('kbd', keys('K')) }))));
    paintFound();
    if (found === null && sys?.can_edit !== false) {
      try { found = (await get('/api/suggestions')).folders; } catch (e) { found = []; foundError = e.status === 403 ? t('home.foldersOnServer') : ''; }
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
      toast(t('home.addedFolder', { name: f.name }), { kind: 'ok' });
      onboarding();
    } catch (e) { btn.disabled = false; toastError(t('home.addFolderFailed'), e); }
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
