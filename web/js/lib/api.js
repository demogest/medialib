// JSON API client. Every change carries X-Medialib: the server refuses changes without it, which web pages
// on other sites cannot add (it would need a CORS preflight the server never answers).
export class ApiError extends Error {
  constructor(message, { status = 0, code = '' } = {}) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export async function api(path, { method = 'GET', body, signal, headers = {} } = {}) {
  const init = { method, signal, headers: { ...headers } };
  if (method !== 'GET') init.headers['X-Medialib'] = '1';
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  let r;
  try {
    r = await fetch(path, init);
  } catch (e) {
    if (e.name === 'AbortError') throw e;
    throw new ApiError('Cannot reach the medialib server. Is it still running?');
  }
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new ApiError(j.error || r.statusText || 'Request failed', { status: r.status, code: j.code || '' });
  return j;
}
export const get = (path, opts) => api(path, opts);
export const post = (path, body = {}, opts) => api(path, { ...opts, method: 'POST', body });
export const put = (path, body = {}, opts) => api(path, { ...opts, method: 'PUT', body });
export const del = (path, opts) => api(path, { ...opts, method: 'DELETE' });

/** PUT a File to the server (which streams it into the bucket). Progress is reported as the browser sends bytes. */
export function upload(url, file, { onProgress, signal } = {}) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', url);
    xhr.setRequestHeader('X-Medialib', '1');
    if (file.type) xhr.setRequestHeader('Content-Type', file.type);
    xhr.upload.onprogress = e => e.lengthComputable && onProgress && onProgress(e.loaded, e.total);
    xhr.onload = () => {
      let j = {};
      try { j = JSON.parse(xhr.responseText); } catch { /* not JSON */ }
      if (xhr.status >= 200 && xhr.status < 300) resolve(j);
      else reject(new ApiError(j.error || xhr.statusText || 'Upload failed', { status: xhr.status, code: j.code || '' }));
    };
    xhr.onerror = () => reject(new ApiError('Upload failed: the connection to medialib dropped.'));
    xhr.onabort = () => reject(new DOMException('Aborted', 'AbortError'));
    if (signal) {
      if (signal.aborted) return reject(new DOMException('Aborted', 'AbortError'));
      signal.addEventListener('abort', () => xhr.abort(), { once: true });
    }
    xhr.send(file);
  });
}

/** URL builders for the storage API. */
export const s3Path = (conn, bucket, action, params = {}) => {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v != null && v !== '') q.set(k, v);
  const qs = q.toString();
  return `/api/s3/${encodeURIComponent(conn)}/b/${encodeURIComponent(bucket)}/${action}${qs ? '?' + qs : ''}`;
};
/** Stable proxied URL of an object (streams with Range support through medialib). */
export const objectUrl = (conn, bucket, key, download = false) =>
  `/s3/${encodeURIComponent(conn)}/${encodeURIComponent(bucket)}/${key.split('/').map(encodeURIComponent).join('/')}${download ? '?download=1' : ''}`;
