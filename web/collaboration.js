/* Unified workbench: the existing assistant owns model calls and approvals. */
(() => {
  const assistant=window.AgentFlowAssistant;
  if(!assistant)return;
  const $id=id=>document.getElementById(id);
  const root=assistant.root;
  root.classList.add('ai-home');
  $id('collabAssistantHost').append(root);
  root.querySelector('.ai-head b').textContent='Main Bot · 协作助手';
  root.querySelector('[data-close]').hidden=true;
  const types={agent:'角色',issue:'需求',sop:'SOP',run:'运行',repository:'仓库'};
  const iconPaths={check:'<path d="m5 12 4 4L19 6"/>',alert:'<circle cx="12" cy="12" r="9"/><path d="M12 7v6m0 4h.01"/>',refresh:'<path d="M20 4v6h-6"/><path d="M20 10a8 8 0 1 0-2 8"/>',menu:'<path d="M4 6h16M4 12h16M4 18h16"/>',agent:'<circle cx="12" cy="8" r="3"/><path d="M5 21v-2a7 7 0 0 1 14 0v2"/>',issue:'<rect x="5" y="3" width="14" height="18" rx="2"/><path d="M9 8h6M9 12h6M9 16h4"/>',sop:'<rect x="3" y="3" width="6" height="6" rx="1"/><rect x="15" y="15" width="6" height="6" rx="1"/><path d="M6 9v9h9M9 6h9v9"/>',run:'<circle cx="12" cy="12" r="9"/><path d="m10 8 6 4-6 4z"/>',repository:'<path d="M3 7h7l2-3h9v16H3z"/>',attach:'<path d="m9 14 6-6a3 3 0 0 1 4 4l-8 8a5 5 0 0 1-7-7l9-9"/>',mention:'<circle cx="12" cy="12" r="4"/><path d="M16 8v7a2 2 0 0 0 4 0v-3a8 8 0 1 0-4 7"/>',chevron:'<path d="m9 5 7 7-7 7"/>',back:'<path d="m14 6-6 6 6 6"/>',search:'<circle cx="10" cy="10" r="6"/><path d="m15 15 5 5"/>'};
  function icon(kind){const node=document.createElementNS('http://www.w3.org/2000/svg','svg');node.setAttribute('viewBox','0 0 24 24');node.setAttribute('fill','none');node.setAttribute('stroke','currentColor');node.setAttribute('stroke-width','1.7');node.setAttribute('stroke-linecap','round');node.setAttribute('stroke-linejoin','round');node.setAttribute('aria-hidden','true');node.innerHTML=iconPaths[kind]||iconPaths.issue;return node}
  const categoryHints={agent:'咨询角色与职责',issue:'查找需求与验收条件',sop:'引用工作流程',run:'查看任务执行情况',repository:'添加代码仓库上下文'};
  let referenceType='',referenceQuery='',nextReferenceOffset=0;
  let ideVisible=false;
 const idePane=document.createElement('div');idePane.className='collab-ide-pane';idePane.hidden=true;$id('collabAssistantHost').after(idePane);
 function showIDE(show){ideVisible=show;idePane.hidden=!show; $id('collabAssistantHost').hidden=show;$id('collabNodePanel').hidden=true; const button=$id('collabToggleIDE');if(button){button.setAttribute('aria-pressed',String(show));button.replaceChildren(icon(show?'back':'repository'),document.createTextNode(show?'返回聊天':'切换 IDE'))}if(show&&idePane.dataset.run!==currentRun){idePane.replaceChildren();const frame=document.createElement('iframe');frame.title='当前任务 VS Code';frame.src=vscodeWorkbenchURL(currentRun,{embed:true});frame.setAttribute('allow','clipboard-read; clipboard-write');idePane.append(frame);idePane.dataset.run=currentRun}}
 let groupActivity=null,groupFollowup=null,groupStateTimer=0,groupStateEpoch=0,groupStateLoading=false;
 let currentRun='',run=null,nodeId='',nodeAttempt=0,sessions=[],references=[],attachments=[],referenceTimer=0,refreshTimer=0,requestGeneration=0,uploading=0,active=false,inspectorTab='artifacts',inspectorGeneration=0;
  // Cached projections only: the server owns handoff jobs and their lifetime.
  const sopReturns=new Map(),sopReturnRequests=new Set(),sopReturnLoads=new Map();
  function handoffActive(job){return ['submitting','pending','running'].includes(job?.status)}
  function sopReturnPending(){return handoffActive(sopReturns.get(currentRun))}
  function handoffPresentation(job){
    if(!job)return null;
    if(job.status==='failed'&&!job.id)return {tone:'failed',label:'切回 SOP 请求未完成',detail:job.error||'请重试提交',retry:'重新提交'};
    const failed=job.status==='failed',saved=!!job.summaryArtifactId||['starting','done'].includes(job.phase);
    if(failed)return {tone:'failed',label:saved?'交接已保存，SOP 未启动':'交接整理未完成',detail:job.error||'可以重试，已有群聊记录和文件保留',retry:saved?'重试继续':'重新整理'};
    if(job.status==='succeeded')return {tone:'idle',label:'交接已保存，SOP 已继续',detail:job.nodeName?'已从「'+job.nodeName+'」继续':'后续节点将读取本次交接摘要'};
    return {tone:'handoff',label:job.complete?'正在保存完成状态':({saving:'正在保存交接',starting:'交接已保存，正在继续 SOP'})[job.phase]||'Main Bot 正在整理交接',detail:'任务在后台继续，刷新或离开页面不影响处理'};
  }
  const input=assistant.input;
  const railToggle=document.createElement('button');railToggle.type='button';railToggle.className='button ghost small collab-rail-toggle';railToggle.append(icon('menu'));railToggle.title='导航与会话';railToggle.setAttribute('aria-expanded','false');
  document.querySelector('.topbar .page-context').before(railToggle);railToggle.setAttribute('aria-label','展开导航');
  railToggle.onclick=()=>{const open=document.body.classList.toggle('sidebar-open');railToggle.setAttribute('aria-expanded',String(open))};
  function closeRail(){document.body.classList.remove('sidebar-open');railToggle.setAttribute('aria-expanded','false')}
  const compose=root.querySelector('.ai-compose-box');
  const context=document.createElement('div');context.className='collab-composer-context';
  const picker=document.createElement('div');picker.className='collab-reference-picker';picker.hidden=true;picker.setAttribute('role','listbox');
  compose.prepend(context);compose.append(picker);
  function positionReferencePicker(){
    if(picker.hidden)return;
    const anchor=compose.getBoundingClientRect(),host=root.getBoundingClientRect(),viewport=window.visualViewport;
    const viewportTop=viewport?.offsetTop||0,viewportBottom=viewportTop+(viewport?.height||window.innerHeight);
    const top=Math.max(host.top,viewportTop,document.querySelector('.topbar')?.getBoundingClientRect().bottom||0)+8;
    const bottom=Math.min(host.bottom,viewportBottom)-8;
    const aboveBottom=Math.min(anchor.top-8,bottom),belowTop=Math.max(anchor.bottom+8,top);
    const above=Math.max(0,aboveBottom-top),below=Math.max(0,bottom-belowTop);
    const downward=above<160&&below>above;
    // A short visual viewport (for example with a mobile keyboard) may leave
    // no useful space beside the input. Keep the list inside the visible host.
    const overlap=Math.max(above,below)<Math.min(160,Math.max(0,bottom-top));
    const available=overlap?Math.max(0,bottom-top):(downward?below:above);
    picker.style.maxHeight=Math.min(380,available)+'px';picker.style.bottom='auto';
    const height=picker.getBoundingClientRect().height;
    const y=overlap?top:(downward?belowTop:aboveBottom-height);
    picker.style.top=(y-anchor.top-compose.clientTop)+'px';
  }
  let referenceLayoutFrame=0;
  function scheduleReferenceLayout(){
    if(picker.hidden||referenceLayoutFrame)return;
    referenceLayoutFrame=requestAnimationFrame(()=>{referenceLayoutFrame=0;positionReferencePicker()});
  }
  window.addEventListener('resize',scheduleReferenceLayout);
  document.addEventListener('scroll',e=>{if(!picker.contains(e.target))scheduleReferenceLayout()},true);
  window.visualViewport?.addEventListener('resize',scheduleReferenceLayout);
  window.visualViewport?.addEventListener('scroll',scheduleReferenceLayout);
  if(typeof ResizeObserver==='function'){
    const referenceResize=new ResizeObserver(scheduleReferenceLayout);
    referenceResize.observe(picker);referenceResize.observe(compose);
  }
  const tools=root.querySelector('.ai-compose-tools');
  const fileInput=document.createElement('input');fileInput.type='file';fileInput.multiple=true;fileInput.hidden=true;
  const attach=document.createElement('button');attach.type='button';attach.className='ai-tool-btn';attach.append(icon('attach'));attach.title='上传图片或文件（每个不超过 10 MB）';attach.setAttribute('aria-label','上传附件');
  const mention=document.createElement('button');mention.type='button';mention.className='ai-tool-btn';mention.append(icon('mention'));mention.title='引用角色、需求、SOP、运行或仓库';mention.setAttribute('aria-label','引用对象');
  attach.dataset.nodeContextTool='';mention.dataset.nodeContextTool='';tools.prepend(attach,mention,fileInput);
  function project(){return state.projectFilter||''}
  function busy(){return assistant.busy()||uploading>0}
  function error(err){toast(err.message||String(err))}
  function paintContext(){
    context.replaceChildren();
    for(const [items,isFile] of [[references,false],[attachments,true]])for(const item of items){
      if(!isFile&&item.type==='agent')continue;
      const chip=document.createElement('span');chip.className='collab-chip';
      const label=document.createElement('span'),name=document.createElement('span');name.dataset.i18nSkip='';name.textContent=item.name;
      if(!isFile)label.append(document.createTextNode((types[item.type]||item.type)+' · '));label.append(name);chip.append(icon(isFile?'attach':item.type));
      const remove=document.createElement('button');remove.type='button';remove.textContent='×';remove.setAttribute('aria-label','移除 '+item.name);
      remove.onclick=()=>{if(assistant.busy())return;if(isFile)attachments=attachments.filter(a=>a.id!==item.id);else references=references.filter(a=>!(a.id===item.id&&a.type===item.type));paintContext()};
      chip.append(label,remove);context.append(chip);
    }
    if(uploading){const note=document.createElement('span');note.textContent='正在上传…';context.append(note)}
    attach.disabled=busy();mention.disabled=!!nodeId;assistant.sync();paintMentionText();
    if(uploading)root.querySelector('[type=submit]').disabled=true;
  }
  function addReference(item){
    if(isGroup()&&item.type==='agent'&&!groupMemberIDs().includes(item.id))return toast('请先邀请角色加入群');
    if(currentRun&&run?.workflowSnapshot?.collaborationMode!=='group'&&item.type==='agent'&&!run?.workflowSnapshot?.nodes?.some(n=>n.agentId===item.id))return toast('只能咨询当前 SOP 的角色');
    if(references.length>=12)return toast('最多引用 12 个对象');
    if(run?.workflowSnapshot?.collaborationMode!=='group'&&item.type==='agent'&&references.some(r=>r.type==='agent'&&r.id!==item.id))return toast('一次咨询一个角色，请先移除已选角色');
    if(!references.some(r=>r.type===item.type&&r.id===item.id))references.push(item);
    const caret=input.selectionStart??input.value.length,before=input.value.slice(0,caret),match=before.match(/@([^@\n]*)$/);const completed=match&&references.some(r=>r.type==='agent'&&match[1].startsWith(r.name+' '));const start=match&&!completed?match.index:caret;input.setRangeText('@'+item.name+' ',start,caret,'end');
    picker.hidden=true;paintContext();paintMentionText();input.focus();
  }
  function parseReferenceQuery(raw){
    for(const [type,label] of Object.entries(types)){const match=raw.match(new RegExp('^'+label+'(?:\\s+|$)','i'));if(match)return {type,q:raw.slice(match[0].length).trim()}}
    return {type:'',q:raw.trim()};
  }
  function referenceHeader(){
    const header=document.createElement('div');header.className='collab-reference-header';
    const back=document.createElement('button');back.type='button';back.className='collab-reference-back';back.title='所有类别';back.setAttribute('aria-label','所有类别');back.append(icon('back'));back.onclick=()=>{referenceType='';referenceQuery='';showReferenceCategories()};
    const search=document.createElement('input');search.className='collab-reference-search';search.type='search';search.placeholder=referenceType?'搜索'+types[referenceType]+'…':'搜索所有对象…';search.setAttribute('aria-label',search.placeholder);search.value=referenceQuery;
    search.oninput=()=>{referenceQuery=search.value;clearTimeout(referenceTimer);referenceTimer=setTimeout(()=>loadReferences(referenceQuery,referenceType,0,true),180)};
    header.append(back,icon('search'),search);return header;
  }
  function showReferenceCategories(){
    requestGeneration++;referenceType='';referenceQuery='';picker.replaceChildren(referenceHeader());
    const list=document.createElement('div');list.className='collab-reference-options';list.setAttribute('role','listbox');
    for(const [type,label] of Object.entries(currentRun?{agent:types.agent}:types)){const b=document.createElement('button');b.type='button';b.setAttribute('role','option');const text=document.createElement('span');const name=document.createElement('b');name.textContent=label;const hint=document.createElement('small');hint.textContent=categoryHints[type];text.append(name,hint);b.append(icon(type),text,icon('chevron'));b.onclick=()=>{referenceType=type;referenceQuery='';loadReferences('',type,0,true)};list.append(b)}
    picker.append(list);picker.hidden=false;positionReferencePicker();
  }
  async function loadReferences(q='',type='',offset=0,focusSearch=false){
    const generation=++requestGeneration, scope=project();referenceType=type;referenceQuery=q;
    const hadSearchFocus=picker.contains(document.activeElement)&&document.activeElement.classList.contains('collab-reference-search');
    const selection=hadSearchFocus?document.activeElement.selectionStart:null;
    try{
      const data=await api('/api/assistant/references?'+new URLSearchParams({projectId:scope,scope:currentRun?'task':'all',runId:currentRun,q,type,offset:String(offset)}),{silent:true});
      if(generation!==requestGeneration||scope!==project())return;
      const items=Array.isArray(data)?data:data.items;
      if(!offset)picker.replaceChildren(referenceHeader());else picker.querySelector('.collab-reference-more')?.remove();
      let list=picker.querySelector('.collab-reference-options');if(!list){list=document.createElement('div');list.className='collab-reference-options';list.setAttribute('role','listbox');picker.append(list)}
      if(!items.length&&!offset){const note=document.createElement('p');note.textContent='没有匹配的对象';list.append(note)}
      for(const item of items){const b=document.createElement('button');b.type='button';b.setAttribute('role','option');const text=document.createElement('span');const name=document.createElement('b');name.dataset.i18nSkip='';name.textContent=item.name;const detail=document.createElement('small');detail.textContent=[types[item.type],item.projectName||'共享',item.id].join(' · ');text.append(name,detail);b.append(icon(item.type),text);b.onclick=()=>addReference(item);list.append(b)}
      nextReferenceOffset=data.nextOffset||0;
      if(nextReferenceOffset){const more=document.createElement('button');more.type='button';more.className='collab-reference-more';more.textContent='加载更多';more.onclick=()=>loadReferences(q,type,nextReferenceOffset);picker.append(more)}
      picker.hidden=false;positionReferencePicker();
      if(focusSearch||hadSearchFocus){const search=picker.querySelector('input');search.focus();if(selection!==null)search.setSelectionRange(selection,selection)}
    }catch(err){error(err)}
  }
  mention.onclick=()=>{showReferenceCategories();picker.querySelector('input').focus()};
  root.addEventListener('keydown',e=>{if(e.key==='Escape'&&!picker.hidden){requestGeneration++;picker.hidden=true;input.focus();e.stopPropagation()}});
  input.addEventListener('input',()=>{clearTimeout(referenceTimer);references=references.filter(r=>r.type!=='agent'||input.value.includes('@'+r.name));paintMentionText();const match=input.value.slice(0,input.selectionStart).match(/@([^@\n]*)$/);if(match&&!references.some(r=>r.type==='agent'&&match[1].startsWith(r.name)))referenceTimer=setTimeout(()=>{const raw=match[1].trim();if(!raw){showReferenceCategories();return}const {type,q}=parseReferenceQuery(raw);loadReferences(q,type)},180);else{requestGeneration++;picker.hidden=true}});
  input.addEventListener('keydown',e=>{if(e.key==='Escape'){requestGeneration++;picker.hidden=true}if(!picker.hidden&&(e.key==='ArrowDown'||e.key==='ArrowUp')){e.preventDefault();picker.querySelector('button')?.focus()}});
  picker.addEventListener('keydown',e=>{if(e.target.tagName==='INPUT'&&e.key==='Enter'){e.preventDefault();picker.querySelector('.collab-reference-options [role=option]')?.click();return}if(e.target.tagName==='INPUT'&&e.key!=='ArrowDown'&&e.key!=='Escape')return;const buttons=[...picker.querySelectorAll('button')],i=buttons.indexOf(document.activeElement);if(e.key==='Escape'){picker.hidden=true;input.focus()}if(e.key==='ArrowDown'||e.key==='ArrowUp'){e.preventDefault();buttons[(i+(e.key==='ArrowDown'?1:-1)+buttons.length)%buttons.length]?.focus()}});
  document.addEventListener('click',e=>{if(!compose.contains(e.target)){requestGeneration++;picker.hidden=true}});

  const inputWrap=document.createElement('div');inputWrap.className='collab-input-wrap';input.before(inputWrap);inputWrap.append(input);
  const mentionText=document.createElement('div');mentionText.className='collab-input-text';mentionText.setAttribute('aria-hidden','true');inputWrap.prepend(mentionText);
  let composingMention=false;
  function paintMentionText(){
    const selected=references.filter(r=>r.type==='agent'&&input.value.includes('@'+r.name));if(isGroup()&&input.value.includes('@Main Bot'))selected.push({name:'Main Bot'});
    inputWrap.classList.toggle('has-mentions',selected.length>0&&!composingMention);mentionText.replaceChildren();
    const style=getComputedStyle(input);for(const key of ['fontFamily','fontSize','fontWeight','fontStyle','lineHeight','letterSpacing','wordSpacing','textIndent','textTransform','tabSize','paddingTop','paddingRight','paddingBottom','paddingLeft','borderTopWidth','borderRightWidth','borderBottomWidth','borderLeftWidth','boxSizing'])mentionText.style[key]=style[key];mentionText.style.width=input.clientWidth+'px';mentionText.style.height=input.clientHeight+'px';
    if(!selected.length)return;
    const tokens=selected.map(r=>'@'+r.name).sort((a,b)=>b.length-a.length);let pos=0;
    while(pos<input.value.length){const token=tokens.find(t=>input.value.startsWith(t,pos));if(token){const mark=document.createElement('span');mark.className='collab-inline-mention';mark.textContent=token;mentionText.append(mark);pos+=token.length}else{let end=pos+1;while(end<input.value.length&&!tokens.some(t=>input.value.startsWith(t,end)))end++;mentionText.append(document.createTextNode(input.value.slice(pos,end)));pos=end}}
    mentionText.append(document.createTextNode('\n'));mentionText.scrollTop=input.scrollTop;mentionText.scrollLeft=input.scrollLeft;
  }
  input.addEventListener('scroll',()=>{mentionText.scrollTop=input.scrollTop});
  input.addEventListener('compositionstart',()=>{composingMention=true;paintMentionText()});input.addEventListener('compositionend',()=>{composingMention=false;paintMentionText()});
  new ResizeObserver(()=>{mentionText.style.height=input.offsetHeight+'px';paintMentionText()}).observe(input);
  let membersOpen=true;
  const membersPane=document.createElement('aside');membersPane.className='collab-members';membersPane.hidden=true;document.querySelector('.collaboration-shell').append(membersPane);
  const modeButton=document.createElement('button');modeButton.type='button';modeButton.className='button small collab-convert';modeButton.hidden=true;$id('collabInspect').before(modeButton);
  const groupPause=document.createElement('button');groupPause.type='button';groupPause.className='button small collab-convert';groupPause.hidden=true;$id('collabInspect').before(groupPause);groupPause.onclick=async()=>{const id=currentRun;try{groupPause.disabled=true;await api('/api/runs/'+encodeURIComponent(id)+'/'+(run.pauseRequested?'resume':'pause'),{method:'POST',body:'{}'});await refresh()}catch(err){error(err)}finally{groupPause.disabled=false}};
  const membersButton=document.createElement('button');membersButton.type='button';membersButton.className='button small collab-members-toggle';membersButton.hidden=true;membersButton.textContent='成员';membersButton.onclick=()=>{membersOpen=!membersOpen;paintMembers()};$id('collabInspect').before(membersButton);
  function groupMemberIDs(){return run?.workflowSnapshot?.groupMembers||[...new Set((run?.workflowSnapshot?.nodes||[]).map(n=>n.agentId).filter(Boolean))]}
  function groupLoopStatus(activity,followup,working){
    if(!activity)return {label:'正在同步当前分工',detail:'成员的实际进展会显示在这里',tone:'waiting'};
    const members=activity?.members||[],running=members.filter(m=>m.status==='running').length,waiting=members.filter(m=>m.status==='waiting').length,failed=members.filter(m=>m.status==='failed').length;
    if(working||followup?.status==='running')return {label:'Main Bot 正在跟进',detail:'继续输入，随时补充或调整方向',tone:'running'};
    if(running)return {label:running+' 位成员正在处理',detail:waiting?waiting+' 项工作等待接力':'进展会同步到群里',tone:'running'};
    if(waiting)return {label:waiting+' 项工作等待处理',detail:'成员接到分工后会在这里回应',tone:'waiting'};
    if(followup?.pending&&followup?.status==='watching')return {label:'等待 Main Bot 跟进',detail:'正在汇总刚完成的成员结果',tone:'waiting'};
    if(followup?.status==='blocked'||followup?.status==='exhausted')return {label:'等待你的下一步指令',detail:followup.error||'可以继续补充目标或调整方向',tone:'waiting'};
    if(failed)return {label:'有 '+failed+' 项工作需要处理',detail:'查看成员反馈，继续讨论解决办法',tone:'failed'};
    return {label:'当前分工已结束',detail:'可以继续讨论、补充修改或发起下一轮',tone:'idle'};
  }
  function groupVerificationLabel(verification){
    if(verification?.status==='passed')return (verification.checks?.filter(c=>!c.historical&&c.status==='passed').length||1)+' 项检查通过';
    return ({passed:'检查通过',failed:'检查未通过',running:'正在检查',historical:'有历史检查，当前修改待验证',unverified:'尚无检查记录'})[verification?.status]||'尚无检查记录';
  }
  function scheduleGroupState(){
    if(!isGroup()||groupStateTimer)return;
    groupStateTimer=setTimeout(()=>{groupStateTimer=0;loadGroupState()},400);
  }
  async function loadGroupState(){
    if(!currentRun||!isGroup()||groupStateLoading)return;
    const id=currentRun,epoch=groupStateEpoch;groupStateLoading=true;
    try{
      const results=await Promise.allSettled([api('/api/runs/'+encodeURIComponent(id)+'/group/activity',{silent:true}),api('/api/runs/'+encodeURIComponent(id)+'/group/followup',{silent:true})]);
      if(epoch!==groupStateEpoch||currentRun!==id||!isGroup())return;
      if(results[0].status==='fulfilled')groupActivity=results[0].value;
      if(results[1].status==='fulfilled')groupFollowup=results[1].value;
      paintMembers();paintGroupRibbon();paintGroupFeed();
    }finally{groupStateLoading=false}
  }
  function revealGroupProcess(id){
    const log=root.querySelector('.ai-messages'),card=[...log.querySelectorAll('[data-group-node]')].find(c=>c.dataset.groupNode===id);
    if(!card)return toast('这项工作的执行过程尚未加载');
    const process=card.querySelector('.collab-chat-process');if(process)process.open=true;card.scrollIntoView({block:'center',behavior:'smooth'});
  }
  function memberActivityLabel(work,fallback){
    if(!work?.activity)return fallback;
    const tool=work.activity.match(/^正在使用 (.+)$/)?.[1]?.toLowerCase();
    if(!tool)return work.activity;
    if(['read','glob','grep'].includes(tool))return '正在查看文件';
    if(['write','edit','multiedit'].includes(tool))return '正在修改文件';
    if(['bash','shell','exec'].includes(tool))return '正在执行命令';
    if(tool==='websearch'||tool==='webfetch')return '正在查阅资料';
    return '正在处理任务';
  }
  function paintMembers(){
    membersButton.hidden=!isGroup();membersPane.hidden=!isGroup()||!membersOpen;membersButton.setAttribute('aria-expanded',String(!membersPane.hidden));membersPane.replaceChildren();if(membersPane.hidden)return;
    const head=document.createElement('header'),title=document.createElement('b'),close=document.createElement('button');title.textContent='一起协作 · '+(groupMemberIDs().length+1);close.type='button';close.className='collab-detail-link';close.textContent='收起';close.onclick=()=>{membersOpen=false;paintMembers()};head.append(title,close);membersPane.append(head);
    const caption=document.createElement('p');caption.className='collab-team-caption';caption.textContent='每个人的进展，都在同一个任务里';membersPane.append(caption);
    function member(id,name,detail,work){
      const wrap=document.createElement('section');wrap.className='collab-member-card '+(work?.status||'idle');
      const row=document.createElement('button');row.type='button';row.className='collab-member';
      const avatar=document.createElement('span');avatar.className='ai-speaker-avatar';avatar.textContent=id==='pm'?'MB':name.slice(0,1);
      const label=document.createElement('span'),b=document.createElement('b'),small=document.createElement('small');label.className='collab-member-copy';b.textContent=name;small.textContent=detail;label.append(b,small);
      const dot=document.createElement('span');dot.className='collab-presence '+(work?.status||'idle');row.append(avatar,label,dot);row.title='@'+name;row.setAttribute('aria-label','@'+name);
      row.onclick=()=>{if(id==='pm'){input.setRangeText('@Main Bot ',input.selectionStart,input.selectionEnd,'end');paintMentionText();input.focus()}else addReference({type:'agent',id,name,projectId:project()})};wrap.append(row);
      if(work?.task){const task=document.createElement('p');task.className='collab-member-task';task.textContent=work.task;task.title=work.task;wrap.append(task)}
      if(work?.nodeId){const inspect=document.createElement('button');inspect.type='button';inspect.className='collab-member-process';inspect.textContent='查看过程';inspect.onclick=()=>revealGroupProcess(work.nodeId);wrap.append(inspect)}
      membersPane.append(wrap);
    }
    const botBusy=assistant.generating?.()||groupFollowup?.status==='running',botWaiting=groupFollowup?.pending&&groupFollowup?.status==='watching';
    member('pm','Main Bot',botBusy?'正在跟进':botWaiting?'等待成员结果':'任务协调与直接修改',{status:botBusy?'running':botWaiting?'waiting':'idle'});
    for(const id of groupMemberIDs()){const a=state.data?.agents?.find(a=>a.id===id),work=groupActivity?.members?.find(m=>m.agentId===id);const status=({running:'正在处理',waiting:'等待接力',failed:'需要处理',idle:'空闲'})[work?.status]||'正在同步状态';member(id,a?.name||id,memberActivityLabel(work,status),work)}
    const invite=document.createElement('button');invite.type='button';invite.className='button small collab-invite';invite.append(icon('agent'),document.createTextNode('邀请角色'));invite.onclick=inviteMemberDialog;invite.disabled=sopReturnPending();membersPane.append(invite);
    if(groupFollowup?.error){const note=document.createElement('p');note.className='collab-followup-error';note.textContent='自动跟进暂未完成：'+groupFollowup.error;membersPane.append(note)}
  }
  function paintGroupRibbon(){
    const bar=$id('collabPipeline').querySelector('.collab-loop-ribbon');if(!bar)return;
    const job=sopReturns.get(currentRun),progress=job?.status!=='succeeded'?handoffPresentation(job):null,state=groupLoopStatus(groupActivity,groupFollowup,!!assistant.generating?.());
    bar.dataset.status=progress?.tone||(run.pauseRequested?'paused':state.tone);
    const label=bar.querySelector('b'),detail=bar.querySelector('small');
    label.textContent=progress?.label||(run.pauseRequested?'协作已暂停':state.label);
    detail.textContent=progress?.detail||(run.pauseRequested?'继续协作后，成员会接着处理':state.detail);
  }
  function handoffRibbon(){
    const bar=document.createElement('div');bar.className='collab-loop-ribbon';bar.setAttribute('role','status');
    const mark=document.createElement('span');mark.className='collab-loop-mark';mark.append(icon('agent'));
    const copy=document.createElement('div');copy.className='collab-loop-copy';copy.append(document.createElement('b'),document.createElement('small'));bar.append(mark,copy);
    const actions=document.createElement('div');actions.className='collab-loop-actions';
    const job=sopReturns.get(currentRun);
    if(job?.status==='failed'){const retry=document.createElement('button');retry.type='button';retry.className='button ghost small';retry.textContent=handoffPresentation(job).retry;retry.onclick=()=>job.id?retrySOPHandoff(currentRun):restoreSOPDialog();actions.append(retry)}
    else if(!sopReturnPending()&&isGroup()&&(run.status==='running'||run.pauseRequested)){const pause=document.createElement('button');pause.type='button';pause.className='button ghost small';pause.textContent=run.pauseRequested?'继续协作':'暂停';pause.onclick=async()=>{try{pause.disabled=true;await api('/api/runs/'+encodeURIComponent(currentRun)+'/'+(run.pauseRequested?'resume':'pause'),{method:'POST',body:'{}'});await refresh()}catch(err){error(err)}finally{pause.disabled=false}};actions.append(pause)}
    const detail=document.createElement('button');detail.type='button';detail.className='collab-detail-link';detail.textContent='运行详情';detail.onclick=()=>openRunFromId(currentRun);actions.append(detail);bar.append(actions);return bar;
  }
  function inviteMemberDialog(){
    const choices=(state.data?.agents||[]).filter(a=>(a.projectId===project()||a.shared)&&!groupMemberIDs().includes(a.id));
    const dialog=document.createElement('dialog');dialog.className='collab-group-dialog';const form=document.createElement('form'),heading=document.createElement('h2');heading.textContent='邀请角色加入协作';form.append(heading);const select=document.createElement('select');select.setAttribute('aria-label','选择邀请角色');for(const a of choices){const opt=document.createElement('option');opt.value=a.id;opt.textContent=a.name;select.append(opt)}form.append(select);const note=document.createElement('p');note.textContent=choices.length?'加入后可以 @ 和派发任务，邀请本身不会开始执行。':'所有可用角色都已加入。';form.append(note);const footer=document.createElement('footer'),cancel=document.createElement('button'),submit=document.createElement('button');cancel.type='button';cancel.className='button ghost';cancel.textContent='取消';cancel.onclick=()=>dialog.close();submit.type='submit';submit.className='button';submit.textContent='邀请加入';submit.disabled=!choices.length;footer.append(cancel,submit);form.append(footer);dialog.append(form);document.body.append(dialog);dismissDialog(dialog);const id=currentRun;form.onsubmit=async e=>{e.preventDefault();submit.disabled=true;try{await api('/api/runs/'+encodeURIComponent(id)+'/group/members',{method:'POST',body:JSON.stringify({agentId:select.value})});dialog.close();await refresh()}catch(err){note.textContent=err.message;submit.disabled=false}};dialog.showModal();
  }
  function dismissDialog(dialog){dialog.addEventListener('close',()=>dialog.remove());dialog.addEventListener('click',e=>{if(e.target===dialog){const r=dialog.getBoundingClientRect();if(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom)dialog.close()}})}
  function paintModeButton(){groupPause.hidden=true;groupPause.textContent=run?.pauseRequested?'继续协作':'暂停协作';modeButton.hidden=!isGroup()||['succeeded','cancelled'].includes(run?.status);modeButton.textContent=run?.workflowSnapshot?.sopCheckpoint?.workflow?.nodes?.length===0?'标记完成':'切回 SOP';modeButton.disabled=sopReturnPending();modeButton.onclick=restoreSOPDialog}
  function cacheSOPHandoff(id,job){
    const previous=sopReturns.get(id);if(job)sopReturns.set(id,job);else if(previous?.id||previous?.status!=='failed')sopReturns.delete(id);
    if(currentRun===id&&job?.status==='succeeded'&&(handoffActive(previous)||previous?.status==='failed'&&previous?.phase==='starting')){nodeId='';assistant.leaveNode()}
  }
  async function loadSOPHandoff(id){
    if(!id)return;
    const epoch=(sopReturnLoads.get(id)||0)+1;sopReturnLoads.set(id,epoch);
    try{
      const data=await api('/api/runs/'+encodeURIComponent(id)+'/group/sop/handoff',{silent:true});
      if(sopReturnLoads.get(id)!==epoch||sopReturnRequests.has(id))return;
      cacheSOPHandoff(id,data.job||null);
    }catch{ /* Keep the last known server state while offline; the next poll retries. */ }
  }
  async function refreshSOPHandoffs(){
    const scope=project(),epochs=new Map(sopReturnLoads);
    const results=await Promise.allSettled([
      api('/api/runs/group/sop/handoffs?'+new URLSearchParams({projectId:scope}),{silent:true}),
      loadSOPHandoff(currentRun)
    ]);
    if(scope!==project()||results[0].status!=='fulfilled')return;
    const jobs=new Map((results[0].value.jobs||[]).map(job=>[job.runId,job]));
    for(const row of taskRows){
      if(sopReturnRequests.has(row.id)||sopReturnLoads.get(row.id)!==epochs.get(row.id))continue;
      cacheSOPHandoff(row.id,jobs.get(row.id)||null);
    }
  }
  async function submitSOPHandoff(id,body,title,retry=false){
    if(handoffActive(sopReturns.get(id))||sopReturnRequests.has(id))return;
    const scope=project(),previous=sopReturns.get(id);
    sopReturnLoads.set(id,(sopReturnLoads.get(id)||0)+1);sopReturnRequests.add(id);sopReturns.set(id,{...previous,status:'submitting',phase:retry?previous?.phase:'summarizing',complete:!!body.complete});renderPipeline();paintTasks();
    try{
      const data=await api('/api/runs/'+encodeURIComponent(id)+'/group/sop'+(retry?'/handoff/retry':''),{method:'POST',body:JSON.stringify(body),silent:true});
      cacheSOPHandoff(id,data.job||null);
      if(!data.job){if(currentRun===id){nodeId='';assistant.leaveNode()}toast('「'+title+'」'+(body.complete?'已标记完成':'交接已保存，SOP 已继续'))}
      else if(currentRun!==id)toast('「'+title+'」正在后台整理交接');
    }catch(err){
      sopReturns.set(id,{...previous,status:'failed',phase:previous?.phase||'summarizing',error:err.message||String(err)});
      toast('「'+title+'」请求未完成，正在核对后台交接状态');
    }finally{
      sopReturnRequests.delete(id);await loadSOPHandoff(id);
      if(scope===project())await refresh();
      if(currentRun===id)renderPipeline();paintTasks();
    }
  }
  async function returnToSOP(id,body,title){return submitSOPHandoff(id,body,title)}
  async function retrySOPHandoff(id){return submitSOPHandoff(id,{},taskTitle(run),true)}
  function restoreSOPDialog(){
    if(sopReturnPending())return;
    if(Object.values(run.nodes||{}).some(n=>n.status==='running')||run.status==='running')return toast('请先停止当前角色任务，再切回 SOP');
    const original=run.workflowSnapshot.sopCheckpoint?.workflow||run.workflowSnapshot,nodes=original.nodes.filter(n=>!n.requestId);
    const dialog=document.createElement('dialog');dialog.className='collab-group-dialog';
    const form=document.createElement('form'),heading=document.createElement('h2');heading.textContent=nodes.length?'切回 SOP':'完成协作任务';form.append(heading);
    const actionLabel=document.createElement('label');actionLabel.textContent='切回后如何处理';
    const action=document.createElement('select');action.setAttribute('aria-label','SOP 恢复方式');
    for(const [value,text] of [...(nodes.length?[['restart','从节点重新开始']]:[]),['complete','直接标记完成']]){const option=document.createElement('option');option.value=value;option.textContent=text;action.append(option)}actionLabel.append(action);actionLabel.hidden=!nodes.length;form.append(actionLabel);
    const label=document.createElement('label');label.textContent='从哪个节点继续';const select=document.createElement('select');select.setAttribute('aria-label','SOP 继续节点');
    for(const n of nodes){const o=document.createElement('option');o.value=n.id;o.textContent=n.name||n.id;select.append(o)}
    const checkpoint=run.workflowSnapshot.sopCheckpoint?.nodes||run.nodes;
    const next=nodes.find(n=>checkpoint?.[n.id]?.status!=='succeeded');if(next)select.value=next.id;label.append(select);form.append(label);
    const note=document.createElement('p');form.append(note);
    const footer=document.createElement('footer'),cancel=document.createElement('button'),submit=document.createElement('button');cancel.type='button';cancel.className='button ghost';cancel.textContent='取消';cancel.onclick=()=>dialog.close();submit.type='submit';submit.className='button';footer.append(cancel,submit);form.append(footer);
    function paintAction(){const complete=action.value==='complete';label.hidden=complete;select.disabled=complete;submit.disabled=!complete&&!nodes.length;submit.textContent=complete?'标记完成':'切回 SOP';note.textContent=complete?'确认本次自由协作已完成，结束运行并将关联需求标记为已完成，不再执行 SOP 节点。已有文件、平台变更和群内记录保留。':'恢复原 SOP 顺序，从所选节点自动继续。已交付闭环的需求将重新变为进行中。已有文件和平台变更不会回退；群内记录保留。Main Bot 会先整理群聊交接摘要并归档，后续节点自动读取。交接保存成功后自动执行；整理期间可以处理其他任务。'}
    action.onchange=paintAction;paintAction();dialog.append(form);document.body.append(dialog);dismissDialog(dialog);const id=currentRun;
    form.onsubmit=e=>{e.preventDefault();submit.disabled=true;const body=action.value==='complete'?{complete:true}:{nodeId:select.value};const title=taskTitle(run);dialog.close();void returnToSOP(id,body,title)};dialog.showModal();
  }

  attach.onclick=()=>fileInput.click();
  async function upload(files){
    if(busy())return;
    const scope=project();
    for(const file of [...files]){
      if(attachments.length>=8){toast('每条消息最多 8 个附件');break}
      if(file.size>10*1024*1024){toast(file.name+' 超过 10 MB');continue}
      uploading++;paintContext();
      try{const form=new FormData();form.append('file',file);form.append('projectId',scope);const res=await fetch('/api/assistant/attachments',{method:'POST',body:form});const data=await res.json();if(!res.ok)throw new Error(data.error||'上传失败');if(scope===project())attachments.push(data)}catch(err){error(err)}finally{uploading--;paintContext()}
    }
  }
  fileInput.onchange=()=>{upload(fileInput.files);fileInput.value=''};
  input.addEventListener('paste',e=>{const files=[...(e.clipboardData?.items||[])].filter(i=>i.kind==='file').map(i=>i.getAsFile()).filter(Boolean);if(files.length){e.preventDefault();upload(files)}});
  compose.addEventListener('dragover',e=>{if(e.dataTransfer?.types.includes('Files')){e.preventDefault();compose.classList.add('is-dragging')}});
  compose.addEventListener('dragleave',()=>compose.classList.remove('is-dragging'));
  compose.addEventListener('drop',e=>{e.preventDefault();compose.classList.remove('is-dragging');if(e.dataTransfer?.files.length)upload(e.dataTransfer.files)});
  // Block submitting while the attachment request is in flight.
  root.querySelector('form').addEventListener('submit',e=>{if(uploading){e.preventDefault();e.stopImmediatePropagation()}},true);
  function renderMessageContext(el,message){
    const bubble=el.querySelector('.ai-user-bubble')||el;
    const roles=(message.references||[]).filter(r=>r.type==='agent');
    if(roles.length){
      bubble.replaceChildren();const tokens=roles.map(r=>({text:'@'+r.name,ref:r})).sort((a,b)=>b.text.length-a.text.length);const text=message.content||'';let pos=0;
      while(pos<text.length){const token=tokens.find(t=>text.startsWith(t.text,pos));if(token){const span=document.createElement('span');span.className='collab-inline-mention';span.textContent=token.text;bubble.append(span);pos+=token.text.length}else{let end=pos+1;while(end<text.length&&!tokens.some(t=>text.startsWith(t.text,end)))end++;bubble.append(document.createTextNode(text.slice(pos,end)));pos=end}}
      for(const r of roles)if(!text.includes('@'+r.name)){const span=document.createElement('span');span.className='collab-inline-mention';span.textContent=' @'+r.name;bubble.prepend(span)}
    }
    const bar=document.createElement('div');bar.className='collab-message-context';
    for(const ref of message.references||[]){if(ref.type==='agent')continue;const b=document.createElement('button');b.type='button';b.className='collab-chip';b.textContent=(types[ref.type]||ref.type)+' · '+ref.name;b.onclick=()=>{if(ref.type==='run')selectRun(ref.id);else{addReference(ref);input.focus()}};bar.append(b)}
    for(const a of message.attachments||[]){const link=document.createElement('a');link.className='collab-chip';link.href='/api/assistant/attachments/'+encodeURIComponent(a.id)+'?'+new URLSearchParams({sessionId:assistant.sessionId()});link.textContent='📎 '+a.name;link.target='_blank';link.rel='noopener';if(/^image\/(png|jpeg|gif|webp)$/.test(a.mimeType)){const img=document.createElement('img');img.src=link.href;img.alt=a.name;img.loading='lazy';link.prepend(img)}bar.append(link)}
    if(bar.childNodes.length)bubble.append(bar);
  }
  let taskRows=[],taskLimit=30,taskRefreshGeneration=0;
  const sessionMenu=document.createElement('div');sessionMenu.className='collab-session-menu';sessionMenu.hidden=true;sessionMenu.setAttribute('role','menu');document.body.append(sessionMenu);
  function closeSessionMenu(){sessionMenu.hidden=true}
  document.addEventListener('click',e=>{if(!sessionMenu.contains(e.target))closeSessionMenu()});window.addEventListener('resize',closeSessionMenu);document.addEventListener('scroll',closeSessionMenu,true);
  sessionMenu.addEventListener('keydown',e=>{if(e.key==='Escape'){closeSessionMenu();return}const buttons=[...sessionMenu.querySelectorAll('button')];if(e.key==='ArrowDown'||e.key==='ArrowUp'){e.preventDefault();const i=buttons.indexOf(document.activeElement);buttons[(i+(e.key==='ArrowDown'?1:-1)+buttons.length)%buttons.length]?.focus()}});
  function openSessionMenu(session,x,y){
    if(session.shared){toast('共享 SOP 会话随任务运行保留');return}sessionMenu.replaceChildren();const action=(label,fn,danger=false)=>{const b=document.createElement('button');b.type='button';b.setAttribute('role','menuitem');b.textContent=label;if(danger)b.classList.add('danger');b.onclick=async()=>{closeSessionMenu();try{await fn()}catch(err){error(err)}};sessionMenu.append(b)};
    action(session.archived?'恢复会话':'归档会话',async()=>{await api('/api/assistant/sessions/'+encodeURIComponent(session.id)+'/archive',{method:'POST',body:JSON.stringify({archived:!session.archived})});if(assistant.sessionId()===session.id&&!session.archived)assistant.reset();await refresh()});
    action('删除会话',async()=>{if(!(await afConfirm({title:'删除会话',message:'确定永久删除「'+(session.title||'新对话')+'」？此操作无法撤销。',danger:true,confirmText:'删除'})))return;await api('/api/assistant/sessions/'+encodeURIComponent(session.id),{method:'DELETE'});if(assistant.sessionId()===session.id)assistant.reset();await refresh()},true);
    sessionMenu.hidden=false;sessionMenu.style.left=Math.max(8,Math.min(x,innerWidth-190))+'px';sessionMenu.style.top=Math.max(8,Math.min(y,innerHeight-100))+'px';sessionMenu.querySelector('button').focus();
  }
  const localSessionStates=new Map();
  function paintSessions(){
    const host=$id('collabSessions');host.replaceChildren();const archived=$id('collabSessionFilter').value==='archived';const rows=sessions.filter(s=>!s.shared&&!s.runId&&!!s.archived===archived);
    for(const s of rows){const row=document.createElement('div');row.className='collab-session-row'+(active&&assistant.sessionId()===s.id?' active':'');const b=document.createElement('button');b.type='button';b.className='collab-list-item'+(assistant.sessionId()===s.id?' active':'');b.textContent=s.title||'新对话';b.onclick=()=>{if(busy())return toast('正在保存或上传，请稍后切换');switchView('collaboration');closeRail();assistant.openSession(s.id)};const more=document.createElement('button');more.type='button';more.className='collab-session-more';more.setAttribute('aria-label','管理会话 '+(s.title||'新对话'));more.innerHTML='<svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>';more.onclick=e=>{e.stopPropagation();const rect=more.getBoundingClientRect();openSessionMenu(s,rect.right,rect.bottom)};row.oncontextmenu=e=>{e.preventDefault();openSessionMenu(s,e.clientX,e.clientY)};const status=s.generationStatus||localSessionStates.get(s.id),seenKey='agentflow.replySeen.'+(state.currentUser?.id||'')+'.'+s.id,stamp=s.updatedAt||'',seen=localStorage.getItem(seenKey);const selected=active&&assistant.sessionId()===s.id;if(selected||seen===null)localStorage.setItem(seenKey,stamp);const unread=status==='completed'&&!selected&&seen!==null&&seen!==stamp;row.append(b);if(status==='running'||unread){const mark=document.createElement('span');mark.className='collab-session-state '+(unread?'unread':'running');mark.title=unread?'有新回复':'正在处理';mark.setAttribute('aria-label',mark.title);if(!unread)mark.append(icon('refresh'));row.append(mark)}if(!s.shared)row.append(more);else row.title='共享 SOP 会话';host.append(row)}
    if(!rows.length)paintSidebarEmpty(host,archived?'没有已归档会话':'开始对话后会保存在这里');
  }
  function paintSidebarEmpty(host,text){const note=document.createElement('p');note.className='collab-sidebar-empty';note.textContent=text;host.append(note)}
  const taskTones={running:'running',queued:'queued',pending:'queued',waiting:'attention',waiting_approval:'attention',waiting_retry:'attention',paused:'attention',failed:'failed',succeeded:'done',cancelled:'muted',skipped:'muted'};
  const taskGroups=[['active','进行中'],['recent','最近结束'],['earlier','更早']],collapsedTaskGroups=new Set(['earlier']);
  function taskStatus(r){
    const job=sopReturns.get(r.id);
    if(job&&job.status!=='succeeded'){const p=handoffPresentation(job);return {tone:p.tone==='failed'?'failed':'running',text:p.label}}
    if(r.status==='waiting'&&r.pauseRequested)return {tone:'attention',text:'已暂停'};
    return {tone:taskTones[r.status]||'queued',text:label(r.status)};
  }
  function taskTime(r){const ms=Date.parse(r.updatedAt||r.createdAt||'');return Number.isFinite(ms)?ms:0}
  function taskClock(ms){
    if(!ms)return '';const diff=Date.now()-ms;if(diff<60e3)return '刚刚';if(diff<3600e3)return Math.floor(diff/60e3)+' 分钟前';
    const d=new Date(ms),now=new Date(),hm=String(d.getHours()).padStart(2,'0')+':'+String(d.getMinutes()).padStart(2,'0');
    const days=Math.round((new Date(now.getFullYear(),now.getMonth(),now.getDate())-new Date(d.getFullYear(),d.getMonth(),d.getDate()))/864e5);
    return days===0?hm:days===1?'昨天 '+hm:d.getFullYear()===now.getFullYear()?(d.getMonth()+1)+'月'+d.getDate()+'日':d.getFullYear()+'/'+(d.getMonth()+1)+'/'+d.getDate();
  }
  function taskElapsed(r){
    const ms=r.timing?.wallMs||Date.now()-(Date.parse(r.createdAt||'')||Date.now()),m=Math.floor(ms/60e3);
    if(m<1)return '刚开始';if(m<60)return m+' 分钟';const h=Math.floor(m/60);return h<24?h+' 小时':Math.floor(h/24)+' 天';
  }
  function taskGroup(r,tone){
    if(['running','queued','attention'].includes(tone))return 'active';
    return Date.now()-taskTime(r)<864e5?'recent':'earlier';
  }
  function taskItem(r,st){
    const b=document.createElement('button');b.type='button';b.className='collab-list-item collab-task-item'+(currentRun===r.id?' active':'');
    const workflowName=r.workflowSnapshot?.name||state.data?.workflows?.find(w=>w.id===r.workflowId)?.name||r.workflowId||'';
    const issue=state.data?.issues?.find(i=>i.id===(r.issueId||r.executionContext?.issueId));
    const full=r.workflowSnapshot?.taskTitle||issue?.title||String(r.input||'').trim().split('\n')[0]||workflowName;
    const tagged=full.match(/^\s*(?:\[([^[\]【】]{1,12})\]|【([^[\]【】]{1,12})】)\s*/),text=tagged&&full.length>tagged[0].length?full.slice(tagged[0].length):full;
    const dot=document.createElement('span');dot.className='collab-task-dot '+st.tone;dot.setAttribute('aria-hidden','true');
    const body=document.createElement('span');body.className='collab-task-body';
    const name=document.createElement('b');if(tagged&&text!==full){const tag=document.createElement('span');tag.className='collab-task-tag';tag.textContent=tagged[1]||tagged[2];name.append(tag)}name.append(document.createTextNode(text.slice(0,120)));
    const meta=document.createElement('small'),stateLabel=document.createElement('span'),flow=document.createElement('span'),time=document.createElement('time');
    stateLabel.className='collab-task-state '+st.tone;stateLabel.textContent=st.text;flow.className='collab-task-workflow';flow.textContent=workflowName?'· '+workflowName:'';
    time.className='collab-task-time';const ms=taskTime(r);if(ms)time.dateTime=new Date(ms).toISOString();time.textContent=st.tone==='running'?taskElapsed(r):taskClock(ms);
    meta.append(stateLabel,flow,time);body.append(name,meta);b.append(dot,body);
    b.title=[full,workflowName&&'流程：'+workflowName,'状态：'+st.text,r.createdAt&&'创建：'+new Date(r.createdAt).toLocaleString(),'ID：'+r.id].filter(Boolean).join('\n');
    b.onclick=()=>selectRun(r.id);return b;
  }
  function paintTasks(){
    const host=$id('collabTasks');host.replaceChildren();const query=$id('collabTaskSearch').value.trim().toLowerCase(),filter=$id('collabTaskFilter').value;
    const rows=taskRows.filter(r=>{const name=r.workflowSnapshot?.name||state.data?.workflows?.find(w=>w.id===r.workflowId)?.name||r.workflowId||'';const text=[name,r.id,r.input,r.workflowSnapshot?.taskTitle,label(r.status)].join(' ').toLowerCase();return (!query||text.includes(query))&&(filter==='all'||filter==='active'&&!['succeeded','failed','cancelled'].includes(r.status)||filter==='waiting'&&['waiting','waiting_approval','waiting_retry','paused'].includes(r.status)||r.status===filter)});
    const buckets=new Map(taskGroups.map(([key])=>[key,[]]));
    for(const r of rows){const st=taskStatus(r);buckets.get(taskGroup(r,st.tone)).push({r,st})}
    for(const [key,list] of buckets)if(key!=='active')list.sort((a,b)=>taskTime(b.r)-taskTime(a.r));
    let budget=taskLimit;
    for(const [key,title] of taskGroups){
      const list=buckets.get(key);if(!list.length||budget<=0)continue;
      const collapsed=!query&&collapsedTaskGroups.has(key),head=document.createElement('button');head.type='button';head.className='collab-task-group';head.setAttribute('aria-expanded',String(!collapsed));
      head.append(icon('chevron'),document.createTextNode(title));const count=document.createElement('em');count.textContent=String(list.length);head.append(count);
      const failed=list.filter(x=>x.st.tone==='failed').length;if(failed){const alert=document.createElement('span');alert.className='collab-task-group-failed';alert.textContent='· '+failed+' 失败';head.append(alert)}
      head.onclick=()=>{collapsed?collapsedTaskGroups.delete(key):collapsedTaskGroups.add(key);paintTasks()};host.append(head);
      if(collapsed)continue;
      for(const {r,st} of list.slice(0,budget))host.append(taskItem(r,st));budget-=list.length;
    }
    if(rows.length>taskLimit){const more=document.createElement('button');more.type='button';more.className='collab-load-more';more.textContent='加载更多（'+rows.length+' 个匹配任务）';more.onclick=()=>{taskLimit+=30;paintTasks()};host.append(more)}
    if(!rows.length)paintSidebarEmpty(host,query||filter!=='all'||$id('collabTaskResponsible').value==='me'?'没有匹配的任务':project()?'还没有任务运行':'选择项目后查看任务');
  }
  $id('collabSessionFilter').onchange=()=>{closeSessionMenu();paintSessions()};
  $id('collabTaskSearch').oninput=$id('collabTaskFilter').onchange=()=>{taskLimit=30;paintTasks()};
  $id('collabTaskResponsible').onchange=()=>{taskLimit=30;taskRows=[];paintTasks();void refresh()};
  const newGroup=document.createElement('button');newGroup.type='button';newGroup.title='新建协作任务';newGroup.setAttribute('aria-label','新建协作任务');newGroup.textContent='+';newGroup.onclick=()=>project()?groupDialog(true):toast('请先选择项目');$id('collabSearchToggle').before(newGroup);
  $id('collabRefresh').append(icon('refresh'));
  $id('collabSearchToggle').append(icon('search'));
  function toggleTaskSearch(open){
    $id('collabTaskFilters').hidden=!open;$id('collabSearchToggle').setAttribute('aria-expanded',String(open));
    if(open)$id('collabTaskSearch').focus();else{const filtered=$id('collabTaskResponsible').value==='me';$id('collabTaskSearch').value='';$id('collabTaskFilter').value='all';$id('collabTaskResponsible').value='all';taskLimit=30;if(filtered){taskRows=[];void refresh()}paintTasks()}
  }
  $id('collabSearchToggle').onclick=()=>toggleTaskSearch($id('collabTaskFilters').hidden);
  $id('collabTaskFilters').onkeydown=e=>{if(e.key==='Escape'){e.preventDefault();toggleTaskSearch(false);$id('collabSearchToggle').focus()}};
  async function refresh(){
    const scope=project(),generation=++taskRefreshGeneration,params=new URLSearchParams({projectId:scope});
    if($id('collabTaskResponsible').value==='me')params.set('responsible','me');
    const stale=()=>scope!==project()||generation!==taskRefreshGeneration;
    try{
      const [history,allRuns]=await Promise.all([api('/api/assistant/sessions',{silent:true}),api('/api/runs?'+params,{silent:true})]);
      if(stale())return;
      sessions=history.filter(s=>(s.projectId||'')===scope);
      await Promise.all(sessions.filter(s=>!s.generationStatus).map(async s=>{try{const res=await fetch('/api/assistant/sessions/'+encodeURIComponent(s.id)+'/generation');if(res.ok){const g=await res.json();localSessionStates.set(s.id,g.status)}}catch{}}));if(stale())return;
      paintSessions();await assistant.refreshShared();
      if(stale())return;
      taskRows=allRuns.filter(r=>(r.executionContext?.projectId||'')===scope);await refreshSOPHandoffs();if(stale())return;paintTasks();
      if(currentRun){const id=currentRun;const before=JSON.stringify(run?.nodes?.[nodeId]);const detail=await api('/api/runs/'+encodeURIComponent(id),{silent:true});if(id===currentRun&&!stale()){run=detail;renderPipeline();scheduleGroupState();if(nodeId&&before!==JSON.stringify(run?.nodes?.[nodeId]))paintNode()}}
    }catch(err){if(active&&!stale())error(err)}
  }
  function label(status){return ({waiting:'等待处理',running:'执行中',queued:'排队中',paused:'已暂停',pending:'未开始',succeeded:'已完成',failed:'失败',cancelled:'已取消',waiting_approval:'待确认',waiting_retry:'待重试',skipped:'已跳过'})[status]||status||'未开始'}
  function isGroup(){return run?.workflowSnapshot?.collaborationMode==='group'}
  function taskTitle(r){const issue=state.data?.issues?.find(i=>i.id===(r?.issueId||r?.executionContext?.issueId));return r?.workflowSnapshot?.taskTitle||issue?.title||String(r?.input||'').trim().split('\n')[0]||r?.workflowSnapshot?.name||'任务协作'}
  function renderPipeline(){
 const ideButton=$id("collabToggleIDE");if(ideButton)ideButton.hidden=!currentRun;
    root.closest('.collaboration-main').querySelector('.collaboration-context').hidden=!currentRun;
    const pipeline=$id('collabPipeline');pipeline.hidden=!run||isGroup();pipeline.replaceChildren();
    $id('collabClearRun').hidden=true;$id('collabInspect').hidden=!currentRun;
    $id('collabScope').textContent=currentRun?(isGroup()?'自由协作 · 共享任务':'SOP · '+(run?.workflowSnapshot?.name||'任务协作')):'平台会话';
    $id('collabTitle').textContent=run?taskTitle(run):'从一句话开始';
    root.querySelector('.ai-head b').textContent=currentRun?'Main Bot · 任务协作':'协作助手';
    root.classList.toggle('is-group-chat',isGroup());document.querySelector('.collaboration-shell').classList.toggle('is-group-workspace',isGroup());assistant.sync();paintMembers();paintModeButton();if(!run)return;
    if(isGroup()){
      pipeline.hidden=false;pipeline.replaceChildren(handoffRibbon());paintGroupRibbon();paintGroupFeed();paintTaskWelcome();return;
    }
    const handoff=sopReturns.get(currentRun);
    if(handoff&&handoff.status!=='succeeded'){pipeline.append(handoffRibbon());paintGroupRibbon()}
    const nodes=document.createElement('div');nodes.className='collab-nodes'+(isGroup()?' collab-group-nodes':'');
    const stages=isGroup()?[run.workflowSnapshot.nodes.filter(n=>n.requestId)]:workflowStages(run.workflowSnapshot);
    for(const stage of stages){
      const group=document.createElement('div');group.className='collab-stage';
      for(const n of stage){const nr=run.nodes?.[n.id]||{};const b=document.createElement('button');b.type='button';b.className='collab-node '+(nr.status||'pending')+(nodeId===n.id?' selected':'');
        const mark=document.createElement('span');mark.className='collab-step-mark';mark.textContent=nr.status==='succeeded'?'✓':nr.status==='waiting_approval'?'!':nr.status==='failed'?'!':String(run.workflowSnapshot.nodes.indexOf(n)+1);if(nr.status==='running'){mark.textContent='';mark.append(icon('refresh'))}
        const text=document.createElement('span'),title=document.createElement('b');title.textContent=n.name||n.id;const status=document.createElement('small');status.textContent=label(nr.status);if(isGroup()&&n.taskPrompt){title.textContent=n.name;status.textContent=n.taskPrompt;status.title=n.taskPrompt}text.append(title,status);b.append(mark,text);b.title=label(nr.status)+(nr.attempt?' · 第 '+nr.attempt+' 次':'');b.onclick=()=>{if(busy())return toast('正在保存或上传，请稍后查看节点');showIDE(false);nodeId=n.id;nodeAttempt=0;paintNode();renderPipeline()};group.append(b)}
      nodes.append(group);
    }
    if(isGroup()&&!stages[0].length){const hint=document.createElement('p');hint.className='collab-group-hint';hint.textContent='Main Bot 和角色可以按需派发任务，执行过程会实时显示在这里。';nodes.append(hint)}
    pipeline.append(nodes);
    const bar=document.createElement('div');bar.className='collab-run-actions';const status=document.createElement('span');status.className='collab-task-status '+run.status;
    const waiting=run.workflowSnapshot.nodes.find(n=>run.nodes?.[n.id]?.status==='waiting_approval');
    status.textContent=run.pauseRequested?'已暂停':waiting?'等待你确认 · '+waiting.name:isGroup()&&run.status==='waiting'?'等待新的分工':label(run.status);if(waiting){status.classList.add('actionable');status.onclick=()=>{nodeId=waiting.id;nodeAttempt=0;paintNode();renderPipeline()}}bar.append(status);
    const action=(text,kind)=>{const b=document.createElement('button');b.className='button ghost small';b.type='button';b.textContent=text;b.onclick=async()=>{try{b.disabled=true;await api('/api/runs/'+encodeURIComponent(currentRun)+'/'+kind,{method:'POST',body:JSON.stringify({})});await refresh()}catch(err){error(err)}finally{b.disabled=false}};bar.append(b)};
    if(run.status==='running'&&!run.pauseRequested)action('暂停执行','pause');
    if(run.status==='waiting'&&run.pauseRequested)action(isGroup()?'继续协作':'继续 SOP','resume');
    if(run.status==='running'||(run.status==='waiting'&&run.pauseRequested)||['succeeded','failed','cancelled'].includes(run.status)){const edit=document.createElement('button');edit.type='button';edit.className='button ghost small';edit.textContent='打开工作区';edit.onclick=async()=>{try{edit.disabled=true;if(run.status==='running'||(run.status==='waiting'&&!run.pauseRequested)){let last;for(let i=0;i<40;i++){try{await api('/api/runs/'+encodeURIComponent(currentRun)+'/workspace/takeover',{method:'POST',body:'{}'});break}catch(err){last=err;if(!/停止|稍后|仍有任务/.test(String(err.message||err)))throw err;await new Promise(r=>setTimeout(r,300));continue}last=null;break}if(last)throw last;await refresh()}showIDE(true);await inspect('files')}catch(err){error(err)}finally{edit.disabled=false}};bar.append(edit)}

    if((!isGroup()||['succeeded','failed','cancelled'].includes(run.status))&&['waiting','running','succeeded','failed','cancelled'].includes(run.status)){const convert=document.createElement('button');convert.type='button';convert.className='button small collab-convert';convert.textContent=isGroup()?'继续自由协作':'自由协作';convert.onclick=async()=>{if(run.status==='running'||Object.values(run.nodes||{}).some(n=>n.status==='running'))return toast('请先暂停 SOP，等待 Agent 停止后再切换');if(!await afConfirm({title:'切换自由协作',message:'将停止按 SOP 顺序推进，后续由 Main Bot 和角色按需派发。执行记录保留；未执行的步骤不再自动启动。'+(run.workspaceCleanedAt?'原工作区已经回收，将重新准备仓库；此前未保存或未推送的文件无法恢复。':'已有工作区修改不会回退。'),confirmText:'切换自由协作'}))return;try{await api('/api/runs/'+encodeURIComponent(currentRun)+'/group/mode',{method:'POST',body:'{}'});await refresh()}catch(err){error(err)}};bar.append(convert)}
    const detail=document.createElement('button');detail.type='button';detail.className='collab-detail-link';detail.textContent='运行详情';detail.onclick=()=>openRunFromId(currentRun);bar.append(detail);
    pipeline.append(bar);paintGroupFeed();paintTaskWelcome();
  }
  function paintTaskWelcome(){const welcome=root.querySelector('.ai-welcome');if(!welcome||!run)return;welcome.querySelector('h1').textContent=isGroup()?'一起推进这个任务':'开始任务讨论';welcome.querySelector('p').textContent=isGroup()?'告诉 Main Bot 你的目标，或 @群内角色。大家在共享工作区协作。':'与 Main Bot 讨论「'+taskTitle(run)+'」，点击上方步骤查看执行过程。'}
  const groupLoads=new Set();
  function groupTime(value){const n=Date.parse(value||'');return n>0?n:0}
  function groupSpeaker(card,name){
    const head=document.createElement('div');head.className='ai-speaker';const avatar=document.createElement('span');avatar.className='ai-speaker-avatar';avatar.textContent=name.slice(0,1);const label=document.createElement('span');label.textContent=name;head.append(avatar,label);card.append(head);
  }
  function groupEventInAttempt(event,node){
    const attempt=Number(event.context?.attempt||event.attributes?.attempt||0);
    if(attempt)return attempt===(node.attempt||1);
    if((node.attempt||0)<=1)return true;
    const start=groupTime(node.startedAt);return start>0&&groupTime(event.timestamp)>=start;
  }
  function groupProcess(card,n,nr){
    let details=card.querySelector('.collab-chat-process');const events=[...nodeLiveEvents.values()].filter(e=>e.scope===run.id&&e.nodeId===n.id&&groupEventInAttempt(e,nr)).sort((a,b)=>a.sequence-b.sequence);
    if(!events.length&&!nr.error){details?.remove();return}
    if(!details){details=document.createElement('details');details.className='collab-chat-process';card.insertBefore(details,card.querySelector('.ai-message-actions'))}
    const key=JSON.stringify(events.map(e=>[e.id,e.phase,e.message]))+nr.status+nr.error;if(details.dataset.key===key)return;details.dataset.key=key;
    const open=details.open;details.replaceChildren();details.open=open;const summary=document.createElement('summary');summary.textContent=['running','pending'].includes(nr.status)?'正在处理…':'处理过程';details.append(summary);
    const tools=new Map(),body=document.createElement('div');body.className='collab-process-body';
    for(const e of events){
      if(e.category==='tool'){const tid=e.toolCallId||e.id;let line=tools.get(tid);if(!line){line=document.createElement('div');line.className='ai-tool-line';line.dataset.toolId=tid;const mark=document.createElement('span');mark.className='ai-tool-spinner';const text=document.createElement('span');text.className='ai-tool-line-text';text.textContent=e.message||e.tool?.name||'调用工具';line.append(mark,text);tools.set(tid,line);body.append(line)}if(e.phase==='finish'){line.firstChild.className='ai-tool-check';line.classList.add('is-done')}}
      else if(e.category==='reasoning'){const thought=document.createElement('details');thought.className='ai-think';const label=document.createElement('summary');label.textContent='思考过程';const text=document.createElement('div');text.className='ai-think-body';text.textContent=e.message;thought.append(label,text);body.append(thought)}
      else if(e.category==='output'){const answer=document.createElement('div');answer.className='markdown-body';answer.innerHTML=renderMarkdown(e.message||'');body.append(answer)}
    }
    if(!['running','pending'].includes(nr.status))for(const line of tools.values()){line.firstChild.className='ai-tool-check';line.classList.add('is-done')}
    if(nr.error){const error=document.createElement('p');error.className='collab-process-error';error.textContent=nr.error;body.append(error)}
    details.append(body);
  }
  function paintGroupFeed(){
    const log=root.querySelector('.ai-messages');log.querySelector('.collab-group-feed')?.remove();
    paintSOPHandoffReceipt(log);
    if(!isGroup()||nodeId){log.querySelectorAll('[data-group-chat]').forEach(e=>e.remove());return}
    const nearEnd=log.scrollHeight-log.scrollTop-log.clientHeight<120,top=log.scrollTop,keep=new Set(),nodes=run.workflowSnapshot.nodes.filter(n=>n.requestId);
    function row(key,name,text,time,system=false){
      keep.add(key);let card=[...log.querySelectorAll('[data-group-chat]')].find(e=>e.dataset.groupChat===key);
      if(!card){card=document.createElement('article');card.dataset.groupChat=key;card.className=system?'collab-system-message':'ai-message ai-role-assistant collab-group-message';if(!system)groupSpeaker(card,name);const body=document.createElement('div');body.className=system?'collab-system-body':'collab-group-body markdown-body';card.append(body);log.append(card)}
      card.dataset.chatTime=String(groupTime(time));const body=card.querySelector('.collab-group-body,.collab-system-body');if(card.dataset.text!==text){card.dataset.text=text;if(system)body.textContent=text;else body.innerHTML=renderMarkdown(text)}
      if(!system&&!card.querySelector('.ai-message-actions')){const bar=document.createElement('div');bar.className='ai-message-actions is-complete';const copy=document.createElement('button');copy.type='button';copy.title='复制消息';copy.setAttribute('aria-label','复制消息');copy.append(icon('issue'));copy.onclick=()=>navigator.clipboard.writeText(card.dataset.text).then(()=>toast('已复制'));const stamp=document.createElement('time');stamp.className='ai-reply-time';stamp.textContent=new Date(time).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});bar.append(copy,stamp);card.append(bar)}
      return card;
    }
    for(const event of run.events||[]){
      const n=nodes.find(n=>n.id===event.nodeId),nr=n&&run.nodes?.[n.id];
      if(event.type==='group.invited')row('event/'+event.id,'',event.message,event.timestamp,true);
      else if(n&&event.type==='group.dispatched'){const sender=nodes.find(x=>x.id===n.requestedBy);row('event/'+event.id,'',(sender?.name||'Main Bot')+' 请 '+n.name+' 一起处理',event.timestamp,true)}
      else if(n&&event.type==='group.message'){row('event/'+event.id,n.name,event.message,event.timestamp);}
    }
    for(const n of nodes){
      const nr=run.nodes?.[n.id]||{},messages=(run.events||[]).filter(e=>e.nodeId===n.id&&e.type==='group.message');
      let card;
      if(['running','pending','waiting_approval'].includes(nr.status))card=row('live/'+n.id,n.name,nr.status==='pending'?'正在接话…':'正在处理…',groupTime(nr.startedAt)?nr.startedAt:(run.events||[]).find(e=>e.nodeId===n.id&&e.type==='group.dispatched')?.timestamp||run.createdAt);
      else {const currentMessages=messages.filter(e=>!groupTime(nr.startedAt)||groupTime(e.timestamp)>=groupTime(nr.startedAt));if(currentMessages.length)card=[...log.querySelectorAll('[data-group-chat]')].find(e=>e.dataset.groupChat==='event/'+currentMessages.at(-1).id);}
      if(!card&&!['running','pending','waiting_approval'].includes(nr.status)&&(nr.output||nr.error))card=row('result/'+n.id+'/'+(nr.attempt||1),n.name,nr.error||nr.output,nr.finishedAt||run.updatedAt);
      if(card){card.dataset.groupNode=n.id;groupProcess(card,n,nr);if(['running','pending','waiting_approval'].includes(nr.status)){card.classList.add('is-typing');card.querySelector('.ai-message-actions')?.remove()}else card.classList.remove('is-typing')}
      const loadKey=run.id+'/'+n.id+'/'+nr.attempt+'/'+nr.status;if(!groupLoads.has(loadKey)&&nr.attempt){groupLoads.add(loadKey);const id=run.id;api('/api/runs/'+encodeURIComponent(id)+'/telemetry?'+new URLSearchParams({nodeId:n.id,limit:'200',expanded:'true'}),{silent:true}).then(data=>{if(currentRun!==id)return;for(const event of data.events||[])nodeLiveEvents.set(id+'/'+(event.id||event.sequence),{...event,scope:id});paintGroupFeed()}).catch(()=>groupLoads.delete(loadKey))}
    }
    paintGroupReceipt(log,keep);
    log.querySelectorAll('[data-group-chat]').forEach(e=>{if(!keep.has(e.dataset.groupChat))e.remove()});
    if(keep.size){root.classList.remove('is-empty');log.querySelector('.ai-welcome')?.remove()}
    const children=[...log.children],sorted=children.slice().sort((a,b)=>Number(a.dataset.chatTime||0)-Number(b.dataset.chatTime||0));
    if(sorted.some((el,i)=>el!==children[i]))sorted.forEach(el=>log.append(el));log.scrollTop=nearEnd?log.scrollHeight:top;
  }
  function paintSOPHandoffReceipt(log){
    const job=sopReturns.get(currentRun),reference=run?.workflowSnapshot?.groupSopHandoff;
    const saved=job?.summaryArtifactId?job:reference;
    const artifactID=saved?.summaryArtifactId,card=log.querySelector('[data-sop-handoff]');
    if(!artifactID||nodeId){card?.remove();return}
    const key=currentRun+'/'+artifactID,signature=JSON.stringify([key,job?.status,job?.phase,saved.summaryPreview]);
    if(card?.dataset.signature===signature)return;
    const receipt=card||document.createElement('article');receipt.className='collab-work-receipt collab-handoff-receipt';receipt.dataset.sopHandoff=key;receipt.dataset.signature=signature;receipt.dataset.chatTime=String(groupTime(saved.createdAt));receipt.replaceChildren();
    const head=document.createElement('div');head.className='collab-receipt-heading';const mark=document.createElement('span');mark.className='collab-receipt-mark';mark.append(icon('check'));const title=document.createElement('b');title.textContent=job&&handoffActive(job)&&!job.summaryArtifactId||run?.workflowSnapshot?.collaborationMode==='group'&&job?.status==='succeeded'?'上一轮群聊交接':'群聊交接已保存';head.append(mark,title);receipt.append(head);
    const target=saved.nodeName||run?.workflowSnapshot?.nodes?.find(n=>n.id===saved.nodeId)?.name||run?.workflowSnapshot?.sopCheckpoint?.workflow?.nodes?.find(n=>n.id===saved.nodeId)?.name||saved.nodeId;
    const note=document.createElement('p');note.textContent=job&&handoffActive(job)&&!job.summaryArtifactId?'本轮交接正在整理，这份摘要保留上一轮决定。':run?.workflowSnapshot?.collaborationMode==='group'&&job?.status==='succeeded'?'上一轮交接已归档，本轮的新决定将在切回 SOP 时更新。':job?.status==='failed'&&job.phase==='starting'?'摘要已归档，尚未启动 SOP；可以重试继续。':job&&handoffActive(job)?'摘要已归档，正在启动 SOP。':'后续 SOP 节点将读取这份交接。';if(target)note.textContent+=' 从「'+target+'」继续。';receipt.append(note);
    if(saved.summaryPreview){const preview=document.createElement('p');preview.className='collab-handoff-preview';preview.textContent=saved.summaryPreview;receipt.append(preview)}
    const files=document.createElement('div');files.className='collab-receipt-files';const open=document.createElement('button');open.type='button';open.append(icon('issue'),document.createTextNode('查看交接摘要'));const id=currentRun,artifact=run?.artifacts?.find(a=>a.id===artifactID)||{id:artifactID,name:'群聊交接摘要',path:saved.summaryPath,mediaType:'text/markdown'};open.onclick=()=>previewArtifact(id,artifact,'群聊交接'+(target?' · '+target:''));files.append(open);receipt.append(files);
    if(!card)log.append(receipt);root.classList.remove('is-empty');log.querySelector('.ai-welcome')?.remove();
    const children=[...log.children],sorted=children.slice().sort((a,b)=>Number(a.dataset.chatTime||0)-Number(b.dataset.chatTime||0));if(sorted.some((el,i)=>el!==children[i]))sorted.forEach(el=>log.append(el));
  }
  function paintGroupReceipt(log,keep){
    const receipt=groupActivity?.receipt;if(!receipt||!receipt.nodeIds?.length||!['completed','failed'].includes(receipt.status))return;
    const key='receipt/current';keep.add(key);let card=log.querySelector('[data-group-chat="receipt/current"]');
    if(!card){card=document.createElement('article');card.dataset.groupChat=key;card.className='collab-work-receipt';log.append(card)}
    const signature=JSON.stringify(receipt);if(card.dataset.signature===signature)return;card.dataset.signature=signature;card.replaceChildren();
    const times=receipt.nodeIds.map(id=>groupTime(run.nodes?.[id]?.finishedAt));card.dataset.chatTime=String(Math.max(...times,0)+1);
    const head=document.createElement('div');head.className='collab-receipt-heading';const mark=document.createElement('span');mark.className='collab-receipt-mark';mark.append(icon(receipt.status==='failed'?'alert':'check'));const title=document.createElement('b');title.textContent=receipt.status==='failed'?'本轮有工作需要处理':'本轮分工已结束';const badge=document.createElement('span');badge.className='collab-verification '+(receipt.verification?.status||'unverified');badge.textContent=groupVerificationLabel(receipt.verification);head.append(mark,title,badge);card.append(head);
    const note=document.createElement('p');note.textContent=receipt.verification?.status==='passed'?'已运行：'+(receipt.verification.checks||[]).filter(c=>!c.historical).map(c=>c.name||c.gateId||'命令检查').join('、'):receipt.verification?.reason||'本轮执行已结束，暂未记录独立检查。';card.append(note);
    const files=document.createElement('div');files.className='collab-receipt-files';for(const artifact of receipt.artifacts||[]){const item=document.createElement('button');item.type='button';item.append(icon('issue'),document.createTextNode(artifact.path||artifact.name));item.onclick=()=>previewArtifact(currentRun,artifact,'本轮产物 · 第 '+artifact.attempt+' 次');files.append(item)}if(files.childElementCount)card.append(files);
    const checks=receipt.verification?.checks||[];if(checks.length){const detail=document.createElement('details');detail.className='collab-receipt-checks';const summary=document.createElement('summary');summary.textContent='查看 '+checks.length+' 项实际检查';detail.append(summary);for(const check of checks){const line=document.createElement('div');const mark=document.createElement('span');mark.textContent=check.status==='passed'?'✓':check.status==='failed'?'!':'·';line.append(mark,document.createTextNode(check.name||check.gateId||'命令检查'));if(check.historical){const old=document.createElement('small');old.textContent='历史结果';line.append(old)}detail.append(line)}card.append(detail)}
  }
  function groupDialog(create){
    const scope=project();
    const dialog=document.createElement('dialog');dialog.className='collab-group-dialog';const form=document.createElement('form');form.method='dialog';const heading=document.createElement('h2');heading.textContent=create?'新建协作任务':'派发角色任务';const intro=document.createElement('p');intro.textContent=create?'默认自由协作。选择本次涉及的仓库，由 Main Bot 按需协调，也可以随时邀请角色加入。':'角色将真实执行任务，可以与其他 Agent 并行修改共享工作区。';form.append(heading,intro);
    const field=(text,control,host=form)=>{const label=document.createElement('label');label.textContent=text;label.append(control);host.append(label);return label};
    const select=document.createElement('select');select.required=!create;select.setAttribute('aria-label',create?'角色与环境模板（可选）':'执行角色');const items=create?(state.data?.workflows||[]).filter(w=>w.projectId===scope||w.shared||w.generic):(state.data?.agents||[]).filter(a=>a.projectId===scope||a.shared);
    if(create){const opt=document.createElement('option');opt.value='';opt.textContent='自由协作（不使用 SOP 模板）';select.append(opt)}
    for(const item of items){const opt=document.createElement('option');opt.value=item.id;opt.textContent=item.name;select.append(opt)}if(!create)field('执行角色',select);
    const prompt=document.createElement('textarea');prompt.required=true;prompt.rows=5;prompt.placeholder=create?'描述这个任务的目标…':'具体需要完成什么，如何验证…';field(create?'任务目标':'工作说明',prompt);
    const repositoryChecks=[],sandbox=document.createElement('select');
    if(create){
      const repositories=(state.data?.repositories||[]).filter(r=>r.projectId===scope),defaultRepository=state.data?.projects?.find(p=>p.id===scope)?.defaultRepositoryId;
      const group=document.createElement('fieldset');group.className='collab-repository-picker';const legend=document.createElement('legend');legend.textContent='涉及仓库（可多选）';group.append(legend);
      const choices=document.createElement('div');choices.className='collab-repository-choices';
      for(const repository of repositories){const check=document.createElement('input');check.type='checkbox';check.value=repository.id;check.checked=repository.id===defaultRepository||repositories.length===1;repositoryChecks.push(check);const row=document.createElement('label'),text=document.createElement('span'),name=document.createElement('b'),branch=document.createElement('small');name.textContent=repository.name||repository.id;branch.textContent='基于 '+(repository.defaultBranch||'默认分支');text.append(name,branch);row.append(check,text);choices.append(row)}
      const summary=document.createElement('small');summary.className='collab-repository-summary';const paintSelection=()=>{summary.textContent=repositories.length?'已选择 '+repositoryChecks.filter(c=>c.checked).length+' / '+repositories.length+' 个仓库；不选仓库可进行讨论和非代码任务。':'当前项目尚未配置仓库，可先创建讨论任务。'};choices.onchange=paintSelection;paintSelection();group.append(choices,summary);form.append(group);
      const settings=document.createElement('details');settings.className='collab-group-settings';const title=document.createElement('summary');title.textContent='运行环境与角色模板';settings.append(title);field('角色与环境模板（可选）',select,settings);
      const environments=(state.data?.sandboxes||[]).filter(s=>s.projectId===scope||s.shared).sort((a,b)=>Number(b.projectId===scope)-Number(a.projectId===scope));
      for(const item of environments){const option=document.createElement('option');option.value=item.id;option.textContent=item.name||item.id;sandbox.append(option)}
      sandbox.setAttribute('aria-label','运行环境');const sandboxLabel=field('运行环境',sandbox,settings);
      const environmentNote=document.createElement('p');environmentNote.className='collab-dialog-note';settings.append(environmentNote);
      const paintEnvironment=()=>{sandboxLabel.hidden=!!select.value;sandbox.disabled=!!select.value;sandbox.required=!select.value&&environments.length>0;environmentNote.textContent=select.value?'仅沿用模板的角色和运行环境，任务仍按自由协作推进。':environments.length?'使用「'+(environments.find(s=>s.id===sandbox.value)?.name||sandbox.value)+'」，可展开调整。':'当前项目没有可用运行环境，请先配置或选择模板。'};select.onchange=sandbox.onchange=paintEnvironment;paintEnvironment();if(!environments.length)settings.open=true;form.append(environmentNote,settings);
    }
    const files=document.createElement('input');files.placeholder='例如 web/login.js, server/auth.go';if(!create)field('涉及文件（可选，用逗号分隔）',files);
    const note=document.createElement('p');note.className='collab-dialog-note';note.textContent=create?'所选仓库共用任务工作区，创建后通过群聊协作。':'已有变更不会回退；角色通过群消息对齐文件修改，文件范围作为分工说明。';form.append(note);
    const err=document.createElement('p');err.className='collab-dialog-error';err.setAttribute('role','alert');form.append(err);const footer=document.createElement('footer'),cancel=document.createElement('button'),submit=document.createElement('button');cancel.type='button';cancel.className='button ghost';cancel.textContent='取消';cancel.onclick=()=>dialog.close();submit.type='submit';submit.className='button';submit.textContent=create?'创建任务':'派发任务';footer.append(cancel,submit);form.append(footer);dialog.append(form);document.body.append(dialog);dialog.addEventListener('close',()=>dialog.remove());dialog.addEventListener('click',e=>{if(e.target===dialog){const r=dialog.getBoundingClientRect();if(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom)dialog.close()}});
    const requestId=crypto.randomUUID();form.onsubmit=async e=>{e.preventDefault();submit.disabled=true;err.textContent='';try{if(scope!==project())throw new Error('当前项目已切换，请重新打开任务窗口');if(create){if(!select.value&&!sandbox.value)throw new Error('请先配置可用的运行环境或选择模板');const data=await api('/api/task-groups',{method:'POST',body:JSON.stringify({workflowId:select.value,projectId:scope,input:prompt.value,repositoryIds:repositoryChecks.filter(c=>c.checked).map(c=>c.value),...(!select.value?{sandboxProfileId:sandbox.value}:{})})});dialog.close();await selectRun(data.id)}else{await api('/api/runs/'+encodeURIComponent(currentRun)+'/group/tasks',{method:'POST',body:JSON.stringify({agentId:select.value,prompt:prompt.value,requestId,files:files.value.split(',').map(f=>f.trim()).filter(Boolean)})});dialog.close();await returnToPM();await refresh()}}catch(error){err.textContent=error.message}finally{submit.disabled=false}};dialog.showModal();prompt.focus();
  }
  async function selectRun(id){
    if(busy())return toast('请先停止当前回复或等待附件上传完成');
    switchView('collaboration');
    closeRail();
    try{const shared=await api('/api/runs/'+encodeURIComponent(id)+'/conversation',{silent:true});await assistant.openSession(shared.id);await refresh()}
    catch(err){error(err)}
  }
  function restoreRun(id){groupStateEpoch++;groupActivity=null;groupFollowup=null;clearTimeout(groupStateTimer);groupStateTimer=0;showIDE(false);idePane.replaceChildren();idePane.dataset.run='';assistant.leaveNode?.();connectRunStream(id);currentRun=id;run=null;membersOpen=window.innerWidth>1000;nodeId='';nodeAttempt=0;$id('collabNodePanel').hidden=true;$id('collabInspector').hidden=true;inspectorGeneration++;renderPipeline();if(id&&active)refresh()}
  async function paintNode(){
    const panel=$id('collabNodePanel'),r=run,nid=nodeId;if(!r)return;
    const definition=r.workflowSnapshot?.nodes?.find(n=>n.id===nid),nr=r.nodes?.[nid]||{};
    panel.hidden=false;panel.replaceChildren();
    const viewingAttempt=nodeAttempt||nr.attempt||0,historical=nodeAttempt!==0&&nodeAttempt!==nr.attempt;const header=document.createElement('header');const title=document.createElement('b');title.textContent=(definition?.name||nid)+' · '+(historical?'历史执行':label(nr.status))+' · '+(viewingAttempt<0?'次数未记录':'第 '+viewingAttempt+' 次');const close=document.createElement('button');close.className='button ghost small';close.textContent='返回 Main Bot 对话';close.onclick=()=>returnToPM();header.append(title,close);panel.append(header);if((nr.attempt||0)>1){const select=document.createElement('select');select.className='collab-attempt-select';select.setAttribute('aria-label','选择节点执行次数');for(let a=nr.attempt;a>=1;a--){const opt=document.createElement('option');opt.value=a===nr.attempt?'0':String(a);opt.textContent='第 '+a+' 次'+(a===nr.attempt?' · 当前':' · 历史');select.append(opt)}const unknown=document.createElement('option');unknown.value='-1';unknown.textContent='次数未记录的历史';select.append(unknown);select.value=String(nodeAttempt);select.onchange=()=>{nodeAttempt=Number(select.value);paintNode()};header.insertBefore(select,close)}
    const actions=document.createElement('div');actions.className='collab-run-actions';
    if(!historical&&!r.pauseRequested&&nr.status==='waiting_approval'&&!isGroup()){
      const decision=(text,kind,target)=>{const b=document.createElement('button');b.type='button';b.className='button ghost small';b.textContent=text;b.onclick=async()=>{try{b.disabled=true;const body={nodeId:nid,expected:{status:nr.status,attempt:nr.attempt||0,reworkCount:nr.reworkCount||0},targetId:target};if(kind==='reject'){const reason=window.prompt('说明返工原因（打回配置的上游节点）');if(!reason?.trim())return;body.reason=reason}await api('/api/runs/'+encodeURIComponent(r.id)+'/'+kind,{method:'POST',body:JSON.stringify(body)});await refresh()}catch(err){error(err)}finally{b.disabled=false}};actions.append(b)};
      decision('批准此轮','approve');
      if(definition?.rejectTo)decision('打回上游','reject',definition.rejectTo);
    }
    if(definition?.agentId){const b=document.createElement('button');b.type='button';b.className='button ghost small';b.textContent='@此角色咨询';b.onclick=async()=>{await returnToPM();addReference({type:'agent',id:definition.agentId,name:definition.name||definition.agentId})};actions.append(b)}
    const discuss=document.createElement('button');discuss.type='button';discuss.className='button ghost small';discuss.textContent='让 Main Bot 分析 / 返工';discuss.onclick=async()=>{await returnToPM();addReference({type:'run',id:r.id,name:r.workflowSnapshot?.name||r.id});input.value='请分析节点「'+(definition?.name||nid)+'」第 '+viewingAttempt+' 次执行的情况，并建议是否需要按 SOP 返工。';input.focus();assistant.sync()};actions.append(discuss);panel.append(actions);
    const feed=document.createElement('div');feed.className='collab-node-feed';feed.hidden=true;panel.append(feed);paintNodeEvents(feed,[...nodeLiveEvents.values()],nr.childRunId?'':nid,nr.childRunId||r.id);
    const note=document.createElement('p');note.className='collab-sidebar-empty';note.textContent=nr.activity||'正在接收节点执行过程…';feed.append(note);
    try{
      const scope=nr.childRunId||r.id,params=new URLSearchParams({nodeId:nr.childRunId?'':nid,limit:'200',expanded:'true'});
      const data=await api('/api/runs/'+encodeURIComponent(scope)+'/telemetry?'+params,{silent:true});
      if(currentRun!==r.id||nodeId!==nid||!panel.contains(feed))return;
      for(const event of data.events||[])nodeLiveEvents.set(scope+'/'+(event.id||event.sequence),{...event,scope});paintNodeEvents(feed,[...nodeLiveEvents.values()],nr.childRunId?'':nid,scope);if(nr.childRunId)connectChildStream(nr.childRunId);if(data.hasMore&&data.nextBefore){const more=document.createElement('button');more.type='button';more.className='button ghost small';more.textContent='加载更早的执行过程';let before=data.nextBefore;more.onclick=async()=>{more.disabled=true;try{params.set('before',String(before));const older=await api('/api/runs/'+encodeURIComponent(scope)+'/telemetry?'+params,{silent:true});if(!panel.contains(more))return;for(const event of older.events||[])nodeLiveEvents.set(scope+'/'+(event.id||event.sequence),{...event,scope});paintNodeEvents(feed,[...nodeLiveEvents.values()],nr.childRunId?'':nid,scope);before=older.nextBefore;more.hidden=!older.hasMore||!before}catch(err){error(err)}finally{more.disabled=false}};actions.append(more)}

    }catch(err){if(panel.contains(feed))toast('执行过程暂时不可用：'+err.message)}
  }
  const nodeLiveEvents=new Map();let liveRunStream=null,childRunStream=null,childStreamID="";
  async function returnToPM(){const draft=input.value,context={references:[...references],attachments:[...attachments]};nodeId='';$id('collabNodePanel').hidden=true;assistant.leaveNode();renderPipeline();await assistant.openSession(assistant.sessionId());paintGroupFeed();paintTaskWelcome();references=context.references;attachments=context.attachments;input.value=draft;paintContext()}
  function paintNodeEvents(feed,events,nid,scope=currentRun){
    if(!nodeId||!run)return;
    const nr=run.nodes?.[nodeId]||{},attempt=nodeAttempt||nr.attempt||0;const unique=new Map();for(const e of events)if((!nid||e.nodeId===nid)&&(!e.scope||e.scope===scope)){let ea=nr.childRunId&&scope===nr.childRunId?nr.attempt:Number(e.context?.attempt||e.attributes?.attempt||0);if(!ea&&nr.attempt<=1)ea=nr.attempt||0;if(!ea){const starts=(run.events||[]).filter(x=>x.nodeId===nodeId&&x.type==='node.started').sort((a,b)=>new Date(a.timestamp)-new Date(b.timestamp));if(starts.length===nr.attempt){const position=starts.findLastIndex(x=>new Date(x.timestamp)<=new Date(e.timestamp));if(position>=0)ea=position+1}else if(nr.startedAt&&new Date(nr.startedAt).getFullYear()>1970&&new Date(e.timestamp)>=new Date(nr.startedAt))ea=nr.attempt;}if(ea===attempt||(attempt<0&&!ea))unique.set(e.id||e.sequence,e);}
    const selected=[...unique.values()].sort((a,b)=>a.sequence-b.sequence),definition=run.workflowSnapshot?.nodes?.find(n=>n.id===nodeId),role=state.data?.agents?.find(a=>a.id===definition?.agentId);
    const historical=nodeAttempt!==0&&nodeAttempt!==nr.attempt;assistant.showNode({key:scope+'/'+nodeId+'/'+attempt,attempt,historical,startedAt:historical?selected.find(e=>e.timestamp)?.timestamp:nr.startedAt,finishedAt:historical?'':nr.finishedAt,name:role?.name||definition?.name||nodeId,type:definition?.type,status:historical?'history':nr.status,output:historical?'':nr.output,error:historical?'':nr.error,events:selected});
  }
  function connectRunStream(id){
    liveRunStream?.close();childRunStream?.close();childRunStream=null;childStreamID="";liveRunStream=null;nodeLiveEvents.clear();groupLoads.clear();if(!id)return;
    const stream=new EventSource('/api/runs/'+encodeURIComponent(id)+'/stream');liveRunStream=stream;
    stream.addEventListener('run',e=>{if(currentRun!==id)return;try{const data=JSON.parse(e.data),oldStatus=run?.nodes?.[nodeId]?.status;for(const event of data.telemetryDelta||[])nodeLiveEvents.set(id+'/'+(event.id||event.sequence),{...event,scope:id});while(nodeLiveEvents.size>500)nodeLiveEvents.delete(nodeLiveEvents.keys().next().value);if(data.run)run={...run,...data.run,workflowSnapshot:data.run.workflowSnapshot||run?.workflowSnapshot};renderPipeline();scheduleGroupState();const panel=$id('collabNodePanel');if(nodeId&&!panel.hidden){if(oldStatus!==run?.nodes?.[nodeId]?.status)paintNode();else {const feed=panel.querySelector('.collab-node-feed');if(feed&&!run?.nodes?.[nodeId]?.childRunId)paintNodeEvents(feed,[...nodeLiveEvents.values()],nodeId)}}}catch(err){error(err)}});
  }
  function connectChildStream(id){if(childStreamID===id)return;childRunStream?.close();childStreamID=id;const stream=new EventSource('/api/runs/'+encodeURIComponent(id)+'/stream');childRunStream=stream;stream.addEventListener('run',e=>{if(childStreamID!==id)return;try{const data=JSON.parse(e.data);for(const event of data.telemetryDelta||[])nodeLiveEvents.set(id+'/'+(event.id||event.sequence),{...event,scope:id});while(nodeLiveEvents.size>500)nodeLiveEvents.delete(nodeLiveEvents.keys().next().value);const feed=$id('collabNodePanel').querySelector('.collab-node-feed');if(feed&&run?.nodes?.[nodeId]?.childRunId===id)paintNodeEvents(feed,[...nodeLiveEvents.values()],'',id)}catch{}})}
  document.addEventListener('pointerdown',e=>{
    // Node selection is a chat context, dismissed explicitly via Return to PM.
    const inspector=$id('collabInspector');if(inspector&&!inspector.hidden&&!inspector.contains(e.target)&&!e.target.closest('#collabInspect,.collab-artifact-dialog')){inspector.hidden=true;inspectorGeneration++}
    if(!picker.hidden&&!picker.contains(e.target)&&e.target!==input&&!mention.contains(e.target))picker.hidden=true;
    document.querySelectorAll('details.collab-settings[open]').forEach(menu=>{if(!menu.contains(e.target))menu.open=false});
  });

  async function previewArtifact(id,artifact,metadata){
    const dialog=document.createElement('dialog');dialog.className='collab-artifact-dialog';dialog.setAttribute('aria-label','产物预览：'+artifact.name);
    const header=document.createElement('header'),heading=document.createElement('div'),title=document.createElement('h2'),meta=document.createElement('p'),close=document.createElement('button');title.textContent=artifact.name;meta.textContent=metadata;heading.append(title,meta);close.type='button';close.className='button ghost small';close.textContent='关闭';close.onclick=()=>dialog.close();header.append(heading,close);
    const content=document.createElement('div');content.className='collab-artifact-preview';content.textContent='正在加载产物…';content.setAttribute('aria-live','polite');
    const footer=document.createElement('footer'),path=document.createElement('span'),download=document.createElement('a');path.textContent=artifact.path||artifact.mediaType;download.className='button small';download.textContent='下载产物';download.href='/api/runs/'+encodeURIComponent(id)+'/artifacts/'+encodeURIComponent(artifact.id);download.download=(artifact.path||artifact.name).split('/').pop();footer.append(path,download);
    dialog.append(header,content,footer);document.body.append(dialog);dismissDialog(dialog);const controller=new AbortController();dialog.addEventListener('close',()=>controller.abort(),{once:true});dialog.showModal();close.focus();
    try{
      const response=await fetch(download.href,{signal:controller.signal});if(!response.ok)throw new Error('产物加载失败（HTTP '+response.status+'）');
      const text=await response.text();if(!dialog.open)return;content.replaceChildren();
      if(isMarkdownArtifact(artifact,text)){content.classList.add('markdown-body');content.innerHTML=renderMarkdown(text)}
      else{const pre=document.createElement('pre');pre.textContent=text;content.append(pre)}
    }catch(err){if(dialog.open){content.classList.add('is-error');content.textContent=err.message}}
  }

  async function inspect(){
    if(!currentRun)return;
    const generation=++inspectorGeneration,id=currentRun,host=$id('collabInspectorBody');$id('collabInspector').hidden=false;host.replaceChildren();
    const loading=document.createElement('p');loading.className='collab-artifact-empty';loading.textContent='正在加载产物…';host.append(loading);
    try{
      const detail=await api('/api/runs/'+encodeURIComponent(id),{silent:true});if(generation!==inspectorGeneration||id!==currentRun)return;host.replaceChildren();
      const artifacts=[...(detail.artifacts||[])].sort((a,b)=>new Date(b.createdAt)-new Date(a.createdAt));
      const caption=document.createElement('p');caption.className='collab-artifact-caption';caption.textContent=artifacts.length+' 个归档产物 · 按执行记录保留';host.append(caption);
      for(const a of artifacts){const card=document.createElement('button');card.type='button';card.className='collab-artifact-card';card.onclick=()=>previewArtifact(id,a,meta.textContent);
        const mark=document.createElement('span');mark.className='collab-artifact-icon';mark.append(icon('issue'));const copy=document.createElement('div'),name=document.createElement('b'),meta=document.createElement('small'),path=document.createElement('span');name.textContent=a.name;const node=detail.workflowSnapshot?.nodes?.find(n=>n.id===a.nodeId)||detail.workflowSnapshot?.groupHistory?.find(h=>h.node.id===a.nodeId)?.node;meta.textContent=[node?.name||a.nodeId,'第 '+a.attempt+' 次',a.size<1024?a.size+' B':(a.size/1024).toFixed(1)+' KB'].join(' · ');path.textContent=a.path||a.mediaType;copy.append(name,meta,path);const arrow=icon('chevron');card.append(mark,copy,arrow);host.append(card)}
      if(!artifacts.length){const empty=document.createElement('p');empty.className='collab-artifact-empty';empty.textContent='暂无归档产物。角色完成执行后，交付文件会显示在这里。';host.append(empty)}
    }catch(err){if(generation===inspectorGeneration){loading.textContent=err.message;host.replaceChildren(loading)}}
  }
  $id('collabNew').onclick=()=>{if(busy())return toast('请先停止当前回复');switchView('collaboration');closeRail();assistant.reset();restoreRun('');refresh()};
  $id('collabClearRun').onclick=$id('collabNew').onclick;
  $id('collabClearRun').replaceChildren(icon('back'),document.createTextNode('普通会话'));$id('collabInspect').replaceChildren(icon('repository'),document.createTextNode('工作区'));
  $id('collabRefresh').onclick=async()=>{const b=$id('collabRefresh');b.disabled=true;b.classList.add('is-refreshing');try{await refresh()}finally{b.disabled=false;b.classList.remove('is-refreshing')}};
  const toggleIDE=document.createElement('button');toggleIDE.id='collabToggleIDE';toggleIDE.type='button';toggleIDE.className='button small collab-ide-toggle';toggleIDE.append(icon('repository'),document.createTextNode('切换 IDE'));toggleIDE.setAttribute('aria-pressed','false');toggleIDE.hidden=!currentRun;toggleIDE.onclick=()=>currentRun&&showIDE(!ideVisible);$id('collabInspect').after(toggleIDE);
 $id('collabInspect').onclick=()=>inspect();
  $id('collabCloseInspector').onclick=()=>{$id('collabInspector').hidden=true;inspectorGeneration++};

  function poll(){clearTimeout(refreshTimer);refreshTimer=setTimeout(async()=>{await refresh();poll()},5000)}
  function activate(value){active=value;root.hidden=!value;document.body.classList.remove('ai-assistant-open');clearTimeout(refreshTimer);if(value)assistant.open();refresh();poll()}
  window.addEventListener('assistant-session-changed',()=>{refresh()});
  document.addEventListener('change',e=>{if(e.target.id!=='projectSwitcher')return;requestGeneration++;references=[];attachments=[];restoreRun('');assistant.reset();refresh()});
  renderPipeline();
  window.AgentFlowCollaboration={handoffPending:sopReturnPending,activate,groupMode:()=>isGroup(),refreshGroupChat:paintGroupFeed,runId:()=>currentRun,restoreRun,renderMessageContext,
    hasAttachments:()=>attachments.length>0,
    takeComposer:()=>{const data={references:[...references],attachments:[...attachments]};references=[];attachments=[];paintContext();return data},
    clearComposer:()=>{references=[];attachments=[];picker.hidden=true;paintContext()},
    nodeSelected:()=>!!nodeId,restoreComposer:m=>{references=m?.references||[];attachments=m?.attachments||[];paintContext()}};
  activate(state.currentView==='collaboration');
})();
