const assert = require('node:assert/strict');
const path = require('node:path');
const { createDOM } = require('./hui_fake_dom');

(async () => {
  const dom = createDOM(), w = dom.window;
  w.$ = selector => dom.document.querySelector(selector);
  w.$$ = selector => [...dom.document.querySelectorAll(selector)];
  w.escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  w.state = {projectFilter:'review-test'};
  w.kbBadge = (label, tone) => `<span class="${tone}">${label}</span>`;
  for (const file of ['evolution.js','app/evolution-simple.js','app/evolution-review.js','app/evolution-story.js']) dom.load(path.join(__dirname, file));
  const api = dom.run('({evo,evoReviewScoreHTML,evoReviewReplayTable,evoReviewChangeHTML,evoReviewJSONChanges,evoReviewTrialHTML,evoLoadLiveVersion,evoStartCanary})');
  const finishedAt = '2026-11-23T00:00:00Z';
  const replays = [
    {id:'waiting',sourceRunId:'a',sourceScore:0,score:0,status:'running',finishedAt:'0001-01-01T00:00:00Z'},
    {id:'blocked',sourceRunId:'b',sourceScore:2,score:2,status:'error',outcome:'blocked',finishedAt},
    {id:'reworked',sourceRunId:'c',sourceScore:0,score:1,outcome:'fixed',finishedAt},
  ];
  const table = api.evoReviewReplayTable({proposal:{replays},compareFilter:'all',data:{items:[{runId:'a',issueTitle:'<img src=x onerror=alert(1)>'}]}});
  dom.html(table);
  const rows = [...dom.document.querySelectorAll('tbody tr')];
  assert.equal(rows[0].querySelectorAll('[role="img"]')[1].getAttribute('aria-label'), '未完成或未记录');
  assert.equal(rows[1].querySelectorAll('[role="img"]')[1].getAttribute('aria-label'), '未完成或未记录');
  assert.equal(rows[2].querySelectorAll('[role="img"]')[1].getAttribute('aria-label'), '返工后成功');
  assert.equal(dom.document.querySelector('img'), null, 'sample labels must remain escaped text');
  assert.match(api.evoReviewScoreHTML(2), /一次成功/);
  assert.match(api.evoReviewScoreHTML(0), /失败/);

  const p = {id:'p',assetKind:'agent',assetId:'role',status:'pending',baseHash:'base',baseContent:'<script>unsafe</script>\n'+ '旧规则。'.repeat(40),candidateContent:'新规则。'.repeat(40),summary:{passed:true}};
  const diff = api.evoReviewChangeHTML({}, p);
  assert.match(diff, /data-evo-diff-expand/, 'long rewrites must be expandable');
  assert.ok(!diff.includes('<script>unsafe'), 'prompt content must be escaped');
  assert.match(api.evoReviewChangeHTML({diffSplit:true}, p), /evo-before-after/);
  const change=api.evoReviewJSONChanges('{"evidence":{"line":1},"same":"ok"}', '{"evidence":{"line":3},"same":"ok"}');
  assert.equal(change.length,1); assert.equal(change[0].path,'evidence.line'); assert.equal(change[0].before,'1'); assert.equal(change[0].after,'3');
  assert.equal(api.evoReviewJSONChanges('plain output','{}'),null,'free text keeps the original output view');
  assert.equal(api.evoReviewJSONChanges('null','{}'),null,'null is not a structured object');
  const typeChange=api.evoReviewJSONChanges('{"v":1}','{"v":"1"}')[0];
  assert.equal(typeChange.before,'1'); assert.equal(typeChange.after,'"1"','type changes must remain visible');
  const multiline={...p,baseContent:Array.from({length:12},(_,i)=>`old-${i}`).join('\n'),candidateContent:Array.from({length:12},(_,i)=>`new-${i}`).join('\n')};
  assert.equal((api.evoReviewChangeHTML({},multiline).match(/evo-diff-preview-line/g)||[]).length,2,'the initial diff shows one addition and one deletion');
  assert.match(api.evoReviewChangeHTML({diffExpanded:true},multiline),/new-11/,'expanding restores the whole actual diff');
  const ended=api.evoReviewTrialHTML({status:'failed',canary:{status:'failed',percent:25,minRuns:5,control:{runs:5,clean:5},canary:{runs:5,clean:1}}});
  assert.match(ended,/1\/5/,'failed trials must retain their actual result counts');
  assert.match(ended,/evoTrafficBlock" hidden/,'ended trials must not imply ongoing traffic');
  assert.ok(!ended.includes('evoTrafficPercent'),'ended trials cannot adjust traffic');

  dom.document.body.innerHTML = '<section id="evolutionView" class="active"><div id="evoDetailActions"><button data-evo-canary="p" disabled></button></div><dialog id="evoDetailCanary"><select id="evoCanaryPercent"></select><select id="evoCanaryMinRuns"></select><button data-evo-canary="p" disabled></button></dialog><span id="evoLiveVersion"></span></section>';
  const buttons = w.$$('[data-evo-canary]');
  const request = {}, m = {projectId:'review-test',proposalRequest:request,proposal:p};
  api.evo.modal=m; api.evo.projectId='review-test';
  w.api = async () => ({currentVersion:1,liveHash:'base'});
  await api.evoLoadLiveVersion(m,p,request);
  assert.ok(buttons.every(b => !b.disabled), 'matching live version enables both entry and dialog confirmation');
  for (const response of [{currentVersion:2,liveHash:'changed'},{currentVersion:1}]) {
    w.api = async () => response;
    await api.evoLoadLiveVersion(m,p,request);
    assert.ok(buttons.every(b => b.disabled), 'drift or missing hash must disable trial in both places');
  }
  buttons.forEach(b => b.disabled=true);
  let resolve;
  w.api = () => new Promise(r => {resolve=r;});
  const pending=api.evoLoadLiveVersion(m,p,request);
  api.evo.modal={};
  resolve({currentVersion:1,liveHash:'base'});
  await pending;
  assert.ok(buttons.every(b => b.disabled), 'stale version response must not enable another detail page');

  api.evo.modal=m;
  const form=w.$('#evoDetailCanary');
  form.open=false; form.showModal=()=>{form.open=true;}; form.close=()=>{form.open=false;};
  w.$('#evoCanaryPercent').value='25'; w.$('#evoCanaryMinRuns').value='10';
  let prompts=0, writes=0;
  w.afPrompt=async()=>{prompts++;return null;};
  await api.evoStartCanary('p',buttons[0]);
  assert.equal(form.open,false,'a drawer action cannot open unverified settings');
  w.api=async()=>({currentVersion:1,liveHash:'base'});
  await api.evoLoadLiveVersion(m,p,request);
  w.api=async()=>{writes++;};
  await api.evoStartCanary('p',buttons[0]);
  assert.ok(form.open); assert.equal(prompts,0); assert.equal(writes,0);
  await api.evoStartCanary('p',buttons[1]);
  assert.equal(form.open,false, 'native settings dialog must close before the shared confirmation');
  assert.equal(prompts,1); assert.equal(writes,0, 'cancelling confirmation must not start a trial');
  console.log('evolution review: scores, escaping, version gates, stale responses and trial cancellation passed');
})().catch(error => {console.error(error);process.exitCode=1;});
