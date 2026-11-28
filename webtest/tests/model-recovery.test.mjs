import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

test('model credential gate offers retry without approving the node', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const html = app.evaluate(`(() => {
    const nr={status:'waiting_approval',waitReason:'model_credentials',activity:'模型凭据无效，请更新凭据后重新执行'};
    const node={id:'agent',name:'Agent',type:'agent'};
    return approvalCalloutHTML(node,nr,[node]);
  })()`);
  assert.match(html, /处理完成，重新执行/);
  assert.match(html, /模型凭据无效/);
  assert.doesNotMatch(html, /data-approve=/);
  assert.equal(app.evaluate(`statusText('waiting_retry')`), '等待重试');
  const recovery = app.evaluate(`modelRecoveryHTML({status:'waiting_retry',activeModel:'backup',modelErrorClass:'rate_limited',nextRetryAt:'2030-01-01T00:00:00Z'})`);
  assert.match(recovery, /backup/);
  assert.match(recovery, /rate_limited/);
  assert.match(recovery, /下次重试/);
});
