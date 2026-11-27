import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../web');

test('#1075: the SOP trigger hint is covered by the 16px panel padding override', async () => {
  const css = await readFile(path.join(WEB, 'sync.css'), 'utf8');
  const html = await readFile(path.join(WEB, 'index.html'), 'utf8');

  // The hint element exists in the SOP trigger panel and uses the shared class.
  assert.match(html, /id="aiScheduleTriggerHint" class="schedule-field-hint"/);

  // #1075: the shared .schedule-field-hint rule keeps margin:6px 0 0 (no
  // horizontal padding), so every hint rendered inside an ai-schedule-panel
  // must be covered by the #aiSchedulesView override that supplies the same
  // 16px padding as the panel head.
  const shared = css.match(/\.schedule-field-hint\{[^}]*\}/)?.[0] ?? '';
  assert.ok(shared, 'no .schedule-field-hint rule');
  assert.match(shared, /margin:6px 0 0/);
  assert.doesNotMatch(shared, /padding/);

  const override = css.match(/#aiSchedulesView :is\(([^)]*)\)\s*\{([^}]*)\}/);
  assert.ok(override, 'no #aiSchedulesView :is(...) override for panel hints');
  const selectors = override[1].split(',').map((s) => s.trim());
  const body = override[2];
  assert.ok(
    selectors.includes('#aiScheduleTriggerHint') && selectors.includes('#aiScheduleRuntimeHint'),
    `the override must cover both panel hints, got: ${selectors.join(', ')}`,
  );
  assert.match(body, /padding:12px 16px/);
  assert.match(body, /margin:0[^;]*;[^}]*color:var\(--hui-secondary\)/);

  // The padding matches the panel head, so the hint text aligns with the
  // "SOP 自动派发" title at the same 16px left edge.
  const head = css.match(/#aiSchedulesView \.ai-schedule-panel-head\s*\{([^}]*)\}/)?.[1] ?? '';
  assert.match(head, /padding:14px 16px/);
  const overridePad = body.match(/padding:(\d+)px (\d+)px/);
  const headPad = head.match(/padding:(\d+)px (\d+)px/);
  assert.equal(overridePad[2], headPad[2]);
});
