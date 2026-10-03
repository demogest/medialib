// Formatting helpers.
export const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
export const num = n => (n ?? 0).toLocaleString('en-US');
const UNITS = ['KB', 'MB', 'GB', 'TB', 'PB']; // 1024-based, as file managers count
export const bytes = n => {
  n = n || 0;
  if (n < 1024) return n + ' B';
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < UNITS.length - 1);
  return n.toFixed(n >= 100 ? 0 : 1) + ' ' + UNITS[i];
};
const pad = n => String(n).padStart(2, '0');
export const clock = s => {
  s = Math.round(s || 0);
  const h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60);
  return h ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`;
};
export const span = s => {
  if (s > 0 && s < 59.5) return `${Math.round(s)} s`;
  const h = Math.floor(s / 3600), m = Math.round(s % 3600 / 60);
  return h ? `${h} h ${m} min` : `${m} min`;
};
export const resLabel = (w, h) => {
  if (!w || !h) return '';
  const hi = Math.max(w, h), lo = Math.min(w, h);
  return hi >= 7600 ? '8K' : hi >= 3800 ? '4K' : hi >= 2500 ? '1440p' : hi >= 1900 ? '1080p' : hi >= 1260 ? '720p' : lo + 'p';
};
export const stem = n => n.replace(/\.[^.]+$/, '');
export const leaf = p => p.replace(/\/$/, '').slice(p.replace(/\/$/, '').lastIndexOf('/') + 1);
export const extOf = name => { const i = name.lastIndexOf('.'); return i > 0 ? name.slice(i + 1).toLowerCase() : ''; };

/** '2026-09-29T11:37:57Z' -> '2026-09-29 11:37' in local time. */
export function when(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
export function ago(iso) {
  const t = typeof iso === 'number' ? iso * 1000 : Date.parse(iso);
  if (isNaN(t)) return '';
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + ' min ago';
  if (s < 86400) return Math.floor(s / 3600) + ' h ago';
  if (s < 86400 * 30) return Math.floor(s / 86400) + ' d ago';
  return when(iso).slice(0, 10);
}
export const plural = (n, one, many = one + 's') => `${num(n)} ${n === 1 ? one : many}`;

// What kind of thing a file is, from its name. Drives icons and which preview opens.
const KINDS = {
  video: ['mp4', 'm4v', 'mov', 'mkv', 'webm', 'avi', 'wmv', 'flv', 'ts', 'm2ts', 'mpg', 'mpeg', '3gp'],
  audio: ['mp3', 'flac', 'm4a', 'aac', 'wav', 'ogg', 'opus', 'wma'],
  image: ['jpg', 'jpeg', 'png', 'gif', 'webp', 'avif', 'bmp', 'svg', 'ico', 'tif', 'tiff'],
  text: ['txt', 'md', 'json', 'csv', 'log', 'xml', 'yml', 'yaml', 'toml', 'ini', 'srt', 'vtt', 'ass', 'nfo', 'html', 'css', 'js', 'py', 'sh', 'conf'],
  archive: ['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'zst'],
};
const KIND_OF = new Map(Object.entries(KINDS).flatMap(([k, exts]) => exts.map(e => [e, k])));
export const kindOf = name => KIND_OF.get(extOf(name)) || 'file';
