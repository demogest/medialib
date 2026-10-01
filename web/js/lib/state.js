// Shared application state, and a watcher that keeps background work (tasks, indexing) up to date for every view.
import { get } from './api.js';

export const bus = new EventTarget();
export const emit = (name, detail) => bus.dispatchEvent(new CustomEvent(name, { detail }));
export const on = (name, fn) => { const f = e => fn(e.detail); bus.addEventListener(name, f); return () => bus.removeEventListener(name, f); };

export const state = {
  libs: [], activeLib: null,
  players: [], defaultPlayer: null,
  connections: [],
  system: null,
  jobs: {},     // indexing jobs by library id
  tasks: [],    // copy / move / delete / size tasks
};

export const running = job => !!job && ['waiting', 'listing', 'indexing'].includes(job.state);

export async function loadLibraries() {
  const j = await get('/api/libraries');
  state.libs = j.libraries;
  state.activeLib = j.active;
  emit('libraries');
  return j;
}
export async function loadConnections() {
  state.connections = (await get('/api/connections')).connections;
  emit('connections');
  return state.connections;
}
export async function loadPlayers() {
  const j = await get('/api/players');
  state.players = j.players;
  state.defaultPlayer = j.default;
  return j;
}

// ---------------------------------------------------------------- watcher
let timer = null, visible = true;
const busy = () => state.tasks.some(t => t.state === 'running') || Object.values(state.jobs).some(running);

async function tick() {
  clearTimeout(timer);
  try {
    const [jobs, tasks] = await Promise.all([get('/api/index'), get('/api/tasks')]);
    const before = state.tasks;
    state.jobs = jobs.jobs;
    state.tasks = tasks.tasks;
    for (const t of state.tasks) {
      const prev = before.find(b => b.id === t.id);
      if (prev && prev.state === 'running' && t.state !== 'running') emit('task-finished', t);
    }
    emit('activity');
  } catch { /* the server may be restarting: try again */ }
  timer = setTimeout(tick, !visible ? 15000 : busy() ? 1200 : 5000);
}
export function startWatcher() {
  document.addEventListener('visibilitychange', () => { visible = !document.hidden; if (visible) tick(); });
  return tick();
}
/** Look at the server's activity right now (after starting a task) instead of waiting for the next tick. */
export const pokeWatcher = () => tick();
