import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { WEB_ROOT } from '../lib/load-app.mjs';

// The password endpoints answer a wrong password and the rate limit in English
// only; the login page shows them in its own language.
async function loginPage({ lang = 'zh', status, error }) {
  const html = (await readFile(path.join(WEB_ROOT, 'login.html'), 'utf8')).replace(/<script[^>]*><\/script>/g, '');
  const dom = new JSDOM(lang === 'en' ? html.replace('<html', '<html data-lang="en"') : html, { runScripts: 'outside-only', url: 'http://127.0.0.1/login.html' });
  const { window } = dom;
  window.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const reply = (code, body) => ({ ok: code < 400, status: code, json: async () => body });
  window.fetch = async (url) => (String(url).includes('/api/auth/setup') ? reply(200, { signupEnabled: true }) : reply(status, { error }));
  for (const file of ['i18n-en.js', 'i18n.js', 'login.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(dom.getInternalVMContext());
  }
  return window;
}

async function submit(window, { register = false } = {}) {
  const { document } = window;
  await new Promise((resolve) => window.setTimeout(resolve, 0));
  if (register) document.getElementById('switchMode').click();
  const form = document.getElementById('loginForm');
  form.elements.login.value = 'alice';
  form.elements.password.value = 'wrong-password';
  if (register) form.elements.confirm.value = 'wrong-password';
  form.dispatchEvent(new window.Event('submit', { cancelable: true }));
  for (let i = 0; i < 5; i += 1) await new Promise((resolve) => window.setTimeout(resolve, 0));
  return document.getElementById('loginError').textContent;
}

test('a wrong password is reported in the page language', async () => {
  let window = await loginPage({ status: 401, error: 'Invalid username or password' });
  assert.equal(await submit(window), '用户名或密码错误');
  window.close();
  window = await loginPage({ lang: 'en', status: 401, error: 'Invalid username or password' });
  assert.equal(await submit(window), 'Invalid username or password');
  window.close();
});

test('the rate limit is reported in the page language for sign-in and sign-up', async () => {
  let window = await loginPage({ status: 429, error: 'Too many attempts. Try again in a minute.' });
  assert.equal(await submit(window), '尝试次数过多，请一分钟后再试');
  window.close();
  window = await loginPage({ status: 429, error: 'Too many attempts. Try again in a minute.' });
  assert.equal(await submit(window, { register: true }), '尝试次数过多，请一分钟后再试');
  window.close();
});

test('other server messages are still shown as returned', async () => {
  const window = await loginPage({ status: 409, error: '该用户名已被使用' });
  assert.equal(await submit(window, { register: true }), '该用户名已被使用');
  window.close();
});
