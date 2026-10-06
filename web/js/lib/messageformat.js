// ICU MessageFormat, the part the interface uses: {name}, {count, plural, =0 {…} one {# video} other {# videos}},
// {kind, select, a {…} other {…}} and '' for an apostrophe next to a brace. Pure, so scripts/i18n.mjs checks the
// message files with the same parser the browser formats them with.

const HASH = { hash: true };

/** Parse a message into parts: strings, HASH (a plural's #), and { arg, type?, options? } (options: { selector: parts }). Throws on a syntax error. */
export function parse(src) {
  let i = 0;
  function text(inPlural, depth) {
    const out = [];
    let s = '';
    const flush = () => { if (s) out.push(s); s = ''; };
    while (i < src.length) {
      const c = src[i];
      if (c === "'") {
        const next = src[i + 1];
        if (next === "'") { s += "'"; i += 2; continue; }
        if (next === '{' || next === '}' || (inPlural && next === '#')) {
          const end = src.indexOf("'", i + 1);
          if (end < 0) throw new SyntaxError('unclosed quote');
          s += src.slice(i + 1, end);
          i = end + 1;
          continue;
        }
        s += c; i++; continue;
      }
      if (c === '{') { flush(); out.push(placeholder(inPlural)); continue; }
      if (c === '}') {
        if (!depth) throw new SyntaxError('unexpected }');
        break;
      }
      if (c === '#' && inPlural) { flush(); out.push(HASH); i++; continue; }
      s += c; i++;
    }
    flush();
    return out;
  }
  function word() {
    while (/\s/.test(src[i] || '')) i++;
    const m = /^[^\s,{}]+/.exec(src.slice(i));
    if (!m) throw new SyntaxError(`expected a name at ${i}`);
    i += m[0].length;
    while (/\s/.test(src[i] || '')) i++;
    return m[0];
  }
  function placeholder(inPlural) {
    i++; // {
    const arg = word();
    if (src[i] === '}') { i++; return { arg }; }
    if (src[i] !== ',') throw new SyntaxError(`expected , or } after ${arg}`);
    i++;
    const type = word();
    if (type !== 'plural' && type !== 'select' && type !== 'number') throw new SyntaxError(`unknown type ${type}`);
    if (type === 'number') {
      if (src[i] !== '}') throw new SyntaxError('number takes no style here');
      i++;
      return { arg };
    }
    if (src[i] !== ',') throw new SyntaxError(`expected , after ${type}`);
    i++;
    const options = {};
    for (;;) {
      while (/\s/.test(src[i] || '')) i++;
      if (src[i] === '}') { i++; break; }
      const key = word();
      if (src[i] !== '{') throw new SyntaxError(`expected { after ${key}`);
      i++;
      options[key] = text(inPlural || type === 'plural', 1);
      if (src[i] !== '}') throw new SyntaxError(`unclosed option ${key}`);
      i++;
    }
    if (!options.other) throw new SyntaxError(`${arg}: ${type} needs an "other" option`);
    return { arg, type, options };
  }
  const parts = text(false, 0);
  if (i < src.length) throw new SyntaxError(`unexpected } at ${i}`);
  return parts;
}

/** Names of the arguments a message uses, at any depth. */
export function argNames(parts, into = new Set()) {
  for (const p of parts) {
    if (typeof p !== 'object' || p === HASH) continue;
    into.add(p.arg);
    for (const o of Object.values(p.options || {})) argNames(o, into);
  }
  return into;
}

/** Format parsed parts. Values may be strings, numbers or DOM nodes; the result is an array of strings and nodes. */
export function format(parts, values = {}, locale = 'en') {
  const nf = new Intl.NumberFormat(locale);
  const show = v => (typeof v === 'number' ? nf.format(v) : v);
  const out = [];
  const walk = (list, n) => {
    for (const p of list) {
      if (typeof p === 'string') out.push(p);
      else if (p === HASH) out.push(nf.format(n));
      else if (!p.type) out.push(values[p.arg] == null ? `{${p.arg}}` : show(values[p.arg]));
      else {
        const v = values[p.arg];
        let pick;
        if (p.type === 'plural') {
          pick = p.options['=' + v] || p.options[new Intl.PluralRules(locale).select(Number(v) || 0)] || p.options.other;
          walk(pick, Number(v) || 0);
        } else {
          pick = p.options[String(v)] || p.options.other;
          walk(pick, n);
        }
      }
    }
  };
  walk(parts, 0);
  return out;
}
