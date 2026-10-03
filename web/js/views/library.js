// Library: indexed folders as a grid of keyframe covers, opened in your player.
import { h, fill, $, $$, Scope, debounce } from '../lib/dom.js';
import { icon, kindIcon } from '../lib/icons.js';
import { get, post, put } from '../lib/api.js';
import { bytes, clock, collator, extOf, leaf, num, plural, resLabel, span, stem, when } from '../lib/fmt.js';
import { href, navigate, replace } from '../lib/router.js';
import { loadLibraries, loadConnections, on, pokeWatcher, running, state, takeLibraryAction } from '../lib/state.js';
import { store } from '../lib/store.js';
import { confirmDialog, contextMenu, modal, showMenu, toast, toastError } from '../lib/ui.js';
import { locationPicker } from '../lib/picker.js';
import { editConnection } from './connections.js';

// Every extension the indexer picks up. Until the user changes the filter, only the common video ones show.
const KNOWN_TYPES = ['mp4', 'm4v', 'mov', 'mkv', 'webm', 'avi', 'wmv', 'flv', 'ts', 'm2ts', 'mp3', 'flac', 'm4a', 'aac', 'wav', 'ogg', 'opus'];
const COMMON_VIDEO = new Set(['mp4', 'm4v', 'mov', 'mkv', 'webm', 'avi', 'wmv']);
const defaultHidden = () => new Set(KNOWN_TYPES.filter(t => !COMMON_VIDEO.has(t)));
const sameSet = (a, b) => a.size === b.size && [...a].every(x => b.has(x));
const shuffled = list => { const a = [...list]; for (let i = a.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [a[i], a[j]] = [a[j], a[i]]; } return a; };
const inScope = (it, s) => !s || it.dir === s || it.dir.startsWith(s + '/');
const TYPE_LABEL = { local: 'Local folder', s3: 'S3 bucket', rclone: 'S3 via rclone' };
// A file's path on the computer running medialib, written the way that computer writes paths (D:\Videos\a.mp4 on
// Windows, /srv/videos/a.mp4 elsewhere), whatever computer the browser is on.
// A browser on the computer that serves the library (the desktop app, or one next to the server): only there can
// medialib open a folder on the screen.
const onThisComputer = () => ['127.0.0.1', 'localhost', '[::1]', '::1'].includes(location.hostname);
const revealLabel = () => ({ windows: 'Show in Explorer', darwin: 'Show in Finder' })[(state.system?.platform || '').split('/')[0]] || 'Show in file manager';
const localPath = (root, key) => {
  const sep = /^[A-Za-z]:|^\\\\|\\/.test(root) ? '\\' : '/';
  return root.replace(/[\\/]+$/, '') + sep + key.split('/').join(sep);
};

const itemSort = {
  rel: (a, b) => a.rank - b.rank,
  name: (a, b) => collator.compare(a.it.name, b.it.name),
  new: (a, b) => b.it.mtime.localeCompare(a.it.mtime) || collator.compare(a.it.name, b.it.name),
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
    lib: null, info: null, items: [], byId: new Map(), scope: '', q: '', sort: store.get('sort', 'name'),
    tree: null, nodes: new Map(), shown: [], hidden: defaultHidden(), visibleIds: [], player: null,
    match: null, rank: null, ranked: true, sortAuto: true, pendingReveal: null,
  };
  try { const saved = store.get('hiddenTypes', null); if (saved) S.hidden = new Set(JSON.parse(saved)); } catch { /* keep the default */ }

  const thumb = (it, i) => `/thumbs/${S.lib}/${it.id}-${it.ver}-${i}.avif`;
  const mediaUrl = it => `${location.origin}/media/${S.lib}/${it.id}/${encodeURIComponent(it.name)}`;

  // ---------------------------------------------------------------- skeleton
  const libBtn = h('button.btn.lib-switch', { type: 'button', 'aria-haspopup': 'menu', onclick: e => openSwitcher(e.currentTarget) });
  const qInput = h('input.input', { type: 'search', placeholder: 'Search names   ( / )', 'aria-label': 'Search names', autocomplete: 'off',
    title: 'Names, folders, codecs, pinyin; typos are forgiven.\nFilters, alone or with words: dur>1h  size<2g  date>=2024-05  res>=1080' });
  const sortSel = h('select', { 'aria-label': 'Sort' }, [['rel', 'Best match'], ['name', 'Name'], ['new', 'Newest'], ['size', 'Largest'], ['dur', 'Longest']].map(([v, t]) => h('option', { value: v }, t)));
  if (S.sort === 'rel') S.sort = 'name'; // "Best match" only means something while searching
  sortSel.value = S.sort;
  const typesBtn = h('button.btn', { type: 'button', 'aria-haspopup': 'true', onclick: e => openTypes(e.currentTarget) }, icon('filter', 'sm'), h('span.types-label', 'Types'));
  const optBtn = h('button.btn.icon-only', { type: 'button', 'aria-label': 'View options', title: 'Player and cover size', onclick: e => openOptions(e.currentTarget) }, icon('sliders', 'sm'));
  const ringText = h('span.ring-text');
  const ringBar = h('circle.ring-bar', { cx: 18, cy: 18, r: 15 });
  const ring = h('button.ring', { type: 'button', hidden: true, onclick: () => manageLibraries() },
    (() => { const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg'); s.setAttribute('viewBox', '0 0 36 36'); s.innerHTML = '<circle class="ring-track" cx="18" cy="18" r="15"/><circle class="ring-bar" cx="18" cy="18" r="15" pathLength="100" stroke-dasharray="0 100"/>'; return s; })(), ringText);
  const navToggle = h('button.icon-btn.nav-toggle', { type: 'button', 'aria-label': 'Folders', title: 'Show or hide folders', 'aria-expanded': 'true', onclick: () => toggleTree() }, icon('folder'));

  const tree = h('ul.tree');
  const nav = h('aside.tree-pane', { 'aria-label': 'Folders' },
    h('div.nav-head', h('span', 'Folders'), h('button.icon-btn', { type: 'button', 'aria-label': 'Close folders', onclick: () => toggleTree() }, icon('x'))), tree);
  const scrim = h('div.scrim', { hidden: true, onclick: () => navOpen(false) });

  const banner = h('div.banner.lib-banner', { hidden: true });
  const warnings = h('ul.warnings', { hidden: true });
  const crumbs = h('ol.crumbs');
  const stats = h('div.stats');
  const playAll = h('button.btn.primary', { type: 'button', onclick: () => play(S.visibleIds) }, icon('play', 'sm'), 'Play all');
  const shuffleAll = h('button.btn', { type: 'button', title: 'Play everything shown, in random order', onclick: () => play(shuffled(S.visibleIds)) }, icon('shuffle', 'sm'), 'Shuffle');
  const copyPl = h('button.btn', { type: 'button', onclick: () => copy(`${location.origin}/api/playlist.m3u8?lib=${encodeURIComponent(S.lib)}&dir=${encodeURIComponent(S.scope)}` + (S.hidden.size ? '&hide=' + encodeURIComponent([...S.hidden].join(',')) : ''), 'Playlist URL') }, icon('link', 'sm'), 'Copy playlist URL');
  const groups = h('div#groups');
  const empty = h('p.empty', { hidden: true }, 'Nothing here matches.');
  const main = h('section.lib-main', banner, warnings,
    h('div.scopebar', h('div.scope-text', crumbs, stats), h('div.actions', playAll, shuffleAll, copyPl)), groups, empty);

  const bar = h('header.lib-bar', navToggle, libBtn, h('div.search', icon('search', 'sm'), qInput), h('div.bar-spacer'), sortSel, typesBtn, optBtn, ring);
  const view = h('div.lib-view', bar, h('div.lib-body', nav, main), scrim);
  root.append(view);
  if (store.get('treeClosed', '0') === '1') { view.classList.add('tree-closed'); navToggle.setAttribute('aria-expanded', 'false'); }
  const noLibs = h('div.page', { hidden: true }, h('div.page-inner', h('div.card-box.empty-state', icon('library'), h('h3', 'Add your first library'),
    h('p', 'A library is a folder on a disk or NAS share, or a folder in an object-store bucket. medialib indexes it and shows keyframe covers you can click to play.'),
    h('div', { style: { display: 'flex', gap: '8px', flexWrap: 'wrap', justifyContent: 'center', marginTop: '8px' } },
      h('button.btn.primary', { type: 'button', onclick: () => addLibrary() }, icon('plus', 'sm'), 'Add a library')))));
  root.append(noLibs);

  // ---------------------------------------------------------------- actions
  async function play(ids) {
    if (!ids.length) return;
    if (ids.length > 500) toast('Playing the first 500 items');
    try {
      const j = await post('/api/play', { lib: S.lib, ids: ids.slice(0, 500), player: S.player });
      toast(j.count > 1 ? `Opening ${j.count} items in ${j.player}` : `Opening in ${j.player}`, { kind: 'ok' });
    } catch (e) {
      if (e.status === 403) { // a browser on another computer: the server cannot open a player on that screen
        const it = ids.length === 1 ? S.items.find(x => x.id === ids[0]) : null;
        window.open(it ? mediaUrl(it) : `/api/playlist.m3u8?lib=${encodeURIComponent(S.lib)}&ids=${ids.slice(0, 500).map(encodeURIComponent).join(',')}`, '_blank');
        toast(it ? 'Opening the stream in your browser' : 'Downloading a playlist for your player', { kind: 'ok' });
        return;
      }
      toastError('Could not start the player', e);
    }
  }
  async function copy(text, what) {
    try { await navigator.clipboard.writeText(text); toast(what + ' copied', { kind: 'ok' }); } catch { toast('Copy failed: ' + text, { kind: 'error' }); }
  }
  const filePath = it => S.info.type === 'local' ? localPath(S.info.location, it.key) : S.info.type === 's3' ? `s3://${S.info.bucket}/${it.key}` : it.key;
  const canReveal = () => S.info.type === 'local' && onThisComputer();
  async function showInFolder(it) {
    try { await post('/api/reveal', { lib: S.lib, id: it.id }); } catch (e) { toastError('Could not open the folder', e); }
  }

  // Everything known about one file: all its keyframes (arrow keys step through them), what the index says, where it is.
  function showDetails(it) {
    const frames = it.frames || 0;
    let cur = Math.min(it.cover ?? 0, Math.max(0, frames - 1));
    const big = frames ? h('img', { alt: '', src: thumb(it, cur) })
      : h('div.det-ph', icon(it.kind === 'audio' ? 'music' : 'film', 'lg'), h('span', it.indexed === false && !it.error ? 'Not indexed yet' : 'No preview'));
    const strip = frames > 1 ? h('div.det-strip', Array.from({ length: frames }, (_, i) =>
      h('button', { type: 'button', 'aria-label': `Keyframe ${i + 1} of ${frames}`, 'aria-pressed': String(i === cur), onclick: () => pick(i) },
        h('img', { alt: '', loading: 'lazy', src: thumb(it, i) })))) : null;
    const pick = i => {
      cur = (i + frames) % frames;
      big.src = thumb(it, cur);
      for (const [j, b] of [...strip.children].entries()) b.setAttribute('aria-pressed', String(j === cur));
    };
    const row = (k, v) => (v == null || v === '' ? null : [h('dt', k), h('dd', v)]);
    const res = resLabel(it.width, it.height);
    const status = it.error ? 'Failed: ' + it.error : it.indexed === false ? 'Not indexed yet'
      : plural(frames, 'keyframe') + (it.note ? ' (read with ffmpeg)' : '');
    const meta = h('dl.det-meta',
      row('Folder', h('button.linkish', { type: 'button', title: 'Show this folder', onclick: () => { m.close(); go(it.dir); } }, it.dir || 'Top level')),
      row('Size', h('span', { title: num(it.size) + ' bytes' }, bytes(it.size))),
      row('Length', it.duration ? clock(it.duration) : null),
      row('Picture', it.width ? `${it.width} × ${it.height}` + (res ? ' · ' + res : '') + (it.fps ? ` · ${it.fps} fps` : '') : null),
      row('Codec', it.codec ? it.codec + (it.kind === 'video' ? (it.audio ? ' · with sound' : ' · no sound') : '') : null),
      row(S.info.type === 'local' ? 'Modified' : 'Uploaded', h('span', { title: it.mtime }, when(it.mtime))),
      row(S.info.type === 'local' ? 'File' : 'Object', h('code.det-path', filePath(it))),
      row('Index', status));
    const m = modal({
      title: it.name, size: 'wide', body: h('div.details', h('div.det-view', big, strip), meta),
      actions: [
        canReveal() ? { label: revealLabel(), left: true, keepOpen: true, onClick: () => showInFolder(it) } : null,
        { label: S.info.type === 'local' ? 'Copy path' : 'Copy key', keepOpen: true, onClick: () => copy(S.info.type === 'local' ? filePath(it) : it.key, 'Path') },
        { label: 'Copy stream URL', keepOpen: true, onClick: () => copy(mediaUrl(it), 'Stream URL') },
        { label: 'Play', primary: true, onClick: () => play([it.id]) },
      ].filter(Boolean),
    });
    m.el.addEventListener('keydown', e => {
      if (frames < 2 || e.target.closest('input, textarea')) return;
      if (e.key === 'ArrowRight') { e.preventDefault(); pick(cur + 1); } else if (e.key === 'ArrowLeft') { e.preventDefault(); pick(cur - 1); }
    });
  }

  // ---------------------------------------------------------------- folder tree
  function buildTree(items) {
    const rootNode = { name: 'All media', path: '', kids: new Map(), count: 0, depth: -1 };
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
      tw = h('button.twist', { type: 'button', 'aria-expanded': 'false', 'aria-label': 'Expand ' + node.name, onclick: () => (node.open ? collapse(node) : expand(node)) }, icon('chevron-right'));
    } else {
      tw = h('button.twist.leaf', { type: 'button', tabindex: -1, 'aria-hidden': 'true' });
    }
    const b = h('button.node', { type: 'button', class: node.depth < 0 ? 'all' : '', title: node.path || 'All media', onclick: () => { go(node.path); navOpen(false); } },
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
    const art = h('article.card', { dataset: { id: it.id } });
    const cover = h('button.cover', { type: 'button', 'aria-label': 'Play ' + it.name });
    const dated = (S.info.type === 'local' ? 'modified ' : 'uploaded ') + it.mtime.slice(0, 10);
    cover.title = [it.name, it.dir, [resLabel(it.width, it.height), it.codec, it.fps && it.fps + ' fps'].filter(Boolean).join(' · '), bytes(it.size) + ' · ' + dated].filter(Boolean).join('\n');
    if (it.frames) {
      cover.append(h('img', { alt: '', loading: 'lazy', decoding: 'async', src: thumb(it, it.cover ?? 0) }));
    } else {
      cover.append(h('div.ph', icon(it.kind === 'audio' ? 'music' : 'film', 'lg'),
        h('span', it.kind === 'audio' ? (it.codec || 'audio').toUpperCase() : it.indexed === false && !it.error ? 'Not indexed yet' : 'No preview')));
    }
    const res = resLabel(it.width, it.height);
    if (res) cover.append(h('span.badge.tl', res));
    if (it.duration) cover.append(h('span.badge.br', clock(it.duration)));
    if (it.frames > 1) cover.append(h('span.ticks', Array.from({ length: it.frames }, () => h('i'))));
    const hint = h('span.playhint'); hint.innerHTML = PLAY_HINT; cover.append(hint);
    const title = h('div.title', { title: it.name + '\nClick for details' }, stem(it.name));
    const sub = h('div.sub', [entry.sub, bytes(it.size)].filter(Boolean).join(' · '));
    const cp = h('button.copy', { type: 'button', title: 'Copy stream URL', 'aria-label': 'Copy stream URL for ' + it.name }, icon('copy', 'sm'));
    art.append(cover, h('div.meta', title, cp, sub));
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
      toast(it && S.hidden.has(it.ext) ? 'That file is hidden by the type filter (Types menu)' : 'Could not find that file in this view');
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
      if (it.mtime > g.newest) g.newest = it.mtime;
    }
    const frag = document.createDocumentFragment();
    S.visibleIds = [];
    const how = effectiveSort();
    for (const g of [...grp.values()].sort(groupSort[how])) {
      g.entries.sort(itemSort[how]);
      const ids = g.entries.map(e => e.it.id);
      S.visibleIds.push(...ids);
      const t = groupPath(s, g.key);
      const pb = h('button.btn.small', { type: 'button', 'aria-label': 'Play all in ' + t.title, onclick: () => play(ids) }, icon('play', 'sm'), 'Play');
      // A group without a key holds the files that sit directly in the folder being viewed.
      frag.append(h('section.group', h('header.ghead', t, h('span.gstats', `${g.key ? '' : 'files directly here · '}${g.entries.length} · ${span(g.dur)} · ${bytes(g.size)}`), pb),
        makeGrid(g.entries)));
    }
    pathIO.disconnect();
    onScreen.clear();
    groups.replaceChildren(frag);
    for (const box of $$('.gpath', groups)) pathIO.observe(box);
    empty.hidden = list.length > 0 || !S.items.length;

    const ps = s ? s.split('/') : [];
    fill(crumbs, [['All media', ''], ...ps.map((p, i) => [p, ps.slice(0, i + 1).join('/')])].map(([name, path], i, arr) => {
      const b = h('button', { type: 'button' }, name);
      if (i < arr.length - 1) b.addEventListener('click', () => go(path)); else b.setAttribute('aria-current', 'page');
      return h('li', b);
    }));
    const total = list.reduce((a, it) => a + (it.duration || 0), 0), size = list.reduce((a, it) => a + it.size, 0);
    const filtered = S.hidden.size ? S.items.filter(it => inScope(it, s) && S.hidden.has(it.ext)).length : 0;
    stats.textContent = `${num(list.length)} items · ${span(total)} · ${bytes(size)}` + (S.q.trim() ? ` · matching “${S.q.trim()}”` : '') + (filtered ? ` · ${num(filtered)} hidden by type` : '');
    document.title = (s ? leaf(s) + ' · ' : '') + (S.info ? S.info.name + ' · ' : '') + 'Media Library';
  }

  // Group titles: the full folder path, every folder from the top of the library down to the group. When it does
  // not fit, whole folders are left out starting right after the first one, so the top folder and the ones nearest
  // to the content stay readable.
  function groupPath(sc, key) {
    const segs = [...(sc ? sc.split('/') : []), ...(key ? [key] : [])];
    const box = h('div.gpath');
    box._parts = segs.length ? segs.map((name, i) => ({ name, path: segs.slice(0, i + 1).join('/') })) : [{ name: 'Top-level files', path: '' }];
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
    if (!box.clientWidth) return;
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
    S.items = lib.items;
    for (const it of lib.items) it.ext = extOf(it.name);
    S.byId = new Map(lib.items.map(it => [it.id, it]));
    rebuildView(keepView);
    paintTypes();
    paintBanner();
    paintSwitch();
    if (S.q.trim()) runSearch(); // files may have arrived
  }
  function rebuildView(keepView, scopePath = S.scope) {
    S.shown = S.hidden.size ? S.items.filter(it => !S.hidden.has(it.ext)) : S.items;
    const { root: r, nodes } = buildTree(S.shown);
    S.tree = r;
    S.nodes = nodes;
    renderTree();
    if (keepView && nodes.has(S.scope)) { markActive(S.scope); render(); } else applyScope(scopePath);
  }

  const paintSwitch = () => {
    const lib = state.libs.find(l => l.id === S.lib);
    fill(libBtn, icon(lib && lib.type === 'local' ? 'folder' : 'cloud', 'sm'), h('span.lib-switch-name', lib ? lib.name : 'Library'), icon('chevron-down', 'sm'));
  };

  async function switchLibrary(id, path = '') {
    S.lib = id;
    S.q = '';
    S.match = S.rank = null;
    S.sortAuto = true;
    searchSeq++;
    qInput.value = '';
    S.scope = path;
    post('/api/libraries/active', { id }).catch(() => {});
    groups.classList.add('busy');
    try { await loadLibrary(); } finally { groups.classList.remove('busy'); }
  }

  // ---------------------------------------------------------------- library switcher + manager
  function openSwitcher(anchor) {
    showMenu({
      anchor, items: [
        { head: 'Libraries' },
        ...state.libs.map(l => ({ label: l.name, icon: l.type === 'local' ? 'folder' : 'cloud', check: l.id === S.lib, onClick: () => navigate('library', l.id) })),
        { sep: true },
        { label: 'Add a library…', icon: 'plus', onClick: () => addLibrary() },
        { label: 'Manage libraries…', icon: 'settings', onClick: () => manageLibraries() },
      ],
    });
  }

  function manageLibraries() {
    const list = h('ul.lib-list');
    const m = modal({ title: 'Libraries', size: 'wide', body: list, actions: [{ label: 'Add a library…', left: true, keepOpen: true, onClick: () => { m.close(); addLibrary(); return false; } }, { label: 'Done', primary: true, value: true }] });
    const paint = () => fill(list, state.libs.map(l => {
      const job = state.jobs[l.id];
      const meta = h('div.lib-meta');
      if (running(job)) {
        meta.append(job.state === 'waiting' ? 'Waiting for another indexer to finish…' : job.state === 'listing' ? 'Scanning…' : `Indexing ${num(job.done)} of ${num(job.total)}`,
          h('div.meter', h('i', { style: { width: (job.state === 'indexing' && job.total ? job.done / job.total * 100 : 0) + '%' } })));
      } else {
        meta.textContent = job && job.state === 'error' ? 'Indexing failed: ' + job.line : l.type === 'local' && !l.reachable ? 'Folder not reachable'
          : l.type === 's3' && !l.reachable ? 'Its connection was removed' : l.updated ? `${num(l.items)} items · indexed ${when(l.updated)}` : 'Not indexed yet';
      }
      const busy = running(job);
      return h('li.lib', h('div.lib-main',
        h('div.lib-name', l.name, h('span.tag', TYPE_LABEL[l.type] || l.type), l.type === 's3' && l.connection_name ? h('span.tag', l.connection_name) : null, l.id === S.lib ? h('span.tag.accent', 'Open') : null),
        h('div.lib-loc.mono', l.location), meta),
        h('div.lib-actions',
          h('button.btn.small', { type: 'button', disabled: l.id === S.lib, onclick: () => { m.close(); navigate('library', l.id); } }, 'Open'),
          h('button.btn.small', { type: 'button', disabled: busy, onclick: () => startIndex(l.id) }, l.updated ? 'Update index' : 'Index'),
          h('button.icon-btn.small', { type: 'button', 'aria-label': 'More', onclick: e => showMenu({ anchor: e.currentTarget, align: 'right', items: [
            { label: 'Edit…', icon: 'edit', disabled: busy, onClick: () => editLibrary(l) },
            { label: 'Re-index everything', icon: 'refresh', disabled: busy, onClick: () => startIndex(l.id, true) },
            l.convertible ? { label: 'Read directly over S3 (no rclone)', icon: 'cloud', disabled: busy, onClick: () => convert(l) } : null,
            { sep: true },
            { label: 'Remove', icon: 'trash', danger: true, disabled: busy || state.libs.length < 2, onClick: () => removeLibrary(l) },
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
        const b = e.currentTarget; b.disabled = true; b.textContent = 'Pick in the dialog…';
        try { const j = await post('/api/pick-folder', {}); if (j.path) pathIn.value = j.path; } catch (x) { err.textContent = x.message; err.hidden = false; }
        b.disabled = false; b.textContent = 'Browse…';
      } }, 'Browse…');
      where = h('div.field', h('label', 'Folder'), h('div.input-wrap', pathIn, browse));
      getBody = () => ({ path: pathIn.value });
    } else if (l.type === 's3') {
      let loc = { conn: l.connection, bucket: l.bucket, prefix: l.prefix };
      const picker = locationPicker({ conn: l.connection, bucket: l.bucket, prefix: l.prefix, onChange: v => { loc = v; } });
      where = h('div.field', h('div.label', 'Location'), picker.el);
      getBody = () => ({ connection: loc.conn, bucket: loc.bucket, prefix: loc.prefix });
    } else {
      where = h('p.hint', 'This library reads through an rclone remote, so only its name can change. Use “Read directly over S3” to make its location editable.');
    }
    const m = modal({
      title: 'Edit library', size: l.type === 's3' ? 'wide' : '',
      body: h('div', err, h('div.field', h('label', 'Name'), nameIn), where,
        l.type === 'rclone' ? null : h('p.hint', 'Changing the location re-indexes the library. Covers of files that are still there are reused only if the file keeps its path.')),
      actions: [{ label: 'Cancel', value: false }, { label: 'Save', primary: true, keepOpen: true, onClick: async api => {
        err.hidden = true;
        try {
          const j = await post('/api/libraries/update', { id: l.id, name: nameIn.value, ...getBody() });
          await loadLibraries();
          api.close(true);
          if (l.id === S.lib) { paintSwitch(); loadLibrary(); }
          if (j.moved) { toast('Location changed. Indexing…', { kind: 'ok' }); pokeWatcher(); }
        } catch (e) { err.textContent = e.message; err.hidden = false; }
        return false;
      } }],
    });
    nameIn.select();
  }
  async function convert(l) {
    if (!await confirmDialog({ title: 'Read this library over S3 directly?', message: `“${l.name}” will use the stored S3 credentials instead of starting an rclone process for each request.`, detail: 'Its index and covers stay as they are.', confirm: 'Switch' })) return;
    try { await post('/api/libraries/convert', { id: l.id }); await Promise.all([loadLibraries(), loadConnections()]); toast('Now reading over S3', { kind: 'ok' }); if (l.id === S.lib) loadLibrary(true); } catch (e) { toastError('Could not switch', e); }
  }
  async function removeLibrary(l) {
    if (!await confirmDialog({ title: 'Remove library?', message: `“${l.name}” will be removed from the list.`, detail: 'Its index and thumbnails are deleted. Your media files are not touched.', confirm: 'Remove', danger: true })) return;
    try {
      const j = await post('/api/libraries/remove', { id: l.id });
      await loadLibraries();
      if (l.id === S.lib) navigate('library', j.active);
    } catch (e) { toastError('Could not remove', e); }
  }
  async function startIndex(id, force = false) {
    try { state.jobs[id] = await post('/api/index', { id, force }); paintBanner(); pokeWatcher(); } catch (e) { toastError('Could not start indexing', e); }
  }

  // ---------------------------------------------------------------- add a library
  function addLibrary() {
    let mode = state.connections.length ? store.get('addMode', 'local') : 'local';
    const pathIn = h('input.input', { placeholder: 'D:\\Videos   or   \\\\nas\\media\\videos', autocomplete: 'off', spellcheck: 'false' });
    const nameIn = h('input.input', { placeholder: 'Name (optional)', autocomplete: 'off' });
    const err = h('p.form-error', { hidden: true });
    const browse = h('button.btn', { type: 'button', onclick: async e => {
      const b = e.currentTarget; b.disabled = true; b.textContent = 'Pick in the dialog…';
      try { const j = await post('/api/pick-folder', {}); if (j.path) { pathIn.value = j.path; err.hidden = true; } } catch (x) { showErr(x.message); }
      b.disabled = false; b.textContent = 'Browse…';
    } }, 'Browse…');
    const localPane = h('div', h('p.hint', 'A folder on a disk or a NAS share. Subfolders are included.'),
      h('div.field', h('label', 'Folder'), h('div.input-wrap', pathIn, browse)), h('div.field', h('label', 'Name'), nameIn));
    let loc = { conn: '', bucket: '', prefix: '' };
    const s3Name = h('input.input', { placeholder: 'Name (optional)', autocomplete: 'off' });
    const picker = state.connections.length ? locationPicker({ onChange: v => { loc = v; } }) : null;
    const s3Pane = h('div', picker ? [h('p.hint', 'Pick a bucket, then the folder that holds your media. Indexing reads it straight through the S3 API.'), picker.el,
      h('div.field', { style: { marginTop: '14px' } }, h('label', 'Name'), s3Name)]
      : h('div.empty-state', icon('plug'), h('h3', 'No connection yet'), h('p', 'Connect to RustFS, MinIO, S3 or another store first.'),
        h('button.btn.primary', { type: 'button', onclick: async () => { const c = await editConnection(); if (c) { m.close(); addLibrary(); } } }, icon('plus', 'sm'), 'Add connection')));
    const tabs = h('div.seg', { role: 'tablist' }, [['local', 'Local folder'], ['s3', 'Bucket (S3)']].map(([id, t]) =>
      h('button', { type: 'button', role: 'tab', dataset: { id }, 'aria-selected': String(id === mode), onclick: () => setMode(id) }, t)));
    const panes = h('div', localPane, s3Pane);
    const setMode = id => {
      mode = id; store.set('addMode', id);
      for (const b of tabs.children) b.setAttribute('aria-selected', String(b.dataset.id === id));
      localPane.hidden = id !== 'local'; s3Pane.hidden = id !== 's3'; err.hidden = true;
    };
    const showErr = msg => { err.textContent = msg; err.hidden = false; };
    setMode(mode);
    const m = modal({
      title: 'Add a library', body: h('div', h('div', { style: { marginBottom: '14px' } }, tabs), err, panes),
      actions: [{ label: 'Cancel', value: false }, { label: 'Add and index', primary: true, keepOpen: true, onClick: async api => {
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
    if (running(job)) {
      if (job.state === 'waiting') msg = job.line;  // progress itself lives in the ring
    } else if (job && job.state === 'error') { msg = 'Indexing failed: ' + job.line; act = 'Retry'; kind = 'error'; }
    else if (info.type === 'local' && !info.reachable) { msg = `Folder not reachable: ${info.location}. Covers are shown from the last index; playback needs the folder back.`; kind = 'warn'; }
    else if (info.type === 's3' && !info.reachable) { msg = 'The connection this library used was removed. Covers are shown from the last index.'; kind = 'warn'; }
    else if (!S.items.length) { msg = info.updated ? 'No media files were found in this library.' : 'This library has not been indexed yet.'; act = info.updated ? 'Scan again' : 'Index now'; }
    else if (pending) { msg = `${num(pending)} of ${num(S.items.length)} files have no preview yet.`; act = 'Index now'; }
    banner.hidden = !msg;
    banner.className = 'banner lib-banner ' + kind;
    fill(banner, h('div.grow', msg), act ? h('button.btn', { type: 'button', onclick: () => startIndex(S.lib) }, act) : null);
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
    const text = job.state === 'waiting' ? `Waiting to index ${name}` : job.state === 'listing' ? `Scanning ${name}`
      : `Indexing ${name}: ${pct}%, ${num(job.done)} of ${num(job.total)} files` + (job.errors ? `, ${num(job.errors)} errors` : '');
    ring.title = text;
    ring.setAttribute('aria-label', text + '. Open libraries');
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
      loadLibrary(true).catch(() => {});
    }
    if (anyFinished) loadLibraries().catch(() => {});  // a finished run changes a library's item count and indexed time
    paintBanner();
    if (finished && now.state === 'done') toast('Indexing finished', { kind: 'ok' });
  }));

  // ---------------------------------------------------------------- type filter
  let typesMenu = null;
  const typeCounts = () => {
    const counts = new Map();
    for (const it of S.items) { const c = counts.get(it.ext) || { n: 0, kind: it.kind }; c.n++; counts.set(it.ext, c); }
    return [...counts].sort((a, b) => (a[1].kind === b[1].kind ? b[1].n - a[1].n : a[1].kind === 'video' ? -1 : 1));
  };
  function paintTypes() {
    const off = typeCounts().filter(([ext]) => S.hidden.has(ext)).length;
    $('.types-label', typesBtn).textContent = off ? `Types · ${off} hidden` : 'Types';
    typesBtn.classList.toggle('on', off > 0);
  }
  function openTypes(anchor) {
    const rows = typeCounts();
    const boxes = new Map();
    const list = h('div', rows.map(([ext, c]) => {
      const cb = h('input', { type: 'checkbox', checked: !S.hidden.has(ext), onchange: () => { if (cb.checked) S.hidden.delete(ext); else S.hidden.add(ext); typesChanged(); sync(); } });
      boxes.set(ext, cb);
      return h('label.type-row', cb, h('span.type-name', ext ? '.' + ext : '(no extension)'), h('span.tag', c.kind), h('span.count', num(c.n)));
    }));
    const all = h('button.btn.small', { type: 'button', onclick: () => { S.hidden.clear(); typesChanged(); sync(); } }, 'Show all');
    const common = h('button.btn.small', { type: 'button', onclick: () => { S.hidden = defaultHidden(); typesChanged(); sync(); } }, 'Common video only');
    const sync = () => { for (const [ext, cb] of boxes) cb.checked = !S.hidden.has(ext); all.disabled = !S.hidden.size; common.disabled = sameSet(S.hidden, defaultHidden()); };
    sync();
    typesMenu = showMenu({ anchor, items: [{ head: 'File types shown' }, { node: list }, { node: h('div.menu-actions', common, all) }] });
  }
  function typesChanged() {
    store.set('hiddenTypes', JSON.stringify([...S.hidden]));
    rebuildView(true);
    paintTypes();
  }

  function openOptions(anchor) {
    const sizeSeg = h('div.seg', ['s', 'm', 'l'].map(sz => h('button', { type: 'button', dataset: { size: sz }, 'aria-pressed': String(store.get('size', 'm') === sz), onclick: () => { setSize(sz); for (const b of sizeSeg.children) b.setAttribute('aria-pressed', String(b.dataset.size === sz)); } }, { s: 'Small', m: 'Medium', l: 'Large' }[sz])));
    const playerSel = h('select', { 'aria-label': 'Play with', onchange: () => { S.player = playerSel.value; store.set('player', S.player); } },
      state.players.map(p => h('option', { value: p.id }, p.name)));
    playerSel.value = S.player || '';
    showMenu({ anchor, align: 'right', items: [
      { head: 'Play with' }, { node: playerSel }, { sep: true },
      { head: 'Cover size' }, { node: sizeSeg },
      { sep: true },
      { label: 'Re-scan this library', icon: 'refresh', onClick: () => startIndex(S.lib) },
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
    $$('.ticks i', cover).forEach(t => t.classList.remove('on'));
    scrubbing = null;
  }
  function onPointerMove(e) {
    if (e.pointerType !== 'mouse') return;
    const cover = e.target.closest('.cover');
    if (!cover) return stopScrub();
    const it = S.byId.get(cover.closest('.card').dataset.id);
    if (!it || it.frames < 2) return;
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
      $$('.ticks i', cover).forEach((t, j) => t.classList.toggle('on', j === idx));
    }
  }

  groups.addEventListener('click', e => {
    const crumb = e.target.closest('.gpath button');
    if (crumb) { if (crumb.dataset.path !== S.scope) go(crumb.dataset.path); return; }
    const cardEl = e.target.closest('.card');
    if (!cardEl) return;
    const it = S.byId.get(cardEl.dataset.id);
    if (e.target.closest('.cover')) play([it.id]);
    else if (e.target.closest('.copy')) copy(mediaUrl(it), 'Stream URL');
    else if (e.target.closest('.title')) showDetails(it);
  });
  groups.addEventListener('contextmenu', e => {
    const cardEl = e.target.closest('.card');
    if (!cardEl) return;
    const it = S.byId.get(cardEl.dataset.id);
    contextMenu(e, [
      { label: 'Play', icon: 'play', onClick: () => play([it.id]) },
      { label: 'Details…', icon: 'info', onClick: () => showDetails(it) },
      canReveal() ? { label: revealLabel(), icon: 'folder', onClick: () => showInFolder(it) } : null,
      { label: 'Copy stream URL', icon: 'link', onClick: () => copy(mediaUrl(it), 'Stream URL') },
      { label: S.info.type === 'local' ? 'Copy file path' : 'Copy object key', icon: 'copy', onClick: () => copy(S.info.type === 'local' ? localPath(S.info.location, it.key) : it.key, 'Path') },
      S.info.type === 's3' ? { label: 'Show in Storage', icon: 'storage', onClick: () => navigate('storage', S.info.connection, S.info.bucket, ...it.key.split('/').slice(0, -1)) } : null,
    ].filter(Boolean));
  });
  groups.addEventListener('pointermove', onPointerMove);
  groups.addEventListener('pointerleave', stopScrub);

  qInput.addEventListener('input', debounce(runSearch, 120));
  sortSel.addEventListener('change', () => {
    S.sort = sortSel.value;
    S.sortAuto = false; // the person chose: stop switching to best match by itself
    if (S.sort !== 'rel') store.set('sort', S.sort);
    render();
  });
  scope.on(document, 'keydown', e => {
    if (e.key === '/' && !/^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName) && !document.querySelector('dialog[open]')) { e.preventDefault(); qInput.focus(); }
    if (e.key === 'Escape') navOpen(false);
  });

  // ---------------------------------------------------------------- start
  setSize(store.get('size', 'm'));
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
    } catch (e) { toastError('Could not load the library', e); }
  }
  await update(parts);
  paintSwitch();
  // runRequested (a dialog or a reveal someone asked for) waits for ready(): the router calls it once this view is the
  // current one. A view that was superseded while it loaded is destroyed instead and must not take the request.
  return { update, ready: runRequested, destroy() { scope.dispose(); typesMenu && typesMenu.close(); document.title = 'Media Library'; } };
}
