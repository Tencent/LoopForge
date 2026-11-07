const assert = require('assert');
const path = require('path');
const { createDOM } = require('./hui_fake_dom');

(async () => {
  const dom = createDOM();
  const { document, window } = dom;
  dom.load(path.join(__dirname, 'app/hui-tooltip.js'));
  dom.load(path.join(__dirname, 'app/hui-tooltip.js'));
  const { HuiTooltip } = window;
  assert.ok(HuiTooltip, 'installs once, a second load is a no-op');

  const root = dom.html(`<div class="toolbar">
    <button id="copy" title="复制链接" aria-describedby="hint"><svg class="harness-icon"></svg></button>
    <span id="hint">说明</span>
    <button id="blank" title="  ">空</button>
    <div data-native><button id="raw" title="浏览器提示">原生</button></div>
    <button id="off" data-hui-tooltip="off" title="关闭">关</button>
    <div title="外层提示"><span id="inner">内层</span></div>
  </div>`);
  const copy = root.querySelector('#copy');
  const icon = copy.querySelector('svg');
  const over = (target, init = {}) => target.dispatchEvent(dom.event('pointerover', { bubbles: true, pointerType: 'mouse', ...init }));
  const out = (target, relatedTarget = null) => target.dispatchEvent(dom.event('pointerout', { bubbles: true, relatedTarget }));
  const tip = () => document.getElementById('huiTooltip');

  over(icon);
  assert.strictEqual(tip(), null, 'nothing is shown before the delay');
  dom.advance(399);
  assert.strictEqual(tip(), null);
  dom.advance(1);
  assert.ok(tip(), 'shows after 400ms hover');
  assert.strictEqual(tip().getAttribute('role'), 'tooltip');
  assert.ok(!tip().hidden);
  assert.ok(tip().classList.contains('is-visible'));
  assert.strictEqual(tip().textContent, '复制链接');
  assert.strictEqual(HuiTooltip.current(), copy);
  assert.strictEqual(copy.getAttribute('title'), null, 'the native title is parked so the browser bubble does not appear');
  assert.strictEqual(copy.getAttribute('data-hui-title'), '复制链接');
  assert.strictEqual(copy.getAttribute('aria-describedby'), 'hint huiTooltip', 'existing descriptions are kept');
  assert.strictEqual(tip().dataset.placement, 'top');

  out(icon, copy);
  assert.strictEqual(HuiTooltip.current(), copy, 'moving within the element keeps the tooltip');
  copy.setAttribute('title', '已复制');
  await dom.settle();
  assert.strictEqual(tip().textContent, '已复制', 'title rewrites while shown update the bubble');
  assert.strictEqual(copy.getAttribute('title'), null);

  out(copy, root);
  assert.strictEqual(HuiTooltip.current(), null);
  assert.ok(tip().hidden);
  assert.strictEqual(copy.getAttribute('title'), '已复制', 'title is restored on leave');
  assert.strictEqual(copy.getAttribute('data-hui-title'), null);
  assert.strictEqual(copy.getAttribute('aria-describedby'), 'hint');

  over(copy);
  out(copy, root);
  dom.advance(500);
  assert.ok(tip().hidden, 'leaving before the delay cancels it');

  for (const id of ['blank', 'raw', 'off']) {
    over(root.querySelector('#' + id));
    dom.advance(500);
    assert.ok(tip().hidden, `#${id} gets no Harness tooltip`);
  }
  assert.strictEqual(root.querySelector('#raw').getAttribute('title'), '浏览器提示', '[data-native] keeps its native title');

  over(copy, { pointerType: 'touch' });
  dom.advance(500);
  assert.ok(tip().hidden, 'touch pointers are ignored');

  over(root.querySelector('#inner'));
  dom.advance(400);
  assert.strictEqual(tip().textContent, '外层提示', 'the nearest titled ancestor is used');
  document.dispatchEvent(dom.event('keydown', { key: 'Escape', bubbles: true }));
  assert.ok(tip().hidden, 'Escape hides');
  assert.strictEqual(root.querySelector('#inner').parentNode.getAttribute('title'), '外层提示');

  copy.focus();
  dom.advance(0);
  assert.strictEqual(HuiTooltip.current(), copy, 'keyboard focus shows immediately');
  root.querySelector('#blank').focus();
  assert.strictEqual(HuiTooltip.current(), null, 'blur hides');
  assert.strictEqual(copy.getAttribute('title'), '已复制');

  over(copy);
  dom.advance(400);
  document.dispatchEvent(dom.event('pointerdown', { bubbles: true }));
  assert.ok(tip().hidden, 'pressing anywhere hides');

  over(copy);
  dom.advance(400);
  window.dispatchEvent(dom.event('scroll'));
  assert.ok(tip().hidden, 'scrolling hides');

  over(copy);
  dom.advance(400);
  copy.remove();
  await dom.settle();
  assert.strictEqual(HuiTooltip.current(), null, 'removing the element hides its tooltip');
  assert.ok(tip().hidden);
  assert.strictEqual(copy.getAttribute('title'), '已复制', 'and leaves the title intact for re-insertion');

  console.log('hui tooltip ok');
})().catch(err => { console.error(err); process.exit(1); });
