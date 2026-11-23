import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { WEB_ROOT } from '../lib/load-app.mjs';

const HAN = /[㐀-鿿]/;
const CHATBOT_HTML = path.join(WEB_ROOT, '..', 'chatbot', 'index.html');

async function englishWindow() {
  const dom = new JSDOM('<html data-lang="en"><body></body></html>', { runScripts: 'outside-only', url: 'http://127.0.0.1/chatbot/' });
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(dom.getInternalVMContext());
  }
  return dom.window;
}

test('the chatbot page loads the shared language layer before its React bundle', async () => {
  const { document } = new JSDOM(await readFile(CHATBOT_HTML, 'utf8')).window;
  const sources = [...document.querySelectorAll('script[src]')].map((script) => script.getAttribute('src'));
  const order = ['/boot.js', '/i18n-en.js', '/i18n.js'].map((src) => sources.indexOf(src));
  assert.ok(order.every((index) => index >= 0), `missing language scripts in ${sources.join(', ')}`);
  assert.deepEqual([...order].sort((a, b) => a - b), order);
  const bundle = sources.findIndex((src) => /main\.jsx$|\/assets\//.test(src));
  assert.ok(bundle > order.at(-1), 'language scripts must run before the chatbot bundle');
});

test('English mode translates the chatbot home page copy', async () => {
  const window = await englishWindow();
  for (const text of ['把想法，变成下一步。', '聊聊你的想法，或交给我一件事', '还没有会话', '新对话', '去配置', '快速接入',
    '接入测试环境', '选用 SOP 与角色', 'CLI 默认模型', '请求批准', '使用 Claude Code CLI 与个人 Claude Code API Key。']) {
    assert.doesNotMatch(window.lfTranslate(text), HAN, text);
  }
  window.close();
});
