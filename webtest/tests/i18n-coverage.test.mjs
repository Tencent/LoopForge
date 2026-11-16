import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { loadApp, WEB_ROOT } from '../lib/load-app.mjs';

const HAN = /[\u3400-\u9fff]/;
const ATTRS = ['placeholder', 'title', 'aria-label', 'data-tip', 'data-tooltip'];
// The zones web/i18n.js leaves alone. Attributes are still rewritten inside a textarea or a code block.
const SKIP_ZONES = '[data-i18n-skip],.monaco-editor,.xterm,.log-line,.run-log,.markdown-body,[contenteditable="true"]';
const SKIP_TEXT = `${SKIP_ZONES},script,style,noscript,textarea,code,pre`;

async function englishPage(html = '<html><body></body></html>') {
  const dom = new JSDOM(html.replace('<html', '<html data-lang="en"'), { runScripts: 'outside-only', url: 'http://127.0.0.1/' });
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(dom.getInternalVMContext());
  }
  return dom.window;
}

function leftoverChinese(roots) {
  const left = [];
  for (const root of roots) {
    for (const el of [root, ...root.querySelectorAll('*')]) {
      if (!el.closest(SKIP_TEXT)) for (const node of el.childNodes) if (node.nodeType === 3 && HAN.test(node.nodeValue)) left.push(node.nodeValue.trim());
      if (el.closest(SKIP_ZONES)) continue;
      for (const name of ATTRS) {
        const value = el.getAttribute(name);
        if (value && HAN.test(value)) left.push(`${name}="${value}"`);
      }
    }
  }
  return left;
}

// Markup the app rendered in Chinese, put into an English page so i18n.js rewrites it as it would live.
async function leftoverIn(...htmls) {
  const window = await englishPage();
  const hosts = htmls.map((html) => {
    const host = window.document.createElement('div');
    host.innerHTML = html;
    window.document.body.append(host);
    return host;
  });
  await new Promise((resolve) => window.setTimeout(resolve, 0));
  const left = leftoverChinese(hosts);
  window.close();
  return left;
}

// Interpolated messages cannot be checked as markup: each [Chinese, English] pair is a real message with its values filled in.
async function assertTranslated(pairs) {
  const window = await englishPage();
  for (const [zh, en] of pairs) assert.equal(window.lfTranslate(zh), en, zh);
  window.close();
}

test('English mode formats step, total and count patterns without touching digit counts that mean characters', async () => {
  const window = await englishPage();
  for (const [zh, en] of Object.entries({
    '第 2 步，共 4 步': 'Step 2 of 4',
    '共 12 条': 'Total 12',
    '共 3 次': 'Total 3 times',
    '共 5 个需求': '5 requirements',
    '1 位': '1 person',
    '3 位': '3 people',
    '1 行': '1 line',
    '12 字': '12 characters',
    '在 mybox 上执行': 'Runs on mybox',
  })) assert.equal(window.lfTranslate(zh), en, zh);
  assert.doesNotMatch(window.lfTranslate('Key 最多 6 位'), /people/);
  window.close();
});

test('English mode leaves no Chinese in the page markup, its tooltips or the title', async () => {
  const window = await englishPage(await readFile(path.join(WEB_ROOT, 'index.html'), 'utf8'));
  assert.deepEqual(leftoverChinese([window.document.body]), []);
  assert.doesNotMatch(window.document.title, HAN);
  window.close();
});

test('English mode translates the work overview, the feedback widget and the shared controls', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const widget = app.document.querySelector('.fb-root');
  assert.ok(widget, 'the feedback widget is mounted');
  assert.deepEqual(await leftoverIn(app.document.querySelector('#dashboardView').innerHTML, widget.innerHTML), []);
  await assertTranslated([
    ['最多 5 张图片', 'Up to 5 images'],
    ['图片 logo.png 超过 5MB', 'Image logo.png exceeds 5MB'],
    ['提交失败（502），请稍后重试', 'Submit failed (502). Try again later.'],
    ['单个文件', 'single file'],
    ['不支持的文件类型：a.exe', 'Unsupported file type: a.exe'],
  ]);
});
