import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { chromium } from '@playwright/test';

test('long node activity truncates without adding a row to the header', async (t) => {
  const styles = await Promise.all(['styles.css', 'observability.css'].map(file =>
    readFile(new URL(`../../web/${file}`, import.meta.url), 'utf8')));
  const browser = await chromium.launch({ headless: true });
  t.after(() => browser.close());
  const page = await browser.newPage();
  await page.setContent(`<!doctype html><style>${styles.join('\n')}</style>
    <section class="node-inspector">
      <div class="section-heading">
        <div class="node-inspector-title">
          <h3>Run tests</h3>
          <span class="node-activity-inline is-running">Current activity:<b>OK</b></span>
          <span class="node-inspector-model">codex · model</span>
        </div>
        <span class="status running node-inspector-status is-running">Running</span>
      </div>
    </section>`);

  const badge = page.locator('.node-activity-inline');
  const text = badge.locator('b');
  const model = page.locator('.node-inspector-model');
  const longActivity = 'Running the verification command with arguments '.repeat(40).trim();
  for (const width of [960, 360, 300, 280]) {
    await page.setViewportSize({ width, height: 600 });
    await text.evaluate(el => { el.textContent = 'OK'; });
    assert.equal(await text.evaluate(el => el.scrollWidth <= el.clientWidth), true);
    const badgeBefore = await badge.boundingBox();
    const modelBefore = await model.boundingBox();
    await text.evaluate((el, activity) => { el.textContent = activity; }, longActivity);

    const activity = await badge.boundingBox();
    const status = await page.locator('.node-inspector-status').boundingBox();
    assert.equal(activity.y, badgeBefore.y, `activity stays on its original row at ${width}px`);
    assert.ok(activity.x + activity.width <= status.x, `activity fits before the status at ${width}px`);
    assert.equal((await model.boundingBox()).y, modelBefore.y, `model stays on its original row at ${width}px`);
    assert.equal(await text.evaluate(el =>
      el.clientWidth > 0 && el.scrollWidth > el.clientWidth &&
      getComputedStyle(el).textOverflow === 'ellipsis'), true);
    assert.equal(await text.textContent(), longActivity);
  }
});
