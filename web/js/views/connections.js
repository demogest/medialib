// Connections: the object-storage accounts (RustFS, MinIO, AWS S3, R2, B2, ...) the library and storage browser use.
import { h, fill } from '../lib/dom.js';
import { icon } from '../lib/icons.js';
import { get, post, put, del } from '../lib/api.js';
import { navigate } from '../lib/router.js';
import { state, loadConnections, loadLibraries, on } from '../lib/state.js';
import { confirmDialog, modal, showMenu, toast, toastError } from '../lib/ui.js';
import { t } from '../lib/i18n.js';

let providers = null;

export async function mount(root) {
  providers ||= (await get('/api/providers')).providers;
  const list = h('div.conn-list');
  const found = h('div');
  const page = h('div.page', h('div.page-inner',
    h('div.page-head',
      h('div', h('h1', t('connections.title')), h('p', t('connections.subtitle'))),
      h('div.actions', h('button.btn.primary', { type: 'button', onclick: () => editConnection() }, icon('plus', 'sm'), t('connections.addConnection')))),
    list, found));
  root.append(page);

  const results = new Map();  // connection id -> latest test result
  const render = () => {
    if (!state.connections.length) {
      fill(list, h('div.card-box.empty-state', icon('plug'), h('h3', t('connections.emptyTitle')),
        h('p', t('connections.emptyText')),
        h('button.btn.primary', { type: 'button', onclick: () => editConnection() }, icon('plus', 'sm'), t('connections.addConnection'))));
    } else {
      fill(list, state.connections.map(c => card(c, results.get(c.id))));
    }
  };

  function card(c, result) {
    const p = providers.find(x => x.id === c.provider);
    const libs = state.libs.filter(l => l.connection === c.id);
    const status = !result ? h('span.status-dot.busy', { title: t('connections.checking') })
      : h('span.status-dot', { class: result.ok ? 'ok' : 'bad', title: result.ok ? t('connections.connected') : result.message });
    return h('article.conn', { dataset: { id: c.id } },
      h('div.conn-top', status, h('h3', c.name), h('span.tag', p ? p.label : c.provider),
        h('div.conn-actions',
          h('button.btn.small', { type: 'button', onclick: () => navigate('storage', c.id) }, icon('storage', 'sm'), t('connections.browse')),
          h('button.btn.small', { type: 'button', onclick: () => check(c) }, icon('refresh', 'sm'), t('connections.test')),
          h('button.icon-btn.small', { type: 'button', 'aria-label': t('common.more'), onclick: e => showMenu({ anchor: e.currentTarget, align: 'right', items: [
            { label: t('common.edit'), icon: 'edit', onClick: () => editConnection(c) },
            { sep: true },
            { label: t('common.remove'), icon: 'trash', danger: true, onClick: () => remove(c, libs) },
          ] }) }, icon('more')))),
      h('dl.conn-meta',
        h('div', h('dt', t('connections.endpoint')), h('dd.mono', c.endpoint)),
        h('div', h('dt', t('connections.accessKey')), h('dd.mono', mask(c.access_key))),
        h('div', h('dt', t('connections.region')), h('dd', c.region || '–')),
        h('div', h('dt', t('connections.addressing')), h('dd', c.addressing === 'virtual' ? t('connections.virtualHosted') : c.addressing === 'auto' ? t('connections.automatic') : t('connections.pathStyle'))),
        h('div', h('dt', t('connections.secret')), h('dd', c.secret_source === 'env' ? t('connections.secretFromEnv', { name: c.secret_key_env }) : c.has_secret ? t('connections.secretStored') : h('span.tag.danger', t('connections.secretMissing'))))),
      h('div.conn-foot',
        result ? (result.ok ? h('span.ok-text', icon('check', 'sm'), result.buckets ? t('connections.connectedBuckets', { count: result.buckets.length, ms: result.latency_ms }) : t('connections.connectedMs', { ms: result.latency_ms }))
          : h('span.bad-text', icon('alert', 'sm'), result.message)) : h('span.muted', t('connections.checking')),
        libs.length ? h('span.muted', t('connections.usedBy', { count: libs.length, names: libs.map(l => l.name).join(', ') })) : null));
  }

  async function check(c) {
    results.delete(c.id);
    render();
    try { results.set(c.id, await post(`/api/connections/${encodeURIComponent(c.id)}/test`)); } catch (e) { results.set(c.id, { ok: false, message: e.message }); }
    render();
  }

  async function remove(c, libs) {
    if (libs.length) return toast(t('connections.removeLibrariesFirst', { name: c.name, libraries: libs.map(l => l.name).join(', ') }), { kind: 'error' });
    if (!await confirmDialog({ title: t('connections.removeTitle'), message: t('connections.removeMessage', { name: c.name }), detail: t('connections.removeDetail'), confirm: t('common.remove'), danger: true })) return;
    try { await del(`/api/connections/${encodeURIComponent(c.id)}`); await loadConnections(); render(); } catch (e) { toastError(t('connections.couldNotRemove'), e); }
  }

  async function renderFound() {
    let sources = [];
    try { sources = (await get('/api/connections/importable')).sources; } catch { /* optional */ }
    const have = new Set(state.connections.map(c => c.endpoint + '|' + c.access_key));
    sources = sources.filter(s => !have.has(s.endpoint + '|' + s.access_key));
    if (!sources.length) return fill(found);
    fill(found, h('div.section-title', t('connections.foundTitle')),
      h('div.card-box.found', sources.map(s => h('div.found-row',
        icon('key'), h('div.grow', h('div.found-name', s.label), h('div.muted.mono', s.endpoint || 'Amazon S3')),
        h('button.btn.small', { type: 'button', onclick: async () => {
          try { const c = await post('/api/connections/import', { source: s.source }); await loadConnections(); render(); renderFound(); check(c); toast(t('connections.added', { name: c.name }), { kind: 'ok' }); } catch (e) { toastError(t('connections.couldNotImport'), e); }
        } }, icon('plus', 'sm'), t('common.add'))))),
      h('p.muted.small-print', t('connections.foundNote')));
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
  const name = input('name', { value: existing?.name || '', placeholder: t('connections.namePlaceholder') });
  const endpoint = input('endpoint', { value: existing?.endpoint || '', placeholder: 'https://s3.example.com', class: 'mono' });
  const region = input('region', { value: existing?.region || '' });
  const access = input('access_key', { value: existing?.access_key || '', class: 'mono' });
  const secret = input('secret_key', { type: 'password', class: 'mono', placeholder: existing?.has_secret ? t('connections.unchanged') : '', autocomplete: 'new-password' });
  const token = input('session_token', { type: 'password', class: 'mono', placeholder: existing?.has_token ? t('connections.unchanged') : t('connections.tokenPlaceholder') });
  const secretEnv = input('secret_key_env', { value: existing?.secret_key_env || '', class: 'mono', placeholder: t('connections.secretEnvPlaceholder') });
  const bucket = input('default_bucket', { value: existing?.default_bucket || '', placeholder: t('connections.bucketPlaceholder') });
  const addressing = h('select', { name: 'addressing' }, h('option', { value: 'path' }, t('connections.pathStyleOption')), h('option', { value: 'virtual' }, t('connections.virtualHostedOption')), h('option', { value: 'auto' }, t('connections.automatic')));
  addressing.value = existing?.addressing || 'path';
  const verify = h('input', { type: 'checkbox', checked: existing ? existing.verify_tls !== false : true });
  const result = h('div');
  const error = h('p.form-error', { hidden: true });
  const reveal = h('button.icon-btn', { type: 'button', 'aria-label': t('connections.showSecret'), onclick: () => { const on = secret.type === 'password'; secret.type = on ? 'text' : 'password'; reveal.replaceChildren(icon(on ? 'eye-off' : 'eye')); } }, icon('eye'));

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
    endpoint.placeholder = p.endpoint || t('connections.endpointPlaceholder');
    regionHint.textContent = id === 'r2' || id === 'gcs' ? t('connections.regionHintAuto') : id === 'aws' ? t('connections.regionHintAws') : t('connections.regionHintDefault');
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
    h('div.field', h('div.label', t('connections.kind')), chips),
    h('div.field', h('label', t('connections.name')), name),
    h('div.field', h('label', t('connections.endpointUrl')), endpoint, h('p.hint', t('connections.endpointHint'))),
    h('div.field-row', h('div.field', h('label', t('connections.accessKey')), access), h('div.field', h('label', t('connections.secretKey')), h('div.input-wrap', secret, reveal))),
    h('div.field', h('label', t('connections.region')), region, regionHint),
    h('details.advanced', h('summary', t('connections.advanced')),
      h('div.field', h('label', t('connections.addressing')), addressing, h('p.hint', t('connections.addressingHint'))),
      h('div.field', h('label', t('connections.secretEnv')), secretEnv, h('p.hint', t('connections.secretEnvHint'))),
      h('div.field', h('label', t('connections.sessionToken')), token),
      h('div.field', h('label', t('connections.defaultBucket')), bucket, h('p.hint', t('connections.defaultBucketHint'))),
      h('label.switch', verify, h('span', h('span.t', t('connections.verifyTls')), h('span.d', t('connections.verifyTlsHint'))))),
    result);

  const m = modal({
    title: existing ? t('connections.editTitle', { name: existing.name }) : t('connections.addTitle'), size: 'wide', body,
    actions: [
      { label: t('connections.testConnection'), left: true, keepOpen: true, onClick: async () => {
        showError('');
        fill(result, h('div.test-line', h('div.spinner'), t('connections.connecting')));
        try {
          const r = await post('/api/connections/test', draft());
          fill(result, r.ok ? h('p.form-ok', r.buckets ? (r.buckets.length ? t('connections.testBuckets', { ms: r.latency_ms, count: r.buckets.length, names: r.buckets.slice(0, 6).join(', ') + (r.buckets.length > 6 ? ', …' : '') }) : t('connections.testNoBuckets', { ms: r.latency_ms })) : t('connections.testConnected', { ms: r.latency_ms }))
            : h('p.form-error', r.message + (r.hint ? ' ' + r.hint : '')));
        } catch (e) { fill(result); showError(e.message); }
        return false;
      } },
      { label: t('common.cancel'), value: undefined },
      { label: existing ? t('common.save') : t('connections.addConnection'), primary: true, keepOpen: true, onClick: async api => {
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
