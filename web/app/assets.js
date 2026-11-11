function assetScopeKey(type){return ({rules:'ruleScope',skills:'skillScope',dockerfiles:'dockerfileScope',environments:'environmentScope',sandboxes:'sandboxScope'})[type]||'ruleScope'}
function isProjectSharedAsset(type){return ['rules','skills','dockerfiles','environments','sandboxes'].includes(type)}
function assetPageCopy(type=state.assetType){
  return ({
    rules:{group:'Agent 能力',page:'能力资产',tab:'规则',desc:'维护当前项目的行为约束和执行规则。',create:'新建规则'},
    skills:{group:'Agent 能力',page:'能力资产',tab:'技能',desc:'维护当前项目可供角色使用的技能和附加文件。',create:'新建技能'},
    dockerfiles:{group:'运行环境',page:'环境资产',tab:'Dockerfile',desc:'维护当前项目的容器构建定义。',create:'新建 Dockerfile'},
    environments:{group:'运行环境',page:'环境资产',tab:'安装脚本',desc:'维护安装到隔离执行环境中的工具与依赖。',create:'新建安装脚本'},
    sandboxes:{group:'运行环境',page:'环境资产',tab:'沙箱',desc:'维护当前项目的运行隔离方式及环境依赖。',create:'新建沙箱'}
  })[type]||{group:'运行环境',page:'环境资产',tab:'Dockerfile',desc:'维护当前项目的容器构建定义。',create:'新建 Dockerfile'};
}
function assetSharedDescription(type=state.assetType){return type==='rules'?'查看其他项目共享的规则；共享规则只读，可直接绑定到本项目角色。':type==='skills'?'查看其他项目共享的技能；共享技能只读，可直接绑定到本项目角色。':'查看其他项目共享的环境资产；共享资产只读，可直接用于本项目。'}
function assetIconName(type=state.assetType){return ({rules:'gate',skills:'skills',dockerfiles:'layers',environments:'terminal',sandboxes:'server'})[type]||'layers'}
function assetRefIconName(type=state.assetType){return ({rules:'robot',skills:'robot',dockerfiles:'server',environments:'server',sandboxes:'workflow'})[type]||'layers'}
function assetScopeItems(scope){return (state.data[state.assetType]||[]).filter(item=>scope==='shared'?catalogShared(item.projectId,item.shared):catalogOwned(item.projectId))}
function assetMatchesQuery(a,query){const q=String(query||'').trim().toLowerCase();if(!q)return true;return [a?.name,a?.id,a?.description].some(v=>String(v||'').toLowerCase().includes(q))}
function assetFilteredItems(){return assetScopeItems(state[assetScopeKey(state.assetType)]).filter(a=>assetMatchesQuery(a,state.assetQuery))}
// Owned assets win over a same-id shared copy so the editable one opens.
function assetFindVisible(id){if(!id)return null;const owned=assetScopeItems('project').find(x=>x.id===id);if(owned)return {asset:owned,scope:'project'};const shared=assetScopeItems('shared').find(x=>x.id===id);return shared?{asset:shared,scope:'shared'}:null}
function assetProjectName(a){return (state.data.projects||[]).find(p=>p.id===a?.projectId)?.name||a?.projectId||'平台'}
function assetIsBuiltin(a,type=state.assetType){return (type==='sandboxes'&&(a?.id==='local'||a?.id==='generic-docker'))||(type==='dockerfiles'&&a?.id==='generic-backend')}
function assetStatusHTML(a){
  if(!catalogOwned(a.projectId))return `<span class="asset-status is-from"><i></i>来自 ${escapeHTML(assetProjectName(a))}</span>`;
  return `<span class="asset-status ${a.shared?'is-on':''}"><i></i>${a.shared?'已共享':'未共享'}</span>`;
}
function assetMetaHTML(a){
  const item=(icon,text)=>`<span>${huiIcon(icon)}${escapeHTML(text)}</span>`;const meta=[];
  if(state.assetType==='rules'){meta.push(item('priority-bolt',`优先级 ${a.priority||0}`));if(a.global){const bound=(a.workflowIds||[]).length;meta.push(item('globe',bound?`全局 · ${bound} 个 SOP`:'全局'))}}
  if(state.assetType==='skills'){
    meta.push(item('doc',`${Object.keys(a.files||{}).length+(a.fileRefs||[]).length} 个文件`));
    const src=assetSkillSource(a);
    if(src){const latest=assetSkillLatest(src);meta.push(latest&&latest!==src.version?`<span class="hui-badge" data-tone="warn" data-size="sm"><span class="hui-lamp"><i></i></span><span class="hui-badge-label">SkillHub v${escapeHTML(src.version)} · 可更新</span></span>`:`<span class="hui-badge" data-tone="accent" data-size="sm">${huiIcon('sparkles')}<span class="hui-badge-label">SkillHub v${escapeHTML(src.version)}</span></span>`)}
  }
  if(state.assetType==='dockerfiles')meta.push(item('doc',`${(a.content||'').split('\n').length} 行`));
  if(state.assetType==='environments')meta.push(item('variable',`${Object.keys(a.variables||{}).length} 个变量`));
  if(state.assetType==='sandboxes')meta.push(item('server',a.driver||'local'));
  const refs=assetReferenceNames(a).length;
  meta.push(item(assetRefIconName(),refs?`${refs} 个${assetRefKindLabel()}引用`:'未被引用'));
  return meta.join('');
}
function assetCardHTML(a){
  const readonly=!catalogOwned(a.projectId);const go=readonly?'查看':'编辑';
  return `<article class="asset-card" role="link" tabindex="0" data-asset-id="${escapeHTML(a.id)}" aria-label="${go}${escapeHTML(assetKindLabel())}「${escapeHTML(a.name||a.id)}」"><div class="asset-card-top"><span class="asset-tile">${huiIcon(assetIconName())}</span><div class="asset-card-name"><h3>${escapeHTML(a.name||a.id)}</h3><code class="asset-mono">${escapeHTML(a.id)}</code></div></div><p class="asset-card-desc">${escapeHTML(a.description||'暂无说明')}</p><div class="asset-meta">${assetMetaHTML(a)}</div><div class="asset-card-foot">${assetStatusHTML(a)}<span class="asset-card-go">${go}${huiIcon('chevron-right')}</span></div></article>`;
}
function assetGridEmptyHTML(){
  const kind=assetKindLabel(),copy=assetPageCopy();
  if(String(state.assetQuery||'').trim())return `<div class="asset-empty"><b>没有匹配「${escapeHTML(state.assetQuery.trim())}」的${escapeHTML(kind)}</b><p>换个关键词试试。</p></div>`;
  if(state[assetScopeKey(state.assetType)]==='shared'){const hint=['rules','skills'].includes(state.assetType)?'请在资产所属项目中共享，或共享一个引用它的角色。':'请在资产所属项目中共享；共享 SOP 时也会自动共享其环境依赖。';return `<div class="asset-empty"><b>其他项目还没有共享${escapeHTML(kind)}</b><p>${hint}</p></div>`}
  return `<div class="asset-empty"><b>当前项目还没有${/^[A-Za-z]/.test(kind)?' ':''}${escapeHTML(kind)}</b><p>${assetEmptyHint()}</p><button type="button" class="button primary" data-empty-new>${huiIcon('plus')}${escapeHTML(copy.create)}</button></div>`;
}
function renderAssetGrid(){
  const list=$('#assetList');if(!list)return;
  const scope=state[assetScopeKey(state.assetType)];
  const items=assetFilteredItems();
  const count=$('#assetCount');if(count)count.innerHTML=`${scope==='shared'?'共享':'本项目'}${escapeHTML(assetKindLabel())}<b>${items.length}</b>`;
  list.innerHTML=items.map(assetCardHTML).join('')||assetGridEmptyHTML();
  list.querySelector('[data-empty-new]')?.addEventListener('click',()=>openAssetDetail('__new__'));
  list.querySelectorAll('[data-asset-id]').forEach(card=>{
    card.onclick=()=>openAssetDetail(card.dataset.assetId);
    card.onkeydown=e=>{if(e.key==='Enter'&&e.target===card){e.preventDefault();openAssetDetail(card.dataset.assetId)}};
  });
}
function renderAssetListChrome(){
  const scope=state[assetScopeKey(state.assetType)],copy=assetPageCopy();
  if($('#assetPageEyebrow'))$('#assetPageEyebrow').textContent=copy.group;
  if($('#assetPageTitle'))$('#assetPageTitle').textContent=copy.page;
  if($('#assetPageDescription'))$('#assetPageDescription').textContent=scope==='skillhub'?'从 SkillHub 搜索公开技能，查看质量评测与安全认证后安装到本项目。':scope==='shared'?assetSharedDescription():copy.desc;
  if($('#newAssetLabel'))$('#newAssetLabel').textContent=copy.create;
  if($('#newAsset'))$('#newAsset').hidden=scope!=='project';
  if(typeof skillhubSyncChrome==='function')skillhubSyncChrome();
  $$('[data-asset-scope]').forEach(btn=>btn.classList.toggle('active',btn.dataset.assetScope===scope));
  $$('[data-asset-scope-count]').forEach(el=>el.textContent=String(assetScopeItems(el.dataset.assetScopeCount).length));
  const search=$('#assetSearch');if(search&&search.value!==(state.assetQuery||''))search.value=state.assetQuery||'';
}
function resetAssetDetailState(){state.assetCreateDefaults={};state.selectedAsset=null;state.assetDirty=false;state.assetDetailKey='';state.assetTab=null;closeAssetMenu()}
function renderAssets(){
  if(!$('#assetsView')||!state.data)return;
  const selected=state.selectedAsset;
  if(selected&&selected!=='__new__'&&state.data[state.assetType]){
    const hit=assetFindVisible(selected);
    if(!hit){
      // Project switch or a remote delete removed the open asset.
      resetAssetDetailState();
      if(routePart(0)===state.assetType&&assetRouteId())history.replaceState(null,'',assetViewHash(state.assetType));
    }else state[assetScopeKey(state.assetType)]=hit.scope;
  }
  renderAssetListChrome();
  const detail=!!state.selectedAsset;
  if($('#assetListPane'))$('#assetListPane').hidden=detail;
  if($('#assetDetailPane'))$('#assetDetailPane').hidden=!detail;
  if(detail)renderAssetDetail();
  else{state.assetDetailKey='';if(state.assetType==='skills'&&state.skillScope==='skillhub'&&typeof renderSkillHubMarket==='function')renderSkillHubMarket();else renderAssetGrid()}
}
function assetCurrent(){
  if(state.selectedAsset==='__new__')return {id:'',name:'',description:'',...state.assetCreateDefaults};
  return assetFindVisible(state.selectedAsset)?.asset||null;
}
function assetDetailHeadHTML(a,{creating=false,dirty=false}={}){
  const kind=assetKindLabel(),copy=assetPageCopy(),readonly=!creating&&!catalogOwned(a.projectId);
  const badge=creating?'':readonly?`<span class="asset-badge is-from">来自 ${escapeHTML(assetProjectName(a))}</span>`:`<span class="asset-badge ${a.shared?'is-on':''}">${a.shared?'已共享':'未共享'}</span>`;
  const title=creating?`新建${kind}`:(a.name||a.id);
  const desc=creating?`填写标识和名称后创建${state.assetType==='skills'?'，创建后即可上传文件':''}。`:(a.description||'暂无说明');
  let actions;
  if(creating)actions=`<button type="button" class="button" data-asset-cancel>取消</button><button type="submit" form="assetForm" class="button primary" data-asset-save>${huiIcon('plus')}创建${escapeHTML(kind)}</button>`;
  else if(readonly)actions=`<span class="asset-badge is-ro">${huiIcon('lock')}只读 · 可直接在本项目使用</span>`;
  else{
    const blocked=assetDeleteBlocked(a);
    actions=`<span class="asset-dirty" data-asset-dirty ${dirty?'':'hidden'}><i></i>未保存</span>`+
      `<button type="button" class="button" data-asset-share>${huiIcon(a.shared?'close':'arrow-up')}${a.shared?'取消共享':'共享给其他项目'}</button>`+
      `<div class="asset-more"><button type="button" class="button hui-icon-button" id="assetMoreBtn" aria-haspopup="menu" aria-expanded="false" aria-controls="assetMoreMenu" aria-label="更多操作" title="更多操作">${huiIcon('more-horizontal')}</button>`+
      `<div id="assetMoreMenu" class="hui-menu asset-menu" role="menu" inert><button type="button" role="menuitem" class="hui-menu-item is-danger" data-asset-delete ${blocked?'disabled aria-disabled="true"':''}>${huiIcon('trash')}<span class="hui-menu-label">删除${escapeHTML(kind)}</span></button>${blocked?`<p class="asset-menu-reason" data-asset-delete-reason>${escapeHTML(blocked)}</p>`:''}</div></div>`+
      `<button type="submit" form="assetForm" class="button primary" data-asset-save>${huiIcon('save')}保存修改</button>`;
  }
  return `<div class="asset-dtitle"><button type="button" class="button hui-icon-button asset-back" data-asset-back aria-label="返回${escapeHTML(copy.page)}" title="返回${escapeHTML(copy.page)}">${huiIcon('arrow-left')}</button><div class="asset-dtitle-text"><h1><span class="asset-dname">${escapeHTML(title)}</span>${badge}</h1><p>${escapeHTML(desc)}</p></div></div><div class="asset-acts">${actions}</div>`;
}
function renderAssetDetailHead(a){
  const creating=state.selectedAsset==='__new__',copy=assetPageCopy();
  const crumbs=$('#assetCrumbs');
  if(crumbs)crumbs.innerHTML=`<a href="${assetViewHash(state.assetType)}" data-asset-back>${escapeHTML(copy.page)} · ${escapeHTML(copy.tab)}</a>${huiIcon('chevron-right')}<span class="is-current">${escapeHTML(creating?`新建${assetKindLabel()}`:(a.name||a.id))}</span>`;
  const head=$('#assetDetailHead');if(!head)return;
  closeAssetMenu();
  head.innerHTML=assetDetailHeadHTML(a,{creating,dirty:!!state.assetDirty});
  $$('#assetDetailPane [data-asset-back], #assetDetailPane [data-asset-cancel]').forEach(el=>el.onclick=e=>{e.preventDefault();closeAssetDetail()});
  const share=head.querySelector('[data-asset-share]');if(share)share.onclick=()=>toggleAssetShare(a.id);
  const more=$('#assetMoreBtn');if(more)more.onclick=e=>{e.stopPropagation();$('#assetMoreMenu')?.classList.contains('is-open')?closeAssetMenu(true):openAssetMenu()};
  const del=head.querySelector('[data-asset-delete]');if(del)del.onclick=()=>{if(del.disabled)return;closeAssetMenu();deleteAsset(a.id)};
  bindAssetDetailChrome();
  updateAssetStuck();
}
function openAssetMenu(){
  const menu=$('#assetMoreMenu'),btn=$('#assetMoreBtn');if(!menu||!btn)return;
  window.HuiSelect?.closeAll?.();
  menu.inert=false;menu.classList.add('is-open');btn.setAttribute('aria-expanded','true');
  menu.querySelector('[role="menuitem"]:not(:disabled)')?.focus();
}
function closeAssetMenu(focusTrigger=false){
  const menu=$('#assetMoreMenu'),btn=$('#assetMoreBtn');if(!menu||!menu.classList.contains('is-open'))return;
  menu.classList.remove('is-open');menu.inert=true;btn?.setAttribute('aria-expanded','false');
  if(focusTrigger)btn?.focus();
}
let assetDetailChromeBound=false;
function bindAssetDetailChrome(){
  if(assetDetailChromeBound)return;assetDetailChromeBound=true;
  document.addEventListener('pointerdown',e=>{if(!(e.target instanceof Element)||!e.target.closest('.asset-more'))closeAssetMenu()});
  document.addEventListener('keydown',e=>{if(e.key==='Escape'&&$('#assetMoreMenu')?.classList.contains('is-open')){e.preventDefault();closeAssetMenu(true)}});
  document.addEventListener('scroll',updateAssetStuck,{capture:true,passive:true});
}
function updateAssetStuck(){
  const head=$('#assetDetailHead'),sentinel=$('#assetStickSentinel');
  if(!head||!sentinel||$('#assetDetailPane')?.hidden)return;
  head.classList.toggle('is-stuck',sentinel.getBoundingClientRect().top<head.getBoundingClientRect().top-1);
}
function assetRefsCardHTML(a){
  const refs=assetReferenceNames(a),kind=assetRefKindLabel();
  return `<h4>被引用<span class="asset-side-count">${refs.length}</span></h4><ul class="asset-refs">${refs.length?refs.map(name=>`<li>${huiIcon(assetRefIconName())}<span>${escapeHTML(name)}</span></li>`).join(''):`<li class="is-none">还没有${kind}引用这个${escapeHTML(assetKindLabel())}</li>`}</ul>`;
}
function assetSkillSource(a){const s=a?.source;return state.assetType==='skills'&&s&&s.provider==='skillhub'?s:null}
function assetSkillLatest(src){return typeof skillhubLatestFor==='function'?skillhubLatestFor(src):''}
function assetSkillSourceRowsHTML(a){
  const src=assetSkillSource(a);if(!src)return '';
  const latest=assetSkillLatest(src),homepage=/^https?:\/\//i.test(src.homepage||'')?src.homepage:'';
  const date=v=>{const d=new Date(v);return v&&!Number.isNaN(d.getTime())?d.toLocaleDateString('zh-CN'):'—'};
  return `<dt>来源</dt><dd>${homepage?`<a href="${escapeHTML(homepage)}" target="_blank" rel="noopener noreferrer">SkillHub · ${escapeHTML(src.slug)}</a>`:`SkillHub · ${escapeHTML(src.slug)}`}</dd><dt>版本</dt><dd>v${escapeHTML(src.version)}${latest&&latest!==src.version?` · 最新 v${escapeHTML(latest)}`:''}</dd><dt>安装于</dt><dd>${escapeHTML(date(src.installedAt))}</dd>${src.updatedAt&&src.updatedAt!==src.installedAt?`<dt>更新于</dt><dd>${escapeHTML(date(src.updatedAt))}</dd>`:''}`;
}
function assetSkillSourceActionHTML(a){
  const src=assetSkillSource(a);if(!src||!catalogOwned(a.projectId))return '';
  const latest=assetSkillLatest(src);
  return latest&&latest!==src.version?`<button type="button" class="button primary asset-source-btn" data-skillhub-source-update>${huiIcon('refresh')}更新到 v${escapeHTML(latest)}</button>`:`<button type="button" class="button hui-btn-soft asset-source-btn" data-skillhub-source-check>${huiIcon('refresh')}检查更新</button>`;
}
function assetOverviewCardHTML(a){
  const readonly=!catalogOwned(a.projectId);
  return `<h4>概览</h4><dl class="asset-kv"><dt>归属</dt><dd>${readonly?escapeHTML(assetProjectName(a)):'本项目'}</dd><dt>共享</dt><dd>${readonly?'其他项目共享':a.shared?'已共享':'未共享'}</dd>${assetIsBuiltin(a)?'<dt>类型</dt><dd>内置</dd>':''}${assetSkillSourceRowsHTML(a)}</dl>${assetSkillSourceActionHTML(a)}`;
}
function bindAssetSourceActions(a){
  const card=$('#assetForm [data-asset-overview]');if(!card||!assetSkillSource(a))return;
  const up=card.querySelector('[data-skillhub-source-update]');if(up)up.onclick=()=>typeof openSkillhubUpdate==='function'&&openSkillhubUpdate(a,{latest:assetSkillLatest(a.source),opener:up});
  const check=card.querySelector('[data-skillhub-source-check]');if(check)check.onclick=()=>typeof checkSkillhubUpdate==='function'&&checkSkillhubUpdate(a,check);
}
function refreshAssetAside(a){
  const refs=$('#assetForm [data-asset-refs]');if(refs)refs.innerHTML=assetRefsCardHTML(a);
  const overview=$('#assetForm [data-asset-overview]');if(overview){overview.innerHTML=assetOverviewCardHTML(a);bindAssetSourceActions(a)}
  if(state.assetType==='skills'){refreshSkillFileTree();const summary=$('#assetForm [data-skill-summary]');if(summary)summary.innerHTML=skillFileSummary(a)}
}
function renderAssetDetail({force=false}={}){
  const a=assetCurrent();if(!a)return;
  const key=`${state.assetType}:${state.selectedAsset}`,same=key===state.assetDetailKey;
  renderAssetDetailHead(a);
  if(same&&state.assetDirty&&!force){refreshAssetAside(a);return}
  state.assetDetailKey=key;
  renderAssetForm(a);
}
function markAssetDirty(){
  if(!state.selectedAsset||(state.selectedAsset!=='__new__'&&!catalogOwned(assetCurrent()?.projectId)))return;
  state.assetDirty=true;$('#assetDetailHead [data-asset-dirty]')?.removeAttribute('hidden');
}
function scrollAssetTop(){
  for(let el=$('#assetsView');el;el=el.parentElement)if(el.scrollTop)el.scrollTop=0;
  window.scrollTo?.(0,0);
}
async function confirmAssetLeave(){
  if(!state.selectedAsset||!state.assetDirty||!isProjectSharedAsset(activeViewName()))return true;
  const ok=await afConfirm({title:'未保存的修改',message:`当前${assetKindLabel()}有未保存修改，离开将放弃这些修改。继续？`,confirmText:'放弃并离开',danger:true});
  if(ok)state.assetDirty=false;
  return ok;
}
async function createConversationSandbox(projectId){
  if(!(state.data.projects||[]).some(project=>project.id===projectId))return;
  if(!await confirmAssetLeave())return;
  if(state.projectFilter!==projectId){
    const select=$('#projectSwitcher');
    select.value=projectId;
    await select.onchange({target:select});
    if(state.projectFilter!==projectId)return;
  }
  switchView('sandboxes');
  openAssetDetail('__new__',{driver:'docker'});
}
function openAssetDetail(id,defaults={}){
  if(!id||state.selectedAsset===id)return;
  const type=state.assetType;
  if(id==='__new__'){state[assetScopeKey(type)]='project';state.assetCreateDefaults=defaults}
  state.selectedAsset=id;state.assetDirty=false;state.assetTab=null;state.assetDetailKey='';state.skillUploadStatus='';
  const next=assetViewHash(type,id);
  if(location.hash!==next)history.pushState({assetFrom:assetViewHash(type)},'',next);
  renderAssets();
  scrollAssetTop();
}
async function closeAssetDetail({force=false}={}){
  if(!state.selectedAsset)return true;
  if(!force&&!(await confirmAssetLeave()))return false;
  const type=state.assetType,list=assetViewHash(type);
  const fromList=history.state?.assetFrom===list&&routePart(0)===type&&!!assetRouteId();
  resetAssetDetailState();
  if(fromList)history.back();
  else if(location.hash!==list)history.replaceState(null,'',list);
  renderAssets();
  scrollAssetTop();
  return true;
}
async function toggleAssetShare(id){
  const item=(state.data[state.assetType]||[]).find(x=>x.id===id);
  if(!item||!catalogOwned(item.projectId))return toast('只能共享本项目的资产');
  try{
    const saved={...item,shared:!item.shared};
    await api(`/api/${state.assetType}`,{method:'PUT',body:JSON.stringify(saved)});
    Object.assign(item,saved);
    if(state.selectedAsset===id){renderAssetDetailHead(item);refreshAssetAside(item)}
    else renderAssets();
    const agent=state.data.agents.find(a=>a.id===state.selectedAgent)||state.agentDraft;if(agent)renderAgentAssetsPanel(agent);
    toast(saved.shared?'已共享，其他项目可绑定':'已取消共享');
  }catch(e){toast(e.message)}
}
function assetEmptyHint(){return ({rules:'规则是写给 Agent 的行为约束，例如编码规范、禁止事项。建好后在「角色定义」里绑定到角色。',skills:'技能是 Agent 可按需加载的说明和附加文件，例如某个框架的操作手册。建好后在「角色定义」里绑定到角色。',dockerfiles:'Dockerfile 定义沙箱容器的镜像。建好后在「沙箱管理」里选用；也可以直接用「共享」里的现成配置。',environments:'安装脚本在沙箱初始化时把工具装进隔离环境，例如指定版本的 Go、PHP、Node。建好后在「沙箱管理」里选用。',sandboxes:'沙箱决定 SOP 节点在什么隔离环境里执行，由 Dockerfile 和安装脚本组合而成。建好后在流程设置里选用；也可以直接用「共享」里的现成配置。'})[state.assetType]||''}
function assetKindLabel(){return ({rules:'规则',skills:'技能',dockerfiles:'Dockerfile',environments:'环境安装脚本',sandboxes:'沙箱'})[state.assetType]||'资产'}
function assetRefKindLabel(){return ({rules:'角色',skills:'角色',dockerfiles:'沙箱',environments:'沙箱',sandboxes:'流程'})[state.assetType]||'引用'}
function assetReferenceNames(a){
  if(!a?.id)return [];
  if(state.assetType==='rules')return (state.data.agents||[]).filter(x=>(x.rules||[]).includes(a.id)).map(x=>x.name||x.id);
  if(state.assetType==='skills')return (state.data.agents||[]).filter(x=>(x.skills||[]).includes(a.id)).map(x=>x.name||x.id);
  if(state.assetType==='dockerfiles')return (state.data.sandboxes||[]).filter(x=>x.dockerfileId===a.id).map(x=>x.name||x.id);
  if(state.assetType==='environments')return (state.data.sandboxes||[]).filter(x=>x.environmentId===a.id).map(x=>x.name||x.id);
  if(state.assetType==='sandboxes')return (state.data.workflows||[]).filter(x=>!catalogGeneric(x)&&x.sandboxProfileId===a.id).map(x=>x.name||x.id);
  return [];
}
function assetDeleteBlocked(a){
  if(!a?.id)return '无法删除';
  if(state.assetType==='sandboxes'&&(a.id==='local'||a.id==='generic-docker'))return `内置沙箱 ${a.id} 不能删除`;
  if(state.assetType==='dockerfiles'&&a.id==='generic-backend')return '内置 Dockerfile generic-backend 不能删除';
  if(a.shared)return `共享状态的${assetKindLabel()}不能删除，请先取消共享`;
  const refs=assetReferenceNames(a);
  if(refs.length)return `${assetKindLabel()}仍被 ${refs.length} 个${assetRefKindLabel()}引用（${refs.join('、')}），请先解除引用`;
  return '';
}
async function deleteAsset(id){
  const item=(state.data[state.assetType]||[]).find(x=>x.id===id);
  if(!item)return toast('资产不存在或已删除');
  if(!catalogOwned(item.projectId))return toast('只能删除本项目的资产');
  const blocked=assetDeleteBlocked(item);
  if(blocked)return toast(blocked);
  const label=assetKindLabel();
  if(!await afConfirm({title:`删除${label}确认`,message:`确认删除${label}「${item.name}」？此操作不可恢复。`,confirmText:'确认删除',danger:true}))return;
  try{
    await api(`/api/${state.assetType}?id=${encodeURIComponent(id)}`,{method:'DELETE'});
    state.data[state.assetType]=(state.data[state.assetType]||[]).filter(x=>x.id!==id);
    if(state.selectedAsset===id)await closeAssetDetail({force:true});
    else renderAssets();
    toast(`${label}已删除`);
  }catch(e){toast(e.message||'删除失败')}
}
function assetKind(){return assetKindLabel()}
function ruleGlobalFields(a){const on=!!a.global;return `<div class="rule-global-block"><label class="rule-global-row"><span><b>作为全局规则</b><small>开启后选择要生效的 SOP。该 SOP 的每个节点执行时都会带上这条规则。</small></span><input name="global" type="checkbox" class="rule-global-input" role="switch" ${on?'checked':''}></label><div id="ruleSOPPickerWrap" class="rule-global-sops" ${on?'':'hidden'}><label>应用到 SOP</label><div id="ruleSOPPicker" class="repo-picker"></div><div class="json-help">从下拉框添加，点击标签移除。选中的 SOP 在运行时，其全部节点都会注入本规则。</div></div></div>`}
function ruleSOPChoices(){return runnableWorkflowOptions()}
function selectedRuleWorkflowIds(){try{const ids=JSON.parse($('#ruleSOPPicker')?.dataset.ids||'[]');return Array.isArray(ids)?ids:[]}catch{return []}}
function setRuleWorkflowIds(ids){const host=$('#ruleSOPPicker');if(!host)return;const known=new Set(ruleSOPChoices().map(w=>w.id));host.dataset.ids=JSON.stringify([...new Set((ids||[]).map(id=>String(id||'').trim()).filter(id=>id&&known.has(id)))]);renderRuleSOPPicker();markAssetDirty()}
function renderRuleSOPPicker(){const host=$('#ruleSOPPicker');if(!host)return;const sops=ruleSOPChoices();const selected=selectedRuleWorkflowIds();const chosen=new Set(selected);const leftover=sops.filter(w=>!chosen.has(w.id));const tags=selected.map(id=>{const sop=sops.find(w=>w.id===id);const name=sop?.name||id;const nodes=sop?.nodes?.length||0;const label=nodes?`${name} · ${nodes} 个节点`:name;return `<button type="button" class="repo-picker-tag" data-remove-rule-sop="${escapeHTML(id)}" title="点击移除">${escapeHTML(label)}<span aria-hidden="true">×</span></button>`}).join('');const options=leftover.map(w=>`<option value="${escapeHTML(w.id)}">${escapeHTML(w.name)}${w.nodes?.length?` · ${w.nodes.length} 个节点`:''}</option>`).join('');const placeholder=!sops.length?'暂无可选 SOP':(!leftover.length?'已全部选择':'＋ 添加 SOP…');host.innerHTML=`<div class="repo-picker-field">${tags}<select class="repo-picker-select"${leftover.length?'':' disabled'}><option value="">${placeholder}</option>${options}</select></div>`;host.querySelectorAll('[data-remove-rule-sop]').forEach(b=>b.onclick=()=>setRuleWorkflowIds(selected.filter(id=>id!==b.dataset.removeRuleSop)));const sel=host.querySelector('select');if(sel)sel.onchange=()=>{if(!sel.value)return;setRuleWorkflowIds([...selected,sel.value])}}
function bindRuleGlobalPicker(a){const host=$('#ruleSOPPicker');if(!host)return;const known=new Set(ruleSOPChoices().map(w=>w.id));host.dataset.ids=JSON.stringify((Array.isArray(a.workflowIds)?a.workflowIds:[]).filter(id=>known.has(id)));const toggle=document.querySelector('#assetForm [name="global"]');const wrap=$('#ruleSOPPickerWrap');const sync=()=>{if(wrap)wrap.hidden=!(toggle&&toggle.checked)};if(toggle)toggle.onchange=sync;sync();renderRuleSOPPicker()}
function dockerfileOptions(selected){const items=(state.data.dockerfiles||[]).filter(x=>catalogVisible(x.projectId,x.shared));if(!items.length)return `<option value="">请先创建 Dockerfile</option>`;return `<option value="">选择 Dockerfile</option>`+items.map(x=>`<option value="${escapeHTML(x.id)}" ${x.id===selected?'selected':''}>${escapeHTML(x.name||x.id)}${catalogForeign(x.projectId)?' · 共享':''}</option>`).join('')}
function environmentOptions(selected){const items=(state.data.environments||[]).filter(x=>catalogVisible(x.projectId,x.shared));return `<option value="">不选择（可用 workspace 的 agentflow-environment）</option>`+items.map(x=>`<option value="${escapeHTML(x.id)}" ${x.id===selected?'selected':''}>${escapeHTML(x.name||x.id)}${catalogForeign(x.projectId)?' · 共享':''}</option>`).join('')}
function syncSandboxDriverFields(select){const driver=select?.value||'local';select?.form?.querySelectorAll('[data-sandbox-drivers]').forEach(el=>{el.hidden=!el.dataset.sandboxDrivers.split(' ').includes(driver)})}
function resolveAssetSaveIdentity(items,selectedID,requestedID){if(selectedID!=='__new__'){const existing=(items||[]).find(item=>item.id===selectedID);return existing?{id:existing.id,existing,created:false}:{error:'选中的资产已不存在，请刷新后重试'}}if((items||[]).some(item=>item.id===requestedID))return {error:'该标识已存在，请换一个标识，或回到列表打开已有资产修改'};return {id:requestedID,existing:null,created:true}}
function assetPaneTabs(type=state.assetType){return ({rules:[['edit','编辑'],['preview','预览']],skills:[['files','文件']],dockerfiles:[['content','Dockerfile'],['files','构建上下文']],environments:[['script','install.sh'],['vars','环境变量'],['path','PATH']],sandboxes:[['env','环境变量']]})[type]||[['edit','编辑']]}
function assetPaneHint(type=state.assetType){return ({rules:'支持 Markdown',skills:'每次上传都会完整替换现有文件',dockerfiles:'沙箱初始化时用库里这份 docker build',environments:'脚本把工具装进 $PREFIX',sandboxes:'驱动与执行环境在右侧设置，不要在这里保存凭证'})[type]||''}
function assetMainPanesHTML(a,{creating,readonly}){
  const pane=(key,body)=>`<div class="asset-tabpanel" data-asset-pane="${key}" role="tabpanel">${body}</div>`;
  if(state.assetType==='rules')return pane('edit',`<textarea name="content" class="asset-editor" rows="18" placeholder="写入必须遵守的行为和约束">${escapeHTML(a.content||'')}</textarea>`)+pane('preview',`<div class="asset-md markdown-body" data-asset-preview></div>`);
  if(state.assetType==='skills'){
    if(creating)return pane('files',`<div class="asset-empty is-inline"><b>先创建技能，再上传文件</b><p>支持文件夹、.zip 压缩包或单独的 SKILL.md。</p></div>`);
    const src=assetSkillSource(a),locked=readonly||!!src;
    return pane('files',`<div class="skill-upload-block">
    ${src?`<div class="asset-source-note">${huiIcon('lock')}<span>文件来自 SkillHub v${escapeHTML(src.version)}，只读；如需新版本请点更新。</span></div>`:''}
    <div class="skill-upload-actions"${locked?' hidden':''}>
      <input type="file" id="skillFolderPick" webkitdirectory multiple style="display:none">
      <input type="file" id="skillZipPick" accept=".zip,application/zip,application/x-zip-compressed" style="display:none">
      <input type="file" id="skillMdPick" accept=".md,.markdown,text/markdown" style="display:none">
      <button type="button" class="button skill-upload-btn" id="skillFolderBtn">${huiIcon('folder')}选择文件夹</button>
      <button type="button" class="button skill-upload-btn" id="skillZipBtn">${huiIcon('download')}上传 .zip 压缩包</button>
      <button type="button" class="button skill-upload-btn skill-upload-btn-overwrite" id="skillMdBtn">${huiIcon('doc')}上传 SKILL.md</button>
    </div>
    <div id="skillUploadStatus" class="skill-upload-status">${state.skillUploadStatus||''}</div>
    ${skillFileRowsHTML(a)}
    ${locked?'':`<div class="json-help">每次上传都是<b>完全替换</b>：文件夹 / zip 替换为整批文件；单独上传 SKILL.md 会先弹出确认，然后该技能<b>只保留这一份 SKILL.md</b>。单文件 ≤1MB，最多 500 个，单技能 ≤64MB。<b>zip 自动解包并保留可执行位</b>；markdown / 代码 / 图片可直接预览。</div>`}
  </div>`);
  }
  if(state.assetType==='dockerfiles')return pane('content',`<textarea name="content" class="asset-editor" rows="18" placeholder="FROM node:22-bookworm-slim">${escapeHTML(a.content||'')}</textarea><div class="json-help">正文和附加文件保存在这条 Dockerfile 资产里，写入数据库，和 <code>install.sh</code> 一样。不要指向仓库目录或现成镜像名。沙箱初始化时用库里这份 <code>docker build</code> 出一次性镜像。</div>`)+pane('files',`<label class="asset-field-label">构建上下文文件 JSON</label><textarea name="files" class="asset-editor is-short" rows="8" placeholder='{"packages.txt":"git\\ncurl"}'>${escapeHTML(JSON.stringify(a.files||{},null,2))}</textarea><div class="json-help">键是构建上下文里的相对路径，值是文件内容；与 Dockerfile 一起保存。</div>`);
  if(state.assetType==='environments')return pane('script',`<textarea name="installScript" class="asset-editor" rows="18" placeholder="#!/bin/sh&#10;set -e&#10;mkdir -p &quot;$PREFIX/bin&quot;&#10;curl -fsSL https://mirror.example/php.tgz | tar -xz -C &quot;$PREFIX&quot;">${escapeHTML(a.installScript||'')}</textarea><div class="json-help">脚本保存在环境资产里。Studio 保存即写库；下次沙箱初始化会按库里这份执行，把工具装进 <code>$PREFIX</code>。不要往宿主机写。脚本内容变化后会重新安装。</div>`)+pane('vars',`<label class="asset-field-label">环境变量 JSON</label><textarea name="variables" class="asset-editor is-short" rows="8" placeholder='{"PHP_VERSION":"7.4.33"}'>${escapeHTML(JSON.stringify(a.variables||{},null,2))}</textarea>`)+pane('path',`<label class="asset-field-label">PATH 条目 <small>相对 $PREFIX，每行一个，默认 bin</small></label><textarea name="pathEntries" class="asset-editor is-short" rows="6" placeholder="bin">${escapeHTML((a.pathEntries||[]).join('\n'))}</textarea>`);
  if(state.assetType==='sandboxes')return pane('env',`<label class="asset-field-label">环境变量 JSON</label><textarea name="environment" class="asset-editor is-short" rows="10" placeholder='{"NAME":"value"}'>${escapeHTML(JSON.stringify(a.environment||{},null,2))}</textarea><div class="json-help">叠加到沙箱基础环境并随资产持久化；不要在这里保存凭证。平台凭证、HOME/TMPDIR 和遥测变量拥有更高优先级。</div>`);
  return '';
}
function assetBasicFieldsHTML(a,{creating}){
  const kind=assetKindLabel();
  let html=`<label class="asset-field-label" for="assetFieldName">名称</label><input id="assetFieldName" name="name" class="asset-input" value="${escapeHTML(a.name||'')}" required placeholder="给${escapeHTML(kind)}起个名字"><label class="asset-field-label" for="assetFieldId">标识</label><input id="assetFieldId" name="id" class="asset-input is-mono" value="${escapeHTML(a.id||'')}" required ${creating?'placeholder="如 loopforge-core"':'readonly aria-readonly="true"'}><div class="asset-field-hint">${creating?'创建后不可修改，用于资产引用。':'标识用于资产引用，创建后不可修改。'}</div><label class="asset-field-label" for="assetFieldDesc">描述</label><input id="assetFieldDesc" name="description" class="asset-input" value="${escapeHTML(a.description||'')}" placeholder="一句话说明用途">`;
  if(state.assetType==='rules')html+=`<label class="asset-field-label" for="assetFieldPriority">优先级</label><input id="assetFieldPriority" name="priority" class="asset-input" type="number" value="${a.priority||100}"><div class="asset-field-hint">数值越大越先注入。</div>${ruleGlobalFields(a)}`;
  if(state.assetType==='sandboxes')html+=`<label class="asset-field-label">隔离方式</label><select name="driver" class="asset-select" aria-label="隔离方式"><option value="local" ${!['seatbelt','bwrap','docker'].includes(a.driver)?'selected':''}>local（本机直接执行）</option><option value="seatbelt" ${a.driver==='seatbelt'?'selected':''}>seatbelt（macOS 轻量隔离）</option><option value="bwrap" ${a.driver==='bwrap'?'selected':''}>bwrap（Linux 轻量隔离）</option><option value="docker" ${a.driver==='docker'?'selected':''}>docker（OCI 容器）</option></select><div data-sandbox-drivers="local" class="json-help">直接在本机 workspace 执行，并继承受控的宿主进程环境。</div><div data-sandbox-drivers="seatbelt bwrap"><label class="asset-field-label">执行环境</label><select name="environmentId" class="asset-select" aria-label="执行环境">${environmentOptions(a.environmentId)}</select><div class="json-help">选中后沙箱会执行该环境的 <code>install.sh</code>，把工具装进隔离的 <code>$PREFIX</code>，再用 PATH 切换。未选择时，若 workspace 存在 <code>agentflow-environment/</code> 也会自动启用。</div></div><div data-sandbox-drivers="docker"><label class="asset-field-label">Dockerfile</label><select name="dockerfileId" class="asset-select" aria-label="Dockerfile">${dockerfileOptions(a.dockerfileId)}</select><div class="json-help">沙箱只引用这条目录资产；初始化时用库里的内容构建临时镜像，结束后删除容器和镜像。</div><label class="asset-switch-row"><span><b>容器根文件系统只读</b><small>仅限制镜像自身的 /usr、/etc 等目录；/workspace 仍可写，/tmp 使用临时内存盘。部分工具可能不兼容，默认建议关闭。</small></span><input name="readOnlyRootFS" type="checkbox" class="asset-switch" role="switch" ${a.readOnlyRootFS?'checked':''}></label></div>`;
  return html;
}
function assetAsideHTML(a,{creating}){
  return `<section class="asset-side-card"><h4>基本信息</h4>${assetBasicFieldsHTML(a,{creating})}</section>${creating?'':`<section class="asset-side-card" data-asset-refs>${assetRefsCardHTML(a)}</section><section class="asset-side-card" data-asset-overview>${assetOverviewCardHTML(a)}</section>`}`;
}
function setAssetTab(key){
  const f=$('#assetForm');if(!f)return;
  const tabs=assetPaneTabs();if(!tabs.some(([k])=>k===key))key=tabs[0][0];
  state.assetTab=key;
  f.querySelectorAll('[data-asset-tab]').forEach(b=>{const on=b.dataset.assetTab===key;b.classList.toggle('active',on);b.setAttribute('aria-selected',String(on))});
  f.querySelectorAll('[data-asset-pane]').forEach(p=>p.hidden=p.dataset.assetPane!==key);
  if(key==='preview'){const host=f.querySelector('[data-asset-preview]');const text=f.elements.content?.value||'';if(host)host.innerHTML=text.trim()?renderMarkdown(text):'<p class="asset-md-empty">还没有内容</p>'}
}
function renderAssetForm(a){
  const f=$('#assetForm');if(!f||!a)return;
  const creating=state.selectedAsset==='__new__',readonly=!creating&&!catalogOwned(a.projectId);
  const tabs=assetPaneTabs(),kind=assetKindLabel();
  const summary=state.assetType==='skills'&&!creating?`<span data-skill-summary>${skillFileSummary(a)}</span>`:'';
  const save=readonly?'':`<div class="asset-panel-foot"><button type="submit" class="button primary">${creating?`${huiIcon('plus')}创建${escapeHTML(kind)}`:`${huiIcon('save')}保存修改`}</button></div>`;
  f.innerHTML=`<section class="asset-panel asset-main"><div class="asset-panel-head"><div class="asset-pane-tabs" role="tablist" aria-label="${escapeHTML(kind)}内容">${tabs.map(([k,label])=>`<button type="button" role="tab" data-asset-tab="${k}">${escapeHTML(label)}</button>`).join('')}</div><span class="asset-panel-hint">${summary||escapeHTML(assetPaneHint())}</span></div><div class="asset-stage">${assetMainPanesHTML(a,{creating,readonly})}</div>${save}</section><aside class="asset-aside">${assetAsideHTML(a,{creating})}</aside>`;
  f.querySelectorAll('[data-asset-tab]').forEach(b=>b.onclick=()=>setAssetTab(b.dataset.assetTab));
  setAssetTab(state.assetTab||tabs[0][0]);
  if(state.assetType==='rules')bindRuleGlobalPicker(a);
  if(state.assetType==='sandboxes'){f.elements.driver.onchange=()=>syncSandboxDriverFields(f.elements.driver);syncSandboxDriverFields(f.elements.driver)}
  f.querySelectorAll('select.asset-select').forEach(sel=>window.enhanceSelect?.(sel));
  if(readonly)f.querySelectorAll('input,textarea,select,.repo-picker-tag').forEach(el=>{if(el.type!=='file')el.disabled=true});
  if(state.assetType==='skills'&&!creating){skillUploadButtons(readonly||!!assetSkillSource(a));bindAssetSourceActions(a)}
}
function fmtBytes(n){n=Number(n)||0;if(n<1024)return n+' B';if(n<1024*1024)return (n/1024).toFixed(1)+' KB';return (n/1024/1024).toFixed(1)+' MB'}
function skillFileSummary(a){
  const refs=a.fileRefs||[];
  const total=refs.reduce((s,r)=>s+(r.size||0),0);
  return `<span class="chip">${refs.length} 个文件 · ${fmtBytes(total)}</span>`;
}
function skillFileIcon(path){
  const ext=path.toLowerCase().split('.').pop()||'';
  if(['md','markdown'].includes(ext))return 'MD';
  if(['png','jpg','jpeg','gif','webp','svg'].includes(ext))return 'IMG';
  if(['json','yaml','yml','toml','xml'].includes(ext))return '{}';
  if(['sh','bash','py','js','ts','go','rs','css','html','sql','lua','rb','java','c','cpp','h'].includes(ext))return '<>';
  if(['zip','tar','gz','tgz','bz2','xz'].includes(ext))return 'ZIP';
  return '📄';
}
function skillFilePreviewable(path){
  const ext=path.toLowerCase().split('.').pop()||'';
  return ['md','markdown','txt','log','json','yaml','yml','xml','sh','bash','py','js','ts','go','rs','css','html','sql','lua','rb','java','c','cpp','h','png','jpg','jpeg','gif','webp','svg'].includes(ext);
}
function skillFileLang(path){
  const ext=path.toLowerCase().split('.').pop()||'';
  const m={sh:'bash',bash:'bash',py:'python',js:'javascript',ts:'typescript',go:'go',rs:'rust',rb:'ruby',lua:'lua',java:'java',c:'c',cpp:'cpp',h:'c',yaml:'yaml',yml:'yaml',toml:'toml',xml:'xml',json:'json',sql:'sql',md:'markdown',markdown:'markdown',html:'html',css:'css',svg:'xml'};
  return m[ext]||'TEXT';
}
function selectedSkill(){return (state.data.skills||[]).find(x=>x.id===state.selectedAsset)||null}
function skillWriteBlocked(){const skill=selectedSkill();return !skill||catalogForeign(skill.projectId)||!!assetSkillSource(skill)}
function refreshSkillFileTree(){
  const skill=selectedSkill();
  const host=$('#skillFileTree')||$('#assetForm .skill-upload-block > .empty-inline');
  if(!host||!skill)return;
  host.outerHTML=skillFileRowsHTML(skill);
}
function skillFileRowsHTML(a){
  const refs=a.fileRefs||[];
  if(!refs.length)return '<p class="empty-inline">还没有文件。选择文件夹或 zip 后自动上传。</p>';
  const base=`/api/skills/${encodeURIComponent(a.id)}/files`;
  const readonly=catalogForeign(a.projectId)||!!assetSkillSource(a);
  if(!(state.skillTreeCollapsed instanceof Set))state.skillTreeCollapsed=new Set();
  // 构建 VS Code 风格文件树：目录可折叠，文件带预览/下载；本项目技能才显示删除
  const root={dirs:{},files:[]};
  refs.forEach(r=>{
    const segs=r.path.split('/');
    let node=root;
    for(let i=0;i<segs.length-1;i++){
      const seg=segs[i];
      if(!node.dirs[seg])node.dirs[seg]={dirs:{},files:[]};
      node=node.dirs[seg];
    }
    node.files.push(r);
  });
  const countFiles=node=>{
    let n=node.files.length;
    Object.values(node.dirs).forEach(d=>n+=countFiles(d));
    return n;
  };
  const html=[];
  (function render(node,depth,prefix){
    Object.keys(node.dirs).sort((x,y)=>x.localeCompare(y)).forEach(d=>{
      const child=node.dirs[d];
      const full=prefix+d;
      const collapsed=state.skillTreeCollapsed.has(full);
      html.push(`<div class="skill-tree-row" data-tree-toggle="${escapeHTML(full)}" style="padding-left:${depth*14+4}px"><span class="skill-tree-caret">${collapsed?'▸':'▾'}</span><span class="skill-file-icon">📁</span><code class="skill-tree-dirname">${escapeHTML(d)}/</code><span class="chip">${countFiles(child)}</span></div>`);
      if(!collapsed){
        html.push('<div class="skill-tree-children">');
        render(child,depth+1,full+'/');
        html.push('</div>');
      }
    });
    node.files.sort((x,y)=>x.path.localeCompare(y.path)).forEach(r=>{
      const name=r.path.split('/').pop();
      const url=`${base}/${r.path.split('/').map(encodeURIComponent).join('/')}`;
      const canPreview=skillFilePreviewable(r.path);
      html.push(`<div class="skill-file-row" style="padding-left:${depth*14+18}px" title="${escapeHTML(r.path)}"><span class="skill-file-icon">${skillFileIcon(r.path)}</span><code class="skill-file-path">${escapeHTML(name)}</code><span class="chip">${fmtBytes(r.size)}</span>${(r.mode&0o111)?'<span class="chip">可执行</span>':''}<button type="button" class="skill-file-preview" data-skill-file-preview="${escapeHTML(r.path)}" title="预览" ${canPreview?'':'disabled'}>预览</button><a href="${url}" download title="下载">下载</a>${readonly?'':`<button type="button" class="skill-file-del" data-skill-file-del="${escapeHTML(r.path)}" title="删除">×</button>`}</div>`);
    });
  })(root,0,'');
  return `<div id="skillFileTree" class="skill-file-list skill-file-tree">${html.join('')}</div>`;
}
function setSkillUploadStatus(html){
  state.skillUploadStatus=html||'';
  const el=$('#skillUploadStatus');
  if(el)el.innerHTML=state.skillUploadStatus;
}
function skillUploadButtons(disabled){
  ['skillFolderBtn','skillZipBtn','skillMdBtn','skillFolderPick','skillZipPick','skillMdPick'].forEach(id=>{
    const el=$('#'+id); if(el)el.disabled=!!disabled;
  });
}
let skillUploadLastStart=0;
async function uploadSkillFiles(input){
  const now=Date.now();
  if(now-skillUploadLastStart<800)return; // change + 轮询双触发去重
  skillUploadLastStart=now;
  const files=[...(input.files||[])];
  if(input)input.value='';
  if(!files.length)return toast('没有读取到任何文件，请重新选择');
  const id=state.selectedAsset;
  if(state.selectedAsset==='__new__')return toast('先保存技能，再上传文件');
  if(skillWriteBlocked())return toast('共享资产只读');
  const totalBytes=files.reduce((s,f)=>s+f.size,0);
  const totalLabel=fmtBytes(totalBytes);
  skillUploadButtons(true);

  // 取消控制：读文件阶段用标志位，fetch 阶段用 AbortController
  let cancelled=false;
  const ctrl=new AbortController();
  const cancelBtnHTML='<button type="button" class="skill-upload-cancel">取消</button>';
  const renderRow=(label,pct)=>`<div class="skill-upload-progress"><span>${label}</span><div class="skill-upload-bar ${pct<0?'indeterminate':''}"><i style="width:${Math.max(0,pct<0?30:pct)}%"></i></div><b class="skill-upload-pct">${pct<0?'···':`${pct}%`}</b>${cancelBtnHTML}</div>`;
  const bindCancel=()=>{const b=$('#skillUploadStatus .skill-upload-cancel');if(b)b.onclick=()=>{cancelled=true;ctrl.abort();toast('已取消上传')}};
  const setStatusRow=(label,pct)=>{setSkillUploadStatus(renderRow(label,pct));bindCancel()};

  const buildFd=parts=>{
    const fd=new FormData();
    // 相对路径走并行 paths 字段：multipart 的 filename 会被服务端只保留 basename。
    parts.forEach(p=>{fd.append('paths',p.path);fd.append('files',p.blob,p.path.split('/').pop())});
    return fd;
  };
  const finishOk=data=>{
    const i=(state.data.skills||[]).findIndex(x=>x.id===id);
    if(i>=0)state.data.skills[i]=data;else state.data.skills.push(data);
    setSkillUploadStatus(`<div class="skill-upload-ok">✓ 已替换为 ${files.length} 个文件 · ${totalLabel}（${new Date().toLocaleTimeString()}）</div>`);
    renderAssets();renderProperties();
    toast(`已替换为 ${files.length} 个文件`,'ok');
  };
  const showFail=err=>{
    setSkillUploadStatus(`<div class="skill-upload-err">✕ ${escapeHTML(err.message)}。现有文件未改动。</div>`);
    toast(err.message,'err',8000);
  };

  // ===== 方式 A：先逐文件读入内存（8 并发，单文件 3s 超时） =====
  const parts=new Array(files.length);
  let done=0, readBytes=0, readErr=null;
  const queue=files.map((_,idx)=>idx);
  setStatusRow(`正在读取 0/${files.length} 个文件`,-1);
  const readWithTimeout=async(f,ms)=>{
    let timer;
    const timeoutP=new Promise((_,rej)=>{timer=setTimeout(()=>rej(new Error('__read_timeout__')),ms)});
    const buf=await Promise.race([f.arrayBuffer(),timeoutP]).finally(()=>clearTimeout(timer));
    return buf;
  };
  const workers=Array.from({length:Math.min(8,files.length)},async()=>{
    while(queue.length&&!cancelled&&!readErr){
      const idx=queue.shift();
      const f=files[idx];
      const rel=f.webkitRelativePath||f.name;
      let buf;
      try{
                buf=await readWithTimeout(f,3000);
      }catch(err){
        if(err.message==='__read_timeout__'){
          readErr=new Error(`读取文件超时：${rel}。浏览器读取文件内容被系统/安全软件阻断，将尝试流式直传。`);
        }else{
          readErr=new Error(`读取文件失败：${rel}（${err.message}）`);
        }
        return;
      }
      parts[idx]={path:rel,blob:new Blob([buf])};
      done++;readBytes+=buf.byteLength;

      setStatusRow(`正在读取 ${done}/${files.length} 个文件 · 已读取 ${fmtBytes(readBytes)}`,-1);
    }
  });
  try{await Promise.all(workers)}
  catch(err){if(!readErr)readErr=new Error(`读取过程异常：${err.message}`)}
  if(cancelled){
    setSkillUploadStatus('<div class="skill-upload-err">已取消上传，现有文件未改动。</div>');
    skillUploadButtons(false);
    return;
  }

  let fetchP=null;
  if(!readErr){
    const fd=buildFd(parts);
    fetchP=fetch(`/api/skills/${encodeURIComponent(id)}/files`,{method:'POST',body:fd,signal:ctrl.signal});
  }else{
    // ===== 方式 B（降级）：流式直传原始 File，由浏览器网络进程读取 =====
    setStatusRow(`读取受阻，正在尝试流式直传 ${files.length} 个文件…`,-1);
    const fd=buildFd(files.map(f=>({path:f.webkitRelativePath||f.name,blob:f})));
    fetchP=fetch(`/api/skills/${encodeURIComponent(id)}/files`,{method:'POST',body:fd,signal:ctrl.signal});
  }

  // ===== 上传等待：模拟进度 + 看门狗 15s 必给结论 =====
  let fakePct=8;
  const ticker=setInterval(()=>{
    fakePct=Math.min(88,fakePct+Math.random()*10+4);
    setStatusRow(`上传中 ${Math.round(fakePct)}% · ${files.length} 个文件 · ${totalLabel}`,Math.round(fakePct));
  },220);
  const watchdog=setTimeout(()=>{
    ctrl.abort();
    setSkillUploadStatus('<div class="skill-upload-err">✕ 上传超过 15 秒未完成，已中止。浏览器读取/发送文件被环境阻断（常见于安全管控软件）。现有文件未改动。</div>');
  },15000);
  try{
    const res=await fetchP;
    clearTimeout(watchdog);clearInterval(ticker);

    const data=await res.json().catch(()=>({}));
    if(!res.ok)throw new Error(data.error||`HTTP ${res.status}`);
    finishOk(data);
  }catch(err){
    clearTimeout(watchdog);clearInterval(ticker);
    if(cancelled||err.name==='AbortError'){
      if(!cancelled)setSkillUploadStatus('<div class="skill-upload-err">✕ 上传超过 15 秒未完成，已中止。浏览器读取/发送文件被环境阻断（常见于安全管控软件）。现有文件未改动。</div>');
      else setSkillUploadStatus('<div class="skill-upload-err">已取消上传，现有文件未改动。</div>');
    }else{
      showFail(err);
    }
  }finally{
    skillUploadButtons(false);
  }
}
let skillMdUploadLastStart=0;
async function uploadSkillMd(input){
  const now=Date.now();
  if(now-skillMdUploadLastStart<800)return;
  skillMdUploadLastStart=now;
  const files=[...(input.files||[])];
  if(input)input.value='';
  if(!files.length)return toast('没有读取到任何文件，请重新选择');
  const file=files[0];
  const lower=(file.name||'').toLowerCase();
  if(!lower.endsWith('.md')&&!lower.endsWith('.markdown'))return toast('请选择 SKILL.md 或 .md 文件');
  if(file.size>1024*1024)return toast('SKILL.md 超过 1MB 限制','err');
  const id=state.selectedAsset;
  if(!id||id==='__new__')return toast('先保存技能，再上传文件');
  if(skillWriteBlocked())return toast('共享资产只读');
  const skill=(state.data.skills||[]).find(x=>x.id===id)||{};
  const extra=(skill.fileRefs||[]).length;
  const ok=await afConfirm({
    title:'覆盖性更改',
    message:extra
      ?`这是覆盖性更改：上传后该技能将只保留 SKILL.md，现有的 ${extra} 个文件（含 scripts、文档和其他资源）全部删除。此操作不可撤销。`
      :'这是覆盖性更改：上传后该技能将只保留这一份 SKILL.md。此操作不可撤销。',
    confirmText:'确认覆盖',
    cancelText:'取消',
    danger:true
  });
  if(!ok)return;
  skillUploadButtons(true);
  setSkillUploadStatus('<div class="skill-upload-progress"><span>正在覆盖为仅 SKILL.md…</span><div class="skill-upload-bar indeterminate"><i></i></div><b class="skill-upload-pct">···</b></div>');
  try{
    const fd=new FormData();
    fd.append('paths','SKILL.md');
    fd.append('files',file,'SKILL.md');
    const res=await fetch(`/api/skills/${encodeURIComponent(id)}/files`,{method:'POST',body:fd});
    const data=await res.json().catch(()=>({}));
    if(!res.ok)throw new Error(data.error||`HTTP ${res.status}`);
    const i=(state.data.skills||[]).findIndex(x=>x.id===id);
    if(i>=0)state.data.skills[i]=data;else state.data.skills.push(data);
    setSkillUploadStatus(`<div class="skill-upload-ok">✓ 已覆盖为仅 SKILL.md · ${fmtBytes(file.size)}（${new Date().toLocaleTimeString()}）</div>`);
    renderAssets();renderProperties();
    toast('已覆盖为仅 SKILL.md','ok');
  }catch(err){
    setSkillUploadStatus(`<div class="skill-upload-err">✕ ${escapeHTML(err.message)}。现有文件未改动。</div>`);
    toast(err.message,'err',8000);
  }finally{
    skillUploadButtons(false);
  }
}
async function deleteSkillFile(path){
  if(!path)return;
  if(!await afConfirm({title:'删除文件',message:`确定删除 ${path} 吗？此操作不可恢复。`,confirmText:'确认删除',danger:true}))return;
  const id=state.selectedAsset;
  try{
    const res=await fetch(`/api/skills/${encodeURIComponent(id)}/files/${path.split('/').map(encodeURIComponent).join('/')}`,{method:'DELETE'});
    const data=await res.json().catch(()=>({}));
    if(!res.ok)throw new Error(data.error||`HTTP ${res.status}`);
    const i=(state.data.skills||[]).findIndex(x=>x.id===id);
    if(i>=0)state.data.skills[i]=data;
    renderAssets();renderProperties();
    toast('文件已删除','ok');
  }catch(err){toast(err.message,'err')}
}
async function openSkillFilePreview(skill, path){
  if(!path)return toast('无法预览：缺少文件路径','err');
  if(!skill)return toast('无法预览：未选中技能','err');
  const ref=(skill.fileRefs||[]).find(r=>r.path===path)||{};
  const url=`/api/skills/${encodeURIComponent(skill.id)}/files/${path.split('/').map(encodeURIComponent).join('/')}`;
  const host=$('#artifactPreview');
  if(!host)return toast('预览窗口不可用','err');
  host.innerHTML='<div class="artifact-loading">正在加载文件…</div>';
  $('#artifactModal').classList.add('open');
  try{
    const res=await fetch(url);
    if(!res.ok)throw new Error(`HTTP ${res.status}`);
    const blob=await res.blob();
    const ext=(path.split('.').pop()||'').toLowerCase();
    const isImg=['png','jpg','jpeg','gif','webp','svg'].includes(ext);
    const isMd=['md','markdown'].includes(ext);
    let viewMode=isMd?'preview':'source';
    const objectURL=URL.createObjectURL(blob);
    const text=isImg?'':await blob.text();
    const lang=skillFileLang(path);
    function renderModal(){
      const body=isImg
        ? `<div class="artifact-body"><img src="${objectURL}" alt="${escapeHTML(path)}" style="max-width:100%"></div>`
        : viewMode==='preview'
          ? `<div class="artifact-body"><div class="markdown-body">${renderMarkdown(text)}</div></div>`
          : `<div class="artifact-body"><pre class="artifact-content">${highlightCode(text, lang)}</pre></div>`;
      host.innerHTML=`<div class="artifact-preview-head"><div class="artifact-preview-head-left"><span class="artifact-preview-icon">${skillFileIcon(path)}</span><div class="artifact-preview-titles"><h2>${escapeHTML(path.split('/').pop())}</h2><div class="artifact-preview-sub"><span>${escapeHTML(path)}</span></div></div></div><div class="artifact-preview-head-actions"><a class="button ghost small" href="${url}" download>下载</a></div></div><div class="artifact-preview-toolbar"><div class="artifact-toolbar-left">${isMd?`<div class="artifact-view-mode"><button type="button" class="artifact-mode-btn ${viewMode==='preview'?'active':''}" data-mode="preview">渲染预览</button><button type="button" class="artifact-mode-btn ${viewMode==='source'?'active':''}" data-mode="source">源码</button></div>`:''}<div class="artifact-evidence-badges"><span>SHA-256: <code>${escapeHTML((ref.sha256||'—').slice(0,16))}${(ref.sha256||'').length>16?'…':''}</code></span><span>${fmtBytes(ref.size||blob.size||0)}</span></div></div></div>${body}`;
      host.querySelectorAll('.artifact-mode-btn').forEach(btn=>btn.onclick=()=>{viewMode=btn.dataset.mode;renderModal()});
    }
    renderModal();
  }catch(err){
    host.innerHTML=`<div class="artifact-loading">预览失败：${escapeHTML(err.message)}</div>`;
    toast(err.message,'err');
  }
}
async function saveAsset(e){e.preventDefault();const f=e.currentTarget,items=state.data[state.assetType]||[],projectScoped=isProjectSharedAsset(state.assetType),identity=resolveAssetSaveIdentity(items,state.selectedAsset,f.elements.id.value.trim());if(identity.error)return toast(identity.error);const existing=identity.existing;if(existing&&projectScoped&&catalogForeign(existing.projectId))return toast('共享资产只读');let value={id:identity.id,name:f.elements.name.value.trim(),description:f.elements.description.value,projectId:existing?(existing.projectId||''):(state.projectFilter||''),shared:projectScoped&&!!existing?.shared};if(!value.id||!value.name)return toast('标识和名称不能为空');try{
  if(state.assetType==='rules'){const global=!!f.elements.global?.checked;Object.assign(value,{priority:Number(f.elements.priority.value),content:f.elements.content.value,global,workflowIds:global?selectedRuleWorkflowIds():[]})}
  if(state.assetType==='skills'){/* 文件经独立上传接口承载；此处只提交元数据。服务端会强制保留 Content/Files/FileRefs。 */}
  if(state.assetType==='dockerfiles')Object.assign(value,{content:f.elements.content.value,files:JSON.parse(f.elements.files.value||'{}')});
  if(state.assetType==='environments')Object.assign(value,{variables:JSON.parse(f.elements.variables.value||'{}'),pathEntries:lines(f.elements.pathEntries.value),installScript:f.elements.installScript.value});
  if(state.assetType==='sandboxes'){const driver=f.elements.driver.value,sandbox={driver,environment:JSON.parse(f.elements.environment.value||'{}')};if(driver==='docker')Object.assign(sandbox,{dockerfileId:f.elements.dockerfileId.value,readOnlyRootFS:f.elements.readOnlyRootFS.checked});if(driver==='seatbelt'||driver==='bwrap')Object.assign(sandbox,{environmentId:f.elements.environmentId.value});Object.assign(value,sandbox)}
  const endpoints={rules:'rules',skills:'skills',dockerfiles:'dockerfiles',environments:'environments',sandboxes:'sandboxes'};await api(`/api/${endpoints[state.assetType]}`,{method:'PUT',body:JSON.stringify(value)});const i=items.findIndex(x=>x.id===value.id);if(state.assetType==='skills'&&existing)value={...existing,...value,fileRefs:existing.fileRefs||[],content:existing.content||'',files:existing.files||{}};if(i>=0)items[i]=value;else items.push(value);state.selectedAsset=value.id;state.assetDirty=false;
  if(identity.created){history.replaceState(history.state,'',assetViewHash(state.assetType,value.id));state.assetDetailKey='';renderAssets()}
  else{renderAssetDetailHead(value);refreshAssetAside(value)}
  renderWorkflowMeta();renderProperties();toast(identity.created?'资产已创建':'资产修改已保存');
  }catch(err){toast(err.message)}
}
