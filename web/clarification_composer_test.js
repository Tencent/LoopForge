'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(process.argv[2], 'utf8');
let questions = [], requests = [], notices = [];
function classes() {
  const values = new Set();
  return {contains:v=>values.has(v),add:v=>values.add(v),remove:v=>values.delete(v),toggle:v=>values.has(v)?values.delete(v):values.add(v)};
}
function question(text, labels, index=0, multi=false) {
  const q = {textContent:text};
  const input = {value:'',dataset:{q:String(index)},focus(){this.focused=true}};
  const buttons = labels.map(textContent=>({
    textContent,dataset:{q:String(index),multi:String(multi)},classList:classes(),
    setAttribute(k,v){this[k]=v},closest:()=>q,
  }));
  q.querySelector = s=>s==='.chat-question-text'?{textContent:text}:input;
  q.querySelectorAll = ()=>buttons.filter(b=>b.classList.contains('selected'));
  q.buttons=buttons;q.input=input;
  return q;
}
const panel = {hidden:true,set innerHTML(value){this.html=value;questions=[]},get innerHTML(){return this.html}};
const elements = {
  '#reqClarifyPanel':panel,
  '#chatInput':{value:'',focus(){this.focused=true}},
  '#chatSend':{},'#chatStop':{},'#chatCreateDirect':{},'#reqComposerHint':{},
};
function all(selector) {
  if(selector==='#reqClarifyPanel .chat-question')return questions;
  let qs=questions;
  const match=/data-q="(\d+)"/.exec(selector);
  if(match)qs=qs.filter(q=>q.input.dataset.q===match[1]);
  return selector.includes('chat-other-input')?qs.map(q=>q.input):qs.flatMap(q=>q.buttons);
}
const ctx = {
  state:{},chatHistory:[],chatClarifySessionId:'',chatClarifyJobId:'',chatClarifyStopping:false,chatAbortController:null,
  $:s=>elements[s],$$:all,
  document:{querySelector:()=>questions[0]?.input},
  currentProject:()=>({id:'p'}),clarificationPolicy:()=>({threshold:80,dimensions:[]}),
  escapeHTML:s=>String(s),clarifyRepoNoticeHTML:()=>'',syncReqComposerLayout:()=>{},
  clarifyThinkingReset:()=>{},clarifyThinkingPush:()=>{},persistRequirementComposer:()=>{},setChatCreateEnabled:()=>{},
  selectedReqRepositoryId:()=>'',selectedReqRepositoryIds:()=>[],selectedReqBaseBranch:()=>'feature/existing',selectedClarifyModel:()=>'test',
  toast:text=>notices.push(text),AbortController,
  fetch:async (url,init)=>{requests.push({url,body:JSON.parse(init.body)});return {ok:true}},
  readChatClarifyResponse:async ()=>({parsed:{summary:'已明确',questions:[],ready:true}}),
};
vm.createContext(ctx);
for(const name of [
  'clarifyOptionLabel','clarifyQuestionOptions','composerSendLabel','setComposerRetryMode','composerClarifyQuestions',
  'syncComposerReplyState','composerClarifyRound','composerAssessmentView','composerProgressHTML','collectChatReply',
  'usableClarifySummary','bindClarifyOptions','renderClarifyPanel','normalizeClarifyParsed','parseClarifyPayload',
  'requirementComposerBusy','applyChatClarifyOutcome','sendChatClarify','newComposerJobId','finishComposerClarifyStream','setChatSendAnalyzing','projectTAPDReady',
]){
  const start=source.search(new RegExp('^(?:async )?function '+name+'\\(','m'));
  assert.ok(start>=0,name);
  const rest=source.slice(start),next=/\n(?:async )?function \w+\(/.exec(rest);
  vm.runInContext(next?rest.slice(0,next.index):rest,ctx);
}
const input=elements['#chatInput'],send=elements['#chatSend'],hint=elements['#reqComposerHint'];
function show(result) {
  ctx.state.chatClarifyResult=result;
  ctx.renderClarifyPanel(result);
  ctx.syncComposerReplyState();
}
(async()=>{
  ctx.syncComposerReplyState();
  assert.equal(send.disabled,true);
  const legacy={summary:'已读懂代码，需要确认修复目的和 ID 输入方式。',questions:[],ready:false,assessment:{missing:['修复目的是什么？','ID 怎么输入？']}};
  show(legacy);
  assert.equal((panel.html.match(/class="chat-other-input"/g)||[]).length,2);
  assert.ok(panel.html.includes('重新生成问题'));
  assert.ok(!panel.html.includes('chip chat-option'));
  assert.ok(!input.placeholder.includes('选项'));
  assert.equal(send.disabled,true);
  assert.equal(send.textContent,'提交回答并继续分析');
  await ctx.sendChatClarify();
  assert.equal(requests.length,0);
  assert.equal(notices.length,0);
  const choice={summary:'确认修复方式',ready:false,questions:[{id:'q1',question:'重新推送？',options:['是','否']}]};
  show(choice);
  assert.ok(panel.html.includes('aria-pressed="false"'));
  assert.ok(input.placeholder.includes('选项'));
  questions=[question('1. 重新推送？',['是','否']),question('2. 更新 retry？',['是','否'],1)];
  ctx.bindClarifyOptions();
  questions[0].buttons[0].onclick();
  questions[1].buttons[1].onclick();
  assert.equal(send.disabled,false);
  assert.equal(ctx.collectChatReply(),'重新推送？：是\n更新 retry？：否');
  questions[0].buttons[0].onclick();
  questions[1].buttons[1].onclick();
  assert.equal(send.disabled,true);
  questions[0].input.value='只修正本地状态';
  questions[0].input.oninput();
  assert.equal(send.disabled,false);
  questions[0].buttons[1].onclick();
  assert.equal(questions[0].input.value,'');
  assert.equal(ctx.collectChatReply(),'重新推送？：否');
  questions[0].input.value='需要进一步核实';
  questions[0].input.oninput();
  assert.equal(questions[0].buttons[1]['aria-pressed'],'false');
  assert.equal(ctx.collectChatReply(),'重新推送？：需要进一步核实');
  questions=[question('1. 更新字段？',['push_time','retry'],0,true)];
  ctx.bindClarifyOptions();
  questions[0].buttons.forEach(b=>b.onclick());
  assert.equal(ctx.collectChatReply(),'更新字段？：push_time、retry');
  show({summary:'填写路径',ready:false,questions:[{id:'q1',question:'文件在哪里？',options:[],freeText:true}]});
  assert.ok(panel.html.includes('placeholder="请填写回答"'));
  assert.ok(!input.placeholder.includes('选项'));
  questions=[question('1. 文件在哪里？',[])];
  ctx.bindClarifyOptions();
  questions[0].input.value='/tmp/ids.txt';
  questions[0].input.oninput();
  ctx.chatHistory=[{role:'user',content:'原始需求'}];
  await ctx.sendChatClarify();
  assert.equal(requests.at(-1).body.message,'文件在哪里？：/tmp/ids.txt');
  assert.equal(requests.at(-1).body.baseBranch,'feature/existing');
  assert.equal(input.value,'');
  assert.equal(send.disabled,true);
  assert.equal(send.textContent,'补充说明');
  assert.equal(ctx.chatHistory.filter(h=>h.role==='user').length,2);
  ctx.renderClarifyPanel(null,'未生成可回答的问题');
  assert.equal(send.disabled,false);
  assert.equal(send.textContent,'重新分析');
  input.value=ctx.chatHistory.filter(h=>h.role==='user').at(-1).content;
  ctx.syncComposerReplyState();
  assert.equal(send.textContent,'重新分析');
  const before=ctx.chatHistory.length;
  await ctx.sendChatClarify(true);
  assert.equal(requests.at(-1).body.message,'文件在哪里？：/tmp/ids.txt');
  assert.equal(ctx.chatHistory.length,before+1); // only the new assistant result
  ctx.renderClarifyPanel(null,'分析失败');
  input.value='补充：只修正本地状态';
  ctx.syncComposerReplyState();
  await ctx.sendChatClarify(true);
  assert.equal(requests.at(-1).body.message,'补充：只修正本地状态');
  ctx.chatAbortController=new AbortController();
  const count=requests.length;
  input.value='重复提交';
  await ctx.sendChatClarify();
  assert.equal(requests.length,count);
  ctx.chatAbortController=null;
  ctx.state.composerClarifyRetry=false;
  input.value='';
  show({...legacy,assessment:{}});
  assert.ok(panel.html.includes('重新生成问题'));
  assert.ok(hint.textContent.includes('重新生成问题'));
  assert.equal(send.disabled,true);
  console.log('clarification composer behavior passed');
})().catch(err=>{console.error(err);process.exitCode=1});
