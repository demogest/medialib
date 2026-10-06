// Global keyboard shortcuts. They stay out of the way while typing in a field (except the Ctrl combinations).
import { navigate } from '../lib/router.js';
import { openPalette } from './palette.js';
import { SECTIONS, offered, toggleSidebar } from './sidebar.js';

export function installShortcuts() {
  document.addEventListener('keydown', e => {
    const mod = e.ctrlKey || e.metaKey;
    if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'k') { e.preventDefault(); openPalette(); }
    else if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'b') { e.preventDefault(); toggleSidebar(); }
    else if (e.altKey && !mod && /^[1-5]$/.test(e.key) && !e.defaultPrevented && offered(SECTIONS[+e.key - 1])) { e.preventDefault(); navigate(SECTIONS[+e.key - 1].id); }
  });
}
