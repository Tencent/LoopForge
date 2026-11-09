const assert = require('assert');
const path = require('path');
const { createDOM } = require('./hui_fake_dom');

(async () => {
  const dom = createDOM();
  const { document, window } = dom;
  const form = dom.html(`<form id="report"><label>开始日期<input type="date" name="from" value="2026-03-15" min="2026-03-03" max="2026-04-10"></label></form>`);
  dom.load(path.join(__dirname, 'app/hui-select.js'));
  dom.load(path.join(__dirname, 'app/hui-date.js'));
  const HuiDate = window.HuiDate;

  assert.strictEqual(HuiDate.format(new Date(2026, 0, 5)), '2026-01-05');
  assert.strictEqual(HuiDate.parse('2026-02-30'), null, 'impossible dates are rejected');
  assert.strictEqual(HuiDate.parse('2026/02/03'), null);

  const input = form.querySelector('input');
  const trigger = input.nextSibling || input.parentNode.children[input.parentNode.children.indexOf(input) + 1];
  assert.ok(trigger && trigger.classList.contains('hui-date-trigger'), 'inputs present at load are enhanced');
  const api = HuiDate.enhance(input);
  assert.strictEqual(api.trigger, trigger, 'enhance is idempotent');
  assert.ok(input.classList.contains('hui-date-native'));
  assert.strictEqual(input.tabIndex, -1);
  assert.strictEqual(trigger.type, 'button');
  assert.strictEqual(trigger.getAttribute('aria-haspopup'), 'dialog');
  const value = () => trigger.querySelector('.hui-date-value');
  assert.strictEqual(value().textContent, '2026-03-15');
  assert.strictEqual(trigger.getAttribute('aria-label'), '开始日期：2026-03-15', 'trigger is named by the wrapping label');
  assert.strictEqual(api.panel.parentNode, null, 'panel is only in the document while open');

  const events = [];
  input.addEventListener('input', () => events.push('input'));
  input.addEventListener('change', () => events.push('change:' + input.value));

  trigger.click();
  assert.ok(api.isOpen());
  assert.strictEqual(api.panel.parentNode, document.body);
  assert.strictEqual(trigger.getAttribute('aria-expanded'), 'true');
  assert.strictEqual(api.panel.querySelector('.hui-date-title').textContent, '2026 年 3 月');
  const days = () => api.panel.querySelectorAll('.hui-date-day');
  const day = key => api.panel.querySelector(`[data-date="${key}"]`);
  assert.strictEqual(days().length, 42, 'six weeks are always rendered');
  assert.deepStrictEqual(api.panel.querySelectorAll('.hui-date-weekday').map(n => n.textContent), ['一', '二', '三', '四', '五', '六', '日']);
  assert.strictEqual(days()[0].dataset.date, '2026-02-23', 'weeks start on Monday');
  assert.ok(days()[0].classList.contains('is-outside'));
  assert.ok(day('2026-03-15').classList.contains('is-selected'));
  assert.strictEqual(day('2026-03-15').getAttribute('aria-selected'), 'true');
  assert.strictEqual(document.activeElement, day('2026-03-15'), 'opening focuses the selected day');
  assert.ok(day('2026-03-02').disabled, 'days before min are disabled');
  assert.ok(!day('2026-03-03').disabled);
  const [prev, next] = api.panel.querySelectorAll('.hui-date-nav');
  assert.ok(prev.disabled, 'cannot page before the min month');
  assert.ok(!next.disabled);
  const [clear, today] = api.panel.querySelectorAll('.hui-date-action');
  assert.ok(today.disabled, 'today is outside min/max');
  assert.ok(!clear.disabled);

  const key = (k, extra) => api.panel.dispatchEvent(dom.event('keydown', { key: k, bubbles: true, ...extra }));
  key('ArrowRight');
  assert.strictEqual(document.activeElement, day('2026-03-16'));
  key('ArrowUp');
  assert.strictEqual(document.activeElement, day('2026-03-09'));
  key('ArrowUp');
  assert.strictEqual(document.activeElement, day('2026-03-03'), 'keyboard focus is clamped to min');
  key('Home');
  assert.strictEqual(document.activeElement, day('2026-03-03'));
  key('End');
  assert.strictEqual(document.activeElement, day('2026-03-08'), 'End goes to Sunday');
  key('PageDown');
  assert.strictEqual(api.panel.querySelector('.hui-date-title').textContent, '2026 年 4 月', 'PageDown moves a month');
  assert.strictEqual(document.activeElement, day('2026-04-08'));
  key('ArrowDown');
  assert.strictEqual(document.activeElement, day('2026-04-10'), 'clamped to max');
  assert.ok(day('2026-04-11').disabled);
  assert.ok(api.panel.querySelectorAll('.hui-date-nav')[1].disabled, 'cannot page past the max month');
  assert.strictEqual(events.length, 0, 'moving focus does not change the value');

  day('2026-04-10').click();
  assert.strictEqual(input.value, '2026-04-10');
  assert.deepStrictEqual(events, ['input', 'change:2026-04-10']);
  assert.ok(!api.isOpen());
  assert.strictEqual(document.activeElement, trigger, 'choosing returns focus to the trigger');
  assert.strictEqual(value().textContent, '2026-04-10');
  assert.ok(new FormData_(form).from === '2026-04-10', 'the form still submits the native value');

  dom.advance(300);
  assert.strictEqual(api.panel.parentNode, null, 'closed panel leaves <body> after its exit transition');

  input.value = '2026-03-20';
  assert.strictEqual(value().textContent, '2026-03-20', 'programmatic writes resync the trigger');
  assert.strictEqual(events.length, 2, 'programmatic writes do not fire events');

  trigger.click();
  key('Escape');
  assert.ok(!api.isOpen());
  assert.strictEqual(document.activeElement, trigger, 'Escape returns focus');

  trigger.click();
  api.panel.querySelectorAll('.hui-date-action')[0].click();
  assert.strictEqual(input.value, '');
  assert.deepStrictEqual(events.slice(2), ['input', 'change:'], 'clear writes an empty value');
  assert.strictEqual(value().textContent, '选择日期');
  assert.ok(value().classList.contains('is-placeholder'));

  input.required = true;
  input.value = '2026-03-04';
  await dom.settle();
  trigger.click();
  assert.ok(api.panel.querySelectorAll('.hui-date-action')[0].disabled, 'required dates cannot be cleared');
  key('Escape');

  input.setAttribute('disabled', '');
  await dom.settle();
  assert.ok(trigger.disabled, 'disabled attribute syncs to the trigger');
  trigger.click();
  assert.ok(!api.isOpen());
  input.removeAttribute('disabled');
  await dom.settle();
  assert.ok(!trigger.disabled);

  document.activeElement = document.body;
  form.querySelector('label').firstChild.parentNode.dispatchEvent(dom.event('click', { bubbles: true, button: 0 }));
  assert.strictEqual(document.activeElement, trigger, 'clicking the label focuses the trigger');

  const late = dom.html(`<div class="metrics-date-filter"><input type="date" id="metricsDateTo"></div>`);
  const skipped = dom.html(`<div data-native><input type="date" id="keepNative"></div>`);
  await dom.settle();
  const lateInput = late.querySelector('input');
  assert.ok(late.querySelector('.hui-date-trigger'), 'inputs inserted later are enhanced');
  assert.strictEqual(late.querySelector('.hui-date-value').textContent, '选择日期');
  assert.ok(!skipped.querySelector('.hui-date-trigger'), '[data-native] keeps the browser date input');
  assert.strictEqual(HuiDate.enhance(skipped.querySelector('input')), null);

  const lateApi = HuiDate.enhance(lateInput);
  lateApi.trigger.click();
  assert.strictEqual(lateApi.panel.parentNode, document.body);
  trigger.click();
  assert.ok(!lateApi.isOpen() && api.isOpen(), 'only one Harness popover is open at a time');
  key('Escape');
  lateApi.trigger.click();
  late.remove();
  await dom.settle();
  assert.ok(!lateApi.isOpen(), 'removing the input closes its calendar');
  assert.strictEqual(lateApi.panel.parentNode, null, 'and drops the panel');
  assert.ok(!lateInput.classList.contains('hui-date-native'), 'removed inputs are restored (destroyed)');

  console.log('hui date ok');
})().catch(err => { console.error(err); process.exit(1); });

function FormData_(form) {
  const out = {};
  form.querySelectorAll('input[name]').forEach(i => { out[i.name] = i.value; });
  return out;
}
