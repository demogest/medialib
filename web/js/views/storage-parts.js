// Pieces of the Storage view: previews, link menu, properties / move / library / cleanup dialogs.
import { h, fill } from '../lib/dom.js';
import { icon, kindIcon } from '../lib/icons.js';
import { get, objectUrl, post, s3Path } from '../lib/api.js';
import { bytes, kindOf, leaf, num, when } from '../lib/fmt.js';
import { locationPicker } from '../lib/picker.js';
import { modal, toast, toastError } from '../lib/ui.js';
import { pokeWatcher, state } from '../lib/state.js';

export async function copyText(text, what = 'Copied') {
  try { await navigator.clipboard.writeText(text); toast(`${what} copied`, { kind: 'ok' }); } catch { toast('Copy failed: ' + text, { kind: 'error' }); }
}

// ---------------------------------------------------------------- preview
const TEXT_LIMIT = 64 * 1024;
const NATIVE_VIDEO = new Set(['mp4', 'm4v', 'webm', 'mov', 'ogv']);

/** A preview element for an object, by kind. size is bytes. */
export function preview(conn, bucket, key, size) {
  const kind = kindOf(key), ext = key.split('.').pop().toLowerCase(), url = objectUrl(conn, bucket, key);
  if (kind === 'image' && size <= 25 * 1024 * 1024) return h('img.pv-img', { src: url, alt: '', loading: 'lazy' });
  if (kind === 'video' && NATIVE_VIDEO.has(ext)) return h('video.pv-video', { src: url, controls: true, preload: 'metadata', playsinline: true });
  if (kind === 'audio') return h('audio.pv-audio', { src: url, controls: true, preload: 'metadata' });
  if (kind === 'text' && size <= 50 * 1024 * 1024) {
    const pre = h('pre.pv-text', 'Loading…');
    fetch(url, { headers: { Range: `bytes=0-${TEXT_LIMIT - 1}` } }).then(r => r.ok ? r.text() : Promise.reject(new Error(r.statusText)))
      .then(t => { pre.textContent = t + (size > TEXT_LIMIT ? '\n…' : ''); }).catch(e => { pre.textContent = 'Could not load a preview: ' + e.message; });
    return pre;
  }
  return h('div.pv-none', icon(kindIcon(kind), 'lg'), h('span', kind === 'video' ? 'This format can’t play in the browser. Use “Play in player”.' : 'No preview for this type.'));
}

// ---------------------------------------------------------------- links
/** Menu items that copy a link or a name for one object. */
export function linkItems(conn, bucket, key) {
  const presigned = (label, seconds) => ({
    label, icon: 'link', onClick: async () => {
      try { copyText((await get(s3Path(conn, bucket, 'presign', { key, expires: seconds }))).url, 'Link'); } catch (e) { toastError('Could not create the link', e); }
    },
  });
  return [
    { head: 'Presigned link (works anywhere)' },
    presigned('Valid for 1 hour', 3600), presigned('Valid for 24 hours', 86400), presigned('Valid for 7 days', 7 * 86400),
    { sep: true },
    { label: 'Through medialib (this computer only)', icon: 'server', onClick: () => copyText(location.origin + objectUrl(conn, bucket, key), 'Link') },
    { label: 's3:// address', icon: 'cloud', onClick: () => copyText(`s3://${bucket}/${key}`, 'Address') },
    { label: 'Object key', icon: 'copy', onClick: () => copyText(key, 'Key') },
  ];
}

// ---------------------------------------------------------------- properties
const COMMON_TYPES = ['video/mp4', 'video/x-matroska', 'video/webm', 'audio/mpeg', 'image/jpeg', 'image/png', 'text/plain', 'application/json', 'application/octet-stream'];

export async function propertiesDialog(conn, bucket, key, head) {
  const type = h('input.input.mono', { value: head.content_type || '', list: 'ct-list', autocomplete: 'off' });
  const cache = h('input.input.mono', { value: head.cache_control || '', placeholder: 'e.g. public, max-age=31536000' });
  const rows = h('div.kv-rows');
  const addRow = (k = '', v = '') => {
    const row = h('div.kv-row', h('input.input.mono', { value: k, placeholder: 'name' }), h('input.input', { value: v, placeholder: 'value' }),
      h('button.icon-btn.small', { type: 'button', 'aria-label': 'Remove', onclick: () => row.remove() }, icon('x')));
    rows.append(row);
  };
  Object.entries(head.metadata || {}).forEach(([k, v]) => addRow(k, v));
  const error = h('p.form-error', { hidden: true });
  const m = modal({
    title: 'Properties', size: 'wide',
    body: h('div', error, h('p.muted.mono', { style: { overflowWrap: 'anywhere' } }, `${bucket}/${key}`),
      h('datalist#ct-list', COMMON_TYPES.map(t => h('option', { value: t }))),
      h('div.field', h('label', 'Content type'), type, h('p.hint', 'Decides how browsers and players treat the file.')),
      h('div.field', h('label', 'Cache-Control'), cache),
      h('div.field', h('div.label', 'Custom metadata'), rows, h('div', h('button.btn.small', { type: 'button', onclick: () => addRow() }, icon('plus', 'sm'), 'Add field')))),
    actions: [{ label: 'Cancel', value: null }, { label: 'Save', primary: true, keepOpen: true, onClick: async api => {
      const metadata = {};
      for (const r of rows.children) { const [k, v] = r.querySelectorAll('input'); if (k.value.trim()) metadata[k.value.trim()] = v.value; }
      try { api.close(await post(s3Path(conn, bucket, 'properties'), { key, content_type: type.value.trim(), cache_control: cache.value.trim(), metadata })); } catch (e) { error.textContent = e.message; error.hidden = false; }
      return false;
    } }],
  });
  return m.closed;
}

// ---------------------------------------------------------------- move / copy
/** Pick a destination and run a transfer. rows: [{ name, key, type }] (folders have a trailing "/" key). Resolves to the task, or null. */
export function transferDialog({ conn, bucket, prefix, rows, move }) {
  let dest = { conn, bucket, prefix };
  let mode = move ? 'move' : 'copy';
  const picker = locationPicker({ conn, bucket, prefix, onChange: v => { dest = v; label(); } });
  const where = h('p.muted.mono', { style: { overflowWrap: 'anywhere' } });
  const label = () => { where.textContent = dest.bucket ? `→ ${state.connections.find(c => c.id === dest.conn)?.name || dest.conn} / ${dest.bucket} / ${dest.prefix}` : ''; };
  const seg = h('div.seg', [['copy', 'Copy'], ['move', 'Move']].map(([id, t]) => h('button', { type: 'button', 'aria-pressed': String(id === mode), dataset: { id }, onclick: () => { mode = id; for (const b of seg.children) b.setAttribute('aria-pressed', String(b.dataset.id === id)); go.textContent = id === 'move' ? 'Move here' : 'Copy here'; } }, t)));
  const skip = h('input', { type: 'checkbox', checked: true });
  const error = h('p.form-error', { hidden: true });
  const go = h('span', mode === 'move' ? 'Move here' : 'Copy here');
  label();
  const names = rows.slice(0, 3).map(r => r.name).join(', ') + (rows.length > 3 ? ` and ${rows.length - 3} more` : '');
  const m = modal({
    title: `${rows.length === 1 ? rows[0].name : rows.length + ' items'}`, size: 'wide',
    body: h('div', error, h('div.field', h('div.label', 'Action'), seg), h('p.hint', names), picker.el, where,
      h('label.switch', { style: { marginTop: '12px' } }, skip, h('span', h('span.t', 'Skip files that already exist'), h('span.d', 'Turn off to overwrite files that are already there.')))),
    actions: [{ label: 'Cancel', value: null }, { label: '', primary: true, keepOpen: true, onClick: async api => {
      if (!dest.bucket) { error.textContent = 'Choose a destination bucket.'; error.hidden = false; return false; }
      const same = dest.conn === conn && dest.bucket === bucket;
      const items = rows.map(r => ({ from: r.key, to: dest.prefix + r.name + (r.type === 'folder' ? '/' : '') }));
      if (same && items.some(i => i.from === i.to)) { error.textContent = 'That is where they already are.'; error.hidden = false; return false; }
      try {
        const j = await post(s3Path(conn, bucket, 'transfer'), { items, move: mode === 'move', to_conn: dest.conn === conn ? undefined : dest.conn, to_bucket: same ? undefined : dest.bucket, skip_existing: skip.checked });
        pokeWatcher();
        api.close(j.task);
      } catch (e) { error.textContent = e.message; error.hidden = false; }
      return false;
    } }],
  });
  const primary = m.foot.querySelector('.primary');
  primary.replaceChildren(go);
  return m.closed.then(v => v || null);
}

// ---------------------------------------------------------------- use a folder as a library
export function libraryDialog({ conn, bucket, prefix }) {
  const name = h('input.input', { value: `${bucket}/${prefix.replace(/\/$/, '')}`.replace(/\/$/, ''), autocomplete: 'off' });
  const error = h('p.form-error', { hidden: true });
  const m = modal({
    title: 'Use as a library', size: 'narrow',
    body: h('div', error, h('p', 'medialib will index the videos in this folder and its subfolders and show them with keyframe covers.'),
      h('p.muted.mono', { style: { overflowWrap: 'anywhere' } }, `s3://${bucket}/${prefix}`),
      h('div.field', h('label', 'Name'), name)),
    actions: [{ label: 'Cancel', value: null }, { label: 'Add and index', primary: true, keepOpen: true, onClick: async api => {
      try { api.close(await post(s3Path(conn, bucket, 'library'), { prefix, name: name.value })); } catch (e) { error.textContent = e.message; error.hidden = false; }
      return false;
    } }],
  });
  return m.closed;
}

// ---------------------------------------------------------------- unfinished multipart uploads
export async function incompleteUploadsDialog(conn, bucket) {
  const body = h('div', h('div.test-line', h('div.spinner'), 'Looking…'));
  const m = modal({ title: 'Unfinished uploads', size: 'wide', body, actions: [{ label: 'Close', primary: true, value: true }] });
  let uploads;
  try { uploads = (await get(s3Path(conn, bucket, 'uploads'))).uploads; } catch (e) { fill(body, h('p.form-error', e.message)); return; }
  if (!uploads.length) return fill(body, h('div.empty-state', icon('check2'), h('h3', 'Nothing left over'), h('p', 'Every multipart upload in this bucket was completed or cancelled.')));
  const paint = list => fill(body, h('p', 'Large uploads that were interrupted leave parts behind. They are invisible in listings but still use space.'),
    h('table.table', h('thead', h('tr', h('th', 'Object'), h('th', 'Started'))),
      h('tbody', list.map(u => h('tr', h('td', { style: { whiteSpace: 'normal', overflowWrap: 'anywhere' } }, u.key), h('td', when(u.started)))))),
    h('div', { style: { marginTop: '14px' } }, h('button.btn.danger', { type: 'button', onclick: async () => {
      try { await post(s3Path(conn, bucket, 'uploads/abort'), { items: list }); toast(`Discarded ${num(list.length)} unfinished upload${list.length === 1 ? '' : 's'}`, { kind: 'ok' }); paint([]); fill(body, h('div.empty-state', icon('check2'), h('h3', 'All cleaned up'))); } catch (e) { toastError('Could not discard', e); }
    } }, icon('trash', 'sm'), `Discard all ${num(list.length)}`)));
  paint(uploads);
}

export { bytes, leaf };
