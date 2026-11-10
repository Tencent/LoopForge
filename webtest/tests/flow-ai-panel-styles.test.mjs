import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../web');
const KEYWORDS = new Set(['none', 'infinite', 'linear', 'ease', 'ease-in', 'ease-out', 'ease-in-out', 'forwards', 'backwards', 'both', 'alternate', 'alternate-reverse', 'reverse', 'normal', 'paused', 'running', 'step-start', 'step-end', 'inherit', 'initial', 'unset']);

async function studioStyles() {
  const files = [];
  for (const dir of ['', 'app']) {
    for (const name of await readdir(path.join(WEB, dir))) {
      if (name.endsWith('.css')) files.push(path.join(dir, name));
    }
  }
  return Promise.all(files.map(async (file) => ({ file, css: await readFile(path.join(WEB, file), 'utf8') })));
}

// A stylesheet removal once took @keyframes with it while rules kept using
// them, so canvas animations silently stopped.
test('every studio animation name has a @keyframes definition', async () => {
  const sheets = await studioStyles();
  const defined = new Set(sheets.flatMap(({ css }) => [...css.matchAll(/@keyframes\s+([\w-]+)/g)].map((m) => m[1])));
  const missing = [];
  for (const { file, css } of sheets) {
    for (const [, value] of css.matchAll(/animation(?:-name)?\s*:\s*([^;}]+)/g)) {
      for (const token of value.split(/[\s,]+/)) {
        if (/^[a-zA-Z][\w-]*$/.test(token) && !KEYWORDS.has(token) && !defined.has(token)) missing.push(`${file}: ${token}`);
      }
    }
  }
  assert.deepEqual(missing, []);
});

// #134: without these rules the composer overlapped the scenario chips and
// the header buttons wrapped under the title.
test('AI orchestrator panel stacks header, log and composer', async () => {
  const css = await readFile(path.join(WEB, 'app/flow-canvas.css'), 'utf8');
  const html = await readFile(path.join(WEB, 'index.html'), 'utf8');
  for (const id of ['aiOrchestratorLog', 'aiOrchestratorSuggest', 'aiOrchestratorForm', 'aiOrchestratorInput']) {
    assert.match(html, new RegExp(`id="${id}"`), `${id} missing from the panel`);
  }
  const rule = (selector) => {
    const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const found = [...css.matchAll(new RegExp(`${escaped}\\{([^}]*)\\}`, 'g'))].map((m) => m[1]).join(';');
    assert.ok(found, `no rule for ${selector}`);
    return found;
  };
  assert.match(rule('#flowView .ai-orchestrator.fd-ai-panel'), /display:flex;flex-direction:column/);
  assert.match(rule('#aiOrchestrator .ai-orchestrator-head'), /display:flex;align-items:center;justify-content:space-between/);
  assert.match(rule('#aiOrchestrator .ai-orchestrator-log'), /flex:1;min-height:0;overflow:auto/);
  assert.match(rule('#aiOrchestrator .ai-orchestrator-form'), /flex:none/);
  assert.match(rule('#aiOrchestrator textarea'), /width:100%/);
  // The send button sits in the composer toolbar instead of floating over the chips.
  assert.match(rule('#flowView #aiOrchestrator .ai-orchestrator-send'), /position:static/);
});

test('canvas build effects are styled', async () => {
  const css = await readFile(path.join(WEB, 'app/flow-canvas.css'), 'utf8');
  for (const selector of ['#flowView .canvas.ai-building::after', '#flowView .canvas.ai-done::before', '#flowView .ai-ghost-node']) {
    assert.ok(css.includes(`${selector}{`), `no rule for ${selector}`);
  }
  assert.match(css, /@media \(prefers-reduced-motion:reduce\)\{[^}]*#flowView \.ai-ghost-node[^}]*\{animation:none\}/);
});
