import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import { chromium } from '@playwright/test';

// Render the production template and CSS without requiring a deployment,
// authentication fixtures, or dependencies from the separate webtest package.
test('execution metrics fit the content area at 1040px and smaller widths', async (t) => {
  const template = await readFile(new URL('../../web/app/schedule-executions.js', import.meta.url), 'utf8');
  const context = vm.createContext({});
  vm.runInContext(template, context, { filename: 'schedule-executions.js' });
  const css = await readFile(new URL('../../web/sync.css', import.meta.url), 'utf8');
  const browser = await chromium.launch({ headless: true });
  t.after(() => browser.close());
  const page = await browser.newPage();
  const overview = { current: { total: 4, finished: 4, success: 2, failed: 2, avgMs: 12500, metered: 4, tokens: { total: 140000 } } };
  for (const scenario of [
    { viewport: 1040, content: 720, tokens: 140000, columns: 2 },
    { viewport: 1440, content: 720, tokens: 9970934, columns: 2 },
    { viewport: 1440, content: 1180, tokens: 9970934, columns: 4 },
    { viewport: 375, content: 327, tokens: 9970934, columns: 1 },
    { viewport: 375, content: 240, tokens: 9999999999999, columns: 1 },
  ]) {
    await page.setViewportSize({ width: scenario.viewport, height: 1000 });
    overview.current.tokens.total = scenario.tokens;
    const metrics = vm.runInContext(`scheduleExecMetricsHTML(${JSON.stringify(overview)})`, context);
    await page.setContent(`<!doctype html><style>
      * { box-sizing: border-box; }
      :root { --hui-sans: Arial,sans-serif; --hui-surface: white; --hui-text: #252529; --hui-muted: #777; --hui-secondary: #666; --hui-border: #ddd; --hui-border-soft: #eee; --hui-accent: #7b61ff; --hui-success: #26804a; --hui-error: #b33; --hui-info: #467ac3; }
      body { margin: 0; }
      #aiSchedulesView { width: ${scenario.content}px; margin: 24px; }
      ${css}
    </style><main id="aiSchedulesView"><section class="sched-exec-card"><div class="sched-exec-overview-main"><h3 class="sched-exec-card-title">执行概览 <span class="muted">最近 7 天</span></h3><div class="sched-exec-metrics">${metrics}</div></div></section></main>`);
    const geometry = await page.evaluate(() => {
      const grid = document.querySelector('.sched-exec-metrics');
      const cards = [...grid.querySelectorAll('.sched-exec-metric')];
      const result = { columns: getComputedStyle(grid).gridTemplateColumns.split(' ').length, overflow: [] };
      for (const card of cards) {
        const bounds = card.getBoundingClientRect();
        for (const child of card.querySelectorAll('.sched-exec-metric-body, .sched-exec-metric-body > *')) {
          const rect = child.getBoundingClientRect();
          if (child.scrollWidth > child.clientWidth + 1 || rect.right > bounds.right + 1 || rect.left < bounds.left - 1) result.overflow.push(child.textContent);
        }
      }
      return result;
    });
    assert.equal(geometry.columns, scenario.columns, JSON.stringify(scenario));
    assert.deepEqual(geometry.overflow, [], JSON.stringify(scenario));
    assert.match(await page.locator('.sched-exec-metric').last().innerText(), /最近 7 天/);
  }
});
