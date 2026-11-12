import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { JSDOM } from 'jsdom';
import { WEB_ROOT } from '../lib/load-app.mjs';

// SlidingTabs enhances every type-tab row with .sliding-tabs[data-sliding-tabs],
// and the enhanced rule forces display:flex. Apply the shipped stylesheet to
// enhanced markup and check computed styles, so a later rule can never make the
// hidden 能力资产/环境资产 group visible again.
test('hidden asset type-tab groups stay hidden after SlidingTabs enhancement', async () => {
  const css = await readFile(path.join(WEB_ROOT, 'app/assets-harness.css'), 'utf8');
  const tabs = (group, hidden) => `<div class="asset-type-tabs sliding-tabs" data-sliding-tabs data-asset-type-group="${group}"${hidden ? ' hidden' : ''}><button class="active">A</button><button>B</button></div>`;
  const dom = new JSDOM(`<!doctype html><style>${css}</style><main><section id="assetsView">${tabs('capability', true)}${tabs('environment', false)}</section><section id="mcpView">${tabs('capability', false)}</section></main>`);
  const { document, getComputedStyle } = dom.window;
  const display = (selector) => getComputedStyle(document.querySelector(selector)).display;
  assert.equal(display('#assetsView [data-asset-type-group="capability"]'), 'none', 'the inactive group is hidden');
  assert.equal(display('#assetsView [data-asset-type-group="environment"]'), 'flex', 'the active group is shown');
  assert.equal(display('#mcpView [data-asset-type-group="capability"]'), 'flex');

  document.querySelector('#assetsView [data-asset-type-group="environment"]').hidden = true;
  document.querySelector('#assetsView [data-asset-type-group="capability"]').hidden = false;
  assert.equal(display('#assetsView [data-asset-type-group="environment"]'), 'none', 'switching groups hides the previous one');
  assert.equal(display('#assetsView [data-asset-type-group="capability"]'), 'flex');
  dom.window.close();
});
