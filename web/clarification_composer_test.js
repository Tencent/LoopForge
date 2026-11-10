'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(process.argv[2], 'utf8');
let requests = [], persisted = 0, footer = 0, nextResult = {summary:'已明确',questions:[],ready:true};
const panel = {hidden:true,html:'',set innerHTML(value){this.html=value},get innerHTML(){return this.html},querySelector:()=>null,querySelectorAll:()=>[]};
const elements = {
  '#reqClarifyPanel':panel,
  '#chatInput':{value:''},
  '#reqSupplement':null,
};
const ctx = {
  state:{},chatHistory:[],chatClarifySessionId:'',chatClarifyJobId:'',chatClarifyStopping:false,chatAbortController:null,reqAdvanceTimer:null,
  $:s=>elements[s],$$:()=>[],window:{},setTimeout,clearTimeout,
  currentProject:()=>({id:'p'}),
  escapeHTML:s=>String(s),clarifyRepoNoticeHTML:()=>'',
  clarifyThinkingReset:()=>{},persistRequirementComposer:()=>{persisted++},renderReqFooter:()=>{footer++},renderReqAnswerHistory:()=>{},
  selectedReqRepositoryId:()=>'r1',selectedReqRepositoryIds:()=>['r1'],selectedReqBaseBranch:()=>'feature/existing',selectedClarifyModel:()=>'test',
  reqCurrentRepoSnapshot:()=>({ids:['r1'],baseBranch:'feature/existing'}),
  AbortController,
  fetch:async (url,init)=>{requests.push({url,body:JSON.parse(init.body)});return {ok:true}},
  readChatClarifyResponse:async ()=>nextResult,
};
vm.createContext(ctx);
for(const name of [
  'reqIcon','hydrateReqIcons','reqDescription','clarifyOptionLabel','clarifyQuestionOptions','composerClarifyQuestions',
  'reqAnswerOf','reqQuestionAnswered','reqAnsweredCount','composerProgressHTML','collectChatReply',
  'usableClarifySummary','reqSupplementHTML','reqQuestionCardHTML','renderClarifyPanel','bindReqClarifyPanel','pickReqOption','setReqOtherAnswer',
  'moveReqQuestion','submitReqAnswers','useRecommendedAnswers','normalizeClarifyParsed','parseClarifyPayload',
  'requirementComposerBusy','applyChatClarifyOutcome','sendChatClarify','newComposerJobId','finishComposerClarifyStream','setChatSendAnalyzing','reqAnswerReceipts','projectTAPDReady',
]){
  const start=source.search(new RegExp('^(?:async )?function '+name+'\\(','m'));
  assert.ok(start>=0,name);
  const rest=source.slice(start),next=/\n(?:async )?function \w+\(/.exec(rest);
  vm.runInContext(next?rest.slice(0,next.index):rest,ctx);
}
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
function show(result) {
  ctx.state.chatClarifyResult=result;
  ctx.state.reqAnswers={};
  ctx.state.reqQuestionIndex=0;
  ctx.renderClarifyPanel(result);
}
(async()=>{
  // Older responses that only carried assessment.missing offer a regenerate action and free-form supplement; no scores.
  const legacy={summary:'已读懂代码，需要确认修复目的和 ID 输入方式。',questions:[],ready:false,assessment:{dimensions:{goal:40},missing:['修复目的是什么？','ID 怎么输入？']}};
  show(legacy);
  assert.ok(panel.html.includes('重新生成问题'));
  assert.ok(panel.html.includes('还需要补充说明'));
  assert.ok(!panel.html.includes('查看完整度详情') && !panel.html.includes('准入线'), 'scores are never shown');
  assert.ok(!/第 \d+ 轮|没有固定轮数/.test(panel.html));

  // One question per card; single choice auto-advances, the last one auto-submits.
  const choice={summary:'确认修复方式',ready:false,assessment:{dimensions:{goal:60},missing:['goal']},questions:[
    {id:'q1',question:'重新推送？',options:['是','否']},
    {id:'q2',question:'更新 retry？',options:['是','否']},
  ]};
  show(choice);
  assert.ok(panel.html.includes('第 1 / 2 题'));
  assert.ok(panel.html.includes('id="reqNextQuestion"') && panel.html.includes('跳过此题'));
  assert.ok(!panel.html.includes('查看完整度详情') && !panel.html.includes('准入线'));
  assert.ok(!panel.html.includes('推荐'), 'no recommended tag without recommended options');
  assert.ok(!panel.html.includes('全部用推荐答案'));
  // Submitting with nothing answered warns and sends nothing.
  ctx.chatHistory=[{role:'user',content:'原始需求'}];
  ctx.state.reqQuestionIndex=1;
  await ctx.submitReqAnswers();
  assert.equal(requests.length,0);
  assert.ok(panel.html.includes('至少回答一题，或点「直接去创建」'));
  ctx.state.reqQuestionIndex=0;
  ctx.renderClarifyPanel(choice);
  ctx.pickReqOption(0);
  assert.equal(ctx.state.reqQuestionIndex,0);
  assert.ok(panel.html.includes('aria-checked="true"'));
  await sleep(300);
  assert.equal(ctx.state.reqQuestionIndex,1);
  assert.ok(panel.html.includes('第 2 / 2 题') && panel.html.includes('提交回答'));
  // Re-clicking toggles a single choice off and does not advance.
  ctx.pickReqOption(1);
  ctx.pickReqOption(1);
  await sleep(300);
  assert.equal(requests.length,0);
  assert.deepEqual([...ctx.reqAnswerOf(1).selected],[]);
  ctx.moveReqQuestion(-1);
  assert.equal(ctx.state.reqQuestionIndex,0);
  ctx.setReqOtherAnswer('只修正本地状态');
  assert.deepEqual([...ctx.reqAnswerOf(0).selected],[]);
  assert.equal(ctx.collectChatReply(),'重新推送？：只修正本地状态');
  ctx.moveReqQuestion(1);
  nextResult={summary:'已明确',questions:[],ready:true};
  ctx.pickReqOption(1);
  await sleep(300);
  assert.equal(requests.length,1);
  assert.equal(requests[0].url,'/api/chat-clarify');
  assert.equal(requests[0].body.message,'重新推送？：只修正本地状态\n更新 retry？：否');
  assert.equal(requests[0].body.baseBranch,'feature/existing');
  assert.deepEqual(requests[0].body.repositoryIds,['r1']);
  assert.equal(ctx.chatHistory.filter(h=>h.role==='user').length,2);
  assert.deepEqual(JSON.parse(JSON.stringify(ctx.state.reqClarifyRepoSnapshot)),{ids:['r1'],baseBranch:'feature/existing'});
  assert.equal(ctx.state.chatClarifyResult.ready,true);
  assert.deepEqual(JSON.parse(JSON.stringify(ctx.state.reqAnswers)),{});
  assert.ok(panel.html.includes('已问清，可以创建'));
  assert.ok(panel.html.includes('id="reqSupplement"'));

  // Multi choice keeps both picks, does not auto-submit, and joins answers.
  show({summary:'字段',ready:false,questions:[{id:'q1',question:'更新字段？',options:['push_time','retry'],multiSelect:true}]});
  assert.ok(panel.html.includes('可多选') && panel.html.includes('提交回答'));
  ctx.pickReqOption(0);ctx.pickReqOption(1);
  await sleep(300);
  assert.equal(requests.length,1);
  assert.equal(ctx.collectChatReply(),'更新字段？：push_time、retry');

  // Recommended options light up the tag and the 全部用推荐答案 shortcut.
  const recommended=ctx.normalizeClarifyParsed({questions:[{question:'范围？',options:[{label:'仅 Web',recommended:true},{label:'全端'}]}]});
  assert.deepEqual([...recommended.questions[0].recommendedOptions],['仅 Web']);
  show({summary:'范围',ready:false,questions:recommended.questions});
  assert.ok(panel.html.includes('class="req-rec">推荐') && panel.html.includes('全部用推荐答案'));
  ctx.useRecommendedAnswers();
  assert.equal(ctx.collectChatReply(),'范围？：仅 Web');
  await sleep(460);
  assert.equal(requests.length,2);
  assert.equal(requests[1].body.message,'范围？：仅 Web');

  // Retry resends the previous user message without adding history.
  ctx.renderClarifyPanel(null,'未生成可回答的问题');
  assert.ok(panel.html.includes('重新澄清'));
  const users=ctx.chatHistory.filter(h=>h.role==='user').length;
  await ctx.sendChatClarify(true);
  assert.equal(requests.at(-1).body.message,'范围？：仅 Web');
  assert.equal(ctx.chatHistory.filter(h=>h.role==='user').length,users);

  // A busy analysis blocks duplicate submits.
  ctx.chatAbortController=new AbortController();
  const count=requests.length;
  await ctx.sendChatClarify(false,'重复提交');
  assert.equal(requests.length,count);
  ctx.chatAbortController=null;
  console.log('clarification composer behavior passed');
})().catch(err=>{console.error(err);process.exitCode=1});
