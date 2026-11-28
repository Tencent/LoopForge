import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

// Records as the platform stores them after a LOOP opened PRs on GitHub:
// GitHub repositories carry gongfengProjectId "github:owner/repo" and the
// delivery MR keeps that key in project.
const fixture = `
  state.data.projects=[{id:'p1',name:'项目',workflowId:'wf',defaultRepositoryId:'core'}];
  state.data.workflows=[{id:'wf',name:'缺陷修复',nodes:[{id:'n1',name:'实现'}],edges:[]}];
  state.data.repositories=[
    {id:'core',projectId:'p1',provider:'github',name:'acme/convkit',gongfengProjectId:'github:acme/convkit',webUrl:'https://github.com/acme/convkit',defaultBranch:'main'},
    {id:'docs',projectId:'p1',provider:'github',name:'acme/convkit-docs',gongfengProjectId:'github:acme/convkit-docs',webUrl:'https://github.com/acme/convkit-docs',defaultBranch:'main'},
    {id:'gf',projectId:'p1',provider:'gongfeng',name:'g/web',gongfengProjectId:'g/web',webUrl:'https://git.example.com/g/web',defaultBranch:'master'}
  ];
  state.data.tasks=[];state.data.runs=[];state.data.issues=[];state.data.agents=[];
  const pr=(repo,iid)=>({repositoryId:repo,repository:'acme/'+(repo==='core'?'convkit':'convkit-docs'),project:'github:acme/'+(repo==='core'?'convkit':'convkit-docs'),iid,url:'https://github.com/acme/'+(repo==='core'?'convkit':'convkit-docs')+'/pull/'+iid,state:'opened',targetBranch:'main'});
  const run=(repos)=>({id:'run-1795860430327874880',workflowId:'wf',status:'succeeded',nodes:{},executionContext:{},
    repositories:repos.map(id=>({repositoryId:id,name:state.data.repositories.find(r=>r.id===id).name,baseBranch:'main',workBranch:'feature/fix-1'}))});
  const html=s=>{const d=document.createElement('div');d.innerHTML=s;return d};
  const facts=el=>[...el.querySelectorAll('.detail-fact')].map(f=>f.querySelector('span').textContent+'='+f.querySelector('b').textContent.trim());
`;

test('LOOP delivery of a GitHub repository shows PR #N and the plain repository path', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = structuredClone(app.evaluate(`(() => {${fixture}
    const issue={id:'gh-repo-1',projectId:'p1',source:'github',status:'done',workflowId:'wf',repositoryId:'core',repositoryIds:['core'],
      deliveryMrs:[pr('core',3)],deliveryMrUrl:'https://github.com/acme/convkit/pull/3',deliveryMrState:'opened'};
    const r=run(['core']);
    // A record without iid falls back to the number in the PR URL.
    const legacy={...issue,deliveryMrs:[],deliveryMrUrl:'https://github.com/acme/convkit/pull/7'};
    return {
      facts:facts(html(sopDeliveryPane(issue,{issue,run:r}))),
      legacyFacts:facts(html(sopDeliveryPane(legacy,{issue:legacy,run:r}))),
      button:html(deliveryMRButtons(issue)).textContent.trim(),
      buttonTitle:html(deliveryMRButtons(issue)).querySelector('a').title,
    };
  })()`));
  assert.ok(out.facts.includes('仓库路径=acme/convkit'), out.facts.join(' | '));
  assert.ok(out.facts.includes('交付 PR=#3 ↗'), out.facts.join(' | '));
  assert.ok(out.legacyFacts.includes('交付 PR=#7 ↗'), out.legacyFacts.join(' | '));
  assert.equal(out.button, '打开 PR ↗');
  assert.equal(out.buttonTitle, 'acme/convkit · 待合入');
  assert.ok(!out.facts.some(f => f.includes('github:') || f.includes('!3')), out.facts.join(' | '));
});

test('multi-repository requirements list every bound repository and label each PR', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = structuredClone(app.evaluate(`(() => {${fixture}
    const issue={id:'issue-5',projectId:'p1',source:'local',status:'done',workflowId:'wf',repositoryId:'core',repositoryIds:['core','docs'],
      deliveryMrs:[pr('core',5),pr('docs',1)],deliveryMrState:'opened'};
    state.data.issues=[issue];
    const r=run(['core','docs']);
    const overview=facts(html(overviewPane(issue,{issue,run:r})));
    const delivery=html(sopDeliveryPane(issue,{issue,run:r}));
    return {
      repoFact:overview.find(f=>f.startsWith('代码仓库=')),
      perRepo:[...delivery.querySelectorAll('.delivery-repo')].map(el=>el.querySelector('.delivery-repo-name').textContent+':'+facts(el).filter(f=>/仓库路径|交付/.test(f)).join(',')),
      buttons:[...html(deliveryMRButtons(issue)).querySelectorAll('a')].map(a=>a.textContent.trim()),
      bound:issueBoundRepositories(issue,state.data.projects[0]).map(r=>r.id),
      fallback:issueBoundRepositories({id:'x',projectId:'p1'},state.data.projects[0]).map(r=>r.id),
    };
  })()`));
  assert.equal(out.repoFact, '代码仓库=acme/convkit、acme/convkit-docs');
  assert.deepEqual(out.perRepo, [
    'acme/convkit:仓库路径=acme/convkit,交付 PR=#5 ↗',
    'acme/convkit-docs:仓库路径=acme/convkit-docs,交付 PR=#1 ↗',
  ]);
  assert.deepEqual(out.buttons, ['打开 PR · acme/convkit #5 ↗', '打开 PR · acme/convkit-docs #1 ↗']);
  assert.deepEqual(out.bound, ['core', 'docs']);
  assert.deepEqual(out.fallback, ['core']);
});

test('Gongfeng merge requests keep the MR !N wording', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = structuredClone(app.evaluate(`(() => {${fixture}
    const issue={id:'gf-1',projectId:'p1',source:'gongfeng',status:'done',workflowId:'wf',repositoryId:'gf',repositoryIds:['gf'],
      deliveryMrs:[{repositoryId:'gf',repository:'web',url:'https://git.example.com/g/web/-/merge_requests/9',state:'merged',targetBranch:'master'}],deliveryMrState:'merged'};
    const r=run(['gf']);
    return {facts:facts(html(sopDeliveryPane(issue,{issue,run:r}))),button:html(deliveryMRButtons(issue)).textContent.trim()};
  })()`));
  assert.ok(out.facts.includes('仓库路径=g/web'), out.facts.join(' | '));
  assert.ok(out.facts.includes('交付 MR=!9 ↗'), out.facts.join(' | '));
  assert.equal(out.button, '打开 MR ↗');
});

test('legacy GitHub Enterprise delivery URLs keep their PR number', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const refs = structuredClone(app.evaluate(`(() => {
    const url = 'https://code.example.test/acme/api/pull/17/files?tab=changes';
    const first = issue => deliveryMRList(issue)[0];
    const legacy = first({deliveryMrUrl:url});
    const listed = first({deliveryMrs:[{url}]});
    const stored = first({deliveryMrs:[{url, project:'enterprise/acme/api', iid:23}]});
    const unrelated = first({deliveryMrUrl:'https://code.example.test/issues/2?next=/pull/30'});
    return [deliveryMRRef(legacy), deliveryMRRef(listed), stored.project, deliveryMRRef(stored), unrelated.iid];
  })()`));
  assert.deepEqual(refs, ['#17', '#17', 'enterprise/acme/api', '#23', 0]);
});
