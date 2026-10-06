// Library: indexed folders as a grid of keyframe covers, opened in your player.
import { h, fill, $, $$, Scope, debounce } from '../lib/dom.js';
import { icon, kindIcon } from '../lib/icons.js';
import { get, post, put } from '../lib/api.js';
import { ago, bytes, clock, collator, extOf, leaf, num, resLabel, span, stem, when } from '../lib/fmt.js';
import { t } from '../lib/i18n.js';
import { href, navigate, replace } from '../lib/router.js';
import { loadLibraries, loadConnections, on, pokeWatcher, running, state, takeLibraryAction } from '../lib/state.js';
import { store } from '../lib/store.js';
import { confirmDialog, contextMenu, modal, showMenu, toast, toastError } from '../lib/ui.js';
import { locationPicker } from '../lib/picker.js';
import { playHere } from '../lib/handoff.js';
import { editConnection } from './connections.js';
import { openLibraryAction } from '../shell/sidebar.js';

// Every extension the indexer picks up. Until the user changes the filter, only the common video ones show.
const KNOWN_TYPES = ['mp4', 'm4v', 'mov', 'mkv', 'webm', 'avi', 'wmv', 'flv', 'ts', 'm2ts', 'mp3', 'flac', 'm4a', 'aac', 'wav', 'ogg', 'opus'];
const COMMON_VIDEO = new Set(['mp4', 'm4v', 'mov', 'mkv', 'webm', 'avi', 'wmv']);
const defaultHidden = () => new Set(KNOWN_TYPES.filter(x => !COMMON_VIDEO.has(x)));
const sameSet = (a, b) => a.size === b.size && [...a].every(x => b.has(x));
const shuffled = list => { const a = [...list]; for (let i = a.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [a[i], a[j]] = [a[j], a[i]]; } return a; };
const inScope = (it, s) => !s || it.dir === s || it.dir.startsWith(s + '/');
const TYPE_LABEL = { local: t('library.typeLocal'), s3: t('library.typeS3'), rclone: t('library.typeRclone') };
// A file's path on the computer running medialib, written the way that computer writes paths (D:\Videos\a.mp4 on
// Windows, /srv/videos/a.mp4 elsewhere), whatever computer the browser is on.
// A browser on the computer that serves the library (the desktop app, or one next to the server): only there can
// medialib open a folder on the screen.
const onThisComputer = () => ['127.0.0.1', 'localhost', '[::1]', '::1'].includes(location.hostname);
const revealLabel = () => ({ windows: t('library.showInExplorer'), darwin: t('library.showInFinder') })[(state.system?.platform || '').split('/')[0]] || t('library.showInFileManager');
const localPath = (root, key) => {
  const sep = /^[A-Za-z]:|^\\\\|\\/.test(root) ? '\\' : '/';
  return root.replace(/[\\/]+$/, '') + sep + key.split('/').join(sep);
};

// When medialib first found a file (an index from before that was kept: the file's own time).
const addedAt = it => it.added || it.mtime;
// Why a file could not be scanned, in plain words, from what ffmpeg or medialib said.
const REASONS = [
  [/moov|end of file|truncat|partial file/i, t('library.reasonIncomplete')],
  [/invalid data|could not find codec|unknown format|not supported|no indexed video track|EBML header parsing failed/i, t('library.reasonDamaged')],
  [/no frame could be extracted/i, t('library.reasonNoPicture')],
  [/permission denied|access is denied|403|forbidden/i, t('library.reasonNotAllowed')],
  [/no such file|not found|404|cannot find/i, t('library.reasonMoved')],
  [/timed? ?out|deadline exceeded|killed/i, t('library.reasonTooLong')],
  [/internal error/i, t('library.reasonInternal')],
];
const failReason = msg => (REASONS.find(([re]) => re.test(msg)) || [, t('library.reasonUnreadable')])[1];

const itemSort = {
  rel: (a, b) => a.rank - b.rank,
  name: (a, b) => collator.compare(a.it.name, b.it.name),
  new: (a, b) => addedAt(b.it).localeCompare(addedAt(a.it)) || collator.compare(a.it.name, b.it.name),
  size: (a, b) => b.it.size - a.it.size,
  dur: (a, b) => (b.it.duration || 0) - (a.it.duration || 0),
};
const groupSort = {
  rel: (a, b) => a.best - b.best,
  name: (a, b) => (b.key === '') - (a.key === '') || collator.compare(a.key, b.key), // loose files first
  new: (a, b) => b.newest.localeCompare(a.newest),
  size: (a, b) => b.size - a.size,
  dur: (a, b) => b.dur - a.dur,
};

export async function mount(root, parts) {
  const scope = new Scope();
  const S = {
    lib: null, info: null, version: null, items: [], byId: new Map(), scope: '', q: '', sort: store.get('sort', 'name'),
    tree: null, nodes: new Map(), shown: [], hidden: defaultHidden(), visibleIds: [], player: null,
    match: null, rank: null, ranked: true, sortAuto: true, pendingReveal: null,
    sel: new Set(), anchor: null, // the files picked with Ctrl or Shift, and the last one clicked (Shift picks from it)
  };
  try { const saved = store.get('hiddenTypes', null); if (saved) S.hidden = new Set(JSON.parse(saved)); } catch { /* keep the default */ }

  const thumb = (it, i) => `/thumbs/${S.lib}/${it.id}-${it.ver}-${i}.avif`;
  const subUrl = (it, name) => `${location.origin}/subs/${S.lib}/${it.id}/${encodeURIComponent(name)}`;
  // "Film.en.srt" next to "Film.mkv" reads "en (SRT)"; "Film.srt" just "SRT".
  const subLabel = (it, name) => { const mid = name.slice(stem(it.name).length + 1, name.lastIndexOf('.')); const ext = extOf(name).toUpperCase(); return mid ? `${mid} (${ext})` : ext; };
  const mediaUrl = it => `${location.origin}/media/${S.lib}/${it.id}/${encodeURIComponent(it.name)}`;
  // A folder's own picture (poster.jpg, folder.jpg), when it has one. The name in the address changes it when the file does.
  const artUrl = dir => S.info?.art?.[dir] != null ? `/art/${S.lib}?dir=${encodeURIComponent(dir)}&n=${encodeURIComponent(S.info.art[dir])}` : '';

  // ---------------------------------------------------------------- skeleton
  const libBtn = h('button.btn.lib-switch', { type: 'button', 'aria-haspopup': 'menu', onclick: e => openSwitcher(e.currentTarget) });
  const qInput = h('input.input', { type: 'search', placeholder: t('library.searchPlaceholder'), 'aria-label': t('library.searchNames'), autocomplete: 'off',
    title: t('library.searchTitle') });
  const sortSel = h('select', { 'aria-label': t('library.sort') }, [['rel', t('library.sortBest')], ['name', t('library.sortName')], ['new', t('library.sortNewest')], ['size', t('library.sortLargest')], ['dur', t('library.sortLongest')]].map(([v, label]) => h('option', { value: v }, label)));
  if (S.sort === 'rel') S.sort = 'name'; // "Best match" only means something while searching
  sortSel.value = S.sort;
  const saveBtn = h('button.icon-btn.small.save-search', { type: 'button', hidden: true, onclick: () => toggleSaved() }, icon('star', 'sm'));
  const savedRow = h('div.saved-searches', { hidden: true, role: 'list', 'aria-label': t('library.savedSearches') });
  const typesBtn = h('button.btn', { type: 'button', 'aria-haspopup': 'true', onclick: e => openTypes(e.currentTarget) }, icon('filter', 'sm'), h('span.types-label', t('library.types')));
  const optBtn = h('button.btn.icon-only', { type: 'button', 'aria-label': t('library.viewOptions'), title: t('library.viewOptionsTitle'), onclick: e => openOptions(e.currentTarget) }, icon('sliders', 'sm'));
  const ringText = h('span.ring-text');
  const ringBar = h('circle.ring-bar', { cx: 18, cy: 18, r: 15 });
  const ring = h('button.ring', { type: 'button', hidden: true, onclick: () => manageLibraries() },
    (() => { const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg'); s.setAttribute('viewBox', '0 0 36 36'); s.innerHTML = '<circle class="ring-track" cx="18" cy="18" r="15"/><circle class="ring-bar" cx="18" cy="18" r="15" pathLength="100" stroke-dasharray="0 100"/>'; return s; })(), ringText);
  const navToggle = h('button.icon-btn.nav-toggle', { type: 'button', 'aria-label': t('library.folders'), title: t('library.toggleFolders'), 'aria-expanded': 'true', onclick: () => toggleTree() }, icon('folder'));

  const tree = h('ul.tree');
  const nav = h('aside.tree-pane', { 'aria-label': t('library.folders') },
    h('div.nav-head', h('span', t('library.folders')), h('button.icon-btn', { type: 'button', 'aria-label': t('library.closeFolders'), onclick: () => toggleTree() }, icon('x'))), tree);
  const scrim = h('div.scrim', { hidden: true, onclick: () => navOpen(false) });

  const banner = h('div.banner.lib-banner', { hidden: true });
  const warnings = h('ul.warnings', { hidden: true });
  const crumbs = h('ol.crumbs');
  const stats = h('div.stats');
  const playAll = h('button.btn.primary', { type: 'button', onclick: () => play(S.visibleIds) }, icon('play', 'sm'), t('library.playAll'));
  const shuffleAll = h('button.btn', { type: 'button', title: t('library.shuffleTitle'), onclick: () => play(shuffled(S.visibleIds)) }, icon('shuffle', 'sm'), t('library.shuffle'));
  // On this computer a playlist is saved as a file of real links (paths, links into the bucket) that any player opens
  // without medialib; a browser elsewhere copies the playlist's address on this server instead.
  const plQuery = () => `lib=${encodeURIComponent(S.lib)}&dir=${encodeURIComponent(S.scope)}` + (S.hidden.size ? '&hide=' + encodeURIComponent([...S.hidden].join(',')) : '');
  const moreBtn = h('button.btn.icon-only', { type: 'button', 'aria-label': t('common.more'), title: t('library.moreTitle'), 'aria-haspopup': 'menu', onclick: e => openMore(e.currentTarget) }, icon('more'));
  const groups = h('div#groups');
  const empty = h('p.empty', { hidden: true }, t('library.nothingMatches'));
  const selText = h('div.sel-text');
  const selBar = h('div.selbar', { hidden: true, role: 'toolbar', 'aria-label': t('library.selectedVideos') },
    h('button.icon-btn.small', { type: 'button', 'aria-label': t('library.clearSelection'), title: t('library.clearSelectionTitle'), onclick: () => clearSelection() }, icon('x', 'sm')),
    selText,
    h('div.sel-actions',
      h('button.btn.small', { type: 'button', title: t('library.selectAllTitle'), onclick: () => selectAll() }, t('library.selectAll')),
      h('button.btn.small.icon-only', { type: 'button', 'aria-label': t('library.selectionPlaylist'), title: t('library.selectionPlaylist'), onclick: () => selectionPlaylist() }, icon(onThisComputer() ? 'download' : 'link', 'sm')),
      h('button.btn.small', { type: 'button', onclick: () => play(shuffled(selectedIds())) }, icon('shuffle', 'sm'), t('library.shuffle')),
      h('button.btn.small.primary', { type: 'button', onclick: () => play(selectedIds()) }, icon('play', 'sm'), t('common.play'))));
  const main = h('section.lib-main', savedRow, banner, warnings,
    h('div.scopebar', h('div.scope-text', crumbs, stats), h('div.actions', playAll, shuffleAll, moreBtn)), groups, empty);

  const bar = h('header.lib-bar', navToggle, libBtn, h('div.search', icon('search', 'sm'), qInput, saveBtn), h('div.bar-spacer'), sortSel, typesBtn, optBtn, ring);
  const view = h('div.lib-view', bar, h('div.lib-body', nav, main, selBar), scrim);
  root.append(view);
  if (store.get('treeClosed', '1') === '1') { view.classList.add('tree-closed'); navToggle.setAttribute('aria-expanded', 'false'); }
  const noLibs = h('div.page', { hidden: true }, h('div.page-inner', h('div.card-box.empty-state', icon('library'), h('h3', t('library.firstLibraryTitle')),
    h('p', t('library.firstLibraryText')),
    h('div', { style: { display: 'flex', gap: '8px', flexWrap: 'wrap', justifyContent: 'center', marginTop: '8px' } },
      h('button.btn.primary', { type: 'button', onclick: () => addLibrary() }, icon('plus', 'sm'), t('library.addLibrary'))))));
  root.append(noLibs);

  // ---------------------------------------------------------------- actions
  async function play(ids) {
    if (!ids.length) return;
    if (ids.length > 500) toast(t('library.playingFirst', { count: 500 }));
    try {
      const j = await post('/api/play', { lib: S.lib, ids: ids.slice(0, 500), player: S.player });
      toast(j.count > 1 ? t('library.openingItemsIn', { count: j.count, player: j.player }) : t('library.openingIn', { player: j.player }), { kind: 'ok' });
    } catch (e) {
      if (e.status === 403) { // a browser on another computer or a phone: the server cannot open a player on that screen
        const it = ids.length === 1 ? S.items.find(x => x.id === ids[0]) : null;
        if (it) { playHere(S.lib, it).catch(x => toastError(t('library.couldNotPlay'), x)); return; }
        window.open(`/api/playlist.m3u8?lib=${encodeURIComponent(S.lib)}&ids=${ids.slice(0, 500).map(encodeURIComponent).join(',')}`, '_blank');
        toast(t('library.downloadingPlaylist'), { kind: 'ok' });
        return;
      }
      toastError(t('library.couldNotStartPlayer'), e);
    }
  }
  async function copy(text, done) {
    try { await navigator.clipboard.writeText(text); toast(done, { kind: 'ok' }); } catch { toast(t('library.copyFailed', { text }), { kind: 'error' }); }
  }
  // Where a file really is, for any player or person: its path on this computer, a link into the bucket that works
  // anywhere (for a week), or, for a browser on another computer, its address on this server (the only way it has).
  const linkLabel = () => S.info.type === 'local' ? (onThisComputer() ? t('library.copyFilePath') : t('library.copyLink')) : t('library.copyLink');
  const linkLabelOf = name => S.info.type === 'local' && onThisComputer() ? t('library.copyFilePathOf', { name }) : t('library.copyLinkOf', { name });
  async function copyLink(it) {
    if (S.info.type === 'local') return onThisComputer() ? copy(localPath(S.info.location, it.key), t('library.filePathCopied')) : copy(mediaUrl(it), t('library.linkCopied'));
    try {
      const j = await get(`/api/link?lib=${encodeURIComponent(S.lib)}&id=${encodeURIComponent(it.id)}`);
      copy(j.url, t('library.linkCopiedWeek'));
    } catch (e) { toastError(t('library.couldNotMakeLink'), e); }
  }
  async function savePlaylist() {
    try {
      const j = await post(`/api/playlist/save?${plQuery()}`, {});
      if (j.path) toast(j.expires ? t('library.savedPlaylistLinks', { count: j.count, path: j.path }) : t('library.savedPlaylist', { count: j.count, path: j.path }), { kind: 'ok', ms: 6000 });
    } catch (e) { toastError(t('library.couldNotSavePlaylist'), e); }
  }
  const filePath = it => S.info.type === 'local' ? localPath(S.info.location, it.key) : S.info.type === 's3' ? `s3://${S.info.bucket}/${it.key}` : it.key;
  const canReveal = () => S.info.type === 'local' && onThisComputer();
  async function showInFolder(it) {
    try { await post('/api/reveal', { lib: S.lib, id: it.id }); } catch (e) { toastError(t('library.couldNotOpenFolder'), e); }
  }

  // Everything known about one file: all its keyframes (arrow keys step through them), what the index says, where it is.
  function showDetails(it) {
    const frames = it.frames || 0;
    let cur = Math.min(it.cover ?? 0, Math.max(0, frames - 1));
    const big = frames ? h('img', { alt: '', src: thumb(it, cur) })
      : h('div.det-ph', icon(it.kind === 'audio' ? 'music' : 'film', 'lg'), h('span', it.indexed === false && !it.error ? t('library.noCoverYet') : t('library.noPreview')));
    const strip = frames > 1 ? h('div.det-strip', Array.from({ length: frames }, (_, i) =>
      h('button', { type: 'button', 'aria-label': t('library.keyframeOf', { n: i + 1, total: frames }), 'aria-pressed': String(i === cur), onclick: () => pick(i) },
        h('img', { alt: '', loading: 'lazy', src: thumb(it, i) })))) : null;
    const pick = i => {
      cur = (i + frames) % frames;
      big.src = thumb(it, cur);
      for (const [j, b] of [...strip.children].entries()) b.setAttribute('aria-pressed', String(j === cur));
    };
    const row = (k, v) => (v == null || v === '' ? null : [h('dt', k), h('dd', v)]);
    const res = resLabel(it.width, it.height);
    const status = it.error ? h('span', { title: it.error }, t('library.couldNotBeScanned', { reason: failReason(it.error) })) : it.indexed === false ? t('library.noCoverYet')
      : it.note ? t('library.keyframesFfmpeg', { count: frames }) : t('library.keyframes', { count: frames });
    const meta = h('dl.det-meta',
      row(t('library.folder'), h('button.linkish', { type: 'button', title: t('library.showThisFolder'), onclick: () => { m.close(); go(it.dir); } }, it.dir || t('library.topLevel'))),
      row(t('library.size'), h('span', { title: t('library.bytesExact', { count: it.size }) }, bytes(it.size))),
      row(t('library.length'), it.duration ? clock(it.duration) : null),
      row(t('library.picture'), it.width ? `${it.width} × ${it.height}` + (res ? ' · ' + res : '') + (it.fps ? ` · ${it.fps} fps` : '') : null),
      row(t('library.subtitles'), it.subs?.length ? h('span', it.subs.map((n, i) => [i ? ', ' : '', h('a', { href: subUrl(it, n), target: '_blank', rel: 'noopener' }, subLabel(it, n))])) : null),
      row(t('library.codec'), it.codec ? it.codec + (it.kind === 'video' ? ' · ' + (it.audio ? t('library.withSound') : t('library.noSound')) : '') : null),
      row(S.info.type === 'local' ? t('library.modified') : t('library.uploaded'), h('span', { title: it.mtime }, when(it.mtime))),
      row(t('library.added'), it.added && it.added !== it.mtime ? h('span', { title: it.added }, when(it.added)) : null),
      row(S.info.type === 'local' ? t('library.file') : t('library.object'), h('code.det-path', filePath(it))),
      row(t('library.index'), status));
    const m = modal({
      title: it.name, size: 'wide', body: h('div.details', h('div.det-view', big, strip), meta),
      actions: [
        canReveal() ? { label: revealLabel(), left: true, keepOpen: true, onClick: () => showInFolder(it) } : null,
        S.info.type === 'local' ? null : { label: t('library.copyKey'), keepOpen: true, onClick: () => copy(it.key, t('library.keyCopied')) },
        { label: linkLabel(), keepOpen: true, onClick: () => copyLink(it) },
        { label: t('common.play'), primary: true, onClick: () => play([it.id]) },
      ].filter(Boolean),
    });
    m.el.addEventListener('keydown', e => {
      if (frames < 2 || e.target.closest('input, textarea')) return;
      if (e.key === 'ArrowRight') { e.preventDefault(); pick(cur + 1); } else if (e.key === 'ArrowLeft') { e.preventDefault(); pick(cur - 1); }
    });
  }

  // ---------------------------------------------------------------- folder tree
  function buildTree(items) {
    const rootNode = { name: t('library.allMedia'), path: '', kids: new Map(), count: 0, depth: -1 };
    const nodes = new Map([['', rootNode]]);
    for (const it of items) {
      rootNode.count++;
      let n = rootNode;
      if (!it.dir) continue;
      for (const part of it.dir.split('/')) {
        let c = n.kids.get(part);
        if (!c) {
          c = { name: part, path: n.path ? n.path + '/' + part : part, kids: new Map(), count: 0, parent: n, depth: n.depth + 1 };
          n.kids.set(part, c);
          nodes.set(c.path, c);
        }
        c.count++;
        n = c;
      }
    }
    return { root: rootNode, nodes };
  }
  const sortedKids = n => [...n.kids.values()].sort((a, b) => collator.compare(a.name, b.name));

  function navRow(node) {
    const li = h('li');
    const row = h('div.nrow', { style: { '--d': Math.max(0, node.depth) } });
    let tw;
    if (node.kids.size && node.depth >= 0) {
      tw = h('button.twist', { type: 'button', 'aria-expanded': 'false', 'aria-label': t('library.expand', { name: node.name }), onclick: () => (node.open ? collapse(node) : expand(node)) }, icon('chevron-right'));
    } else {
      tw = h('button.twist.leaf', { type: 'button', tabindex: -1, 'aria-hidden': 'true' });
    }
    const b = h('button.node', { type: 'button', class: node.depth < 0 ? 'all' : '', title: node.path || t('library.allMedia'), onclick: () => { go(node.path); navOpen(false); } },
      h('span.nname', node.name), h('span.count', String(node.count)));
    row.append(tw, b);
    li.append(row);
    Object.assign(node, { li, btn: b, tw });
    return li;
  }
  function expand(node) {
    if (!node.kids.size || node.depth < 0) return;
    if (!node.ul) { node.ul = h('ul'); for (const k of sortedKids(node)) node.ul.append(navRow(k)); node.li.append(node.ul); }
    node.ul.hidden = false;
    node.open = true;
    node.tw.setAttribute('aria-expanded', 'true');
  }
  function collapse(node) {
    if (node.ul) node.ul.hidden = true;
    node.open = false;
    node.tw.setAttribute('aria-expanded', 'false');
  }
  function renderTree() {
    fill(tree, navRow(S.tree), sortedKids(S.tree).map(navRow));
  }
  function markActive(path) {
    for (const b of $$('.node.active', tree)) b.classList.remove('active');
    const chain = [];
    for (let n = S.nodes.get(path); n && n.depth >= 0; n = n.parent) chain.unshift(n);
    chain.slice(0, -1).forEach(expand);
    const node = S.nodes.get(path);
    if (node && node.btn) { node.btn.classList.add('active'); node.btn.scrollIntoView({ block: 'nearest' }); }
  }
  // Wide windows: the folder pane slides away and the covers get the room. Narrow ones: it is a drawer.
  function toggleTree() {
    if (matchMedia('(min-width: 861px)').matches) {
      const closed = !view.classList.contains('tree-closed');
      view.classList.toggle('tree-closed', closed);
      store.set('treeClosed', closed ? '1' : '0');
      navToggle.setAttribute('aria-expanded', String(!closed));
    } else navOpen(!nav.classList.contains('open'));
  }
  function navOpen(open) { nav.classList.toggle('open', open); scrim.hidden = !open; navToggle.setAttribute('aria-expanded', String(open)); }

  // ---------------------------------------------------------------- cards
  const PLAY_HINT = '<svg viewBox="0 0 48 48" aria-hidden="true"><circle cx="24" cy="24" r="23" fill="rgb(0 0 0 / .55)"/><path d="M19 15v18l15-9z" fill="#fff"/></svg>';
  function card(entry) {
    const it = entry.it;
    const art = h('article.card', { dataset: { id: it.id }, class: S.sel.has(it.id) ? 'selected' : '' });
    const cover = h('button.cover', { type: 'button', 'aria-label': t('library.playName', { name: it.name }) });
    const dated = S.info.type === 'local' ? t('library.modifiedOn', { date: it.mtime.slice(0, 10) }) : t('library.uploadedOn', { date: it.mtime.slice(0, 10) });
    cover.title = [it.name, it.dir, [resLabel(it.width, it.height), it.codec, it.fps && it.fps + ' fps'].filter(Boolean).join(' · '), bytes(it.size) + ' · ' + dated].filter(Boolean).join('\n');
    if (it.frames) {
      cover.append(h('img', { alt: '', loading: 'lazy', decoding: 'async', src: thumb(it, it.cover ?? 0) }));
    } else {
      cover.append(h('div.ph', icon(it.kind === 'audio' ? 'music' : 'film', 'lg'),
        h('span', it.kind === 'audio' ? (it.codec || t('library.audio')).toUpperCase() : it.indexed === false && !it.error ? t('library.noCoverYet') : t('library.noPreview'))));
    }
    const res = resLabel(it.width, it.height);
    if (res) cover.append(h('span.badge.tl', res));
    if (it.duration) cover.append(h('span.badge.br', clock(it.duration)));
    if (it.subs?.length) cover.append(h('span.badge.tr', { title: t('library.subtitlesList', { list: it.subs.join(', ') }) }, 'CC'));
    if (it.frames > 1) cover.append(h('span.ticks', Array.from({ length: it.frames }, () => h('i'))));
    const hint = h('span.playhint'); hint.innerHTML = PLAY_HINT; cover.append(hint);
    const title = h('div.title', { title: t('library.clickForDetails', { name: it.name }) }, stem(it.name));
    const sub = h('div.sub', { title: `${bytes(it.size)} · ${when(it.mtime)}` }, entry.sub || t('library.addedAgo', { ago: ago(addedAt(it)) }));
    const cp = h('button.copy', { type: 'button', title: linkLabel(), 'aria-label': linkLabelOf(it.name) }, icon('copy', 'sm'));
    const pick = h('button.pick', { type: 'button', 'aria-label': t('library.selectName', { name: it.name }), 'aria-pressed': String(S.sel.has(it.id)), title: t('library.selectTitle') }, icon('check', 'sm'));
    art.append(cover, pick, h('div.meta', title, cp, sub));
    return art;
  }

  // Cards are made as they come near the screen. A library of ten thousand videos would otherwise build a hundred
  // thousand elements before showing anything; this way the first screen costs a few dozen. A spacer, sized from an
  // estimate of the cards still to come, keeps the scrollbar honest until they exist.
  const CHUNK = 48, MAX_STEP = 400, GAP_X = 18, GAP_Y = 24, META_H = 66, AHEAD = 1600;
  // Columns and row pitch come from the cards already on screen once there are two rows of them; until then a guess.
  function layoutOf(grid, remaining) {
    const cards = grid.getElementsByClassName('card');
    if (cards.length > 1) {
      const t0 = cards[0].offsetTop;
      let cols = 1;
      while (cols < cards.length && cards[cols].offsetTop === t0) cols++;
      if (cols < cards.length) return { cols, rowH: cards[cols].offsetTop - t0 };
      grid._cols = cols;
    }
    const w = Math.max(300, (groups.clientWidth || 1000) - 48);
    const min = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--card-min')) || 230;
    const cols = Math.max(1, Math.floor((w + GAP_X) / (min + GAP_X)));
    return { cols, rowH: ((w - GAP_X * (cols - 1)) / cols) * 9 / 16 + META_H + GAP_Y };
  }
  const spacerHeight = (grid, remaining) => {
    if (remaining <= 0) return 0;
    const { cols, rowH } = layoutOf(grid);
    return Math.ceil(remaining / cols) * rowH;
  };
  const fillIO = new IntersectionObserver(es => { for (const e of es) if (e.isIntersecting) pump(e.target); }, { root: main, rootMargin: `${AHEAD}px 0px` });
  scope.add(() => fillIO.disconnect());
  function makeGrid(entries) {
    const grid = h('div.grid');
    const spacer = h('div.spacer', { style: { gridColumn: '1 / -1' } });
    Object.assign(grid, { _entries: entries, _next: 0, _spacer: spacer });
    spacer._grid = grid;
    grid.append(spacer);
    spacer.style.height = spacerHeight(grid, entries.length) + 'px';
    fillIO.observe(spacer);
    return grid;
  }
  // Make the cards of a grid up to index end (exclusive) now.
  function fillTo(grid, end) {
    const spacer = grid._spacer, es = grid._entries;
    end = Math.min(es.length, end);
    const frag = document.createDocumentFragment();
    for (let i = grid._next; i < end; i++) frag.append(card(es[i]));
    grid.insertBefore(frag, spacer);
    grid._next = end;
    if (end >= es.length) { fillIO.unobserve(spacer); spacer.remove(); } else spacer.style.height = spacerHeight(grid, es.length - end) + 'px';
  }
  // Scroll to a file and flash it. Its card may not exist yet: the cards of its folder are made on the way.
  function reveal(id) {
    for (const grid of $$('.grid', groups)) {
      const i = (grid._entries || []).findIndex(e => e.it.id === id);
      if (i < 0) continue;
      fillTo(grid, i + 1);
      const el = grid.querySelector(`.card[data-id="${id}"]`);
      if (!el) return false;
      el.scrollIntoView({ block: 'center' });
      el.classList.add('flash');
      setTimeout(() => el.classList.remove('flash'), 2400);
      return true;
    }
    return false;
  }
  // Reveal the pending file as soon as its folder is drawn. A render may still be under way (a big folder, a slow
  // machine), so ask again for a couple of seconds before giving up.
  function consumeReveal() {
    const id = S.pendingReveal;
    if (!id) return;
    S.pendingReveal = null;
    let tries = 0;
    const attempt = () => {
      if (reveal(id)) return;
      if (++tries < 40) { setTimeout(attempt, 60); return; }
      const it = S.byId.get(id);
      toast(it && S.hidden.has(it.ext) ? t('library.hiddenByTypeFilter') : t('library.notInView'));
    };
    setTimeout(attempt, 0);
  }
  function pump(spacer) {
    const grid = spacer._grid;
    const step = () => {
      if (!grid.isConnected) return;
      const es = grid._entries, { cols, rowH } = layoutOf(grid);
      // How far the spacer's top is above the bottom of the area we want filled: that many pixels of rows, at once
      // but not so many that one frame stalls.
      const gap = main.getBoundingClientRect().bottom + AHEAD - spacer.getBoundingClientRect().top;
      let count = Math.min(MAX_STEP, Math.max(CHUNK, Math.ceil(gap / rowH) * cols));
      let end = Math.min(es.length, grid._next + count);
      if (end < es.length && end % cols) end = Math.min(es.length, end + cols - (end % cols)); // whole rows
      fillTo(grid, end);
      if (end >= es.length) return;
      // Still close to the screen: carry on. Otherwise the observer wakes us when the spacer comes within reach.
      if (spacer.getBoundingClientRect().top < main.getBoundingClientRect().bottom + AHEAD) requestAnimationFrame(step);
    };
    step();
  }

  // The query is answered by the server (CJK, pinyin, typos, tags: see /api/search); until its answer arrives, or when it
  // cannot answer, plain substring matching keeps the view usable.
  const currentList = () => {
    const s = S.scope, q = S.q.trim().toLowerCase();
    if (q && S.match) return S.shown.filter(it => inScope(it, s) && S.match.has(it.id));
    return S.shown.filter(it => inScope(it, s) && (!q || it.name.toLowerCase().includes(q) || it.dir.toLowerCase().includes(q)));
  };
  // "Best match" only means something for words: a query of filters alone (dur>1h) keeps the chosen order.
  const effectiveSort = () => (S.match && S.q.trim() && S.ranked && (S.sortAuto || S.sort === 'rel') ? 'rel' : S.sort === 'rel' ? 'name' : S.sort);
  const paintSort = () => { sortSel.value = effectiveSort(); };
  let searchSeq = 0;
  async function runSearch() {
    const q = qInput.value, mine = ++searchSeq;
    S.q = q;
    if (!q.trim()) { S.match = S.rank = null; S.sortAuto = true; paintSort(); render(); return; }
    try {
      const j = await get(`/api/search?lib=${encodeURIComponent(S.lib)}&limit=0&ids=1&q=${encodeURIComponent(q)}`);
      if (mine !== searchSeq) return;
      S.match = new Set(j.ids);
      S.rank = new Map(j.ids.map((id, i) => [id, i]));
      S.ranked = j.ranked !== false;
    } catch {
      if (mine !== searchSeq) return;
      S.match = S.rank = null;
    }
    paintSort();
    render();
  }

  function render() {
    fillIO.disconnect(); // the spacers of the previous render
    const s = S.scope, list = currentList(), grp = new Map();
    for (const it of list) {
      const rel = it.dir === s ? '' : s ? it.dir.slice(s.length + 1) : it.dir;
      const key = rel.split('/')[0];
      let g = grp.get(key);
      if (!g) grp.set(key, g = { key, entries: [], size: 0, dur: 0, newest: '', best: 1e9 });
      const rank = S.rank ? S.rank.get(it.id) ?? 1e9 : 0;
      g.entries.push({ it, rank, sub: rel.includes('/') ? rel.slice(key.length + 1) : '' });
      if (rank < g.best) g.best = rank;
      g.size += it.size;
      g.dur += it.duration || 0;
      if (addedAt(it) > g.newest) g.newest = addedAt(it);
    }
    const frag = document.createDocumentFragment();
    S.visibleIds = [];
    const how = effectiveSort();
    // Folders as cards (the default): each subfolder is one tile, its files a click away; the files right here
    // follow. While searching, or with "All videos" chosen, every match shows, grouped by folder.
    if (store.get('libDisplay', 'folders') === 'folders' && !S.q.trim()) {
      const folders = [...grp.values()].filter(g => g.key).sort(groupSort[how]);
      const here = grp.get('');
      for (const g of folders) S.visibleIds.push(...g.entries.sort(itemSort[how]).map(e => e.it.id));
      if (folders.length) frag.append(h('section.group.folders', h('div.fgrid', folders.map(g => folderCard(s, g)))));
      if (here) {
        here.entries.sort(itemSort[how]);
        S.visibleIds.push(...here.entries.map(e => e.it.id));
        frag.append(h('section.group', folders.length ? h('header.ghead', h('div.gpath', h('span.gtitle', t('library.filesInThisFolder'))),
          h('span.gstats', `${here.entries.length} · ${span(here.dur)}`)) : null, makeGrid(here.entries)));
      }
    } else for (const g of [...grp.values()].sort(groupSort[how])) {
      g.entries.sort(itemSort[how]);
      const ids = g.entries.map(e => e.it.id);
      S.visibleIds.push(...ids);
      const gp = groupPath(s, g.key);
      const pb = h('button.btn.small', { type: 'button', 'aria-label': t('library.playAllIn', { name: gp.title }), onclick: () => play(ids) }, icon('play', 'sm'), t('common.play'));
      // A group without a key holds the files that sit directly in the folder being viewed.
      frag.append(h('section.group', h('header.ghead', gp, h('span.gstats', `${g.key ? '' : t('library.filesDirectlyHere') + ' · '}${g.entries.length} · ${span(g.dur)} · ${bytes(g.size)}`), pb),
        makeGrid(g.entries)));
    }
    pathIO.disconnect();
    onScreen.clear();
    groups.replaceChildren(frag);
    for (const box of $$('.gpath', groups)) pathIO.observe(box);
    empty.hidden = list.length > 0 || !S.items.length;

    const ps = s ? s.split('/') : [];
    fill(crumbs, [[S.info?.name || t('library.allMedia'), ''], ...ps.map((p, i) => [p, ps.slice(0, i + 1).join('/')])].map(([name, path], i, arr) => {
      const b = h('button', { type: 'button' }, name);
      if (i < arr.length - 1) b.addEventListener('click', () => go(path)); else b.setAttribute('aria-current', 'page');
      return h('li', b);
    }));
    const total = list.reduce((a, it) => a + (it.duration || 0), 0), size = list.reduce((a, it) => a + it.size, 0);
    const filtered = S.hidden.size ? S.items.filter(it => inScope(it, s) && S.hidden.has(it.ext)).length : 0;
    stats.textContent = `${t('library.videos', { count: list.length })} · ${span(total)} · ${bytes(size)}` + (S.q.trim() ? ' · ' + t('library.matching', { q: S.q.trim() }) : '') + (filtered ? ' · ' + t('library.hiddenByType', { count: filtered }) : '');
    document.title = (s ? leaf(s) + ' · ' : '') + (S.info ? S.info.name + ' · ' : '') + 'Media Library';
  }

  // A subfolder as a tile: the covers of its newest videos, its name, how much is in it.
  function folderCard(s, g) {
    const path = s ? s + '/' + g.key : g.key;
    const covers = [];
    for (const e of g.entries) {
      if (!e.it.frames) continue;
      let pos = covers.length;
      while (pos > 0 && addedAt(covers[pos - 1]) < addedAt(e.it)) pos--;
      if (pos < 4) { covers.splice(pos, 0, e.it); covers.length = Math.min(covers.length, 4); }
    }
    const ids = () => g.entries.map(e => e.it.id);
    const mosaic = () => h('div.tile-mosaic', { class: `n${covers.length}` }, covers.length ? covers.map(it => h('img', { src: thumb(it, it.cover ?? 0), alt: '', loading: 'lazy', decoding: 'async' }))
      : h('div.ph', icon('folder', 'lg')));
    const own = artUrl(path);
    // The folder's own poster when it has one; its newest covers otherwise, and if the picture cannot be loaded.
    const tile = own ? h('div.tile-mosaic.art', h('img', { src: own, alt: '', loading: 'lazy', decoding: 'async', onerror: () => tile.replaceWith(mosaic()) })) : mosaic();
    return h('button.fcard', { type: 'button', title: path, onclick: () => go(path),
      oncontextmenu: e => contextMenu(e, [
        { label: t('common.open'), icon: 'folder', onClick: () => go(path) },
        { label: t('library.playAll'), icon: 'play', onClick: () => play(ids()) },
        { label: t('library.shuffle'), icon: 'shuffle', onClick: () => play(shuffled(ids())) },
      ]) },
    tile,
    h('div.tile-meta', h('span.tile-name', icon('folder', 'sm'), g.key), h('span.tile-status', `${t('library.videos', { count: g.entries.length })}${g.dur ? ' · ' + span(g.dur) : ''}`)));
  }
  function openMore(anchor) {
    showMenu({ anchor, align: 'right', items: [
      onThisComputer()
        ? { label: t('library.saveAsPlaylist'), icon: 'download', onClick: () => savePlaylist() }
        : { label: t('library.copyPlaylistLink'), icon: 'link', onClick: () => copy(`${location.origin}/api/playlist.m3u8?${plQuery()}`, t('library.playlistLinkCopied')) },
      { label: t('library.findDuplicates'), icon: 'layers', onClick: () => showDuplicates() },
      { sep: true },
      { label: t('library.scanForNewVideos'), icon: 'refresh', onClick: () => startIndex(S.lib) },
      { label: t('library.librarySettings'), icon: 'sliders', onClick: () => manageLibraries() },
    ] });
  }

  // Group titles: the full folder path, every folder from the top of the library down to the group. When it does
  // not fit, whole folders are left out starting right after the first one, so the top folder and the ones nearest
  // to the content stay readable.
  function groupPath(sc, key) {
    const segs = [...(sc ? sc.split('/') : []), ...(key ? [key] : [])];
    const box = h('div.gpath');
    box._parts = segs.length ? segs.map((name, i) => ({ name, path: segs.slice(0, i + 1).join('/') })) : [{ name: t('library.topLevelFiles'), path: '' }];
    box.title = box._parts.map(p => p.name).join(' › ');
    drawPath(box, 0);
    return box;
  }
  function drawPath(box, omit) {
    const parts = box._parts, last = parts.length - 1;
    box.replaceChildren();
    parts.forEach((p, i) => {
      if (i > 0 && i <= omit && i < last) {
        if (i === 1) {
          const more = h('span.gmore', { title: parts.slice(1, Math.min(omit, last - 1) + 1).map(x => x.name).join(' › ') }, '…');
          box.append(h('span.gsep', '›'), more);
        }
        return;
      }
      if (i > 0) box.append(h('span.gsep', '›'));
      box.append(h('button', { type: 'button', class: i === last ? 'gtitle' : 'gcrumb', dataset: { path: p.path } }, p.name));
    });
  }
  function fitPath(box) {
    if (!box.clientWidth || !box._parts) return; // "Files in this folder" is a plain title, not a path
    box.classList.add('measuring');
    const middle = Math.max(0, box._parts.length - 2);
    for (let omit = 0; omit <= middle; omit++) { drawPath(box, omit); if (box.scrollWidth <= box.clientWidth) break; }
    box.classList.remove('measuring');
  }
  const onScreen = new Set();
  const pathIO = new IntersectionObserver(entries => {
    for (const e of entries) { if (e.isIntersecting) { onScreen.add(e.target); fitPath(e.target); } else onScreen.delete(e.target); }
  }, { root: main });
  const pathRO = new ResizeObserver(() => { for (const box of onScreen) fitPath(box); });
  pathRO.observe(groups);
  scope.add(() => { pathIO.disconnect(); pathRO.disconnect(); });

  function go(path) { navigate('library', S.lib, ...(path ? path.split('/') : [])); }
  function applyScope(path) {
    S.scope = S.nodes.has(path) ? path : '';
    markActive(S.scope);
    render();
    main.scrollTop = 0;
  }

  // ---------------------------------------------------------------- loading
  async function loadLibrary(keepView = false) {
    const lib = await get('/api/library?lib=' + encodeURIComponent(S.lib));
    S.info = lib;
    S.version = lib.version;
    S.items = lib.items;
    for (const it of lib.items) it.ext = extOf(it.name);
    S.byId = new Map(lib.items.map(it => [it.id, it]));
    rebuildView(keepView);
    paintTypes();
    paintBanner();
    paintSwitch();
    if (S.q.trim()) runSearch(); // files may have arrived
  }
  // While a scan runs: fetch only what changed since the version on screen, and redraw just the cards it touches.
  // Files that come or go change the folders, so then (and when the scan ends) the view is rebuilt, still without
  // downloading the whole library again.
  async function refreshLibrary(final = false) {
    if (!S.version) return loadLibrary(true);
    const id = S.lib;
    const j = await get(`/api/library?lib=${encodeURIComponent(id)}&since=${encodeURIComponent(S.version)}`);
    if (id !== S.lib) return;
    if (!j.changed) { // the server no longer knows that version (it restarted): this is the whole library
      S.info = j; S.version = j.version; S.items = j.items;
      for (const it of S.items) it.ext = extOf(it.name);
      S.byId = new Map(S.items.map(it => [it.id, it]));
      final = true;
    } else {
      S.info = j;
      S.version = j.version;
      let shape = j.removed.length > 0;
      for (const fresh of j.changed) {
        fresh.ext = extOf(fresh.name);
        const old = S.byId.get(fresh.id);
        if (!old) { S.items.push(fresh); S.byId.set(fresh.id, fresh); shape = true; continue; }
        for (const k of Object.keys(old)) if (!(k in fresh)) delete old[k]; // a field the server leaves out when empty
        Object.assign(old, fresh);
      }
      if (j.removed.length) {
        const gone = new Set(j.removed);
        S.items = S.items.filter(it => !gone.has(it.id));
        for (const x of gone) S.byId.delete(x);
      }
      if (!shape && !final) { redrawCards(new Set(j.changed.map(x => x.id))); paintBanner(); return; }
    }
    rebuildView(true);
    paintTypes();
    paintBanner();
    paintSwitch();
    if (S.q.trim()) runSearch();
  }
  function redrawCards(ids) {
    if (!ids.size) return;
    for (const grid of $$('.grid', groups)) {
      for (const el of grid.querySelectorAll('.card')) {
        if (!ids.has(el.dataset.id)) continue;
        const entry = grid._entries.find(e => e.it.id === el.dataset.id);
        if (entry) el.replaceWith(card(entry));
      }
    }
  }
  function rebuildView(keepView, scopePath = S.scope) {
    S.shown = S.hidden.size ? S.items.filter(it => !S.hidden.has(it.ext)) : S.items;
    const { root: r, nodes } = buildTree(S.shown);
    S.tree = r;
    S.nodes = nodes;
    renderTree();
    if (keepView && nodes.has(S.scope)) { markActive(S.scope); render(); } else applyScope(scopePath);
    paintSelection();
  }

  const paintSwitch = () => {
    const lib = state.libs.find(l => l.id === S.lib);
    fill(libBtn, icon(lib && lib.type === 'local' ? 'folder' : 'cloud', 'sm'), h('span.lib-switch-name', lib ? lib.name : t('library.library')), icon('chevron-down', 'sm'));
  };

  async function switchLibrary(id, path = '') {
    S.lib = id;
    S.sel.clear(); S.anchor = null; paintSelection();
    S.q = '';
    S.match = S.rank = null;
    S.sortAuto = true;
    searchSeq++;
    qInput.value = '';
    paintSaved();
    S.scope = path;
    post('/api/libraries/active', { id }).catch(() => {});
    groups.classList.add('busy');
    try { await loadLibrary(); } finally { groups.classList.remove('busy'); }
  }

  // ---------------------------------------------------------------- library switcher + manager
  function openSwitcher(anchor) {
    showMenu({
      anchor, items: [
        { head: t('library.libraries') },
        ...state.libs.map(l => ({ label: l.name, icon: l.type === 'local' ? 'folder' : 'cloud', check: l.id === S.lib, onClick: () => navigate('library', l.id) })),
        { sep: true },
        { label: t('library.addLibraryDots'), icon: 'plus', onClick: () => addLibrary() },
        { label: t('library.manageLibraries'), icon: 'settings', onClick: () => manageLibraries() },
      ],
    });
  }

  function manageLibraries() {
    const list = h('ul.lib-list');
    const m = modal({ title: t('library.libraries'), size: 'wide', body: list, actions: [{ label: t('library.addLibraryDots'), left: true, keepOpen: true, onClick: () => { m.close(); addLibrary(); return false; } }, { label: t('common.done'), primary: true, value: true }] });
    const paint = () => fill(list, state.libs.map(l => {
      const job = state.jobs[l.id];
      const meta = h('div.lib-meta');
      if (running(job)) {
        meta.append(job.state === 'waiting' ? t('library.waitingForScan') : job.state === 'listing' ? t('library.lookingForVideos') : t('library.makingCovers', { done: job.done, total: job.total }),
          h('div.meter', h('i', { style: { width: (job.state === 'indexing' && job.total ? job.done / job.total * 100 : 0) + '%' } })));
      } else {
        meta.textContent = job && job.state === 'error' ? t('library.scanFailed', { error: job.line }) : l.type === 'local' && !l.reachable ? t('library.folderNotFound')
          : l.type === 's3' && !l.reachable ? t('library.connectionRemoved') : l.updated ? t('library.videosScanned', { count: l.items, ago: ago(l.updated) }) : t('library.notScannedYet');
      }
      const busy = running(job);
      return h('li.lib', h('div.lib-main',
        h('div.lib-name', l.name, h('span.tag', TYPE_LABEL[l.type] || l.type), l.type === 's3' && l.connection_name ? h('span.tag', l.connection_name) : null, l.id === S.lib ? h('span.tag.accent', t('library.current')) : null),
        h('div.lib-loc.mono', l.location), meta),
        h('div.lib-actions',
          h('button.btn.small', { type: 'button', disabled: l.id === S.lib, onclick: () => { m.close(); navigate('library', l.id); } }, t('common.open')),
          h('button.btn.small', { type: 'button', disabled: busy, onclick: () => startIndex(l.id) }, l.updated ? t('library.updateIndex') : t('library.indexNow')),
          h('button.icon-btn.small', { type: 'button', 'aria-label': t('common.more'), onclick: e => showMenu({ anchor: e.currentTarget, align: 'right', items: [
            { label: t('library.editDots'), icon: 'edit', disabled: busy, onClick: () => editLibrary(l) },
            { label: t('library.remakeCovers'), icon: 'refresh', disabled: busy, onClick: () => startIndex(l.id, true) },
            l.convertible ? { label: t('library.readOverS3'), icon: 'cloud', disabled: busy, onClick: () => convert(l) } : null,
            { sep: true },
            { label: t('common.remove'), icon: 'trash', danger: true, disabled: busy || state.libs.length < 2, onClick: () => removeLibrary(l) },
          ].filter(Boolean) }) }, icon('more'))));
    }));
    const off = on('activity', paint), off2 = on('libraries', paint);
    m.closed.then(() => { off(); off2(); });
    paint();
  }

  // Edit a library: its name, and where it points (folder, or connection + bucket + folder). A new location is re-indexed.
  function editLibrary(l) {
    const nameIn = h('input.input', { value: l.name, autocomplete: 'off' });
    const err = h('p.form-error', { hidden: true });
    let where = null, getBody = () => ({});
    if (l.type === 'local') {
      const pathIn = h('input.input', { value: l.location, autocomplete: 'off', spellcheck: 'false' });
      const browse = h('button.btn', { type: 'button', onclick: async e => {
        const b = e.currentTarget; b.disabled = true; b.textContent = t('library.pickInDialog');
        try { const j = await post('/api/pick-folder', {}); if (j.path) pathIn.value = j.path; } catch (x) { err.textContent = x.message; err.hidden = false; }
        b.disabled = false; b.textContent = t('library.browse');
      } }, t('library.browse'));
      where = h('div.field', h('label', t('library.folder')), h('div.input-wrap', pathIn, browse));
      getBody = () => ({ path: pathIn.value });
    } else if (l.type === 's3') {
      let loc = { conn: l.connection, bucket: l.bucket, prefix: l.prefix };
      const picker = locationPicker({ conn: l.connection, bucket: l.bucket, prefix: l.prefix, onChange: v => { loc = v; } });
      where = h('div.field', h('div.label', t('library.location')), picker.el);
      getBody = () => ({ connection: loc.conn, bucket: loc.bucket, prefix: loc.prefix });
    } else {
      where = h('p.hint', t('library.rcloneHint'));
    }
    const m = modal({
      title: t('library.editLibrary'), size: l.type === 's3' ? 'wide' : '',
      body: h('div', err, h('div.field', h('label', t('library.name')), nameIn), where,
        l.type === 'rclone' ? null : h('p.hint', t('library.locationChangeHint'))),
      actions: [{ label: t('common.cancel'), value: false }, { label: t('common.save'), primary: true, keepOpen: true, onClick: async api => {
        err.hidden = true;
        try {
          const j = await post('/api/libraries/update', { id: l.id, name: nameIn.value, ...getBody() });
          await loadLibraries();
          api.close(true);
          if (l.id === S.lib) { paintSwitch(); loadLibrary(); }
          if (j.moved) { toast(t('library.locationChanged'), { kind: 'ok' }); pokeWatcher(); }
        } catch (e) { err.textContent = e.message; err.hidden = false; }
        return false;
      } }],
    });
    nameIn.select();
  }
  async function convert(l) {
    if (!await confirmDialog({ title: t('library.convertTitle'), message: t('library.convertMessage', { name: l.name }), detail: t('library.convertDetail'), confirm: t('library.switch') })) return;
    try { await post('/api/libraries/convert', { id: l.id }); await Promise.all([loadLibraries(), loadConnections()]); toast(t('library.nowReadingOverS3'), { kind: 'ok' }); if (l.id === S.lib) loadLibrary(true); } catch (e) { toastError(t('library.couldNotSwitch'), e); }
  }
  async function removeLibrary(l) {
    if (!await confirmDialog({ title: t('library.removeTitle'), message: t('library.removeMessage', { name: l.name }), detail: t('library.removeDetail'), confirm: t('common.remove'), danger: true })) return;
    try {
      const j = await post('/api/libraries/remove', { id: l.id });
      await loadLibraries();
      if (l.id === S.lib) navigate('library', j.active);
    } catch (e) { toastError(t('library.couldNotRemove'), e); }
  }
  async function startIndex(id, force = false, retry = false) {
    try { state.jobs[id] = await post('/api/index', { id, force, retry }); paintBanner(); pokeWatcher(); } catch (e) { toastError(t('library.couldNotStartIndexing'), e); }
  }

  // ---------------------------------------------------------------- files that could not be scanned
  const failedItems = () => S.items.filter(it => it.error);
  function showFailed() {
    const list = h('ul.fail-list');
    const m = modal({
      title: t('library.failedTitle'), size: 'wide', body: h('div', h('p.hint',
        t('library.failedHint')), list),
      actions: [{ label: t('common.close'), left: true, value: false },
        { label: t('common.retry'), primary: true, onClick: () => startIndex(S.lib, false, true) }],
    });
    const paint = () => {
      const items = failedItems().sort((a, b) => collator.compare(a.key, b.key));
      if (!items.length) { fill(list, h('li.fail-row.muted', t('library.everyFileScanned'))); return; }
      fill(list, items.map(it => h('li.fail-row',
        h('div.grow', h('div.fail-name', it.name), h('div.fail-dir.muted', it.dir || t('library.topLevel')),
          h('div.fail-why', failReason(it.error)), h('div.fail-raw.mono', it.error)),
        h('div.fail-actions',
          canReveal() ? h('button.btn.small', { type: 'button', onclick: () => showInFolder(it) }, revealLabel()) : null,
          h('button.btn.small', { type: 'button', onclick: () => { m.close(); openLibraryAction('reveal', { lib: S.lib, dir: it.dir, id: it.id }); } }, t('library.showInLibrary'))))));
    };
    paint();
  }

  // ---------------------------------------------------------------- add a library
  function addLibrary() {
    let mode = state.connections.length ? store.get('addMode', 'local') : 'local';
    const pathIn = h('input.input', { placeholder: t('library.pathPlaceholder'), autocomplete: 'off', spellcheck: 'false' });
    const nameIn = h('input.input', { placeholder: t('library.nameOptional'), autocomplete: 'off' });
    const err = h('p.form-error', { hidden: true });
    const browse = h('button.btn', { type: 'button', onclick: async e => {
      const b = e.currentTarget; b.disabled = true; b.textContent = t('library.pickInDialog');
      try { const j = await post('/api/pick-folder', {}); if (j.path) { pathIn.value = j.path; err.hidden = true; } } catch (x) { showErr(x.message); }
      b.disabled = false; b.textContent = t('library.browse');
    } }, t('library.browse'));
    const localPane = h('div', h('p.hint', t('library.localHint')),
      h('div.field', h('label', t('library.folder')), h('div.input-wrap', pathIn, browse)), h('div.field', h('label', t('library.name')), nameIn));
    let loc = { conn: '', bucket: '', prefix: '' };
    const s3Name = h('input.input', { placeholder: t('library.nameOptional'), autocomplete: 'off' });
    const picker = state.connections.length ? locationPicker({ onChange: v => { loc = v; } }) : null;
    const s3Pane = h('div', picker ? [h('p.hint', t('library.s3Hint')), picker.el,
      h('div.field', { style: { marginTop: '14px' } }, h('label', t('library.name')), s3Name)]
      : h('div.empty-state', icon('plug'), h('h3', t('library.noConnectionTitle')), h('p', t('library.noConnectionText')),
        h('button.btn.primary', { type: 'button', onclick: async () => { const c = await editConnection(); if (c) { m.close(); addLibrary(); } } }, icon('plus', 'sm'), t('library.addConnection'))));
    const tabs = h('div.seg', { role: 'tablist' }, [['local', t('library.typeLocal')], ['s3', t('library.bucketS3')]].map(([id, label]) =>
      h('button', { type: 'button', role: 'tab', dataset: { id }, 'aria-selected': String(id === mode), onclick: () => setMode(id) }, label)));
    const panes = h('div', localPane, s3Pane);
    const setMode = id => {
      mode = id; store.set('addMode', id);
      for (const b of tabs.children) b.setAttribute('aria-selected', String(b.dataset.id === id));
      localPane.hidden = id !== 'local'; s3Pane.hidden = id !== 's3'; err.hidden = true;
    };
    const showErr = msg => { err.textContent = msg; err.hidden = false; };
    setMode(mode);
    const m = modal({
      title: t('library.addLibrary'), body: h('div', h('div', { style: { marginBottom: '14px' } }, tabs), err, panes),
      actions: [{ label: t('common.cancel'), value: false }, { label: t('library.addAndScan'), primary: true, keepOpen: true, onClick: async api => {
        err.hidden = true;
        try {
          const lib = mode === 'local' ? await post('/api/libraries', { path: pathIn.value, name: nameIn.value })
            : await post('/api/libraries', { type: 's3', connection: loc.conn, bucket: loc.bucket, prefix: loc.prefix, name: s3Name.value });
          await loadLibraries();
          api.close(true);
          navigate('library', lib.id);
          startIndex(lib.id);
        } catch (e) { showErr(e.message); }
        return false;
      } }],
    });
  }

  // ---------------------------------------------------------------- banner, ring
  function paintBanner() {
    const job = state.jobs[S.lib], info = S.info;
    if (!info) return;
    const pending = S.items.filter(it => it.kind === 'video' && !it.frames && !it.error).length;
    let msg = '', act = '', kind = '';
    paintRing();
    let detail = '', edit = false;
    if (running(job)) {
      if (job.state === 'waiting') msg = job.line;  // progress itself lives in the ring
      else if (!S.items.length) { msg = t('library.lookingForVideos'); detail = t('library.coversAppear'); }
    } else if (job && job.state === 'error') { msg = t('library.lastScanFailed'); detail = job.line; act = t('common.retry'); kind = 'error'; }
    else if (info.type === 'local' && !info.reachable) {
      msg = t('library.cantFindFolder'); detail = t('library.cantFindFolderDetail', { location: info.location }); kind = 'warn'; edit = true;
    } else if (info.type === 's3' && !info.reachable) { msg = t('library.connectionRemovedBanner'); detail = t('library.coversFromLastScan'); kind = 'warn'; edit = true; }
    else if (!S.items.length) {
      msg = info.updated ? t('library.noVideosFound') : t('library.notScannedYetBanner');
      detail = info.updated ? t('library.addVideosThenScan') : t('library.scanFindsVideos'); act = info.updated ? t('library.scanAgain') : t('library.scanForVideos');
    } else if (pending) { msg = t('library.withoutCover', { count: pending }); act = t('library.makeCovers'); }
    let failed = 0;
    if (!msg && !running(job) && (failed = failedItems().length)) {
      msg = t('library.filesCouldNotBeScanned', { count: failed }); detail = t('library.failedDetail'); kind = 'warn';
    }
    banner.hidden = !msg;
    banner.className = 'banner lib-banner ' + kind;
    fill(banner, h('div.grow', h('strong', msg), detail ? h('div.banner-detail', detail) : null),
      edit ? h('button.btn', { type: 'button', onclick: () => manageLibraries() }, t('library.editLibraryDots')) : null,
      act ? h('button.btn', { type: 'button', class: kind ? '' : 'primary', onclick: () => startIndex(S.lib) }, act) : null,
      failed ? h('button.btn', { type: 'button', onclick: () => showFailed() }, t('library.seeWhich')) : null);
    fill(warnings, (info.warnings || []).map(w => h('li', w)));
    warnings.hidden = !warnings.children.length;
  }
  function paintRing() {
    let id = S.lib, job = state.jobs[id];
    if (!running(job)) { id = Object.keys(state.jobs).find(k => running(state.jobs[k])); job = id && state.jobs[id]; }
    if (!running(job)) { ring.hidden = true; return; }
    const name = (state.libs.find(l => l.id === id) || {}).name || id;
    const busy = job.state !== 'indexing' || !job.total;
    const pct = busy ? 0 : Math.floor(job.done / job.total * 100);
    ring.hidden = false;
    ring.classList.toggle('busy', busy);
    $('.ring-bar', ring).setAttribute('stroke-dasharray', `${busy ? 25 : pct} 100`);
    ringText.textContent = busy ? '' : pct + '%';
    const text = job.state === 'waiting' ? t('library.ringWaiting', { name }) : job.state === 'listing' ? t('library.ringListing', { name })
      : job.errors ? t('library.ringMakingFailed', { name, pct, done: job.done, total: job.total, errors: job.errors }) : t('library.ringMaking', { name, pct, done: job.done, total: job.total });
    ring.title = text;
    ring.setAttribute('aria-label', t('library.ringLabel', { text }));
  }

  // Refresh the open library's covers as indexing lands them, and once more when a run finishes.
  let prevJobs = JSON.parse(JSON.stringify(state.jobs)), lastRefresh = 0;
  const runRequested = () => {
    const a = takeLibraryAction();
    if (!a) return;
    if (a.name === 'add') addLibrary();
    else if (a.name === 'manage') manageLibraries();
    else if (a.name === 'reveal') {
      // The caller has navigated to the file's folder. Drop any search so the file is in view, then scroll to it:
      // now if this view already shows that folder, otherwise when the pending update has drawn it.
      const { lib, dir, id } = a.arg;
      qInput.value = '';
      paintSaved();
      S.q = ''; S.match = S.rank = null; S.sortAuto = true; searchSeq++;
      S.pendingReveal = id;
      if (S.lib === lib && S.scope === dir) { render(); consumeReveal(); }
    }
  };
  scope.on(document, 'medialib:library-action', runRequested);
  scope.add(on('activity', () => {
    const before = prevJobs[S.lib], now = state.jobs[S.lib];
    const anyFinished = Object.keys(state.jobs).some(id => running(prevJobs[id]) && !running(state.jobs[id]));
    prevJobs = JSON.parse(JSON.stringify(state.jobs));
    const finished = running(before) && !running(now);
    if (finished || (running(now) && now.state === 'indexing' && Date.now() - lastRefresh > 8000)) {
      lastRefresh = Date.now();
      refreshLibrary(finished).catch(() => loadLibrary(true).catch(() => {}));
    }
    if (anyFinished) loadLibraries().catch(() => {});  // a finished run changes a library's item count and indexed time
    paintBanner();
    if (finished && now.state === 'done') {
      if (now.errors) toast(t('library.scanFinishedErrors', { count: now.errors }), { kind: 'error', ms: 8000, action: { label: t('library.seeWhich'), run: showFailed } });
      else toast(t('library.scanFinishedOk'), { kind: 'ok' });
    }
  }));

  // ---------------------------------------------------------------- selection
  // Picked files stay picked from folder to folder, so a playlist can gather videos from all over the library.
  const selectedIds = () => {
    const shown = S.visibleIds.filter(id => S.sel.has(id));
    const seen = new Set(shown);
    return [...shown, ...[...S.sel].filter(id => !seen.has(id))];
  };
  function pickCard(id, range) {
    if (range && S.anchor && S.anchor !== id) {
      const a = S.visibleIds.indexOf(S.anchor), b = S.visibleIds.indexOf(id);
      if (a >= 0 && b >= 0) { for (const x of S.visibleIds.slice(Math.min(a, b), Math.max(a, b) + 1)) S.sel.add(x); S.anchor = id; paintSelection(); return; }
    }
    if (S.sel.has(id)) S.sel.delete(id); else S.sel.add(id);
    S.anchor = id;
    paintSelection();
  }
  function selectAll() {
    if (!S.visibleIds.length) return;
    for (const id of S.visibleIds) S.sel.add(id);
    paintSelection();
  }
  function clearSelection() { S.sel.clear(); S.anchor = null; paintSelection(); }
  function paintSelection() {
    for (const id of [...S.sel]) if (!S.byId.has(id)) S.sel.delete(id); // removed by a scan
    for (const el of groups.querySelectorAll('.card')) {
      const on = S.sel.has(el.dataset.id);
      el.classList.toggle('selected', on);
      el.querySelector('.pick')?.setAttribute('aria-pressed', String(on));
    }
    view.classList.toggle('selecting', S.sel.size > 0);
    selBar.hidden = !S.sel.size;
    if (!S.sel.size) return;
    let dur = 0, size = 0;
    for (const id of S.sel) { const it = S.byId.get(id); dur += it.duration || 0; size += it.size; }
    selText.textContent = `${t('library.videosSelected', { count: S.sel.size })} · ${span(dur)} · ${bytes(size)}`;
  }
  async function selectionPlaylist() {
    const ids = selectedIds().slice(0, 500), q = `lib=${encodeURIComponent(S.lib)}&ids=${ids.map(encodeURIComponent).join(',')}`;
    if (!onThisComputer()) return copy(`${location.origin}/api/playlist.m3u8?${q}`, t('library.playlistLinkCopied'));
    try {
      const j = await post(`/api/playlist/save?${q}`, {});
      if (j.path) toast(j.expires ? t('library.savedPlaylistLinks', { count: j.count, path: j.path }) : t('library.savedPlaylist', { count: j.count, path: j.path }), { kind: 'ok', ms: 6000 });
    } catch (e) { toastError(t('library.couldNotSavePlaylist'), e); }
  }

  // ---------------------------------------------------------------- duplicates
  // Copies of one file: the same size to the byte and, where both lengths are known, the same length. A file whose
  // length is not known yet (no cover) goes with the copies of its size.
  function duplicateSets() {
    const bySize = new Map();
    for (const it of S.items) if (it.size > 0) { const l = bySize.get(it.size); if (l) l.push(it); else bySize.set(it.size, [it]); }
    const sets = [];
    for (const list of bySize.values()) {
      if (list.length < 2) continue;
      const byLen = new Map(), unknown = [];
      for (const it of list) {
        if (!it.duration) { unknown.push(it); continue; }
        const k = Math.round(it.duration);
        const near = [k, k - 1, k + 1].find(x => byLen.has(x));
        if (near != null) byLen.get(near).push(it); else byLen.set(k, [it]);
      }
      const groupsOf = [...byLen.values()].sort((a, b) => b.length - a.length);
      if (groupsOf.length) groupsOf[0].push(...unknown); else groupsOf.push(unknown);
      for (const g of groupsOf) if (g.length > 1) sets.push(g.sort((a, b) => addedAt(a).localeCompare(addedAt(b)) || collator.compare(a.key, b.key)));
    }
    return sets.sort((a, b) => b[0].size * (b.length - 1) - a[0].size * (a.length - 1));
  }
  function showDuplicates() {
    const sets = duplicateSets();
    const spare = sets.reduce((n, g) => n + g[0].size * (g.length - 1), 0);
    const row = it => h('li.dup-row',
      h('div.grow', h('div.dup-dir', it.dir || t('library.topLevel')), h('div.dup-name.muted', it.name, ' · ', t('library.addedAgoLower', { ago: ago(addedAt(it)) }))),
      h('div.fail-actions',
        h('button.btn.small', { type: 'button', onclick: () => play([it.id]) }, t('common.play')),
        canReveal() ? h('button.btn.small', { type: 'button', onclick: () => showInFolder(it) }, revealLabel())
          : S.info.type === 's3' ? h('button.btn.small', { type: 'button', onclick: () => { m.close(); navigate('storage', S.info.connection, S.info.bucket, ...it.key.split('/').slice(0, -1)); } }, t('library.showInStorage')) : null,
        h('button.btn.small', { type: 'button', onclick: () => { m.close(); openLibraryAction('reveal', { lib: S.lib, dir: it.dir, id: it.id }); } }, t('library.showInLibrary'))));
    const body = sets.length
      ? h('div', h('p.hint', t('library.duplicatesHint', { count: sets.length, size: bytes(spare) })),
        h('ul.dup-list', sets.slice(0, 300).map(g => h('li.dup-set',
          h('div.dup-head', h('strong', stem(g[0].name)), h('span.muted', t('library.copiesEach', { count: g.length, size: bytes(g[0].size) }) + (g.find(x => x.duration) ? ' · ' + clock(g.find(x => x.duration).duration) : ''))),
          h('ul.dup-files', g.map(row))))))
      : h('div.empty-state', icon('check2'), h('h3', t('library.noDuplicates')), h('p', t('library.noDuplicatesText')));
    const m = modal({ title: t('library.duplicates'), size: 'wide', body, actions: [{ label: t('common.close'), primary: true, value: false }] });
  }

  // ---------------------------------------------------------------- saved searches
  // Searches worth keeping (words, filters like dur>1h res>=2160, or both) as one-click chips above the covers. They
  // belong to this browser, like the sort and the type filter, and work in every library.
  const savedList = () => { const l = store.json('savedSearches', []); return Array.isArray(l) ? l.filter(x => typeof x === 'string') : []; };
  function toggleSaved() {
    const q = qInput.value.trim();
    if (!q) return;
    const list = savedList();
    store.setJson('savedSearches', list.includes(q) ? list.filter(x => x !== q) : [...list, q].slice(-20));
    if (!list.includes(q)) toast(t('library.searchSaved'), { kind: 'ok' });
    paintSaved();
  }
  function useSaved(q) {
    qInput.value = qInput.value.trim() === q ? '' : q;
    paintSaved();
    runSearch();
  }
  function paintSaved() {
    const q = qInput.value.trim(), list = savedList();
    saveBtn.hidden = !q;
    const on = list.includes(q);
    saveBtn.setAttribute('aria-pressed', String(on));
    saveBtn.setAttribute('aria-label', on ? t('library.removeThisSavedSearch') : t('library.saveThisSearch'));
    saveBtn.title = on ? t('library.removeThisSavedSearch') : t('library.saveThisSearch');
    savedRow.hidden = !list.length;
    fill(savedRow, list.map(x => h('span.chip', { role: 'listitem', class: x === q ? 'on' : '' },
      h('button.chip-label', { type: 'button', 'aria-pressed': String(x === q), title: x === q ? t('library.clearThisSearch') : t('library.searchFor', { q: x }), onclick: () => useSaved(x) }, icon('search', 'sm'), x),
      h('button.chip-x', { type: 'button', 'aria-label': t('library.removeSavedSearch', { q: x }), title: t('common.remove'), onclick: () => { store.setJson('savedSearches', savedList().filter(y => y !== x)); paintSaved(); } }, icon('x', 'sm')))));
  }

  // ---------------------------------------------------------------- type filter
  let typesMenu = null;
  const typeCounts = () => {
    const counts = new Map();
    for (const it of S.items) { const c = counts.get(it.ext) || { n: 0, kind: it.kind }; c.n++; counts.set(it.ext, c); }
    return [...counts].sort((a, b) => (a[1].kind === b[1].kind ? b[1].n - a[1].n : a[1].kind === 'video' ? -1 : 1));
  };
  function paintTypes() {
    const off = typeCounts().filter(([ext]) => S.hidden.has(ext)).length;
    $('.types-label', typesBtn).textContent = off ? t('library.typesHidden', { count: off }) : t('library.types');
    typesBtn.classList.toggle('on', off > 0);
  }
  function openTypes(anchor) {
    const rows = typeCounts();
    const boxes = new Map();
    const list = h('div', rows.map(([ext, c]) => {
      const cb = h('input', { type: 'checkbox', checked: !S.hidden.has(ext), onchange: () => { if (cb.checked) S.hidden.delete(ext); else S.hidden.add(ext); typesChanged(); sync(); } });
      boxes.set(ext, cb);
      return h('label.type-row', cb, h('span.type-name', ext ? '.' + ext : t('library.noExtension')), h('span.tag', { video: t('library.kindVideo'), audio: t('library.kindAudio') }[c.kind] || c.kind), h('span.count', num(c.n)));
    }));
    const all = h('button.btn.small', { type: 'button', onclick: () => { S.hidden.clear(); typesChanged(); sync(); } }, t('library.showAll'));
    const common = h('button.btn.small', { type: 'button', onclick: () => { S.hidden = defaultHidden(); typesChanged(); sync(); } }, t('library.commonVideoOnly'));
    const sync = () => { for (const [ext, cb] of boxes) cb.checked = !S.hidden.has(ext); all.disabled = !S.hidden.size; common.disabled = sameSet(S.hidden, defaultHidden()); };
    sync();
    typesMenu = showMenu({ anchor, items: [{ head: t('library.fileTypesShown') }, { node: list }, { node: h('div.menu-actions', common, all) }] });
  }
  function typesChanged() {
    store.set('hiddenTypes', JSON.stringify([...S.hidden]));
    rebuildView(true);
    paintTypes();
  }

  function openOptions(anchor) {
    const sizeSeg = h('div.seg', ['s', 'm', 'l'].map(sz => h('button', { type: 'button', dataset: { size: sz }, 'aria-pressed': String(store.get('size', 'm') === sz), onclick: () => { setSize(sz); for (const b of sizeSeg.children) b.setAttribute('aria-pressed', String(b.dataset.size === sz)); } }, { s: t('library.sizeSmall'), m: t('library.sizeMedium'), l: t('library.sizeLarge') }[sz])));
    const playerSel = h('select', { 'aria-label': t('library.playWith'), onchange: () => { S.player = playerSel.value; store.set('player', S.player); } },
      state.players.map(p => h('option', { value: p.id }, p.name)));
    playerSel.value = S.player || '';
    const showSeg = h('div.seg', [['folders', t('library.folders')], ['all', t('library.allVideos')]].map(([v, label]) => h('button', { type: 'button', 'aria-pressed': String(store.get('libDisplay', 'folders') === v),
      onclick: () => { store.set('libDisplay', v); for (const b of showSeg.children) b.setAttribute('aria-pressed', String(b.textContent === label)); render(); } }, label)));
    showMenu({ anchor, align: 'right', items: [
      { head: t('library.show') }, { node: showSeg }, { sep: true },
      { head: t('library.playWith') }, { node: playerSel }, { sep: true },
      { head: t('library.coverSize') }, { node: sizeSeg },
    ] });
  }
  function setSize(size) {
    document.documentElement.style.setProperty('--card-min', { s: '170px', m: '230px', l: '320px' }[size] || '230px');
    store.set('size', size);
  }

  // ---------------------------------------------------------------- hover scrub (delegated)
  let scrubbing = null;
  function stopScrub() {
    if (!scrubbing) return;
    const { cover, it } = scrubbing;
    cover.classList.remove('scrubbing');
    const img = $('img', cover);
    if (img) img.src = thumb(it, it.cover ?? 0);
    $$('.ticks i', cover).forEach(x => x.classList.remove('on'));
    scrubbing = null;
  }
  function onPointerMove(e) {
    if (e.pointerType !== 'mouse') return;
    const cover = e.target.closest('.cover');
    if (!cover) return stopScrub();
    const it = S.byId.get(cover.closest('.card').dataset.id);
    if (!it || !(it.frames > 1)) return; // no frames: the field is left out
    if (!scrubbing || scrubbing.cover !== cover) {
      stopScrub();
      scrubbing = { cover, it, idx: -1 };
      if (!cover.dataset.pre) { cover.dataset.pre = '1'; for (let i = 0; i < it.frames; i++) new Image().src = thumb(it, i); }
      cover.classList.add('scrubbing');
    }
    const r = cover.getBoundingClientRect();
    const idx = Math.min(it.frames - 1, Math.max(0, Math.floor((e.clientX - r.left) / r.width * it.frames)));
    if (idx !== scrubbing.idx) {
      scrubbing.idx = idx;
      $('img', cover).src = thumb(it, idx);
      $$('.ticks i', cover).forEach((x, j) => x.classList.toggle('on', j === idx));
    }
  }

  groups.addEventListener('click', e => {
    const crumb = e.target.closest('.gpath button');
    if (crumb) { if (crumb.dataset.path !== S.scope) go(crumb.dataset.path); return; }
    const cardEl = e.target.closest('.card');
    if (!cardEl) return;
    const it = S.byId.get(cardEl.dataset.id);
    if (e.target.closest('.pick')) pickCard(it.id, e.shiftKey);
    else if (e.target.closest('.cover')) {
      // Ctrl (⌘ on a Mac) or Shift picks instead of playing, and so does any click once something is picked.
      if (e.ctrlKey || e.metaKey || e.shiftKey || S.sel.size) pickCard(it.id, e.shiftKey);
      else play([it.id]);
    } else if (e.target.closest('.copy')) copyLink(it);
    else if (e.target.closest('.title')) showDetails(it);
  });
  groups.addEventListener('contextmenu', e => {
    const cardEl = e.target.closest('.card');
    if (!cardEl) return;
    const it = S.byId.get(cardEl.dataset.id);
    const many = S.sel.has(it.id) && S.sel.size > 1;
    contextMenu(e, [
      many ? { label: t('library.playSelected', { count: S.sel.size }), icon: 'play', onClick: () => play(selectedIds()) } : { label: t('common.play'), icon: 'play', onClick: () => play([it.id]) },
      { label: S.sel.has(it.id) ? t('library.deselect') : t('library.select'), icon: 'check', onClick: () => pickCard(it.id, false) },
      { label: t('library.details'), icon: 'info', onClick: () => showDetails(it) },
      canReveal() ? { label: revealLabel(), icon: 'folder', onClick: () => showInFolder(it) } : null,
      { label: linkLabel(), icon: 'link', onClick: () => copyLink(it) },
      S.info.type === 'local' && onThisComputer() ? null : { label: S.info.type === 'local' ? t('library.copyFilePath') : t('library.copyObjectKey'), icon: 'copy', onClick: () => copy(S.info.type === 'local' ? localPath(S.info.location, it.key) : it.key, S.info.type === 'local' ? t('library.filePathCopied') : t('library.keyCopied')) },
      S.info.type === 's3' ? { label: t('library.showInStorage'), icon: 'storage', onClick: () => navigate('storage', S.info.connection, S.info.bucket, ...it.key.split('/').slice(0, -1)) } : null,
    ].filter(Boolean));
  });
  groups.addEventListener('pointermove', onPointerMove);
  groups.addEventListener('pointerleave', stopScrub);

  qInput.addEventListener('input', debounce(runSearch, 120));
  qInput.addEventListener('input', () => paintSaved());
  sortSel.addEventListener('change', () => {
    S.sort = sortSel.value;
    S.sortAuto = false; // the person chose: stop switching to best match by itself
    if (S.sort !== 'rel') store.set('sort', S.sort);
    render();
  });
  scope.on(document, 'keydown', e => {
    if (e.key === '/' && !/^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName) && !document.querySelector('dialog[open]')) { e.preventDefault(); qInput.focus(); }
    if (e.key === 'Escape') { navOpen(false); if (S.sel.size && !document.querySelector('dialog[open]')) clearSelection(); }
    if ((e.key === 'a' || e.key === 'A') && (e.ctrlKey || e.metaKey) && !/^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName) && !document.querySelector('dialog[open]')) {
      e.preventDefault();
      selectAll();
    }
  });

  // ---------------------------------------------------------------- start
  setSize(store.get('size', 'm'));
  paintSaved();
  S.player = store.get('player', state.defaultPlayer);
  if (!state.players.some(p => p.id === S.player)) S.player = state.players[0]?.id;
  scope.add(on('libraries', paintSwitch));

  async function update(parts) {
    const [id, ...path] = parts;
    if (!state.libs.length) { view.hidden = true; noLibs.hidden = false; return; }
    view.hidden = false; noLibs.hidden = true;
    const wanted = state.libs.some(l => l.id === id) ? id : state.activeLib && state.libs.some(l => l.id === state.activeLib) ? state.activeLib : state.libs[0].id;
    if (wanted !== id) { replace('library', wanted, ...path); return; }
    try {
      if (wanted !== S.lib) { await switchLibrary(wanted, path.join('/')); paintSwitch(); }
      else applyScope(path.join('/'));
      consumeReveal();
    } catch (e) { toastError(t('library.couldNotLoad'), e); }
  }
  await update(parts);
  paintSwitch();
  // runRequested (a dialog or a reveal someone asked for) waits for ready(): the router calls it once this view is the
  // current one. A view that was superseded while it loaded is destroyed instead and must not take the request.
  return { update, ready: runRequested, destroy() { scope.dispose(); typesMenu && typesMenu.close(); document.title = 'Media Library'; } };
}
