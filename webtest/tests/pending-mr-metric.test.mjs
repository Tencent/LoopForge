import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

test('project header counts pending MRs instead of artifacts and filters by them', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = await app.evaluate(`(() => {
    state.data.projects=[{id:'project-1',key:'AF',name:'AgentFlow'}];
    state.data.repositories=[];
    state.data.issues=[
      {id:'a',projectId:'project-1',number:1,title:'one',status:'in_progress',
       deliveryMrs:[{url:'https://git.example.com/team/api/-/merge_requests/11',state:'opened'}]},
      {id:'b',projectId:'project-1',number:2,title:'two',status:'done',
       deliveryMrUrl:'https://git.example.com/team/web/-/merge_requests/12',deliveryMrState:'merged'},
      {id:'c',projectId:'project-1',number:3,title:'three',status:'in_progress',
       deliveryMrs:[{url:'https://git.example.com/team/api/-/merge_requests/13',state:'reopened'},
                    {url:'https://git.example.com/team/other/-/merge_requests/14',state:'merged'}]},
      {id:'d',projectId:'project-1',number:4,title:'four',status:'todo'}
    ];
    state.projectFilter='project-1';
    state.issueRunFilter='';
    const counts=state.data.issues.map(i=>pendingDeliveryMRCount(i));
    renderProjectContext(state.data.issues);
    const metrics=$('#projectContextMetrics').innerHTML;
    state.issueRunFilter='mr';
    const filtered=filteredIssues().map(i=>i.id);
    state.issueRunFilter='';
    return {counts,metrics,filtered};
  })()`);
  assert.deepEqual([...result.counts], [1, 0, 1, 0]);
  assert.match(result.metrics, /<span>\u5f85\u5904\u7406 MR<\/span><b>2<\/b>/);
  assert.equal(result.metrics.includes('\u4ea4\u4ed8\u7269'), false);
  assert.deepEqual([...result.filtered].sort(), ['a', 'c']);
});
