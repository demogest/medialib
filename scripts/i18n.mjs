#!/usr/bin/env node
// Checks the interface's translations, and starts new ones. See "Translations" in CONTRIBUTING.md.
//
//   node scripts/i18n.mjs check       every key the code uses is in en.json and every key there is used; each
//                                     translation parses, has only English's keys and the same placeholders; the
//                                     template is current. Untranslated messages are listed, not failed.
//   node scripts/i18n.mjs template    rewrite web/locales/template.json from en.json
//   node scripts/i18n.mjs new <code>  start web/locales/<code>.json from the template
import { readFileSync, writeFileSync, readdirSync, existsSync } from 'node:fs';
import { join, relative, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse, argNames } from '../web/js/lib/messageformat.js';
import { LANGUAGES, SOURCE } from '../web/js/lib/languages.js';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const dir = join(root, 'web', 'locales');
const jsDir = join(root, 'web', 'js');
const TEMPLATE = 'template.json';
const ci = !!process.env.GITHUB_ACTIONS;

const read = f => JSON.parse(readFileSync(join(dir, f), 'utf8'));
const write = (f, obj) => writeFileSync(join(dir, f), JSON.stringify(obj, null, 2) + '\n');
const blank = src => Object.fromEntries(Object.keys(src).map(k => [k, '']));

let errors = 0, warnings = 0;
const error = (file, msg) => { errors++; console.log(ci ? `::error file=${file}::${msg}` : `${file}: ${msg}`); };
const warn = (file, msg) => { warnings++; console.log(ci ? `::warning file=${file}::${msg}` : `${file}: ${msg}`); };

function jsFiles(d) {
  return readdirSync(d, { withFileTypes: true }).flatMap(e =>
    e.isDirectory() ? jsFiles(join(d, e.name)) : e.name.endsWith('.js') ? [join(d, e.name)] : []);
}

function check() {
  const en = read(`${SOURCE}.json`);
  const enFile = `web/locales/${SOURCE}.json`;
  const enArgs = {};
  for (const [k, v] of Object.entries(en)) {
    if (typeof v !== 'string' || !v) { error(enFile, `${k}: empty`); continue; }
    try { enArgs[k] = argNames(parse(v)); } catch (e) { error(enFile, `${k}: ${e.message}`); }
  }

  // Keys in the code: t('key' or tx('key' with a literal key, so every one can be found here.
  const used = new Set();
  for (const f of jsFiles(jsDir)) {
    const rel = relative(root, f);
    // Comments may show t() in examples; they are not uses.
    const src = readFileSync(f, 'utf8').split('\n').map(l => (/^\s*(\/\/|\/?\*)/.test(l) ? '' : l)).join('\n');
    const imports = /import\s*\{[^}]*\bt\b[^}]*\}\s*from\s*['"][./]*(?:lib\/)?i18n\.js['"]/.test(src);
    for (const m of src.matchAll(/(?<![\w.$])tx?\(\s*(['"`])?([^'"`)\s,]*)/g)) {
      if (!m[1] || m[1] === '`') { if (imports) error(rel, `t( needs a literal key: ${m[0]}…`); continue; }
      used.add(m[2]);
      if (!(m[2] in en)) error(rel, `${m[2]}: not in ${enFile}`);
    }
    // A local `t` hides the translate function: t('x') would call the wrong thing.
    if (imports) {
      const lines = src.split('\n');
      lines.forEach((line, n) => {
        if (/(?:[(,]\s*t\s*[,)=]|\bt\s*=>|\b(?:let|const|var)\s+t\b|function\s*\w*\s*\(\s*t\b)/.test(line.replace(/\/\/.*$/, '').replace(/(['"`])(?:\\.|(?!\1).)*\1/g, '""')))
          error(`${rel}:${n + 1}`, 'a variable named t hides the translate function; rename it');
      });
    }
  }
  for (const k of Object.keys(en)) if (!used.has(k)) error(enFile, `${k}: not used by the interface`);

  // Translations.
  const files = readdirSync(dir).filter(f => f.endsWith('.json'));
  const listed = new Set(LANGUAGES.map(([c]) => c));
  for (const code of listed) if (!files.includes(`${code}.json`)) error('web/js/lib/languages.js', `${code}: no web/locales/${code}.json`);
  for (const f of files) {
    if (f === TEMPLATE || f === `${SOURCE}.json`) continue;
    const file = `web/locales/${f}`;
    const code = f.replace(/\.json$/, '');
    if (!listed.has(code)) error(file, `${code} is not in LANGUAGES in web/js/lib/languages.js`);
    const tr = read(f);
    let missing = 0;
    for (const [k, v] of Object.entries(tr)) {
      if (!(k in en)) { error(file, `${k}: not in ${enFile} (renamed or removed?)`); continue; }
      if (typeof v !== 'string') { error(file, `${k}: not a string`); continue; }
      if (!v) continue;
      let args;
      try { args = argNames(parse(v)); } catch (e) { error(file, `${k}: ${e.message}`); continue; }
      const want = enArgs[k] || new Set();
      const extra = [...args].filter(a => !want.has(a)), lost = [...want].filter(a => !args.has(a));
      if (extra.length || lost.length) error(file, `${k}: placeholders differ from English (${[...extra.map(a => '+' + a), ...lost.map(a => '-' + a)].join(' ')})`);
    }
    for (const k of Object.keys(en)) if (!tr[k]) missing++;
    if (missing) warn(file, `${missing} of ${Object.keys(en).length} messages not translated yet (English shows instead)`);
  }

  // The template: every key of English, empty, in the same order.
  const tpl = existsSync(join(dir, TEMPLATE)) ? readFileSync(join(dir, TEMPLATE), 'utf8') : '';
  if (tpl !== JSON.stringify(blank(en), null, 2) + '\n') error(`web/locales/${TEMPLATE}`, 'out of date: run node scripts/i18n.mjs template');

  console.log(errors ? `i18n: ${errors} problem(s)` : `i18n ok${warnings ? ` (${warnings} warning(s))` : ''}`);
  process.exit(errors ? 1 : 0);
}

const [cmd, arg] = process.argv.slice(2);
if (cmd === 'check') check();
else if (cmd === 'template') { write(TEMPLATE, blank(read(`${SOURCE}.json`))); console.log(`wrote web/locales/${TEMPLATE}`); }
else if (cmd === 'new' && /^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$/.test(arg || '')) {
  const f = `${arg}.json`;
  if (existsSync(join(dir, f))) { console.error(`web/locales/${f} exists already`); process.exit(1); }
  write(f, blank(read(`${SOURCE}.json`)));
  console.log(`wrote web/locales/${f}; add ['${arg}', '<its own name>'] to LANGUAGES in web/js/lib/languages.js`);
} else {
  console.error('usage: node scripts/i18n.mjs check | template | new <code, like fr or pt-BR>');
  process.exit(2);
}
