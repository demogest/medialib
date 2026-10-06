// Application shell: sidebar, command palette, shortcuts, routing.
import { $ } from './lib/dom.js';
import { startRouter } from './lib/router.js';
import { state, startWatcher, loadConnections, loadLibraries, loadPlayers, loadSystem, loadUpdate } from './lib/state.js';
import { toastError } from './lib/ui.js';
import { buildSidebar } from './shell/sidebar.js';
import { openPalette } from './shell/palette.js';
import { installShortcuts } from './shell/shortcuts.js';

const VIEWS = {
  home: () => import('./views/home.js'),
  library: () => import('./views/library.js'),
  storage: () => import('./views/storage.js'),
  connections: () => import('./views/connections.js'),
  activity: () => import('./views/activity.js'),
  settings: () => import('./views/settings.js'),
  welcome: () => import('./views/welcome.js'),
};

async function boot() {
  const side = buildSidebar($('#rail'), { onSearch: openPalette });
  installShortcuts();
  try {
    await Promise.all([loadLibraries(), loadConnections(), loadPlayers(), loadSystem()]);
  } catch (e) {
    toastError('Could not reach the server', e);
  }
  startWatcher();
  // The server looks for a new version itself as it starts (and once a day after): read what it found.
  if (state.system?.can_edit) {
    loadUpdate();
    setTimeout(loadUpdate, 5000);
    setTimeout(loadUpdate, 40000);
    setInterval(loadUpdate, 3600000);
  }
  // Home: the guide on a first run, else what was played and added lately. Someone who only browses buckets
  // (connections, no library) starts in Storage.
  const fallback = !state.libs.length && state.connections.length ? 'storage' : 'home';
  await startRouter({ root: $('#view'), views: VIEWS, fallback, onChange: side.paintActive });
}

boot();
