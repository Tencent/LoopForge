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

// #134: the panel stacks a fixed header over the embedded chat, which scrolls
// and lays out its own composer (chatbot/src/sop-designer.css).
test('AI orchestrator panel stacks header, build status and the embedded chat', async () => {
  const css = await readFile(path.join(WEB, 'app/flow-canvas.css'), 'utf8');
  const html = await readFile(path.join(WEB, 'index.html'), 'utf8');
  for (const id of ['aiOrchestratorReset', 'closeAIOrchestrator', 'aiBuildStatus', 'aiOrchestratorFrame']) {
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
  assert.match(rule('#aiOrchestrator .ai-orchestrator-frame'), /flex:1;min-height:0;width:100%;border:0/);
  // The page tooltip must not cover the panel while the pointer is over the chat.
  assert.match(html, /<iframe id="aiOrchestratorFrame"[^>]*data-hui-tooltip="off"/);
  assert.doesNotMatch(css, /#aiOrchestrator \.ai-orchestrator-(log|form|suggest)/, 'styles of the removed hand-written chat remain');
});

test('canvas build effects are styled', async () => {
  const css = await readFile(path.join(WEB, 'app/flow-canvas.css'), 'utf8');
  for (const selector of ['#flowView .canvas.ai-building::after', '#flowView .canvas.ai-done::before', '#flowView .ai-ghost-node']) {
    assert.ok(css.includes(`${selector}{`), `no rule for ${selector}`);
  }
  // The ghost node replaces the "还没有步骤" card while the assistant builds.
  assert.ok(css.includes('#flowView .canvas.ai-building .fd-empty{display:none}'), 'empty-canvas card stays visible during an AI build');
  const effects = css.split('\n').filter((line) => /^#flowView (\.canvas\.ai-|\.ai-ghost-node)/.test(line)).join('\n');
  assert.doesNotMatch(effects, /rgba\(/, 'build effects must follow --hui-* tokens in both themes');
  assert.match(css, /@media \(prefers-reduced-motion:reduce\)\{[^}]*#flowView \.ai-ghost-node[^}]*\{animation:none\}/);
});
