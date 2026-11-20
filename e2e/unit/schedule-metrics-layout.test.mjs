import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';

// Browser geometry checks live in layout/; these template checks use Node only.
test('execution metrics retain four cards and the reported token total', async () => {
  const template = await readFile(new URL('../../web/app/schedule-executions.js', import.meta.url), 'utf8');
  const context = vm.createContext({});
  vm.runInContext(template, context, { filename: 'schedule-executions.js' });
  const overview = { current: { total: 4, finished: 4, success: 2, failed: 2, metered: 4, tokens: { total: 135000 } } };
  const metrics = vm.runInContext(`scheduleExecMetricsHTML(${JSON.stringify(overview)})`, context);
  assert.equal((metrics.match(/class="sched-exec-metric"/g) || []).length, 4);
  assert.match(metrics, /成功率.*成功 2 次.*失败 2 次/s);
  assert.match(metrics, /Token 总消耗.*135,000.*4 次已上报 · 最近 7 天/s);
});
