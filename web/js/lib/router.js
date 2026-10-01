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
  let current = null, name = null, token = 0;
  async function apply() {
    let { view, parts } = parseHash();
    if (!views[view]) { view = fallback; parts = []; history.replaceState(null, '', location.pathname + location.search + href(view)); }
    const mine = ++token;
    if (current && name === view && current.update) {
      current.update(parts);
    } else {
      if (current && current.destroy) current.destroy();
      root.replaceChildren();
      current = null;
      name = view;
      const mod = await views[view]();
      if (mine !== token) return;  // another navigation won while the module was loading
      current = await mod.mount(root, parts);
    }
    onChange && onChange(view, parts);
  }
  window.addEventListener('hashchange', apply);
  return apply();
}
