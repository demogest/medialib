// Activity: copy / move / delete / size tasks and library indexing runs, with live progress.
import { h, fill } from '../lib/dom.js';
import { icon } from '../lib/icons.js';
import { del, post } from '../lib/api.js';
import { ago, bytes, num } from '../lib/fmt.js';
import { navigate } from '../lib/router.js';
import { on, pokeWatcher, running, state } from '../lib/state.js';
import { toastError } from '../lib/ui.js';

const KIND_ICON = { copy: 'copy', move: 'move', delete: 'trash', size: 'layers' };
const STATE_LABEL = { running: 'Running', done: 'Done', error: 'Failed', cancelled: 'Cancelled' };

export function mount(root) {
  const body = h('div');
  const clear = h('button.btn', { type: 'button', onclick: async () => { await del('/api/tasks'); pokeWatcher(); } }, 'Clear finished');
  root.append(h('div.page', h('div.page-inner',
    h('div.page-head', h('div', h('h1', 'Activity'), h('p', 'Background work: copying and moving objects, deleting folders, measuring sizes, and indexing libraries.')),
      h('div.actions', clear)),
    body)));

  const render = () => {
    const tasks = state.tasks;
    const jobs = Object.entries(state.jobs).map(([id, j]) => ({ id, ...j, lib: state.libs.find(l => l.id === id) })).filter(j => j.lib);
    const active = [...tasks.filter(t => t.state === 'running').map(taskCard), ...jobs.filter(j => running(j)).map(jobCard)];
    const recent = [...tasks.filter(t => t.state !== 'running').map(taskCard), ...jobs.filter(j => !running(j)).map(jobCard)];
    clear.disabled = !tasks.some(t => t.state !== 'running');
    fill(body,
      active.length ? [h('div.section-title', 'In progress'), h('div.act-list', active)] : null,
      recent.length ? [h('div.section-title', 'Recent'), h('div.act-list', recent)] : null,
      !active.length && !recent.length ? h('div.card-box.empty-state', icon('activity'), h('h3', 'Nothing running'), h('p', 'Copies, moves, deletes and indexing runs show up here while they work.')) : null);
  };

  const off = on('activity', render);
  render();
  return { destroy: off };
}

function taskCard(t) {
  const pct = t.total ? Math.min(100, Math.round(t.done / t.total * 100)) : 0;
  const live = t.state === 'running';
  const cancel = live ? h('button.btn.small', { type: 'button', onclick: async () => { try { await post(`/api/tasks/${t.id}/cancel`); pokeWatcher(); } catch (e) { toastError('Could not cancel', e); } } }, icon('stop', 'sm'), 'Cancel')
    : h('button.icon-btn.small', { type: 'button', 'aria-label': 'Dismiss', onclick: async () => { await del(`/api/tasks/${t.id}`); pokeWatcher(); } }, icon('x'));
  const detail = t.kind === 'size' && t.result ? `${num(t.result.objects)} objects · ${bytes(t.result.bytes)}`
    : t.total ? `${num(t.done)} of ${num(t.total)}${t.bytes ? ' · ' + bytes(t.bytes) : ''}` : t.line;
  return h('article.act', { class: t.state },
    h('div.act-icon', icon(KIND_ICON[t.kind] || 'activity')),
    h('div.act-main',
      h('div.act-title', t.title),
      live ? h('div.meter', { class: t.total ? '' : 'indeterminate' }, h('i', { style: { width: pct + '%' } })) : null,
      h('div.act-sub', h('span.tag', { class: t.state === 'done' ? 'ok' : t.state === 'error' ? 'danger' : t.state === 'running' ? 'accent' : '' }, STATE_LABEL[t.state]),
        h('span', live ? [detail, t.line && t.total ? ' · ' + t.line : ''] : [detail, ' · ', ago(t.finished || t.started)])),
      t.error_count ? h('details.act-errors', h('summary', `${num(t.error_count)} problem${t.error_count === 1 ? '' : 's'}`), h('ul', t.errors.map(e => h('li', e)), t.error_count > t.errors.length ? h('li.muted', `…and ${t.error_count - t.errors.length} more`) : null)) : null),
    cancel);
}

function jobCard(j) {
  const live = running(j);
  const pct = j.state === 'indexing' && j.total ? Math.floor(j.done / j.total * 100) : 0;
  const label = j.state === 'waiting' ? 'Waiting for another indexer' : j.state === 'listing' ? 'Scanning' : j.state === 'indexing' ? `Indexing ${num(j.done)} of ${num(j.total)}` : j.state === 'done' ? 'Done' : 'Failed';
  return h('article.act', { class: j.state },
    h('div.act-icon', icon('library')),
    h('div.act-main', h('div.act-title', `Index “${j.lib.name}”`),
      live ? h('div.meter', { class: j.state === 'indexing' && j.total ? '' : 'indeterminate' }, h('i', { style: { width: pct + '%' } })) : null,
      h('div.act-sub', h('span.tag', { class: j.state === 'done' ? 'ok' : j.state === 'error' ? 'danger' : live ? 'accent' : '' }, label),
        h('span.act-line', j.errors ? `${num(j.errors)} errors · ` : '', live ? '' : (j.finished ? ago(j.finished) + ' · ' : ''), (j.line || '').slice(0, 160)))),
    h('button.btn.small', { type: 'button', onclick: () => navigate('library', j.lib.id) }, 'Open'));
}
