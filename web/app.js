'use strict';
(() => {
  const $ = s => document.querySelector(s);
  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };
  const store = {
    get(k, d) { try { return localStorage.getItem(k) ?? d; } catch { return d; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch { /* private mode */ } },
  };
  const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
  const num = n => n.toLocaleString('en-US');
  const UNITS = ['KiB', 'MiB', 'GiB', 'TiB'];
  const bytes = n => {
    if (n < 1024) return n + ' B';
    let i = -1;
    do { n /= 1024; i++; } while (n >= 1024 && i < UNITS.length - 1);
    return n.toFixed(n >= 100 ? 0 : 1) + ' ' + UNITS[i];
  };
  const pad = n => String(n).padStart(2, '0');
  const clock = s => {
    s = Math.round(s || 0);
    const h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60);
    return h ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`;
  };
  const span = s => {
    const h = Math.floor(s / 3600), m = Math.round(s % 3600 / 60);
    return h ? `${h} h ${m} min` : `${m} min`;
  };
  const resLabel = (w, h) => {
    if (!w || !h) return '';
    const hi = Math.max(w, h), lo = Math.min(w, h);
    return hi >= 3800 ? '4K' : hi >= 2500 ? '1440p' : hi >= 1900 ? '1080p' : hi >= 1260 ? '720p' : lo + 'p';
  };
  const stem = n => n.replace(/\.[^.]+$/, '');
  const thumb = (it, i) => `/thumbs/${state.lib}/${it.id}-${it.ver}-${i}.jpg`;
  const mediaUrl = it => `${location.origin}/media/${state.lib}/${it.id}/${encodeURIComponent(it.name)}`;
  const inScope = (it, s) => !s || it.dir === s || it.dir.startsWith(s + '/');
  const leaf = p => p.slice(p.lastIndexOf('/') + 1);
  const extOf = name => { const i = name.lastIndexOf('.'); return i > 0 ? name.slice(i + 1).toLowerCase() : ''; };
  // Every extension the indexer picks up. Until the user changes the filter, only the common video ones show.
  const KNOWN_TYPES = ['mp4', 'm4v', 'mov', 'mkv', 'webm', 'avi', 'wmv', 'flv', 'ts', 'm2ts', 'mp3', 'flac', 'm4a', 'aac', 'wav', 'ogg', 'opus'];
  const COMMON_VIDEO = new Set(['mp4', 'm4v', 'mov', 'mkv', 'webm', 'avi', 'wmv']);
  const defaultHidden = () => new Set(KNOWN_TYPES.filter(t => !COMMON_VIDEO.has(t)));
  const sameSet = (a, b) => a.size === b.size && [...a].every(x => b.has(x));
  const running = job => !!job && ['waiting', 'listing', 'indexing'].includes(job.state);

  const ICON = {
    play: '<svg viewBox="0 0 48 48" aria-hidden="true"><circle cx="24" cy="24" r="23" fill="rgb(0 0 0 / .55)"/><path d="M19 15v18l15-9z" fill="#fff"/></svg>',
    copy: '<svg viewBox="0 0 16 16" aria-hidden="true"><rect x="5" y="5" width="8.5" height="8.5" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.4"/><path d="M10.5 3.2V3A1.5 1.5 0 0 0 9 1.5H3A1.5 1.5 0 0 0 1.5 3v6A1.5 1.5 0 0 0 3 10.5h.3" fill="none" stroke="currentColor" stroke-width="1.4"/></svg>',
    chevron: '<svg viewBox="0 0 10 10" aria-hidden="true"><path d="M3 1.5L7 5l-4 3.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    note: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 18V5l11-2v13" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round"/><circle cx="6.5" cy="18" r="2.5" fill="none" stroke="currentColor" stroke-width="1.7"/><circle cx="17.5" cy="16" r="2.5" fill="none" stroke="currentColor" stroke-width="1.7"/></svg>',
    film: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="5" width="18" height="14" rx="2" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M3 9h18M3 15h18M8 5v14M16 5v14" stroke="currentColor" stroke-width="1.2"/></svg>',
  };

  const state = {
    libs: [], lib: null, info: null, jobs: {},
    items: [], byId: new Map(), scope: '', q: '', sort: store.get('sort', 'name'), tree: null, nodes: new Map(),
    shown: [],              // items minus the hidden file types: what the tree, grid, stats and Play all work on
    hidden: defaultHidden(), // hidden extensions ("mp3", "flv"), remembered across libraries and visits
  };
  try {
    const saved = store.get('hiddenTypes', null);
    if (saved) state.hidden = new Set(JSON.parse(saved));
  } catch { /* keep the default */ }

  async function api(path, body) {
    const r = await fetch(path, body === undefined ? undefined
      : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || r.statusText);
    return j;
  }

  // ---------------------------------------------------------------- toast
  let toastTimer;
  function toast(msg, error = false) {
    const t = $('#toast');
    t.textContent = msg;
    t.classList.toggle('error', error);
    t.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.remove('show'), error ? 5000 : 2600);
  }

  // ---------------------------------------------------------------- actions
  async function play(ids) {
    if (!ids.length) return;
    if (ids.length > 500) toast('Playing the first 500 items');
    try {
      const j = await api('/api/play', { lib: state.lib, ids: ids.slice(0, 500), player: $('#player').value });
      toast(j.count > 1 ? `Opening ${j.count} items in ${j.player}` : `Opening in ${j.player}`);
    } catch (e) {
      toast('Could not start the player: ' + e.message, true);
    }
  }

  async function copy(text, what) {
    try {
      await navigator.clipboard.writeText(text);
      toast(what + ' copied');
    } catch {
      toast('Copy failed: ' + text, true);
    }
  }

  // ---------------------------------------------------------------- folder tree
  function buildTree(items) {
    const root = { name: 'All media', path: '', kids: new Map(), count: 0, depth: -1 };
    const nodes = new Map([['', root]]);
    for (const it of items) {
      root.count++;
      let n = root;
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
    return { root, nodes };
  }
  const sortedKids = n => [...n.kids.values()].sort((a, b) => collator.compare(a.name, b.name));

  function navRow(node) {
    const li = el('li');
    const row = el('div', 'nrow');
    row.style.setProperty('--d', Math.max(0, node.depth));
    const tw = el('button', 'twist');
    tw.type = 'button';
    if (node.kids.size && node.depth >= 0) {
      tw.innerHTML = ICON.chevron;
      tw.setAttribute('aria-expanded', 'false');
      tw.setAttribute('aria-label', 'Expand ' + node.name);
      tw.addEventListener('click', () => (node.open ? collapse(node) : expand(node)));
    } else {
      tw.classList.add('leaf');
      tw.tabIndex = -1;
      tw.setAttribute('aria-hidden', 'true');
    }
    const b = el('button', 'node' + (node.depth < 0 ? ' all' : ''));
    b.type = 'button';
    b.append(el('span', 'nname', node.name), el('span', 'count', String(node.count)));
    b.title = node.path || 'All media';
    b.addEventListener('click', () => { go(node.path); closeNav(); });
    row.append(tw, b);
    li.append(row);
    Object.assign(node, { li, btn: b, tw });
    return li;
  }
  function expand(node) {
    if (!node.kids.size || node.depth < 0) return;
    if (!node.ul) {
      node.ul = el('ul');
      for (const k of sortedKids(node)) node.ul.append(navRow(k));
      node.li.append(node.ul);
    }
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
    const ul = $('#tree');
    ul.replaceChildren(navRow(state.tree));
    for (const k of sortedKids(state.tree)) ul.append(navRow(k));
  }
  function markActive(path) {
    for (const b of document.querySelectorAll('.node.active')) b.classList.remove('active');
    const chain = [];
    for (let n = state.nodes.get(path); n && n.depth >= 0; n = n.parent) chain.unshift(n);
    chain.slice(0, -1).forEach(expand);
    const node = state.nodes.get(path);
    if (node && node.btn) {
      node.btn.classList.add('active');
      node.btn.scrollIntoView({ block: 'nearest' });
    }
  }

  // ---------------------------------------------------------------- main view
  const itemSort = {
    name: (a, b) => collator.compare(a.it.name, b.it.name),
    new: (a, b) => b.it.mtime.localeCompare(a.it.mtime) || collator.compare(a.it.name, b.it.name),
    size: (a, b) => b.it.size - a.it.size,
    dur: (a, b) => (b.it.duration || 0) - (a.it.duration || 0),
  };
  const groupSort = {
    name: (a, b) => (b.key === '') - (a.key === '') || collator.compare(a.key, b.key), // loose files first
    new: (a, b) => b.newest.localeCompare(a.newest),
    size: (a, b) => b.size - a.size,
    dur: (a, b) => b.dur - a.dur,
  };

  function card(entry) {
    const it = entry.it;
    const art = el('article', 'card');
    art.dataset.id = it.id;
    const cover = el('button', 'cover');
    cover.type = 'button';
    cover.setAttribute('aria-label', 'Play ' + it.name);
    const dated = (state.info.type === 'local' ? 'modified ' : 'uploaded ') + it.mtime.slice(0, 10);
    cover.title = [it.name, it.dir, [resLabel(it.width, it.height), it.codec, it.fps && it.fps + ' fps'].filter(Boolean).join(' · '),
      bytes(it.size) + ' · ' + dated].filter(Boolean).join('\n');
    if (it.frames) {
      const img = el('img');
      img.alt = '';
      img.loading = 'lazy';
      img.decoding = 'async';
      img.src = thumb(it, it.cover ?? 0);
      cover.append(img);
    } else {
      const ph = el('div', 'ph');
      ph.innerHTML = it.kind === 'audio' ? ICON.note : ICON.film;
      ph.append(el('span', null, it.kind === 'audio' ? (it.codec || 'audio').toUpperCase()
        : it.indexed === false && !it.error ? 'Not indexed yet' : 'No preview'));
      cover.append(ph);
    }
    const res = resLabel(it.width, it.height);
    if (res) cover.append(el('span', 'badge tl', res));
    if (it.duration) cover.append(el('span', 'badge br', clock(it.duration)));
    if (it.frames > 1) {
      const ticks = el('span', 'ticks');
      for (let i = 0; i < it.frames; i++) ticks.append(el('i'));
      cover.append(ticks);
    }
    cover.insertAdjacentHTML('beforeend', `<span class="playhint">${ICON.play}</span>`);

    const meta = el('div', 'meta');
    const title = el('div', 'title', stem(it.name));
    title.title = it.name;
    const sub = el('div', 'sub', [entry.sub, bytes(it.size)].filter(Boolean).join(' · '));
    const cp = el('button', 'copy');
    cp.type = 'button';
    cp.innerHTML = ICON.copy;
    cp.title = 'Copy stream URL';
    cp.setAttribute('aria-label', 'Copy stream URL for ' + it.name);
    meta.append(title, cp, sub);
    art.append(cover, meta);
    return art;
  }

  function currentList() {
    const s = state.scope, q = state.q.trim().toLowerCase();
    return state.shown.filter(it => inScope(it, s) && (!q || it.name.toLowerCase().includes(q) || it.dir.toLowerCase().includes(q)));
  }

  let visibleIds = [];
  function render() {
    const s = state.scope;
    const list = currentList();
    const groups = new Map();
    for (const it of list) {
      const rel = it.dir === s ? '' : s ? it.dir.slice(s.length + 1) : it.dir;
      const key = rel.split('/')[0];
      let g = groups.get(key);
      if (!g) groups.set(key, g = { key, entries: [], size: 0, dur: 0, newest: '' });
      g.entries.push({ it, sub: rel.includes('/') ? rel.slice(key.length + 1) : '' });
      g.size += it.size;
      g.dur += it.duration || 0;
      if (it.mtime > g.newest) g.newest = it.mtime;
    }
    const ordered = [...groups.values()].sort(groupSort[state.sort]);
    const frag = document.createDocumentFragment();
    visibleIds = [];
    for (const g of ordered) {
      g.entries.sort(itemSort[state.sort]);
      const sec = el('section', 'group');
      const head = el('header', 'ghead');
      const t = groupPath(s, g.key);
      const label = t.title;
      const ids = g.entries.map(e => e.it.id);
      visibleIds.push(...ids);
      const pb = el('button', 'btn small');
      pb.type = 'button';
      pb.innerHTML = '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 2.5v11l9-5.5z" fill="currentColor"/></svg>';
      pb.append('Play');
      pb.setAttribute('aria-label', 'Play all in ' + label);
      pb.addEventListener('click', () => play(ids));
      // A group without a key holds the files that sit directly in the folder being viewed.
      head.append(t, el('span', 'gstats', `${g.key ? '' : 'files directly here · '}${g.entries.length} · ${span(g.dur)} · ${bytes(g.size)}`), pb);
      const grid = el('div', 'grid');
      for (const e of g.entries) grid.append(card(e));
      sec.append(head, grid);
      frag.append(sec);
    }
    pathIO.disconnect();
    onScreen.clear();
    $('#groups').replaceChildren(frag);
    for (const box of document.querySelectorAll('.gpath')) pathIO.observe(box);
    $('#empty').hidden = list.length > 0 || !state.items.length;

    const crumbs = $('#crumbs');
    crumbs.replaceChildren();
    const parts = s ? s.split('/') : [];
    [['All media', ''], ...parts.map((p, i) => [p, parts.slice(0, i + 1).join('/')])].forEach(([name, path], i, arr) => {
      const li = el('li');
      const b = el('button', null, name);
      b.type = 'button';
      if (i < arr.length - 1) b.addEventListener('click', () => go(path));
      else b.setAttribute('aria-current', 'page');
      li.append(b);
      crumbs.append(li);
    });
    const total = list.reduce((a, it) => a + (it.duration || 0), 0), size = list.reduce((a, it) => a + it.size, 0);
    const filtered = state.hidden.size ? state.items.filter(it => inScope(it, s) && state.hidden.has(it.ext)).length : 0;
    $('#stats').textContent = `${num(list.length)} items · ${span(total)} · ${bytes(size)}` +
      (state.q.trim() ? ` · matching “${state.q.trim()}”` : '') + (filtered ? ` · ${num(filtered)} hidden by type` : '');
    document.title = (s ? leaf(s) + ' · ' : '') + (state.info ? state.info.name + ' · ' : '') + 'Media Library';
  }

  // ---------------------------------------------------------------- group titles: the full folder path
  // "Movies › 2024 › Extras": every folder from the top of the library down to the group (the library's own
  // root folder is not shown). When it does not fit, whole folders are left out starting right after the first one,
  // so the top folder and the ones nearest to the content stay readable.
  function groupPath(scope, key) {
    const segs = [...(scope ? scope.split('/') : []), ...(key ? [key] : [])];
    const box = el('div', 'gpath');
    box._parts = segs.length ? segs.map((name, i) => ({ name, path: segs.slice(0, i + 1).join('/') }))
      : [{ name: 'Top-level files', path: '' }];  // files lying directly in the library's root
    box.title = box._parts.map(p => p.name).join(' › ');
    drawPath(box, 0);
    return box;
  }
  function drawPath(box, omit) {  // omit = how many folders after the first are replaced by "…"
    const parts = box._parts, last = parts.length - 1;
    box.replaceChildren();
    parts.forEach((p, i) => {
      if (i > 0 && i <= omit && i < last) {
        if (i === 1) {
          const more = el('span', 'gmore', '…');
          more.title = parts.slice(1, Math.min(omit, last - 1) + 1).map(x => x.name).join(' › ');
          box.append(el('span', 'gsep', '›'), more);
        }
        return;
      }
      if (i > 0) box.append(el('span', 'gsep', '›'));
      const b = el('button', i === last ? 'gtitle' : 'gcrumb', p.name);
      b.type = 'button';
      b.dataset.path = p.path;
      box.append(b);
    });
  }
  function fitPath(box) {
    if (!box.clientWidth) return;
    box.classList.add('measuring');  // the last folder may not shrink while we look for a version that fits
    const middle = Math.max(0, box._parts.length - 2);
    for (let omit = 0; omit <= middle; omit++) {
      drawPath(box, omit);
      if (box.scrollWidth <= box.clientWidth) break;
    }
    box.classList.remove('measuring');  // if even "first › … › last" is too long, CSS ellipsizes the last name
  }
  // Fit a title when it comes on screen, and again when the layout width changes.
  const onScreen = new Set();
  const pathIO = new IntersectionObserver(entries => {
    for (const e of entries) {
      if (e.isIntersecting) { onScreen.add(e.target); fitPath(e.target); } else onScreen.delete(e.target);
    }
  });
  new ResizeObserver(() => { for (const box of onScreen) fitPath(box); }).observe($('#groups'));

  function go(path) {
    const hash = '#' + encodeURIComponent(path);
    if (location.hash !== hash) location.hash = hash; else applyHash();
  }
  function applyHash() {
    const path = decodeURIComponent(location.hash.slice(1));
    state.scope = state.nodes.has(path) ? path : '';
    markActive(state.scope);
    render();
    $('#main').scrollTop = 0;
    window.scrollTo(0, 0);
  }

  // ---------------------------------------------------------------- libraries
  function fillLibrarySelect() {
    const sel = $('#library');
    sel.replaceChildren(...state.libs.map(l => new Option(l.name, l.id)));
    sel.value = state.lib;
  }

  async function loadLibraries() {
    const j = await api('/api/libraries');
    state.libs = j.libraries;
    if (!state.libs.some(l => l.id === state.lib)) state.lib = j.active;
    fillLibrarySelect();
    renderLibList();
  }

  // Load the current library's items. keepView: refresh in place (scope and scroll stay) while it is being indexed.
  async function loadLibrary(keepView = false) {
    const lib = await api('/api/library?lib=' + encodeURIComponent(state.lib));
    state.info = lib;
    state.items = lib.items;
    for (const it of lib.items) it.ext = extOf(it.name);
    state.byId = new Map(lib.items.map(it => [it.id, it]));
    rebuildView(keepView);
    renderTypes();
    updateBanner();
  }

  // Apply the type filter, then rebuild the folder tree (its counts follow the filter) and the grid.
  function rebuildView(keepView) {
    state.shown = state.hidden.size ? state.items.filter(it => !state.hidden.has(it.ext)) : state.items;
    const { root, nodes } = buildTree(state.shown);
    state.tree = root;
    state.nodes = nodes;
    renderTree();
    if (keepView && nodes.has(state.scope)) { markActive(state.scope); render(); } else applyHash();
  }

  // ---------------------------------------------------------------- file type filter
  let typesSig = '';
  function renderTypes() {
    const counts = new Map();
    for (const it of state.items) {
      const c = counts.get(it.ext) || { n: 0, kind: it.kind };
      c.n++;
      counts.set(it.ext, c);
    }
    const rows = [...counts].sort((a, b) => (a[1].kind === b[1].kind ? b[1].n - a[1].n : a[1].kind === 'video' ? -1 : 1));
    const present = rows.map(([ext]) => ext);
    const off = present.filter(ext => state.hidden.has(ext)).length;
    $('#types-label').textContent = off ? `Types · ${off} hidden` : 'Types';
    $('#types-btn').classList.toggle('on', off > 0);
    $('#types-all').disabled = !state.hidden.size;
    $('#types-common').disabled = sameSet(state.hidden, defaultHidden());
    // Rebuilding the rows under an open menu would drop the focused checkbox; only do it when the list changed.
    const sig = rows.map(([ext, c]) => `${ext}:${c.n}`).join(',');
    if (sig === typesSig && !$('#types-menu').hidden) {
      for (const cb of document.querySelectorAll('#types-list input')) cb.checked = !state.hidden.has(cb.dataset.ext);
      return;
    }
    typesSig = sig;
    $('#types-list').replaceChildren(...rows.map(([ext, c]) => {
      const label = el('label', 'type-row');
      const cb = el('input');
      cb.type = 'checkbox';
      cb.dataset.ext = ext;
      cb.checked = !state.hidden.has(ext);
      cb.addEventListener('change', () => {
        if (cb.checked) state.hidden.delete(ext); else state.hidden.add(ext);
        typesChanged();
      });
      label.append(cb, el('span', 'type-name', ext ? '.' + ext : '(no extension)'), el('span', 'tag', c.kind), el('span', 'count', num(c.n)));
      return label;
    }));
  }
  function typesChanged() {
    store.set('hiddenTypes', JSON.stringify([...state.hidden]));
    rebuildView(true);
    renderTypes();
  }
  function openTypes() {
    const btn = $('#types-btn'), menu = $('#types-menu'), r = btn.getBoundingClientRect();
    menu.hidden = false;
    menu.style.top = r.bottom + 6 + 'px';
    menu.style.left = Math.max(16, Math.min(r.left, innerWidth - menu.offsetWidth - 16)) + 'px';
    btn.setAttribute('aria-expanded', 'true');
  }
  function closeTypes(refocus = false) {
    if ($('#types-menu').hidden) return;
    $('#types-menu').hidden = true;
    $('#types-btn').setAttribute('aria-expanded', 'false');
    if (refocus) $('#types-btn').focus();
  }

  async function switchLibrary(id) {
    state.lib = id;
    $('#library').value = id;
    api('/api/libraries/active', { id }).catch(() => {});
    if (location.hash.length > 1) history.replaceState(null, '', location.pathname);
    state.q = '';
    $('#q').value = '';
    await loadLibrary();
    renderLibList();
  }

  async function startIndex(id) {
    try {
      state.jobs[id] = await api('/api/index', { id });
      updateBanner();
      renderLibList();
      poll();
    } catch (e) {
      toast('Could not start indexing: ' + e.message, true);
    }
  }

  function updateBanner() {
    const banner = $('#banner'), text = $('#banner-text'), meter = $('#banner-meter'), action = $('#banner-action');
    const job = state.jobs[state.lib], info = state.info;
    if (!info) return;
    const pending = state.items.filter(it => it.kind === 'video' && !it.frames && !it.error).length;
    let msg = '', act = '';
    updateRing();
    if (running(job)) {
      // Progress lives in the header ring; only the "someone else is indexing this" notice needs words here.
      if (job.state === 'waiting') msg = job.line;
    } else if (job && job.state === 'error') {
      msg = 'Indexing failed: ' + job.line; act = 'Retry';
    } else if (info.type === 'local' && !info.reachable) {
      msg = `Folder not reachable: ${info.location}. Covers are shown from the last index; playback needs the folder back.`;
    } else if (!state.items.length) {
      msg = info.updated ? 'No media files were found in this library.' : 'This library has not been indexed yet.'; act = info.updated ? 'Scan again' : 'Index now';
    } else if (pending) {
      msg = `${num(pending)} of ${num(state.items.length)} files have no preview yet.`; act = 'Index now';
    }
    banner.hidden = !msg;
    text.textContent = msg;
    meter.hidden = true;
    action.hidden = !act;
    action.textContent = act;
    const warn = $('#warnings');   // problems the last scan found in the library's folders
    warn.replaceChildren(...(info.warnings || []).map(w => el('li', null, w)));
    warn.hidden = !warn.children.length;
  }

  // Header ring: percentage of the open library's indexing run (or, failing that, of any other running one).
  function updateRing() {
    const ring = $('#ring');
    let id = state.lib, job = state.jobs[id];
    if (!running(job)) {
      id = Object.keys(state.jobs).find(k => running(state.jobs[k]));
      job = id && state.jobs[id];
    }
    if (!running(job)) { ring.hidden = true; return; }
    const name = (state.libs.find(l => l.id === id) || {}).name || id;
    const busy = job.state !== 'indexing' || !job.total;
    const pct = busy ? 0 : Math.floor(job.done / job.total * 100);
    ring.hidden = false;
    ring.classList.toggle('busy', busy);
    $('#ring-bar').setAttribute('stroke-dasharray', `${busy ? 25 : pct} 100`);
    $('#ring-text').textContent = busy ? '' : pct + '%';
    const text = job.state === 'waiting' ? `Waiting to index ${name}` : job.state === 'listing' ? `Scanning ${name}`
      : `Indexing ${name}: ${pct}%, ${num(job.done)} of ${num(job.total)} files` + (job.errors ? `, ${num(job.errors)} errors` : '');
    ring.title = text;
    ring.setAttribute('aria-label', text + '. Open libraries');
  }

  function renderLibList() {
    const ul = $('#lib-list');
    ul.replaceChildren(...state.libs.map(l => {
      const job = state.jobs[l.id];
      const li = el('li', 'lib');
      const main = el('div', 'lib-main');
      const name = el('div', 'lib-name', l.name);
      name.append(el('span', 'tag', l.type === 'local' ? 'Local folder' : 'S3 (rclone)'));
      if (l.id === state.lib) name.append(el('span', 'tag current', 'Open'));
      const meta = el('div', 'lib-meta');
      if (running(job)) {
        const m = el('div', 'meter'), bar = el('i');
        bar.style.width = (job.state === 'indexing' && job.total ? job.done / job.total * 100 : 0) + '%';
        m.append(bar);
        meta.append(job.state === 'waiting' ? 'Waiting for another indexer to finish…' : job.state === 'listing' ? 'Scanning…'
          : `Indexing ${num(job.done)} of ${num(job.total)}`, m);
      } else {
        meta.textContent = job && job.state === 'error' ? 'Indexing failed: ' + job.line
          : l.type === 'local' && !l.reachable ? 'Folder not reachable'
          : l.updated ? `${num(l.items)} items · indexed ${l.updated.slice(0, 16).replace('T', ' ')}` : 'Not indexed yet';
      }
      main.append(name, el('div', 'lib-loc', l.location), meta);
      const acts = el('div', 'lib-actions');
      const btn = (label, fn, cls = '') => {
        const b = el('button', 'btn small ' + cls, label);
        b.type = 'button';
        b.addEventListener('click', fn);
        acts.append(b);
        return b;
      };
      btn('Open', () => { switchLibrary(l.id); $('#libs').close(); }).disabled = l.id === state.lib;
      btn(l.updated ? 'Update index' : 'Index', () => startIndex(l.id)).disabled = running(job);
      btn('Remove', () => removeLibrary(l), 'danger').disabled = state.libs.length < 2 || running(job);
      li.append(main, acts);
      return li;
    }));
  }

  async function removeLibrary(l) {
    if (!confirm(`Remove “${l.name}” from the library list?\n\nIts index and thumbnails are deleted. Your media files are not touched.`)) return;
    try {
      const j = await api('/api/libraries/remove', { id: l.id });
      const wasOpen = l.id === state.lib;
      if (wasOpen) state.lib = j.active;
      await loadLibraries();
      if (wasOpen) await switchLibrary(state.lib);
    } catch (e) {
      toast(e.message, true);
    }
  }

  // Poll indexing progress while anything runs; refresh the open library's covers as they land.
  let pollTimer, lastRefresh = 0;
  async function poll() {
    clearTimeout(pollTimer);
    let jobs;
    try { jobs = (await api('/api/index')).jobs; } catch { pollTimer = setTimeout(poll, 4000); return; }
    const before = state.jobs;
    state.jobs = jobs;
    const now = jobs[state.lib];
    const finished = running(before[state.lib]) && !running(now);
    if (finished || (running(now) && now.state === 'indexing' && Date.now() - lastRefresh > 8000)) {
      lastRefresh = Date.now();
      await loadLibrary(true).catch(() => {});
    }
    // A finished job changes that library's item count and "indexed" time in the list.
    if (Object.keys(jobs).some(id => running(before[id]) && !running(jobs[id]))) await loadLibraries().catch(() => {});
    updateBanner();
    renderLibList();
    if (finished && now.state === 'done') toast('Indexing finished');
    if (Object.values(jobs).some(running)) pollTimer = setTimeout(poll, 1500);
  }

  // ---------------------------------------------------------------- hover scrub (delegated)
  let scrubbing = null;
  function stopScrub() {
    if (!scrubbing) return;
    const { cover, it } = scrubbing;
    cover.classList.remove('scrubbing');
    const img = cover.querySelector('img');
    if (img) img.src = thumb(it, it.cover ?? 0);
    cover.querySelectorAll('.ticks i').forEach(t => t.classList.remove('on'));
    scrubbing = null;
  }
  function onPointerMove(e) {
    if (e.pointerType !== 'mouse') return;
    const cover = e.target.closest('.cover');
    if (!cover) return stopScrub();
    const it = state.byId.get(cover.closest('.card').dataset.id);
    if (!it || it.frames < 2) return;
    if (!scrubbing || scrubbing.cover !== cover) {
      stopScrub();
      scrubbing = { cover, it, idx: -1 };
      if (!cover.dataset.pre) {
        cover.dataset.pre = '1';
        for (let i = 0; i < it.frames; i++) new Image().src = thumb(it, i);
      }
      cover.classList.add('scrubbing');
    }
    const r = cover.getBoundingClientRect();
    const idx = Math.min(it.frames - 1, Math.max(0, Math.floor((e.clientX - r.left) / r.width * it.frames)));
    if (idx !== scrubbing.idx) {
      scrubbing.idx = idx;
      cover.querySelector('img').src = thumb(it, idx);
      cover.querySelectorAll('.ticks i').forEach((t, j) => t.classList.toggle('on', j === idx));
    }
  }

  // ---------------------------------------------------------------- nav drawer (narrow screens)
  function openNav() { $('#nav').classList.add('open'); $('#scrim').hidden = false; $('#nav-toggle').setAttribute('aria-expanded', 'true'); }
  function closeNav() { $('#nav').classList.remove('open'); $('#scrim').hidden = true; $('#nav-toggle').setAttribute('aria-expanded', 'false'); }

  // ---------------------------------------------------------------- boot
  function setSize(size) {
    const px = { s: '170px', m: '230px', l: '320px' }[size] || '230px';
    document.documentElement.style.setProperty('--card-min', px);
    document.querySelectorAll('.sizes button').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.size === size)));
    store.set('size', size);
  }

  async function boot() {
    const [pl] = await Promise.all([api('/api/players'), loadLibraries()]);
    const sel = $('#player');
    for (const p of pl.players) sel.append(new Option(p.name, p.id));
    const saved = store.get('player', pl.default);
    sel.value = pl.players.some(p => p.id === saved) ? saved : (pl.players[0] || {}).id;
    sel.addEventListener('change', () => store.set('player', sel.value));
    await loadLibrary();
    poll();
  }

  $('#sort').value = state.sort;
  $('#sort').addEventListener('change', e => { state.sort = e.target.value; store.set('sort', state.sort); render(); });
  let qTimer;
  $('#q').addEventListener('input', e => { clearTimeout(qTimer); qTimer = setTimeout(() => { state.q = e.target.value; render(); }, 150); });
  document.addEventListener('keydown', e => {
    if (e.key === '/' && !/^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName) && !$('#libs').open) { e.preventDefault(); $('#q').focus(); }
    if (e.key === 'Escape') { closeNav(); closeTypes(true); }
  });
  document.querySelectorAll('.sizes button').forEach(b => b.addEventListener('click', () => setSize(b.dataset.size)));
  setSize(store.get('size', 'm'));
  $('#play-all').addEventListener('click', () => play(visibleIds));
  $('#copy-playlist').addEventListener('click', () =>
    copy(`${location.origin}/api/playlist.m3u8?lib=${encodeURIComponent(state.lib)}&dir=${encodeURIComponent(state.scope)}` +
      (state.hidden.size ? '&hide=' + encodeURIComponent([...state.hidden].join(',')) : ''), 'Playlist URL'));
  $('#types-btn').addEventListener('click', () => ($('#types-menu').hidden ? openTypes() : closeTypes()));
  $('#types-all').addEventListener('click', () => { state.hidden.clear(); typesChanged(); });
  $('#types-common').addEventListener('click', () => { state.hidden = defaultHidden(); typesChanged(); });
  document.addEventListener('pointerdown', e => { if (!e.target.closest('.types')) closeTypes(); });
  window.addEventListener('resize', () => closeTypes());
  $('#banner-action').addEventListener('click', () => startIndex(state.lib));
  $('#nav-toggle').addEventListener('click', openNav);
  $('#nav-close').addEventListener('click', closeNav);
  $('#scrim').addEventListener('click', closeNav);
  window.addEventListener('hashchange', applyHash);

  $('#library').addEventListener('change', e => switchLibrary(e.target.value));
  $('#ring').addEventListener('click', () => $('#manage').click());
  $('#manage').addEventListener('click', async () => { await loadLibraries().catch(() => {}); $('#libs').showModal(); });
  $('#libs-close').addEventListener('click', () => $('#libs').close());
  $('#libs').addEventListener('click', e => { if (e.target === e.currentTarget) e.currentTarget.close(); }); // backdrop
  $('#browse').addEventListener('click', async e => {
    const b = e.currentTarget;
    b.disabled = true;
    b.textContent = 'Pick in the dialog…';
    try {
      const j = await api('/api/pick-folder', {});
      if (j.path) { $('#add-path').value = j.path; $('#add-error').hidden = true; }
    } catch (err) {
      showAddError(err.message);
    }
    b.disabled = false;
    b.textContent = 'Browse…';
  });
  function showAddError(msg) { const p = $('#add-error'); p.textContent = msg; p.hidden = false; }
  $('#add-form').addEventListener('submit', async e => {
    e.preventDefault();
    $('#add-error').hidden = true;
    const submit = $('#add-submit');
    submit.disabled = true;
    try {
      const lib = await api('/api/libraries', { path: $('#add-path').value, name: $('#add-name').value });
      e.target.reset();
      await loadLibraries();
      await switchLibrary(lib.id);
      $('#libs').close();
      startIndex(lib.id);
    } catch (err) {
      showAddError(err.message);
    }
    submit.disabled = false;
  });

  const groupsEl = $('#groups');
  groupsEl.addEventListener('click', e => {
    const crumb = e.target.closest('.gpath button');
    if (crumb) { if (crumb.dataset.path !== state.scope) go(crumb.dataset.path); return; }
    const cardEl = e.target.closest('.card');
    if (!cardEl) return;
    const it = state.byId.get(cardEl.dataset.id);
    if (e.target.closest('.cover')) play([it.id]);
    else if (e.target.closest('.copy')) copy(mediaUrl(it), 'Stream URL');
  });
  groupsEl.addEventListener('pointermove', onPointerMove);
  groupsEl.addEventListener('pointerleave', stopScrub);

  boot().catch(e => toast('Could not load the library: ' + e.message, true));
})();
