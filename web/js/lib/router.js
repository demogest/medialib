// Hash router: #/<view>/<part>/<part>... with every part URI-encoded.
// A view module exports mount(root, parts) -> { update?(parts), destroy?() }.
export const href = (view, ...parts) => '#/' + [view, ...parts.filter(p => p != null && p !== '')].map(encodeURIComponent).join('/');

export function parseHash(hash = location.hash) {
  const raw = hash.replace(/^#\/?/, '');
  if (!raw) return { view: '', parts: [] };
  const [view, ...parts] = raw.split('/').map(s => { try { return decodeURIComponent(s); } catch { return s; } });
  return { view, parts };
}

export function navigate(view, ...parts) {
  const h = href(view, ...parts);
  if (location.hash !== h) location.hash = h;
}
/** Replace the current history entry instead of adding one. */
export function replace(view, ...parts) {
  history.replaceState(null, '', location.pathname + location.search + href(view, ...parts));
  window.dispatchEvent(new HashChangeEvent('hashchange'));
}

export function startRouter({ root, views, fallback, onChange }) {
  // A Map, so the name in the address can only ever pick one of these loaders: looked up on the object itself, it
  // could also find what every object inherits ("#/constructor", "#/toString") and call that.
  const loaders = new Map(Object.entries(views));
  let current = null, name = null, token = 0;
  async function apply() {
    let { view, parts } = parseHash();
    if (!loaders.has(view)) { view = fallback; parts = []; history.replaceState(null, '', location.pathname + location.search + href(view)); }
    const mine = ++token;
    if (current && name === view && current.update) {
      current.update(parts);
    } else {
      if (current && current.destroy) current.destroy();
      root.replaceChildren();
      current = null;
      name = view;
      const mod = await loaders.get(view)();
      if (mine !== token) return;  // another navigation won while the module was loading
      const inst = await mod.mount(root, parts);
      // Another navigation won while this view was loading (a view may redirect while it mounts, and the first
      // visit does). It never became the current view, so nothing else would ever destroy it: do it here, or its
      // listeners live on and answer requests meant for the real one.
      if (mine !== token) { if (inst && inst.destroy) inst.destroy(); return; }
      current = inst;
      if (inst && inst.ready) inst.ready();   // for what the view may only do once it is the current one
    }
    onChange && onChange(view, parts);
  }
  window.addEventListener('hashchange', apply);
  return apply();
}
