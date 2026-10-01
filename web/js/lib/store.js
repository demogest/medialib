// localStorage that never throws (private windows, blocked storage).
export const store = {
  get(key, fallback) {
    try { return localStorage.getItem(key) ?? fallback; } catch { return fallback; }
  },
  set(key, value) {
    try { localStorage.setItem(key, value); } catch { /* private mode: the setting just won't persist */ }
  },
  json(key, fallback) {
    try { const v = localStorage.getItem(key); return v == null ? fallback : JSON.parse(v); } catch { return fallback; }
  },
  setJson(key, value) { this.set(key, JSON.stringify(value)); },
};
