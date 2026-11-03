const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { createDOM } = require('./hui_fake_dom');

// Runs the afConfirm / afPrompt / afAlert block of app.js on its own.
const source = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
const start = source.indexOf('let confirmPromiseResolve');
const end = source.indexOf('function highlightCode');
assert.ok(start > 0 && end > start, 'dialog block found in app.js');
const block = source.slice(start, end);
assert.doesNotMatch(block, /window\.(confirm|prompt|alert)\b/, 'no native dialog fallback');

(async () => {
  const dom = createDOM();
  const { document, window } = dom;
  const nativeCalls = [];
  window.confirm = () => { nativeCalls.push('confirm'); return true; };
  window.prompt = () => { nativeCalls.push('prompt'); return 'x'; };
  window.alert = () => { nativeCalls.push('alert'); };
  window.$ = selector => document.querySelector(selector);
  window.console = { error() {} };
  dom.run(block + '\n;globalThis.__dialogs={afConfirm,afPrompt,afAlert,afOpenDialog};', 'app-dialogs.js');
  const { afConfirm, afPrompt, afAlert } = window.__dialogs;
  const $ = window.$;
  const open = () => !!$('#confirmModal')?.classList.contains('open');
  const key = (target, k, init) => target.dispatchEvent(dom.event('keydown', { bubbles: true, key: k, ...init }));

  const opener = dom.html('<button id="deleteEntry">删除</button>');
  opener.focus();
  assert.strictEqual($('#confirmModal'), null);
  let pending = afConfirm({ title: '删除沉淀条目', message: '删除这条沉淀条目？', confirmText: '删除', danger: true });
  const modal = $('#confirmModal');
  assert.ok(modal, 'the dialog is created from its template when the page has none');
  assert.ok(open());
  const card = modal.querySelector('.confirm-modal-card');
  assert.strictEqual(card.getAttribute('role'), 'dialog');
  assert.strictEqual(card.getAttribute('aria-modal'), 'true');
  assert.strictEqual($('#confirmTitle').textContent, '删除沉淀条目');
  assert.strictEqual($('#confirmMessage').textContent, '删除这条沉淀条目？');
  assert.strictEqual($('#confirmOkBtn').textContent, '删除');
  assert.strictEqual($('#confirmOkBtn').className, 'button danger-btn', 'danger confirms use the red solid button');
  assert.strictEqual($('#confirmIconWrap').className, 'confirm-icon-wrap danger');
  assert.strictEqual($('#confirmIcon svg').getAttribute('data-icon'), 'trash', 'Harness icon, not an emoji');
  assert.ok($('#confirmPromptWrap').hidden);
  assert.strictEqual(document.activeElement, $('#confirmOkBtn'));

  key($('#confirmOkBtn'), 'Tab');
  assert.strictEqual(document.activeElement, $('#confirmCancelBtn'), 'Tab wraps inside the dialog');
  key($('#confirmCancelBtn'), 'Tab', { shiftKey: true });
  assert.strictEqual(document.activeElement, $('#confirmOkBtn'), 'Shift+Tab wraps back');

  $('#confirmCancelBtn').click();
  assert.strictEqual(await pending, false, 'cancel resolves false');
  assert.ok(!open());
  assert.strictEqual(document.activeElement, opener, 'focus returns to the opener');

  pending = afConfirm('继续？');
  key($('#confirmOkBtn'), 'Escape');
  assert.strictEqual(await pending, false, 'Escape cancels');
  pending = afConfirm({ message: '写回？', info: true });
  assert.strictEqual($('#confirmIconWrap').className, 'confirm-icon-wrap info');
  assert.strictEqual($('#confirmOkBtn').className, 'button primary');
  modal.dispatchEvent(dom.event('click', { bubbles: true }));
  assert.strictEqual(await pending, false, 'backdrop click cancels');
  pending = afConfirm({ message: '确定？' });
  assert.strictEqual($('#confirmTitle').textContent, '操作确认');
  assert.strictEqual($('#confirmIcon svg').getAttribute('data-icon'), 'warning');
  key(modal.querySelector('.confirm-modal-card'), 'Enter');
  assert.strictEqual(await pending, true, 'Enter confirms');

  const first = afConfirm({ title: '第一个' });
  const second = afConfirm({ title: '第二个' });
  assert.strictEqual($('#confirmTitle').textContent, '第一个', 'a second request waits in the queue');
  $('#confirmOkBtn').click();
  assert.strictEqual(await first, true);
  assert.ok(open(), 'the queued dialog opens next');
  assert.strictEqual($('#confirmTitle').textContent, '第二个');
  $('#confirmCancelBtn').click();
  assert.strictEqual(await second, false);
  assert.ok(!open());

  pending = afPrompt({ title: '打回到上游节点', message: '说明返工原因', label: '返工原因', multiline: true, confirmText: '打回' });
  const area = $('#confirmPromptMultiline');
  assert.ok(!$('#confirmPromptWrap').hidden);
  assert.ok(!area.hidden && $('#confirmPromptInput').hidden, 'multiline prompts use the textarea');
  assert.strictEqual($('#confirmPromptLabel').textContent, '返工原因');
  assert.strictEqual($('#confirmPromptLabel').getAttribute('for'), 'confirmPromptMultiline');
  assert.strictEqual(document.activeElement, area);
  assert.strictEqual($('#confirmIcon svg').getAttribute('data-icon'), 'edit');
  area.value = '缺少验收用例';
  key(area, 'Enter');
  assert.ok(open(), 'plain Enter in the textarea inserts a newline');
  key(area, 'Enter', { ctrlKey: true });
  assert.strictEqual(await pending, '缺少验收用例', 'Ctrl+Enter submits the text');

  pending = afPrompt({ title: '重命名', defaultValue: '旧名称' });
  const input = $('#confirmPromptInput');
  assert.ok(!input.hidden && area.hidden);
  assert.strictEqual(input.value, '旧名称');
  key(input, 'Enter');
  assert.strictEqual(await pending, '旧名称');
  pending = afPrompt('原因');
  $('#confirmCancelBtn').click();
  assert.strictEqual(await pending, null, 'cancelled prompts resolve null');

  pending = afAlert({ type: 'err', title: '保存失败', message: '网络错误' });
  assert.ok($('#confirmCancelBtn').hidden, 'alerts have a single button');
  assert.strictEqual($('#confirmOkBtn').textContent, '我知道了');
  assert.strictEqual($('#confirmIcon svg').getAttribute('data-icon'), 'alert');
  $('#confirmOkBtn').click();
  assert.strictEqual(await pending, undefined);
  pending = afConfirm({ message: '再次确认' });
  assert.ok(!$('#confirmCancelBtn').hidden, 'the cancel button comes back for confirms');
  $('#confirmOkBtn').click();
  await pending;

  // The generic handlers in app.js bind(): Escape drops .open from the topmost
  // modal, and a backdrop pointerdown drops .open from that modal.
  const underlying = dom.html('<div id="syncModal" class="modal open"><button id="syncBtn">同步</button></div>');
  document.body.appendChild(modal);
  document.addEventListener('keydown', e => {
    if (e.key !== 'Escape') return;
    const top = [...document.querySelectorAll('.modal.open')].pop();
    if (top) top.classList.remove('open');
  });
  modal.addEventListener('pointerdown', e => { if (e.target === modal) modal.classList.remove('open'); });

  pending = afConfirm({ message: '放弃修改？' });
  document.activeElement?.blur?.();
  key(document.body, 'Escape');
  assert.strictEqual(await pending, false, 'Escape cancels even when focus is outside the dialog');
  assert.ok(underlying.classList.contains('open'), 'that Escape does not also close the modal underneath');
  pending = afConfirm({ message: '再删一次？' });
  assert.ok(open(), 'the next dialog still opens after an outside Escape');
  $('#confirmOkBtn').click();
  assert.strictEqual(await pending, true);

  pending = afConfirm({ message: '点遮罩' });
  modal.dispatchEvent(dom.event('pointerdown', { bubbles: true }));
  assert.ok(!open());
  await dom.settle();
  assert.strictEqual(await pending, false, 'closing the dialog from outside settles the pending call');
  pending = afConfirm({ message: '遮罩之后' });
  assert.ok(open(), 'the queue is not stuck after a backdrop close');
  $('#confirmOkBtn').click();
  assert.strictEqual(await pending, true);

  pending = afConfirm({ message: '被外部关闭' });
  modal.classList.remove('open');
  const next = afConfirm({ message: '紧接着' });
  assert.strictEqual(await pending, false, 'a dialog closed behind our back is settled before the next opens');
  assert.ok(open());
  assert.strictEqual($('#confirmMessage').textContent, '紧接着');
  $('#confirmOkBtn').click();
  assert.strictEqual(await next, true);

  assert.deepStrictEqual(nativeCalls, [], 'browser dialogs are never used');
  console.log('hui dialog ok');
})().catch(err => { console.error(err); process.exit(1); });
