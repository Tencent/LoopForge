import assert from 'node:assert/strict';
import test from 'node:test';
import {loadApp} from '../lib/load-app.mjs';

test('squad form sends the chosen opener, allowed teammates, project and budget', async t => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.agents = [
      {id:'writer',name:'Writer',projectId:'project-1'},
      {id:'audit',name:'Audit',projectId:'other',shared:true},
      {id:'hidden',name:'Hidden',projectId:'other'}
    ];
    showRunDetail = () => {};
    bindSquadForm();
  `);
  const get = id => app.document.getElementById(id);
  get('openAgentSquad').click();
  const checkboxes = [...get('squadMembers').querySelectorAll('input')];
  assert.deepEqual(checkboxes.map(el => el.value), ['writer', 'audit']);
  assert.equal(get('startAgentSquad').disabled, true);
  checkboxes.forEach(el => el.click());
  get('squadFirst').value = 'audit';
  get('squadLimit').value = '3';
  get('squadGoal').value = 'Verify the bill';
  app.router.post('/api/runs', {id:'run-squad', status:'running'});
  await get('squadForm').onsubmit({preventDefault() {}});
  const call = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/runs');
  assert.deepEqual(call.body, {
    projectId:'project-1', input:'Verify the bill',
    squad:{memberIds:['audit','writer'],maxTurns:3,sandboxProfileId:''},
  });
});

test('squad stream follows the current route after a handoff or rewind', async t => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`
    state.activeRun = {id:'s',workflowSnapshot:{id:'agent-squad',squad:{},nodes:[{id:'stale'}]},nodes:{stale:{status:'failed'}}};
    mergeLiveRun('s',{id:'s',workflowSnapshot:{id:'agent-squad',squad:{},nodes:[{id:'new'}]},nodes:{new:{status:'running'}}},{})
  `);
  assert.deepEqual(Object.keys(result.nodes), ['new']);
  assert.equal(result.workflowSnapshot.nodes[0].id, 'new');
});

test('English squad form translates the goal label and the auto sandbox option', async t => {
  const app = await loadApp({lang:'en'});
  t.after(app.close);
  app.evaluate(`
    state.data.agents = [{id:'writer',name:'Writer',projectId:'project-1'},{id:'audit',name:'Audit',projectId:'project-1'}];
    showRunDetail = () => {};
    bindSquadForm();
  `);
  const get = id => app.document.getElementById(id);
  get('openAgentSquad').click();
  await new Promise(resolve => setTimeout(resolve, 50));
  assert.equal(app.document.querySelector('label[for="squadGoal"]').textContent.trim(), 'Goal');
  assert.equal(get('squadSandbox').options[0].textContent, 'Auto select');
  assert.equal(get('squadSandbox').options[0].value, '');
  assert.doesNotMatch(get('squadForm').textContent, /任务目标|自动选择/);
});
