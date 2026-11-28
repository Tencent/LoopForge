// Repository add/edit drawer for the project detail page. Saves write straight
// to /api/repositories; #repositoryModal stays for its other callers.
const repoDrawerState={projectId:'',repoId:'',nameTouched:false,saving:false,returnFocus:null,branchExecutorId:''};
function repoDrawerOpen(){return !!$('#repoDrawer')?.classList.contains('open')}
function setRepoDrawerSwitch(on){const sw=$('#repoDrawerDefault');if(!sw)return;sw.classList.toggle('on',!!on);sw.setAttribute('aria-checked',String(!!on))}
function repoDrawerError(show){
  const f=$('#repoDrawerForm');if(!f)return;
  f.elements.repoUrl.classList.toggle('bad',!!show);
  const error=$('#repoDrawerUrlError');
  if(show)error.textContent=f.elements.provider?.value==='forgejo'
    ?'请输入同一个 Forgejo 仓库的 SSH 克隆地址和 HTTPS 网页地址。'
    :'请输入有效的 Git HTTP(S) 地址或 Git SSH 克隆地址。';
  error.hidden=!show;
}
function repoDrawerInfo(form){
  const {provider,repoUrl,webUrl}=form.elements;
  if(provider?.value==='forgejo')return globalThis.LoopForgeForgejoRepository(repoUrl.value,webUrl.value);
  return parseGongfengRepoURL(repoUrl.value);
}
function syncRepoDrawerDetect(){
  const f=$('#repoDrawerForm');if(!f)return;
  const forgejo=f.elements.provider?.value==='forgejo';
  const info=repoDrawerInfo(f),box=$('#repoDrawerDetect');
  f.querySelector('[data-github-repositories]').hidden=forgejo;
  f.querySelector('[data-repo-address-label]').textContent=forgejo?'SSH 克隆地址':'Git 仓库地址';
  f.querySelector('.pa-fmt').hidden=forgejo;
  f.elements.repoUrl.placeholder=forgejo?'git@forge.example.org:team/repo.git':'https://github.com/owner/repo';
  $('#repoDrawerSSH').hidden=!forgejo&&info?.provider!=='ssh';
  $('#repoDrawerForgejoHint').hidden=!forgejo;
  $('#repoDrawerSSH [data-optional-web-url]').hidden=forgejo;
  f.elements.webUrl.placeholder=forgejo?'https://forge.example.org/team/repo':'https://gitlab.com/group/repo';
  renderGitSSHMissingHint($('#repoDrawerSSHMissing'),forgejo?globalThis.LoopForgeGitSSH(f.elements.repoUrl.value):info);
  if(!info){box.hidden=true;return}
  repoDrawerError(false);
  if(!repoDrawerState.nameTouched)f.elements.name.value=info.name;
  $('#repoDrawerDetectName').textContent=f.elements.name.value.trim()||info.name;
  $('#repoDrawerDetectPath').textContent=forgejo?repoDisplayPath(info):info.project;
  box.hidden=false;
}
function fillRepoBranchNaming(form,naming){
  const fields=form.elements;
  const prefix=naming?.mode==='prefix'?(naming.prefix??''):'feature/';
  const preset=Array.from(fields.workBranchPrefix.options).some(option=>option.value===prefix&&option.value!=='custom');
  fields.workBranchNamingMode.value=naming?.mode==='ai'?'ai':'prefix';
  fields.workBranchPrefix.value=preset?prefix:'custom';
  fields.workBranchCustomPrefix.value=preset?'':prefix;
  fields.workBranchPrompt.value=naming?.prompt||'';
  repoDrawerState.branchExecutorId=naming?.mode==='ai'?naming.executorId||'':'';
  fillRepoBranchExecutors(form,repoDrawerState.branchExecutorId);
  fillRepoBranchModels(form,naming?.model||'');
  syncRepoBranchNaming(form);
}
function repoBranchOption(id,name,icon){
  const option=new Option(name,id);
  if(icon)option.dataset.brand=icon.split('/').at(-1);
  return option;
}
// AI branch naming runs on the dispatching user's credentials, like role
// nodes, so the picker offers the same configured CLIs as the role editor
// (executorConfigured). A saved CLI that is not configured for this user stays
// visible but disabled, so editing other fields keeps the stored setting.
function fillRepoBranchExecutors(form,selected=''){
  const executors=(state.data.executors||[]).filter(executor=>executor.kind);
  const usable=executors.filter(executor=>executorConfigured(executor.id));
  const icon=executor=>window.AgentFlowAssistantModels.cliIcon(executor.kind);
  const options=[new Option(usable.length||selected?lfT('选择 CLI','Choose a CLI'):executorModelCatalogRequest?lfT('检查配置中…','Checking configuration…'):lfT('无可用 CLI，请先在个人配置中配置','No configured CLI. Configure one in Settings.'),'')];
  if(selected&&!usable.some(executor=>executor.id===selected)){
    const saved=executors.find(executor=>executor.id===selected);
    const status=executorModelCatalogRequest?lfT('检查配置中','checking'):lfT('未配置','not configured');
    const option=repoBranchOption(selected,`${saved?.name||selected}（${status}）`,saved?icon(saved):'');
    option.disabled=true;
    options.push(option);
  }
  options.push(...usable.map(executor=>repoBranchOption(executor.id,executor.name||executor.id,icon(executor))));
  form.elements.workBranchExecutorId.replaceChildren(...options);
  form.elements.workBranchExecutorId.value=selected;
}
function fillRepoBranchModels(form,model='',custom=false){
  const fields=form.elements,executor=fields.workBranchExecutorId.value,choice=fields.workBranchModelChoice;
  const catalog=window.AgentFlowAssistantModels;
  const items=(executorModelCatalogs()[executor]?.models||[]).filter(item=>catalog.concreteModelId(item.id));
  const listed=items.some(item=>item.id===model);
  choice.replaceChildren(
    new Option(lfT('选择模型','Choose a model'),''),
    ...items.map(item=>repoBranchOption(item.id,item.name||item.id,catalog.modelIcon(item))),
  );
  if(executor)choice.add(new Option(lfT('手动输入模型 ID…','Enter a model ID…'),AGENT_CUSTOM_MODEL));
  const manual=!!executor&&(custom||!!model&&!listed);
  choice.disabled=!executor;
  choice.value=manual?AGENT_CUSTOM_MODEL:listed?model:'';
  fields.workBranchCustomModel.value=manual?model:'';
  form.querySelector('[data-work-branch-custom-model]').hidden=!manual;
}
function repoBranchModel(form){
  const fields=form.elements,choice=fields.workBranchModelChoice.value;
  return choice===AGENT_CUSTOM_MODEL?fields.workBranchCustomModel.value.trim():choice;
}
function syncRepoBranchNaming(form){
  const isAI=form.elements.workBranchNamingMode.value==='ai';
  form.querySelector('[data-work-branch-prefix]').hidden=isAI;
  form.querySelector('[data-work-branch-prompt]').hidden=!isAI;
  form.querySelector('[data-work-branch-custom-prefix]').hidden=form.elements.workBranchPrefix.value!=='custom';
  if(isAI){
    const user=state.currentUser?.id;
    const loading=loadExecutorModelCatalogs();
    // Repaint while the catalog loads so the picker says "checking" instead
    // of reporting configured CLIs as missing until the request returns.
    fillRepoBranchExecutors(form,form.elements.workBranchExecutorId.value);
    loading.then(()=>{
      if(repoDrawerOpen()&&state.currentUser?.id===user&&form.elements.workBranchNamingMode.value==='ai'){
        const manual=form.elements.workBranchModelChoice.value===AGENT_CUSTOM_MODEL;
        const model=repoBranchModel(form);
        fillRepoBranchExecutors(form,form.elements.workBranchExecutorId.value);
        fillRepoBranchModels(form,model,manual&&!model);
      }
    });
  }
}
function repoBranchNamingPayload(form){
  const fields=form.elements;
  if(fields.workBranchNamingMode.value==='ai'){
    return {mode:'ai',executorId:fields.workBranchExecutorId.value,model:repoBranchModel(form),prompt:fields.workBranchPrompt.value.trim()};
  }
  const prefix=fields.workBranchPrefix.value;
  return {mode:'prefix',prefix:prefix==='custom'?fields.workBranchCustomPrefix.value.trim():prefix};
}
function openRepoDrawer(repoId=''){
  const projectId=state.projectAdminDetailId;
  const project=(state.data.projects||[]).find(p=>p.id===projectId);
  if(!project||projectId==='new')return toast('保存项目后可用','warn');
  const repo=repoId?(state.data.repositories||[]).find(r=>r.id===repoId&&r.projectId===projectId):null;
  if(repoId&&!repo)return toast('仓库不存在或已被删除','err');
  const f=$('#repoDrawerForm');if(!f)return;
  const hasDefault=!projectNeedsDefaultRepository(project);
  const forgejo=repo?.provider==='forgejo',usesSSH=forgejo||repo?.provider==='ssh';
  const repoAddress=globalThis.LoopForgeGitRepositoryReference(repo);
  const parsedName=(forgejo?globalThis.LoopForgeForgejoRepository(repoAddress,repo.webUrl):parseGongfengRepoURL(repoAddress))?.name||'';
  Object.assign(repoDrawerState,{projectId,repoId:repo?.id||'',nameTouched:!!repo&&!!repo.name&&repo.name!==parsedName,saving:false,returnFocus:document.activeElement});
  f.elements.id.value=repo?.id||'';
  f.elements.provider.value=forgejo?'forgejo':'';
  f.elements.repoUrl.value=repoAddress;
  f.elements.webUrl.value=usesSSH?repo.webUrl||'':'';
  f.elements.name.value=repo?.name||'';
  f.elements.defaultBranch.value=repo?.defaultBranch||'main';
  f.elements.integrateBranch.value=repo?.integrateBranch||'';
  fillRepoBranchNaming(f,repo?.workBranchNaming);
  f.elements.localPath.value=repo?.localPath||'';
  $('#repoDrawerAdvanced').open=!!repo?.localPath;
  $('#repoDrawerNameField').hidden=!repoDrawerState.nameTouched;
  $('#repoDrawerTitle').textContent=repo?'编辑代码仓库':'添加代码仓库';
  $('#repoDrawerSave').disabled=false;
  setRepoDrawerSwitch(repo?project.defaultRepositoryId===repo.id:!hasDefault);
  gitSSHKeyCache=null;
  repoDrawerError(false);syncRepoDrawerDetect();
  const drawer=$('#repoDrawer');
  drawer.inert=false;drawer.setAttribute('aria-hidden','false');
  drawer.classList.add('open');$('#repoDrawerScrim').classList.add('open');
  if(typeof mountGitHubRepoPicker==='function')mountGitHubRepoPicker(drawer.querySelector('[data-github-repositories]'),repository=>{
    f.elements.provider.value='';
    f.elements.repoUrl.value=repository.html_url;
    f.elements.defaultBranch.value=repository.default_branch||'main';
    syncRepoDrawerDetect();
  });
  setTimeout(()=>f.elements.repoUrl.focus(),120);
}
function closeRepoDrawer(opt){
  const drawer=$('#repoDrawer');if(!drawer||!drawer.classList.contains('open'))return;
  drawer.classList.remove('open');$('#repoDrawerScrim')?.classList.remove('open');
  drawer.setAttribute('aria-hidden','true');drawer.inert=true;
  const back=repoDrawerState.returnFocus;repoDrawerState.returnFocus=null;
  if(opt?.restoreFocus!==false&&back&&document.contains(back))back.focus?.();
}
function repoDrawerPayload(f,info){
  const existing=(state.data.repositories||[]).find(r=>r.id===repoDrawerState.repoId)||{};
  const payload={...existing,id:existing.id||undefined,projectId:repoDrawerState.projectId,provider:info.provider||'gongfeng',name:f.elements.name.value.trim()||info.name,gongfengProjectId:info.project,webUrl:info.webUrl,cloneUrl:info.cloneUrl,defaultBranch:f.elements.defaultBranch.value.trim()||'main',integrateBranch:f.elements.integrateBranch.value.trim(),localPath:f.elements.localPath.value.trim(),repoUrl:f.elements.repoUrl.value.trim()};
  payload.workBranchNaming=repoBranchNamingPayload(f);
  if(['ssh','http','https'].includes(info.provider)){payload.gongfengProjectId='';payload.webUrl=f.elements.webUrl.value.trim()}
  delete payload.gitToken;if(!payload.id)delete payload.id;
  return payload;
}
// A project with no valid default repository adopts the next repository saved into it.
function projectNeedsDefaultRepository(project){
  return !(state.data.repositories||[]).some(r=>r.projectId===project?.id&&r.id===project?.defaultRepositoryId);
}
async function saveProjectDefaultRepository(project,repoId){
  const next=await persistProject({...project,defaultRepositoryId:repoId});
  if(next)syncProjectDraftDefaultRepo(project.id,next.defaultRepositoryId||'');
  return next;
}
async function saveRepoDrawer(e){
  e.preventDefault();
  if(repoDrawerState.saving)return;
  const f=e.currentTarget,info=repoDrawerInfo(f);
  if(!info){repoDrawerError(true);f.elements.repoUrl.focus();return}
  const projectId=repoDrawerState.projectId,project=(state.data.projects||[]).find(p=>p.id===projectId);
  if(!project)return toast('请先保存项目','warn');
  const makeDefault=$('#repoDrawerDefault').classList.contains('on'),payload=repoDrawerPayload(f,info),editing=!!payload.id;
  if(payload.workBranchNaming.mode==='prefix'&&f.elements.workBranchPrefix.value==='custom'&&!payload.workBranchNaming.prefix){
    f.elements.workBranchCustomPrefix.focus();
    return toast('请输入自定义前缀，或选择无前缀','warn');
  }
  if(payload.workBranchNaming.mode==='ai'&&(!payload.workBranchNaming.executorId||!payload.workBranchNaming.model)){
    return toast(lfT('请选择 AI 分支命名的 CLI 和模型','Choose the CLI and model for AI branch naming'),'warn');
  }
  const branchExecutor=payload.workBranchNaming.executorId;
  if(payload.workBranchNaming.mode==='ai'&&branchExecutor!==repoDrawerState.branchExecutorId&&!executorConfigured(branchExecutor)){
    f.elements.workBranchExecutorId.focus();
    return toast(executorModelCatalogRequest?lfT('正在检查执行器配置，请稍后再试','Checking executor configuration, try again shortly'):lfT('请选用已配置的 CLI','Choose a configured CLI'),'warn');
  }
  repoDrawerState.saving=true;$('#repoDrawerSave').disabled=true;
  try{
    captureProjectAdminForm();
    const repo=await api('/api/repositories',{method:editing?'PUT':'POST',body:JSON.stringify(payload)});
    const list=state.data.repositories||(state.data.repositories=[]),ri=list.findIndex(r=>r.id===repo.id);
    if(ri>=0)list[ri]=repo;else list.push(repo);
    const wasDefault=project.defaultRepositoryId===repo.id,nextDefault=makeDefault?repo.id:(wasDefault?'':project.defaultRepositoryId||'');
    let defaultError='';
    if(nextDefault!==(project.defaultRepositoryId||'')){
      try{await saveProjectDefaultRepository(project,nextDefault)}
      catch(err){defaultError=`仓库已保存，${makeDefault?'设为默认':'取消默认'}失败：${err.message}`}
    }
    closeRepoDrawer({restoreFocus:false});
    renderAll();
    refreshProjectAdminRepos(repo.id);
    toast(defaultError||(editing?'仓库已保存':`仓库已添加${makeDefault&&!wasDefault?'，并设为默认':''}`),defaultError?'err':'ok');
  }catch(err){toast(err.message,'err')}
  finally{repoDrawerState.saving=false;const btn=$('#repoDrawerSave');if(btn)btn.disabled=false}
}
function bindRepoDrawer(){
  const f=$('#repoDrawerForm');if(!f||f.dataset.bound)return;f.dataset.bound='true';
  f.onsubmit=saveRepoDrawer;
  f.elements.provider.addEventListener('change',syncRepoDrawerDetect);
  f.elements.webUrl.addEventListener('input',syncRepoDrawerDetect);
  f.elements.repoUrl.addEventListener('input',syncRepoDrawerDetect);
  f.elements.name.addEventListener('input',()=>{repoDrawerState.nameTouched=!!f.elements.name.value.trim();syncRepoDrawerDetect()});
  f.elements.workBranchNamingMode.addEventListener('change',()=>syncRepoBranchNaming(f));
  f.elements.workBranchPrefix.addEventListener('change',()=>syncRepoBranchNaming(f));
  f.elements.workBranchExecutorId.addEventListener('change',()=>fillRepoBranchModels(f));
  f.elements.workBranchModelChoice.addEventListener('change',()=>{
    const manual=f.elements.workBranchModelChoice.value===AGENT_CUSTOM_MODEL;
    f.querySelector('[data-work-branch-custom-model]').hidden=!manual;
    if(manual)f.elements.workBranchCustomModel.focus();
  });
  $('#repoDrawerRename').onclick=()=>{repoDrawerState.nameTouched=true;$('#repoDrawerNameField').hidden=false;f.elements.name.focus();f.elements.name.select()};
  $('#repoDrawerDefault').onclick=()=>setRepoDrawerSwitch(!$('#repoDrawerDefault').classList.contains('on'));
  $$('[data-repo-drawer-close]').forEach(b=>b.onclick=closeRepoDrawer);
  $('#repoDrawerScrim').onclick=closeRepoDrawer;
  document.addEventListener('keydown',e=>{
    if(e.key!=='Escape'||!repoDrawerOpen()||$('#gitSSHKeyDialog')?.open||$('#confirmModal')?.classList.contains('open'))return;
    e.preventDefault();e.stopImmediatePropagation();closeRepoDrawer();
  },true);
}
