import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { WEB_ROOT } from '../lib/load-app.mjs';

const HAN = /[\u3400-\u9fff]/;

async function englishPage(html) {
  const dom = new JSDOM(html.replace('<html', '<html data-lang="en"'), { runScripts: 'outside-only', url: 'http://127.0.0.1/' });
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(dom.getInternalVMContext());
  }
  return dom.window;
}

const settle = window => new Promise(resolve => window.setTimeout(resolve, 50));

test('English mode translates the issue search and runner onboarding copy in index.html', async () => {
  const window = await englishPage(await readFile(path.join(WEB_ROOT, 'index.html'), 'utf8'));
  await settle(window);
  const { document } = window;
  assert.equal(document.querySelector('#issueSearch').getAttribute('placeholder'), 'Fuzzy search titles, IDs, or descriptions');
  const step = [...document.querySelectorAll('.runner-step-no')].find(el => el.textContent.trim() === '3').parentElement;
  assert.equal(step.querySelector('small').textContent, 'Account login for Claude Code, Codex and Antigravity');
});

test('English mode translates guest, role empty-state and bind-code copy rendered from JS', async () => {
  const sources = {
    'app/workbench.js': ['访客会话 · 未登录'],
    'app/runners.js': ['<登录后自动生成>'],
    'app/agents.js': ['没有匹配的角色', '尝试更换关键词，或清除筛选条件。', '调整搜索关键词，或新建一个角色。', '点击右上角「新建角色」即可创建第一个角色。'],
  };
  const strings = [];
  for (const [file, list] of Object.entries(sources)) {
    const code = await readFile(path.join(WEB_ROOT, file), 'utf8');
    for (const s of list) {
      assert.ok(code.includes(s), `${file} still renders ${s}`);
      strings.push(s);
    }
  }
  const window = await englishPage('<!doctype html><html><body><main id="root"></main></body></html>');
  const { document } = window;
  const root = document.querySelector('#root');
  for (const s of strings) {
    const p = document.createElement('p');
    p.textContent = s;
    root.append(p);
  }
  await settle(window);
  const out = [...root.children].map(p => p.textContent);
  assert.deepEqual(out.filter(text => HAN.test(text)), []);
  assert.deepEqual(out, [
    'Guest session · Not signed in',
    '<Generated after sign-in>',
    'No matching Roles',
    'Try different keywords, or clear the filters.',
    'Adjust your search or create a Role.',
    'Click "New role" in the top right to create your first Role.',
  ]);
});
