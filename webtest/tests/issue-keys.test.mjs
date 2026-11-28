import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

// GitHub and 工蜂 sync stores the tracker number in issue.number, so a synced
// issue and a platform requirement of the same project can both have number 1.
// Every surface must show the same, unambiguous key for each of them.
test('synced tracker issues keep their tracker key on every surface', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = structuredClone(await app.evaluate(`(() => {
    state.data.projects[0].key = 'SWP';
    const local = {id:'local-1',projectId:'project-1',number:1,title:'title_case 吞掉连续空格',source:'local',status:'todo',priority:'medium'};
    const github = {id:'gh-repo-1',projectId:'project-1',number:1,title:'word_count 全角空格',source:'github',kind:'issue',status:'todo',priority:'medium',
      externalRef:{source:'github',externalId:'990001',externalIid:1,externalType:'issue'}};
    const gongfeng = {id:'gf-repo-5',projectId:'project-1',number:5,title:'工蜂缺陷',source:'gongfeng',status:'todo',priority:'medium',
      externalRef:{source:'gongfeng',externalId:'77005',externalIid:5}};
    const tapd = {id:'tapd-1',projectId:'project-1',number:2,title:'TAPD 需求',source:'tapd',status:'todo',priority:'medium',
      externalRef:{source:'tapd',externalId:'1010101'}};
    const plain = {id:'p-9',projectId:'missing',number:9,title:'无项目',status:'todo'};
    state.data.issues = [local, github, gongfeng, tapd];
    state.projectFilter = 'project-1';
    state.sourceFilter=''; state.issueStatusFilter=''; state.issueAssigneeFilter=''; state.issueRunFilter='';
    const search = q => { state.issueQuery = q; return filteredIssues().map(i => i.id); };
    state.data.tasks = [{id:'t1',issueId:'local-1',runId:'run-local'},{id:'t2',issueId:'gh-repo-1',runId:'run-gh'}];
    const run = (id, issueId) => ({id, workflowId:'flow-1', status:'waiting', nodes:{}, createdAt:new Date().toISOString(),
      executionContext:{issueId}, workflowSnapshot:{id:'flow-1', name:'LOOP', nodes:[{id:'accept',name:'人工验收',type:'approval'}]}});
    const runRef = r => { const d = document.createElement('div'); d.innerHTML = runRowHTML(r, state.data.workflows || []); return d.querySelector('.run-ref')?.textContent; };
    const apKey = r => { const d = document.createElement('div'); d.innerHTML = approvalCardHTML(r); return d.querySelector('.ap-key')?.textContent; };
    openIssue(github);
    const modalGitHub = document.querySelector('#issueModalTitle').textContent;
    openIssue(local);
    const modalLocal = document.querySelector('#issueModalTitle').textContent;
    return {
      labels: [local, github, gongfeng, tapd, plain].map(issueKeyLabel),
      searchLocalKey: search('SWP-1'),
      searchGitHubKey: search('#1'),
      searchGongfengKey: search('#5'),
      runRefs: [runRef(run('run-local','local-1')), runRef(run('run-gh','gh-repo-1'))],
      approvalKeys: [apKey(run('run-local','local-1')), apKey(run('run-gh','gh-repo-1'))],
      modalGitHub, modalLocal,
    };
  })()`));
  assert.deepEqual(result.labels, ['SWP-1', '#1', '#5', '1010101', 'ISS-9']);
  assert.deepEqual(result.searchLocalKey, ['local-1']);
  assert.deepEqual(result.searchGitHubKey, ['gh-repo-1']);
  assert.deepEqual(result.searchGongfengKey, ['gf-repo-5']);
  assert.deepEqual(result.runRefs, ['SWP-1', '#1']);
  assert.deepEqual(result.approvalKeys, ['SWP-1', '#1']);
  assert.equal(result.modalGitHub, '#1 word_count 全角空格');
  assert.equal(result.modalLocal, 'SWP-1 title_case 吞掉连续空格');
});
