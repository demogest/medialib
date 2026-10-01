// Storage: browse, preview, upload, move, rename and delete objects in any S3-compatible bucket.
import { h, fill, $, Scope, debounce } from '../lib/dom.js';
import { icon, kindIcon } from '../lib/icons.js';
import { get, objectUrl, post, s3Path, del } from '../lib/api.js';
import { bytes, collator, kindOf, leaf, num, plural, when } from '../lib/fmt.js';
import { navigate } from '../lib/router.js';
import { loadConnections, loadLibraries, on, pokeWatcher, state } from '../lib/state.js';
import { store } from '../lib/store.js';
import { confirmDialog, contextMenu, modal, promptDialog, showMenu, toast, toastError } from '../lib/ui.js';
import { enqueue, filesFromDrop } from '../lib/uploads.js';
import { editConnection } from './connections.js';
import { copyText, incompleteUploadsDialog, libraryDialog, linkItems, preview, propertiesDialog, transferDialog } from './storage-parts.js';

const MEDIA_KINDS = new Set(['video', 'audio']);
const PAGE = 500;

export async function mount(root, parts) {
  const scope = new Scope();
  const S = {
    conn: '', bucket: '', prefix: '', rows: [], token: null, loading: false, error: null, loaded: false,
    sel: new Set(), anchor: null, sort: store.json('stSort', { col: 'name', dir: 1 }), layout: store.get('stLayout', 'list'),
    filter: '', search: null, drawer: store.get('stDrawer', matchMedia('(min-width: 1280px)').matches ? '1' : '0') === '1',
    buckets: new Map(), bucketState: new Map(), heads: new Map(), measure: new Map(), reported: new Set(), gen: 0,
  };

  // ---------------------------------------------------------------- skeleton
  const side = h('aside.st-side');
  const crumbs = h('nav.st-crumbs', { 'aria-label': 'Location' });
  const filterIn = h('input.input', { type: 'search', placeholder: 'Filter files', title: 'Type to filter this folder. Press Enter to search subfolders too.', 'aria-label': 'Filter or search', autocomplete: 'off' });
  const fileInput = h('input', { type: 'file', multiple: true, hidden: true });
  const dirInput = h('input', { type: 'file', webkitdirectory: true, hidden: true });
  const uploadBtn = h('button.btn.primary', { type: 'button', onclick: e => showMenu({ anchor: e.currentTarget, items: [
    { label: 'Upload files…', icon: 'upload', onClick: () => fileInput.click() },
    { label: 'Upload a folder…', icon: 'folder', onClick: () => dirInput.click() },
  ] }) }, icon('upload', 'sm'), h('span.lbl', 'Upload'));
  const newFolderBtn = h('button.btn#newfolder', { type: 'button', onclick: () => newFolder() }, icon('folder-plus', 'sm'), h('span.lbl', 'New folder'));
  const refreshBtn = h('button.icon-btn', { type: 'button', 'aria-label': 'Refresh', title: 'Refresh', onclick: () => reload() }, icon('refresh'));
  const layoutBtn = h('button.icon-btn', { type: 'button', onclick: () => { S.layout = S.layout === 'list' ? 'grid' : 'list'; store.set('stLayout', S.layout); paintAll(); } });
  const detailBtn = h('button.icon-btn', { type: 'button', 'aria-label': 'Details', title: 'Details', onclick: () => { S.drawer = !S.drawer; store.set('stDrawer', S.drawer ? '1' : '0'); paintAll(); } }, icon('info'));
  const moreBtn = h('button.icon-btn', { type: 'button', 'aria-label': 'More', onclick: e => showMenu({ anchor: e.currentTarget, align: 'right', items: moreItems() }) }, icon('more'));
  const sideToggle = h('button.icon-btn.side-toggle', { type: 'button', 'aria-label': 'Buckets', onclick: () => sideOpen(true) }, icon('menu'));
  const bar = h('header.st-bar', sideToggle, crumbs, h('div.search.st-filter', icon('search', 'sm'), filterIn), newFolderBtn, uploadBtn, refreshBtn, layoutBtn, detailBtn, moreBtn);
  const selbar = h('div.st-selbar', { hidden: true });
  const body = h('div.st-scroll', { tabindex: 0, 'aria-label': 'Objects' });
  const detail = h('aside.st-detail');
  const drop = h('div.st-drop', { hidden: true }, h('div', icon('upload', 'lg'), h('strong', 'Drop to upload'), h('span.muted.dest')));
  const scrim = h('div.scrim', { hidden: true, onclick: () => sideOpen(false) });
  const main = h('section.st-main', bar, selbar, body, drop);
  root.append(h('div.st-view', side, main, detail, scrim, fileInput, dirInput));
  const sideOpen = open => { side.classList.toggle('open', open); scrim.hidden = !open; };

  // ---------------------------------------------------------------- helpers
  const connName = id => state.connections.find(c => c.id === id)?.name || id;
  const base = () => ({ conn: S.conn, bucket: S.bucket });
  const here = (prefix = S.prefix) => navigate('storage', S.conn, S.bucket, ...prefix.split('/').filter(Boolean));
  const rowKey = r => r.key;
  const byKey = k => S.rows.find(r => r.key === k) || S.search?.rows.find(r => r.key === k);
  const selected = () => [...S.sel].map(byKey).filter(Boolean);

  const toRow = {
    folder: p => ({ type: 'folder', key: p, name: p.slice(S.prefix.length).replace(/\/$/, ''), size: null, mtime: '' }),
    file: o => ({ type: 'file', key: o.key, name: o.key.slice(S.prefix.length), size: o.size, mtime: o.mtime, storage_class: o.storage_class, etag: o.etag, kind: kindOf(o.key) }),
  };

  // ---------------------------------------------------------------- loading
  async function ensureBuckets(conn, force = false) {
    if (!force && S.buckets.has(conn)) return S.buckets.get(conn);
    S.bucketState.set(conn, 'loading');
    paintSide();
    try {
      const j = await get(`/api/s3/${encodeURIComponent(conn)}/buckets`);
      S.buckets.set(conn, j.buckets);
      S.bucketState.set(conn, j.limited ? 'limited' : 'ok');
    } catch (e) {
      S.buckets.set(conn, []);
      S.bucketState.set(conn, e.message);
    }
    paintSide();
    return S.buckets.get(conn);
  }

  async function load(reset = true) {
    if (!S.bucket) return;
    const mine = reset ? ++S.gen : S.gen;
    if (reset) { S.rows = []; S.token = null; S.error = null; S.loaded = false; S.heads.clear(); }
    S.loading = true;
    paintBody();
    try {
      const j = await get(s3Path(S.conn, S.bucket, 'list', { prefix: S.prefix, token: S.token, limit: PAGE }));
      if (mine !== S.gen) return;
      S.rows.push(...j.prefixes.map(toRow.folder), ...j.objects.map(toRow.file));
      S.token = j.next;
      S.loaded = true;
    } catch (e) {
      if (mine !== S.gen) return;
      S.error = e;
    }
    S.loading = false;
    S.sel = new Set([...S.sel].filter(k => byKey(k)));
    paintAll();
  }
  const reload = () => { S.search = null; load(true); };
  const softReload = debounce(() => { if (S.bucket && !S.search) load(true); }, 700);

  // ---------------------------------------------------------------- routing
  async function update(p) {
    const [conn = '', bucket = '', ...path] = p;
    const prefix = path.length ? path.join('/') + '/' : '';
    if (!conn) {  // no connection in the address: go where you were last, or to the first connection
      const saved = store.get('stLastPath', '').split('/').filter(Boolean).map(decodeURIComponent);
      const target = state.connections.some(c => c.id === saved[0]) ? saved : state.connections[0] ? [state.connections[0].id] : null;
      if (target) { navigate('storage', ...target); return; }
    }
    const changed = conn !== S.conn || bucket !== S.bucket || prefix !== S.prefix;
    S.conn = conn; S.bucket = bucket; S.prefix = prefix;
    if (conn) store.set('stLastPath', [conn, bucket, ...path].filter(Boolean).map(encodeURIComponent).join('/'));
    sideOpen(false);
    if (changed) {
      S.sel = new Set(); S.search = null; S.filter = ''; filterIn.value = ''; S.rows = []; S.token = null; S.error = null; S.loaded = false;
      S.gen++;
    }
    paintAll();
    if (conn && !state.connections.some(c => c.id === conn)) return;
    if (conn) ensureBuckets(conn).then(() => { if (!bucket) paintBody(); });
    if (changed && bucket) load(true);
  }

  // ---------------------------------------------------------------- painting
  function paintAll() {
    paintSide();
    paintBar();
    paintBody();
    paintSel();
    paintDetail();
    document.title = (S.bucket ? `${S.bucket} · ` : '') + 'Storage · Media Library';
  }

  function paintSide() {
    fill(side,
      h('div.side-head', h('span', 'Storage'), h('button.icon-btn.small', { type: 'button', 'aria-label': 'Refresh buckets', title: 'Refresh buckets', onclick: () => { for (const c of state.connections) ensureBuckets(c.id, true); } }, icon('refresh', 'sm')),
        h('button.icon-btn.small.side-close', { type: 'button', 'aria-label': 'Close', onclick: () => sideOpen(false) }, icon('x', 'sm'))),
      state.connections.length ? state.connections.map(c => {
        const open = c.id === S.conn;
        const st = S.bucketState.get(c.id);
        return h('div.side-conn', { class: open ? 'open' : '' },
          h('button.side-conn-btn', { type: 'button', onclick: () => { if (!S.buckets.has(c.id)) ensureBuckets(c.id); navigate('storage', c.id); } },
            icon(open ? 'chevron-down' : 'chevron-right', 'sm'), icon('server', 'sm'), h('span.grow', c.name)),
          open ? h('div.side-buckets',
            st === 'loading' ? h('div.side-note', h('div.spinner')) : null,
            (S.buckets.get(c.id) || []).map(b => h('button.side-bucket', { type: 'button', 'aria-current': b.name === S.bucket ? 'true' : null, onclick: () => navigate('storage', c.id, b.name) }, icon('bucket', 'sm'), h('span.grow', b.name))),
            st && !['ok', 'loading', 'limited'].includes(st) ? h('div.side-note.bad', st) : null,
            st === 'ok' && !(S.buckets.get(c.id) || []).length ? h('div.side-note', 'No buckets yet.') : null,
            h('button.side-bucket.add', { type: 'button', onclick: () => newBucket(c.id) }, icon('plus', 'sm'), 'New bucket')) : null);
      }) : h('div.side-note', 'No connections yet.'),
      h('div.side-foot', h('button.btn.small', { type: 'button', onclick: () => navigate('connections') }, icon('plug', 'sm'), 'Connections')));
  }

  // Breadcrumbs: every level is a link. When they do not fit, the earliest levels fold into a "…" menu.
  let crumbItems = [], folded = 0;
  function paintBar() {
    const parts = S.prefix.split('/').filter(Boolean);
    crumbItems = [];
    if (S.conn) crumbItems.push({ label: connName(S.conn), go: () => navigate('storage', S.conn) });
    if (S.bucket) crumbItems.push({ label: S.bucket, go: () => navigate('storage', S.conn, S.bucket) });
    parts.forEach((p, i) => crumbItems.push({ label: p, go: () => here(parts.slice(0, i + 1).join('/') + '/') }));
    folded = 0;
    fitCrumbs();
    const inBucket = !!S.bucket;
    for (const b of [newFolderBtn, uploadBtn, refreshBtn, layoutBtn, detailBtn]) b.hidden = !inBucket;
    $('.st-filter', bar).hidden = !inBucket;
    moreBtn.hidden = !S.conn;
    fill(layoutBtn, icon(S.layout === 'list' ? 'grid' : 'list'));
    layoutBtn.setAttribute('aria-label', S.layout === 'list' ? 'Grid view' : 'List view');
    layoutBtn.title = layoutBtn.getAttribute('aria-label');
    detailBtn.classList.toggle('on', S.drawer);
    $('.dest', drop).textContent = inBucket ? `${S.bucket}/${S.prefix}` : '';
  }

  function drawCrumbs() {
    const last = crumbItems.length - 1;
    const piece = (it, i) => h('button.crumb', { type: 'button', 'aria-current': i === last ? 'page' : null, onclick: it.go }, it.label);
    const hidden = crumbItems.slice(0, folded);
    fill(crumbs, S.conn ? null : h('span.crumb', 'Storage'),
      hidden.length ? [h('button.crumb', { type: 'button', 'aria-label': 'Earlier levels', onclick: e => showMenu({ anchor: e.currentTarget, items: hidden.map(it => ({ label: it.label, icon: 'folder', onClick: it.go })) }) }, '…'), h('span.sep', '›')] : null,
      crumbItems.slice(folded).map((it, i) => [i ? h('span.sep', '›') : null, piece(it, folded + i)]));
  }
  function fitCrumbs() {
    if (!crumbItems.length) return;
    folded = 0;
    drawCrumbs();
    while (crumbs.scrollWidth > crumbs.clientWidth + 1 && folded < crumbItems.length - 1) { folded++; drawCrumbs(); }
  }
  const crumbRO = new ResizeObserver(() => fitCrumbs());
  crumbRO.observe(crumbs);
  scope.add(() => crumbRO.disconnect());

  const sortFns = {
    name: (a, b) => collator.compare(a.name, b.name),
    size: (a, b) => (a.size || 0) - (b.size || 0),
    mtime: (a, b) => (a.mtime || '').localeCompare(b.mtime || ''),
  };
  function visibleRows() {
    const q = S.filter.trim().toLowerCase();
    let rows = S.search ? S.search.rows : S.rows;
    if (q && !S.search) rows = rows.filter(r => r.name.toLowerCase().includes(q));
    const f = sortFns[S.sort.col], d = S.sort.dir;
    return [...rows].sort((a, b) => (a.type === 'folder' ? 0 : 1) - (b.type === 'folder' ? 0 : 1) || f(a, b) * d || collator.compare(a.name, b.name));
  }

  let rowEls = new Map(), shownRows = [];
  function paintBody() {
    rowEls = new Map();
    if (!S.conn) return fill(body, connectionsLanding());
    if (!state.connections.some(c => c.id === S.conn)) return fill(body, emptyState('alert', 'Unknown connection', 'That connection no longer exists.'));
    if (!S.bucket) return fill(body, bucketsLanding());
    if (S.error) return fill(body, h('div.empty-state', icon('alert'), h('h3', 'Could not list this location'), h('p', S.error.message),
      h('div', { style: { display: 'flex', gap: '8px' } }, h('button.btn', { type: 'button', onclick: () => reload() }, icon('refresh', 'sm'), 'Try again'), h('button.btn', { type: 'button', onclick: () => navigate('connections') }, 'Check connection'))));
    shownRows = visibleRows();
    if (!S.loaded) return fill(body, h('div.empty-state', h('div.spinner')));
    const searching = S.search ? h('div.banner.st-search', h('div.grow', `${plural(S.search.rows.length, 'match', 'matches')} for “${S.search.q}” in ${S.bucket}/${S.prefix}` + (S.search.truncated ? ` · stopped after ${num(S.search.scanned)} objects` : '')),
      h('button.btn.small', { type: 'button', onclick: () => { S.search = null; S.filter = ''; filterIn.value = ''; paintAll(); } }, 'Clear search')) : null;
    if (!shownRows.length) {
      return fill(body, searching, S.filter || S.search ? emptyState('search', 'No matches', 'Nothing here has that name. Press Enter in the filter box to search subfolders too.')
        : h('div.empty-state', icon('folder'), h('h3', 'This folder is empty'), h('p', 'Drop files anywhere here, or use Upload.'),
          h('button.btn.primary', { type: 'button', onclick: () => fileInput.click() }, icon('upload', 'sm'), 'Upload files')));
    }
    const more = S.token && !S.search ? h('div.st-more', h('button.btn', { type: 'button', disabled: S.loading, onclick: () => loadMore() }, S.loading ? 'Loading…' : `Load more (${num(S.rows.length)} loaded)`)) : null;
    fill(body, searching, S.layout === 'list' ? listView(shownRows) : gridView(shownRows), more);
    if (more) { moreObserver.disconnect(); moreObserver.observe(more); }
  }
  const moreObserver = new IntersectionObserver(es => { if (es.some(e => e.isIntersecting) && S.token && !S.loading) loadMore(); }, { root: body, rootMargin: '300px' });
  scope.add(() => moreObserver.disconnect());
  async function loadMore() { if (!S.token || S.loading) return; await load(false); }

  const emptyState = (ic, title, text) => h('div.empty-state', icon(ic), h('h3', title), h('p', text));

  function connectionsLanding() {
    if (!state.connections.length) {
      return h('div.empty-state', icon('storage'), h('h3', 'Connect to an object store'),
        h('p', 'Add the endpoint and keys of RustFS, MinIO, Amazon S3, Cloudflare R2 or any S3-compatible service to browse and manage its buckets here.'),
        h('button.btn.primary', { type: 'button', onclick: async () => { const c = await editConnection(); if (c) { await loadConnections(); navigate('storage', c.id); } } }, icon('plus', 'sm'), 'Add connection'));
    }
    return h('div.empty-state', icon('storage'), h('h3', 'Choose a connection'));
  }

  function bucketsLanding() {
    const list = S.buckets.get(S.conn), st = S.bucketState.get(S.conn);
    if (!list && st !== 'loading' && !st) return h('div.empty-state', h('div.spinner'));
    if (st === 'loading' && !list) return h('div.empty-state', h('div.spinner'));
    if (st && !['ok', 'limited', 'loading'].includes(st)) {
      return h('div.empty-state', icon('alert'), h('h3', `Could not reach ${connName(S.conn)}`), h('p', st),
        h('div', { style: { display: 'flex', gap: '8px' } }, h('button.btn', { type: 'button', onclick: () => ensureBuckets(S.conn, true).then(paintBody) }, icon('refresh', 'sm'), 'Try again'),
          h('button.btn', { type: 'button', onclick: () => navigate('connections') }, 'Edit connection')));
    }
    return h('div.st-buckets',
      h('div.st-buckets-head', h('h2', 'Buckets'), h('button.btn', { type: 'button', onclick: () => newBucket(S.conn) }, icon('plus', 'sm'), 'New bucket')),
      st === 'limited' ? h('p.muted', 'This key cannot list all buckets, so only the default bucket of the connection is shown.') : null,
      (list || []).length ? h('div.bucket-grid', list.map(b => h('button.bucket-card', { type: 'button', onclick: () => navigate('storage', S.conn, b.name) }, icon('bucket', 'lg'), h('div.bucket-name', b.name), h('div.muted', b.created ? 'Created ' + b.created.slice(0, 10) : '')))) : emptyState('bucket', 'No buckets yet', 'Create one to start storing objects.'));
  }

  // ---- list view
  function listView(rows) {
    const th = (col, label, cls = '') => h('th', { class: `sortable ${cls}`, 'aria-sort': S.sort.col === col ? (S.sort.dir === 1 ? 'ascending' : 'descending') : null,
      onclick: () => { S.sort = { col, dir: S.sort.col === col ? -S.sort.dir : (col === 'mtime' ? -1 : 1) }; store.setJson('stSort', S.sort); paintBody(); } },
      label, S.sort.col === col ? h('span.arrow', S.sort.dir === 1 ? '↑' : '↓') : null);
    const allOn = rows.length && rows.every(r => S.sel.has(r.key));
    const head = h('tr', h('th.chk', h('input', { type: 'checkbox', 'aria-label': 'Select all', checked: allOn, onchange: e => { if (e.target.checked) rows.forEach(r => S.sel.add(r.key)); else S.sel.clear(); paintSelection(); } })),
      th('name', 'Name'), th('size', 'Size', 'num'), th('mtime', 'Modified', 'mtime'));
    const tbody = h('tbody', rows.map(r => {
      const tr = h('tr', { dataset: { key: r.key }, class: S.sel.has(r.key) ? 'selected' : '' },
        h('td.chk', h('input', { type: 'checkbox', tabindex: -1, 'aria-label': 'Select ' + r.name, checked: S.sel.has(r.key) })),
        h('td.nm', h('div.nm-in', icon(r.type === 'folder' ? 'folder' : kindIcon(r.kind), 'sm'), h('span.nm-text', { title: r.key }, r.name))),
        h('td.num', r.size == null ? '–' : bytes(r.size)), h('td.mtime', r.mtime ? when(r.mtime) : ''));
      rowEls.set(r.key, tr);
      return tr;
    }));
    return h('table.table.st-table', h('thead', head), tbody);
  }

  // ---- grid view
  function gridView(rows) {
    const grid = h('div.st-grid', rows.map(r => {
      const imgOk = r.type === 'file' && r.kind === 'image' && r.size <= 5 * 1024 * 1024;
      const tile = h('div.tile', { dataset: { key: r.key }, class: S.sel.has(r.key) ? 'selected' : '' },
        h('div.tile-art', imgOk ? h('img', { src: objectUrl(S.conn, S.bucket, r.key), alt: '', loading: 'lazy' }) : icon(r.type === 'folder' ? 'folder' : kindIcon(r.kind), 'lg')),
        h('div.tile-name', { title: r.key }, r.name), h('div.tile-sub', r.size == null ? 'Folder' : bytes(r.size)));
      rowEls.set(r.key, tile);
      return tile;
    }));
    return grid;
  }

  // ---------------------------------------------------------------- selection
  function paintSelection() {
    for (const [k, el] of rowEls) {
      const on = S.sel.has(k);
      el.classList.toggle('selected', on);
      const cb = el.querySelector('input[type=checkbox]');
      if (cb) cb.checked = on;
    }
    paintSel();
    paintDetail();
  }

  function paintSel() {
    const rows = selected();
    selbar.hidden = !rows.length;
    if (!rows.length) return;
    const files = rows.filter(r => r.type === 'file'), size = files.reduce((a, r) => a + r.size, 0);
    fill(selbar, h('span.sel-count', `${plural(rows.length, 'item')} selected${files.length ? ' · ' + bytes(size) : ''}`),
      h('div.sel-actions', actionButtons(rows)), h('button.btn.ghost.small', { type: 'button', onclick: () => { S.sel.clear(); paintSelection(); } }, 'Clear'));
  }

  function actionButtons(rows) {
    const one = rows.length === 1 ? rows[0] : null;
    const files = rows.filter(r => r.type === 'file');
    const b = (label, ic, fn, cls = '') => h('button.btn.small', { type: 'button', class: cls, onclick: fn }, icon(ic, 'sm'), h('span.lbl', label));
    return [
      files.length ? b('Download', 'download', () => download(files)) : null,
      one && one.type === 'file' ? h('button.btn.small', { type: 'button', onclick: e => showMenu({ anchor: e.currentTarget, items: linkItems(S.conn, S.bucket, one.key) }) }, icon('link', 'sm'), h('span.lbl', 'Copy link')) : null,
      files.some(r => MEDIA_KINDS.has(r.kind)) ? b('Play', 'play', () => playInPlayer(files.filter(r => MEDIA_KINDS.has(r.kind)))) : null,
      one ? b('Rename', 'edit', () => rename(one)) : null,
      b('Move', 'move', () => transfer(rows, true)), b('Copy', 'copy', () => transfer(rows, false)),
      b('Delete', 'trash', () => remove(rows), 'danger'),
    ];
  }

  // click / keyboard selection
  body.addEventListener('click', e => {
    const el = e.target.closest('[data-key]');
    if (!el) { if (e.target === body || e.target.closest('.st-more') === null && !e.target.closest('button, a, input')) { if (S.sel.size) { S.sel.clear(); paintSelection(); } } return; }
    const key = el.dataset.key;
    const order = shownRows.map(r => r.key);
    if (e.shiftKey && S.anchor && order.includes(S.anchor)) {
      const a = order.indexOf(S.anchor), b = order.indexOf(key);
      S.sel = new Set(order.slice(Math.min(a, b), Math.max(a, b) + 1));
    } else if (e.ctrlKey || e.metaKey || e.target.matches('input[type=checkbox]')) {
      if (S.sel.has(key)) S.sel.delete(key); else S.sel.add(key);
      S.anchor = key;
    } else {
      S.sel = new Set([key]);
      S.anchor = key;
    }
    paintSelection();
  });
  body.addEventListener('dblclick', e => {
    const el = e.target.closest('[data-key]');
    if (!el) return;
    openRow(byKey(el.dataset.key));
  });
  body.addEventListener('contextmenu', e => {
    const el = e.target.closest('[data-key]');
    if (!el) return;
    if (!S.sel.has(el.dataset.key)) { S.sel = new Set([el.dataset.key]); S.anchor = el.dataset.key; paintSelection(); }
    contextMenu(e, menuFor(selected()));
  });
  body.addEventListener('keydown', e => {
    if (e.target.matches('input, select, textarea')) return;
    const order = shownRows.map(r => r.key);
    const cur = order.indexOf([...S.sel].pop());
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      const next = order[Math.max(0, Math.min(order.length - 1, (cur < 0 ? (e.key === 'ArrowDown' ? -1 : order.length) : cur) + (e.key === 'ArrowDown' ? 1 : -1)))];
      if (!next) return;
      if (e.shiftKey && S.anchor) { const a = order.indexOf(S.anchor), b = order.indexOf(next); S.sel = new Set(order.slice(Math.min(a, b), Math.max(a, b) + 1)); } else { S.sel = new Set([next]); S.anchor = next; }
      paintSelection();
      rowEls.get(next)?.scrollIntoView({ block: 'nearest' });
    } else if (e.key === 'Enter' && S.sel.size === 1) { openRow(selected()[0]); }
    else if (e.key === 'Delete' && S.sel.size) { remove(selected()); }
    else if (e.key === 'F2' && S.sel.size === 1) { rename(selected()[0]); }
    else if ((e.key === 'a' || e.key === 'A') && (e.ctrlKey || e.metaKey)) { e.preventDefault(); S.sel = new Set(order); paintSelection(); }
    else if (e.key === 'Backspace' && S.prefix) { e.preventDefault(); here(S.prefix.replace(/[^/]+\/$/, '')); }
    else if (e.key === 'Escape' && S.sel.size) { S.sel.clear(); paintSelection(); }
  });

  function openRow(r) {
    if (!r) return;
    if (r.type === 'folder') { here(r.key); return; }
    if (!S.drawer) { S.drawer = true; store.set('stDrawer', '1'); paintAll(); }
  }

  function menuFor(rows) {
    const one = rows.length === 1 ? rows[0] : null, files = rows.filter(r => r.type === 'file'), media = files.filter(r => MEDIA_KINDS.has(r.kind));
    return [
      one && one.type === 'folder' ? { label: 'Open', icon: 'folder', onClick: () => here(one.key) } : null,
      one && one.type === 'file' ? { label: 'Details & preview', icon: 'info', onClick: () => openRow(one) } : null,
      S.search && one ? { label: 'Show in folder', icon: 'folder', onClick: () => { const p = one.key.slice(0, one.key.lastIndexOf('/') + 1); S.search = null; here(p); } } : null,
      files.length ? { label: files.length > 1 ? `Download ${files.length} files` : 'Download', icon: 'download', onClick: () => download(files) } : null,
      media.length ? { label: 'Play in player', icon: 'play', onClick: () => playInPlayer(media) } : null,
      { sep: true },
      one && one.type === 'file' ? { head: 'Copy link' } : null,
      ...(one && one.type === 'file' ? linkItems(S.conn, S.bucket, one.key).slice(1) : []),
      { sep: true },
      one ? { label: 'Rename', icon: 'edit', kb: 'F2', onClick: () => rename(one) } : null,
      { label: 'Move to…', icon: 'move', onClick: () => transfer(rows, true) },
      { label: 'Copy to…', icon: 'copy', onClick: () => transfer(rows, false) },
      one && one.type === 'file' ? { label: 'Properties…', icon: 'sliders', onClick: () => properties(one) } : null,
      one && one.type === 'folder' ? { label: 'Calculate size', icon: 'layers', onClick: () => measure(one.key) } : null,
      one && one.type === 'folder' ? { label: 'Use as a library…', icon: 'library', onClick: () => makeLibrary(one.key) } : null,
      { sep: true },
      { label: 'Delete', icon: 'trash', kb: 'Del', danger: true, onClick: () => remove(rows) },
    ].filter(Boolean);
  }

  // ---------------------------------------------------------------- actions
  async function download(files) {
    if (files.length > 1 && !await confirmDialog({ title: `Download ${files.length} files?`, message: 'Your browser may ask to allow multiple downloads.', confirm: 'Download' })) return;
    files.forEach((f, i) => setTimeout(() => {
      const a = h('a', { href: objectUrl(S.conn, S.bucket, f.key, true), download: f.name.split('/').pop() });
      document.body.append(a); a.click(); a.remove();
    }, i * 450));
  }

  async function playInPlayer(rows) {
    try {
      const j = await post(s3Path(S.conn, S.bucket, 'play'), { keys: rows.map(r => r.key), player: store.get('player', state.defaultPlayer) });
      toast(j.count > 1 ? `Opening ${j.count} items in ${j.player}` : `Opening in ${j.player}`, { kind: 'ok' });
    } catch (e) { toastError('Could not start the player', e); }
  }

  async function newFolder() {
    const name = await promptDialog({ title: 'New folder', label: 'Folder name', confirm: 'Create', hint: 'S3 has no real folders: this creates an empty marker so the folder shows up before it has files.',
      validate: v => !v ? 'Enter a name.' : v.includes('//') ? 'Use single slashes.' : '' });
    if (!name) return;
    try { await post(s3Path(S.conn, S.bucket, 'folder'), { prefix: S.prefix + name }); toast('Folder created', { kind: 'ok' }); reload(); } catch (e) { toastError('Could not create the folder', e); }
  }

  async function newBucket(conn) {
    const name = await promptDialog({ title: 'New bucket', label: 'Bucket name', confirm: 'Create', mono: true, hint: '3 to 63 characters: lowercase letters, digits, dots and hyphens.',
      validate: v => /^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/.test(v) ? '' : 'Use 3 to 63 lowercase letters, digits, dots or hyphens.' });
    if (!name) return;
    try { await post(`/api/s3/${encodeURIComponent(conn)}/buckets`, { name }); await ensureBuckets(conn, true); navigate('storage', conn, name); toast(`Bucket “${name}” created`, { kind: 'ok' }); } catch (e) { toastError('Could not create the bucket', e); }
  }

  async function deleteBucket() {
    if (!await confirmDialog({ title: `Delete bucket “${S.bucket}”?`, message: 'The bucket must be empty. This cannot be undone.', confirm: 'Delete bucket', danger: true })) return;
    try { await del(`/api/s3/${encodeURIComponent(S.conn)}/buckets/${encodeURIComponent(S.bucket)}`); const c = S.conn; await ensureBuckets(c, true); navigate('storage', c); toast('Bucket deleted', { kind: 'ok' }); } catch (e) { toastError('Could not delete the bucket', e); }
  }

  async function rename(r) {
    const name = await promptDialog({ title: `Rename ${r.type === 'folder' ? 'folder' : 'file'}`, label: 'New name', value: r.name.split('/').pop(), confirm: 'Rename',
      validate: v => !v ? 'Enter a name.' : v.includes('/') ? 'A name cannot contain “/”. Use Move to change folders.' : '' });
    if (!name || name === r.name.split('/').pop()) return;
    const parent = r.key.slice(0, r.key.replace(/\/$/, '').lastIndexOf('/') + 1);
    try {
      const j = await post(s3Path(S.conn, S.bucket, 'transfer'), { items: [{ from: r.key, to: parent + name + (r.type === 'folder' ? '/' : '') }], move: true, skip_existing: true });
      pokeWatcher();
      finishTask(j.task, 'Renamed');
    } catch (e) { toastError('Could not rename', e); }
  }

  async function transfer(rows, move) {
    const task = await transferDialog({ conn: S.conn, bucket: S.bucket, prefix: S.prefix, rows, move });
    if (task) finishTask(task, move ? 'Moved' : 'Copied');
  }

  async function remove(rows) {
    const files = rows.filter(r => r.type === 'file'), folders = rows.filter(r => r.type === 'folder');
    const what = [files.length ? plural(files.length, 'file') : '', folders.length ? plural(folders.length, 'folder') : ''].filter(Boolean).join(' and ');
    const ok = await confirmDialog({
      title: `Delete ${what}?`, danger: true, confirm: 'Delete',
      message: rows.length === 1 ? `“${rows[0].name}” will be deleted from ${S.bucket}.` : `${what} will be deleted from ${S.bucket}.`,
      detail: (folders.length ? 'Folders are deleted together with everything inside them. ' : '') + 'This cannot be undone' + (' unless the bucket keeps versions.'),
    });
    if (!ok) return;
    try {
      const j = await post(s3Path(S.conn, S.bucket, 'delete'), { keys: files.map(r => r.key), prefixes: folders.map(r => r.key) });
      pokeWatcher();
      finishTask(j.task, 'Deleted');
    } catch (e) { toastError('Could not delete', e); }
  }

  function finishTask(task, verb) {
    if (task.state === 'running') { toast(`${verb.replace(/d$/, 'ing').replace(/ied$/, 'ying')}… see Activity for progress`); return; }
    S.reported.add(task.id);
    if (task.error_count) toast(`${verb} with ${plural(task.error_count, 'problem')}. See Activity.`, { kind: 'error' });
    else toast(`${verb} ${plural(task.done, 'object')}`, { kind: 'ok' });
    reload();
  }
  scope.add(on('task-finished', t => {
    if (!S.reported.has(t.id) && ['copy', 'move', 'delete'].includes(t.kind)) {
      S.reported.add(t.id);
      toast(t.error_count ? `${t.title}: ${plural(t.error_count, 'problem')}` : `${t.title}: done`, { kind: t.error_count ? 'error' : 'ok' });
      softReload();
    }
    if (t.kind === 'size') paintDetail();
  }));
  scope.add(on('activity', () => { if ([...S.measure.values()].some(m => m.state === 'running')) paintDetail(); }));
  scope.add(on('uploaded', d => { if (d.conn === S.conn && d.bucket === S.bucket && d.key.startsWith(S.prefix)) softReload(); }));

  async function properties(r) {
    try {
      const head = await get(s3Path(S.conn, S.bucket, 'head', { key: r.key }));
      const out = await propertiesDialog(S.conn, S.bucket, r.key, head);
      if (out) { S.heads.set(r.key, out); toast('Properties saved', { kind: 'ok' }); paintDetail(); }
    } catch (e) { toastError('Could not read the properties', e); }
  }

  async function measure(prefix) {
    try {
      const j = await post(s3Path(S.conn, S.bucket, 'measure'), { prefix });
      S.measure.set(prefix, j.task);
      pokeWatcher();
      paintDetail();
    } catch (e) { toastError('Could not measure', e); }
  }
  const measureState = prefix => {
    const m = S.measure.get(prefix);
    if (!m) return null;
    return state.tasks.find(t => t.id === m.id) || m;
  };

  async function makeLibrary(prefix) {
    const lib = await libraryDialog({ conn: S.conn, bucket: S.bucket, prefix });
    if (lib) { await loadLibraries(); toast('Library added. Indexing…', { kind: 'ok' }); navigate('library', lib.id); }
  }

  function moreItems() {
    return [
      S.bucket ? { label: 'Calculate size of this folder', icon: 'layers', onClick: () => { S.sel.clear(); paintSelection(); measure(S.prefix); S.drawer = true; paintAll(); } } : null,
      S.bucket ? { label: 'Use this folder as a library…', icon: 'library', onClick: () => makeLibrary(S.prefix) } : null,
      S.bucket ? { label: 'Unfinished uploads…', icon: 'upload', onClick: () => incompleteUploadsDialog(S.conn, S.bucket) } : null,
      S.bucket ? { sep: true } : null,
      { label: 'New bucket…', icon: 'plus', onClick: () => newBucket(S.conn) },
      S.bucket && !S.prefix ? { label: 'Delete this bucket…', icon: 'trash', danger: true, onClick: () => deleteBucket() } : null,
      { label: 'Edit connection…', icon: 'plug', onClick: () => { const c = state.connections.find(x => x.id === S.conn); c && editConnection(c).then(saved => saved && (S.buckets.delete(S.conn), update([S.conn, S.bucket, ...S.prefix.split('/').filter(Boolean)]))); } },
    ].filter(Boolean);
  }

  // ---------------------------------------------------------------- search / filter
  filterIn.addEventListener('input', () => { S.filter = filterIn.value; if (S.search) S.search = null; paintBody(); });
  filterIn.addEventListener('keydown', async e => {
    if (e.key === 'Escape') { filterIn.value = ''; S.filter = ''; S.search = null; paintBody(); return; }
    if (e.key !== 'Enter' || !filterIn.value.trim()) return;
    const q = filterIn.value.trim();
    S.loaded = false; paintBody();
    try {
      const j = await get(s3Path(S.conn, S.bucket, 'search', { prefix: S.prefix, q }));
      S.search = { q, rows: j.objects.map(toRow.file), truncated: j.truncated, scanned: j.scanned };
      S.filter = '';
      S.sel.clear();
    } catch (err) { toastError('Search failed', err); }
    S.loaded = true;
    paintAll();
  });
  scope.on(document, 'keydown', e => {
    if (e.key === '/' && !/^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName) && S.bucket && !document.querySelector('dialog[open]')) { e.preventDefault(); filterIn.focus(); }
  });

  // ---------------------------------------------------------------- upload (picker and drag & drop)
  async function startUpload(entries) {
    if (!S.bucket || !entries.length) return;
    const names = new Set(S.rows.filter(r => r.type === 'file').map(r => r.name));
    const clash = entries.filter(e => !e.path.includes('/') && names.has(e.path));
    let replace = false;
    if (clash.length) {
      const m = modal({
        title: `${plural(clash.length, 'file')} already exist${clash.length === 1 ? 's' : ''}`, size: 'narrow',
        body: h('div', h('p', clash.slice(0, 4).map(e => e.path).join(', ') + (clash.length > 4 ? ` and ${clash.length - 4} more` : '')), h('p.muted', 'Replace them with the new files, or keep the existing ones?')),
        actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Keep existing', value: 'skip' }, { label: 'Replace', primary: true, value: 'replace' }],
      });
      const choice = await m.closed;
      if (!choice || choice === 'cancel') return;
      replace = choice === 'replace';
    }
    const clashed = new Set(clash.map(e => e.path));
    enqueue(S.conn, S.bucket, entries.map(e => ({ file: e.file, key: S.prefix + e.path, overwrite: replace && clashed.has(e.path) })));
    toast(`Uploading ${plural(entries.length, 'file')} to ${S.bucket}/${S.prefix}`);
  }
  fileInput.addEventListener('change', () => { startUpload([...fileInput.files].map(file => ({ file, path: file.name }))); fileInput.value = ''; });
  dirInput.addEventListener('change', () => { startUpload([...dirInput.files].map(file => ({ file, path: file.webkitRelativePath || file.name }))); dirInput.value = ''; });
  let dragDepth = 0;
  const hasFiles = e => [...(e.dataTransfer?.types || [])].includes('Files');
  main.addEventListener('dragenter', e => { if (!S.bucket || !hasFiles(e)) return; e.preventDefault(); dragDepth++; drop.hidden = false; });
  main.addEventListener('dragover', e => { if (S.bucket && hasFiles(e)) e.preventDefault(); });
  main.addEventListener('dragleave', e => { if (!hasFiles(e)) return; if (--dragDepth <= 0) { dragDepth = 0; drop.hidden = true; } });
  main.addEventListener('drop', async e => {
    if (!S.bucket || !hasFiles(e)) return;
    e.preventDefault(); dragDepth = 0; drop.hidden = true;
    startUpload(await filesFromDrop(e.dataTransfer));
  });

  // ---------------------------------------------------------------- details panel
  async function headOf(key) {
    if (!S.heads.has(key)) S.heads.set(key, get(s3Path(S.conn, S.bucket, 'head', { key })).catch(e => ({ error: e.message })));
    return S.heads.get(key);
  }

  function paintDetail() {
    detail.hidden = !S.bucket || !S.drawer;
    if (detail.hidden) return;
    const rows = selected();
    const close = h('button.icon-btn.small.detail-close', { type: 'button', 'aria-label': 'Close details', onclick: () => { S.drawer = false; store.set('stDrawer', '0'); paintAll(); } }, icon('x', 'sm'));
    if (!rows.length) {
      const folders = S.rows.filter(r => r.type === 'folder').length, files = S.rows.filter(r => r.type === 'file');
      const m = measureState(S.prefix);
      return fill(detail, h('div.dt-head', icon(S.prefix ? 'folder' : 'bucket'), h('div.dt-title', S.prefix ? leaf(S.prefix) : S.bucket), close),
        h('dl.dt-list', kv('Location', `s3://${S.bucket}/${S.prefix}`, true), kv('Connection', connName(S.conn)),
          kv('Loaded here', `${plural(folders, 'folder')}, ${plural(files.length, 'file')} · ${bytes(files.reduce((a, r) => a + r.size, 0))}${S.token ? ' (more available)' : ''}`),
          measureLine(m)),
        h('div.dt-actions', h('button.btn.small', { type: 'button', onclick: () => measure(S.prefix) }, icon('layers', 'sm'), 'Calculate size'),
          h('button.btn.small', { type: 'button', onclick: () => makeLibrary(S.prefix) }, icon('library', 'sm'), 'Use as library')));
    }
    if (rows.length > 1) {
      const files = rows.filter(r => r.type === 'file');
      return fill(detail, h('div.dt-head', icon('copy'), h('div.dt-title', plural(rows.length, 'item') + ' selected'), close),
        h('dl.dt-list', kv('Files', `${num(files.length)} · ${bytes(files.reduce((a, r) => a + r.size, 0))}`), kv('Folders', num(rows.length - files.length))));
    }
    const r = rows[0];
    if (r.type === 'folder') {
      const m = measureState(r.key);
      return fill(detail, h('div.dt-head', icon('folder'), h('div.dt-title', r.name), close),
        h('dl.dt-list', kv('Location', `s3://${S.bucket}/${r.key}`, true), measureLine(m)),
        h('div.dt-actions', h('button.btn.small', { type: 'button', onclick: () => measure(r.key) }, icon('layers', 'sm'), 'Calculate size'),
          h('button.btn.small', { type: 'button', onclick: () => makeLibrary(r.key) }, icon('library', 'sm'), 'Use as library')));
    }
    const list = h('dl.dt-list', kv('Key', r.key, true), kv('Size', `${bytes(r.size)} (${num(r.size)} bytes)`), kv('Modified', when(r.mtime)));
    const pv = h('div.dt-preview', preview(S.conn, S.bucket, r.key, r.size));
    fill(detail, h('div.dt-head', icon(kindIcon(r.kind)), h('div.dt-title', { title: r.name }, r.name.split('/').pop()), close), pv, list);
    headOf(r.key).then(info => {
      if (!S.sel.has(r.key) || S.sel.size !== 1) return;
      if (info.error) return list.append(kv('Details', info.error));
      list.append(...[kv('Content type', info.content_type || info.guessed_type || '–'), kv('ETag', info.etag, true), kv('Storage class', info.storage_class),
        info.cache_control ? kv('Cache-Control', info.cache_control) : null, info.version_id ? kv('Version', info.version_id, true) : null,
        ...Object.entries(info.metadata || {}).map(([k, v]) => kv(`x-amz-meta-${k}`, v)),
        h('div', h('button.btn.small', { type: 'button', onclick: () => properties(r) }, icon('sliders', 'sm'), 'Edit properties'))].filter(Boolean));
    });
  }
  const kv = (k, v, mono = false) => h('div', h('dt', k), h('dd', { class: mono ? 'mono' : '' }, v));
  function measureLine(m) {
    if (!m) return null;
    if (m.state === 'running') return kv('Size', `Counting… ${num(m.done)} objects so far`);
    if (m.state === 'done' && m.result) return kv('Size', `${bytes(m.result.bytes)} in ${plural(m.result.objects, 'object')}`);
    return kv('Size', m.state === 'cancelled' ? 'Cancelled' : (m.errors[0] || 'Could not measure'));
  }

  // ---------------------------------------------------------------- start
  scope.add(on('connections', () => { paintSide(); paintBar(); }));
  await update(parts);
  return { update, destroy() { scope.dispose(); document.title = 'Media Library'; } };
}
