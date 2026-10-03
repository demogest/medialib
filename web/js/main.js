// Application shell: sidebar, command palette, shortcuts, routing.
import { $ } from './lib/dom.js';
import { startRouter } from './lib/router.js';
import { state, startWatcher, loadConnections, loadLibraries, loadPlayers, loadSystem } from './lib/state.js';
import { toastError } from './lib/ui.js';
import { buildSidebar } from './shell/sidebar.js';
import { openPalette } from './shell/palette.js';
import { installShortcuts } from './shell/shortcuts.js';

const VIEWS = {
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
  const fallback = !state.libs.length && !state.connections.length ? 'welcome' : state.libs.length || !state.connections.length ? 'library' : 'storage';
  await startRouter({ root: $('#view'), views: VIEWS, fallback, onChange: side.paintActive });
}

boot();
