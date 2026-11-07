import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

// A requirement worked on only in a conversation workspace has no SOP nodes:
// its stage follows the MR it handed in, and 查看 opens the requirement
// instead of the empty run page.
test('conversation requirements follow their MR and open the requirement drawer', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = JSON.parse(app.evaluate(`
    const pid=state.projectFilter||state.data.projects[0].id;
    const base={id:'iss-chat',projectId:pid,number:51,kind:'requirement',source:'local',status:'in_progress',title:'对话需求',conversationSessionId:'chat-1',workflowId:state.data.workflows[0].id};
    const ws={id:'ws-chat',kind:'workspace',workflowId:'conversation-workspace',status:'waiting',createdAt:new Date().toISOString(),executionContext:{projectId:pid,issueId:'iss-chat'},repositories:[{name:'web',baseBranch:'master',workBranch:'feature/chat-1'}],workflowSnapshot:{id:'conversation-workspace',name:'对话工作区'}};
    state.data.tasks=[];
    state.data.runs=[ws];
    const mr=(st)=>({deliveryMrs:[{url:'https://git.example.com/g/web/-/merge_requests/9',state:st,repository:'web',targetBranch:'master'}],deliveryMrState:st});
    const view=(issue)=>{
      state.data.issues=[issue];
      const box=document.createElement('div');box.innerHTML=issueRow(issue);
      const card=document.createElement('div');card.innerHTML=issueKanbanCard(issue);
      return {stage:issueCurrentStage(issue).label,health:issueRunHealth(issue),progress:box.querySelector('.issue-progress-cell')?.textContent.trim(),
        progressCls:box.querySelector('.issue-progress-cell')?.className,cardCls:card.querySelector('.kanban-card-progress')?.className,
        failed:box.querySelector('.issue-table-row').className.includes('needs-attention'),
        card:card.querySelector('.kanban-card-progress')?.textContent.trim(),
        sop:issueSOPLabel(issue),actions:issueActionButtons(issue)};
    };
    const dev=view(base);
    const pending=view({...base,status:'in_review',...mr('opened')});
    const direct=view({...base,status:'in_review'});
    const closedMR=view({...base,...mr('closed')});
    const merged=view({...base,status:'done',deliveryState:'merged',...mr('merged')});
    state.data.runs=[{...ws,status:'cancelled'}];
    const ended=view({...base,status:'in_review',...mr('opened')});
    state.data.runs=[ws,{id:'sop-1',workflowId:'wf-a',status:'running',createdAt:new Date(Date.now()-60000).toISOString(),executionContext:{projectId:pid,issueId:'iss-chat'},nodes:{a:{status:'running'}}}];
    const withSOP=conversationWorkspaceIssue(base);
    state.data.runs=[];
    const gone=view({...base,status:'in_review',...mr('opened')});
    JSON.stringify({dev,pending,direct,closedMR,merged,ended,withSOP,gone});
  `));
  assert.equal(out.dev.stage, '对话开发中');
  assert.equal(out.dev.health, 'active');
  assert.equal(out.dev.progress, '开发中');
  assert.equal(out.dev.card, '开发中');
  assert.equal(out.dev.sop, '对话工作区');
  assert.match(out.dev.actions, /data-open-issue="iss-chat" data-dtab="sop">查看/);
  assert.doesNotMatch(out.dev.actions, /data-view-run|data-trigger-issue/);
  assert.equal(out.pending.stage, 'MR 待合入');
  assert.equal(out.pending.progress, '待合入');
  assert.equal(out.direct.stage, '已直推 · 待验收');
  assert.equal(out.direct.progress, '待验收');
  assert.equal(out.closedMR.stage, 'MR 已关闭');
  assert.equal(out.closedMR.health, 'closed');
  // Closed without merging is not a delivery: grey bar, not the green "done" one.
  assert.match(out.closedMR.progressCls, /\bended\b/);
  assert.doesNotMatch(out.closedMR.progressCls, /\bdone\b/);
  assert.match(out.closedMR.cardCls, /\bended\b/);
  assert.match(out.merged.progressCls, /\bdone\b/);
  assert.equal(out.merged.stage, 'MR 已合入');
  assert.equal(out.merged.progress, '100%');
  assert.match(out.merged.actions, /data-open-issue="iss-chat"/);
  assert.doesNotMatch(out.merged.actions, /data-view-run/);
  // A deleted conversation ends its workspace: that is not a failure.
  assert.equal(out.ended.stage, 'MR 待合入');
  assert.equal(out.ended.failed, false);
  // Once dispatched to an SOP the requirement follows its SOP run.
  assert.equal(out.withSOP, false);
  // The source conversation still marks it after the workspace record is gone.
  assert.equal(out.gone.stage, 'MR 待合入');
  assert.equal(out.gone.sop, '对话工作区');
});

test('conversation requirement drawer shows MR, branches and the conversation', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = JSON.parse(app.evaluate(`
    const pid=state.projectFilter||state.data.projects[0].id;
    const issue={id:'iss-chat',projectId:pid,number:51,kind:'requirement',source:'local',status:'in_review',title:'对话需求',conversationSessionId:'chat-1',
      deliveryMrs:[{url:'https://git.example.com/g/web/-/merge_requests/9',iid:9,state:'opened',repository:'web',targetBranch:'master'}],deliveryMrState:'opened'};
    const run={id:'ws-chat',kind:'workspace',workflowId:'conversation-workspace',status:'waiting',createdAt:new Date().toISOString(),executionContext:{projectId:pid,issueId:'iss-chat'},repositories:[{name:'web',baseBranch:'master',workBranch:'feature/chat-1'}]};
    state.data.issues=[issue];state.data.tasks=[];state.data.runs=[run];
    const live={issue,run,requirement:{conversation:{sessionId:'chat-1',available:true,branches:[{repository:'web',baseBranch:'master',workBranch:'feature/chat-1'}]}}};
    const html=(s)=>{const d=document.createElement('div');d.innerHTML=s;return d};
    const sop=html(sopPane(issue,live));
    const link=sop.querySelector('a[href*="chatSession"]')?.getAttribute('href')||'';
    const overview=html(overviewPane(issue,live));
    const delivery=html(deliveryPane(issue,live));
    const deleted=html(sopPane(issue,{...live,requirement:{conversation:{sessionId:'chat-1',available:false}}}));
    const noSnapshot=html(sopPane(issue,{issue,run,requirement:null}));
    const card=sop.querySelector('.conv-repo');
    const hrefs=el=>[...el.querySelectorAll('a[href]')].map(a=>a.getAttribute('href'));
    const pushed={...issue,deliveryMrs:[],deliveryMrState:''};
    const pushedBranches=[{repository:'web',webUrl:'https://git.example.com/g/web.git',baseBranch:'preview',workBranch:'preview'},{repository:'api',webUrl:'https://git.example.com/g/api',baseBranch:'main',workBranch:'feature/chat 2'}];
    const push=html(sopPane(pushed,{issue:pushed,run,requirement:{conversation:{sessionId:'chat-1',available:true,branches:pushedBranches}}}));
    JSON.stringify({
      head:sop.querySelector('.issue-outcome b')?.textContent,
      badge:card?.querySelector('.hui-badge')?.textContent,
      flow:card?.querySelector('.conv-repo-flow')?.textContent,
      cardLinks:card?hrefs(card):[],
      artifacts:html(artifactsPane(live)).textContent,acceptance:html(acceptancePane(issue,live)).textContent,activity:html(activityPane(issue,{...live,comments:[]})).textContent,
      push:push.textContent,pushCards:[...push.querySelectorAll('.conv-repo')].map(c=>({badge:c.querySelector('.hui-badge')?.textContent,links:hrefs(c),flow:c.querySelector('.conv-repo-flow')?.textContent})),
      sop:sop.textContent,link,runLinks:sop.querySelectorAll('[data-open-run],[data-open-logs]').length,
      overview:overview.textContent,overviewRunLinks:overview.querySelectorAll('[data-open-run],[data-open-logs],[data-export-logs]').length,
      overviewChat:!!overview.querySelector('a[href*="chatSession"]'),
      delivery:delivery.textContent,deliveryRunLinks:delivery.querySelectorAll('[data-open-run]').length,
      deleted:deleted.textContent,deletedLink:!!deleted.querySelector('a[href*="chatSession"]'),
      fallback:noSnapshot.textContent,fallbackLink:!!noSnapshot.querySelector('a[href*="chatSession"]')
    });
  `));
  assert.equal(out.head, 'MR 待合入');
  assert.equal(out.badge, 'MR 待合入');
  assert.equal(out.flow, 'feature/chat-1→master');
  // The branch, the MR and the diff all open in Gongfeng; the repository comes from the MR link.
  assert.deepEqual(out.cardLinks, [
    'https://git.example.com/g/web',
    'https://git.example.com/g/web/-/tree/feature/chat-1',
    'https://git.example.com/g/web/-/merge_requests/9',
    'https://git.example.com/g/web/-/compare/master...feature/chat-1',
  ]);
  assert.match(out.sop, /打开会话/);
  // Pushed straight to the shared branch: no MR, no diff against itself; other repos still compare.
  assert.equal(out.pushCards[0].badge, '已直推');
  assert.match(out.pushCards[0].flow, /直接提交在这个分支上/);
  assert.deepEqual(out.pushCards[0].links, ['https://git.example.com/g/web', 'https://git.example.com/g/web/-/tree/preview']);
  assert.deepEqual(out.pushCards[1].links, [
    'https://git.example.com/g/api',
    'https://git.example.com/g/api/-/tree/feature/chat%202',
    'https://git.example.com/g/api/-/compare/main...feature/chat%202',
  ]);
  assert.match(out.push, /已直推 · 待验收/);
  // The SOP-only tabs explain where a conversation requirement keeps that information.
  assert.match(out.artifacts, /不产出 SOP 节点产物/);
  assert.match(out.acceptance, /在 MR 里评审，合入默认分支后需求自动完成/);
  assert.doesNotMatch(out.acceptance, /尚未执行/);
  assert.match(out.activity, /对话需求没有 SOP 任务/);
  const url = new URL(out.link);
  assert.equal(url.searchParams.get('chatSession'), 'chat-1');
  assert.equal(url.hash, '#/collaboration');
  assert.equal(out.runLinks, 0);
  assert.equal(out.overviewRunLinks, 0);
  assert.ok(out.overviewChat);
  assert.match(out.overview, /对话工作区/);
  assert.equal(out.deliveryRunLinks, 0);
  assert.match(out.delivery, /没有 SOP 运行/);
  assert.match(out.deleted, /对话已删除/);
  assert.equal(out.deletedLink, false);
  // Without the snapshot the branches come from the run list.
  assert.match(out.fallback, /feature\/chat-1/);
  assert.ok(out.fallbackLink);
});

// GitHub repositories get GitHub URLs and PR wording on the same card.
test('conversation repo cards link GitHub branches, PRs and diffs', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = JSON.parse(app.evaluate(`
    const pid=state.projectFilter||state.data.projects[0].id;
    const issue={id:'iss-gh',projectId:pid,number:52,kind:'requirement',source:'local',status:'in_review',title:'对话需求',conversationSessionId:'chat-2',
      deliveryMrs:[{url:'https://github.com/acme/web/pull/12',iid:12,state:'opened',repository:'web',project:'acme/web',targetBranch:'main'}],deliveryMrState:'opened'};
    const run={id:'ws-gh',kind:'workspace',workflowId:'conversation-workspace',status:'waiting',createdAt:new Date().toISOString(),executionContext:{projectId:pid,issueId:'iss-gh'},repositories:[{name:'web',baseBranch:'main',workBranch:'feature/chat-2'}]};
    state.data.issues=[issue];state.data.tasks=[];state.data.runs=[run];
    const live={issue,run,requirement:{conversation:{sessionId:'chat-2',available:true,branches:[{repository:'web',webUrl:'https://github.com/acme/web.git',baseBranch:'main',workBranch:'feature/chat-2'}]}}};
    const d=document.createElement('div');d.innerHTML=sopPane(issue,live);
    const card=d.querySelector('.conv-repo');
    const pushed={...issue,deliveryMrs:[],deliveryMrState:''};
    const p=document.createElement('div');p.innerHTML=sopPane(pushed,{issue:pushed,run,requirement:{conversation:{sessionId:'chat-2',available:true,branches:[{repository:'web',webUrl:'https://github.com/acme/web',baseBranch:'main',workBranch:'feature/chat-2'}]}}});
    JSON.stringify({
      badge:card?.querySelector('.hui-badge')?.textContent,
      links:[...card.querySelectorAll('a[href]')].map(a=>a.getAttribute('href')),
      titles:[...card.querySelectorAll('a[title]')].map(a=>a.getAttribute('title')),
      buttons:[...card.querySelectorAll('.conv-repo-actions a')].map(a=>a.textContent.trim()),
      unsubmitted:p.querySelector('.conv-repo .hui-badge')?.textContent
    });
  `));
  assert.equal(out.badge, 'PR 待合入');
  assert.deepEqual(out.links, [
    'https://github.com/acme/web',
    'https://github.com/acme/web/tree/feature/chat-2',
    'https://github.com/acme/web/pull/12',
    'https://github.com/acme/web/compare/main...feature/chat-2',
  ]);
  assert.ok(out.titles.some(title => title.startsWith('在GitHub打开分支')), JSON.stringify(out.titles));
  assert.equal(out.buttons[0], 'PR #12 ↗');
  assert.equal(out.unsubmitted, '已直推');
});
