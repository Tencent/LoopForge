import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('page feedback widget covers helpers, image attachments, element picking, submission, and drag-to-hide', async (t) => {
  let feedbackStatus = 201;
  let feedbackBody = { id: 'fb-1' };
  let throwNetworkError = false;
  const app = await loadApp({
    hash: '#/runs/run-1?token=secret&view=log',
    routes: [
      {
        method: 'POST',
        path: '/api/page-feedback',
        body: () => {
          if (throwNetworkError) throw new Error('network down');
          return { status: feedbackStatus, body: feedbackBody };
        },
      },
    ],
  });
  t.after(app.close);
  const { window, document } = app;
  const api = window.AgentFlowPageFeedback;
  const T = api._test;

  assert.equal(T.deriveTitle('\n  首行标题 \n次行'), '首行标题');
  assert.equal(T.deriveTitle('   '), '');
  assert.equal(
    T.sanitizeURL('https://u:p@flow.test/p?q=1&access_token=x&api-key=y#/runs?code=1&v=2'),
    'https://flow.test/p?q=1#/runs?v=2',
  );
  assert.equal(T.sanitizeURL('javascript:alert(1)'), '');
  assert.equal(T.sanitizeRoute('#/runs?sessionId=1&tab=log'), '#/runs?tab=log');
  assert.equal(T.imageProblem({ name: 'a.svg', type: 'image/svg+xml', size: 10 }, 0), '仅支持 PNG / JPEG / GIF / WebP 图片');
  assert.equal(T.imageProblem({ name: 'big.png', type: 'image/png', size: 6 * 1024 * 1024 }, 0), 'big.png 超过 5MB');
  assert.equal(T.imageProblem({ name: 'a.png', type: 'image/png', size: 10 }, 5), '最多 5 张图片');
  assert.equal(T.imageProblem({ name: 'a.webp', type: 'image/webp', size: 10 }, 2), '');

  const hostSection = document.createElement('div');
  hostSection.id = 'fbTestFixture';
  hostSection.innerHTML = `
    <ul><li>one</li><li data-row="2">two</li><li>three</li></ul>
    <p>first para</p><p>second para</p>
    <button id="fbDangerBtn">删除</button>
    <button data-testid="fb-icon-btn" aria-label="图标按钮"><svg><g><path d="M0 0h1"></path></g></svg></button>
    <span class="fb-bare-ico"><svg><path d="M0 0h1"></path></svg></span>
  `;
  document.body.appendChild(hostSection);
  assert.equal(T.cssSelector(hostSection.querySelector('#fbDangerBtn'), document), '#fbDangerBtn');
  assert.ok(T.cssSelector(hostSection.querySelector('[data-row="2"]'), document).includes('[data-row="2"]'));
  assert.ok(T.cssSelector(hostSection.querySelectorAll('li')[2], document).includes(':nth-of-type(3)'));

  // Guest hides widget, non-guest mounts it
  app.evaluate(`state.currentUser = { isGuest: true, loginName: 'guest' }`);
  api.sync();
  assert.equal(document.querySelector('.fb-fab'), null);
  app.evaluate(`state.currentUser = { id: 'u-1', loginName: 'alice', isGuest: false }`);
  api.sync();
  api.sync();
  const fab = document.querySelector('.fb-fab');
  assert.ok(fab);

  // Open, context toggle, Escape close
  fab.click();
  assert.ok(document.querySelector('.fb-panel').classList.contains('open'));
  document.querySelector('.fb-ctx-row').click();
  assert.ok(document.querySelector('.fb-foot').classList.contains('open'));
  window.dispatchEvent(new window.Event('hashchange'));

  const area = document.querySelector('.fb-text');
  area.value = '   ';
  area.dispatchEvent(new window.Event('input', { bubbles: true }));
  assert.equal(document.querySelector('.fb-submit').disabled, true);

  area.value = '按钮错位\n详情描述';
  area.dispatchEvent(new window.Event('input', { bubbles: true }));
  assert.equal(document.querySelector('.fb-submit').disabled, false);

  area.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal(document.querySelector('.fb-panel').classList.contains('open'), false);
  fab.click();
  document.querySelector('.fb-close').click();
  assert.equal(document.querySelector('.fb-panel').classList.contains('open'), false);
  fab.click();

  // Image attachments via file input, paste, drag-drop, and removal
  const makeImg = (name, type = 'image/png', size = 512) => new window.File(['x'], name, { type });
  const bigImg = { name: 'huge.png', type: 'image/png', size: 6 * 1024 * 1024 };
  const fileInput = document.querySelector('.fb-file');
  document.querySelector('.fb-add-image').click();
  Object.defineProperty(fileInput, 'files', {
    configurable: true,
    value: [makeImg('bad.svg', 'image/svg+xml'), makeImg('one.png')],
  });
  fileInput.dispatchEvent(new window.Event('change', { bubbles: true }));
  assert.equal(T.state().images, 1);

  const pasteEvt = new window.Event('paste', { bubbles: true, cancelable: true });
  pasteEvt.clipboardData = { items: [{ kind: 'file', getAsFile: () => makeImg('two.png') }] };
  document.querySelector('.fb-panel').dispatchEvent(pasteEvt);
  assert.equal(T.state().images, 2);

  const composer = document.querySelector('.fb-composer');
  const dragEnter = new window.Event('dragenter', { bubbles: true, cancelable: true });
  dragEnter.dataTransfer = { types: ['Files'] };
  composer.dispatchEvent(dragEnter);
  const dragOver = new window.Event('dragover', { bubbles: true, cancelable: true });
  dragOver.dataTransfer = { types: ['Files'] };
  composer.dispatchEvent(dragOver);
  composer.dispatchEvent(new window.Event('dragleave', { bubbles: true }));
  const dropEvt = new window.Event('drop', { bubbles: true, cancelable: true });
  dropEvt.dataTransfer = {
    files: [bigImg, makeImg('three.gif', 'image/gif'), makeImg('four.webp', 'image/webp'), makeImg('five.jpg', 'image/jpeg'), makeImg('six.png')],
  };
  composer.dispatchEvent(dropEvt);
  assert.equal(T.state().images, 5);

  document.querySelector('.fb-atts .fb-thumb button').click();
  await new Promise((r) => setTimeout(r, 220));
  assert.equal(T.state().images, 4);

  // Element picking (normal element, icon button, bare svg, cancel, clear)
  document.querySelector('.fb-pick').click();
  assert.equal(T.state().picking, true);
  const secondPara = hostSection.querySelectorAll('p')[1];
  secondPara.dispatchEvent(new window.MouseEvent('mousemove', { bubbles: true }));
  secondPara.dispatchEvent(new window.MouseEvent('click', { bubbles: true, clientX: 50, clientY: 60 }));
  assert.equal(T.state().picking, false);
  assert.equal(T.state().element.tag, 'p');

  document.querySelector('.fb-repick').click();
  document.body.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal(T.state().picking, false);

  document.querySelector('.fb-repick').click();
  document.querySelector('.fb-pick-cancel').click();
  assert.equal(T.state().picking, false);

  const iconPath = hostSection.querySelector('[data-testid="fb-icon-btn"] path');
  document.querySelector('.fb-repick').click();
  iconPath.dispatchEvent(new window.MouseEvent('mousemove', { bubbles: true }));
  iconPath.dispatchEvent(new window.MouseEvent('click', { bubbles: true }));
  assert.equal(T.state().element.tag, 'button');

  const barePath = hostSection.querySelector('.fb-bare-ico path');
  document.querySelector('.fb-repick').click();
  barePath.dispatchEvent(new window.MouseEvent('click', { bubbles: true }));
  assert.equal(T.state().element.tag, 'span');

  document.querySelector('.fb-clear-el').click();
  assert.equal(T.state().element, null);

  // Submit: 401 -> 400 -> network error -> 201 success
  feedbackStatus = 401;
  area.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true }));
  await settle(6);
  assert.equal(document.querySelector('.fb-err').textContent, '登录已失效，请重新登录后提交');

  feedbackStatus = 400;
  feedbackBody = { error: '格式有误' };
  document.querySelector('.fb-submit').click();
  await settle(6);
  assert.equal(document.querySelector('.fb-err').textContent, '格式有误');

  throwNetworkError = true;
  document.querySelector('.fb-submit').click();
  await settle(6);
  assert.equal(document.querySelector('.fb-err').textContent, '网络异常，提交失败，请稍后重试');
  throwNetworkError = false;

  feedbackStatus = 201;
  feedbackBody = { id: 'fb-ok' };
  document.querySelector('.fb-submit').click();
  await settle(6);
  assert.ok(document.querySelector('.fb-done').classList.contains('show'));

  // Drag to trash and undo
  const currentFab = document.querySelector('.fb-fab');
  const firePtr = (type, clientX, clientY) => {
    const evt = new window.MouseEvent(type, { bubbles: true, button: 0, clientX, clientY });
    currentFab.dispatchEvent(evt);
  };
  firePtr('pointerdown', 800, 500);
  firePtr('pointermove', 820, 500);
  assert.ok(currentFab.classList.contains('dragging'));
  document.body.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  firePtr('pointerup', 820, 500);
  currentFab.click();

  firePtr('pointerdown', 800, 500);
  firePtr('pointermove', 820, 500);
  firePtr('pointercancel', 820, 500);

  const binX = (window.innerWidth || 1024) / 2;
  const binY = (window.innerHeight || 768) - 58;
  currentFab.getBoundingClientRect = () => ({ left: binX - 22, top: binY - 22, width: 44, height: 44 });
  firePtr('pointerdown', binX, binY);
  firePtr('pointermove', binX + 10, binY);
  firePtr('pointerup', binX + 10, binY);
  await new Promise((r) => setTimeout(r, 400));
  assert.equal(T.state().trashed, true);
  document.querySelector('.fb-undo').click();
  assert.equal(T.state().trashed, false);
});
