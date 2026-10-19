/* Conversation transcripts are stored per-user on the server, never with API keys. */
(() => {
  const shell=document.querySelector('.app-shell')||document.body;
  const root=document.createElement('aside');
  root.id='aiAssistant';root.className='ai-assistant';root.hidden=true;
  root.setAttribute('role','complementary');root.setAttribute('aria-label','AI 助手');
  root.innerHTML=`<button type="button" class="ai-resize" data-resize title="拖动调节宽度，双击恢复默认" aria-label="拖动调节助手宽度"></button>
    <header class="ai-head">
      <div><b>AI 助手</b><span data-project-hint>跟随顶栏项目</span></div>
      <div class="ai-head-actions">
        <button type="button" class="button ghost small" data-history aria-haspopup="true" aria-expanded="false">历史</button>
        <button type="button" class="button ghost small" data-new>新对话</button>
        <button type="button" class="ai-icon-btn" data-close aria-label="关闭 AI 助手">×</button>
      </div>
    </header>
    <div class="ai-history-menu" hidden role="menu"></div>
    <div class="ai-messages" role="log" aria-live="polite"></div>
    <form class="ai-compose">
      <div class="ai-compose-box">
        <textarea class="ai-compose-input" aria-label="发送给 AI 助手的消息" placeholder="快来与我对话吧（Shift+Enter 换行）" rows="2" maxlength="8000"></textarea>
        <div class="ai-compose-bar">
          <div class="ai-compose-model">
            <select class="ai-executor-select" data-executor-select aria-label="执行器"><option value="codebuddy">CodeBuddy</option><option value="codex">Codex</option><option value="claude">Claude Code</option></select>
            <button type="button" class="ai-model-toggle" data-model-toggle aria-haspopup="listbox" aria-expanded="false">
              <span class="ai-model-icon" data-family="glm">智</span>
              <span data-model-label>GLM-5.3-Flash</span>
              <span class="ai-model-caret" aria-hidden="true"></span>
            </button>
            <div class="ai-model-menu" hidden role="listbox"></div>
          </div>
          <div class="ai-compose-mode">
            <button type="button" class="ai-model-toggle ai-mode-toggle" data-mode-toggle aria-haspopup="listbox" aria-expanded="false" title="审批模式">
              <span class="ai-mode-icon" aria-hidden="true">
                <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M9 12l2 2 4-5"/><path d="M12 3l7 4v5c0 4.4-3 8.1-7 9-4-.9-7-4.6-7-9V7z"/></svg>
              </span>
              <span data-mode-label>每次确认</span>
              <span class="ai-model-caret" aria-hidden="true"></span>
            </button>
            <div class="ai-model-menu ai-mode-menu" hidden role="listbox"></div>
          </div>
          <small data-compose-hint hidden role="status"><span>未配置 API Key，暂时无法发送</span><button type="button" class="button ghost small" data-configure-credentials>配置 API Key</button></small>
          <div class="ai-compose-tools">
            <button type="button" class="ai-tool-btn" data-stop hidden aria-label="停止">
              <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true"><rect x="6" y="6" width="12" height="12" rx="2" fill="currentColor"/></svg>
            </button>
            <button type="submit" class="ai-send-btn" aria-label="发送">
              <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 2 11 13"/><path d="M22 2 15 22l-4-9-9-4 20-7z"/></svg>
            </button>
          </div>
        </div>
      </div>
    </form>`;
  shell.append(root);

  const launch=document.createElement('button');
  launch.className='button ghost small ai-launch';launch.type='button';
  launch.innerHTML='<span aria-hidden="true">✦</span><b>AI 助手</b>';
  launch.setAttribute('aria-controls','aiAssistant');
  launch.setAttribute('aria-expanded','false');
  launch.title='打开 AI 助手';
  const actions=document.querySelector('.top-actions');
  if(actions){
    const theme=actions.querySelector('#themeToggle');
    actions.insertBefore(launch, theme||actions.firstChild);
  }else{
    document.body.append(launch);
  }

  const log=root.querySelector('.ai-messages');
  log.addEventListener('click',e=>{
    const b=e.target.closest('.md-code-copy');
    if(!b||!log.contains(b))return;
    copyText(b.closest('.md-code-block')?.querySelector('pre code')?.textContent||'','已复制');
  });
  let redirecting=false;
  const input=root.querySelector('.ai-compose textarea');
  const historyMenu=root.querySelector('.ai-history-menu');
  const modelMenu=root.querySelector('.ai-compose .ai-model-menu');
  const modelToggle=root.querySelector('.ai-compose [data-model-toggle]');
  const executorSelect=root.querySelector('[data-executor-select]');
  let executor=['codebuddy','codex','claude'].includes(localStorage.getItem('loopforge.assistant.executor'))?localStorage.getItem('loopforge.assistant.executor'):'codebuddy',providerCatalogs={};
  executorSelect.value=executor;
  function composerCatalog(){return executor==='codebuddy'?models:(providerCatalogs[executor]||{default:'default',models:[{id:'default',name:'CLI default',family:executor==='codex'?'gpt':'claude'}]})}
  const modeMenu=root.querySelector('.ai-compose .ai-mode-menu');
  const modeToggle=root.querySelector('.ai-compose [data-mode-toggle]');
  const DEFAULT_MODEL='default';
  const PANEL_MIN=280, PANEL_DEFAULT=380, PANEL_KEY='agentflow.assistantWidth';
  const MODE_KEY='agentflow.assistantApprovalMode';
  const APPROVAL_MODES=[
    {id:'confirm',name:'请求批准',desc:'写入平台数据时始终询问，由你批准后执行'},
    {id:'auto',name:'完全访问权限',desc:'无需逐项批准，直接执行当前用户有权限的操作'}
  ];
  function panelMax(){
    const nav=window.innerWidth<=900?76:232;
    return Math.max(PANEL_MIN, window.innerWidth-nav-240);
  }
  function currentPanelWidth(){
    return parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--ai-panel-w'))||PANEL_DEFAULT;
  }
  function applyPanelWidth(px){
    const w=Math.round(Math.min(panelMax(), Math.max(PANEL_MIN, Number(px)||PANEL_DEFAULT)));
    document.documentElement.style.setProperty('--ai-panel-w', w+'px');
    return w;
  }
  function persistPanelWidth(){
    localStorage.setItem(PANEL_KEY, String(currentPanelWidth()));
  }
  applyPanelWidth(Number(localStorage.getItem(PANEL_KEY))||PANEL_DEFAULT);
  window.addEventListener('resize',()=>applyPanelWidth(currentPanelWidth()));
  (function bindAssistantResize(){
    const handle=root.querySelector('[data-resize]');
    handle.onpointerdown=ev=>{
      if(ev.button!=null&&ev.button!==0)return;
      ev.preventDefault();
      const startX=ev.clientX;
      const startW=root.getBoundingClientRect().width||currentPanelWidth();
      handle.setPointerCapture(ev.pointerId);
      handle.classList.add('dragging');
      document.body.classList.add('ai-assistant-resizing');
      handle.onpointermove=move=>{
        applyPanelWidth(startW+(startX-move.clientX));
      };
      const stop=()=>{
        handle.classList.remove('dragging');
        document.body.classList.remove('ai-assistant-resizing');
        handle.onpointermove=null;
        handle.onpointerup=null;
        handle.onpointercancel=null;
        persistPanelWidth();
      };
      handle.onpointerup=stop;
      handle.onpointercancel=stop;
    };
    handle.ondblclick=()=>{applyPanelWidth(PANEL_DEFAULT);persistPanelWidth()};
    handle.onkeydown=ev=>{
      const cur=root.getBoundingClientRect().width||currentPanelWidth();
      if(ev.key==='ArrowLeft'){ev.preventDefault();applyPanelWidth(cur+16);persistPanelWidth()}
      if(ev.key==='ArrowRight'){ev.preventDefault();applyPanelWidth(cur-16);persistPanelWidth()}
    };
  })();
  const FAMILY_MARK={claude:'',gpt:'',glm:'智',gemini:'G',kimi:'K',deepseek:'D',minimax:'M',hunyuan:'混',echo:'E',other:'AI'};
  const FAMILY_ICON={
    claude:'/images/credentials/claude.svg',
    gpt:'/images/credentials/codex.svg',
    glm:'/images/credentials/glm.svg',
    gemini:'/images/credentials/gemini.svg',
    kimi:'/images/credentials/kimi.svg',
    deepseek:'/images/credentials/deepseek.svg',
    minimax:'/images/credentials/minimax.svg',
    hunyuan:'/images/credentials/hunyuan.svg'
  };
  let messages=[],controller=null,owner='',saving=false,sessionId='',sharedSession=false,sessionUpdatedAt='',model=DEFAULT_MODEL,models={default:DEFAULT_MODEL,models:[{id:DEFAULT_MODEL,name:'GLM-5.3-Flash',family:'glm',credits:'x0.06'}]};
  let approvalMode=APPROVAL_MODES.some(m=>m.id===localStorage.getItem(MODE_KEY))?localStorage.getItem(MODE_KEY):'confirm';

  function projectId(){return state.projectFilter||''}
  function projectName(){
    const id=projectId();
    return ((state.data?.projects||[]).find(p=>p.id===id)||{}).name||(id?'未命名项目':'未选择项目（仅问答）');
  }
  function hasKey(){const field={codebuddy:'codeBuddyApiKeyConfigured',codex:'codexApiKeyConfigured',claude:'claudeCodeApiKeyConfigured'}[executor];return !!(userById(state.currentUser?.id)||{})[field]}
  function modelMeta(id){return (models.models||[]).find(m=>m.id===id)||{id:id||DEFAULT_MODEL,family:familyOf(id),name:id||DEFAULT_MODEL}}
  function familyOf(id){
    const v=String(id||'').toLowerCase();
    if(v.startsWith('claude'))return 'claude';
    if(v.startsWith('gpt')||v.includes('codex'))return 'gpt';
    if(v.startsWith('glm'))return 'glm';
    if(v.startsWith('gemini'))return 'gemini';
    if(v.startsWith('kimi'))return 'kimi';
    if(v.startsWith('deepseek'))return 'deepseek';
    if(v.startsWith('minimax'))return 'minimax';
    if(v.startsWith('hy')||v.includes('hunyuan'))return 'hunyuan';
    if(v==='echo')return 'echo';
    return 'other';
  }
  function fillModelIcon(el, family, id){
    if(!el)return;
    el.dataset.family=family||'other';
    const src=FAMILY_ICON[family];
    if(src){el.innerHTML=`<img src="${src}" alt="">`;return}
    el.textContent=FAMILY_MARK[family]||String(id||'AI').slice(0,1).toUpperCase();
  }
  function creditLabel(item){
    const credits=String(item?.credits||'').trim();
    if(!credits)return '';
    return credits.endsWith('credits')?credits:credits+' credits';
  }
  function paintModelToggle(toggle, selectedId){
    if(!toggle)return;
    const meta=modelMeta(selectedId);
    const icon=toggle.querySelector('.ai-model-icon');
    if(icon) fillModelIcon(icon, meta.family||familyOf(meta.id), meta.id);
    const label=toggle.querySelector('[data-model-label]');
    if(label)label.textContent=meta.name||meta.id;
    const credits=creditLabel(meta);
    toggle.title=credits?`${meta.name||meta.id} ${credits}`:(meta.id||'');
  }
  function renderModelList(menu, selectedId, onPick){
    renderOptionList(menu, models.models||[], selectedId, onPick);
  }
  // renderOptionList 渲染任意 {id,name,family,credits,icon} 选项列表，供
  // 角色向导等场景复用同一套 ai-model-* 下拉样式。icon:false 跳过图标位。
  function renderOptionList(menu, items, selectedId, onPick){
    if(!menu)return;
    menu.replaceChildren();
    for(const item of items||[]){
      const opt=document.createElement('button');
      opt.type='button';opt.setAttribute('role','option');opt.className='ai-model-option'+(item.id===selectedId?' is-active':'');
      opt.dataset.id=item.id;
      if(item.icon!==false){
        const icon=document.createElement('span');icon.className='ai-model-icon';
        fillModelIcon(icon, item.family||familyOf(item.id), item.id);
        opt.append(icon);
      }
      const label=document.createElement('span');label.className='ai-model-name';label.textContent=item.name||item.id;label.title=item.name?`${item.name} (${item.id})`:item.id;
      const credits=document.createElement('span');credits.className='ai-model-credits';credits.textContent=creditLabel(item);
      opt.append(label,credits);
      opt.onclick=()=>{
        try{
          onPick(item.id);
        }finally{
          menu.hidden=true;
          const t=menu.parentElement?.querySelector('[data-model-toggle]');
          if(t) t.setAttribute('aria-expanded','false');
          closeMenus();
        }
      };
      menu.append(opt);
    }
  }
  // paintItem 按给定的 {id,name,family,credits} 绘制一个 ai-model-toggle。
  function paintItem(toggle, item){
    if(!toggle)return;
    const id=item?.id||'';
    const icon=toggle.querySelector('.ai-model-icon');
    if(icon) fillModelIcon(icon, item?.family||familyOf(id), id);
    const label=toggle.querySelector('[data-model-label]');
    if(label)label.textContent=item?.name||id||'选择模型';
    const credits=creditLabel(item);
    toggle.title=credits?`${item?.name||id} ${credits}`:(item?.name||id||'');
  }
  function renderModelToggle(){
    const meta=(composerCatalog().models||[]).find(m=>m.id===model)||{id:model,name:model,family:executor==='codex'?'gpt':executor==='claude'?'claude':familyOf(model)};
    paintItem(modelToggle,meta);
  }
  function renderModelMenu(){
    renderOptionList(modelMenu,composerCatalog().models||[],model,id=>{model=id;renderModelToggle()});
    if(executor!=='codebuddy'){
      const row=document.createElement('div');row.className='ai-custom-model';
      const field=document.createElement('input');field.type='text';field.maxLength=256;field.placeholder=lfT('自定义模型 ID','Custom model ID');field.setAttribute('aria-label',field.placeholder);
      const save=document.createElement('button');save.type='button';save.className='button ghost small';save.textContent=lfT('使用','Use');row.append(field,save);
      save.onclick=()=>{const id=field.value.trim();if(id){model=id;renderModelToggle();closeMenus()}};field.onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();e.stopPropagation();save.click()}};modelMenu.append(row);
    }
  }
  executorSelect.onchange=async()=>{
    executor=executorSelect.value;localStorage.setItem('loopforge.assistant.executor',executor);
    model=composerCatalog().default||'default';renderModelToggle();renderModelMenu();syncChrome();await loadProviderModels();
  };
  async function loadProviderModels(){
    if(executor==='codebuddy')return;
    const selected=executor;
    try{const catalog=await api('/api/assistant/models?executor='+selected,{silent:true});if(catalog?.models?.length)providerCatalogs[selected]=catalog}catch{}
    if(selected===executor){renderModelToggle();renderModelMenu()}
  }
  function renderModeToggle(){
    const meta=APPROVAL_MODES.find(m=>m.id===approvalMode)||APPROVAL_MODES[0];
    const label=modeToggle.querySelector('[data-mode-label]');
    if(label)label.textContent=meta.name;
    modeToggle.title=meta.desc;modeToggle.classList.toggle('is-full-access',approvalMode==='auto');
  }
  function renderModeMenu(){
    if(!modeMenu)return;
    modeMenu.replaceChildren();const heading=document.createElement('div');heading.className='ai-mode-heading';heading.textContent='应如何批准 AI 操作？';modeMenu.append(heading);
    for(const item of APPROVAL_MODES){
      const opt=document.createElement('button');
      opt.type='button';opt.setAttribute('role','option');opt.className='ai-model-option ai-mode-option'+(item.id===approvalMode?' is-active':'');
      opt.dataset.id=item.id;
      const label=document.createElement('span');label.className='ai-model-name';label.textContent=item.name;
      const desc=document.createElement('span');desc.className='ai-mode-desc';desc.textContent=item.desc;
      const copy=document.createElement('span');copy.className='ai-mode-copy';copy.append(label,desc);const icon=document.createElement('span');icon.className='ai-mode-symbol';icon.innerHTML='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><path d="M12 3 4 6v6c0 5 8 9 8 9s8-4 8-9V6z"/><path d="M12 7v6m0 3v1"/></svg>';const check=document.createElement('span');check.className='ai-mode-check';check.textContent=item.id===approvalMode?'✓':'';opt.classList.toggle('is-full-access',item.id==='auto');opt.append(icon,copy,check);
      opt.onclick=()=>{
        approvalMode=item.id;
        localStorage.setItem(MODE_KEY,approvalMode);
        renderModeToggle();
        modeMenu.hidden=true;
        modeToggle.setAttribute('aria-expanded','false');
      };
      modeMenu.append(opt);
    }
  }
  function closeMenus(){
    historyMenu.hidden=true;
    modelMenu.hidden=true;
    modeMenu.hidden=true;
    root.querySelector('[data-history]').setAttribute('aria-expanded','false');
    modelToggle.setAttribute('aria-expanded','false');
    modeToggle.setAttribute('aria-expanded','false');
  }
  let nodeView=null;
  function add(role,text){
    root.classList.remove('is-empty');
    log.querySelector('.ai-welcome')?.remove();
    const el=document.createElement('div');
    el.className=`ai-message ai-role-${role}`;
    if(role==='user'){const bubble=document.createElement('div');bubble.className='ai-user-bubble';bubble.textContent=text;el.append(bubble)}else el.textContent=text;
    el.dataset.chatTime=String(Date.now());log.append(el);
    log.scrollTop=log.scrollHeight;
    return el;
  }
  function setOpen(open){
    root.hidden=!open;
    launch.classList.toggle('is-open',open);
    launch.setAttribute('aria-expanded',String(open));
    document.body.classList.toggle('ai-assistant-open',open);
    if(root.classList.contains('ai-home'))document.body.classList.remove('ai-assistant-open');
    if(!open)closeMenus()
  }
  function shortcuts(){
    const box=document.createElement('div');box.className='ai-shortcuts';
    for(const q of ['帮我梳理当前项目的待办需求','我有一个想法，先帮我讨论需求和验收条件','帮我选择适合这个需求的 SOP','看看当前任务进展，分析阻塞点和下一步']){
      const b=document.createElement('button');b.type='button';b.textContent=q;
      b.onclick=()=>{if(input.disabled)return;input.value=q;input.focus();root.querySelector('.ai-compose').requestSubmit()};
      box.append(b);
    }
    return box;
  }
  function activeSessionKey(){return 'agentflow:last-chat:'+String(state.currentUser?.id||state.currentUser?.loginName||'')+':'+projectId()}
  function rememberSession(){if(sessionId)localStorage.setItem(activeSessionKey(),sessionId);else localStorage.removeItem(activeSessionKey())}
  let openEpoch=0,generationId='',generationActorID=0,generationTrigger='';
  function welcome(){
    log.replaceChildren();root.classList.add('is-empty');
    const hero=document.createElement('div');hero.className='ai-welcome';
    const title=document.createElement('h1');title.textContent='我们从哪里开始？';
    const subtitle=document.createElement('p');subtitle.textContent='讨论想法、推进需求，或一起完成一次交付。';hero.append(title,subtitle);log.append(hero);
    onboardingSignature='';paintOnboarding();
    requestAnimationFrame(()=>{if(log.contains(hero))log.scrollTop=0});
  }
  const ONBOARDING_DISMISSED='loopforge.onboarding.dismissed',ONBOARDING_CHATTED='loopforge.onboarding.chatted',ONBOARDING_DEMO='loopforge.onboarding.demo';
  let onboardingSignature='';
  function onboardingSteps(){
    const data=state.data||{};
    return [
      {id:'key',done:hasKey(),title:'配置模型 API Key',desc:'AI 聊天和 Agent 任务需要模型 API Key；下面的脚本体验无需 Key。',action:'去配置'},
      {id:'project',done:myProjects().length>0||(typeof isPlatformAdmin==='function'&&isPlatformAdmin()&&(data.projects||[]).length>0),title:'准备一个项目',desc:'项目用来归集需求、SOP 和运行记录。可以直接用体验项目，也可以新建项目，不要求绑定仓库。',action:'新建项目'},
      {id:'chat',done:localStorage.getItem(ONBOARDING_CHATTED)==='1',title:'了解 LoopForge 能做什么',desc:'让 Main Bot 介绍可用能力、推荐上手路径，并说明哪些操作需要你确认。',action:'问问能做什么'},
      {id:'run',done:(data.runs||[]).length>0||localStorage.getItem(ONBOARDING_DEMO)==='1',title:'跑通第一条 SOP',desc:'一键在体验项目里跑一条示例 SOP：脚本生成报告 → 你来审批 → 继续执行，无需 API Key。',action:'一键体验'},
      {id:'repo',optional:true,done:projectRepos().length>0&&gitTokenReady(),title:'接入 GitHub 代码仓库',desc:'分析或修改仓库代码、提交 PR 时再配置仓库和 GitHub Token；聊天、文档生成及无仓库 SOP 可以跳过。',action:projectRepos().length?'配置 Token':'添加仓库'}
    ];
  }
  function myProjects(){const id=state.currentUser?.id;return (state.data?.projects||[]).filter(p=>id&&(p.ownerId===id||(p.members||[]).some(m=>m.userId===id)))}
  function projectRepos(){return (state.data?.repositories||[]).filter(r=>r.projectId===projectId())}
  function gitTokenReady(){return !!(userById(state.currentUser?.id)||{}).gitTokenConfigured}
  function paintOnboarding(){
    const hero=log.querySelector('.ai-welcome');
    const previous=root.querySelector('.ai-onboarding');
    if(!hero||groupChat()||window.AgentFlowCollaboration?.runId?.()){previous?.remove();onboardingSignature='';return}
    const steps=onboardingSteps(),required=steps.filter(s=>!s.optional),left=required.filter(s=>!s.done).length;
    const hidden=localStorage.getItem(ONBOARDING_DISMISSED)==='1'||!left;
    const signature=hidden?'hidden':JSON.stringify(steps);
    if(signature===onboardingSignature)return;onboardingSignature=signature;
    previous?.remove();if(hidden)return;
    const next=required.find(s=>!s.done);
    const box=document.createElement('section');box.className='ai-onboarding';box.setAttribute('aria-label','新手引导');
    box.innerHTML=`<header><b>快速上手</b><span class="ai-onboarding-progress" aria-hidden="true">${required.map(s=>`<i class="${s.done?'is-done':''}"></i>`).join('')}</span><span class="ai-onboarding-count">${required.length-left} / ${required.length} 已完成</span></header><ol>${steps.map((s,i)=>`<li class="${s.done?'is-done':''}${s===next?' is-next':''}" data-step="${s.id}" data-optional="${!!s.optional}"><i aria-hidden="true">${s.done?'✓':s.optional?'＋':i+1}</i><div><strong>${escapeHTML(s.title)}</strong>${s.optional?'<span class="ai-onboarding-optional">可选</span>':''}<p>${escapeHTML(s.desc)}</p></div>${s.done&&s.id!=='chat'?'':`<button type="button" class="button ${s===next?'primary':'ghost'} small" data-onboarding-step="${s.id}">${escapeHTML(s.action)}</button>`}</li>`).join('')}</ol><button type="button" class="ai-onboarding-close" data-onboarding-dismiss aria-label="关闭引导" title="关闭引导">×</button>`;
    box.querySelector('[data-onboarding-dismiss]').onclick=()=>{localStorage.setItem(ONBOARDING_DISMISSED,'1');paintOnboarding()};
    box.querySelectorAll('[data-onboarding-step]').forEach(b=>b.onclick=()=>runOnboardingStep(b.dataset.onboardingStep));
    hero.append(box);
  }
  function runOnboardingStep(id){
    if(id==='key')return root.querySelector('[data-configure-credentials]').click();
    if(id==='project'){switchView('projects');return setTimeout(()=>document.getElementById('newProject')?.click(),50)}
    if(id==='chat'){
      const zh='请介绍 LoopForge 能帮我做什么，并结合当前可用功能推荐一条从零上手的路径。请说明哪些操作需要 API Key、GitHub Token 或人工审批。本次只介绍，不创建、修改或删除任何数据，也不执行任务。';
      const question=window.lfT?window.lfT(zh,'Introduce what LoopForge can help me do and recommend a getting-started path based on its available features. Explain which operations need an API key, a GitHub token or human approval. Only explain: do not create, modify or delete any data, or execute tasks.'):zh;
      if(input.value.trim()&&input.value.trim()!==question){input.focus();return toast('请先发送或保存当前草稿，再来问问能做什么。')}
      input.value=question;syncChrome();
      if(!hasKey()){root.querySelector('[data-configure-credentials]').click();return toast('问题已准备好，配置模型 API Key 后即可发送。')}
      input.focus();
      if(!root.querySelector('.ai-compose [type=submit]').disabled)root.querySelector('.ai-compose').requestSubmit();
      return;
    }
    if(id==='run'){localStorage.setItem(ONBOARDING_DEMO,'1');return window.LoopForgeTour?.startDemoRun()}
    if(id==='repo'){
      if(!projectRepos().length)return openRepositoryModal();
      switchView('settings');
      const field=document.querySelector('#userCredentialForm [name="gitToken"]');
      field?.focus();field?.scrollIntoView({block:'center'});
    }
  }
  function reset(){leaveNode();openEpoch++;controller?.abort();controller=null;generationId='';messages=[];sessionId='';sharedSession=false;sessionUpdatedAt='';rememberSession();welcome();input.value='';window.AgentFlowCollaboration?.clearComposer();syncChrome();window.dispatchEvent(new Event('assistant-session-changed'))}
  function groupChat(){return !!window.AgentFlowCollaboration?.groupMode?.()}
  let renderedGroupMode=false;
  function syncChrome(){
    const group=groupChat();
    if(group!==renderedGroupMode&&!controller&&!saving&&!nodeView){
      renderedGroupMode=group;const top=log.scrollTop,nearEnd=log.scrollHeight-top-log.clientHeight<120;
      log.replaceChildren();if(messages.length)messages.forEach(renderTurn);else welcome();log.scrollTop=nearEnd?log.scrollHeight:top;
    }
    modeToggle.hidden=groupChat();if(groupChat())modeMenu.hidden=true;
    root.querySelector('[data-project-hint]').textContent=projectId()?('项目 · '+projectName()):'未选择项目 · 仅问答';
    const keyed=hasKey();
    const busy=!!controller||saving;
    input.disabled=!!nodeView||saving;
    input.placeholder=!nodeView&&groupChat()?(busy?'继续输入，发送可重新引导':'发消息，@群成员一起协作'):nodeView?'正在查看节点执行过程 · 返回 Main Bot 对话后可发送消息':keyed?(busy?'继续输入，发送可停止当前回复并重新引导':'描述需求或提问，Main Bot 会推荐合适的 SOP；用 @ 引用上下文'):'先写下需求，配置 API Key 后即可发送';
    if(!nodeView&&keyed&&window.AgentFlowCollaboration?.handoffPending?.())input.placeholder='交接正在后台处理，可以先起草消息或切换任务';
    root.querySelector('.ai-compose [type=submit]').disabled=!!nodeView||!keyed||saving||redirecting||!!window.AgentFlowCollaboration?.handoffPending?.()||(!input.value.trim()&&!window.AgentFlowCollaboration?.hasAttachments());
    root.querySelector('[data-stop]').hidden=!!nodeView||!controller;root.querySelector('[data-stop]').disabled=!!controller&&generationTrigger!=='group_followup'&&generationActorID!==0&&generationActorID!==state.currentUser?.id;root.querySelector('[data-stop]').title=generationTrigger!=='group_followup'&&generationActorID&&generationActorID!==state.currentUser?.id?'只有本轮发起人可以停止回复':'停止回复';
    root.querySelector('[data-new]').disabled=saving;
    root.querySelector('[data-history]').disabled=saving;
    root.querySelectorAll('[data-node-context-tool]').forEach(b=>b.disabled=!!nodeView||saving);
    modelToggle.disabled=!!nodeView||busy;
    executorSelect.disabled=!!nodeView||busy;
    modeToggle.disabled=!!nodeView||busy;
    const hint=root.querySelector('[data-compose-hint]');
    hint.hidden=keyed||!!nodeView;
    root.querySelector('.ai-compose')?.classList.toggle('needs-key',!keyed&&!nodeView);
    paintOnboarding();
  }
  function setBusy(busy){
    if(busy){/* controller already set */}
    syncChrome();
  }
  async function loadModels(){
    try{
      const catalog=await api('/api/assistant/models',{silent:true});
      if(catalog?.models?.length){
        models=catalog;
        if(executor==='codebuddy'&&!(models.models||[]).some(m=>m.id===model))model=catalog.default||DEFAULT_MODEL;
      }
    }catch{}
    await loadProviderModels();
    renderModelToggle();
    renderModelMenu();
    return models;
  }
  window.AgentFlowAssistantModels={
    DEFAULT:DEFAULT_MODEL,
    catalog(){return models},
    load:loadModels,
    paint:paintModelToggle,
    paintItem,
    renderList:renderModelList,
    renderOptions:renderOptionList
  };
  async function loadHistoryMenu(){
    historyMenu.replaceChildren();
    const fresh=document.createElement('button');fresh.type='button';fresh.className='ai-history-new';fresh.textContent='新对话';
    fresh.onclick=()=>{reset();closeMenus()};
    historyMenu.append(fresh);
    try{
      const items=(await api('/api/assistant/sessions',{silent:true})).filter(item=>!item.archived);
      if(!items?.length){
        const empty=document.createElement('div');empty.className='ai-history-empty';empty.textContent='还没有历史对话';historyMenu.append(empty);return;
      }
      for(const item of items){
        const row=document.createElement('div');row.className='ai-history-item'+(item.id===sessionId?' is-active':'');
        const open=document.createElement('button');open.type='button';open.className='ai-history-open';
        open.textContent=item.title||'未命名对话';
        open.title=item.updatedAt?new Date(item.updatedAt).toLocaleString():'';
        open.onclick=()=>openSession(item.id);
        const del=document.createElement('button');del.type='button';del.className='ai-history-del';del.setAttribute('aria-label','删除对话');del.textContent='×';
        del.onclick=async e=>{
          e.stopPropagation();
          try{
            await api('/api/assistant/sessions/'+encodeURIComponent(item.id),{method:'DELETE',silent:true});
            if(sessionId===item.id)reset();
            await loadHistoryMenu();
          }catch(err){add('error',err.message)}
        };
        row.append(open,del);historyMenu.append(row);
      }
    }catch(err){
      const empty=document.createElement('div');empty.className='ai-history-empty';empty.textContent=err.message||'读取历史失败';historyMenu.append(empty);
    }
  }
  function intervalLabel(sec){
    const n=Number(sec)||0;
    if(n>=86400&&n%86400===0)return n===86400?'每天':`每 ${n/86400} 天`;
    if(n>=3600&&n%3600===0)return n===3600?'每小时':`每 ${n/3600} 小时`;
    if(n>=60&&n%60===0)return `每 ${n/60} 分钟`;
    return n?`每 ${n} 秒`:'';
  }
  function scheduleLabel(task){
    if(task?.cron)return task.cron;
    return intervalLabel(task?.intervalSec||3600);
  }
  function parseAssistantTurn(content){
    try{
      const parsed=JSON.parse(content);
      if(parsed&&typeof parsed.message==='string')return parsed;
    }catch{}
    return {message:content,drafts:[]};
  }
  function isSaveReceipt(text){
    return /^(已通过 MCP 执行 |已保存助手定时任务：|上一个草稿已确认保存成功|刚才的草稿保存失败：)/.test(String(text||''));
  }
  function stampSpeaker(el,name,authorID){if(el.classList.contains('ai-role-user'))el.classList.toggle('is-own',!authorID||authorID===state.currentUser?.id);el.querySelector('.ai-speaker')?.remove();const badge=document.createElement('div');badge.className='ai-speaker';const avatar=document.createElement('span');avatar.className='ai-speaker-avatar';avatar.textContent=name==='Main Bot'?'MB':name.slice(0,1);if(el.classList.contains('ai-role-user')){const url=(!authorID||authorID===state.currentUser?.id)?state.currentUser?.avatarUrl:'';const photo=url||('/api/auth/photo/100/'+encodeURIComponent(name.replace(/\.png$/,''))+'.png');const img=document.createElement('img');img.src=photo;img.alt='';img.loading='lazy';img.onerror=()=>img.remove();avatar.append(img)}const label=document.createElement('span');label.textContent=name;badge.append(avatar,label);el.prepend(badge)}
  function messageActions(el,text,index,completedAt){el.querySelector('.ai-message-actions')?.remove();const bar=document.createElement('div');bar.className='ai-message-actions';const button=document.createElement('button');button.type='button';button.title='复制消息';button.setAttribute('aria-label','复制消息');button.innerHTML='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V4H4v12h4"/></svg>';button.onclick=()=>copyText(typeof text==='function'?text():text,'已复制');bar.append(button);if(Number.isInteger(index)&&el.classList.contains('ai-role-user')){const edit=document.createElement('button');edit.type='button';edit.title='编辑并重新生成';edit.setAttribute('aria-label','编辑并重新生成');edit.innerHTML='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><path d="m15 4 5 5M4 20l4-1L20 7a2 2 0 0 0-4-4L4 15z"/></svg>';edit.onclick=()=>editFromMessage(index);bar.append(edit)}if(el.classList.contains('ai-role-assistant')){bar.classList.add('is-complete');const time=document.createElement('time');time.className='ai-reply-time';const date=new Date(completedAt&&new Date(completedAt).getFullYear()>1970?completedAt:'');time.textContent=Number.isNaN(date.getTime())?'已完成':date.toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});if(!Number.isNaN(date.getTime())){time.dateTime=date.toISOString();time.title=date.toLocaleString()}bar.append(time)}el.append(bar)}
  async function editFromMessage(index){
    if(saving||nodeView)return toast('请先完成当前操作');if(!hasKey())return toast('请先配置模型凭据');
    const original=messages[index],sourceID=sessionId;if(!sourceID||original?.role!=='user')return;
    const edited=await afPrompt({title:'编辑并重新生成',message:'将替换这条消息及其后续回复。已执行的文件修改和平台变更不会回退，重跑时可能再次触发操作。若原回复仍在生成，将先停止它。',multiline:true,label:'修改消息',defaultValue:original.content,confirmText:'替换并重新生成'});
    if(edited===null||!edited.trim())return;if(sessionId!==sourceID)return toast('会话已切换，请重新编辑');
    try{
      let response=await fetch('/api/assistant/sessions/'+encodeURIComponent(sourceID)+'/generation',{cache:'no-store'}),job=response.ok?await response.json():null;
      if(job?.status==='running'){
        if(job.actorId&&job.actorId!==state.currentUser?.id)return toast('请等待该成员的回复结束，或由发起人停止');
        await api('/api/assistant/sessions/'+encodeURIComponent(sourceID)+'/generation/cancel?'+new URLSearchParams({generationId:job.id}),{method:'POST'});
        for(let i=0;i<40&&job.status==='running';i++){await new Promise(resolve=>setTimeout(resolve,250));response=await fetch('/api/assistant/sessions/'+encodeURIComponent(sourceID)+'/generation',{cache:'no-store'});if(!response.ok)throw new Error('无法确认生成已停止');job=await response.json()}
        if(job.status==='running')throw new Error('仍在停止生成，请稍后再重新生成');
      }
      const source=await api('/api/assistant/sessions/'+encodeURIComponent(sourceID),{silent:true});
      if(source.messages?.[index]?.content!==original.content)throw new Error('原消息已更新，请重新打开后编辑');
      const saved=await api('/api/assistant/sessions/'+encodeURIComponent(sourceID)+'/edit',{method:'POST',body:JSON.stringify({messageIndex:index,content:edited,updatedAt:source.updatedAt})});
      await openSession(saved.id);window.dispatchEvent(new Event('assistant-session-changed'));await sendAssistantTurn(edited,true);
    }catch(err){toast(err.message||'重新生成失败')}
  }
  function leaveNode(){nodeView=null;root.classList.remove('is-node-view');syncChrome()}
  function showNode(data){
    if(nodeView?.key!==data.key||data.events.some(e=>(!nodeView.seen.has(e.id||e.sequence)&&e.sequence<nodeView.lastSequence)||(nodeView.payloads.has(e.id||e.sequence)&&nodeView.payloads.get(e.id||e.sequence)!==JSON.stringify([e.category,e.phase,e.message,e.toolCallId])))){openEpoch++;controller?.abort();controller=null;generationId='';nodeView={key:data.key,seen:new Set(),payloads:new Map(),lastSequence:0,pending:null,done:false};root.classList.add('is-node-view');log.replaceChildren();resetTimeline();nodeView.pending=add('assistant','');stampSpeaker(nodeView.pending,data.name+' · 节点执行');const attempt=document.createElement('div');attempt.className='ai-node-attempt-label';const when=new Date(data.startedAt||'');attempt.textContent=(data.attempt<0?'执行次数未记录':'第 '+(data.attempt||0)+' 次执行')+(data.historical?' · 历史过程':' · 当前过程')+(!Number.isNaN(when.getTime())&&when.getFullYear()>1970?' · '+when.toLocaleString():'');nodeView.pending.querySelector('.ai-speaker').after(attempt);}
    const pending=nodeView.pending,existingOutput=pending.querySelector('.ai-node-output');existingOutput?.remove();
    for(const e of data.events){const id=e.id||e.sequence;if(nodeView.seen.has(id))continue;nodeView.seen.add(id);nodeView.payloads.set(id,JSON.stringify([e.category,e.phase,e.message,e.toolCallId]));nodeView.lastSequence=Math.max(nodeView.lastSequence,e.sequence||0);const text=e.message||'';if(e.category==='reasoning'&&text)pushThought(pending,{kind:'thinking',text});else if(e.category==='output'&&text)pushThought(pending,{kind:'answer',text});else if(e.category==='tool'){resetTimeline();const tid=e.toolCallId||e.tool?.callId||id;if(e.phase==='finish'){finishToolLine(pending,tid);if(!pending.querySelector('[data-tool-id="'+CSS.escape(String(tid))+'"]')){const line=newToolLine(pending,{id:tid,text:text||e.tool?.name});settleToolLine(line)}}else if(!pending.querySelector('[data-tool-id="'+CSS.escape(String(tid))+'"]'))newToolLine(pending,{id:tid,text:text||e.tool?.name||'调用工具'});}else if(['process','verification'].includes(e.category)&&text){const part=newAnswerPart(pending);part.__raw=text;paintAnswerPart(part);timeline=null;}}
    const terminal=data.historical||['succeeded','failed','cancelled','skipped'].includes(data.status);if(terminal){collapseThinking(pending);pending.querySelectorAll('.ai-tool-line').forEach(settleToolLine)}
    let status=pending.querySelector('.ai-status-line');if(!data.events.length)setStatusLine(pending,data.historical?'此次执行暂无已保存的过程':terminal?'此次执行已结束，暂无已加载的过程记录':data.type==='approval'?'人工确认节点 · 请在上方批准或打回':data.status==='pending'?'节点尚未开始':'正在等待执行过程…');else status?.remove();
    if(existingOutput)pending.insertBefore(existingOutput,pending.querySelector('.ai-message-actions'));
    if((data.output||data.error)&&terminal&&!nodeView.done){
      nodeView.done=true;const finalText=data.error||data.output;
      // Streamed answers and the stored node result can carry the same final reply.
      const alreadyRendered=[...pending.querySelectorAll('.ai-answer-part')].some(part=>String(part.__raw||'').trim()===finalText.trim());
      if(!alreadyRendered){const out=document.createElement('div');out.className='ai-answer markdown-body ai-node-output';out.innerHTML=renderMarkdown(finalText);pending.append(out)}
      messageActions(pending,finalText,undefined,data.finishedAt);
    }
    syncChrome();
  }
  async function refreshShared(){
    if(nodeView||!sharedSession||controller||saving||!sessionId)return;
    const id=sessionId,epoch=openEpoch;
    try{const item=await api('/api/assistant/sessions/'+encodeURIComponent(id),{silent:true});if(epoch!==openEpoch||controller||saving)return;
      if(item.updatedAt!==sessionUpdatedAt){messages=item.messages||[];sessionUpdatedAt=item.updatedAt;const nearEnd=log.scrollHeight-log.scrollTop-log.clientHeight<80,top=log.scrollTop;log.replaceChildren();if(!messages.length)welcome();else messages.forEach(renderTurn);log.scrollTop=nearEnd?log.scrollHeight:top;syncChrome()}
      const response=await fetch('/api/assistant/sessions/'+encodeURIComponent(id)+'/generation');if(epoch!==openEpoch||controller||saving)return;
      if(response.ok){const job=await response.json();if(job.status==='running')sendAssistantTurn(messages.at(-1)?.content||'继续',true,false,'',job)}
    }catch{}
  }
  function hasDraftReceipt(index,draft){
    const turn=parseAssistantTurn(messages[index]?.content||'');if((turn.drafts||[]).filter(d=>d.tool===draft.tool).length!==1)return false;
    for(let i=index+1;i<messages.length;i++){const m=messages[i];if(m.role==='assistant')break;if(m.role==='user'&&m.content.startsWith('已通过 MCP 执行 '+draft.tool+'：'))return true;}return false;
  }
  function chatTime(message,index){
    const valid=value=>{const n=Date.parse(value||'');return n>0?n:0};const start=valid(message.startedAt);if(start)return start;
    // Older replies only saved completion time. Anchor them to their user turn,
    // so role dispatches that happened during that reply stay after the PM.
    if(message.role==='assistant'&&groupChat()&&message.trigger!=='group_followup'){for(let i=index-1;i>=0;i--){if(messages[i].role==='user'&&valid(messages[i].createdAt))return valid(messages[i].createdAt)+(index-i)}}
    const own=valid(message.createdAt);if(own)return own;
    for(let i=index+1;i<messages.length;i++){const n=valid(messages[i].createdAt);if(n)return n-(i-index)}
    for(let i=index-1;i>=0;i--){const n=valid(messages[i].createdAt);if(n)return n+(index-i)}
    return index;
  }
  function stampAutomaticFollowup(el,trigger){
    if(trigger!=='group_followup')return;
    const head=el.querySelector('.ai-speaker');if(!head||head.querySelector('.collab-auto-badge'))return;
    const badge=document.createElement('span');badge.className='collab-auto-badge';badge.textContent='自动跟进';head.append(badge);
  }
  function renderTurn(message,index){
    if(message.role==='user'){
      if(isSaveReceipt(message.content))return;
      const el=add('user',message.content);el.dataset.chatTime=String(chatTime(message,index));messageActions(el,message.content,index);stampSpeaker(el,message.authorName||state.currentUser?.loginName||'我',message.authorId);window.AgentFlowCollaboration?.renderMessageContext(el,message);return;
    }
    const parsed=parseAssistantTurn(message.content);
    const el=add('assistant','');el.dataset.chatTime=String(chatTime(message,index));
    el.innerHTML=renderMarkdown(parsed.message||'');
    renderProcess(el,message.process);
    el.classList.add('markdown-body');stampSpeaker(el,message.speakerName?message.speakerName+(groupChat()?'':' · 咨询'):sharedSession?'Main Bot':'平台助手');stampAutomaticFollowup(el,message.trigger);messageActions(el,parsed.message||'',undefined,message.createdAt);
    (parsed.drafts||[]).forEach((d,draftIndex)=>{if(!d.saved&&hasDraftReceipt(index,d)){d.saved=true;d.historicalReceipt=true}showDraft(d,{messageIndex:index,draftIndex},el)});
  }
  async function openSession(id){
    leaveNode();
    closeMenus();
    const epoch=++openEpoch;controller?.abort();controller=null;generationId='';
    try{
      const item=await api('/api/assistant/sessions/'+encodeURIComponent(id),{silent:true});
      if((item.projectId||'')!==projectId())throw new Error('该会话属于其他项目，请先切换项目');
      if(epoch!==openEpoch)return;
      if(saving)throw new Error('正在保存草稿，请稍后切换');
      window.AgentFlowCollaboration?.restoreRun(item.runId||'');
      controller?.abort();controller=null;
      window.AgentFlowCollaboration?.clearComposer();input.value='';
      sessionId=item.id||id;sharedSession=!!item.shared;sessionUpdatedAt=item.updatedAt||'';rememberSession();
      messages=Array.isArray(item.messages)?item.messages:[];
      executor=['codebuddy','codex','claude'].includes(item.executor)?item.executor:'codebuddy';executorSelect.value=executor;await loadProviderModels();
      if(item.model)model=item.model;
      renderModelToggle();renderModelMenu();
      log.replaceChildren();
      if(!messages.length)welcome();
      else messages.forEach(renderTurn);
      syncChrome();
      log.scrollTop=log.scrollHeight;
      window.dispatchEvent(new Event('assistant-session-changed'));
      const response=await fetch('/api/assistant/sessions/'+encodeURIComponent(id)+'/generation');
      if(epoch!==openEpoch)return;
      if(response.ok){const job=await response.json();if(job.status==='running')sendAssistantTurn(messages.at(-1)?.content||'继续',true,false,'',job)}
    }catch(err){if(epoch===openEpoch)add('error',err.message||'无法打开对话')}
  }
  function openPanel(){
    setOpen(true);
    const who=String(state.currentUser?.id||state.currentUser?.loginName||'');
    if(owner!==who){const saved=localStorage.getItem(activeSessionKey());reset();owner=who;if(saved)openSession(saved)}
    loadModels();
    loadHistoryMenu();
    syncChrome();
    if(hasKey())input.focus();
  }
  launch.innerHTML='<span aria-hidden="true">✦</span><b>AI 工作台</b>';
  launch.onclick=()=>switchView('collaboration');launch.remove();
  root.querySelector('[data-close]').onclick=()=>{setOpen(false);launch.focus()};
  root.querySelector('[data-new]').onclick=reset;
  root.querySelector('[data-stop]').onclick=async()=>{if(!generationId)return;try{await api('/api/assistant/sessions/'+encodeURIComponent(sessionId)+'/generation/cancel?'+new URLSearchParams({generationId}),{method:'POST'})}catch(err){toast(err.message)}};
  root.querySelector('[data-history]').onclick=async e=>{
    e.stopPropagation();
    const open=historyMenu.hidden;
    closeMenus();
    if(open){await loadHistoryMenu();historyMenu.hidden=false;root.querySelector('[data-history]').setAttribute('aria-expanded','true')}
  };
  modelToggle.onclick=e=>{
    e.stopPropagation();
    const open=modelMenu.hidden;
    closeMenus();
    if(open){renderModelMenu();modelMenu.hidden=false;modelToggle.setAttribute('aria-expanded','true');fitComposerMenu(modelMenu,modelToggle)}
  };
  modeToggle.onclick=e=>{
    e.stopPropagation();
    const open=modeMenu.hidden;
    closeMenus();
    if(open){renderModeMenu();modeMenu.hidden=false;modeToggle.setAttribute('aria-expanded','true');fitComposerMenu(modeMenu,modeToggle)}
  };
  function fitComposerMenu(menu,toggle){
    if(menu.hidden)return;
    const view=window.visualViewport,host=root.getBoundingClientRect(),header=document.querySelector('.topbar')?.getBoundingClientRect();
    const left=Math.max(view?.offsetLeft||0,host.left)+8,right=Math.min((view?.offsetLeft||0)+(view?.width||innerWidth),host.right)-8;
    const top=Math.max(view?.offsetTop||0,host.top,header?.bottom||0)+8,bottom=Math.min((view?.offsetTop||0)+(view?.height||innerHeight),host.bottom)-8;
    const anchor=toggle.getBoundingClientRect(),width=Math.max(80,Math.min(340,right-left));
    Object.assign(menu.style,{position:'fixed',boxSizing:'border-box',minWidth:'0',width:width+'px',maxWidth:width+'px',right:'auto',bottom:'auto',left:Math.max(left,Math.min(anchor.left,right-width))+'px'});
    const natural=menu.scrollHeight+2,above=anchor.top-top-8,below=bottom-anchor.bottom-8;
    const useAbove=above>=Math.min(natural,160)||above>=below;
    const room=useAbove?above:below,overlap=room<80;
    const height=Math.max(40,Math.min(natural,420,overlap?bottom-top:room));
    menu.style.maxHeight=height+'px';
    menu.style.top=Math.max(top,Math.min(overlap?top:useAbove?anchor.top-8-height:anchor.bottom+8,bottom-height))+'px';
  }
  const refitComposerMenus=()=>{fitComposerMenu(modelMenu,modelToggle);fitComposerMenu(modeMenu,modeToggle)};
  root.addEventListener('scroll',refitComposerMenus,{passive:true});
  window.addEventListener('resize',refitComposerMenus);
  window.visualViewport?.addEventListener('resize',refitComposerMenus);
  document.addEventListener('click',e=>{if(![historyMenu,modelMenu,modeMenu].some(menu=>menu.contains(e.target))&&!e.target.closest('[data-history],[data-model-toggle],[data-mode-toggle]'))closeMenus()});
  document.addEventListener('pointerdown',e=>{root.querySelectorAll('.ai-messages details[open]').forEach(box=>{if(!box.contains(e.target))box.open=false})});
  document.addEventListener('change',e=>{if(e.target?.id==='projectSwitcher')syncChrome()});
  root.addEventListener('keydown',e=>{if(e.key==='Escape'){if(!historyMenu.hidden||!modelMenu.hidden||!modeMenu.hidden){closeMenus();e.stopPropagation();return}if(!root.classList.contains('ai-home'))root.querySelector('[data-close]').click()}});
  input.onkeydown=e=>{if(e.key==='Enter'&&!e.shiftKey&&!e.isComposing){e.preventDefault();root.querySelector('.ai-compose').requestSubmit()}};
  input.oninput=()=>{
    input.style.height='auto';
    input.style.height=Math.min(input.scrollHeight,140)+'px';
    syncChrome();
  };
  input.onfocus=syncChrome;

  async function refresh(){const data=await api('/api/bootstrap',{silent:true});Object.assign(state.data,data);renderAll()}
  function draftKind(tool){
    const labels={
      agents_save:{label:'角色',save:'保存角色',view:'agents'},
      workflows_save:{label:'SOP',save:'保存 SOP',view:'flow'},
      workflows_publish:{label:'发布 SOP',save:'发布',view:'flow',danger:true},
      workflows_delete:{label:'删除 SOP',save:'删除',view:'sops',danger:true},
      workflows_clone:{label:'克隆 SOP',save:'克隆',view:'flow'},
      dockerfiles_save:{label:'Dockerfile',save:'保存 Dockerfile',view:'dockerfiles'},
      dockerfiles_delete:{label:'删除 Dockerfile',save:'删除',view:'dockerfiles',danger:true},
      environments_save:{label:'环境安装脚本',save:'保存环境',view:'environments'},
      environments_delete:{label:'删除环境安装脚本',save:'删除',view:'environments',danger:true},
      sandboxes_save:{label:'沙箱',save:'保存沙箱',view:'sandboxes'},
      sandboxes_delete:{label:'删除沙箱',save:'删除',view:'sandboxes',danger:true},
      rules_save:{label:'规则',save:'保存规则',view:'rules'},
      rules_delete:{label:'删除规则',save:'删除',view:'rules',danger:true},
      skills_save:{label:'技能',save:'保存技能',view:'skills'},
      skills_delete:{label:'删除技能',save:'删除',view:'skills',danger:true},
      mcp_servers_save:{label:'MCP',save:'保存 MCP',view:'mcp'},
      mcp_servers_import:{label:'导入 MCP',save:'导入',view:'mcp'},
      schedule_jobs_save:{label:'平台定时任务',save:'保存定时任务',view:'issues'},
      schedule_jobs_delete:{label:'删除平台定时任务',save:'删除',view:'issues',danger:true},
      schedule_jobs_run:{label:'立即执行平台定时任务',save:'立即执行',view:'issues',danger:true},
      assistant_tasks_save:{label:'助手定时任务',save:'保存定时任务'},
      runs_start:{label:'启动 Run',save:'启动',view:'runs',danger:true},
      issues_save:{label:'需求',save:'保存需求',view:'issues'},
      issues_dispatch:{label:'派发 SOP',save:'派发',view:'runs',danger:true},
      projects_save:{label:'项目',save:'保存项目',view:'projects'},
      repositories_save:{label:'仓库',save:'保存仓库',view:'projects'},
      repositories_delete:{label:'删除仓库',save:'删除',view:'projects',danger:true}
    };
    if(labels[tool])return labels[tool];
    const danger=/(_delete|_publish|_dispatch|_start|_cancel|_reject|_run|_restore|_pass|_retry|_pause|_resume|_reopen)$/.test(tool)||tool==='sync_start';
    let save='确认执行';
    if(tool.endsWith('_save'))save='保存';
    else if(tool.endsWith('_delete'))save='删除';
    else if(tool.endsWith('_publish'))save='发布';
    else if(tool.endsWith('_start')||tool.endsWith('_run'))save='执行';
    return {label:tool,save,danger};
  }
  function draftTitle(draft){
    return draft.body?.name||draft.body?.id||draft.path?.id||draft.body?.workflowId||'未命名';
  }
  function sameDraft(a,b){
    if(!a||!b||a.tool!==b.tool)return false;
    const aid=a.body?.id||a.path?.id||a.savedId||'';
    const bid=b.body?.id||b.path?.id||b.savedId||'';
    if(aid&&bid)return String(aid)===String(bid);
    return JSON.stringify(a.path||{})===JSON.stringify(b.path||{})&&JSON.stringify(a.body||{})===JSON.stringify(b.body||{});
  }
  function markDraftSaved(draft,saved,origin){
    const id=saved?.id||draft.body?.id||draft.path?.id||'';
    draft.saved=true;
    draft.savedId=id;
    if(saved&&typeof saved==='object')draft.savedResult=saved;
    for(const [messageIndex,m] of messages.entries()){
      if(origin&&messageIndex!==origin.messageIndex)continue;
      if(m.role!=='assistant')continue;
      const parsed=parseAssistantTurn(m.content);
      if(!parsed.drafts?.length)continue;
      let changed=false;
      for(const [draftIndex,d] of parsed.drafts.entries()){
        if(origin?draftIndex!==origin.draftIndex:!sameDraft(d,draft))continue;
        d.saved=true;d.savedId=id;
        if(saved&&typeof saved==='object')d.savedResult=saved;
        changed=true;
      }
      if(changed){
        const out={message:parsed.message,drafts:parsed.drafts};
        if(parsed.lookups)out.lookups=parsed.lookups;
        if(parsed.sources)out.sources=parsed.sources;
        m.content=JSON.stringify(out);
      }
    }
  }
  async function persistSession(){
    if(!sessionId||!messages.length)return;
    {
      const id=sessionId;
      const stored=await api('/api/assistant/sessions/'+encodeURIComponent(id),{method:'PUT',silent:true,body:JSON.stringify({id,projectId:projectId(),executor,model,messages,updatedAt:sessionUpdatedAt})});
      if(sessionId===id&&Array.isArray(stored.messages)){messages=stored.messages;sessionUpdatedAt=stored.updatedAt||''}
    }
  }
  function bindSavedDraft(save,result,draft,saved,kind){
    const data=saved&&typeof saved==='object'?saved:(draft.savedResult||draft.body||{});
    save.disabled=false;save.className='button ghost small';
    result.textContent=draft.historicalReceipt?'已执行 · 已恢复历史回执':kind.danger?'已执行':'已保存';
    save.textContent=kind.view?'打开':'完成';
    save.onclick=async()=>{
      if(draft.tool==='workflows_save'&&state.workflowDirty&&!await afConfirm({title:'未保存更改',message:'当前流程有未保存修改，打开生成的 SOP 将放弃这些修改。继续？',danger:true}))return;
      const project=data.projectId||draft.body?.projectId;
      if(project){state.projectFilter=project;localStorage.setItem('agentflow.activeProject',state.projectFilter)}
      setOpen(false);
      openDraftTarget(draft.tool,data,kind.view);
    };
  }
  function showDraft(draft,origin,target){
    if(groupChat()){
      const record=document.createElement('details');record.className='collab-operation-record';
      const summary=document.createElement('summary');summary.textContent=draft.saved?'已完成操作':draft.saveError?'操作未完成':'历史操作请求';
      const text=document.createElement('p');text.textContent=draft.saveError||draft.tool;record.append(summary,text);
      const message=target||[...log.querySelectorAll('.ai-message:not([data-group-chat])')].at(-1);(message||log).append(record);return;
    }
    const kind=draftKind(draft.tool);
    const card=document.createElement('section');card.className='ai-draft';if(draft.inherited){const note=document.createElement('small');note.textContent='原会话草稿 · 仅供参考，不在分支重复执行';const detail=document.createElement('details');const summary=document.createElement('summary');summary.textContent=draft.tool+' · 查看记录';const pre=document.createElement('pre');pre.textContent=JSON.stringify(draft,null,2);detail.append(summary,pre);card.append(note,detail);log.append(card);return;}
    if(draft.auto){
      // 自动执行的草稿只渲染执行回执，不再弹出审批卡片。
      card.classList.add('ai-draft-auto');
      const title=document.createElement('strong');title.textContent=(draft.saved?'已自动执行':'自动执行失败')+' · '+kind.label+' · '+draftTitle(draft);card.append(title);
      const result=document.createElement('small');
      const hb=draft.saved?null:humanizeDraftError(draft.saveError||'');
      result.textContent=draft.saved?('已通过 MCP 自动执行'+(draft.savedResult?.id?' · '+draft.savedResult.id:'')):('自动执行失败'+(hb.display?'：'+hb.display:''));
      card.append(result);
      if(!draft.saved)setTimeout(()=>queueDraftFailureFeedback(draft,hb.text),60);
      log.append(card);return;
    }
    const title=document.createElement('strong');title.textContent=kind.label+' · '+draftTitle(draft);card.append(title);
    const description=document.createElement('p');
    description.textContent=draft.body?.taskPrompt?.objective||draft.body?.description||(draft.tool==='assistant_tasks_save'?`${scheduleLabel(draft.body||{})} · ${draft.body?.model||'默认模型'}`:(draft.tool==='schedule_jobs_save'?`${draft.body?.skipDispatch?'AI 定时任务':'触发任务'} · ${scheduleLabel(draft.body||{})}`:(draft.body?.driver||(kind.danger?'确认后将立即调用平台接口':''))));
    if(description.textContent)card.append(description);
    if(draft.warnings?.length){
      const warn=document.createElement('ul');warn.className='ai-draft-warn';
      for(const w of draft.warnings){const li=document.createElement('li');li.textContent=w;warn.append(li)}
      card.append(warn);
    }
    const steps=draft.tool==='agents_save'?(draft.body?.taskPrompt?.actions||[]):draft.tool==='workflows_save'?(draft.body?.nodes||[]).map(n=>n.name||n.type):[];
    if(steps.length){const list=document.createElement('ol');for(const text of steps.slice(0,8)){const item=document.createElement('li');item.textContent=text;list.append(item)}card.append(list)}
    if(draft.tool==='assistant_tasks_save'){
      draft.body.notifications=draft.body.notifications||[];
      const notify=document.createElement('div');notify.className='assistant-notification-editor';card.append(notify);
      mountNotificationEditor(notify,draft.body.notifications,{sourceType:'assistant_task',projectId:draft.body.projectId||projectId()});
    }
    const preview={tool:draft.tool};if(draft.path)preview.path=draft.path;if(draft.query)preview.query=draft.query;if(draft.body!==undefined)preview.body=draft.body;
    const detail=document.createElement('details'),summary=document.createElement('summary');summary.textContent='预览完整定义';const pre=document.createElement('pre');pre.textContent=JSON.stringify(preview,null,2);detail.append(summary,pre);card.append(detail);
    const result=document.createElement('small');result.textContent=(kind.danger?'待确认':'待保存')+' · '+(draft.body?.projectId||projectId()||'未选择项目');card.append(result);
    const save=document.createElement('button');save.type='button';save.className=kind.danger?'button danger-outline small':'button primary small';save.textContent=kind.save;card.append(save);
    if(draft.saved){
      bindSavedDraft(save,result,draft,draft.savedResult,kind);
      log.append(card);return;
    }
    save.onclick=async()=>{
      if(saving)return;
      if(kind.danger&&!await afConfirm({title:kind.label,message:'将通过 MCP 执行 '+draft.tool+'（'+draftTitle(draft)+'）。此操作可能发布、启动或删除平台资源。',danger:true,confirmText:kind.save}))return;
      saving=true;save.disabled=true;syncChrome();result.textContent='正在提交…';
      try{
        const args={};
        if(draft.tool==='assistant_tasks_save'){
          args.body={
            id:draft.body?.id||'',
            name:draft.body?.name||'',
            prompt:draft.body?.prompt||'',
            intervalSec:draft.body?.cron?0:Number(draft.body?.intervalSec)||3600,
            cron:draft.body?.cron||'',
            enabled:draft.body?.enabled!==false,
            model:draft.body?.model||model,
            projectId:draft.body?.projectId||projectId(),
            notifications:notificationPayload(draft.body?.notifications||[])
          };
        }else{
          if(draft.path&&Object.keys(draft.path).length)args.path=draft.path;
          if(draft.query&&Object.keys(draft.query).length)args.query=draft.query;
          if(draft.body!==undefined&&draft.body!==null)args.body=draft.body;
        }
        const res=await api('/mcp',{method:'POST',body:JSON.stringify({jsonrpc:'2.0',id:Date.now(),method:'tools/call',params:{name:draft.tool,arguments:args}})});
        if(res.error||res.result?.isError)throw new Error(res.error?.message||res.result?.content?.[0]?.text||'提交失败');
        if(!res.result)throw new Error('保存响应无效');
        const saved=res.result.structuredContent?.data||draft.body||{};
        bindSavedDraft(save,result,draft,saved,kind);
        markDraftSaved(draft,saved,origin);
        messages.push({role:'user',content:`已通过 MCP 执行 ${draft.tool}：${saved.id||saved.name||draftTitle(draft)}。后续修改请使用最新定义。`});
        try{await persistSession()}catch(receiptError){result.textContent='操作已执行，但回执保存失败：'+receiptError.message+'。请勿重复执行。';const retry=document.createElement('button');retry.type='button';retry.className='button ghost small';retry.textContent='重试保存回执';retry.onclick=async()=>{retry.disabled=true;try{await persistSession();bindSavedDraft(save,result,draft,saved,kind);retry.remove();queueAutoContinue()}catch(err){result.textContent='操作已执行，回执仍未保存：'+err.message}finally{retry.disabled=false}};card.append(retry);return;}
        try{await refresh()}catch{result.textContent=(kind.danger?'已执行':'已保存')+'；页面刷新失败，可稍后刷新页面查看'}
        // 保存成功后自动续跑下一步（延迟到 finally 释放 saving 后）。
        setTimeout(queueAutoContinue,60);
      }catch(e){
        const hb=humanizeDraftError(e.message);
        result.textContent='保存失败：'+hb.display+(hb.fromServer?'；已把失败原因发回给 AI 自动修正':'');
        save.disabled=false;
        if(hb.fromServer)setTimeout(()=>queueDraftFailureFeedback(draft,hb.text),60);
      }
      finally{saving=false;syncChrome()}
    };
    log.append(card);
  }
  function openDraftTarget(tool,saved,view){
    const id=saved.id||saved.workflowId;
    if(tool==='agents_save'){state.selectedAgent=id;switchView('agents');openAgentWizard((state.data.agents||[]).find(a=>a.id===id)||saved);return}
    if(tool==='workflows_save'||tool==='workflows_clone'||tool==='workflows_publish'){
      const wf=(state.data.workflows||[]).find(w=>w.id===id)||saved;
      if(wf&&wf.nodes){state.workflow=clone(wf);state.selectedNode=null;markWorkflowClean();renderAll();switchView('flow');return}
    }
    if(tool==='runs_start'||tool==='issues_dispatch'){switchView('runs');if(id&&typeof showRunDetail==='function')showRunDetail(id);return}
    if(view)switchView(view);
  }
  // 时间线：思考折叠框（完成后收起为"已完成思考"）、回答分段（Markdown）、
  // 工具调用行（保留不消除，转圈→完成态）。timeline 记录当前追加目标。
  let timeline=null;
  function resetTimeline(){timeline=null}
  // 仅当用户本来就停在底部附近时才自动跟随滚动，避免流式输出把
  // 向上翻阅的视口强拉回底部。
  function nearBottom(){return log.scrollHeight-log.scrollTop-log.clientHeight<120}
  function processDetails(){
    const details=document.createElement('details');details.className='collab-chat-process';
    const summary=document.createElement('summary');summary.textContent=lfT('处理过程','Process');
    const body=document.createElement('div');body.className='collab-process-body';details.append(summary,body);return details;
  }
  function summarizeProcess(details){
    const body=details?.querySelector('.collab-process-body');if(!body)return;
    const thoughts=body.querySelectorAll('.ai-think').length,tools=body.querySelectorAll('.ai-tool-line').length,parts=[];
    if(thoughts)parts.push(lfT('思考 '+thoughts+' 段',thoughts+' reasoning step'+(thoughts>1?'s':'')));
    if(tools)parts.push(lfT('工具 '+tools+' 次',tools+' tool call'+(tools>1?'s':'')));
    details.querySelector('summary').textContent=lfT('处理过程','Process')+(parts.length?' · '+parts.join(' · '):'');
  }
  function foldGroupProcess(){
    if(nodeView)return;
    const pending=log.querySelector('[data-pm-streaming]');if(!pending)return;
    const parts=[...pending.children].filter(e=>e.matches('.ai-think,.ai-tool-line'));
    if(!parts.length)return;
    let details=pending.querySelector('.collab-chat-process');if(!details){details=processDetails();pending.insertBefore(details,parts[0])}
    const body=details.querySelector('.collab-process-body');parts.forEach(el=>body.append(el));summarizeProcess(details);
  }
  function renderProcess(el,process){
    if(!Array.isArray(process)||!process.length)return;
    const details=processDetails(),body=details.querySelector('.collab-process-body');
    for(const step of process){
      if(step.kind==='thinking'){
        const box=document.createElement('details');box.className='ai-think';
        const summary=document.createElement('summary');summary.textContent=lfT('已完成思考','Reasoning');
        const text=document.createElement('div');text.className='ai-think-body';text.textContent=step.text||'';box.append(summary,text);body.append(box);
      }else if(step.kind==='tool'){
        const line=document.createElement('div');line.className='ai-tool-line is-done';
        const mark=document.createElement('span');mark.className='ai-tool-check';mark.setAttribute('aria-hidden','true');
        const text=document.createElement('span');text.className='ai-tool-line-text';text.textContent=step.text||'';line.append(mark,text);body.append(line);
      }
    }
    summarizeProcess(details);el.prepend(details);
  }
  function timelineScroll(){foldGroupProcess();if(nearBottom())log.scrollTop=log.scrollHeight}
  // 收起当前展开的思考框，并把标题改为"已完成思考"。
  function collapseThinking(pending){
    pending.querySelectorAll('.ai-think[open]').forEach(box=>{
      box.open=false;
      const s=box.querySelector('summary');
      if(s)s.textContent='已完成思考';
    });
  }
  // 把仍在转圈的工具行标记为完成（保留展示，不移除）。
  function settleToolLine(line,failed=false){
    if(!line)return;
    // forEach also supplies an index: only an explicit boolean can mark failure.
    // A later bulk settle must preserve a failure received from tool_result.
    const isFailed=failed===true||line.classList.contains('is-failed');
    line.classList.add('is-done');
    if(isFailed)line.classList.add('is-failed');
    const mark=line.querySelector('.ai-tool-spinner')||line.querySelector('.ai-tool-check')||line.querySelector('.ai-tool-error');
    if(mark){mark.classList.remove('ai-tool-spinner','ai-tool-check','ai-tool-error');mark.classList.add(isFailed?'ai-tool-error':'ai-tool-check')}
    if(isFailed&&!line.querySelector('.ai-tool-state')){
      const state=document.createElement('span');state.className='ai-tool-state';state.textContent='失败';line.append(state);
    }
  }
  function newThinkingSeg(pending){
    collapseThinking(pending);
    const box=document.createElement('details');box.className='ai-think';box.open=true;
    box.innerHTML='<summary>正在思考…</summary><div class="ai-think-body"></div>';
    pending.append(box);timelineScroll();
    return box.querySelector('.ai-think-body');
  }
  function newAnswerPart(pending){
    collapseThinking(pending);
    const el=document.createElement('div');el.className='ai-answer ai-answer-part markdown-body';el.__raw='';
    pending.append(el);timelineScroll();return el;
  }
  function paintAnswerPart(el){
    if(el.__raf)return;
    el.__raf=requestAnimationFrame(()=>{el.__raf=0;el.innerHTML=renderMarkdown(el.__raw||'');timelineScroll()});
  }
  function newToolLine(pending,ev){
    collapseThinking(pending);
    const line=document.createElement('div');line.className='ai-tool-line';line.dataset.toolId=ev.id||'';
    line.innerHTML='<span class="ai-tool-spinner" aria-hidden="true"></span><span class="ai-tool-line-text"></span>';
    line.querySelector('.ai-tool-line-text').textContent=ev.text||('调用工具 '+(ev.tool||''));
    pending.append(line);timelineScroll();return line;
  }
  function finishToolLine(pending,id,failed=false){
    if(!id)return;
    let found=false;
    pending.querySelectorAll('.ai-tool-line').forEach(line=>{if(line.dataset.toolId===String(id)){found=true;settleToolLine(line,failed)}});
    if(failed===true&&!found){
      // Protocol submissions are hidden during normal operation. Their failure
      // still needs a visible signal, without exposing draft arguments/results.
      settleToolLine(newToolLine(pending,{id:String(id),text:'草稿 / 工具调用失败'}),true);
    }
  }
  function setStatusLine(pending,text){
    let line=pending.querySelector('.ai-status-line');
    if(!line){line=document.createElement('div');line.className='ai-status-line';pending.append(line)}
    line.textContent=text;timelineScroll();
  }
  function pushThought(pending,ev){
    switch(ev.kind||''){
      case 'thinkingStart':
        collapseThinking(pending);
        timeline={kind:'thinking',el:null};
        return;
      case 'thinking':
        if(!ev.text)return;
        if(!timeline||timeline.kind!=='thinking'||!timeline.el)timeline={kind:'thinking',el:newThinkingSeg(pending)};
        timeline.el.textContent+=ev.text;
        timelineScroll();
        return;
      case 'answer':
        if(!ev.text)return;
        if(!timeline||timeline.kind!=='answer')timeline={kind:'answer',el:newAnswerPart(pending)};
        timeline.el.__raw+=ev.text;
        paintAnswerPart(timeline.el);
        return;
      case 'tool':
        if(String(ev.tool||'').replace(/^mcp__agentflow_session__/,'')==='submit_assistant_reply')return;
        timeline={kind:'tool',el:newToolLine(pending,ev)};
        return;
      case 'toolDone':
        finishToolLine(pending,ev.id,ev.failed===true);
        return;
      case 'draftStart':
      case 'draftDelta':
        return;
    }
    if(ev.text)setStatusLine(pending,ev.text);
  }
  async function consumeAssistantStream(resp){
    const reader=resp.body.getReader();
    const decoder=new TextDecoder();
    let buffer='',result=null,error=null;
    const dispatch=(event,dataStr)=>{
      let payload={};try{payload=JSON.parse(dataStr)}catch{return}
      if(event==='thought')pushThought(consumeAssistantStream.pending,payload);
      else if(event==='result')result=payload;
      else if(event==='error')error=payload.error||'生成失败';
    };
    for(;;){
      const {value,done}=await reader.read();
      if(done)break;
      buffer+=decoder.decode(value,{stream:true});
      let idx;
      while((idx=buffer.indexOf('\n\n'))>=0){
        const chunk=buffer.slice(0,idx);buffer=buffer.slice(idx+2);
        let event='message',dataStr='';
        chunk.split('\n').forEach(line=>{
          if(line.startsWith('event:'))event=line.slice(6).trim();
          else if(line.startsWith('data:'))dataStr+=(dataStr?'\n':'')+line.slice(5).trimStart();
        });
        if(dataStr)dispatch(event,dataStr);
      }
    }
    if(error)throw new Error(error);
    if(!result)throw new Error('生成中断，未返回完整回答');
    return result;
  }

  // confirm 模式下草稿保存成功后自动续跑下一步，直到计划完成；
  // 手动发送会重置计数，连续续跑有上限防失控。
  let autoContinues=0;
  const AUTO_CONTINUE_MAX=30;
  function queueAutoContinue(){
    if(controller||saving)return;
    if(approvalMode==='auto')return;
    if(autoContinues>=AUTO_CONTINUE_MAX){
      add('assistant','已连续自动续跑 '+AUTO_CONTINUE_MAX+' 轮，先停一下。需要继续时发送"继续"即可。');
      return;
    }
    autoContinues++;
    sendAssistantTurn('上一个草稿已确认保存成功，请继续执行计划中的下一步；如果计划已全部完成，请直接给出总结，不要再提交新草稿。',false,true,'已保存，正在让 AI 继续下一步…');
  }
  // 把服务端错误整理成给用户看的文案 + 给 AI 的原始信息。
  function humanizeDraftError(msg){
    let text=String(msg||'').trim();
    let fromServer=false;
    try{
      const p=JSON.parse(text);
      if(p&&typeof p==='object'&&p.error){text=String(p.error);fromServer=true}
    }catch{}
    text=text.trim().slice(0,300);
    const display=text
      .replace(/(?:workflow|rule|rules|agent|agents|sandbox|skill|environment|dockerfile)\s+(\w+)(\s+is\s+required)/gi,'缺少必填字段 "$1"')
      .replace(/(\w+)\s+is\s+required/gi,'缺少必填字段 "$1"')
      .replace(/is required/gi,'缺少必填字段');
    return{text,display,fromServer};
  }
  // 草稿执行失败时自动打回给 AI：带原始错误让它修正后重新提交。
  function queueDraftFailureFeedback(draft,errText){
    if(controller||saving)return;
    if(autoContinues>=AUTO_CONTINUE_MAX)return;
    autoContinues++;
    sendAssistantTurn('刚才的草稿保存失败：'+draft.tool+'（'+draftTitle(draft)+'）。服务端返回错误：'+(errText||'执行失败')+'。请分析原因并修正：若是缺少必填字段或字段不符合平台 schema（例如 sandboxProfileId、instructions、revision），先用只读工具读取现有定义与要求，修正后重新提交草稿；若是依赖未满足，先提交能解除依赖的草稿，或向我说明需要先做什么。',false,true,'保存失败，正在让 AI 重新生成…');
  }
  // 系统状态行：自动续跑/失败打回等内部动作以轻量提示展示，不出用户气泡。
  function addSystemNote(text){
    const line=document.createElement('div');line.className='ai-system-note';
    const tx=document.createElement('span');tx.textContent=text;
    line.append(tx);log.append(line);log.scrollTop=log.scrollHeight;
    return line;
  }
  function rebaseAutomaticReplies(local,remote){
    const appended=local.at(-1);if(!appended||appended.role!=='user')return null;
    const prefix=local.slice(0,-1);if(remote.length<=prefix.length)return null;
    if(prefix.some((message,i)=>JSON.stringify(message)!==JSON.stringify(remote[i])))return null;
    const extra=remote.slice(prefix.length);if(extra.some(m=>m.role!=='assistant'||m.trigger!=='group_followup'))return null;
    return [...remote,appended];
  }
  async function sendAssistantTurn(text,reuseUser,isAuto,note,resumeJob){
    if(!resumeJob&&window.AgentFlowCollaboration?.handoffPending?.())return toast('正在切回 SOP，可先起草消息或处理其他任务');
    if(nodeView)return;
    if(!isAuto)autoContinues=0;
    if(controller||saving||!String(text||'').trim())return;
    if(!resumeJob&&!hasKey()){syncChrome();return}
    text=String(text).trim();
    let noteEl=null;
    if(!reuseUser){
      const context=isAuto?{}:(window.AgentFlowCollaboration?.takeComposer()||{});
      messages.push({role:'user',content:text,createdAt:new Date().toISOString(),...context});
      if(isAuto)noteEl=addSystemNote(note||'正在继续…');
      else{const el=add('user',text);messageActions(el,text,messages.length-1);stampSpeaker(el,state.currentUser?.loginName||'我');window.AgentFlowCollaboration?.renderMessageContext(el,messages[messages.length-1]);input.value=''}
    }
    controller=new AbortController();const current=controller;setBusy(true);const pending=add('assistant','正在整理方案…');
    pending.textContent='';const role=messages.at(-1)?.references?.find(r=>r.type==='agent');pending.dataset.pmStreaming='true';pending.dataset.chatTime=String(Date.parse(resumeJob?.startedAt||'')||chatTime(messages.at(-1),messages.length-1)+1);stampSpeaker(pending,role&&!groupChat()?role.name+' · 咨询':sharedSession?'Main Bot':'平台助手');stampAutomaticFollowup(pending,resumeJob?.trigger);
    consumeAssistantStream.pending=pending;
    resetTimeline();
    try{
      if(!sessionId)sessionId='af'+Array.from(crypto.getRandomValues(new Uint8Array(16)),b=>b.toString(16).padStart(2,'0')).join('');rememberSession();
      let job=resumeJob;
      if(!job){
        const submit=()=>fetch('/api/assistant/generations',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({projectId:projectId(),sessionId,executor,model,approvalMode:groupChat()?'auto':approvalMode,messages,runId:window.AgentFlowCollaboration?.runId()||''})});
        let res=await submit();
        if(res.status===409&&groupChat()&&!isAuto){
          const latest=await api('/api/assistant/sessions/'+encodeURIComponent(sessionId),{silent:true});
          const rebased=rebaseAutomaticReplies(messages,latest.messages||[]);
          if(rebased){messages=rebased;sessionUpdatedAt=latest.updatedAt||'';res=await submit()}
        }
        if(!res.ok){let message='HTTP '+res.status;try{message=(await res.json()).error||message}catch{}throw new Error(message)}
        job=await res.json();
        localStorage.setItem(ONBOARDING_CHATTED,'1');
      }
      if(controller!==current)return;
      generationId=job.id;generationActorID=job.actorId||state.currentUser?.id||0;generationTrigger=job.trigger||'';syncChrome();window.dispatchEvent(new Event('assistant-session-changed'));
      let after=0,failures=0;
      let reply;
      while(controller===current){
        let snapshot;
        try{
          const res=await fetch('/api/assistant/sessions/'+encodeURIComponent(sessionId)+'/generation?after='+after,{signal:current.signal,cache:'no-store'});
          if(!res.ok)throw new Error('无法恢复生成进度（'+res.status+'）');
          snapshot=await res.json();failures=0;
        }catch(err){if(err.name==='AbortError')throw err;if(++failures>20)throw new Error('连接暂时中断。生成会继续，可重新打开此对话查看。');await new Promise(resolve=>setTimeout(resolve,1000));continue}
        if(controller!==current)return;
        if(snapshot.id!==job.id)throw new Error('会话已更新，请重新打开');
        for(const event of snapshot.events||[]){if(event.sequence>after){pushThought(pending,event.data);after=event.sequence}}
        if(snapshot.status!=='running'){
          if(snapshot.status!=='completed')throw new Error(snapshot.error||(snapshot.status==='cancelled'?'已停止生成。':'生成中断，请重试。'));
          reply=snapshot.reply;if(snapshot.workspaceWarning){const warning=document.createElement('p');warning.className='ai-workspace-warning';warning.textContent=snapshot.workspaceWarning;pending.append(warning)}break;
        }
        await new Promise(resolve=>setTimeout(resolve,500));
      }
      if(controller!==current)return;
      if(reply.error)throw new Error(reply.error);
      sessionId=reply.sessionId||sessionId;
      // Keep the server transcript byte-for-byte: JSON field order, nulls and
      // escaping differ from JSON.stringify and are checked on the next turn.
      const transcriptResponse=await fetch('/api/assistant/sessions/'+encodeURIComponent(sessionId),{signal:current.signal,cache:'no-store'});
      if(!transcriptResponse.ok)throw new Error('回复已完成，但会话同步失败，请重新打开此对话');
      const transcript=await transcriptResponse.json();
      if(controller!==current)return;
      if(!Array.isArray(transcript.messages))throw new Error('会话同步响应无效');
      messages=transcript.messages;sharedSession=!!transcript.shared;sessionUpdatedAt=transcript.updatedAt||'';
      pending.querySelector('.ai-status-line')?.remove();
      // 思考框收起、工具行标记完成（保留过程展示，不移除）。
      collapseThinking(pending);
      pending.querySelectorAll('.ai-tool-line:not(.is-done)').forEach(line=>settleToolLine(line));
      summarizeProcess(pending.querySelector('.collab-chat-process'));
      // 最后一个回答分段即模型的最终回答，用 result 的权威文本替换；
      // 之前的思考框与回答分段保留为过程记录。
      const parts=pending.querySelectorAll('.ai-answer-part');
      const body=document.createElement('div');body.className='ai-answer markdown-body';body.innerHTML=renderMarkdown(reply.message||'');
      if(parts.length)parts[parts.length-1].replaceWith(body);
      else pending.append(body);
      pending.dataset.chatTime=String(chatTime(messages.at(-1),messages.length-1));delete pending.dataset.pmStreaming;pending.classList.add('markdown-body');stampSpeaker(pending,reply.speakerName?reply.speakerName+(groupChat()?'':' · 咨询'):sharedSession?'Main Bot':'平台助手');stampAutomaticFollowup(pending,messages.at(-1)?.trigger||resumeJob?.trigger);messageActions(pending,reply.message||'',undefined,messages.at(-1)?.createdAt||new Date().toISOString());
      if(reply.sources?.length){
        const details=document.createElement('details');details.className='ai-sources';
        const title=document.createElement('summary');title.textContent='代码依据 · '+new Set(reply.sources.map(s=>s.path)).size+' 个文件';details.append(title);
        const pre=document.createElement('pre');pre.textContent=reply.sources.map(s=>s.path+':'+s.line+'  '+s.text).join('\n');details.append(pre);log.append(details);
      }
      for(const [draftIndex,d] of (reply.drafts||[]).entries()){
        showDraft(d,{messageIndex:messages.length-1,draftIndex},pending);
        if(d.tool==='workflows_save'&&d.body&&typeof playWorkflowOnCanvas==='function')playWorkflowOnCanvas(d.body,{source:'assistant'});
      }
      loadHistoryMenu();window.dispatchEvent(new Event('assistant-session-changed'));
    }catch(err){
      if(controller!==current)return;
      pending.classList.remove('markdown-body');
      pending.classList.add('ai-role-error');
      const msg=err.name==='AbortError'?'已停止生成。':err.message;
      pending.querySelector('.ai-status-line')?.remove();
      const textEl=document.createElement('div');
      textEl.textContent=msg;
      pending.append(textEl);
      if(resumeJob?.trigger==='group_followup'){
        const reload=document.createElement('button');reload.type='button';reload.className='button ghost small';reload.textContent='查看最新对话';reload.onclick=()=>openSession(sessionId);pending.append(reload);
      }else if(err.name!=='AbortError'){
        const retry=document.createElement('button');
        retry.type='button';
        retry.className='button primary small ai-retry';
        retry.textContent='重试';
        retry.onclick=()=>{
          pending.remove();
          sendAssistantTurn(text,true);
        };
        pending.append(retry);
      }else{
        const undone=messages.pop();
        window.AgentFlowCollaboration?.restoreComposer(undone);
        input.value=text;
      }
    }finally{
      if(noteEl){noteEl.classList.add('is-done')}
      if(controller===current){controller=null;generationId='';setBusy(false);window.AgentFlowCollaboration?.refreshGroupChat?.();if(nearBottom())log.scrollTop=log.scrollHeight;if(hasKey())input.focus()}
    }
  }
  async function redirectTurn(text){
    if(redirecting||saving)return;
    if(generationTrigger!=='group_followup'&&generationActorID&&generationActorID!==state.currentUser?.id){toast('本轮由其他成员发起，请等待完成后发送');return}
    if(!generationId){toast('正在启动回复，请稍后发送');return}
    redirecting=true;syncChrome();const source=sessionId;const context=window.AgentFlowCollaboration?.takeComposer()||{};
    try{
      await api('/api/assistant/sessions/'+encodeURIComponent(source)+'/generation/cancel?'+new URLSearchParams({generationId}),{method:'POST'});
      let stopped=false;for(let i=0;i<40;i++){const job=await api('/api/assistant/sessions/'+encodeURIComponent(source)+'/generation',{silent:true});if(job.status!=='running'){stopped=true;break}await new Promise(r=>setTimeout(r,250))}
      if(!stopped)throw new Error('上一轮仍在停止，请稍后重试');
      if(sessionId!==source)throw new Error('会话已切换，未发送新指令');
      await openSession(source);window.AgentFlowCollaboration?.restoreComposer(context);input.value=text;
      redirecting=false;await sendAssistantTurn(text,false);
    }catch(err){window.AgentFlowCollaboration?.restoreComposer(context);input.value=text;toast(err.message)}finally{redirecting=false;syncChrome()}
  }
  root.querySelector('.ai-compose').onsubmit=async e=>{
    e.preventDefault();
    if(window.AgentFlowCollaboration?.handoffPending?.())return toast('正在切回 SOP，可先起草消息或处理其他任务');
    const text=input.value||(window.AgentFlowCollaboration?.hasAttachments()?'请分析这些附件。':'');if(!text.trim())return;if(controller)await redirectTurn(text);else await sendAssistantTurn(text,false);
  };
  root.querySelector('[data-configure-credentials]').onclick=()=>{
    switchView('settings');
    const name={codebuddy:'codeBuddyApiKey',codex:'codexApiKey',claude:'claudeCodeApiKey'}[executor];
    const field=document.querySelector('#userCredentialForm [name="'+name+'"]');
    field?.focus();
    field?.scrollIntoView({block:'center'});
  };
  window.AgentFlowAssistant={root,input,showNode,leaveNode,open:openPanel,openSession,refreshShared,shared:()=>sharedSession,reset,busy:()=>saving,generating:()=>!!controller,sessionId:()=>sessionId,sync:syncChrome,send:text=>sendAssistantTurn(text,false)};
  welcome();
  renderModeToggle();
  syncChrome();
})();
