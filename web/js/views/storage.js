// Storage: browse, preview, upload, move, rename and delete objects in any S3-compatible bucket.
import { h, fill, $, Scope, debounce } from '../lib/dom.js';
import { icon, kindIcon } from '../lib/icons.js';
import { get, objectUrl, post, s3Path, del } from '../lib/api.js';
import { bytes, collator, kindOf, leaf, num, when } from '../lib/fmt.js';
import { t } from '../lib/i18n.js';
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
  const crumbs = h('nav.st-crumbs', { 'aria-label': t('storage.location') });
  const filterIn = h('input.input', { type: 'search', placeholder: t('storage.filterFiles'), title: t('storage.filterHint'), 'aria-label': t('storage.filterOrSearch'), autocomplete: 'off' });
  const fileInput = h('input', { type: 'file', multiple: true, hidden: true });
  const dirInput = h('input', { type: 'file', webkitdirectory: true, hidden: true });
  const uploadBtn = h('button.btn.primary', { type: 'button', onclick: e => showMenu({ anchor: e.currentTarget, items: [
    { label: t('storage.uploadFilesMenu'), icon: 'upload', onClick: () => fileInput.click() },
    { label: t('storage.uploadFolderMenu'), icon: 'folder', onClick: () => dirInput.click() },
  ] }) }, icon('upload', 'sm'), h('span.lbl', t('storage.upload')));
  const newFolderBtn = h('button.btn#newfolder', { type: 'button', onclick: () => newFolder() }, icon('folder-plus', 'sm'), h('span.lbl', t('storage.newFolder')));
  const refreshBtn = h('button.icon-btn', { type: 'button', 'aria-label': t('storage.refresh'), title: t('storage.refresh'), onclick: () => reload() }, icon('refresh'));
  const layoutBtn = h('button.icon-btn', { type: 'button', onclick: () => { S.layout = S.layout === 'list' ? 'grid' : 'list'; store.set('stLayout', S.layout); paintAll(); } });
  const detailBtn = h('button.icon-btn', { type: 'button', 'aria-label': t('storage.details'), title: t('storage.details'), onclick: () => { S.drawer = !S.drawer; store.set('stDrawer', S.drawer ? '1' : '0'); paintAll(); } }, icon('info'));
  const moreBtn = h('button.icon-btn', { type: 'button', 'aria-label': t('common.more'), onclick: e => showMenu({ anchor: e.currentTarget, align: 'right', items: moreItems() }) }, icon('more'));
  const sideToggle = h('button.icon-btn.side-toggle', { type: 'button', 'aria-label': t('storage.buckets'), onclick: () => sideOpen(true) }, icon('menu'));
  const bar = h('header.st-bar', sideToggle, crumbs, h('div.search.st-filter', icon('search', 'sm'), filterIn), newFolderBtn, uploadBtn, refreshBtn, layoutBtn, detailBtn, moreBtn);
  const selbar = h('div.st-selbar', { hidden: true });
  const body = h('div.st-scroll', { tabindex: 0, 'aria-label': t('storage.objects') });
  const detail = h('aside.st-detail');
  const drop = h('div.st-drop', { hidden: true }, h('div', icon('upload', 'lg'), h('strong', t('storage.dropToUpload')), h('span.muted.dest')));
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
    document.title = S.bucket ? t('storage.pageTitleBucket', { bucket: S.bucket }) : t('storage.pageTitle');
  }

  function paintSide() {
    fill(side,
      h('div.side-head', h('span', t('storage.title')), h('button.icon-btn.small', { type: 'button', 'aria-label': t('storage.refreshBuckets'), title: t('storage.refreshBuckets'), onclick: () => { for (const c of state.connections) ensureBuckets(c.id, true); } }, icon('refresh', 'sm')),
        h('button.icon-btn.small.side-close', { type: 'button', 'aria-label': t('common.close'), onclick: () => sideOpen(false) }, icon('x', 'sm'))),
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
            st === 'ok' && !(S.buckets.get(c.id) || []).length ? h('div.side-note', t('storage.noBucketsShort')) : null,
            h('button.side-bucket.add', { type: 'button', onclick: () => newBucket(c.id) }, icon('plus', 'sm'), t('storage.newBucket'))) : null);
      }) : h('div.side-note', t('storage.noConnections')),
      h('div.side-foot', h('button.btn.small', { type: 'button', onclick: () => navigate('connections') }, icon('plug', 'sm'), t('storage.connections'))));
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
    layoutBtn.setAttribute('aria-label', S.layout === 'list' ? t('storage.gridView') : t('storage.listView'));
    layoutBtn.title = layoutBtn.getAttribute('aria-label');
    detailBtn.classList.toggle('on', S.drawer);
    $('.dest', drop).textContent = inBucket ? `${S.bucket}/${S.prefix}` : '';
  }

  function drawCrumbs() {
    const last = crumbItems.length - 1;
    const piece = (it, i) => h('button.crumb', { type: 'button', 'aria-current': i === last ? 'page' : null, onclick: it.go }, it.label);
    const hidden = crumbItems.slice(0, folded);
    fill(crumbs, S.conn ? null : h('span.crumb', t('storage.title')),
      hidden.length ? [h('button.crumb', { type: 'button', 'aria-label': t('storage.earlierLevels'), onclick: e => showMenu({ anchor: e.currentTarget, items: hidden.map(it => ({ label: it.label, icon: 'folder', onClick: it.go })) }) }, '…'), h('span.sep', '›')] : null,
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
    if (!state.connections.some(c => c.id === S.conn)) return fill(body, emptyState('alert', t('storage.unknownConnection'), t('storage.unknownConnectionText')));
    if (!S.bucket) return fill(body, bucketsLanding());
    if (S.error) return fill(body, h('div.empty-state', icon('alert'), h('h3', t('storage.couldNotList')), h('p', S.error.message),
      h('div', { style: { display: 'flex', gap: '8px' } }, h('button.btn', { type: 'button', onclick: () => reload() }, icon('refresh', 'sm'), t('common.retry')), h('button.btn', { type: 'button', onclick: () => navigate('connections') }, t('storage.checkConnection')))));
    shownRows = visibleRows();
    if (!S.loaded) return fill(body, h('div.empty-state', h('div.spinner')));
    const searching = S.search ? h('div.banner.st-search', h('div.grow', S.search.truncated ? t('storage.searchResultsStopped', { count: S.search.rows.length, q: S.search.q, path: `${S.bucket}/${S.prefix}`, scanned: S.search.scanned }) : t('storage.searchResults', { count: S.search.rows.length, q: S.search.q, path: `${S.bucket}/${S.prefix}` })),
      h('button.btn.small', { type: 'button', onclick: () => { S.search = null; S.filter = ''; filterIn.value = ''; paintAll(); } }, t('storage.clearSearch'))) : null;
    if (!shownRows.length) {
      return fill(body, searching, S.filter || S.search ? emptyState('search', t('storage.noMatches'), t('storage.noMatchesText'))
        : h('div.empty-state', icon('folder'), h('h3', t('storage.folderEmpty')), h('p', t('storage.folderEmptyText')),
          h('button.btn.primary', { type: 'button', onclick: () => fileInput.click() }, icon('upload', 'sm'), t('storage.uploadFiles'))));
    }
    const more = S.token && !S.search ? h('div.st-more', h('button.btn', { type: 'button', disabled: S.loading, onclick: () => loadMore() }, S.loading ? t('common.loading') : t('storage.loadMore', { count: S.rows.length }))) : null;
    fill(body, searching, S.layout === 'list' ? listView(shownRows) : gridView(shownRows), more);
    if (more) { moreObserver.disconnect(); moreObserver.observe(more); }
  }
  const moreObserver = new IntersectionObserver(es => { if (es.some(e => e.isIntersecting) && S.token && !S.loading) loadMore(); }, { root: body, rootMargin: '300px' });
  scope.add(() => moreObserver.disconnect());
  async function loadMore() { if (!S.token || S.loading) return; await load(false); }

  const emptyState = (ic, title, text) => h('div.empty-state', icon(ic), h('h3', title), h('p', text));

  function connectionsLanding() {
    if (!state.connections.length) {
      return h('div.empty-state', icon('storage'), h('h3', t('storage.connectStore')),
        h('p', t('storage.connectStoreText')),
        h('button.btn.primary', { type: 'button', onclick: async () => { const c = await editConnection(); if (c) { await loadConnections(); navigate('storage', c.id); } } }, icon('plus', 'sm'), t('storage.addConnection')));
    }
    return h('div.empty-state', icon('storage'), h('h3', t('storage.chooseConnection')));
  }

  function bucketsLanding() {
    const list = S.buckets.get(S.conn), st = S.bucketState.get(S.conn);
    if (!list && st !== 'loading' && !st) return h('div.empty-state', h('div.spinner'));
    if (st === 'loading' && !list) return h('div.empty-state', h('div.spinner'));
    if (st && !['ok', 'limited', 'loading'].includes(st)) {
      return h('div.empty-state', icon('alert'), h('h3', t('storage.couldNotReach', { name: connName(S.conn) })), h('p', st),
        h('div', { style: { display: 'flex', gap: '8px' } }, h('button.btn', { type: 'button', onclick: () => ensureBuckets(S.conn, true).then(paintBody) }, icon('refresh', 'sm'), t('common.retry')),
          h('button.btn', { type: 'button', onclick: () => navigate('connections') }, t('storage.editConnection'))));
    }
    return h('div.st-buckets',
      h('div.st-buckets-head', h('h2', t('storage.buckets')), h('button.btn', { type: 'button', onclick: () => newBucket(S.conn) }, icon('plus', 'sm'), t('storage.newBucket'))),
      st === 'limited' ? h('p.muted', t('storage.limitedKey')) : null,
      (list || []).length ? h('div.bucket-grid', list.map(b => h('button.bucket-card', { type: 'button', onclick: () => navigate('storage', S.conn, b.name) }, icon('bucket', 'lg'), h('div.bucket-name', b.name), h('div.muted', b.created ? t('storage.created', { date: b.created.slice(0, 10) }) : '')))) : emptyState('bucket', t('storage.noBuckets'), t('storage.noBucketsText')));
  }

  // ---- list view
  function listView(rows) {
    const th = (col, label, cls = '') => h('th', { class: `sortable ${cls}`, 'aria-sort': S.sort.col === col ? (S.sort.dir === 1 ? 'ascending' : 'descending') : null,
      onclick: () => { S.sort = { col, dir: S.sort.col === col ? -S.sort.dir : (col === 'mtime' ? -1 : 1) }; store.setJson('stSort', S.sort); paintBody(); } },
      label, S.sort.col === col ? h('span.arrow', S.sort.dir === 1 ? '↑' : '↓') : null);
    const allOn = rows.length && rows.every(r => S.sel.has(r.key));
    const head = h('tr', h('th.chk', h('input', { type: 'checkbox', 'aria-label': t('storage.selectAll'), checked: allOn, onchange: e => { if (e.target.checked) rows.forEach(r => S.sel.add(r.key)); else S.sel.clear(); paintSelection(); } })),
      th('name', t('storage.name')), th('size', t('storage.size'), 'num'), th('mtime', t('storage.modified'), 'mtime'));
    const tbody = h('tbody', rows.map(r => {
      const tr = h('tr', { dataset: { key: r.key }, class: S.sel.has(r.key) ? 'selected' : '' },
        h('td.chk', h('input', { type: 'checkbox', tabindex: -1, 'aria-label': t('storage.selectItem', { name: r.name }), checked: S.sel.has(r.key) })),
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
        h('div.tile-name', { title: r.key }, r.name), h('div.tile-sub', r.size == null ? t('storage.folder') : bytes(r.size)));
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
    fill(selbar, h('span.sel-count', files.length ? t('storage.selectedWithSize', { count: rows.length, size: bytes(size) }) : t('storage.selected', { count: rows.length })),
      h('div.sel-actions', actionButtons(rows)), h('button.btn.ghost.small', { type: 'button', onclick: () => { S.sel.clear(); paintSelection(); } }, t('storage.clear')));
  }

  function actionButtons(rows) {
    const one = rows.length === 1 ? rows[0] : null;
    const files = rows.filter(r => r.type === 'file');
    const b = (label, ic, fn, cls = '') => h('button.btn.small', { type: 'button', class: cls, onclick: fn }, icon(ic, 'sm'), h('span.lbl', label));
    return [
      files.length ? b(t('storage.download'), 'download', () => download(files)) : null,
      one && one.type === 'file' ? h('button.btn.small', { type: 'button', onclick: e => showMenu({ anchor: e.currentTarget, items: linkItems(S.conn, S.bucket, one.key) }) }, icon('link', 'sm'), h('span.lbl', t('storage.copyLink'))) : null,
      files.some(r => MEDIA_KINDS.has(r.kind)) ? b(t('common.play'), 'play', () => playInPlayer(files.filter(r => MEDIA_KINDS.has(r.kind)))) : null,
      one ? b(t('common.rename'), 'edit', () => rename(one)) : null,
      b(t('storage.move'), 'move', () => transfer(rows, true)), b(t('common.copy'), 'copy', () => transfer(rows, false)),
      b(t('common.delete'), 'trash', () => remove(rows), 'danger'),
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
      one && one.type === 'folder' ? { label: t('common.open'), icon: 'folder', onClick: () => here(one.key) } : null,
      one && one.type === 'file' ? { label: t('storage.detailsPreview'), icon: 'info', onClick: () => openRow(one) } : null,
      S.search && one ? { label: t('storage.showInFolder'), icon: 'folder', onClick: () => { const p = one.key.slice(0, one.key.lastIndexOf('/') + 1); S.search = null; here(p); } } : null,
      files.length ? { label: files.length > 1 ? t('storage.downloadFiles', { count: files.length }) : t('storage.download'), icon: 'download', onClick: () => download(files) } : null,
      media.length ? { label: t('storage.playInPlayer'), icon: 'play', onClick: () => playInPlayer(media) } : null,
      { sep: true },
      one && one.type === 'file' ? { head: t('storage.copyLink') } : null,
      ...(one && one.type === 'file' ? linkItems(S.conn, S.bucket, one.key).slice(1) : []),
      { sep: true },
      one ? { label: t('common.rename'), icon: 'edit', kb: 'F2', onClick: () => rename(one) } : null,
      { label: t('storage.moveTo'), icon: 'move', onClick: () => transfer(rows, true) },
      { label: t('storage.copyTo'), icon: 'copy', onClick: () => transfer(rows, false) },
      one && one.type === 'file' ? { label: t('storage.propertiesMenu'), icon: 'sliders', onClick: () => properties(one) } : null,
      one && one.type === 'folder' ? { label: t('storage.calculateSize'), icon: 'layers', onClick: () => measure(one.key) } : null,
      one && one.type === 'folder' ? { label: t('storage.useAsLibraryMenu'), icon: 'library', onClick: () => makeLibrary(one.key) } : null,
      { sep: true },
      { label: t('common.delete'), icon: 'trash', kb: 'Del', danger: true, onClick: () => remove(rows) },
    ].filter(Boolean);
  }

  // ---------------------------------------------------------------- actions
  async function download(files) {
    if (files.length > 1 && !await confirmDialog({ title: t('storage.downloadFilesConfirm', { count: files.length }), message: t('storage.downloadFilesMessage'), confirm: t('storage.download') })) return;
    files.forEach((f, i) => setTimeout(() => {
      const a = h('a', { href: objectUrl(S.conn, S.bucket, f.key, true), download: f.name.split('/').pop() });
      document.body.append(a); a.click(); a.remove();
    }, i * 450));
  }

  async function playInPlayer(rows) {
    try {
      const j = await post(s3Path(S.conn, S.bucket, 'play'), { keys: rows.map(r => r.key), player: store.get('player', state.defaultPlayer) });
      toast(j.count > 1 ? t('storage.openingItemsIn', { count: j.count, player: j.player }) : t('storage.openingIn', { player: j.player }), { kind: 'ok' });
    } catch (e) {
      if (e.status === 403 && rows.length === 1) { // a browser on another computer: play in the browser instead
        window.open(objectUrl(S.conn, S.bucket, rows[0].key), '_blank');
        return;
      }
      toastError(t('storage.couldNotStartPlayer'), e);
    }
  }

  async function newFolder() {
    const name = await promptDialog({ title: t('storage.newFolder'), label: t('storage.folderName'), confirm: t('storage.create'), hint: t('storage.newFolderHint'),
      validate: v => !v ? t('storage.enterName') : v.includes('//') ? t('storage.singleSlashes') : '' });
    if (!name) return;
    try { await post(s3Path(S.conn, S.bucket, 'folder'), { prefix: S.prefix + name }); toast(t('storage.folderCreated'), { kind: 'ok' }); reload(); } catch (e) { toastError(t('storage.couldNotCreateFolder'), e); }
  }

  async function newBucket(conn) {
    const name = await promptDialog({ title: t('storage.newBucket'), label: t('storage.bucketName'), confirm: t('storage.create'), mono: true, hint: t('storage.bucketNameHint'),
      validate: v => /^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/.test(v) ? '' : t('storage.bucketNameInvalid') });
    if (!name) return;
    try { await post(`/api/s3/${encodeURIComponent(conn)}/buckets`, { name }); await ensureBuckets(conn, true); navigate('storage', conn, name); toast(t('storage.bucketCreated', { name }), { kind: 'ok' }); } catch (e) { toastError(t('storage.couldNotCreateBucket'), e); }
  }

  async function deleteBucket() {
    if (!await confirmDialog({ title: t('storage.deleteBucketTitle', { name: S.bucket }), message: t('storage.deleteBucketMessage'), confirm: t('storage.deleteBucket'), danger: true })) return;
    try { await del(`/api/s3/${encodeURIComponent(S.conn)}/buckets/${encodeURIComponent(S.bucket)}`); const c = S.conn; await ensureBuckets(c, true); navigate('storage', c); toast(t('storage.bucketDeleted'), { kind: 'ok' }); } catch (e) { toastError(t('storage.couldNotDeleteBucket'), e); }
  }

  async function rename(r) {
    const name = await promptDialog({ title: r.type === 'folder' ? t('storage.renameFolder') : t('storage.renameFile'), label: t('storage.newName'), value: r.name.split('/').pop(), confirm: t('common.rename'),
      validate: v => !v ? t('storage.enterName') : v.includes('/') ? t('storage.nameNoSlash') : '' });
    if (!name || name === r.name.split('/').pop()) return;
    const parent = r.key.slice(0, r.key.replace(/\/$/, '').lastIndexOf('/') + 1);
    try {
      const j = await post(s3Path(S.conn, S.bucket, 'transfer'), { items: [{ from: r.key, to: parent + name + (r.type === 'folder' ? '/' : '') }], move: true, skip_existing: true });
      pokeWatcher();
      finishTask(j.task, 'rename');
    } catch (e) { toastError(t('storage.couldNotRename'), e); }
  }

  async function transfer(rows, move) {
    const task = await transferDialog({ conn: S.conn, bucket: S.bucket, prefix: S.prefix, rows, move });
    if (task) finishTask(task, move ? 'move' : 'copy');
  }

  async function remove(rows) {
    const files = rows.filter(r => r.type === 'file'), folders = rows.filter(r => r.type === 'folder');
    const what = { mix: !folders.length ? 'files' : !files.length ? 'folders' : 'both', files: files.length, folders: folders.length };
    const ok = await confirmDialog({
      title: t('storage.deleteTitle', what), danger: true, confirm: t('common.delete'),
      message: rows.length === 1 ? t('storage.deleteOneMessage', { name: rows[0].name, bucket: S.bucket }) : t('storage.deleteManyMessage', { ...what, bucket: S.bucket }),
      detail: folders.length ? t('storage.deleteDetailFolders') : t('storage.deleteDetail'),
    });
    if (!ok) return;
    try {
      const j = await post(s3Path(S.conn, S.bucket, 'delete'), { keys: files.map(r => r.key), prefixes: folders.map(r => r.key) });
      pokeWatcher();
      finishTask(j.task, 'delete');
    } catch (e) { toastError(t('storage.couldNotDelete'), e); }
  }

  function finishTask(task, kind) {
    if (task.state === 'running') { toast(t('storage.taskRunning', { kind })); return; }
    S.reported.add(task.id);
    if (task.error_count) toast(t('storage.taskProblems', { kind, count: task.error_count }), { kind: 'error' });
    else toast(t('storage.taskDone', { kind, count: task.done }), { kind: 'ok' });
    reload();
  }
  scope.add(on('task-finished', task => {
    if (!S.reported.has(task.id) && ['copy', 'move', 'delete'].includes(task.kind)) {
      S.reported.add(task.id);
      toast(task.error_count ? t('storage.taskFinishedProblems', { title: task.title, count: task.error_count }) : t('storage.taskFinishedDone', { title: task.title }), { kind: task.error_count ? 'error' : 'ok' });
      softReload();
    }
    if (task.kind === 'size') paintDetail();
  }));
  scope.add(on('activity', () => { if ([...S.measure.values()].some(m => m.state === 'running')) paintDetail(); }));
  scope.add(on('uploaded', d => { if (d.conn === S.conn && d.bucket === S.bucket && d.key.startsWith(S.prefix)) softReload(); }));

  async function properties(r) {
    try {
      const head = await get(s3Path(S.conn, S.bucket, 'head', { key: r.key }));
      const out = await propertiesDialog(S.conn, S.bucket, r.key, head);
      if (out) { S.heads.set(r.key, out); toast(t('storage.propertiesSaved'), { kind: 'ok' }); paintDetail(); }
    } catch (e) { toastError(t('storage.couldNotReadProperties'), e); }
  }

  async function measure(prefix) {
    try {
      const j = await post(s3Path(S.conn, S.bucket, 'measure'), { prefix });
      S.measure.set(prefix, j.task);
      pokeWatcher();
      paintDetail();
    } catch (e) { toastError(t('storage.couldNotMeasure'), e); }
  }
  const measureState = prefix => {
    const m = S.measure.get(prefix);
    if (!m) return null;
    return state.tasks.find(x => x.id === m.id) || m;
  };

  async function makeLibrary(prefix) {
    const lib = await libraryDialog({ conn: S.conn, bucket: S.bucket, prefix });
    if (lib) { await loadLibraries(); toast(t('storage.libraryAdded'), { kind: 'ok' }); navigate('library', lib.id); }
  }

  function moreItems() {
    return [
      S.bucket ? { label: t('storage.calculateFolderSize'), icon: 'layers', onClick: () => { S.sel.clear(); paintSelection(); measure(S.prefix); S.drawer = true; paintAll(); } } : null,
      S.bucket ? { label: t('storage.useFolderAsLibrary'), icon: 'library', onClick: () => makeLibrary(S.prefix) } : null,
      S.bucket ? { label: t('storage.unfinishedUploadsMenu'), icon: 'upload', onClick: () => incompleteUploadsDialog(S.conn, S.bucket) } : null,
      S.bucket ? { sep: true } : null,
      { label: t('storage.newBucketMenu'), icon: 'plus', onClick: () => newBucket(S.conn) },
      S.bucket && !S.prefix ? { label: t('storage.deleteThisBucket'), icon: 'trash', danger: true, onClick: () => deleteBucket() } : null,
      { label: t('storage.editConnectionMenu'), icon: 'plug', onClick: () => { const c = state.connections.find(x => x.id === S.conn); c && editConnection(c).then(saved => saved && (S.buckets.delete(S.conn), update([S.conn, S.bucket, ...S.prefix.split('/').filter(Boolean)]))); } },
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
    } catch (err) { toastError(t('storage.searchFailed'), err); }
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
        title: t('storage.filesExist', { count: clash.length }), size: 'narrow',
        body: h('div', h('p', clash.length > 4 ? t('storage.namesAndMore', { names: clash.slice(0, 4).map(e => e.path).join(', '), count: clash.length - 4 }) : clash.map(e => e.path).join(', ')), h('p.muted', t('storage.replaceOrKeep'))),
        actions: [{ label: t('common.cancel'), value: 'cancel' }, { label: t('storage.keepExisting'), value: 'skip' }, { label: t('storage.replace'), primary: true, value: 'replace' }],
      });
      const choice = await m.closed;
      if (!choice || choice === 'cancel') return;
      replace = choice === 'replace';
    }
    const clashed = new Set(clash.map(e => e.path));
    enqueue(S.conn, S.bucket, entries.map(e => ({ file: e.file, key: S.prefix + e.path, overwrite: replace && clashed.has(e.path) })));
    toast(t('storage.uploadingTo', { count: entries.length, path: `${S.bucket}/${S.prefix}` }));
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
    const close = h('button.icon-btn.small.detail-close', { type: 'button', 'aria-label': t('storage.closeDetails'), onclick: () => { S.drawer = false; store.set('stDrawer', '0'); paintAll(); } }, icon('x', 'sm'));
    if (!rows.length) {
      const folders = S.rows.filter(r => r.type === 'folder').length, files = S.rows.filter(r => r.type === 'file');
      const m = measureState(S.prefix);
      return fill(detail, h('div.dt-head', icon(S.prefix ? 'folder' : 'bucket'), h('div.dt-title', S.prefix ? leaf(S.prefix) : S.bucket), close),
        h('dl.dt-list', kv(t('storage.location'), `s3://${S.bucket}/${S.prefix}`, true), kv(t('storage.connection'), connName(S.conn)),
          kv(t('storage.loadedHere'), (S.token ? t('storage.loadedSummaryMore', { folders, files: files.length, size: bytes(files.reduce((a, r) => a + r.size, 0)) }) : t('storage.loadedSummary', { folders, files: files.length, size: bytes(files.reduce((a, r) => a + r.size, 0)) }))),
          measureLine(m)),
        h('div.dt-actions', h('button.btn.small', { type: 'button', onclick: () => measure(S.prefix) }, icon('layers', 'sm'), t('storage.calculateSize')),
          h('button.btn.small', { type: 'button', onclick: () => makeLibrary(S.prefix) }, icon('library', 'sm'), t('storage.useAsLibrary'))));
    }
    if (rows.length > 1) {
      const files = rows.filter(r => r.type === 'file');
      return fill(detail, h('div.dt-head', icon('copy'), h('div.dt-title', t('storage.selected', { count: rows.length })), close),
        h('dl.dt-list', kv(t('storage.files'), `${num(files.length)} · ${bytes(files.reduce((a, r) => a + r.size, 0))}`), kv(t('storage.folders'), num(rows.length - files.length))));
    }
    const r = rows[0];
    if (r.type === 'folder') {
      const m = measureState(r.key);
      return fill(detail, h('div.dt-head', icon('folder'), h('div.dt-title', r.name), close),
        h('dl.dt-list', kv(t('storage.location'), `s3://${S.bucket}/${r.key}`, true), measureLine(m)),
        h('div.dt-actions', h('button.btn.small', { type: 'button', onclick: () => measure(r.key) }, icon('layers', 'sm'), t('storage.calculateSize')),
          h('button.btn.small', { type: 'button', onclick: () => makeLibrary(r.key) }, icon('library', 'sm'), t('storage.useAsLibrary'))));
    }
    const list = h('dl.dt-list', kv(t('storage.key'), r.key, true), kv(t('storage.size'), t('storage.sizeBytes', { size: bytes(r.size), bytes: r.size })), kv(t('storage.modified'), when(r.mtime)));
    const pv = h('div.dt-preview', preview(S.conn, S.bucket, r.key, r.size));
    fill(detail, h('div.dt-head', icon(kindIcon(r.kind)), h('div.dt-title', { title: r.name }, r.name.split('/').pop()), close), pv, list);
    headOf(r.key).then(info => {
      if (!S.sel.has(r.key) || S.sel.size !== 1) return;
      if (info.error) return list.append(kv(t('storage.details'), info.error));
      list.append(...[kv(t('storage.contentType'), info.content_type || info.guessed_type || '–'), kv('ETag', info.etag, true), kv(t('storage.storageClass'), info.storage_class),
        info.cache_control ? kv('Cache-Control', info.cache_control) : null, info.version_id ? kv(t('storage.version'), info.version_id, true) : null,
        ...Object.entries(info.metadata || {}).map(([k, v]) => kv(`x-amz-meta-${k}`, v)),
        h('div', h('button.btn.small', { type: 'button', onclick: () => properties(r) }, icon('sliders', 'sm'), t('storage.editProperties')))].filter(Boolean));
    });
  }
  const kv = (k, v, mono = false) => h('div', h('dt', k), h('dd', { class: mono ? 'mono' : '' }, v));
  function measureLine(m) {
    if (!m) return null;
    if (m.state === 'running') return kv(t('storage.size'), t('storage.counting', { count: m.done }));
    if (m.state === 'done' && m.result) return kv(t('storage.size'), t('storage.sizeInObjects', { size: bytes(m.result.bytes), count: m.result.objects }));
    return kv(t('storage.size'), m.state === 'cancelled' ? t('storage.cancelled') : (m.errors[0] || t('storage.couldNotMeasure')));
  }

  // ---------------------------------------------------------------- start
  scope.add(on('connections', () => { paintSide(); paintBar(); }));
  await update(parts);
  return { update, destroy() { scope.dispose(); document.title = 'Media Library'; } };
}
