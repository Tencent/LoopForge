import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { loadApp } from '../lib/load-app.mjs';

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../web');

function inlineCodeRules(css) {
  return [...css.matchAll(/([^{}]*\.markdown-body[^{}]*\bcode\b[^{}]*)\{([^}]*)\}/g)]
    .filter(([, selector]) => !/\bpre\s+code\b/.test(selector.replace(/:not\(pre code\)/g, '')))
    .map(([, selector, body]) => ({ selector: selector.replace(/\/\*[\s\S]*?\*\//g, '').trim(), body }));
}

// Requirement descriptions, artifact previews and skill docs render Markdown
// in .markdown-body. Inline code is literal: `slugify("a  b")` must keep both
// spaces, as the chat does (chatbot/src/styles.css .markdown code), or a
// whitespace bug report reads as if the input had one space.
test('inline code in .markdown-body keeps consecutive spaces', async () => {
  const css = await readFile(path.join(WEB, 'artifacts.css'), 'utf8');
  const base = inlineCodeRules(css).find((rule) => rule.selector === '.markdown-body code:not(pre code)');
  assert.ok(base, 'the shared inline code rule exists');
  assert.match(base.body, /white-space:\s*break-spaces/);
  assert.match(base.body, /overflow-wrap:\s*anywhere/, 'long unbroken code still wraps');

  const overrides = [];
  for (const dir of ['', 'app']) {
    for (const name of await readdir(path.join(WEB, dir))) {
      if (!name.endsWith('.css')) continue;
      const file = path.join(dir, name);
      for (const rule of inlineCodeRules(await readFile(path.join(WEB, file), 'utf8'))) {
        if (/white-space:\s*(normal|nowrap)/.test(rule.body)) overrides.push(`${file}: ${rule.selector}`);
      }
    }
  }
  assert.deepEqual(overrides, [], 'no stylesheet collapses inline code whitespace again');
});

test('the requirement Markdown keeps the spaces inside inline code', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const html = app.evaluate(`renderRequirementMarkdown('现状：\`slugify("Hello,  World!")\` 返回 \`"hello,--world!"\`')`);
  const box = app.document.createElement('div');
  box.className = 'markdown-body';
  box.innerHTML = html;
  assert.equal(box.querySelector('code').textContent, 'slugify("Hello,  World!")');
});
