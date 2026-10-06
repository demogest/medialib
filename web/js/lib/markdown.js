// Markdown for release notes, built as elements and never as HTML: whatever the text holds shows as text. Covers
// what GitHub release notes use: headings, paragraphs, nested lists, quotes, code, tables, rules, links, bold,
// italic and strikethrough. Raw HTML stays text, images become links (nothing loads from elsewhere), and a link
// goes only to http(s) or mailto.
import { h } from './dom.js';

/** markdown(text, { skip }) -> elements. skip(heading) leaves out the section under a heading it accepts. */
export function markdown(text, { skip } = {}) {
  return render(parse(String(text || '').replace(/\r\n?/g, '\n').split('\n'), skip));
}

const HEADING = /^ {0,3}(#{1,6})\s+(.*?)(?:\s+#+)?\s*$/;
const RULE = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/;
const ITEM = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/;
const FENCE = /^\s*(```+|~~~+)\s*(\S*)/;
const TABLE_SEP = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
const indent = s => s.match(/^\s*/)[0].replace(/\t/g, '    ').length;
const cells = s => s.trim().replace(/^\|/, '').replace(/\|$/, '').replace(/\\\|/g, '\0').split('|').map(c => c.replace(/\0/g, '\\|').trim());

// Blocks: { t: 'h', level, text } | { t: 'p', text } | { t: 'hr' } | { t: 'code', text } | { t: 'quote', blocks }
// | { t: 'list', ordered, start, items: [blocks] } | { t: 'table', head, rows }.
function parse(lines, skip) {
  const out = [];
  let para = null, skipping = 0;
  const end = () => { para = null; };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    let m;
    if ((m = line.match(HEADING))) {
      end();
      if (skipping && m[1].length > skipping) continue;
      skipping = skip && skip(m[2]) ? m[1].length : 0;
      if (!skipping) out.push({ t: 'h', level: m[1].length, text: m[2] });
      continue;
    }
    if (skipping) continue;
    if (!line.trim()) { end(); continue; }
    if ((m = line.match(FENCE))) {
      end();
      const code = [];
      while (++i < lines.length && !lines[i].trim().startsWith(m[1])) code.push(lines[i]);
      out.push({ t: 'code', text: code.join('\n') });
      continue;
    }
    if (RULE.test(line)) { end(); out.push({ t: 'hr' }); continue; }
    if (/^ {0,3}>/.test(line)) {
      end();
      const inner = [];
      for (; i < lines.length && lines[i].trim() && /^ {0,3}>/.test(lines[i]); i++) inner.push(lines[i].replace(/^ {0,3}> ?/, ''));
      i--;
      out.push({ t: 'quote', blocks: parse(inner) });
      continue;
    }
    if (line.includes('|') && i + 1 < lines.length && TABLE_SEP.test(lines[i + 1]) && lines[i + 1].includes('-')) {
      end();
      const head = cells(line), rows = [];
      for (i += 2; i < lines.length && lines[i].includes('|') && lines[i].trim(); i++) rows.push(cells(lines[i]));
      i--;
      out.push({ t: 'table', head, rows });
      continue;
    }
    if ((m = line.match(ITEM)) && indent(line) < 4) {
      end();
      i = list(lines, i, out) - 1;
      continue;
    }
    if (para) para.text += '\n' + line.trim();
    else out.push(para = { t: 'p', text: line.trim() });
  }
  return out;
}

// A list from lines[i]: its items, each with the lines indented under it (nested lists, more paragraphs). Returns
// the line after it.
function list(lines, i, out) {
  const first = lines[i].match(ITEM), base = indent(lines[i]);
  const ordered = /\d/.test(first[2]), mark = first[2].slice(-1);
  const node = { t: 'list', ordered, start: ordered ? parseInt(first[2], 10) : 1, items: [] };
  out.push(node);
  let body = null, width = 0, gap = false;
  for (; i < lines.length; i++) {
    const line = lines[i];
    if (!line.trim()) { gap = true; body && body.push(''); continue; }
    const m = line.match(ITEM), at = indent(line);
    if (m && at <= base + 1 && /\d/.test(m[2]) === ordered && m[2].slice(-1) === mark) {
      node.items.push(body = [m[3]]);
      width = m[1].length + m[2].length + 1;
      gap = false;
      continue;
    }
    if (at >= base + Math.min(width, 2) && body) { body.push(line.slice(Math.min(at, base + width))); gap = false; continue; }
    // A line not indented under the item: it carries on the item's text, unless a blank line or another block
    // came between.
    if (!gap && !m && !HEADING.test(line) && !RULE.test(line) && !FENCE.test(line) && !/^ {0,3}>/.test(line)) { body.push(line.trim()); continue; }
    break;
  }
  node.items = node.items.map(lines => {
    while (lines.length && !lines[lines.length - 1].trim()) lines.pop();
    const blocks = parse(lines);
    node.loose = node.loose || lines.includes('');
    return blocks;
  });
  return i;
}

function render(blocks, tight = false) {
  return blocks.map(b => {
    switch (b.t) {
      case 'h': return h(b.level <= 2 ? 'h3' : b.level === 3 ? 'h4' : 'h5', inline(b.text));
      case 'p': return tight ? inline(b.text) : h('p', inline(b.text));
      case 'hr': return h('hr');
      case 'code': return h('pre', h('code', b.text));
      case 'quote': return h('blockquote', render(b.blocks));
      case 'table': return h('div.notes-table', h('table',
        h('thead', h('tr', b.head.map(c => h('th', inline(c))))),
        h('tbody', b.rows.map(r => h('tr', b.head.map((_, k) => h('td', inline(r[k] || ''))))))));
      case 'list': return h(b.ordered ? 'ol' : 'ul', { start: b.ordered && b.start !== 1 ? b.start : null },
        b.items.map(item => h('li', render(item, !b.loose))));
    }
    return null;
  });
}

// Inline: `code`, links, <autolinks>, bare URLs, **bold**, *italic*, ~~struck~~, \escapes and hard line breaks.
const INLINE = /(\\[\\`*_{}[\]()#+\-.!|~<>])|(`+)([\s\S]*?[^`])\2(?!`)|(!?)\[((?:[^\[\]]|\[[^\]]*\])*)\]\(\s*<?([^\s<>()]*(?:\([^\s()]*\)[^\s<>()]*)*)>?(?:\s+"[^"]*")?\s*\)|<((?:https?:\/\/|mailto:)[^\s<>]+)>|((?:https?:\/\/|www\.)[^\s<]*[^\s<.,:;"')\]!?*_~])|(\*\*|__)(?=\S)([\s\S]*?\S)\9|(\*|_)(?=[^\s*_])([\s\S]*?[^\s\\])\11(?![\w*])|(~~)(?=\S)([\s\S]*?\S)~~|( {2,}|\\)\n/g;

export function inline(text) {
  const out = [];
  const re = new RegExp(INLINE); // its own lastIndex: the calls below for **bold** and the like run one of their own
  let last = 0, m;
  while ((m = re.exec(text))) {
    // _ inside a word (snake_case) is not emphasis.
    if (m[11] === '_' && m.index > 0 && /\w/.test(text[m.index - 1])) { re.lastIndex = m.index + 1; continue; }
    if (m.index > last) out.push(text.slice(last, m.index));
    last = re.lastIndex;
    if (m[1]) out.push(m[1][1]);
    else if (m[2]) out.push(h('code', m[3].replace(/^ (.*) $/, '$1')));
    else if (m[5] != null && m[6] != null) out.push(link(m[6], m[4] ? m[5] || m[6] : inline(m[5])));
    else if (m[7]) out.push(link(m[7], m[7].replace(/^mailto:/, '')));
    else if (m[8]) out.push(link(m[8].startsWith('www.') ? 'https://' + m[8] : m[8], m[8]));
    else if (m[9]) out.push(h('strong', inline(m[10])));
    else if (m[11]) out.push(h('em', inline(m[12])));
    else if (m[13]) out.push(h('del', inline(m[14])));
    else if (m[15]) out.push(h('br'));
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function link(href, label) {
  let url = null;
  try { url = new URL(href); } catch { /* relative or not a URL */ }
  if (!url || !['http:', 'https:', 'mailto:'].includes(url.protocol)) return label;
  return h('a', { href: url.href, target: '_blank', rel: 'noopener noreferrer' }, label);
}
