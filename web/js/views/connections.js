// Connections: the object-storage accounts (RustFS, MinIO, AWS S3, R2, B2, ...) the library and storage browser use.
import { h, fill } from '../lib/dom.js';
import { icon } from '../lib/icons.js';
import { get, post, put, del } from '../lib/api.js';
import { navigate } from '../lib/router.js';
import { state, loadConnections, loadLibraries, on } from '../lib/state.js';
import { confirmDialog, modal, showMenu, toast, toastError } from '../lib/ui.js';

let providers = null;

export async function mount(root) {
  providers ||= (await get('/api/providers')).providers;
  const list = h('div.conn-list');
  const found = h('div');
  const page = h('div.page', h('div.page-inner',
    h('div.page-head',
      h('div', h('h1', 'Connections'), h('p', 'Object-storage accounts. Libraries and the storage browser talk to them directly over the S3 API: RustFS, MinIO, Amazon S3, Cloudflare R2, Backblaze B2 and anything else that speaks S3.')),
      h('div.actions', h('button.btn.primary', { type: 'button', onclick: () => editConnection() }, icon('plus', 'sm'), 'Add connection'))),
    list, found));
  root.append(page);

  const results = new Map();  // connection id -> latest test result
  const render = () => {
    if (!state.connections.length) {
      fill(list, h('div.card-box.empty-state', icon('plug'), h('h3', 'No connections yet'),
        h('p', 'Add the endpoint and keys of your object store to browse it, upload to it, and build libraries from its buckets.'),
        h('button.btn.primary', { type: 'button', onclick: () => editConnection() }, icon('plus', 'sm'), 'Add connection')));
    } else {
      fill(list, state.connections.map(c => card(c, results.get(c.id))));
    }
  };

  function card(c, result) {
    const p = providers.find(x => x.id === c.provider);
    const libs = state.libs.filter(l => l.connection === c.id);
    const status = !result ? h('span.status-dot.busy', { title: 'Checking…' })
      : h('span.status-dot', { class: result.ok ? 'ok' : 'bad', title: result.ok ? 'Connected' : result.message });
    return h('article.conn', { dataset: { id: c.id } },
      h('div.conn-top', status, h('h3', c.name), h('span.tag', p ? p.label : c.provider),
        h('div.conn-actions',
          h('button.btn.small', { type: 'button', onclick: () => navigate('storage', c.id) }, icon('storage', 'sm'), 'Browse'),
          h('button.btn.small', { type: 'button', onclick: () => check(c) }, icon('refresh', 'sm'), 'Test'),
          h('button.icon-btn.small', { type: 'button', 'aria-label': 'More', onclick: e => showMenu({ anchor: e.currentTarget, align: 'right', items: [
            { label: 'Edit', icon: 'edit', onClick: () => editConnection(c) },
            { sep: true },
            { label: 'Remove', icon: 'trash', danger: true, onClick: () => remove(c, libs) },
          ] }) }, icon('more')))),
      h('dl.conn-meta',
        h('div', h('dt', 'Endpoint'), h('dd.mono', c.endpoint)),
        h('div', h('dt', 'Access key'), h('dd.mono', mask(c.access_key))),
        h('div', h('dt', 'Region'), h('dd', c.region || '–')),
        h('div', h('dt', 'Addressing'), h('dd', c.addressing === 'virtual' ? 'Virtual-hosted' : c.addressing === 'auto' ? 'Automatic' : 'Path style')),
        h('div', h('dt', 'Secret'), h('dd', c.secret_source === 'env' ? `Environment variable ${c.secret_key_env}` : c.has_secret ? 'Stored in config.json' : h('span.tag.danger', 'Missing')))),
      h('div.conn-foot',
        result ? (result.ok ? h('span.ok-text', icon('check', 'sm'), result.buckets ? `Connected · ${result.buckets.length} bucket${result.buckets.length === 1 ? '' : 's'} · ${result.latency_ms} ms` : `Connected · ${result.latency_ms} ms`)
          : h('span.bad-text', icon('alert', 'sm'), result.message)) : h('span.muted', 'Checking…'),
        libs.length ? h('span.muted', `${libs.length} librar${libs.length === 1 ? 'y' : 'ies'}: ${libs.map(l => l.name).join(', ')}`) : null));
  }

  async function check(c) {
    results.delete(c.id);
    render();
    try { results.set(c.id, await post(`/api/connections/${encodeURIComponent(c.id)}/test`)); } catch (e) { results.set(c.id, { ok: false, message: e.message }); }
    render();
  }

  async function remove(c, libs) {
    if (libs.length) return toast(`Remove the libraries that use “${c.name}” first: ${libs.map(l => l.name).join(', ')}.`, { kind: 'error' });
    if (!await confirmDialog({ title: 'Remove connection?', message: `“${c.name}” will be removed from medialib.`, detail: 'Nothing in the bucket is touched. The stored keys are deleted from config.json.', confirm: 'Remove', danger: true })) return;
    try { await del(`/api/connections/${encodeURIComponent(c.id)}`); await loadConnections(); render(); } catch (e) { toastError('Could not remove', e); }
  }

  async function renderFound() {
    let sources = [];
    try { sources = (await get('/api/connections/importable')).sources; } catch { /* optional */ }
    const have = new Set(state.connections.map(c => c.endpoint + '|' + c.access_key));
    sources = sources.filter(s => !have.has(s.endpoint + '|' + s.access_key));
    if (!sources.length) return fill(found);
    fill(found, h('div.section-title', 'Found on this computer'),
      h('div.card-box.found', sources.map(s => h('div.found-row',
        icon('key'), h('div.grow', h('div.found-name', s.label), h('div.muted.mono', s.endpoint || 'Amazon S3')),
        h('button.btn.small', { type: 'button', onclick: async () => {
          try { const c = await post('/api/connections/import', { source: s.source }); await loadConnections(); render(); renderFound(); check(c); toast(`Added “${c.name}”`, { kind: 'ok' }); } catch (e) { toastError('Could not import', e); }
        } }, icon('plus', 'sm'), 'Add')))),
      h('p.muted.small-print', 'Credentials are read from your rclone configuration, ~/.aws files or AWS_* environment variables, and stay on this computer.'));
  }

  const off = on('connections', () => { render(); });
  render();
  renderFound();
  for (const c of state.connections) check(c);
  return { destroy() { off(); } };
}

const mask = key => (key && key.length > 8 ? key.slice(0, 4) + '••••••' + key.slice(-3) : key || '');

// ---------------------------------------------------------------- add / edit dialog
/** Open the connection form. Resolves to the saved connection, or undefined when cancelled. */
export async function editConnection(existing) {
  providers ||= (await get('/api/providers')).providers;
  const f = { provider: existing?.provider || 'rustfs' };
  const input = (name, props = {}) => h('input.input', { name, autocomplete: 'off', spellcheck: 'false', ...props });
  const name = input('name', { value: existing?.name || '', placeholder: 'My RustFS' });
  const endpoint = input('endpoint', { value: existing?.endpoint || '', placeholder: 'https://s3.example.com', class: 'mono' });
  const region = input('region', { value: existing?.region || '' });
  const access = input('access_key', { value: existing?.access_key || '', class: 'mono' });
  const secret = input('secret_key', { type: 'password', class: 'mono', placeholder: existing?.has_secret ? '•••••••• (unchanged)' : '', autocomplete: 'new-password' });
  const token = input('session_token', { type: 'password', class: 'mono', placeholder: existing?.has_token ? '•••••••• (unchanged)' : 'Only for temporary credentials' });
  const secretEnv = input('secret_key_env', { value: existing?.secret_key_env || '', class: 'mono', placeholder: 'e.g. MY_S3_SECRET' });
  const bucket = input('default_bucket', { value: existing?.default_bucket || '', placeholder: 'Only if the key cannot list buckets' });
  const addressing = h('select', { name: 'addressing' }, h('option', { value: 'path' }, 'Path style: host/bucket/key'), h('option', { value: 'virtual' }, 'Virtual-hosted: bucket.host/key'), h('option', { value: 'auto' }, 'Automatic'));
  addressing.value = existing?.addressing || 'path';
  const verify = h('input', { type: 'checkbox', checked: existing ? existing.verify_tls !== false : true });
  const result = h('div');
  const error = h('p.form-error', { hidden: true });
  const reveal = h('button.icon-btn', { type: 'button', 'aria-label': 'Show secret key', onclick: () => { const on = secret.type === 'password'; secret.type = on ? 'text' : 'password'; reveal.replaceChildren(icon(on ? 'eye-off' : 'eye')); } }, icon('eye'));

  const chips = h('div.provider-grid', providers.map(p => h('button.provider', { type: 'button', dataset: { id: p.id }, 'aria-pressed': String(p.id === f.provider), onclick: () => pick(p.id, true) }, p.label)));
  const regionHint = h('p.hint');
  let applied = '';  // the endpoint text we filled in from a preset, so a later region change may rewrite it
  const fromPreset = p => p.endpoint.replace('{region}', region.value);
  function pick(id, userChoice) {
    f.provider = id;
    const p = providers.find(x => x.id === id);
    for (const b of chips.children) b.setAttribute('aria-pressed', String(b.dataset.id === id));
    if (userChoice || !existing) {
      if (!region.value || providers.some(x => x.region === region.value)) region.value = p.region;
      if (!endpoint.value || endpoint.value === applied) { endpoint.value = applied = fromPreset(p); }
      addressing.value = p.addressing;
    }
    endpoint.placeholder = p.endpoint || 'https://s3.example.com  or  http://192.168.1.10:9000';
    regionHint.textContent = id === 'r2' || id === 'gcs' ? 'Use “auto”.' : id === 'aws' ? 'The bucket’s region, e.g. eu-west-1.' : 'Most self-hosted stores accept the default.';
  }
  region.addEventListener('input', () => {
    const p = providers.find(x => x.id === f.provider);
    if (p.endpoint.includes('{region}') && (!endpoint.value || endpoint.value === applied)) endpoint.value = applied = fromPreset(p);
  });
  pick(f.provider, false);

  const draft = () => ({
    id: existing?.id, name: name.value, provider: f.provider, endpoint: endpoint.value, region: region.value, access_key: access.value,
    secret_key: secret.value, session_token: token.value, secret_key_env: secretEnv.value, addressing: addressing.value, verify_tls: verify.checked,
    default_bucket: bucket.value,
  });
  const showError = msg => { error.textContent = msg; error.hidden = !msg; };

  const body = h('form', { onsubmit: e => e.preventDefault() }, error,
    h('div.field', h('div.label', 'Kind of store'), chips),
    h('div.field', h('label', 'Name'), name),
    h('div.field', h('label', 'Endpoint URL'), endpoint, h('p.hint', 'Where the S3 API lives. For a self-hosted server, its address and port.')),
    h('div.field-row', h('div.field', h('label', 'Access key'), access), h('div.field', h('label', 'Secret key'), h('div.input-wrap', secret, reveal))),
    h('div.field', h('label', 'Region'), region, regionHint),
    h('details.advanced', h('summary', 'Advanced'),
      h('div.field', h('label', 'Addressing'), addressing, h('p.hint', 'Self-hosted servers almost always want path style. AWS, Alibaba and Tencent use virtual-hosted.')),
      h('div.field', h('label', 'Secret key from an environment variable'), secretEnv, h('p.hint', 'Keeps the secret out of config.json. Used when the field above is empty.')),
      h('div.field', h('label', 'Session token'), token),
      h('div.field', h('label', 'Default bucket'), bucket, h('p.hint', 'For a key that is limited to one bucket and so cannot list them all.')),
      h('label.switch', verify, h('span', h('span.t', 'Verify the TLS certificate'), h('span.d', 'Turn off only for a server with a self-signed certificate on a network you trust.')))),
    result);

  const m = modal({
    title: existing ? `Edit ${existing.name}` : 'Add a connection', size: 'wide', body,
    actions: [
      { label: 'Test connection', left: true, keepOpen: true, onClick: async () => {
        showError('');
        fill(result, h('div.test-line', h('div.spinner'), 'Connecting…'));
        try {
          const r = await post('/api/connections/test', draft());
          fill(result, r.ok ? h('p.form-ok', r.buckets ? `Connected in ${r.latency_ms} ms. ${r.buckets.length} bucket${r.buckets.length === 1 ? '' : 's'}${r.buckets.length ? ': ' + r.buckets.slice(0, 6).join(', ') + (r.buckets.length > 6 ? ', …' : '') : ''}.` : `Connected in ${r.latency_ms} ms.`)
            : h('p.form-error', r.message + (r.hint ? ' ' + r.hint : '')));
        } catch (e) { fill(result); showError(e.message); }
        return false;
      } },
      { label: 'Cancel', value: undefined },
      { label: existing ? 'Save' : 'Add connection', primary: true, keepOpen: true, onClick: async api => {
        showError('');
        try {
          const saved = existing ? await put(`/api/connections/${encodeURIComponent(existing.id)}`, draft()) : await post('/api/connections', draft());
          await Promise.all([loadConnections(), loadLibraries()]);
          api.close(saved);
        } catch (e) { showError(e.message); }
        return false;
      } },
    ],
  });
  (existing ? access : name).focus();
  return m.closed;
}
