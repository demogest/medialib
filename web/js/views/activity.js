// Activity: copy / move / delete / size tasks and library scans, with live progress.
import { h, fill } from '../lib/dom.js';
import { icon } from '../lib/icons.js';
import { del, post } from '../lib/api.js';
import { ago, bytes } from '../lib/fmt.js';
import { navigate } from '../lib/router.js';
import { store } from '../lib/store.js';
import { on, pokeWatcher, requestLibraryAction, running, state } from '../lib/state.js';
import { toastError } from '../lib/ui.js';
import { t } from '../lib/i18n.js';

const KIND_ICON = { copy: 'copy', move: 'move', delete: 'trash', size: 'layers' };
const STATE_LABEL = { running: t('activity.running'), done: t('common.done'), error: t('activity.failed'), cancelled: t('activity.cancelled') };

export function mount(root) {
  const body = h('div');
  // Finished scans are the server's last word on each library, so clearing them only hides them here, until the next scan.
  const clear = h('button.btn', { type: 'button', onclick: async () => {
    store.set('scansCleared', String(Date.now() / 1000));
    try { await del('/api/tasks'); } catch (e) { toastError(t('activity.clearFailed'), e); }
    pokeWatcher(); render();
  } }, t('activity.clearFinished'));
  root.append(h('div.page', h('div.page-inner',
    h('div.page-head', h('div', h('h1', t('nav.activity')), h('p', t('activity.intro'))),
      h('div.actions', clear)),
    body)));

  const render = () => {
    const tasks = state.tasks;
    const cleared = Number(store.get('scansCleared', '0')) || 0;
    const jobs = Object.entries(state.jobs).map(([id, j]) => ({ id, ...j, lib: state.libs.find(l => l.id === id) }))
      .filter(j => j.lib && (running(j) || !(j.finished <= cleared)));
    const active = [...tasks.filter(task => task.state === 'running').map(taskCard), ...jobs.filter(j => running(j)).map(jobCard)];
    const recent = [...tasks.filter(task => task.state !== 'running').map(taskCard), ...jobs.filter(j => !running(j)).map(jobCard)];
    clear.disabled = !recent.length;
    fill(body,
      active.length ? [h('div.section-title', t('activity.inProgress')), h('div.act-list', active)] : null,
      recent.length ? [h('div.section-title', t('activity.recent')), h('div.act-list', recent)] : null,
      !active.length && !recent.length ? h('div.card-box.empty-state', icon('activity'), h('h3', t('activity.emptyTitle')), h('p', t('activity.emptyText'))) : null);
  };

  const off = on('activity', render);
  render();
  return { destroy: off };
}

function taskCard(task) {
  const pct = task.total ? Math.min(100, Math.round(task.done / task.total * 100)) : 0;
  const live = task.state === 'running';
  const cancel = live ? h('button.btn.small', { type: 'button', onclick: async () => { try { await post(`/api/tasks/${task.id}/cancel`); pokeWatcher(); } catch (e) { toastError(t('activity.cancelFailed'), e); } } }, icon('stop', 'sm'), t('common.cancel'))
    : h('button.icon-btn.small', { type: 'button', 'aria-label': t('common.dismiss'), onclick: async () => { try { await del(`/api/tasks/${task.id}`); } catch (e) { toastError(t('activity.clearFailed'), e); } pokeWatcher(); } }, icon('x'));
  const detail = task.kind === 'size' && task.result ? t('activity.sizeResult', { count: task.result.objects, size: bytes(task.result.bytes) })
    : task.total ? (task.bytes ? t('activity.progressBytes', { done: task.done, total: task.total, size: bytes(task.bytes) }) : t('activity.progress', { done: task.done, total: task.total })) : task.line;
  return h('article.act', { class: task.state },
    h('div.act-icon', icon(KIND_ICON[task.kind] || 'activity')),
    h('div.act-main',
      h('div.act-title', task.title),
      live ? h('div.meter', { class: task.total ? '' : 'indeterminate' }, h('i', { style: { width: pct + '%' } })) : null,
      h('div.act-sub', h('span.tag', { class: task.state === 'done' ? 'ok' : task.state === 'error' ? 'danger' : task.state === 'running' ? 'accent' : '' }, STATE_LABEL[task.state]),
        h('span', live ? [detail, task.line && task.total ? ' · ' + task.line : ''] : [detail, ' · ', ago(task.finished || task.started)])),
      task.error_count ? h('details.act-errors', h('summary', t('activity.problems', { count: task.error_count })), h('ul', task.errors.map(e => h('li', e)), task.error_count > task.errors.length ? h('li.muted', t('activity.andMore', { count: task.error_count - task.errors.length })) : null)) : null),
    cancel);
}

function jobCard(j) {
  const live = running(j);
  const pct = j.state === 'indexing' && j.total ? Math.floor(j.done / j.total * 100) : 0;
  const label = j.state === 'waiting' ? t('activity.waiting') : j.state === 'listing' ? t('activity.listing') : j.state === 'indexing' ? t('activity.indexing', { done: j.done, total: j.total }) : j.state === 'done' ? t('common.done') : t('activity.failed');
  return h('article.act', { class: j.state },
    h('div.act-icon', icon('library')),
    h('div.act-main', h('div.act-title', t('activity.scanLibrary', { name: j.lib.name })),
      live ? h('div.meter', { class: j.state === 'indexing' && j.total ? '' : 'indeterminate' }, h('i', { style: { width: pct + '%' } })) : null,
      h('div.act-sub', h('span.tag', { class: j.state === 'done' ? 'ok' : j.state === 'error' ? 'danger' : live ? 'accent' : '' }, label),
        // What the scan says as it goes is for the log; here: when it ended, what failed, and why a scan stopped.
        h('span.act-line', [j.errors ? t('activity.errors', { count: j.errors }) : '', live || !j.finished ? '' : ago(j.finished), j.state === 'error' ? (j.line || '').slice(0, 160) : '']
          .filter(Boolean).join(' · '))),
      ),
    j.errors && !live ? h('button.btn.small', { type: 'button', onclick: () => { navigate('library', j.lib.id); requestLibraryAction('failed', { lib: j.lib.id }); } }, t('activity.seeWhich')) : null,
    h('button.btn.small', { type: 'button', onclick: () => navigate('library', j.lib.id) }, t('common.open')));
}
