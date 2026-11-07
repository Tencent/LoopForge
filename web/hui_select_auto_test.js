const assert = require('assert');
const path = require('path');
const { createDOM } = require('./hui_fake_dom');

(async () => {
  const dom = createDOM();
  const { document, window } = dom;
  dom.load(path.join(__dirname, 'app/hui-select.js'));
  const { HuiSelect } = window;
  const press = (target, init) => {
    const event = dom.event('mousedown', { bubbles: true, button: 0, ...init });
    target.dispatchEvent(event);
    return event;
  };
  const key = (target, k, init) => {
    const event = dom.event('keydown', { bubbles: true, key: k, ...init });
    target.dispatchEvent(event);
    return event;
  };
  const openPanel = () => document.body.querySelectorAll('.hui-select-panel.is-open')[0] || null;

  // ---- in place: the select stays the trigger ----
  const view = dom.html(`<section>
    <label>流程<select id="flow">
      <option value="">全部流程</option>
      <optgroup label="研发"><option value="dev">开发</option><option value="fix" disabled>修复</option></optgroup>
      <option value="old" hidden>旧流程</option>
    </select></label>
    <div data-native><select id="raw"><option>原生</option></select></div>
    <select id="tags" multiple><option>a</option></select>
    <select id="off" disabled><option>停用</option></select>
  </section>`);
  const flow = view.querySelector('#flow');
  let changes = [];
  flow.addEventListener('change', () => changes.push(flow.value));

  assert.ok(press(flow).defaultPrevented, 'mousedown swaps the browser popup for the Harness panel');
  let panel = openPanel();
  assert.ok(panel, 'panel opens');
  assert.strictEqual(panel.parentNode, document.body);
  assert.strictEqual(flow.getAttribute('aria-expanded'), 'true');
  assert.strictEqual(flow.getAttribute('aria-controls'), panel.id);
  assert.strictEqual(panel.getAttribute('aria-label'), '流程', 'panel is named by the label text, not the option text');
  const group = panel.querySelector('.hui-select-group');
  assert.strictEqual(group.textContent, '研发', 'optgroups render as headers');
  assert.strictEqual(group.getAttribute('role'), 'presentation');
  const options = panel.querySelectorAll('.hui-select-option');
  assert.deepStrictEqual(options.map(o => o.querySelector('.hui-select-option-label').textContent), ['全部流程', '开发', '修复'], 'hidden options are skipped');
  assert.ok(options[2].disabled, 'disabled options stay unselectable');
  assert.strictEqual(document.activeElement, options[0]);

  options[2].click();
  assert.deepStrictEqual(changes, [], 'disabled option is ignored');
  options[1].click();
  assert.deepStrictEqual(changes, ['dev'], 'choosing fires change with the new value');
  assert.strictEqual(flow.getAttribute('aria-expanded'), 'false');
  assert.strictEqual(document.activeElement, flow, 'focus returns to the select');
  assert.strictEqual(panel.parentNode, document.body, 'panel stays for the exit transition');
  dom.advance(240);
  assert.strictEqual(panel.parentNode, null, 'then leaves <body>, so nothing leaks');

  assert.ok(key(flow, 'ArrowDown').defaultPrevented, 'ArrowDown opens from the keyboard');
  panel = openPanel();
  assert.ok(panel);
  assert.ok(panel.querySelectorAll('.hui-select-option')[1].classList.contains('is-selected'));
  flow.innerHTML = '<option value="a">甲</option><option value="b">乙</option>';
  await dom.settle();
  assert.deepStrictEqual(openPanel().querySelectorAll('.hui-select-option-label').map(n => n.textContent), ['甲', '乙'], 'option rewrites while open re-render the panel');
  key(openPanel(), 'Escape');
  assert.strictEqual(openPanel(), null);
  assert.strictEqual(document.activeElement, flow);
  key(flow, 'Enter');
  assert.ok(openPanel(), 'Enter opens');
  key(openPanel(), 'Tab');
  assert.strictEqual(openPanel(), null, 'Tab closes and lets focus move on');
  key(flow, ' ');
  flow.setAttribute('disabled', '');
  await dom.settle();
  assert.strictEqual(openPanel(), null, 'disabling the select closes its panel');
  flow.removeAttribute('disabled');

  const many = dom.html(`<select id="many">${[1, 2, 3, 4, 5, 6, 7].map(n => `<option value="${n}">选项${n}</option>`).join('')}</select>`);
  many.value = '2';
  press(many);
  assert.ok(!openPanel().querySelector('.hui-select-search').hidden, 'more than five options show the search box');
  assert.strictEqual(document.activeElement, openPanel().querySelector('.hui-select-search input'), 'a click focuses the search box');
  many.innerHTML = '<option value="1">甲</option><option value="2">乙</option>';
  await dom.settle();
  assert.ok(openPanel().querySelector('.hui-select-search').hidden);
  assert.strictEqual(document.activeElement, openPanel().querySelectorAll('.hui-select-option')[1], 'focus moves off the hidden search box onto the selected option');
  key(document.activeElement, 'Escape');
  assert.strictEqual(openPanel(), null, 'so Escape still closes the panel');
  many.innerHTML = [1, 2, 3, 4, 5, 6, 7].map(n => `<option value="${n}">选项${n}</option>`).join('');
  many.value = '3';
  const touchPoint = { clientX: 10, clientY: 10 };
  many.dispatchEvent(dom.event('touchstart', { bubbles: true, touches: [touchPoint] }));
  many.dispatchEvent(dom.event('touchend', { bubbles: true, changedTouches: [touchPoint] }));
  assert.ok(openPanel(), 'a tap opens the panel');
  assert.strictEqual(document.activeElement, openPanel().querySelectorAll('.hui-select-option')[2], 'a tap focuses the selected option, not the search box');
  key(document.activeElement, 'Escape');
  many.remove();

  for (const id of ['raw', 'tags', 'off']) {
    const select = view.querySelector('#' + id);
    assert.ok(!press(select).defaultPrevented, `#${id} keeps the browser behaviour`);
    assert.strictEqual(openPanel(), null);
  }

  press(flow);
  assert.ok(openPanel());
  document.dispatchEvent(dom.event('pointerdown', { bubbles: true, target: document.body }));
  view.dispatchEvent(dom.event('pointerdown', { bubbles: true }));
  assert.strictEqual(openPanel(), null, 'outside pointerdown closes');

  press(flow);
  const leaving = openPanel();
  flow.parentNode.remove();
  await dom.settle();
  assert.strictEqual(openPanel(), null, 'removing an open select closes its panel');
  dom.advance(240);
  assert.strictEqual(leaving.parentNode, null);

  // ---- trigger mode: explicit enhanceSelect ----
  const bar = dom.html(`<div class="issue-toolbar">
    <label>负责人<select id="owner" style="width:180px"><option value="">全部责任人</option><option value="u1" data-meta="4 条">胡</option></select></label>
  </div>`);
  const owner = bar.querySelector('#owner');
  owner.style.width = '180px';
  const before = HuiSelect.instanceCount();
  const api = HuiSelect.enhance(owner);
  assert.strictEqual(HuiSelect.enhance(owner), api, 'idempotent');
  assert.strictEqual(HuiSelect.instanceCount(), before + 1);
  assert.strictEqual(api.trigger.getAttribute('aria-label'), '负责人');
  assert.strictEqual(api.trigger.style.width, '180px', 'inline width is copied to the trigger');
  assert.ok(!press(owner).defaultPrevented && !openPanel(), 'trigger-mode selects are not handled in place');

  owner.disabled = true;
  assert.ok(api.trigger.disabled, 'disabled writes sync to the trigger');
  api.trigger.click();
  assert.ok(!api.isOpen());
  owner.disabled = false;
  assert.ok(!api.trigger.disabled);

  owner.setAttribute('hidden', '');
  await dom.settle();
  assert.ok(api.trigger.hidden, 'hiding the select hides the trigger');
  owner.removeAttribute('hidden');
  await dom.settle();
  assert.ok(!api.trigger.hidden);

  document.activeElement = document.body;
  bar.querySelector('label').dispatchEvent(dom.event('click', { bubbles: true, button: 0 }));
  assert.strictEqual(document.activeElement, api.trigger, 'clicking the label focuses the trigger');
  owner.focus();
  assert.strictEqual(document.activeElement, api.trigger, 'select.focus() is forwarded');

  api.trigger.click();
  assert.ok(api.isOpen());
  bar.remove();
  await dom.settle();
  assert.ok(!api.isOpen());
  assert.strictEqual(api.panel.parentNode, null, 'removed selects drop their panel');
  assert.strictEqual(HuiSelect.instanceCount(), before, 'and their instance');
  assert.ok(!owner.classList.contains('hui-select-native'), 'the select is restored');
  owner.disabled = true;
  assert.ok(!api.trigger.disabled, 'setters are unpatched after destroy');

  console.log('hui select auto ok');
})().catch(err => { console.error(err); process.exit(1); });
