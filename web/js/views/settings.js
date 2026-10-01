// Settings: appearance and a health check of what medialib relies on.
import { h, fill } from '../lib/dom.js';
import { icon } from '../lib/icons.js';
import { get } from '../lib/api.js';
import { store } from '../lib/store.js';

export async function mount(root) {
  const body = h('div');
  root.append(h('div.page', h('div.page-inner',
    h('div.page-head', h('div', h('h1', 'Settings'), h('p', 'Appearance, and what medialib found on this computer.'))), body)));

  const theme = store.get('theme', 'system');
  const apply = t => {
    if (t === 'system') { delete document.documentElement.dataset.theme; store.set('theme', 'system'); }
    else { document.documentElement.dataset.theme = t; store.set('theme', t); }
    for (const b of seg.children) b.setAttribute('aria-pressed', String(b.dataset.t === t));
    document.dispatchEvent(new Event('themechange'));
  };
  const seg = h('div.seg', ['system', 'light', 'dark'].map(t => h('button', { type: 'button', dataset: { t }, 'aria-pressed': String(t === theme), onclick: () => apply(t) }, t[0].toUpperCase() + t.slice(1))));

  let info;
  try { info = await get('/api/system'); } catch (e) { info = null; }
  const tool = (name, path, why) => h('div.set-row', h('div.grow', h('div.set-name', name), h('div.muted', why)),
    path ? h('span.tag.ok', icon('check', 'sm'), 'Found') : h('span.tag.warn', 'Not found'), path ? h('code.mono.set-path', path) : null);

  fill(body,
    h('div.section-title', 'Appearance'),
    h('div.card-box.set-card', h('div.set-row', h('div.grow', h('div.set-name', 'Theme'), h('div.muted', 'Follow the system, or pick one.')), seg)),
    info ? [
      h('div.section-title', 'Tools'),
      h('div.card-box.set-card',
        tool('ffmpeg', info.ffmpeg, 'Decodes keyframes into covers. Required for indexing.'),
        tool('ffprobe', info.ffprobe, 'Reads duration, resolution and codec.'),
        tool('Pillow', info.pillow, 'Optional: picks the most detailed frame as the cover.'),
        tool('rclone', info.rclone, 'Optional: only for older libraries that read through an rclone remote.')),
      h('div.section-title', 'Players'),
      h('div.card-box.set-card', info.players.length ? info.players.map(p => h('div.set-row', h('div.grow', h('div.set-name', p.name)), p.path ? h('code.mono.set-path', p.path) : h('span.muted', 'Windows default'))) : h('div.set-row.muted', 'No player found. Add one under "players" in config.json.')),
      h('div.section-title', 'About'),
      h('div.card-box.set-card',
        h('div.set-row', h('div.grow', 'medialib'), h('span.mono', info.version)),
        h('div.set-row', h('div.grow', 'Python'), h('span.mono', info.python)),
        h('div.set-row', h('div.grow', 'Settings and index'), h('code.mono.set-path', info.config_dir)),
        h('div.set-row', h('div.grow', 'Listening on'), h('span.mono', `127.0.0.1:${info.port}`)))] : h('div.banner.error', 'Could not read system information.'));
  return {};
}
