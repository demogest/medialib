// Settings: appearance, updates, indexing, players, tools, and where medialib keeps its files. Every change is saved
// in config.json at once; nothing here needs a restart.
import { h, fill } from '../lib/dom.js';
import { icon } from '../lib/icons.js';
import { get, post, put, del } from '../lib/api.js';
import { ago, bytes, plural } from '../lib/fmt.js';
import { loadPlayers, loadUpdate, on, pokeWatcher, state } from '../lib/state.js';
import { store } from '../lib/store.js';
import { setTheme } from '../shell/sidebar.js';
import { confirmDialog, modal, promptDialog, toast, toastError } from '../lib/ui.js';

const INTERVALS = [[0, 'Off'], [15, 'Every 15 minutes'], [30, 'Every 30 minutes'], [60, 'Every hour'], [180, 'Every 3 hours'], [360, 'Every 6 hours'], [1440, 'Once a day']];
const QUALITY = [[0, 'Balanced'], [45, 'Smaller files'], [85, 'Sharper']];
const WORKERS = [[0, 'Automatic'], [2, '2'], [4, '4'], [8, '8'], [16, '16'], [32, '32']];
const UPDATES = [['off', 'Off'], ['notify', 'Tell me'], ['auto', 'Install automatically']];
const TOOLS = [
  ['ffmpeg', 'Decodes keyframes into covers. Required for indexing.'],
  ['ffprobe', 'Reads duration, resolution and codec.'],
  ['rclone', 'Optional: only for older libraries that read through an rclone remote.'],
];
const base = p => p.split(/[\\/]/).pop().replace(/\.(exe|app)$/i, '');

export async function mount(root) {
  const body = h('div');
  root.append(h('div.page', h('div.page-inner',
    h('div.page-head', h('div', h('h1', 'Settings'), h('p', 'How medialib looks, indexes, plays and updates.'))), body)));

  let info = null, upd = null, moving = null, poll = null;
  const offs = [];
  const editable = () => !!info?.can_edit;
  const here = () => !!info?.on_machine; // dialogs and folders open on this screen

  async function refresh() {
    try { info = await get('/api/system'); state.system = info; } catch (e) { info = null; }
    upd = info && info.can_edit ? await loadUpdate() : null;
    render();
  }
  const saved = j => { info = j; state.system = j; render(); toast('Saved', { kind: 'ok', ms: 1500 }); };
  async function save(patch) {
    try { saved(await put('/api/settings', patch)); } catch (e) { toastError('Could not save', e); render(); }
  }

  // ---------------------------------------------------------------- pieces
  const row = (name, hint, ...right) => h('div.set-row', h('div.grow', h('div.set-name', name), hint ? h('div.muted', hint) : null), ...right);
  // A <select> of [value, label] pairs; a value set by hand in config.json that is not one of them is shown as other(value).
  const choice = (list, value, onChange, label, other = String) => {
    const known = list.some(([v]) => v === value);
    const sel = h('select', { 'aria-label': label, disabled: !editable() },
      [...list, ...(known ? [] : [[value, other(value)]])].map(([v, t]) => h('option', { value: String(v) }, t)));
    sel.value = String(value);
    sel.addEventListener('change', () => onChange(typeof value === 'number' ? Number(sel.value) : sel.value));
    return sel;
  };
  const btn = (label, onClick, { primary, icon: ic, title, disabled } = {}) =>
    h('button.btn.small', { type: 'button', class: primary ? 'primary' : '', title, disabled, onclick: onClick }, ic ? icon(ic, 'sm') : null, label);

  // ---------------------------------------------------------------- appearance
  function appearance() {
    const theme = store.get('theme', 'system');
    const seg = h('div.seg', ['system', 'light', 'dark'].map(t => h('button', { type: 'button', 'aria-pressed': String(t === theme),
      onclick: () => { setTheme(t); render(); } }, t[0].toUpperCase() + t.slice(1))));
    return [h('div.section-title', 'Appearance'), h('div.card-box.set-card', row('Theme', 'Follow the system, or pick one.', seg))];
  }

  // ---------------------------------------------------------------- updates
  function updates() {
    if (!upd) return [];
    const rows = [];
    const pct = upd.total ? Math.round(100 * upd.done / upd.total) : 0;
    if (upd.state === 'downloading') {
      rows.push(row(`Downloading ${upd.latest}…`, `${pct}% of ${bytes(upd.total)}`, h('progress.set-progress', { max: 100, value: pct })));
    } else if (upd.ready) {
      rows.push(row(`Version ${upd.latest} is ready`, 'Restart medialib to finish the update.',
        here() ? btn('Restart now', () => install(), { primary: true, icon: 'refresh' }) : null));
    } else if (upd.available) {
      rows.push(row(`Version ${upd.latest} is available`, upd.can_install ? `You have ${upd.current}.${upd.published ? ' Released ' + ago(upd.published) + '.' : ''}` : upd.why,
        upd.notes ? btn('What’s new', () => notes(upd)) : null,
        upd.can_install && here() ? btn('Update now', () => install(), { primary: true, icon: 'download' })
          : upd.page ? h('a.btn.small', { href: upd.page, target: '_blank', rel: 'noopener' }, icon('external', 'sm'), 'Download') : null));
    } else if (upd.state === 'error') {
      rows.push(row('Could not look for updates', upd.error, btn('Try again', () => check())));
    } else {
      rows.push(row(upd.latest ? 'medialib is up to date' : 'Updates', upd.checked ? `Version ${upd.current} · looked ${ago(upd.checked)}` : `Version ${upd.current}`,
        btn('Check now', () => check(), { icon: 'refresh' })));
    }
    rows.push(row('Automatic updates', info.mode === 'desktop' ? 'Look for a new version every few hours; or also download it, and install it as medialib closes.'
      : 'Look for a new version every few hours and say so here. A server is updated by replacing its program (or image).',
    choice(info.mode === 'desktop' ? UPDATES : UPDATES.slice(0, 2), info.updates === 'auto' && info.mode !== 'desktop' ? 'notify' : info.updates, v => save({ updates: v }), 'Automatic updates')));
    return [h('div.section-title', 'Updates'), h('div.card-box.set-card', rows)];
  }
  async function check() {
    try { upd = await post('/api/update', { action: 'check' }); state.update = upd; } catch (e) { toastError('Could not look for updates', e); }
    render();
  }
  async function install() {
    try {
      upd = await post('/api/update', { action: 'install' });
      render();
      watchUpdate();
    } catch (e) { toastError('Could not update', e); }
  }
  // While the update downloads: its progress; then the app closes and the new version opens by itself.
  function watchUpdate() {
    clearInterval(poll);
    poll = setInterval(async () => {
      try { upd = await get('/api/update'); state.update = upd; render(); } catch { clearInterval(poll); toast('Restarting with the new version…', { ms: 10000 }); return; }
      if (upd.state === 'error') { clearInterval(poll); toastError('The update failed', upd.error); }
    }, 1000);
  }
  function notes(u) {
    modal({ title: `What’s new in ${u.latest}`, size: 'wide', body: h('div.notes', markdown(u.notes)),
      actions: [u.page ? { label: 'Open on GitHub', left: true, onClick: () => window.open(u.page, '_blank') } : null, { label: 'Close', primary: true }].filter(Boolean) });
  }

  // ---------------------------------------------------------------- indexing
  function indexing() {
    const auto = info.auto_index_env
      ? h('span.mono', { title: 'Set by MEDIALIB_AUTO_INDEX' }, info.auto_index ? `Every ${info.auto_index} min` : 'Off')
      : choice(INTERVALS, info.auto_index, v => save({ auto_index: v }), 'Automatic indexing', v => `Every ${v} minutes`);
    return [h('div.section-title', 'Indexing'), h('div.card-box.set-card',
      row('Automatic indexing', info.auto_index_env ? 'MEDIALIB_AUTO_INDEX sets it on this computer.' : 'Bring every library up to date by itself: new files get covers, removed ones go.', auto),
      row('Cover quality', 'For covers made from now on. Sharper covers take more space.', choice(QUALITY, info.thumb_quality || 0, v => save({ thumb_quality: v }), 'Cover quality', v => `Quality ${v}`)),
      row('Files at once', 'How many files are read in parallel while indexing. Automatic suits most disks and connections.', choice(WORKERS, info.workers, v => save({ workers: v }), 'Files at once')))];
  }

  // ---------------------------------------------------------------- players
  function playersCard() {
    const all = info.players;
    const def = all.find(p => p.id === info.default_player && !p.hidden && !p.missing) ? info.default_player : (all.find(p => !p.hidden && !p.missing) || {}).id;
    const hidden = all.filter(p => p.hidden);
    const rows = all.filter(p => !p.hidden).map(p => h('div.set-row.player-row',
      h('button.radio', { type: 'button', role: 'radio', 'aria-checked': String(p.id === def), 'aria-label': `Use ${p.name} by default`, title: 'Use by default',
        disabled: !editable() || p.missing, onclick: () => p.id !== def && save({ default_player: p.id }) }),
      h('div.grow', h('div.set-name', p.name, p.id === def ? h('span.tag.accent', 'Default') : null, p.custom ? h('span.tag', 'Added') : null),
        p.missing ? h('div.bad-text', icon('alert', 'sm'), 'Not found: ', p.path) : p.path ? h('code.mono.set-path', p.path) : h('div.muted', 'Opens with the program your system uses for the file')),
      p.id !== 'system' && editable() && here() ? h('button.icon-btn', { type: 'button', 'aria-label': `Remove ${p.name}`, title: 'Remove from the list', onclick: () => removePlayer(p) }, icon('trash', 'sm')) : null));
    const foot = editable() && here() ? h('div.set-row.set-foot',
      btn('Add a player…', () => addPlayer(), { icon: 'plus' }),
      btn('Look again', async () => { try { await loadPlayers(await post('/api/players/detect', {})); await refresh(); toast('Players looked for again', { kind: 'ok' }); } catch (e) { toastError('Could not look for players', e); } },
        { icon: 'refresh', title: 'Look for players installed since medialib started' }),
      hidden.length ? btn(`Bring back ${plural(hidden.length, 'removed player')}`, () => restorePlayers(), { title: hidden.map(p => p.name).join(', ') }) : null) : null;
    return [h('div.section-title', 'Players'), h('div.card-box.set-card', { role: 'radiogroup', 'aria-label': 'Default player' }, rows, foot)];
  }
  async function afterPlayers(j) { await loadPlayers(j); await refresh(); }
  async function removePlayer(p) {
    const ok = await confirmDialog({ title: `Remove ${p.name}?`, message: p.custom ? `${p.name} is taken off the list.` : `${p.name} stays installed; medialib stops offering it.`,
      detail: p.custom ? null : 'Bring it back any time from this page.', confirm: 'Remove', danger: true });
    if (!ok) return;
    try { await afterPlayers(await del('/api/players/' + encodeURIComponent(p.id))); } catch (e) { toastError('Could not remove the player', e); }
  }
  async function restorePlayers() {
    try { await afterPlayers(await post('/api/players/restore', {})); } catch (e) { toastError('Could not bring the players back', e); }
  }
  function addPlayer() {
    const name = h('input.input', { placeholder: 'mpv, VLC, PotPlayer…', autocomplete: 'off' });
    const path = h('input.input.mono', { placeholder: info.platform.startsWith('windows') ? 'C:\\Program Files\\…\\player.exe' : '/Applications/… or a program name', autocomplete: 'off', spellcheck: 'false' });
    const args = h('input.input.mono', { placeholder: 'e.g. --fullscreen', autocomplete: 'off', spellcheck: 'false' });
    const err = h('p.form-error', { hidden: true });
    const browse = h('button.btn', { type: 'button', onclick: async () => {
      try {
        const j = await post('/api/pick-file', { title: 'Choose the player’s program' });
        if (j.path) { path.value = j.path; if (!name.value.trim()) name.value = base(j.path); }
      } catch (x) { err.textContent = x.message; err.hidden = false; }
    } }, 'Browse…');
    modal({ title: 'Add a player', body: [err,
      h('div.field', h('label', 'Program'), h('div.input-wrap', path, browse), h('p.hint', 'The player’s program file, or its name if it is on the PATH.')),
      h('div.field', h('label', 'Name'), name),
      h('div.field', h('label', 'Extra arguments (optional)'), args, h('p.hint', 'Put before the file on every play.'))],
    actions: [{ label: 'Cancel' }, { label: 'Add', primary: true, onClick: async () => {
      try { await afterPlayers(await post('/api/players', { name: name.value, path: path.value, args: args.value })); toast('Player added', { kind: 'ok' }); } catch (x) { err.textContent = x.message; err.hidden = false; return false; }
    } }] });
  }

  // ---------------------------------------------------------------- tools
  function tools() {
    return [h('div.section-title', 'Tools'), h('div.card-box.set-card', TOOLS.map(([name, why]) => {
      const found = info[name], set = info.tools?.[name], custom = set && set !== name;
      return h('div.set-row', h('div.grow', h('div.set-name', name), h('div.muted', why), typeof found === 'string' ? h('code.mono.set-path', found) : null),
        found ? h('span.tag.ok', icon('check', 'sm'), 'Found') : h('span.tag.warn', 'Not found'),
        editable() ? btn(custom ? 'Change…' : 'Choose…', () => chooseTool(name, set), { title: `Use a ${name} of your choice` }) : null,
        editable() && custom ? btn('Default', () => save({ [name]: '' }), { title: `Look for ${name} on the PATH again` }) : null);
    }))];
  }
  function chooseTool(name, current) {
    const path = h('input.input.mono', { value: current && current !== name ? current : '', placeholder: info.platform.startsWith('windows') ? `C:\\…\\${name}.exe` : `/usr/local/bin/${name}`, autocomplete: 'off', spellcheck: 'false' });
    const err = h('p.form-error', { hidden: true });
    const browse = here() ? h('button.btn', { type: 'button', onclick: async () => {
      try { const j = await post('/api/pick-file', { title: `Choose ${name}` }); if (j.path) path.value = j.path; } catch (x) { err.textContent = x.message; err.hidden = false; }
    } }, 'Browse…') : null;
    modal({ title: `Use another ${name}`, body: [err, h('div.field', h('label', 'Program'), h('div.input-wrap', path, browse), h('p.hint', `Leave it empty to use the ${name} on the PATH.`))],
      actions: [{ label: 'Cancel' }, { label: 'Use it', primary: true, onClick: async () => {
        try { saved(await put('/api/settings', { [name]: path.value })); } catch (x) { err.textContent = x.message; err.hidden = false; return false; }
      } }] });
  }

  // ---------------------------------------------------------------- files
  function files() {
    if (!info.config_file) return []; // not shown to a browser that may only watch
    const t = moving && state.tasks.find(x => x.id === moving);
    const cacheHint = t && t.state === 'running' ? `Moving… ${plural(t.done, 'file')} of ${t.total} · ${bytes(t.bytes)}` : 'The index of every library and its covers. They can grow large: put them on a roomy disk.';
    return [h('div.section-title', 'Files'), h('div.card-box.set-card',
      h('div.set-row', h('div.grow', h('div.set-name', 'Index and covers'), h('div.muted', cacheHint), h('code.mono.set-path', info.cache_dir)),
        here() ? btn('Open', () => openFolder('cache'), { icon: 'folder' }) : null,
        editable() ? btn('Move…', () => moveCache(), { disabled: !!(t && t.state === 'running') }) : null,
        editable() && info.cache_custom ? btn('Default', () => moveCache(true), { title: 'Move them back beside the settings', disabled: !!(t && t.state === 'running') }) : null),
      h('div.set-row', h('div.grow', h('div.set-name', 'Settings'), h('div.muted', 'Libraries, connections and these settings (config.json). MEDIALIB_HOME picks another folder.'), h('code.mono.set-path', info.config_file)),
        here() ? btn('Open', () => openFolder('config'), { icon: 'folder' }) : null))];
  }
  async function openFolder(what) {
    try { await post('/api/open-folder', { what }); } catch (e) { toastError('Could not open the folder', e); }
  }
  async function moveCache(toDefault = false) {
    let path = '';
    if (!toDefault) {
      if (here()) {
        try { path = (await post('/api/pick-folder', { title: 'Choose where to keep the index and covers' })).path; } catch (e) { path = null; }
      }
      if (path === null || (!here() && !path)) path = await promptDialog({ title: 'Move the index and covers', label: 'Folder', mono: true, confirm: 'Next' });
      if (!path) return;
    }
    const req = toDefault ? { default: true } : { path };
    try {
      const plan = await post('/api/settings/cache-dir', { ...req, check: true });
      const ok = await confirmDialog({ title: 'Move the index and covers?', message: `${plural(plan.files, 'file')} (${bytes(plan.bytes)}) move to ${plan.to}.`,
        detail: 'Indexing waits until the move is done. Your media is not touched.', confirm: 'Move' });
      if (!ok) return;
      const j = await post('/api/settings/cache-dir', req);
      moving = j.task.id;
      pokeWatcher();
      if (j.task.state !== 'running') finishMove(j.task); else render();
    } catch (e) { toastError('Could not move the index and covers', e); }
  }
  function finishMove(t) {
    moving = null;
    if (t.state === 'done') toast('The index and covers moved', { kind: 'ok' });
    else toastError('The move did not finish', t.line || t.state);
    refresh();
  }

  // ---------------------------------------------------------------- cloud storage
  function cloud() {
    const n = state.connections.length;
    return [h('div.section-title', 'Cloud storage'), h('div.card-box.set-card',
      row('Connections', n ? `${plural(n, 'store')} connected: ${state.connections.map(c => c.name).join(', ')}.` : 'Amazon S3, Cloudflare R2, Backblaze B2, MinIO, RustFS and anything else that speaks S3: browse and manage buckets, or make a bucket folder a library.',
        h('a.btn.small', { href: '#/connections' }, icon(n ? 'plug' : 'plus', 'sm'), n ? 'Manage' : 'Connect a store')))];
  }

  // ---------------------------------------------------------------- about
  function about() {
    return [h('div.section-title', 'About'), h('div.card-box.set-card',
      row('Media Library', `${info.mode === 'desktop' ? 'Desktop app' : 'Server'} · ${info.platform} · ${info.runtime}`, h('span.mono', info.version)))];
  }

  function render() {
    if (!info) { fill(body, appearance(), h('div.banner.error', 'Could not read the settings.')); return; }
    fill(body,
      editable() ? null : h('div.banner', icon('info', 'sm'), 'Settings can be changed on the computer running medialib, or after signing in.'),
      appearance(), updates(), indexing(), playersCard(), cloud(), tools(), files(), about());
  }

  offs.push(on('activity', () => {
    const t = moving && state.tasks.find(x => x.id === moving);
    if (!t) return;
    if (t.state !== 'running') finishMove(t); else render();
  }));
  await refresh();
  return { destroy() { clearInterval(poll); offs.forEach(f => f()); } };
}

// A small part of Markdown, enough for release notes: headings, lists, paragraphs, `code` and **bold** (the
// downloads table is left to the release page). Built as elements, never as HTML.
function markdown(text) {
  const out = [];
  let list = null, para = [];
  const inline = s => s.split(/(`[^`]+`|\*\*[^*]+\*\*)/).map(p => p.startsWith('`') && p.endsWith('`') && p.length > 1 ? h('code', p.slice(1, -1))
    : p.startsWith('**') && p.endsWith('**') && p.length > 3 ? h('strong', p.slice(2, -2)) : p);
  const flush = () => { if (para.length) out.push(h('p', inline(para.join(' ')))); para = []; list = null; };
  let skip = 0; // inside the downloads section (a table of files): the release page has it
  for (const raw of (text || '').split('\n')) {
    const line = raw.trimEnd();
    let m;
    if ((m = line.match(/^(#{1,4})\s+(.*)/))) {
      flush();
      if (skip && m[1].length > skip) continue;
      skip = /^downloads$/i.test(m[2].trim()) ? m[1].length : 0;
      if (!skip) out.push(h(m[1].length <= 2 ? 'h3' : 'h4', inline(m[2])));
      continue;
    }
    if (skip) continue;
    if (!line.trim()) { flush(); continue; }
    if ((m = line.match(/^\s*[-*]\s+(.*)/))) {
      if (para.length) { out.push(h('p', inline(para.join(' ')))); para = []; }
      if (!list) out.push(list = h('ul'));
      list.append(h('li', inline(m[1])));
      continue;
    }
    if (line.startsWith('|')) continue; // tables (the downloads): the release page shows them
    if (list && /^\s+\S/.test(raw)) { list.lastChild.append(' ', ...inline(line.trim())); continue; }
    list = null;
    para.push(line.trim());
  }
  flush();
  return out;
}
