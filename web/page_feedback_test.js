const assert = require('assert');
const path = require('path');
const { createDOM } = require('./hui_fake_dom');

(async () => {
  const dom = createDOM();
  const { document, window } = dom;

  const storageCalls = [];
  const storage = name => ({
    getItem: () => { storageCalls.push(name + '.getItem'); return null; },
    setItem: () => storageCalls.push(name + '.setItem'),
    removeItem: () => storageCalls.push(name + '.removeItem'),
  });
  Object.defineProperty(document, 'cookie', {
    get: () => { storageCalls.push('cookie.get'); return ''; },
    set: () => storageCalls.push('cookie.set'),
  });
  const revoked = [];
  const requests = [];
  let respond = () => ({ status: 201, ok: true, json: async () => ({ id: 'fb-1' }) });
  class FakeFormData {
    constructor() { this.entries = []; }
    append(name, value, filename) { this.entries.push({ name, value, filename }); }
  }
  Object.assign(window, {
    location: { href: 'https://flow.test/?code=abc&tab=2#/runs/r1?token=t&view=log', hash: '#/runs/r1?token=t&view=log' },
    navigator: { userAgent: 'FakeUA/1.0', language: 'zh-CN' },
    devicePixelRatio: 2, scrollX: 0, scrollY: 0,
    localStorage: storage('localStorage'), sessionStorage: storage('sessionStorage'),
    URL: { createObjectURL: file => 'blob:' + file.name, revokeObjectURL: url => revoked.push(url) },
    FormData: FakeFormData,
    fetch: async (url, init) => { requests.push({ url, init }); return respond(); },
    state: { currentUser: null },
  });
  dom.html('<header><b id="pageTitle">运行记录</b></header>');
  const app = dom.html(`<main><section class="page-view active" id="runsView"><div id="biz"><ul><li>a</li><li data-row="2">b</li><li>c</li></ul><section><p>first</p><p>second paragraph</p></section><button id="danger">删除</button><button data-view="issues" aria-label="需求列表"><svg viewBox="0 0 16 16"><g><path d="M1 1h4"></path><path d="M1 5h4"></path></g></svg></button><span class="ico"><svg><path d="M0 0h1"></path></svg></span></div></section></main>`);

  dom.load(path.join(__dirname, 'app/page-feedback.js'));
  const api = window.AgentFlowPageFeedback;
  const T = api._test;
  const $ = sel => document.querySelector(sel);
  const press = (type, init = {}) => {
    const event = dom.event(type, { bubbles: true, button: 0, pointerId: 1, ...init });
    $('.fb-fab').dispatchEvent(event);
    return event;
  };
  const key = (target, k, init = {}) => {
    const event = dom.event('keydown', { bubbles: true, key: k, ...init });
    target.dispatchEvent(event);
    return event;
  };
  const type = text => {
    const area = $('.fb-text');
    area.value = text;
    area.dispatchEvent(dom.event('input', { bubbles: true }));
  };
  const flush = async () => { for (let i = 0; i < 4; i++) await dom.settle(); };

  // --- pure helpers ---
  assert.strictEqual(T.deriveTitle('\n\n   首行标题  \n第二行'), '首行标题', 'leading blank lines are skipped and the line is trimmed');
  assert.strictEqual(T.deriveTitle('字'.repeat(130)), '字'.repeat(120), 'the title is clipped to 120 characters');
  assert.strictEqual(T.deriveTitle('   '), '');
  assert.strictEqual(T.sanitizeURL('https://u:p@a.test/x?q=1&access_token=z&api-key=k&keyword=ok#/i?code=1&v=2'), 'https://a.test/x?q=1&keyword=ok#/i?v=2');
  assert.strictEqual(T.sanitizeURL('javascript:alert(1)'), '');
  assert.strictEqual(T.sanitizeRoute('#/runs?sessionId=1'), '#/runs');
  assert.strictEqual(T.imageProblem({ name: 'a.svg', type: 'image/svg+xml', size: 10 }, 0), '仅支持 PNG / JPEG / GIF / WebP 图片');
  assert.strictEqual(T.imageProblem({ name: 'big.png', type: 'image/png', size: 5 * 1024 * 1024 + 1 }, 0), 'big.png 超过 5MB');
  assert.strictEqual(T.imageProblem({ name: 'a.png', type: 'image/png', size: 1 }, 5), '最多 5 张图片');
  assert.strictEqual(T.imageProblem({ name: 'a.webp', type: 'image/webp', size: 1 }, 4), '');

  const li = app.querySelector('[data-row="2"]');
  const second = app.querySelectorAll('p')[1];
  for (const el of [li, second, app.querySelector('#danger'), app.querySelectorAll('li')[2]]) {
    const selector = T.cssSelector(el, document);
    const hits = document.querySelectorAll(selector);
    assert.ok(hits.length === 1 && hits[0] === el, `${selector} must match exactly its element`);
  }
  assert.strictEqual(T.cssSelector(app.querySelector('#danger'), document), '#danger', 'ids win');
  assert.strictEqual(T.cssSelector(li, document), 'li[data-row="2"]', 'data-* attributes come before positions');

  // --- visibility follows the signed-in user ---
  assert.ok(!$('.fb-fab'), 'no entry without a user');
  window.state.currentUser = { isGuest: true, loginName: 'guest' };
  api.sync();
  assert.ok(!$('.fb-fab'), 'guests never see the entry');
  window.state.currentUser = { id: 7, loginName: 'alice', chineseName: '爱丽丝', isGuest: false };
  api.sync();
  api.sync();
  assert.strictEqual(document.querySelectorAll('.fb-fab').length, 1, 'signing in mounts one entry');
  const fab = $('.fb-fab');
  assert.strictEqual(fab.getAttribute('aria-label'), '帮助我们做得更好');

  // --- card copy and structure ---
  assert.strictEqual($('.fb-title').textContent, '帮助我们做得更好');
  assert.strictEqual($('.fb-subtitle').textContent, 'AI Native团队将在24小时以内快速响应');
  assert.strictEqual($('.fb-text').getAttribute('placeholder'), '您的不知所措也是我们的问题');
  assert.strictEqual($('.fb-submit-label').textContent, '提交');
  const composer = $('.fb-composer');
  assert.strictEqual(composer.querySelectorAll('textarea').length, 1, 'one text box only');
  assert.strictEqual(composer.querySelectorAll('input').length, 0, 'no title input inside the composer');
  assert.strictEqual(composer.querySelectorAll('hr').length, 0, 'no divider');
  assert.strictEqual($('.fb-root').querySelectorAll('select').length, 0, 'no dropdowns');
  assert.ok(!$('.fb-root').textContent.includes('项目'), 'no project information');
  assert.ok($('.fb-submit').disabled, 'empty input disables submit');

  // --- open, context, Esc ---
  fab.click();
  assert.ok($('.fb-panel').classList.contains('open'));
  assert.strictEqual(fab.getAttribute('aria-expanded'), 'true');
  const sum = $('.fb-ctx-sum').textContent;
  assert.ok(sum.startsWith('已附上页面位置 · #/runs/r1?view=log · 运行记录 · 1280×800'), sum);
  assert.ok(!sum.includes('token'));
  $('.fb-ctx-row').click();
  assert.ok($('.fb-foot').classList.contains('open'), 'the footer expands');
  assert.ok($('.fb-ctx-list').textContent.includes('FakeUA/1.0'));
  assert.ok(!$('.fb-ctx-list').textContent.includes('code=abc'), 'sensitive query parameters are not shown or kept');

  type('   \n  ');
  assert.ok($('.fb-submit').disabled, 'whitespace only keeps submit disabled');
  type('\n  按钮错位  \n运行详情里「重试」按钮和状态重叠');
  assert.ok(!$('.fb-submit').disabled);
  key($('.fb-text'), 'Escape');
  assert.ok(!$('.fb-panel').classList.contains('open'), 'Esc closes the card');
  assert.strictEqual(document.activeElement, fab, 'focus returns to the button');
  fab.click();
  assert.ok($('.fb-text').value.includes('按钮错位'), 'the draft survives closing');

  // --- images ---
  const img = (name, t = 'image/png', size = 1024) => ({ name, type: t, size });
  const fileInput = $('.fb-file');
  assert.ok(!composer.contains(fileInput));
  fileInput.files = [img('logo.svg', 'image/svg+xml'), img('a.png')];
  fileInput.dispatchEvent(dom.event('change', { bubbles: true }));
  assert.strictEqual(T.state().images, 1, 'svg is refused, png accepted');
  assert.ok($('.fb-err').classList.contains('show'));
  assert.strictEqual($('.fb-err').textContent, '仅支持 PNG / JPEG / GIF / WebP 图片');
  dom.advance(2600);
  assert.ok(!$('.fb-err').classList.contains('show'), 'the error collapses after 2.6s');

  const paste = dom.event('paste', { bubbles: true, clipboardData: { items: [{ kind: 'file', getAsFile: () => img('shot.png') }] } });
  $('.fb-text').dispatchEvent(paste);
  assert.ok(paste.defaultPrevented);
  const textPaste = dom.event('paste', { bubbles: true, clipboardData: { items: [{ kind: 'string', getAsFile: () => null }] } });
  $('.fb-text').dispatchEvent(textPaste);
  assert.ok(!textPaste.defaultPrevented, 'text pastes into the box');
  composer.dispatchEvent(dom.event('drop', { bubbles: true, dataTransfer: { files: [img('big.png', 'image/png', 6 * 1024 * 1024), img('c.gif', 'image/gif'), img('d.webp', 'image/webp')] } }));
  assert.strictEqual(T.state().images, 4);
  assert.strictEqual($('.fb-err').textContent, 'big.png 超过 5MB');
  composer.dispatchEvent(dom.event('drop', { bubbles: true, dataTransfer: { files: [img('e.jpg', 'image/jpeg'), img('f.png')] } }));
  assert.strictEqual(T.state().images, 5);
  assert.ok($('.fb-add-image').hidden, 'the image button hides at 5');
  assert.strictEqual($('.fb-count').textContent, '5/5');
  assert.strictEqual($('.fb-err').textContent, '最多 5 张图片');
  $('.fb-atts').querySelectorAll('.fb-thumb')[4].querySelector('button').click();
  dom.advance(200);
  assert.strictEqual(T.state().images, 4);
  assert.deepStrictEqual(revoked, ['blob:e.jpg'], 'removing a thumbnail revokes its object URL');
  assert.ok(!$('.fb-add-image').hidden);

  // --- element picking ---
  let businessClicks = 0;
  second.addEventListener('click', () => businessClicks++);
  $('.fb-pick').click();
  assert.ok(T.state().picking);
  assert.ok(!$('.fb-panel').classList.contains('open'), 'the card steps aside while picking');
  assert.ok($('.fb-pickbar').classList.contains('show'));
  assert.ok(fab.classList.contains('is-hidden'));
  press('pointerdown');
  dom.advance(400);
  assert.ok(!fab.classList.contains('dragging'), 'the button cannot be dragged while picking');
  press('pointerup');
  second.dispatchEvent(dom.event('mousemove', { bubbles: true }));
  assert.ok($('.fb-hl').classList.contains('show'));
  assert.ok($('.fb-hl span').textContent.startsWith('p '));
  second.dispatchEvent(dom.event('click', { bubbles: true, clientX: 140, clientY: 120 }));
  assert.strictEqual(businessClicks, 0, 'page clicks do not reach business handlers while picking');
  assert.ok(!T.state().picking);
  assert.ok($('.fb-panel').classList.contains('open'), 'the card comes back');
  const picked = T.state().element;
  assert.strictEqual(document.querySelectorAll(picked.selector)[0], second);
  assert.strictEqual(picked.tag, 'p');
  assert.strictEqual(picked.text, 'second paragraph');
  assert.strictEqual(JSON.stringify(picked.point), '{"x":140,"y":120}');
  assert.strictEqual(picked.inFrame, false);
  assert.ok($('.fb-el').classList.contains('has'));
  assert.strictEqual($('.fb-el-text').textContent, '“second paragraph”');

  $('.fb-repick').click();
  key(document.body, 'Escape');
  assert.ok(!T.state().picking, 'Esc leaves picking');
  assert.strictEqual(T.state().element, picked, 'Esc keeps the earlier annotation');

  const iconButton = app.querySelector('[data-view="issues"]');
  const iconPath = iconButton.querySelectorAll('path')[1];
  let tooltipHovers = 0;
  const countHover = () => tooltipHovers++;
  document.addEventListener('pointerover', countHover);
  $('.fb-repick').click();
  iconPath.dispatchEvent(dom.event('pointerover', { bubbles: true }));
  assert.strictEqual(tooltipHovers, 0, 'business hover effects (tooltips) stay quiet while picking');
  iconPath.dispatchEvent(dom.event('mousemove', { bubbles: true }));
  assert.ok($('.fb-hl span').textContent.startsWith('button '), 'hovering an icon highlights its button, not the inner path');
  iconPath.dispatchEvent(dom.event('click', { bubbles: true, clientX: 20, clientY: 20 }));
  const iconPick = T.state().element;
  assert.strictEqual(iconPick.tag, 'button', 'clicking an icon picks the interactive ancestor');
  assert.strictEqual(document.querySelectorAll(iconPick.selector)[0], iconButton);
  assert.strictEqual(iconPick.selector, 'button[data-view="issues"]');
  assert.strictEqual(iconPick.text, '需求列表', 'icon-only controls fall back to aria-label');
  document.removeEventListener('pointerover', countHover);

  const bareIcon = app.querySelector('.ico path');
  $('.fb-repick').click();
  bareIcon.dispatchEvent(dom.event('click', { bubbles: true }));
  assert.strictEqual(T.state().element.tag, 'span', 'a non-interactive icon resolves to the svg parent');

  const frameDom = createDOM();
  const frameButton = frameDom.html('<div id="wb"><button data-testid="send">发送</button></div>').querySelector('button');
  frameButton.rect = { left: 10, top: 20, width: 60, height: 30 };
  const frame = document.createElement('iframe');
  frame.id = 'chatNextFrame';
  frame.contentDocument = frameDom.document;
  frame.contentWindow = { location: { href: 'https://flow.test/chatbot/?projectId=p1&token=secret' } };
  frame.rect = { left: 300, top: 50, width: 800, height: 600 };
  app.querySelector('#runsView').appendChild(frame);
  $('.fb-pick').click();
  frameButton.dispatchEvent(frameDom.event('mousemove', { bubbles: true }));
  assert.strictEqual($('.fb-hl').style.left, 307 + 'px', 'the highlight adds the iframe offset');
  frameButton.dispatchEvent(frameDom.event('click', { bubbles: true, clientX: 15, clientY: 25 }));
  const inFrame = T.state().element;
  assert.strictEqual(inFrame.inFrame, true);
  assert.strictEqual(inFrame.selector, 'button[data-testid="send"]');
  assert.strictEqual(JSON.stringify(inFrame.point), '{"x":315,"y":75}');
  assert.strictEqual(inFrame.rect.x, 310);
  assert.ok($('.fb-el-text').textContent.includes('工作台内'));
  $('.fb-clear-el').click();
  assert.strictEqual(T.state().element, null);
  assert.ok(!$('.fb-el').classList.contains('has'));

  // --- submit: 401 keeps the draft, FormData has no submitter ---
  respond = () => ({ status: 401, ok: false, json: async () => ({ error: '请先登录' }) });
  key($('.fb-text'), 'Enter', { ctrlKey: true });
  assert.ok($('.fb-submit').classList.contains('busy'));
  assert.strictEqual($('.fb-submit-label').textContent, '提交中');
  assert.ok($('.fb-submit').disabled, 'no double submit');
  await flush();
  assert.strictEqual(requests.length, 1);
  assert.strictEqual(requests[0].url, '/api/page-feedback');
  assert.strictEqual(requests[0].init.method, 'POST');
  assert.strictEqual($('.fb-err').textContent, '登录已失效，请重新登录后提交');
  assert.ok($('.fb-text').value.includes('按钮错位'), 'the draft is kept on 401');
  assert.strictEqual(T.state().images, 4, 'images are kept on 401');
  const entries = requests[0].init.body.entries;
  const payload = JSON.parse(entries.find(e => e.name === 'payload').value);
  assert.strictEqual(payload.title, '按钮错位');
  assert.strictEqual(payload.description, '按钮错位  \n运行详情里「重试」按钮和状态重叠');
  for (const forbidden of ['userId', 'loginName', 'chineseName', 'submitter', 'user']) {
    assert.ok(!(forbidden in payload), `payload must not carry ${forbidden}`);
  }
  assert.ok(!JSON.stringify(payload).includes('alice'), 'no identity anywhere in the payload');
  assert.strictEqual(payload.url, 'https://flow.test/?tab=2#/runs/r1?view=log');
  assert.strictEqual(payload.route, '#/runs/r1?view=log');
  assert.strictEqual(payload.view, '运行记录');
  assert.strictEqual(payload.frameUrl, 'https://flow.test/chatbot/?projectId=p1', 'the workbench location is captured without secrets');
  assert.deepStrictEqual(Object.keys(payload.context).sort(), ['capturedAt', 'dpr', 'language', 'scroll', 'theme', 'userAgent', 'viewport']);
  assert.strictEqual(payload.context.dpr, 2);
  assert.strictEqual(entries.filter(e => e.name === 'images').length, 4);

  respond = () => ({ status: 400, ok: false, json: async () => ({ error: 'shot.png 不是支持的图片格式' }) });
  $('.fb-submit').click();
  await flush();
  assert.strictEqual($('.fb-err').textContent, 'shot.png 不是支持的图片格式', 'server errors are shown');

  respond = () => ({ status: 201, ok: true, json: async () => ({ id: 'fb-1' }) });
  $('.fb-submit').click();
  await flush();
  assert.ok($('.fb-done').classList.contains('show'), 'success state');
  const sentCount = requests.length;
  assert.ok($('.fb-submit').disabled, 'submit stays disabled during the success state');
  key($('.fb-text'), 'Enter', { ctrlKey: true });
  $('.fb-submit').click();
  await flush();
  assert.strictEqual(requests.length, sentCount, 'Ctrl+Enter during the success state does not submit again');
  dom.advance(1600);
  assert.ok(!$('.fb-panel').classList.contains('open'), 'the card closes after 1.6s');
  dom.advance(300);
  assert.strictEqual($('.fb-text').value, '', 'the draft clears after success');
  assert.strictEqual(T.state().images, 0);
  assert.ok(!$('.fb-done').classList.contains('show'));

  // --- drag into the trash ---
  fab.rect = { left: 1212, top: 732, width: 44, height: 44 };
  const start = { clientX: 1234, clientY: 754 };
  const hot = { clientX: 640, clientY: 742 };

  press('pointerdown', start);
  dom.advance(100);
  press('pointerup', start);
  fab.click();
  assert.ok($('.fb-panel').classList.contains('open'), 'a short press still toggles the card');
  type('隐藏前的草稿');

  press('pointerdown', start);
  dom.advance(320);
  assert.ok(fab.classList.contains('dragging'), 'holding 320ms starts dragging');
  assert.ok($('.fb-trash').classList.contains('show') && $('.fb-scrim').classList.contains('show'));
  assert.ok(!$('.fb-panel').classList.contains('open'), 'dragging folds the card');
  press('pointerup', start);
  assert.ok(fab.classList.contains('returning') && !$('.fb-trash').classList.contains('show'));
  fab.click();
  assert.ok(!$('.fb-panel').classList.contains('open'), 'the click after a drag is swallowed');
  dom.advance(600);

  press('pointerdown', start);
  press('pointermove', { clientX: start.clientX - 7, clientY: start.clientY });
  assert.ok(fab.classList.contains('dragging'), 'moving more than 6px starts dragging');
  press('pointermove', hot);
  assert.ok($('.fb-trash').classList.contains('hot'), 'near the bin it turns hot');
  assert.ok(fab.style.transform.includes('scale(0.72)'));
  key(document.body, 'Escape');
  assert.ok(!fab.classList.contains('dragging') && fab.classList.contains('returning'), 'Esc springs the button back');
  press('pointermove', hot);
  assert.ok(!$('.fb-trash').classList.contains('show'), 'moves after Esc are ignored');
  press('pointerup', hot);
  fab.click();
  assert.ok(!$('.fb-panel').classList.contains('open'), 'releasing after Esc does not open the card');
  assert.ok(!T.state().trashed);
  dom.advance(600);

  press('pointerdown', start);
  press('pointermove', hot);
  press('pointercancel', hot);
  assert.ok(!T.state().trashed && fab.classList.contains('returning'), 'pointercancel springs back');
  dom.advance(600);

  press('pointerdown', start);
  press('pointermove', { clientX: 900, clientY: 500 });
  assert.ok(!$('.fb-trash').classList.contains('hot'));
  press('pointermove', hot);
  press('pointerup', hot);
  assert.ok(fab.classList.contains('trashed'), 'releasing on the bin swallows the button');
  assert.ok($('.fb-trash').classList.contains('eat'));
  dom.advance(360);
  assert.ok(fab.classList.contains('gone'));
  assert.ok($('.fb-toast').classList.contains('show'));
  assert.strictEqual($('.fb-toast span').textContent, '反馈入口已隐藏，刷新页面后恢复');
  window.dispatchEvent(dom.event('hashchange'));
  api.sync();
  assert.ok(fab.classList.contains('gone'), 'stays hidden across routes');
  fab.click();
  press('pointerdown', start);
  dom.advance(400);
  assert.ok(!fab.classList.contains('dragging'), 'a hidden button cannot be dragged');
  press('pointerup', start);
  $('.fb-undo').click();
  assert.ok(!fab.classList.contains('gone') && fab.classList.contains('back'), 'undo brings it back');
  assert.ok(!$('.fb-toast').classList.contains('show'));
  dom.advance(600);
  fab.click();
  assert.strictEqual($('.fb-text').value, '隐藏前的草稿', 'the draft survives hiding');

  press('pointerdown', start);
  press('pointermove', hot);
  press('pointerup', hot);
  dom.advance(360 + 4000);
  assert.ok(!$('.fb-toast').classList.contains('show'), 'the toast leaves after 4s');
  assert.ok(T.state().trashed);

  // --- signing out removes everything ---
  window.state.currentUser = { isGuest: true, loginName: 'guest' };
  api.sync();
  assert.ok(!$('.fb-root'), 'signing out removes the widget');
  assert.deepStrictEqual(storageCalls, [], 'nothing is kept in browser storage');

  console.log('page feedback ok');
})().catch(err => { console.error(err); process.exit(1); });
