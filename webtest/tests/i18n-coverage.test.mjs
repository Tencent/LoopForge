import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { WEB_ROOT } from '../lib/load-app.mjs';

async function englishPage(html = '<html><body></body></html>') {
  const dom = new JSDOM(html.replace('<html', '<html data-lang="en"'), { runScripts: 'outside-only', url: 'http://127.0.0.1/' });
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(dom.getInternalVMContext());
  }
  return dom.window;
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
