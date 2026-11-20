import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const catalog = {
  default: 'default',
  models: [
    { id: 'cb-eval', engine: 'codebuddy', name: 'CodeBuddy evaluator' },
    { id: 'pi:eval-model', engine: 'pi', name: 'Pi evaluator' },
  ],
  engines: [{ id: 'codebuddy', configured: false }, { id: 'pi', configured: true }],
};

function renderEvaluation(app, nodes = {}) {
  app.evaluate(`(() => {
    state.currentUser={id:'user-1',codeBuddyApiKeyConfigured:false};
    state.data.users=[state.currentUser];
    state.activeRun={id:'run-eval-runtime',status:'succeeded',workflowId:'flow-1',nodes:${JSON.stringify(nodes)}};
    state.selectedRun=state.activeRun.id;state.runEval=null;state.runEvalBusy=false;
    const host=document.querySelector('#runDetail');
    host.innerHTML='<div data-run-pane="eval">'+runEvalPaneHTML(state.activeRun)+'</div>';
    bindRunEvalPane(state.activeRun,host);
  })()`);
}

function pickPi(app) {
  const picker = app.document.querySelector('#runEvalModelPicker');
  picker.querySelector('[data-model-toggle]').click();
  const option = picker.querySelector('[data-id="pi:eval-model"]');
  assert.ok(option, 'the evaluation picker must offer the configured Pi model');
  option.click();
}

test('evaluation selects and retains a non-CodeBuddy model and posts that runtime', async (t) => {
  const app = await loadApp({ routes: [{ path: '/api/assistant/models', body: catalog }] });
  t.after(app.close);
  await app.window.AgentFlowAssistantModels.load();
  app.router.post('/api/runs/run-eval-runtime/eval', { id: 'eval-pi', status: 'running', model: 'pi:eval-model' });
  renderEvaluation(app);
  pickPi(app);
  assert.equal(app.window.localStorage.getItem('agentflow.runEvalModel'), 'pi:eval-model');

  app.evaluate(`state.runEvalModel=''`);
  renderEvaluation(app);
  assert.equal(app.document.querySelector('[name="runEvalModel"]').value, 'pi:eval-model');
  assert.match(app.document.querySelector('[data-model-label]').textContent, /Pi evaluator/);
  app.document.querySelector('[data-start-run-eval]').click();
  await settle(5);
  const posts = app.router.calls.filter(c => c.method === 'POST' && c.path === '/api/runs/run-eval-runtime/eval');
  assert.equal(posts.length, 1, 'a user without a CodeBuddy key can evaluate on Pi');
  assert.equal(posts[0].body.model, 'pi:eval-model');
});

test('a single recorded runtime is the default while mixed runtimes require a choice', async (t) => {
  const app = await loadApp({ routes: [{ path: '/api/assistant/models', body: catalog }] });
  t.after(app.close);
  await app.window.AgentFlowAssistantModels.load();
  app.router.post('/api/runs/run-eval-runtime/eval', { id: 'eval-pi', status: 'running', model: 'pi:eval-model' });
  app.evaluate(`state.runEvalModel='';localStorage.removeItem('agentflow.runEvalModel')`);
  renderEvaluation(app, { task: { provider: 'pi', model: 'eval-model' } });
  assert.equal(app.document.querySelector('[name="runEvalModel"]').value, 'pi:eval-model');
  app.document.querySelector('[data-start-run-eval]').click();
  await settle(4);
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/eval')).length, 1);

  app.evaluate(`state.runEvalStream?.close();state.runEvalModel='';localStorage.removeItem('agentflow.runEvalModel')`);
  renderEvaluation(app, { first: { provider: 'pi', model: 'eval-model' }, second: { provider: 'codex', model: 'gpt-test' } });
  app.document.querySelector('[data-start-run-eval]').click();
  await settle(3);
  assert.match(app.document.body.textContent, /请选择评测模型/);
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/eval')).length, 1);
  pickPi(app);
  app.document.querySelector('[data-start-run-eval]').click();
  await settle(4);
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/eval')).length, 2);
});

test('missing runtime evidence requires a choice instead of an unrelated CodeBuddy key', async (t) => {
  const app = await loadApp({ routes: [{ path: '/api/assistant/models', body: catalog }] });
  t.after(app.close);
  await app.window.AgentFlowAssistantModels.load();
  app.evaluate(`state.runEvalModel='';localStorage.removeItem('agentflow.runEvalModel')`);
  renderEvaluation(app);
  app.document.querySelector('[data-start-run-eval]').click();
  await settle(3);
  assert.match(app.document.body.textContent, /请选择评测模型/);
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/eval')).length, 0);
});

test('a saved CLI model works before and after the model catalog arrives', async (t) => {
  let releaseCatalog;
  const pending = new Promise(resolve => { releaseCatalog = resolve; });
  const app = await loadApp({ routes: [{ path: '/api/assistant/models', body: () => pending }] });
  t.after(async () => { releaseCatalog(catalog); await app.close(); });
  app.window.localStorage.setItem('agentflow.runEvalModel', 'pi:eval-model');
  app.router.post('/api/runs/run-eval-runtime/eval', { id: 'eval-pi', status: 'running', model: 'pi:eval-model' });
  renderEvaluation(app);
  assert.equal(app.evaluate('runEvalModel()'), 'pi:eval-model');
  assert.equal(app.evaluate('runEvalHasKey()'), true);
  app.document.querySelector('[data-start-run-eval]').click();
  await settle(4);
  assert.equal(app.router.calls.find(c => c.method === 'POST' && c.path.endsWith('/eval'))?.body.model, 'pi:eval-model');

  releaseCatalog(catalog);
  await app.window.AgentFlowAssistantModels.load();
  renderEvaluation(app);
  assert.equal(app.document.querySelector('[name="runEvalModel"]').value, 'pi:eval-model');
  assert.equal(app.evaluate('runEvalHasKey()'), true);
});

test('a missing target credential keeps the runtime error and allows model changes', async (t) => {
  const app = await loadApp({ routes: [{ path: '/api/assistant/models', body: catalog }] });
  t.after(app.close);
  await app.window.AgentFlowAssistantModels.load();
  app.router.post('/api/runs/run-eval-runtime/eval', { error: '请先配置 Pi API Key' }, 400);
  renderEvaluation(app);
  pickPi(app);
  app.document.querySelector('[data-start-run-eval]').click();
  await settle(5);
  assert.match(app.document.body.textContent, /请先配置 Pi API Key/);
  assert.equal(app.evaluate('state.runEvalBusy'), false);
  const picker = app.document.querySelector('#runEvalModelPicker');
  picker.querySelector('[data-model-toggle]').click();
  picker.querySelector('[data-id="cb-eval"]').click();
  assert.equal(app.document.querySelector('[name="runEvalModel"]').value, 'cb-eval');
});
