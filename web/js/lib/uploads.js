// Upload queue: files go to medialib (which streams them into the bucket), three at a time, with a floating progress panel
// that stays while you move between views.
import { h, fill, raf } from './dom.js';
import { icon } from './icons.js';
import { s3Path, upload } from './api.js';
import { bytes, leaf } from './fmt.js';
import { emit } from './state.js';

const MAX_PARALLEL = 3;
let seq = 0;
const items = [];
let panel = null, collapsed = false;

/** Queue files. entries: [{ file, key, overwrite }]. */
export function enqueue(conn, bucket, entries) {
  for (const e of entries) {
    items.push({ id: ++seq, conn, bucket, key: e.key, file: e.file, size: e.file.size, loaded: 0, state: 'queued', error: '', overwrite: !!e.overwrite, ctrl: null });
  }
  collapsed = false;
  pump();
  paint();
}

const pending = () => items.filter(i => i.state === 'queued' || i.state === 'uploading');

function pump() {
  while (items.filter(i => i.state === 'uploading').length < MAX_PARALLEL) {
    const next = items.find(i => i.state === 'queued');
    if (!next) break;
    start(next);
  }
}

async function start(it) {
  it.state = 'uploading';
  it.ctrl = new AbortController();
  try {
    await upload(s3Path(it.conn, it.bucket, 'object', { key: it.key, overwrite: it.overwrite ? '1' : '0' }), it.file, {
      signal: it.ctrl.signal, onProgress: (loaded) => { it.loaded = loaded; paint(); },
    });
    it.state = 'done';
    it.loaded = it.size;
    emit('uploaded', { conn: it.conn, bucket: it.bucket, key: it.key });
  } catch (e) {
    if (e.name === 'AbortError') it.state = 'canceled';
    else if (/already exists/.test(e.message)) { it.state = 'skipped'; it.error = 'Already exists'; }
    else { it.state = 'error'; it.error = e.message; }
  }
  it.ctrl = null;
  pump();
  paint();
}

function cancel(it) {
  if (it.state === 'queued') it.state = 'canceled';
  else if (it.ctrl) it.ctrl.abort();
  paint();
}
function retry(it) { it.state = 'queued'; it.loaded = 0; it.error = ''; pump(); paint(); }
function clearFinished() {
  for (let i = items.length - 1; i >= 0; i--) if (!['queued', 'uploading'].includes(items[i].state)) items.splice(i, 1);
  paint();
}

let hideTimer = null;
const paint = raf(() => {
  clearTimeout(hideTimer);
  if (!items.length) { panel?.remove(); panel = null; return; }
  if (!panel) panel = document.body.appendChild(h('aside.uploads', { 'aria-label': 'Uploads' }));
  const total = items.reduce((a, i) => a + i.size, 0), done = items.reduce((a, i) => a + i.loaded, 0);
  const live = pending().length, failed = items.filter(i => i.state === 'error').length;
  const pct = total ? Math.floor(done / total * 100) : 100;
  const title = live ? `Uploading ${live} file${live === 1 ? '' : 's'} · ${pct}%` : failed ? `${failed} upload${failed === 1 ? '' : 's'} failed` : 'Uploads finished';
  const head = h('div.up-head', h('div.up-title', live ? h('div.spinner') : icon(failed ? 'alert' : 'check2', 'sm'), title),
    h('button.icon-btn.small', { type: 'button', 'aria-label': collapsed ? 'Expand' : 'Collapse', onclick: () => { collapsed = !collapsed; paint(); } }, icon(collapsed ? 'chevron-up' : 'chevron-down')),
    h('button.icon-btn.small', { type: 'button', 'aria-label': live ? 'Cancel all and close' : 'Close', onclick: () => { for (const i of pending()) cancel(i); clearFinished(); } }, icon('x')));
  const overall = live ? h('div.meter', h('i', { style: { width: pct + '%' } })) : null;
  const list = h('ul.up-list', { hidden: collapsed }, items.slice(-60).reverse().map(row));
  fill(panel, head, overall, list);
  // A finished batch without problems tidies itself away.
  if (!live && !items.some(i => i.state === 'error')) hideTimer = setTimeout(() => { if (!pending().length) { clearFinished(); } }, 8000);
});

function row(it) {
  const p = it.size ? Math.floor(it.loaded / it.size * 100) : 100;
  const status = { queued: 'Waiting', uploading: `${p}%`, done: 'Done', error: it.error, skipped: 'Already exists, skipped', canceled: 'Cancelled' }[it.state];
  return h('li.up-row', { class: it.state },
    h('div.up-main', h('div.up-name', { title: it.key }, leaf(it.key)), h('div.up-sub', `${bytes(it.size)} · ${status}`),
      it.state === 'uploading' ? h('div.meter', h('i', { style: { width: p + '%' } })) : null),
    it.state === 'queued' || it.state === 'uploading' ? h('button.icon-btn.small', { type: 'button', 'aria-label': 'Cancel', onclick: () => cancel(it) }, icon('x'))
      : it.state === 'error' || it.state === 'canceled' ? h('button.icon-btn.small', { type: 'button', 'aria-label': 'Retry', onclick: () => retry(it) }, icon('refresh')) : null);
}

// ---------------------------------------------------------------- reading dropped files and folders
/** Files in a drop, with folders walked recursively. Returns [{ file, path }] where path is relative to the drop ("dir/sub/a.mp4"). */
export async function filesFromDrop(dataTransfer) {
  const out = [];
  const entries = [...dataTransfer.items].map(i => (i.kind === 'file' && i.webkitGetAsEntry ? i.webkitGetAsEntry() : null));
  if (entries.some(Boolean)) {
    const walk = async (entry, base) => {
      if (entry.isFile) {
        const file = await new Promise((res, rej) => entry.file(res, rej));
        out.push({ file, path: base + entry.name });
      } else if (entry.isDirectory) {
        const reader = entry.createReader();
        for (;;) {  // readEntries returns batches until it returns an empty one
          const batch = await new Promise((res, rej) => reader.readEntries(res, rej));
          if (!batch.length) break;
          for (const child of batch) await walk(child, base + entry.name + '/');
        }
      }
    };
    for (const e of entries) if (e) await walk(e, '');
    return out;
  }
  return [...dataTransfer.files].map(file => ({ file, path: file.name }));
}
