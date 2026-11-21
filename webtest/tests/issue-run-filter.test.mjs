import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

test('已闭环 lists delivered work and 已关闭 lists work closed without delivery', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = structuredClone(await app.evaluate(`(() => {
    state.data.issues=[
      {id:'delivered',projectId:'project-1',number:3,title:'Delivered',status:'done',source:'local',
       deliveryState:'merged',deliveryMrState:'merged',deliveryMrUrl:'https://git.example.com/team/app/-/merge_requests/1'},
      {id:'manual',projectId:'project-1',number:2,title:'Closed by hand',status:'closed',closeSource:'manual',source:'local'},
      {id:'external',projectId:'project-1',number:1,title:'Closed in TAPD',status:'closed',closeSource:'external',source:'tapd'},
      {id:'open',projectId:'project-1',number:4,title:'Open',status:'todo',source:'local'}
    ];
    state.projectFilter='project-1';
    state.sourceFilter='';state.issueStatusFilter='';state.issueAssigneeFilter='';state.issueQuery='';
    const pick=filter=>{state.issueRunFilter=filter;return filteredIssues().map(x=>x.id).sort()};
    return {
      health:state.data.issues.map(i=>[i.id,issueRunHealth(i)]),
      closed:pick('closed'),
      shut:pick('shut'),
      all:pick(''),
    };
  })()`));
  assert.deepEqual(result.shut, ['external', 'manual']);
  assert.deepEqual(result.closed, ['delivered'], JSON.stringify(result));
  assert.equal(result.all.length, 4);
  for (const id of result.closed) {
    assert.equal(Object.fromEntries(result.health)[id], 'closed');
  }
});
