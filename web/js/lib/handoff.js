// Playing on a phone, a tablet or another computer: the server cannot open a player on that screen, so the video
// goes to the browser if it can play it, and otherwise to a player app on the device (VLC, Infuse), or to a
// playlist the device's own player opens.
import { h } from './dom.js';
import { get } from './api.js';
import { extOf } from './fmt.js';
import { state } from './state.js';
import { store } from './store.js';
import { modal, toast, toastError } from './ui.js';

const ua = navigator.userAgent;
const isIOS = /iPad|iPhone|iPod/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1);
const isAndroid = /Android/.test(ua);

// What the browser is asked about a file: its container, and its video codec as ffprobe names it.
const CONTAINER = { mp4: 'video/mp4', m4v: 'video/mp4', mov: 'video/quicktime', webm: 'video/webm', mkv: 'video/x-matroska',
  mp3: 'audio/mpeg', m4a: 'audio/mp4', aac: 'audio/aac', flac: 'audio/flac', wav: 'audio/wav', ogg: 'audio/ogg', opus: 'audio/ogg' };
const CODEC_NAME = { h264: 'H.264', hevc: 'HEVC', av1: 'AV1', vp9: 'VP9', vp8: 'VP8', mpeg4: 'MPEG-4', mpeg2video: 'MPEG-2', vc1: 'VC-1' };
const CODEC = { h264: 'avc1.640028', hevc: 'hvc1.1.6.L120.90', av1: 'av01.0.08M.08', vp9: 'vp09.00.40.08', vp8: 'vp8' };

/** Whether this browser can most likely play the file itself. */
export function browserPlays(it) {
  const type = CONTAINER[extOf(it.name)];
  if (!type) return false;
  const probe = document.createElement(it.kind === 'audio' ? 'audio' : 'video');
  if (it.kind === 'audio') return probe.canPlayType(type) !== '';
  const codec = CODEC[it.codec];
  if (it.codec && !codec) return false; // MPEG-2, VC-1, old MPEG-4: no browser plays them
  return probe.canPlayType(codec ? `${type}; codecs="${codec}"` : type) !== '';
}

// A link the player app can open by itself: a bucket's own link (no password needed), or this server's address.
async function streamUrl(lib, it) {
  const l = state.libs.find(x => x.id === lib);
  if (l && l.type !== 'local') {
    try { return (await get(`/api/link?lib=${encodeURIComponent(lib)}&id=${encodeURIComponent(it.id)}`)).url; } catch { /* fall back to this server */ }
  }
  return `${location.origin}/media/${lib}/${it.id}/${encodeURIComponent(it.name)}`;
}
const subUrl = (lib, it) => (it.subs?.length ? `${location.origin}/subs/${lib}/${it.id}/${encodeURIComponent(it.subs[0])}` : '');

// The apps a video can be handed to on this device, with the link that opens each.
function apps(url, sub, it) {
  const q = encodeURIComponent;
  if (isIOS) {
    return [
      { id: 'vlc', label: 'Open in VLC', href: `vlc-x-callback://x-callback-url/stream?url=${q(url)}` + (sub ? `&sub=${q(sub)}` : '') },
      { id: 'infuse', label: 'Open in Infuse', href: `infuse://x-callback-url/play?url=${q(url)}` },
    ];
  }
  if (isAndroid) {
    const u = new URL(url);
    const intent = pkg => `intent://${u.host}${u.pathname}${u.search}#Intent;scheme=${u.protocol.slice(0, -1)};type=${it.kind === 'audio' ? 'audio' : 'video'}/*;`
      + (pkg ? `package=${pkg};` : '') + `S.title=${q(it.name)};end`;
    return [
      { id: 'vlc', label: 'Open in VLC', href: intent('org.videolan.vlc') },
      { id: 'other', label: 'Open in another app', href: intent('') },
    ];
  }
  return [];
}

/** Play one file on this device: in the browser when it can, otherwise offer the player apps it has. */
export async function playHere(lib, it) {
  const url = await streamUrl(lib, it);
  if (browserPlays(it)) {
    window.open(url, '_blank');
    toast('Opening the video in your browser', { kind: 'ok' });
    return;
  }
  const list = apps(url, subUrl(lib, it), it);
  const last = store.get('handoffApp', '');
  const go = a => { store.set('handoffApp', a.id); location.href = a.href; };
  const playlist = `/api/playlist.m3u8?lib=${encodeURIComponent(lib)}&ids=${encodeURIComponent(it.id)}`;
  const copy = async () => {
    try { await navigator.clipboard.writeText(url); toast('Link copied', { kind: 'ok' }); } catch (e) { toastError('Could not copy the link', e); }
  };
  const ext = extOf(it.name).toUpperCase();
  const m = modal({
    title: 'Play in an app',
    body: h('div',
      h('p', `This browser can’t play ${ext ? 'this ' + ext + ' video' : 'this file'}${it.codec ? ` (${CODEC_NAME[it.codec] || it.codec.toUpperCase()})` : ''}. Open it in a player app instead.`),
      list.length ? h('div.handoff', list.map(a => h('a.btn', { href: a.href, class: a.id === last || (!last && a === list[0]) ? 'primary' : '',
        onclick: e => { e.preventDefault(); m.close(); go(a); } }, a.label)))
        : h('p.hint', 'Download the playlist and open it with VLC, mpv or any other player on this computer.'),
      list.length ? h('p.hint', isIOS ? 'Each one needs its app from the App Store.' : 'Each one needs its app from Google Play.') : null),
    actions: [
      { label: 'Copy link', left: true, keepOpen: true, onClick: copy },
      { label: 'Try the browser', onClick: () => window.open(url, '_blank') },
      list.length ? null : { label: 'Download playlist', primary: true, onClick: () => { location.href = playlist; } },
    ].filter(Boolean),
  });
}
