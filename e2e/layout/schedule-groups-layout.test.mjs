import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import { chromium } from '@playwright/test';

test('AI schedule group names and counts fit narrow cards with visible keyboard focus', async (t) => {
  const context = vm.createContext({
    window: { addEventListener() {} },
    escapeHTML: value => String(value ?? '').replace(/[&<>'"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[c])),
    knownTime: () => null,
  });
  for (const name of ['shared-schedules', 'sync-schedule']) {
    vm.runInContext(await readFile(new URL(`../../web/app/${name}.js`, import.meta.url), 'utf8'), context);
  }
  const css = (await Promise.all(['app/harness-ui.css', 'sync.css'].map(name => readFile(new URL(`../../web/${name}`, import.meta.url), 'utf8')))).join('\n');
  const browser = await chromium.launch({ headless: true });
  t.after(() => browser.close());
  const page = await browser.newPage();
  for (const scenario of [
    { width: 390, theme: 'light', forcedColors: 'none' },
    { width: 320, theme: 'dark', forcedColors: 'none' },
    { width: 390, theme: 'light', forcedColors: 'active' },
    { width: 1280, theme: 'light', forcedColors: 'none' },
  ]) {
    await page.setViewportSize({ width: scenario.width, height: 800 });
    await page.emulateMedia({ forcedColors: scenario.forcedColors });
    const jobs = [1, 2].map(id => ({ id: `check-${id}`, projectId: 'project-1', name: 'AtlasProductionInfrastructureHealthCheck', skipDispatch: true, enabled: true, intervalSec: 3600 }));
    const html = vm.runInContext(`scheduleJobGroupHTML(scheduleJobGroups(${JSON.stringify(jobs)})[0], () => '')`, context);
    await page.setContent(`<!doctype html><html data-theme="${scenario.theme}"><style>
      * { box-sizing: border-box; }
      body { margin: 24px; }
      ${css}
    </style><main id="aiSchedulesView"><section class="ai-schedule-panel">${html}</section></main></html>`);
    await page.keyboard.press('Tab');
    const result = await page.locator('.schedule-job-group-head').evaluate(head => {
      const name = head.querySelector('.schedule-job-group-name');
      const badge = head.querySelector('.hui-badge');
      const panelBounds = head.closest('.ai-schedule-panel').getBoundingClientRect();
      const nameBounds = name.getBoundingClientRect(), badgeBounds = badge.getBoundingClientRect();
      const style = getComputedStyle(head);
      return {
        focused: document.activeElement === head && head.matches(':focus-visible'),
        outlineStyle: style.outlineStyle, outlineWidth: parseFloat(style.outlineWidth),
        outlineOffset: parseFloat(style.outlineOffset), outlineColor: style.outlineColor,
        backgroundColor: style.backgroundColor,
        nameFits: name.scrollWidth <= name.clientWidth + 1 && nameBounds.right <= badgeBounds.left,
        badgeFits: badge.scrollWidth <= badge.clientWidth + 1 && badgeBounds.right <= panelBounds.right - 1,
        pageFits: document.documentElement.scrollWidth <= window.innerWidth,
      };
    });
    const label = JSON.stringify(scenario);
    assert.equal(result.nameFits, true, label);
    assert.equal(result.badgeFits, true, label);
    assert.equal(result.pageFits, true, label);
    assert.equal(result.focused, true, label);
    assert.equal(result.outlineStyle, 'solid', label);
    assert.ok(result.outlineWidth >= 2 && result.outlineOffset < 0, label);
    assert.notEqual(result.outlineColor, result.backgroundColor, label);
  }
});
