// Settings: appearance, updates, indexing, players, tools, and where medialib keeps its files. Every change is saved
// in config.json at once; nothing here needs a restart.
import { h, fill } from '../lib/dom.js';
import { icon } from '../lib/icons.js';
import { get, post, put, del } from '../lib/api.js';
import { ago, bytes } from '../lib/fmt.js';
import { LANGUAGES, chosen, setLanguage, t } from '../lib/i18n.js';
import { loadPlayers, loadUpdate, on, pokeWatcher, state } from '../lib/state.js';
import { markdown } from '../lib/markdown.js';
import { store } from '../lib/store.js';
import { setTheme } from '../shell/sidebar.js';
import { confirmDialog, modal, promptDialog, toast, toastError } from '../lib/ui.js';

const INTERVALS = [[0, t('settings.intervalOff')], [15, t('settings.interval15m')], [30, t('settings.interval30m')], [60, t('settings.interval1h')], [180, t('settings.interval3h')], [360, t('settings.interval6h')], [1440, t('settings.intervalDaily')]];
const QUALITY = [[0, t('settings.qualityBalanced')], [45, t('settings.qualitySmaller')], [85, t('settings.qualitySharper')]];
const WORKERS = [[0, t('settings.workersAuto')], [2, '2'], [4, '4'], [8, '8'], [16, '16'], [32, '32']];
const UPDATES = [['off', t('settings.updatesOff')], ['notify', t('settings.updatesNotify')], ['auto', t('settings.updatesAuto')]];
const TOOLS = [
  ['ffmpeg', t('settings.toolFfmpeg')],
  ['ffprobe', t('settings.toolFfprobe')],
  ['rclone', t('settings.toolRclone')],
];
// The system medialib runs on, by the name people know it ('windows/amd64' is Windows).
const osName = platform => ({ windows: 'Windows', darwin: 'macOS', linux: 'Linux', freebsd: 'FreeBSD' })[(platform || '').split('/')[0]] || platform;
const base = p => p.split(/[\\/]/).pop().replace(/\.(exe|app)$/i, '');

export async function mount(root, parts = []) {
  const body = h('div');
  root.append(h('div.page', h('div.page-inner',
    h('div.page-head', h('div', h('h1', t('settings.title')), h('p', t('settings.subtitle')))), body)));

  let info = null, upd = null, moving = null, poll = null, installing = false;
  // One bar for the whole update, kept across renders so it glides from value to value instead of starting over.
  const bar = h('progress.set-progress', { max: 100 });
  const setBar = pct => { if (pct === null) bar.removeAttribute('value'); else bar.value = pct; return bar; };
  const offs = [];
  const editable = () => !!info?.can_edit;
  const here = () => !!info?.on_machine; // dialogs and folders open on this screen

  async function refresh() {
    try { info = await get('/api/system'); state.system = info; } catch (e) { info = null; }
    upd = info && info.can_edit ? await loadUpdate() : null;
    render();
  }
  const saved = j => { info = j; state.system = j; render(); toast(t('settings.saved'), { kind: 'ok', ms: 1500 }); };
  async function save(patch) {
    try { saved(await put('/api/settings', patch)); } catch (e) { toastError(t('settings.couldNotSave'), e); render(); }
  }

  // ---------------------------------------------------------------- pieces
  const row = (name, hint, ...right) => h('div.set-row', h('div.grow', h('div.set-name', name), hint ? h('div.muted', hint) : null), ...right);
  // A <select> of [value, label] pairs; a value set by hand in config.json that is not one of them is shown as other(value).
  const choice = (list, value, onChange, label, other = String) => {
    const known = list.some(([v]) => v === value);
    const sel = h('select', { 'aria-label': label, disabled: !editable() },
      [...list, ...(known ? [] : [[value, other(value)]])].map(([v, label]) => h('option', { value: String(v) }, label)));
    sel.value = String(value);
    sel.addEventListener('change', () => onChange(typeof value === 'number' ? Number(sel.value) : sel.value));
    return sel;
  };
  const btn = (label, onClick, { primary, icon: ic, title, disabled } = {}) =>
    h('button.btn.small', { type: 'button', class: primary ? 'primary' : '', title, disabled, onclick: onClick }, ic ? icon(ic, 'sm') : null, label);

  // ---------------------------------------------------------------- appearance
  function appearance() {
    const theme = store.get('theme', 'system');
    const seg = h('div.seg', [['system', t('settings.themeSystem')], ['light', t('settings.themeLight')], ['dark', t('settings.themeDark')]].map(([mode, label]) => h('button', { type: 'button', 'aria-pressed': String(mode === theme),
      onclick: () => { setTheme(mode); render(); } }, label)));
    // The language belongs to this browser, like the theme: someone else on another device keeps theirs.
    const language = h('select', { 'aria-label': t('settings.language') },
      [['', t('settings.languageSystem')], ...LANGUAGES].map(([code, name]) => h('option', { value: code, lang: code || null }, name)));
    language.value = chosen;
    language.addEventListener('change', () => setLanguage(language.value));
    return [h('div.section-title#appearance', t('settings.appearance')), h('div.card-box.set-card', row(t('settings.theme'), t('settings.themeHint'), seg),
      row(t('settings.language'), t('settings.languageHint'), language))];
  }

  // ---------------------------------------------------------------- updates (shown in About)
  function updates() {
    if (!upd) return [];
    const rows = [];
    if (upd.state === 'downloading') {
      // done and total are left out of the JSON while 0; with no size known, the bar just shows that something is happening.
      const done = upd.done || 0, total = upd.total || 0;
      const pct = total ? Math.min(100, Math.round(100 * done / total)) : null;
      const hint = total && done >= total ? t('settings.updatePlacing')
        : pct === null ? (done ? t('settings.updateSoFar', { size: bytes(done) }) : t('settings.updateStarting')) : t('settings.updateProgress', { done: bytes(done), total: bytes(total), percent: pct });
      rows.push(row(t('settings.updateDownloading', { version: upd.latest }), hint, setBar(pct)));
    } else if (upd.ready && installing) {
      rows.push(row(t('settings.updateRestarting', { version: upd.latest }), t('settings.updateRestartingHint'), setBar(100)));
    } else if (upd.ready) {
      rows.push(row(t('settings.updateReady', { version: upd.latest }), t('settings.updateReadyHint'),
        upd.notes ? btn(t('settings.whatsNew'), () => notes(upd)) : null,
        here() ? btn(t('settings.restartNow'), () => install(), { primary: true, icon: 'refresh' }) : null));
    } else if (upd.available) {
      // A download that failed leaves the update on offer: say why it stopped, and Update now tries again.
      const hint = upd.state === 'error' && upd.error ? upd.error
        : upd.can_install ? (upd.published ? t('settings.youHaveReleased', { version: upd.current, when: ago(upd.published) }) : t('settings.youHave', { version: upd.current })) : upd.why;
      rows.push(row(t('settings.updateAvailable', { version: upd.latest }), hint,
        upd.notes ? btn(t('settings.whatsNew'), () => notes(upd)) : null,
        upd.can_install && here() ? btn(t('settings.updateNow'), () => install(), { primary: true, icon: 'download' })
          : upd.page ? h('a.btn.small', { href: upd.page, target: '_blank', rel: 'noopener' }, icon('external', 'sm'), t('settings.download')) : null));
    } else if (upd.state === 'error') {
      rows.push(row(t('settings.couldNotCheck'), upd.error, btn(t('common.retry'), () => check())));
    } else {
      rows.push(row(upd.latest ? t('settings.upToDate') : t('settings.updates'), upd.checked ? t('settings.versionChecked', { version: upd.current, when: ago(upd.checked) }) : t('settings.version', { version: upd.current }),
        btn(t('settings.checkNow'), () => check(), { icon: 'refresh' })));
    }
    rows.push(row(t('settings.autoUpdates'), info.mode === 'desktop' ? t('settings.autoUpdatesDesktopHint')
      : t('settings.autoUpdatesServerHint'),
    choice(info.mode === 'desktop' ? UPDATES : UPDATES.slice(0, 2), info.updates === 'auto' && info.mode !== 'desktop' ? 'notify' : info.updates, v => save({ updates: v }), t('settings.autoUpdates'))));
    return rows;
  }
  async function check() {
    try { upd = await post('/api/update', { action: 'check' }); state.update = upd; } catch (e) { toastError(t('settings.couldNotCheck'), e); }
    render();
    if (upd && upd.available && !upd.ready && upd.state !== 'downloading' && (upd.notes || (upd.releases || []).length)) notes(upd); // asked for: say what the new version brings
  }
  async function install() {
    try {
      setBar(0);
      upd = await post('/api/update', { action: 'install' });
      installing = true;
      render();
      watchUpdate();
    } catch (e) { toastError(t('settings.couldNotUpdate'), e); }
  }
  // While the update downloads: its progress, read often (a release is a few MB and can arrive in under a second);
  // then the app closes and the new version opens by itself.
  function watchUpdate() {
    clearInterval(poll);
    poll = setInterval(async () => {
      try { upd = await get('/api/update'); state.update = upd; render(); } catch { clearInterval(poll); toast(t('settings.restartingNew'), { ms: 10000 }); return; }
      if (upd.state === 'error') { clearInterval(poll); installing = false; render(); toastError(t('settings.updateFailed'), upd.error); }
    }, 250);
  }
  // What changed in every version since this one, newest first.
  function notes(u) {
    const list = u.releases && u.releases.length ? u.releases : [{ version: u.latest, notes: u.notes, published: u.published, page: u.page }];
    const title = list.length > 1 ? t('settings.whatsNewSince', { version: u.current }) : t('settings.whatsNewIn', { version: list[0].version });
    const page = list[0].page || u.page;
    const body = r => {
      // "Changes since v3.3.1" says again what the version heading does; the downloads table is the release page's.
      const els = markdown((r.notes || '').replace(/^\s*#{1,6}\s+changes since\b.*\n/i, ''), { skip: text => /^downloads$/i.test(text.trim()) });
      return els.length ? els : h('p.muted', t('settings.noNotes'));
    };
    modal({ title, size: 'wide', body: h('div.notes', list.map(r => h('section.notes-release',
      h('h2.notes-version', t('settings.version', { version: r.version }),
        r.published ? h('span.notes-when', t('settings.notesReleased', { when: ago(r.published) })) : null),
      body(r)))),
      actions: [page ? { label: t('settings.openOnGitHub'), left: true, onClick: () => window.open(page, '_blank') } : null,
        canUpdate(u) ? { label: t('common.close') } : { label: t('common.close'), primary: true },
        canUpdate(u) ? { label: t('settings.updateTo', { version: u.latest }), primary: true, onClick: () => install() } : null].filter(Boolean) });
  }
  // Whether this page can update medialib right now: a newer version this copy installs itself, on this computer.
  function canUpdate(u) {
    return u.available && u.can_install && !u.ready && u.state !== 'downloading' && here();
  }

  // ---------------------------------------------------------------- indexing
  function indexing() {
    const auto = info.auto_index_env
      ? h('span.mono', { title: t('settings.setByEnv') }, info.auto_index ? t('settings.everyMin', { minutes: info.auto_index }) : t('settings.intervalOff'))
      : choice(INTERVALS, info.auto_index, v => save({ auto_index: v }), t('settings.autoIndex'), v => t('settings.everyMinutes', { minutes: v }));
    return [h('div.section-title#scanning', t('settings.indexing')), h('div.card-box.set-card',
      row(t('settings.autoIndex'), info.auto_index_env ? t('settings.autoIndexEnvHint') : t('settings.autoIndexHint'), auto),
      row(t('settings.coverQuality'), t('settings.coverQualityHint'), choice(QUALITY, info.thumb_quality || 0, v => save({ thumb_quality: v }), t('settings.coverQuality'), v => t('settings.qualityValue', { value: v }))),
      row(t('settings.workers'), t('settings.workersHint'), choice(WORKERS, info.workers, v => save({ workers: v }), t('settings.workers'))))];
  }

  // ---------------------------------------------------------------- players
  function playersCard() {
    const all = info.players;
    const def = all.find(p => p.id === info.default_player && !p.hidden && !p.missing) ? info.default_player : (all.find(p => !p.hidden && !p.missing) || {}).id;
    const hidden = all.filter(p => p.hidden);
    const rows = all.filter(p => !p.hidden).map(p => h('div.set-row.player-row',
      h('button.radio', { type: 'button', role: 'radio', 'aria-checked': String(p.id === def), 'aria-label': t('settings.useNameByDefault', { name: p.name }), title: t('settings.useByDefault'),
        disabled: !editable() || p.missing, onclick: () => p.id !== def && save({ default_player: p.id }) }),
      h('div.grow', h('div.set-name', p.name, p.id === def ? h('span.tag.accent', t('settings.defaultTag')) : null, p.custom ? h('span.tag', t('settings.addedTag')) : null),
        p.missing ? h('div.bad-text', icon('alert', 'sm'), t('settings.playerNotFound', { path: p.path })) : p.path ? h('code.mono.set-path', p.path) : h('div.muted', t('settings.systemPlayerHint'))),
      p.id !== 'system' && editable() && here() ? h('button.icon-btn', { type: 'button', 'aria-label': t('settings.removeName', { name: p.name }), title: t('settings.removeFromList'), onclick: () => removePlayer(p) }, icon('trash', 'sm')) : null));
    const foot = editable() && here() ? h('div.set-row.set-foot',
      btn(t('settings.addPlayerButton'), () => addPlayer(), { icon: 'plus' }),
      btn(t('settings.lookAgain'), async () => { try { await loadPlayers(await post('/api/players/detect', {})); await refresh(); toast(t('settings.lookedAgain'), { kind: 'ok' }); } catch (e) { toastError(t('settings.couldNotLookPlayers'), e); } },
        { icon: 'refresh', title: t('settings.lookAgainHint') }),
      hidden.length ? btn(t('settings.bringBack', { count: hidden.length }), () => restorePlayers(), { title: hidden.map(p => p.name).join(', ') }) : null) : null;
    return [h('div.section-title#players', t('settings.players')), h('div.card-box.set-card', { role: 'radiogroup', 'aria-label': t('settings.defaultPlayer') }, rows, foot)];
  }
  async function afterPlayers(j) { await loadPlayers(j); await refresh(); }
  async function removePlayer(p) {
    const ok = await confirmDialog({ title: t('settings.removePlayerTitle', { name: p.name }), message: p.custom ? t('settings.removeCustomMessage', { name: p.name }) : t('settings.removeFoundMessage', { name: p.name }),
      detail: p.custom ? null : t('settings.removeFoundDetail'), confirm: t('common.remove'), danger: true });
    if (!ok) return;
    try { await afterPlayers(await del('/api/players/' + encodeURIComponent(p.id))); } catch (e) { toastError(t('settings.couldNotRemovePlayer'), e); }
  }
  async function restorePlayers() {
    try { await afterPlayers(await post('/api/players/restore', {})); } catch (e) { toastError(t('settings.couldNotRestorePlayers'), e); }
  }
  function addPlayer() {
    const name = h('input.input', { placeholder: 'mpv, VLC, PotPlayer…', autocomplete: 'off' });
    const path = h('input.input.mono', { placeholder: info.platform.startsWith('windows') ? 'C:\\Program Files\\…\\player.exe' : t('settings.playerPathPlaceholder'), autocomplete: 'off', spellcheck: 'false' });
    const args = h('input.input.mono', { placeholder: t('settings.argsPlaceholder'), autocomplete: 'off', spellcheck: 'false' });
    const err = h('p.form-error', { hidden: true });
    const browse = h('button.btn', { type: 'button', onclick: async () => {
      try {
        const j = await post('/api/pick-file', { title: t('settings.choosePlayerProgram') });
        if (j.path) { path.value = j.path; if (!name.value.trim()) name.value = base(j.path); }
      } catch (x) { err.textContent = x.message; err.hidden = false; }
    } }, t('settings.browse'));
    modal({ title: t('settings.addPlayerTitle'), body: [err,
      h('div.field', h('label', t('settings.program')), h('div.input-wrap', path, browse), h('p.hint', t('settings.programHint'))),
      h('div.field', h('label', t('settings.name')), name),
      h('div.field', h('label', t('settings.extraArgs')), args, h('p.hint', t('settings.extraArgsHint')))],
    actions: [{ label: t('common.cancel') }, { label: t('common.add'), primary: true, onClick: async () => {
      try { await afterPlayers(await post('/api/players', { name: name.value, path: path.value, args: args.value })); toast(t('settings.playerAdded'), { kind: 'ok' }); } catch (x) { err.textContent = x.message; err.hidden = false; return false; }
    } }] });
  }

  // ---------------------------------------------------------------- tools
  function tools() {
    return [h('div.section-title#tools', t('settings.tools')), h('div.card-box.set-card', TOOLS.map(([name, why]) => {
      const found = info[name], set = info.tools?.[name], custom = set && set !== name;
      return h('div.set-row', h('div.grow', h('div.set-name', name), h('div.muted', why), typeof found === 'string' ? h('code.mono.set-path', found) : null),
        found ? h('span.tag.ok', icon('check', 'sm'), t('settings.found')) : h('span.tag.warn', t('settings.notFound')),
        editable() ? btn(custom ? t('settings.change') : t('settings.choose'), () => chooseTool(name, set), { title: t('settings.chooseToolHint', { name }) }) : null,
        editable() && custom ? btn(t('settings.default'), () => save({ [name]: '' }), { title: t('settings.toolDefaultHint', { name }) }) : null);
    }))];
  }
  function chooseTool(name, current) {
    const path = h('input.input.mono', { value: current && current !== name ? current : '', placeholder: info.platform.startsWith('windows') ? `C:\\…\\${name}.exe` : `/usr/local/bin/${name}`, autocomplete: 'off', spellcheck: 'false' });
    const err = h('p.form-error', { hidden: true });
    const browse = here() ? h('button.btn', { type: 'button', onclick: async () => {
      try { const j = await post('/api/pick-file', { title: t('settings.chooseName', { name }) }); if (j.path) path.value = j.path; } catch (x) { err.textContent = x.message; err.hidden = false; }
    } }, t('settings.browse')) : null;
    modal({ title: t('settings.useAnother', { name }), body: [err, h('div.field', h('label', t('settings.program')), h('div.input-wrap', path, browse), h('p.hint', t('settings.useAnotherHint', { name })))],
      actions: [{ label: t('common.cancel') }, { label: t('settings.useIt'), primary: true, onClick: async () => {
        try { saved(await put('/api/settings', { [name]: path.value })); } catch (x) { err.textContent = x.message; err.hidden = false; return false; }
      } }] });
  }

  // ---------------------------------------------------------------- files
  function files() {
    if (!info.config_file) return []; // not shown to a browser that may only watch
    const task = moving && state.tasks.find(x => x.id === moving);
    const cacheHint = task && task.state === 'running' ? t('settings.moving', { done: task.done, total: task.total, size: bytes(task.bytes) }) : t('settings.cacheHint');
    return [h('div.section-title#files', t('settings.files')), h('div.card-box.set-card',
      h('div.set-row', h('div.grow', h('div.set-name', t('settings.cache')), h('div.muted', cacheHint), h('code.mono.set-path', info.cache_dir)),
        here() ? btn(t('common.open'), () => openFolder('cache'), { icon: 'folder' }) : null,
        editable() ? btn(t('settings.move'), () => moveCache(), { disabled: !!(task && task.state === 'running') }) : null,
        editable() && info.cache_custom ? btn(t('settings.default'), () => moveCache(true), { title: t('settings.cacheDefaultHint'), disabled: !!(task && task.state === 'running') }) : null),
      h('div.set-row', h('div.grow', h('div.set-name', t('settings.title')), h('div.muted', t('settings.configHint')), h('code.mono.set-path', info.config_file)),
        here() ? btn(t('common.open'), () => openFolder('config'), { icon: 'folder' }) : null))];
  }
  async function openFolder(what) {
    try { await post('/api/open-folder', { what }); } catch (e) { toastError(t('settings.couldNotOpenFolder'), e); }
  }
  async function moveCache(toDefault = false) {
    let path = '';
    if (!toDefault) {
      if (here()) {
        try { path = (await post('/api/pick-folder', { title: t('settings.chooseCacheFolder') })).path; } catch (e) { path = null; }
      }
      if (path === null || (!here() && !path)) path = await promptDialog({ title: t('settings.moveCacheTitle'), label: t('settings.folder'), mono: true, confirm: t('settings.next') });
      if (!path) return;
    }
    const req = toDefault ? { default: true } : { path };
    try {
      const plan = await post('/api/settings/cache-dir', { ...req, check: true });
      const ok = await confirmDialog({ title: t('settings.moveCacheConfirm'), message: t('settings.moveCacheMessage', { count: plan.files, size: bytes(plan.bytes), folder: plan.to }),
        detail: t('settings.moveCacheDetail'), confirm: t('settings.moveConfirm') });
      if (!ok) return;
      const j = await post('/api/settings/cache-dir', req);
      moving = j.task.id;
      pokeWatcher();
      if (j.task.state !== 'running') finishMove(j.task); else render();
    } catch (e) { toastError(t('settings.couldNotMoveCache'), e); }
  }
  function finishMove(task) {
    moving = null;
    if (task.state === 'done') toast(t('settings.cacheMoved'), { kind: 'ok' });
    else toastError(t('settings.moveFailed'), task.line || task.state);
    refresh();
  }

  // ---------------------------------------------------------------- cloud storage
  function cloud() {
    const n = state.connections.length;
    return [h('div.section-title#cloud', t('settings.cloud')), h('div.card-box.set-card',
      row(t('settings.connections'), n ? t('settings.connectedStores', { count: n, names: state.connections.map(c => c.name).join(', ') }) : t('settings.cloudHint'),
        h('a.btn.small', { href: '#/connections' }, icon(n ? 'plug' : 'plus', 'sm'), n ? t('settings.manage') : t('settings.connectStore'))))];
  }

  // ---------------------------------------------------------------- about
  function about() {
    return [h('div.section-title#about', t('settings.about')), h('div.card-box.set-card',
      row('Media Library', h('span', { title: `${info.platform} · ${info.runtime}` }, info.mode === 'desktop' ? t('settings.aboutDesktop', { os: osName(info.platform) }) : t('settings.aboutServer', { os: osName(info.platform) })), h('span.mono', info.version)),
      updates())];
  }

  function render() {
    if (!info) {
      fill(body, appearance(), h('div.banner.error', h('span.grow', t('settings.couldNotRead')), h('button.btn.small', { type: 'button', onclick: () => refresh() }, icon('refresh', 'sm'), t('common.retry'))));
      return;
    }
    const jump = h('nav.set-jump', { 'aria-label': t('settings.sections') });
    fill(body,
      jump,
      editable() ? null : h('div.banner', icon('info', 'sm'), t('settings.readOnly')),
      appearance(), indexing(), playersCard(), cloud(), tools(), files(), about());
    // A row of the sections to jump to: a long page, most of it set once.
    fill(jump, [...body.querySelectorAll('.section-title[id]')].map(el => h('button', { type: 'button', onclick: () => el.scrollIntoView({ block: 'start', behavior: 'smooth' }) }, el.textContent)));
  }

  offs.push(on('activity', () => {
    const task = moving && state.tasks.find(x => x.id === moving);
    if (!task) return;
    if (task.state !== 'running') finishMove(task); else render();
  }));
  // #/settings/about (the sidebar's update button): the version and its updates.
  const show = p => { if (/^[a-z]+$/.test(p[0] || '')) requestAnimationFrame(() => body.querySelector('#' + p[0])?.scrollIntoView({ block: 'start' })); };
  await refresh();
  show(parts);
  return { update: show, destroy() { clearInterval(poll); offs.forEach(f => f()); } };
}
