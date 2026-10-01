// A small location picker for object stores: connection, bucket and a folder inside it.
import { h, fill } from './dom.js';
import { icon } from './icons.js';
import { get, s3Path } from './api.js';
import { state } from './state.js';

/** locationPicker({ conn, bucket, prefix, onChange, lockConnection }) -> { el, value() }.  value() = { conn, bucket, prefix } */
export function locationPicker({ conn, bucket = '', prefix = '', onChange, lockConnection = false } = {}) {
  const sel = { conn: conn || state.connections[0]?.id || '', bucket, prefix };
  const connSel = h('select', { 'aria-label': 'Connection', disabled: lockConnection }, state.connections.map(c => h('option', { value: c.id }, c.name)));
  const bucketSel = h('select', { 'aria-label': 'Bucket' });
  const crumbs = h('div.pk-crumbs');
  const list = h('div.pk-list', { role: 'listbox', 'aria-label': 'Folders' });
  const note = h('p.hint.pk-note', { hidden: true });
  const el = h('div.picker', h('div.pk-row', connSel, bucketSel), crumbs, list, note);
  connSel.value = sel.conn;
  let gen = 0;  // drop answers of requests that were overtaken

  const changed = () => onChange && onChange({ ...sel });

  async function loadBuckets() {
    const mine = ++gen;
    fill(bucketSel, h('option', { value: '' }, 'Loading…'));
    note.hidden = true;
    try {
      const j = await get(`/api/s3/${encodeURIComponent(sel.conn)}/buckets`);
      if (mine !== gen) return;
      fill(bucketSel, j.buckets.map(b => h('option', { value: b.name }, b.name)));
      if (!j.buckets.some(b => b.name === sel.bucket)) { sel.bucket = j.buckets[0]?.name || ''; sel.prefix = ''; }
      bucketSel.value = sel.bucket;
    } catch (e) {
      if (mine !== gen) return;
      fill(bucketSel, h('option', { value: '' }, 'No buckets'));
      sel.bucket = '';
      note.textContent = e.message;
      note.hidden = false;
    }
    await loadFolders();
  }

  async function loadFolders() {
    const mine = ++gen;
    paintCrumbs();
    if (!sel.bucket) { fill(list, h('div.pk-empty', 'Choose a bucket.')); changed(); return; }
    fill(list, h('div.pk-empty', h('div.spinner')));
    try {
      const j = await get(s3Path(sel.conn, sel.bucket, 'list', { prefix: sel.prefix, delimiter: '/', limit: 1000 }));
      if (mine !== gen) return;
      const rows = j.prefixes.map(p => h('button.pk-item', { type: 'button', onclick: () => go(p) }, icon('folder', 'sm'), h('span', p.slice(sel.prefix.length).replace(/\/$/, ''))));
      fill(list, rows.length ? rows : h('div.pk-empty', 'No subfolders here.'));
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

  connSel.addEventListener('change', () => { sel.conn = connSel.value; sel.bucket = ''; sel.prefix = ''; loadBuckets(); });
  bucketSel.addEventListener('change', () => { sel.bucket = bucketSel.value; sel.prefix = ''; loadFolders(); });
  if (sel.conn) loadBuckets(); else fill(list, h('div.pk-empty', 'Add a connection first.'));
  return { el, value: () => ({ ...sel }), set: v => { Object.assign(sel, v); connSel.value = sel.conn; loadBuckets(); } };
}
