import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

// Opt-in: connects the actual designer and run-detail code to an isolated
// LoopForge server, rather than mocking its workflow/run endpoints.
// Run from the repository root: go run ./webtest/fixtures/conditional-routing-server.go
// Then: LOOPFORGE_ROUTING_TEST_URL=http://127.0.0.1:39154 node --test webtest/tests/conditional-routing-integration.test.mjs
test('conditional routing designer and real engine', { skip: !process.env.LOOPFORGE_ROUTING_TEST_URL }, async (t) => {
  const base = process.env.LOOPFORGE_ROUTING_TEST_URL;
  const app = await loadApp();
  t.after(app.close);
  const fetchReal = async (path, options = {}) => {
    const response = await fetch(new URL(path, base), options);
    const body = await response.text();
    if (!response.ok) throw new Error(`${options.method || 'GET'} ${path}: ${response.status} ${body}`);
    return JSON.parse(body);
  };
  // Keep unrelated bootstrap requests mocked; the actual actions below use
  // the real workflow and run endpoints, including their validation/storage.
  app.window.fetch = (path, options) => /^\/api\/(workflows|runs)(\/|\?|$)/.test(String(path))
    ? fetch(new URL(path, base), options) : app.router.fetch(path, options);
  const workflowID = `issue354-ui-smoke-${Date.now()}`;
  app.evaluate(`switchView('flow');state.projectFilter='platform';state.data.projects=[{id:'platform',name:'Test'}];state.data.sandboxes=[{id:'local',projectId:'platform',driver:'local'}];
    state.workflow={id:${JSON.stringify(workflowID)},projectId:'platform',name:'Conditional routing simulation',status:'draft',sandboxProfileId:'local',nodes:[
      {id:'route',name:'Classify',type:'script',script:${JSON.stringify(`printf '%s' '{"route":"small"}' > "$LOOPFORGE_ROUTE_FILE"`)},maxAttempts:1,position:{x:20,y:30}},
      {id:'small',name:'Small branch',type:'script',script:'echo small',maxAttempts:1,position:{x:300,y:30}},
      {id:'large',name:'Large branch',type:'script',script:'echo large',maxAttempts:1,position:{x:300,y:150}},
      {id:'join',name:'Join',type:'script',script:'echo joined',maxAttempts:1,position:{x:600,y:80}}
    ],edges:[{id:'small-edge',source:'route',target:'small',when:'success'},{id:'large-edge',source:'route',target:'large',when:'success'},{id:'sj',source:'small',target:'join',when:'success'},{id:'lj',source:'large',target:'join',when:'success'}]};
    state.selectedNode='route';renderCanvas();renderProperties()`);
  for (const [id, label] of [['small-edge', 'small'], ['large-edge', 'large']]) {
    let control = app.document.querySelector(`[data-route-edge="${id}"] [data-route-field="type"]`);
    control.value = 'route'; control.dispatchEvent(new app.window.Event('change'));
    control = app.document.querySelector(`[data-route-edge="${id}"] [data-route-field="values"]`);
    control.value = label; control.dispatchEvent(new app.window.Event('change'));
  }
  assert.match(app.document.querySelector('#edges').textContent, /route: small/);
  app.evaluate(`state.selectedNode='join';renderProperties()`);
  assert.match(app.document.querySelector('#properties').textContent, /汇合条件分支/);
  const join = app.document.querySelector('#properties [data-p="joinRule"]');
  join.value = 'none_failed_min_one_success'; join.dispatchEvent(new app.window.Event('input'));
  const saved = await app.evaluate('saveWorkflow()');
  assert.equal(saved.edges[0].condition.values[0], 'small');
  assert.equal(saved.nodes.find(n => n.id === 'join').joinRule, 'none_failed_min_one_success');

  for (const route of ['small', 'large']) {
    if (route === 'large') {
      app.evaluate(`state.workflow.nodes[0].script=${JSON.stringify(`printf '%s' '{"route":"large"}' > "$LOOPFORGE_ROUTE_FILE"`)}`);
      await app.evaluate('saveWorkflow()');
    }
    const started = await fetchReal('/api/runs', { method:'POST', headers:{'content-type':'application/json'}, body:JSON.stringify({workflowId:saved.id,projectId:'platform'}) });
    let run;
    for (let i=0; i<100; i++) {
      run = await fetchReal(`/api/runs/${started.id}`);
      if (['succeeded','failed','waiting'].includes(run.status)) break;
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    assert.equal(run.status, 'succeeded', JSON.stringify(run.nodes));
    const skipped = route === 'small' ? 'large' : 'small';
    assert.equal(run.nodes[route].status, 'succeeded');
    assert.equal(run.nodes[skipped].status, 'skipped');
    assert.equal(run.nodes.join.status, 'succeeded');
    assert.equal(run.nodes[skipped].attempt, 0);
    app.window.__routeSmokeRun = run;
    app.evaluate(`state.activeRun=window.__routeSmokeRun;state.selectedRun=state.activeRun.id;state.selectedRunNode=${JSON.stringify(skipped)};renderRunDetail(state.activeRun)`);
    await new Promise(resolve => setTimeout(resolve, 400));
    const reason = app.document.querySelector('#runDetail .node-skip-reason');
    assert.ok(reason, 'skipped node reason appears in run detail');
    assert.match(reason.textContent, new RegExp(`路由.*${route}.*未选择此分支`));
    console.log(`route=${route}: ${route}=succeeded, ${skipped}=skipped, join=succeeded; UI: ${reason.textContent.trim()}`);
  }
});
