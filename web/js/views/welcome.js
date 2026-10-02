// First run: nothing is set up yet. Three clear ways in, instead of an empty library.
import { h } from '../lib/dom.js';
import { icon, logo } from '../lib/icons.js';
import { get, post } from '../lib/api.js';
import { navigate } from '../lib/router.js';
import { loadConnections, state } from '../lib/state.js';
import { toast, toastError } from '../lib/ui.js';
import { editConnection } from './connections.js';
import { openLibraryAction } from '../shell/sidebar.js';

export async function mount(root) {
  let found = [];
  try { found = (await get('/api/connections/importable')).sources || []; } catch { /* optional */ }

  const card = (ic, title, text, action, cta, primary) =>
    h('div.way', h('span.way-icon', icon(ic, 'lg')), h('h3', title), h('p', text),
      h('button.btn', { class: primary ? 'primary' : '', type: 'button', onclick: action }, cta));

  const importAll = async () => {
    try {
      for (const s of found) await post('/api/connections/import', { source: s.source });
      await loadConnections();
      toast(`Imported ${found.length} connection${found.length > 1 ? 's' : ''}`, { kind: 'ok' });
      navigate('storage');
    } catch (e) { toastError('Could not import', e); }
  };

  root.append(h('div.page', h('div.welcome',
    h('div.welcome-mark', logo()),
    h('h1', 'Welcome to Media Library'),
    h('p.lede', 'Browse your videos as a wall of real keyframes, and manage S3-compatible storage, all in one place. Pick a starting point; you can add the rest later.'),
    h('div.ways',
      card('folder', 'Add a folder', 'A folder on a disk or a NAS share. Covers are generated once and kept.',
        () => openLibraryAction('add'), 'Choose a folder…', true),
      card('cloud', 'Connect object storage', 'RustFS, MinIO, Amazon S3, Cloudflare R2, Backblaze B2 and anything S3-compatible.',
        async () => { const c = await editConnection(); if (c) navigate('storage', c.id); }, 'Add a connection…'),
      found.length ? card('key', `Use what is on this computer`, `Found ${found.length} set of credentials (${found.map(s => s.name || s.source).slice(0, 3).join(', ')}).`, importAll, 'Import') : null),
    h('p.welcome-tip', h('kbd', 'Ctrl K'), ' opens search and commands anywhere.'))));
  return { destroy() { root.replaceChildren(); } };
}
