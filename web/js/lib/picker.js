// A small location picker for object stores: connection, bucket and a folder inside it.
import { h, fill } from './dom.js';
import { icon } from './icons.js';
import { get, s3Path } from './api.js';
import { state } from './state.js';
import { t } from './i18n.js';

/** locationPicker({ conn, bucket, prefix, onChange, lockConnection }) -> { el, value() }.  value() = { conn, bucket, prefix } */
export function locationPicker({ conn, bucket = '', prefix = '', onChange, lockConnection = false } = {}) {
  const sel = { conn: conn || state.connections[0]?.id || '', bucket, prefix };
  const connSel = h('select', { 'aria-label': t('picker.connection'), disabled: lockConnection }, state.connections.map(c => h('option', { value: c.id }, c.name)));
  const bucketSel = h('select', { 'aria-label': t('picker.bucket') });
  const crumbs = h('div.pk-crumbs');
  const list = h('div.pk-list', { role: 'listbox', 'aria-label': t('picker.folders') });
  const note = h('p.hint.pk-note', { hidden: true });
  const el = h('div.picker', h('div.pk-row', connSel, bucketSel), crumbs, list, note);
  connSel.value = sel.conn;
  let gen = 0, initial = !!bucket;  // drop answers of requests that were overtaken

  const changed = () => onChange && onChange({ ...sel });

  async function loadBuckets() {
    const mine = ++gen;
    fill(bucketSel, h('option', { value: '' }, t('common.loading')));
    note.hidden = true;
    try {
      const j = await get(`/api/s3/${encodeURIComponent(sel.conn)}/buckets`);
      if (mine !== gen) return;
      const names = j.buckets.map(b => b.name);
      if (sel.bucket && !names.includes(sel.bucket) && initial) names.unshift(sel.bucket);  // a key that cannot list every bucket may still use this one
      fill(bucketSel, names.map(n => h('option', { value: n }, n)));
      if (!names.includes(sel.bucket)) { sel.bucket = names[0] || ''; sel.prefix = ''; }
      bucketSel.value = sel.bucket;
    } catch (e) {
      if (mine !== gen) return;
      if (initial && sel.bucket) fill(bucketSel, h('option', { value: sel.bucket }, sel.bucket));  // keep the library's own bucket
      else { fill(bucketSel, h('option', { value: '' }, t('picker.noBuckets'))); sel.bucket = ''; }
      note.textContent = e.message;
      note.hidden = false;
    }
    await loadFolders();
  }

  async function loadFolders() {
    const mine = ++gen;
    paintCrumbs();
    if (!sel.bucket) { fill(list, h('div.pk-empty', t('picker.chooseBucket'))); changed(); return; }
    fill(list, h('div.pk-empty', h('div.spinner')));
    try {
      const j = await get(s3Path(sel.conn, sel.bucket, 'list', { prefix: sel.prefix, delimiter: '/', limit: 1000 }));
      if (mine !== gen) return;
      const rows = j.prefixes.map(p => h('button.pk-item', { type: 'button', onclick: () => go(p) }, icon('folder', 'sm'), h('span', p.slice(sel.prefix.length).replace(/\/$/, ''))));
      fill(list, rows.length ? rows : h('div.pk-empty', t('picker.noSubfolders')));
    } catch (e) {
      if (mine !== gen) return;
      fill(list, h('div.pk-empty', e.message));
    }
    changed();
  }

  function paintCrumbs() {
    const parts = sel.prefix.split('/').filter(Boolean);
    const piece = (label, p) => h('button.pk-crumb', { type: 'button', onclick: () => go(p) }, label);
    fill(crumbs, sel.bucket ? [piece(sel.bucket, ''), parts.map((x, i) => [h('span.muted', '/'), piece(x, parts.slice(0, i + 1).join('/') + '/')])] : null);
  }
  function go(p) { sel.prefix = p; loadFolders(); }

  connSel.addEventListener('change', () => { initial = false; sel.conn = connSel.value; sel.bucket = ''; sel.prefix = ''; loadBuckets(); });
  bucketSel.addEventListener('change', () => { sel.bucket = bucketSel.value; sel.prefix = ''; loadFolders(); });
  if (sel.conn) loadBuckets(); else fill(list, h('div.pk-empty', t('picker.addConnectionFirst')));
  return { el, value: () => ({ ...sel }), set: v => { Object.assign(sel, v); connSel.value = sel.conn; loadBuckets(); } };
}
