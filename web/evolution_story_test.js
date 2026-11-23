const assert = require('node:assert/strict');
const path = require('node:path');
const { createDOM } = require('./hui_fake_dom');

(async () => {
  const dom=createDOM(),w=dom.window;
  w.$=s=>dom.document.querySelector(s);w.$$=s=>[...dom.document.querySelectorAll(s)];
  w.state={projectFilter:'p'};
  w.escapeHTML=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  w.kbBadge=(text,tone)=>`<span class="badge ${tone}">${text}</span>`;
  for(const name of ['evolution.js','app/evolution-simple.js','app/evolution-review.js','app/evolution-story.js'])dom.load(path.join(__dirname,name));
  const api=dom.run('({evo,evoStoryLogEvidence,evoStoryBehaviorHTML,evoStoryArtifactPairs,evoStoryReadArtifact,evoStoryLoadArtifacts,evoStoryProblemsHTML,evoStoryReplayTable,evoStoryValidationHTML,evoStoryNodePickerHTML,evoGroupFacts})');
  const lines=['ERROR exited 1','--- FAIL: TestOutput','<img src=x onerror=alert(1)>'];
  const input=JSON.stringify({caseId:'case',lines});
  const before=JSON.stringify({evidence:{line:1,text:lines[0]}}),after=JSON.stringify({evidence:{line:2,text:lines[1]}});
  assert.equal(api.evoStoryLogEvidence(input,before,after).after,2);
  assert.equal(api.evoStoryLogEvidence(input,before,JSON.stringify({evidence:{line:2,text:'different'}})),null,'a guessed line or altered quote must not become a log diagram');
  assert.equal(api.evoStoryLogEvidence(input,before,JSON.stringify({evidence:{line:0,text:lines[0]}})),null);
  const data={replay:{id:'replay',sourceStatus:'failed',sourceScore:0},comparison:{nodes:[{id:'node',shaped:true,source:{status:'failed'},replay:{status:'succeeded'}}]},source:{run:{id:'source',input,status:'failed',nodes:{node:{output:before}}}},candidate:{run:{id:'candidate',input,status:'succeeded',nodes:{node:{output:after}}}}};
  const html=api.evoStoryBehaviorHTML({evidenceNode:'node'},data);
  dom.html(html);
  assert.equal(dom.document.querySelector('img'),null,'input and artifact content must remain escaped text');
  assert.match(html,/L1/);assert.match(html,/L2/);
  const different={...data,candidate:{run:{...data.candidate.run,input:'different'}}};
  assert.ok(!api.evoStoryBehaviorHTML({evidenceNode:'node'},different).includes('evo-story-log-line'));
  assert.match(api.evoStoryBehaviorHTML({evidenceNode:'node'},different),/输入不同/);
  const reused={...data,comparison:{nodes:[{...data.comparison.nodes[0],reused:true}]}};
  assert.ok(!api.evoStoryBehaviorHTML({evidenceNode:'node'},reused).includes('evo-story-log-line'));
  assert.match(api.evoStoryBehaviorHTML({evidenceNode:'node'},reused),/未重跑/);

  const finishedAt='2026-11-23T00:00:00Z';
  const proposal={id:'p',findingId:'current',assetKind:'agent',assetId:'role',summary:{total:3,finished:3,fixed:1,blocked:1,unchanged:1,passed:true},replays:[
    {id:'a',sourceRunId:'source-a',group:'diagnosis',sourceScore:0,score:2,outcome:'fixed',finishedAt},
    {id:'b',sourceRunId:'source-b',group:'holdout',sourceScore:0,score:0,outcome:'blocked',finishedAt},
    {id:'c',sourceRunId:'source-c',group:'pinned',sourceScore:2,score:2,outcome:'unchanged',finishedAt}
  ]};
  const m={proposal,compareFilter:'all',evidence:{type:'replay',id:'a'},data:{items:[]}};
  api.evo.modal=m;
  const validation=api.evoStoryValidationHTML(proposal);
  assert.ok(!validation.includes('回放成功 <b>3/3'),'finished count must not be presented as passing count');
  assert.match(validation,/阻塞 1/);
  const problems=api.evoStoryProblemsHTML(m,proposal);
  assert.ok(!problems.includes('source-c'),'successful regression samples are not new problems');
  assert.match(problems,/2 条问题样例/);
  dom.html(api.evoStoryReplayTable(m));
  assert.equal(dom.document.querySelectorAll('tbody tr').length,3,'the whole proposal stays visible when one problem is selected');
  assert.equal(dom.document.querySelectorAll('tbody tr')[1].querySelectorAll('[role="img"]')[1].getAttribute('aria-label'),'未完成或未记录');
  const current={id:'current',sampleRunIds:['source-a','source-b']},queued={id:'later',sampleRunIds:['later-run']};
  const facts=api.evoGroupFacts({p:proposal,issues:[{f:current,c:{}},{f:current,c:{}},{f:queued,c:{}}],queue:[{}],proposals:[proposal]});
  assert.match(facts,/2 条问题运行/);assert.match(facts,/后续建议 1/);

  const artifact={id:'artifact',nodeId:'node',attempt:2,name:'triage',path:'triage.json',mediaType:'application/json',size:100};
  const run={nodes:{node:{attempt:2}},artifacts:[artifact]};
  assert.equal(api.evoStoryArtifactPairs(run,run,'node').length,1);
  assert.equal(api.evoStoryArtifactPairs(run,{...run,artifacts:[{...artifact,path:'other.json'}]},'node').length,0);
  assert.equal(api.evoStoryArtifactPairs(run,{...run,artifacts:[{...artifact,attempt:1}]},'node').length,0,'previous attempts cannot be silently substituted');
  assert.equal(api.evoStoryArtifactPairs(run,{...run,artifacts:[artifact,{...artifact,id:'duplicate'}]},'node').length,0,'ambiguous registrations need raw inspection');
  assert.equal(api.evoStoryArtifactPairs(run,{...run,artifacts:[{...artifact,size:50000}]},'node').length,0);
  assert.ok(!api.evoStoryNodePickerHTML(['node'],'node',{workflowSnapshot:{nodes:[{id:'node',name:'Agent'}]}}).includes('<select'),'a single execution node needs no picker');
  w.AbortController=AbortController;w.TextDecoder=TextDecoder;
  w.fetch=async()=>new Response(after);
  assert.equal(await api.evoStoryReadArtifact('run',artifact),after);
  w.fetch=async()=>new Response('x'.repeat(32769));
  await assert.rejects(api.evoStoryReadArtifact('run',artifact),/产物过大/,'actual response bytes must be bounded even with small metadata');
  dom.html('<section id="evolutionView" class="active"><div id="evoStoryBehavior"></div></section>');
  const artifactData={...data,source:{run:{...data.source.run,artifacts:[artifact]}},candidate:{run:{...data.candidate.run,artifacts:[artifact]}}};
  const artifactModal={projectId:'p',evidenceNode:'node',evidenceData:artifactData};
  api.evo.modal=artifactModal;
  const pending=[];w.fetch=()=>new Promise(resolve=>pending.push(resolve));
  const loading=api.evoStoryLoadArtifacts(artifactModal,artifactData);
  assert.equal(pending.length,2);
  api.evo.modal={projectId:'another-project'};
  w.$('#evoStoryBehavior').textContent='new selection';
  pending.forEach(resolve=>resolve(new Response(after)));await loading;
  assert.equal(w.$('#evoStoryBehavior').textContent,'new selection','late artifact reads must not replace another detail or project');
  console.log('evolution story: grounded diagrams, input mismatch, reused nodes, sample scope, artifact identity and size limits passed');
})().catch(error=>{console.error(error);process.exitCode=1;});
