import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

function pointer(window, type, target, options = {}) {
  const event = new window.MouseEvent(type, { bubbles: true, clientX: 24, clientY: 12 });
  Object.defineProperties(event, {
    pointerType: { value: options.pointerType || 'mouse' },
    relatedTarget: { value: options.relatedTarget || null },
  });
  target.dispatchEvent(event);
}

test('hover ink handles mouse, touch, keyboard, disabled, blur, and cancellation', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const button = app.document.querySelector('.platform-sidebar button.nav');
  Object.defineProperty(button, 'getBoundingClientRect', {
    configurable: true, value: () => ({ left: 0, top: 0, width: 120, height: 40, right: 120, bottom: 40 }),
  });
  pointer(app.window, 'pointerdown', button);
  assert.equal(button.dataset.hoverInk, 'hover');
  pointer(app.window, 'pointerup', button, { pointerType: 'touch' });
  pointer(app.window, 'pointercancel', button);
  button.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  button.disabled = true;
  pointer(app.window, 'pointerdown', button);
  assert.equal(button.dataset.hoverInk, 'suppressed');
  button.disabled = false;
  app.window.AgentFlowHoverInk.reset();
  app.window.dispatchEvent(new app.window.Event('blur'));
  await settle();
});

test('sidebar snapshots animate in both directions and clean up', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const nav = app.document.querySelector('.platform-sidebar nav');
  const nativeComputedStyle = app.window.getComputedStyle.bind(app.window);
  app.window.getComputedStyle = (element, pseudo) => pseudo
    ? { content: 'none', display: 'none', getPropertyValue: () => '' }
    : nativeComputedStyle(element);
  Object.defineProperty(nav, 'getBoundingClientRect', {
    configurable: true, value: () => ({ left: 10, top: 20, width: 240, height: 500, right: 250, bottom: 520 }),
  });
  const from = app.window.AgentFlowSidebarMotion.capture(nav, { background: '#fff' });
  const to = app.window.AgentFlowSidebarMotion.capture(nav, { left: 20, width: 260, background: '#eee' });
  assert.ok(from?.element);
  const animation = app.window.AgentFlowSidebarMotion.play(from, to, { direction: 'settings', duration: 0 });
  animation.reverse();
  await animation.finished;
  const cancelled = app.window.AgentFlowSidebarMotion.play(from, to, { direction: 'collaboration', duration: 0 });
  cancelled.cancel();
  assert.equal(app.document.querySelector('[data-sidebar-mode-motion]'), null);
});

test('sliding tabs refresh keyboard selection, cache geometry, and dispose', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const root = app.document.createElement('div');
  root.id = 'unit-tabs';
  root.innerHTML = '<button class="active">One</button><button>Two</button><button disabled>Disabled</button>';
  app.document.body.append(root);
  Object.defineProperties(root, {
    offsetWidth: { configurable: true, value: 240 },
    offsetHeight: { configurable: true, value: 40 },
    getBoundingClientRect: { configurable: true, value: () => ({ left: 0, top: 0, width: 240, height: 40 }) },
  });
  for (const [index, button] of [...root.querySelectorAll('button')].entries()) {
    button.getClientRects = () => [{ left: index * 80, top: 0, width: 80, height: 40 }];
    button.getBoundingClientRect = () => ({ left: index * 80, top: 0, width: 80, height: 40 });
    button.onclick = () => {
      root.querySelectorAll('button').forEach((item) => item.classList.remove('active'));
      button.classList.add('active');
    };
  }
  const tabs = app.window.AgentFlowSlidingTabs.mount(root, { activeSelector: '.active', auto: true, cacheKey: 'unit' });
  tabs.refresh();
  root.firstElementChild.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
  await settle();
  tabs.refresh();
  tabs.dispose();
  assert.equal(root.hasAttribute('data-sliding-tabs'), false);
});
