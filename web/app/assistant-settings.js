// AI 助手设置：生成中消息的发送方式、主动派发、帮我批准审查模型（跟随当前对话 / 自定义 CLI / 自定义 API），以及每轮自定义规则。只对当前用户生效。
const ASSISTANT_RULES_MAX=4000;
const ASSISTANT_REVIEW_MODEL_MAX=160;
const ASSISTANT_REVIEW_CUSTOM='__custom__';
const ASSISTANT_REVIEW_MODES=[
 {id:'follow',label:'跟随当前对话 CLI 和模型'},
 {id:'cli',label:'自定义 CLI 和模型'},
 {id:'api',label:'自定义 API 和模型'},
];
const ASSISTANT_REVIEW_PROTOCOLS=[
 {id:'chat',label:'OpenAI 兼容（Chat Completions）',basePlaceholder:'例如 https://gateway.example.com/v1（留空用 OpenAI 官方）',modelPlaceholder:'例如 gpt-4.1-mini、deepseek-chat'},
 {id:'responses',label:'OpenAI（Responses）',basePlaceholder:'例如 https://gateway.example.com/v1（留空用 OpenAI 官方）',modelPlaceholder:'例如 gpt-5.4-mini、gpt-4.1-mini'},
 {id:'messages',label:'Anthropic（Messages）',basePlaceholder:'例如 https://gateway.example.com（留空用 Anthropic 官方）',modelPlaceholder:'例如 claude-haiku-4-5'},
 {id:'gemini',label:'Google Gemini',basePlaceholder:'留空用 Gemini 官方地址，或填写代理 Base URL',modelPlaceholder:'例如 gemini-2.5-flash'},
];
const ASSISTANT_ENGINE_ICONS={
 codebuddy:'/images/credentials/codebuddy.svg',
 codex:'/images/credentials/codex.svg',
 'claude-code':'/images/credentials/claude.svg',
 opencode:'/images/credentials/opencode.svg',
 cursor:'/images/credentials/cursor.svg',
 agy:'/images/credentials/gemini.svg',
 kimi:'/images/credentials/kimi.svg',
 pi:'/images/credentials/pi.svg',
 dsh:'/images/credentials/deepseek.svg',
 zcode:'/images/credentials/glm.svg',
};
const ASSISTANT_FAMILY_ICONS={
 claude:'/images/credentials/claude.svg',
 gpt:'/images/credentials/codex.svg',
 glm:'/images/credentials/glm.svg',
 gemini:'/images/credentials/gemini.svg',
 kimi:'/images/credentials/kimi.svg',
 deepseek:'/images/credentials/deepseek.svg',
 minimax:'/images/credentials/minimax.svg',
 hunyuan:'/images/credentials/hunyuan.svg',
 mimo:'/images/credentials/mimo.svg',
};
const ASSISTANT_FAMILY_MARKS={claude:'',gpt:'',glm:'智',gemini:'G',kimi:'K',deepseek:'D',minimax:'M',hunyuan:'混',mimo:'Mi',echo:'E',other:'AI'};
const ASSISTANT_CHEVRON_SVG='<svg class="asst-picker-chevron" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg>';
const ASSISTANT_CHECK_SVG='<svg class="asst-picker-check" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>';
const ASSISTANT_SEARCH_SVG='<svg class="asst-picker-search-icon" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>';
const assistantPrefsUI={saved:null,loading:false,saving:false,error:'',selectApis:[],docCleanup:null,probeToken:0,probeModels:null};

function assistantPrefsSignedIn(){return !!state.currentUser&&!state.currentUser.isGuest}
function assistantPrefsForm(){return document.querySelector('#assistantPrefsForm')}
function assistantReviewModeOf(p){
 const mode=String(p?.approvalReviewMode||'').trim();
 if(mode==='follow'||mode==='cli'||mode==='api')return mode;
 if(String(p?.approvalReviewModel||'').trim())return 'cli';
 return 'follow';
}
function assistantReviewProtocolMeta(id){
 return ASSISTANT_REVIEW_PROTOCOLS.find(p=>p.id===id)||ASSISTANT_REVIEW_PROTOCOLS[0];
}
function assistantReviewFamilyOf(id){
 if(typeof AgentFlowAssistantModels!=='undefined'&&AgentFlowAssistantModels?.familyOf){
  return AgentFlowAssistantModels.familyOf(id);
 }
 const v=String(id||'').toLowerCase();
 if(v.startsWith('claude'))return 'claude';
 if(v.startsWith('gpt')||v.includes('codex'))return 'gpt';
 if(v.startsWith('glm'))return 'glm';
 if(v.startsWith('gemini'))return 'gemini';
 if(v.startsWith('kimi'))return 'kimi';
 if(v.startsWith('deepseek'))return 'deepseek';
 if(v.startsWith('minimax'))return 'minimax';
 if(v.startsWith('hy')||v.includes('hunyuan'))return 'hunyuan';
 if(v.startsWith('mimo')||v.includes('xiaomi'))return 'mimo';
 return 'other';
}
function assistantReviewEngineIconHTML(engineId,name){
 const src=(typeof AgentFlowAssistantModels!=='undefined'&&AgentFlowAssistantModels?.cliIcon?.(engineId))||ASSISTANT_ENGINE_ICONS[engineId];
 if(src)return `<span class="asst-cli-icon"><img src="${src}" alt=""></span>`;
 const mark=String(name||engineId||'C').slice(0,1).toUpperCase();
 return `<span class="asst-cli-icon is-letter">${escapeHTML(mark)}</span>`;
}
function assistantReviewModelIconHTML(family,rawId){
 const fam=family||assistantReviewFamilyOf(rawId);
 const src=ASSISTANT_FAMILY_ICONS[fam];
 if(src)return `<span class="ai-model-icon" data-family="${escapeHTML(fam)}"><img src="${src}" alt=""></span>`;
 const mark=ASSISTANT_FAMILY_MARKS[fam]||String(rawId||'AI').slice(0,1).toUpperCase();
 return `<span class="ai-model-icon" data-family="${escapeHTML(fam)}">${escapeHTML(mark)}</span>`;
}
function assistantReviewCatalogData(){
 if(typeof AgentFlowAssistantModels==='undefined'||!AgentFlowAssistantModels?.catalog)return {engines:[],allEngines:new Map(),modelsByEngine:new Map()};
 const cat=AgentFlowAssistantModels.catalog()||{};
 const allEngines=new Map();
 const engines=[];
 const hasEngineList=Array.isArray(cat.engines)&&cat.engines.length>0;
 for(const e of cat.engines||[]){
  if(!e?.id)continue;
  const entry={id:e.id,name:e.name||e.id,configured:e.configured===true};
  allEngines.set(entry.id,entry);
  if(entry.configured)engines.push(entry);
 }
 const modelsByEngine=new Map();
 const seen=new Set();
 for(const item of cat.models||[]){
  const id=String(item?.id||'').trim();
  if(!id||id==='default'||seen.has(id))continue;
  const engine=item.engine||'codebuddy';
  if(hasEngineList&&!allEngines.get(engine)?.configured)continue;
  seen.add(id);
  const rawId=engine==='codebuddy'?id:(id.startsWith(engine+':')?id.slice(engine.length+1):id);
  const name=String(item.name||'').trim()||rawId;
  const family=String(item.family||'').trim()||assistantReviewFamilyOf(rawId);
  const credits=String(item.credits||'').trim();
  if(!modelsByEngine.has(engine))modelsByEngine.set(engine,[]);
  modelsByEngine.get(engine).push({id,rawId,engine,name,family,credits});
 }
 return {engines,allEngines,modelsByEngine};
}
function assistantReviewSplitModel(modelId,catalogData){
 const val=String(modelId||'').trim();
 const firstCli=catalogData.engines[0]?.id||'';
 if(!val)return {cli:firstCli,rawModel:''};
 const colon=val.indexOf(':');
 if(colon>0){
  const prefix=val.slice(0,colon).trim();
  if(prefix&&(catalogData.allEngines.has(prefix)||/^[a-z][a-z0-9-]*$/.test(prefix))){
   return {cli:prefix,rawModel:val.slice(colon+1).trim()};
  }
 }
 return {cli:'codebuddy',rawModel:val};
}
function assistantReviewComposeModel(cli,rawModel){
 const cleanCli=String(cli||'').trim();
 let cleanModel=String(rawModel||'').trim();
 if(!cleanModel)return '';
 if(cleanCli&&cleanModel.startsWith(cleanCli+':'))cleanModel=cleanModel.slice(cleanCli.length+1).trim();
 if(!cleanModel)return '';
 if(!cleanCli||cleanCli==='codebuddy')return cleanModel;
 return `${cleanCli}:${cleanModel}`;
}
function assistantReviewSavedApi(saved){
 const apiCfg=saved?.approvalReviewApi||{};
 return {
  protocol:ASSISTANT_REVIEW_PROTOCOLS.some(p=>p.id===apiCfg.protocol)?apiCfg.protocol:'chat',
  baseUrl:String(apiCfg.baseUrl||'').trim(),
  model:String(apiCfg.model||'').trim(),
  apiKeyConfigured:!!apiCfg.apiKeyConfigured,
 };
}
function assistantPrefsDraft(form){
 const mode=assistantReviewModeOf({approvalReviewMode:form.approvalReviewMode?.value});
 const apiProtocol=String(form.approvalReviewApiProtocol?.value||'chat').trim()||'chat';
 const apiBaseUrl=String(form.approvalReviewApiBaseUrl?.value||'').trim();
 const apiModel=String(form.approvalReviewApiModel?.value||'').trim();
 const apiKey=String(form.approvalReviewApiKey?.value||'').trim();
 const apiKeyClear=form.approvalReviewApiKeyClear?.value==='1';
 const draft={
  proactiveRoles:form.proactiveRoles.checked,
  proactiveSops:form.proactiveSops.checked,
  steerOnSend:form.messageDuringGeneration?.value==='steer',
  approvalReviewMode:mode,
  approvalReviewModel:String(form.approvalReviewModel?.value||'').trim(),
  instructions:form.instructions.value.trim(),
 };
 if(mode==='api'||apiKey||apiKeyClear){
  draft.approvalReviewApi={protocol:apiProtocol,baseUrl:apiBaseUrl,model:apiModel};
  if(apiKey)draft.approvalReviewApi.apiKey=apiKey;
  if(apiKeyClear&&!apiKey)draft.approvalReviewApi.apiKeyClear=true;
 }
 return draft;
}
function assistantPrefsDirty(form){
 const saved=assistantPrefsUI.saved;if(!saved||!form)return false;
 const draft=assistantPrefsDraft(form);
 const savedMode=assistantReviewModeOf(saved);
 if(draft.proactiveRoles!==!!saved.proactiveRoles||
    draft.proactiveSops!==!!saved.proactiveSops||
    draft.steerOnSend!==!!saved.steerOnSend||
    draft.instructions!==(saved.instructions||'')||
    draft.approvalReviewMode!==savedMode)return true;
 if(draft.approvalReviewMode==='cli'){
  return draft.approvalReviewModel!==String(saved.approvalReviewModel||'').trim();
 }
 if(draft.approvalReviewMode==='api'){
  const prev=assistantReviewSavedApi(saved);
  const next=draft.approvalReviewApi||{};
  return (next.protocol||'chat')!==prev.protocol||
   (next.baseUrl||'')!==prev.baseUrl||
   (next.model||'')!==prev.model||
   !!next.apiKey||!!next.apiKeyClear;
 }
 return false;
}
function assistantPrefsSwitchRow(name,title,help,on){
 return `<label class="asst-switch-row"><span><b>${title}</b><small>${help}</small></span><input type="checkbox" role="switch" name="${name}" ${on?'checked':''}></label>`;
}
function assistantReviewCliLabel(cli,catalogData,unconfigured){
 const name=catalogData.allEngines.get(cli)?.name||cli||'选择 CLI';
 return unconfigured?`${name}（未配置凭证）`:name;
}
function assistantReviewCliMenuHTML(activeCli,catalogData){
 const engines=catalogData.engines;
 if(!engines.length){
  return '<p class="asst-picker-empty">暂无已配置的 CLI</p>';
 }
 return engines.map(e=>{
  const count=(catalogData.modelsByEngine.get(e.id)||[]).length;
  const meta=count?`${count} 个可用模型`:'可手动输入模型';
  const selected=e.id===activeCli;
  return `<button type="button" class="asst-picker-option${selected?' is-selected':''}" role="menuitemradio" aria-checked="${selected}" data-pick-cli="${escapeHTML(e.id)}">
   ${assistantReviewEngineIconHTML(e.id,e.name)}
   <span class="asst-picker-copy"><strong>${escapeHTML(e.name)}</strong><small>${escapeHTML(meta)}</small></span>
   ${selected?ASSISTANT_CHECK_SVG:''}
  </button>`;
 }).join('');
}
function assistantReviewModelMenuHTML(cli,selectedCanonical,catalogData){
 const models=catalogData.modelsByEngine.get(cli)||[];
 const matched=selectedCanonical&&models.some(m=>m.id===selectedCanonical);
 const isCustom=!!selectedCanonical&&!matched||!models.length;
 const items=models.map(m=>{
  const selected=m.id===selectedCanonical;
  const sub=[m.rawId!==m.name?m.rawId:'',m.credits?(m.credits.endsWith('credits')?m.credits:m.credits+' credits'):''].filter(Boolean).join(' · ');
  const search=escapeHTML(`${m.name} ${m.rawId} ${m.family}`.toLowerCase());
  return `<button type="button" class="asst-picker-option${selected?' is-selected':''}" role="menuitemradio" aria-checked="${selected}" data-pick-model="${escapeHTML(m.id)}" data-search="${search}">
   ${assistantReviewModelIconHTML(m.family,m.rawId)}
   <span class="asst-picker-copy"><strong>${escapeHTML(m.name)}</strong>${sub?`<small>${escapeHTML(sub)}</small>`:''}</span>
   ${selected?ASSISTANT_CHECK_SVG:''}
  </button>`;
 }).join('');
 return `<div class="asst-picker-search-wrap">${ASSISTANT_SEARCH_SVG}<input type="search" class="asst-picker-search" id="assistantPrefsReviewModelSearch" placeholder="搜索模型" aria-label="搜索模型" autocomplete="off" spellcheck="false"></div>
  <div class="asst-picker-list" id="assistantPrefsReviewModelList">${items||'<p class="asst-picker-empty">该 CLI 暂无预设模型列表，可手动输入模型 ID</p>'}<p class="asst-picker-empty" id="assistantPrefsReviewModelNoMatch" hidden>没有匹配的模型</p></div>
  <div class="asst-picker-foot"><button type="button" class="asst-picker-option asst-picker-custom-btn${isCustom?' is-selected':''}" role="menuitemradio" aria-checked="${isCustom}" data-pick-model="${ASSISTANT_REVIEW_CUSTOM}"><span class="asst-picker-copy"><strong>手动输入模型 ID</strong><small>适用于网关未公开目录的自定义模型</small></span>${isCustom?ASSISTANT_CHECK_SVG:''}</button></div>`;
}
function assistantReviewModelTriggerHTML(cli,selectedCanonical,rawModel,catalogData){
 const models=catalogData.modelsByEngine.get(cli)||[];
 const found=models.find(m=>m.id===selectedCanonical);
 if(found){
  return `${assistantReviewModelIconHTML(found.family,found.rawId)}<span class="asst-picker-trigger-label">${escapeHTML(found.name)}</span>${ASSISTANT_CHEVRON_SVG}`;
 }
 if(rawModel){
  return `${assistantReviewModelIconHTML('',rawModel)}<span class="asst-picker-trigger-label">${escapeHTML(rawModel)}</span>${ASSISTANT_CHEVRON_SVG}`;
 }
 return `${assistantReviewModelIconHTML('other','AI')}<span class="asst-picker-trigger-label is-placeholder">选择或手动输入模型</span>${ASSISTANT_CHEVRON_SVG}`;
}
function assistantReviewCliBoxHTML(mode,reviewModel,catalogData){
 const hidden=mode==='cli'?'':' hidden';
 const {cli,rawModel}=assistantReviewSplitModel(reviewModel,catalogData);
 const engines=catalogData.engines;
 const hasUnconfiguredSaved=!!reviewModel&&!!cli&&!engines.some(e=>e.id===cli);
 const head=`<div class="asst-review-sub-head"><span class="asst-review-meta">实时同步「个人配置」中已配置的 CLI 与模型</span><div class="asst-review-sub-actions"><button type="button" class="asst-link-btn" id="assistantPrefsRefreshCli">刷新列表</button><button type="button" class="asst-link-btn" id="assistantPrefsManageCli">管理 CLI 凭证</button></div></div>`;
 if(!engines.length&&!hasUnconfiguredSaved){
  return `<div id="assistantPrefsReviewCliBox" class="asst-review-sub"${hidden}>
   ${head}
   <div class="asst-review-empty-cli"><p class="asst-review-note">尚未在「个人配置」中配置任何 CLI 凭证，可先前往配置，或切换为「自定义 API 和模型」。</p><button type="button" class="button small" id="assistantPrefsGoSettings">前往个人配置</button></div>
  </div>`;
 }
 const activeCli=cli||(engines[0]?.id||'');
 const models=catalogData.modelsByEngine.get(activeCli)||[];
 const canonical=reviewModel?assistantReviewComposeModel(activeCli,rawModel):(models[0]?.id||'');
 const effectiveRaw=canonical?assistantReviewSplitModel(canonical,catalogData).rawModel:rawModel;
 const matched=canonical&&models.some(m=>m.id===canonical);
 const showCustomInput=!matched&&(!!effectiveRaw||!models.length);
 const cliLabel=assistantReviewCliLabel(activeCli,catalogData,hasUnconfiguredSaved);
 const countMeta=hasUnconfiguredSaved?'需重新选择':(models.length?`${models.length} 个模型`:'手动输入');
 return `<div id="assistantPrefsReviewCliBox" class="asst-review-sub"${hidden}>
  ${head}
  <div class="asst-model-grid">
   <div class="asst-field">
    <span>CLI</span>
    <div class="asst-picker-anchor">
     <button type="button" id="assistantPrefsReviewCliBtn" class="asst-picker-trigger" aria-haspopup="menu" aria-expanded="false" data-active-cli="${escapeHTML(activeCli)}">
      ${assistantReviewEngineIconHTML(activeCli,cliLabel)}
      <span class="asst-picker-trigger-label">${escapeHTML(cliLabel)}</span>
      <span class="asst-picker-trigger-meta">${escapeHTML(countMeta)}</span>
      ${ASSISTANT_CHEVRON_SVG}
     </button>
     <div id="assistantPrefsReviewCliMenu" class="asst-picker-menu" role="menu" aria-label="选择已配置的 CLI" hidden>${assistantReviewCliMenuHTML(activeCli,catalogData)}</div>
    </div>
   </div>
   <div class="asst-field">
    <span>模型</span>
    <div class="asst-picker-anchor">
     <button type="button" id="assistantPrefsReviewModelBtn" class="asst-picker-trigger" aria-haspopup="menu" aria-expanded="false" data-active-model="${escapeHTML(canonical)}">
      ${assistantReviewModelTriggerHTML(activeCli,canonical,effectiveRaw,catalogData)}
     </button>
     <div id="assistantPrefsReviewModelMenu" class="asst-picker-menu asst-model-picker-menu" role="menu" aria-label="选择审查模型" hidden>${assistantReviewModelMenuHTML(activeCli,canonical,catalogData)}</div>
    </div>
   </div>
  </div>
  <label id="assistantPrefsReviewCustomWrap" class="asst-field asst-custom-wrap"${showCustomInput?'':' hidden'}><span>自定义模型 ID</span><input type="text" id="assistantPrefsReviewModelCustom" class="input asst-model-input" name="approvalReviewModelCustom" maxlength="${ASSISTANT_REVIEW_MODEL_MAX}" value="${escapeHTML(effectiveRaw)}" placeholder="例如 claude-haiku-4-5、gpt-5.4-mini" autocomplete="off" spellcheck="false"></label>
 </div>`;
}
function assistantReviewApiProbeListHTML(models,selectedModel){
 const items=(models||[]).map(m=>{
  const id=String(m.id||'').trim();
  const name=String(m.name||'').trim();
  const selected=id===selectedModel;
  const search=escapeHTML(`${id} ${name}`.toLowerCase());
  return `<button type="button" class="asst-probe-option${selected?' is-selected':''}" data-api-model="${escapeHTML(id)}" data-search="${search}">
   ${assistantReviewModelIconHTML('',id)}
   <span class="asst-probe-copy"><code>${escapeHTML(id)}</code>${name&&name!==id?`<small>${escapeHTML(name)}</small>`:''}</span>
   ${selected?ASSISTANT_CHECK_SVG:''}
  </button>`;
 }).join('');
 return `<div class="asst-probe-head">
   <div class="asst-picker-search-wrap asst-probe-search">${ASSISTANT_SEARCH_SVG}<input type="search" id="assistantPrefsApiProbeSearch" class="asst-picker-search" placeholder="搜索可用模型" aria-label="搜索可用模型" autocomplete="off" spellcheck="false"></div>
   <span class="asst-probe-count" id="assistantPrefsApiProbeCount">共 ${(models||[]).length} 个可用模型</span>
   <button type="button" class="asst-link-btn" data-api-probe-close>收起</button>
  </div>
  <div class="asst-probe-list" role="listbox" aria-label="获取到的可用模型">${items}<p class="asst-picker-empty" id="assistantPrefsApiProbeNoMatch" hidden>没有匹配的模型</p></div>`;
}
function assistantReviewApiBoxHTML(mode,saved){
 const hidden=mode==='api'?'':' hidden';
 const cfg=assistantReviewSavedApi(saved);
 const meta=assistantReviewProtocolMeta(cfg.protocol);
 const protocolOptions=ASSISTANT_REVIEW_PROTOCOLS.map(p=>`<option value="${escapeHTML(p.id)}" ${cfg.protocol===p.id?'selected':''}>${escapeHTML(p.label)}</option>`).join('');
 const keyAction=cfg.apiKeyConfigured?`<button type="button" class="asst-key-clear" id="assistantPrefsApiKeyClearBtn" data-clear-state="0">清除已存 Key</button>`:'';
 const keyPlaceholder=cfg.apiKeyConfigured?'已配置，留空保持不变':'填写 API Key（仅用于帮我批准审查）';
 return `<div id="assistantPrefsReviewApiBox" class="asst-review-sub"${hidden}>
  <div class="asst-api-grid">
   <label class="asst-field"><span>接口协议</span><select id="assistantPrefsReviewApiProtocol" class="input asst-select" name="approvalReviewApiProtocol" aria-label="接口协议">${protocolOptions}</select></label>
   <label class="asst-field"><span>Base URL</span><input type="url" id="assistantPrefsReviewApiBaseUrl" class="input asst-model-input" name="approvalReviewApiBaseUrl" maxlength="512" value="${escapeHTML(cfg.baseUrl)}" placeholder="${escapeHTML(meta.basePlaceholder)}" autocomplete="off" spellcheck="false"></label>
   <div class="asst-field"><div class="asst-field-head"><label for="assistantPrefsReviewApiKey">API Key</label>${keyAction}</div><input type="password" id="assistantPrefsReviewApiKey" class="input asst-model-input" name="approvalReviewApiKey" maxlength="512" value="" placeholder="${escapeHTML(keyPlaceholder)}" autocomplete="new-password" spellcheck="false"><input type="hidden" name="approvalReviewApiKeyClear" id="assistantPrefsReviewApiKeyClear" value="0"></div>
   <div class="asst-field"><div class="asst-field-head"><label for="assistantPrefsReviewApiModel">模型 ID</label><button type="button" class="asst-link-btn" id="assistantPrefsApiProbeBtn">获取可用模型</button></div><input type="text" id="assistantPrefsReviewApiModel" class="input asst-model-input" name="approvalReviewApiModel" maxlength="${ASSISTANT_REVIEW_MODEL_MAX}" value="${escapeHTML(cfg.model)}" placeholder="${escapeHTML(meta.modelPlaceholder)}" autocomplete="off" spellcheck="false"></div>
  </div>
  <div id="assistantPrefsApiProbePanel" class="asst-probe-panel" hidden></div>
 </div>`;
}
function assistantPrefsFormHTML(p){
 const rules=p.instructions||'';
 const mode=assistantReviewModeOf(p);
 const reviewModel=String(p.approvalReviewModel||'').trim();
 const catalogData=assistantReviewCatalogData();
 const modeOptions=ASSISTANT_REVIEW_MODES.map(m=>`<option value="${m.id}" ${mode===m.id?'selected':''}>${escapeHTML(m.label)}</option>`).join('');
 return `<form id="assistantPrefsForm" class="asst-form" novalidate><fieldset ${assistantPrefsUI.saving?'disabled':''}>
  <section class="asst-panel"><header class="asst-panel-head"><h2>消息发送</h2></header>
   <div class="asst-rows"><label class="asst-switch-row" for="assistantPrefsMessageDuringGeneration"><span><b>生成中发送消息</b><small>排队会等当前回复结束；立即发送会打断当前回复，保留已输出内容并继续处理新消息。</small></span>
    <select id="assistantPrefsMessageDuringGeneration" class="input" name="messageDuringGeneration"><option value="queue" ${p.steerOnSend?'':'selected'}>排队</option><option value="steer" ${p.steerOnSend?'selected':''}>立即发送</option></select>
   </label></div></section>
  <section class="asst-panel"><header class="asst-panel-head"><h2>主动派发</h2><p>打开后 AI 助手会更主动地把事情交给角色或 SOP。派角色不需要确认；走 SOP 在「请求批准」下先征求同意，「帮我批准」下由 AI 审查直接裁决，「完全权限」下直接启动。</p></header>
   <div class="asst-rows">
    ${assistantPrefsSwitchRow('proactiveRoles','主动派角色','整件事是某个角色的本职（如独立评审、整套测试），或改了高风险代码需要复核时，直接交给这个角色。',p.proactiveRoles)}
    ${assistantPrefsSwitchRow('proactiveSops','主动走 SOP','遇到要落到仓库的新需求时，先交给项目里用途对得上的 SOP，而不是自己直接改代码。',p.proactiveSops)}
   </div></section>
  <section class="asst-panel"><header class="asst-panel-head"><h2>帮我批准审查模型</h2><p>「帮我批准」模式由 AI 独立审查平台及外部操作并直接放行或拒绝，无需人工审批。默认沿用当前对话的 CLI 与模型（最低思考强度）；也可指定已配置的 CLI 模型或独立轻量 API 节省 Token。</p></header>
   <div class="asst-review-block">
    <label class="asst-field"><span>快捷选择</span><select id="assistantPrefsReviewMode" class="input asst-select" name="approvalReviewMode" aria-label="帮我批准审查方式">${modeOptions}</select></label>
    <input type="hidden" id="assistantPrefsReviewModel" name="approvalReviewModel" value="${escapeHTML(reviewModel)}">
    ${assistantReviewCliBoxHTML(mode,reviewModel,catalogData)}
    ${assistantReviewApiBoxHTML(mode,p)}
   </div></section>
  <section class="asst-panel"><header class="asst-panel-head"><h2>自定义规则</h2><p>每轮对话都会带上这些规则，适合写你的习惯和偏好；不会越过审批模式和平台规则。</p></header>
   <label class="asst-rules-label" for="assistantPrefsRules">规则</label>
   <textarea id="assistantPrefsRules" class="input asst-rules" name="instructions" rows="8" maxlength="${ASSISTANT_RULES_MAX}" placeholder="例如：回复先给结论再给细节；改代码前先说明要改哪些文件；不要自动发起 MR。">${escapeHTML(rules)}</textarea>
   <div class="asst-rules-foot"><span id="assistantPrefsCount" class="asst-count">${rules.length} / ${ASSISTANT_RULES_MAX}</span></div></section>
  <div class="asst-actions"><button type="submit" class="button primary" id="assistantPrefsSave" disabled>${assistantPrefsUI.saving?'保存中…':'保存'}</button></div>
 </fieldset></form>`;
}
function destroyAssistantSelects(){
 for(const api of assistantPrefsUI.selectApis||[]){try{api?.destroy?.()}catch{}}
 assistantPrefsUI.selectApis=[];
 if(typeof assistantPrefsUI.docCleanup==='function'){
  try{assistantPrefsUI.docCleanup()}catch{}
  assistantPrefsUI.docCleanup=null;
 }
}
function assistantReviewApiOrigin(protocol,rawBaseUrl){
 let base=String(rawBaseUrl||'').trim();
 if(!base){
  if(protocol==='messages')base='https://api.anthropic.com';
  else if(protocol==='gemini')base='https://generativelanguage.googleapis.com';
  else if(protocol==='chat'||protocol==='responses')base='https://api.openai.com/v1';
  else return '';
 }
 try{
  const u=new URL(base);
  if(u.protocol!=='http:'&&u.protocol!=='https:')return '';
  const port=u.port||(u.protocol==='https:'?'443':'80');
  return `${u.protocol}//${u.hostname.toLowerCase()}:${port}`;
 }catch{return ''}
}
function assistantReviewApiKeyReuseState(saved,protocol,baseUrl,apiKeyClear){
 const prev=assistantReviewSavedApi(saved);
 if(!prev.apiKeyConfigured||apiKeyClear)return {reusable:false,originChanged:false};
 const oldOrigin=assistantReviewApiOrigin(prev.protocol,prev.baseUrl);
 const nextOrigin=assistantReviewApiOrigin(protocol,baseUrl);
 if(oldOrigin&&oldOrigin===nextOrigin)return {reusable:true,originChanged:false};
 return {reusable:false,originChanged:true};
}
function validateAssistantPrefsDraft(draft,saved){
 if(draft.approvalReviewMode==='cli'&&!draft.approvalReviewModel){
  return {message:'请选择或填写自定义审查模型 ID',focus:'#assistantPrefsReviewModelCustom, #assistantPrefsReviewModelBtn'};
 }
 if(draft.approvalReviewMode==='api'){
  const cfg=draft.approvalReviewApi||{};
  if(!cfg.model)return {message:'请填写自定义审查模型 ID',focus:'#assistantPrefsReviewApiModel'};
  if(cfg.baseUrl){
   try{const u=new URL(cfg.baseUrl);if(u.protocol!=='http:'&&u.protocol!=='https:')throw new Error('bad')}catch{return {message:'Base URL 必须以 http:// 或 https:// 开头',focus:'#assistantPrefsReviewApiBaseUrl'}}
  }
  const reuse=assistantReviewApiKeyReuseState(saved,cfg.protocol||'chat',cfg.baseUrl||'',!!cfg.apiKeyClear);
  if(!cfg.apiKey&&!reuse.reusable){
   return {message:reuse.originChanged?'API 地址已跨源变更，请重新填写自定义审查 API Key':'请填写自定义审查 API Key',focus:'#assistantPrefsReviewApiKey'};
  }
 }
 return null;
}
function captureAssistantPrefsRenderModel(form){
 const saved=assistantPrefsUI.saved;
 if(!form||!saved)return saved;
 const apiCfg=assistantReviewSavedApi(saved);
 return {
  ...saved,
  proactiveRoles:form.proactiveRoles.checked,
  proactiveSops:form.proactiveSops.checked,
  steerOnSend:form.messageDuringGeneration?.value==='steer',
  approvalReviewMode:assistantReviewModeOf({approvalReviewMode:form.approvalReviewMode?.value}),
  approvalReviewModel:String(form.approvalReviewModel?.value||'').trim(),
  approvalReviewApi:{
   protocol:String(form.approvalReviewApiProtocol?.value||apiCfg.protocol).trim()||'chat',
   baseUrl:String(form.approvalReviewApiBaseUrl?.value||'').trim(),
   model:String(form.approvalReviewApiModel?.value||'').trim(),
   apiKeyConfigured:apiCfg.apiKeyConfigured,
  },
  instructions:form.instructions.value,
 };
}
function renderAssistantSettings(draftOverride){
 const host=document.querySelector('#assistantContent');if(!host)return;
 destroyAssistantSelects();
 if(!assistantPrefsSignedIn()){host.innerHTML='<section class="asst-panel asst-empty"><p>登录后可以设置 AI 助手。</p></section>';return}
 if(assistantPrefsUI.error&&!assistantPrefsUI.saved){host.innerHTML=`<section class="asst-panel asst-empty"><p>${escapeHTML(assistantPrefsUI.error)}</p><button type="button" class="button" data-assistant-prefs-retry>重试</button></section>`;host.querySelector('[data-assistant-prefs-retry]').onclick=loadAssistantSettings;return}
 if(!assistantPrefsUI.saved){host.innerHTML='<section class="asst-panel asst-empty"><p>正在读取设置…</p></section>';return}
 const viewModel=draftOverride||assistantPrefsUI.saved;
 host.innerHTML=assistantPrefsFormHTML(viewModel);
 const form=assistantPrefsForm(),save=form.querySelector('#assistantPrefsSave'),count=form.querySelector('#assistantPrefsCount');
 const catalogData=assistantReviewCatalogData();
 const modeSel=form.querySelector('#assistantPrefsReviewMode');
 const hiddenModel=form.querySelector('#assistantPrefsReviewModel');
 const cliBox=form.querySelector('#assistantPrefsReviewCliBox');
 const apiBox=form.querySelector('#assistantPrefsReviewApiBox');
 const cliBtn=form.querySelector('#assistantPrefsReviewCliBtn');
 const cliMenu=form.querySelector('#assistantPrefsReviewCliMenu');
 const modelBtn=form.querySelector('#assistantPrefsReviewModelBtn');
 const modelMenu=form.querySelector('#assistantPrefsReviewModelMenu');
 const customWrap=form.querySelector('#assistantPrefsReviewCustomWrap');
 const customInput=form.querySelector('#assistantPrefsReviewModelCustom');
 const refreshCliBtn=form.querySelector('#assistantPrefsRefreshCli');
 const manageCliBtn=form.querySelector('#assistantPrefsManageCli');
 const goSettingsBtn=form.querySelector('#assistantPrefsGoSettings');
 const apiProtoSel=form.querySelector('#assistantPrefsReviewApiProtocol');
 const apiModelInput=form.querySelector('#assistantPrefsReviewApiModel');
 const apiBaseInput=form.querySelector('#assistantPrefsReviewApiBaseUrl');
 const apiKeyInput=form.querySelector('#assistantPrefsReviewApiKey');
 const apiKeyClearInput=form.querySelector('#assistantPrefsReviewApiKeyClear');
 const apiKeyClearBtn=form.querySelector('#assistantPrefsApiKeyClearBtn');
 const apiProbeBtn=form.querySelector('#assistantPrefsApiProbeBtn');
 const apiProbePanel=form.querySelector('#assistantPrefsApiProbePanel');

 const msgSendSel=form.querySelector('#assistantPrefsMessageDuringGeneration');
 if(typeof window!=='undefined'&&window.enhanceSelect){
  for(const sel of [msgSendSel,modeSel,apiProtoSel]){
   if(sel){
    const api=window.enhanceSelect(sel);
    if(api)assistantPrefsUI.selectApis.push(api);
   }
  }
 }

 const closePickers=()=>{
  if(cliMenu)cliMenu.hidden=true;
  if(cliBtn)cliBtn.setAttribute('aria-expanded','false');
  if(modelMenu)modelMenu.hidden=true;
  if(modelBtn)modelBtn.setAttribute('aria-expanded','false');
 };
 const onDocPointerDown=event=>{
  if(!event.target?.closest?.('.asst-picker-anchor'))closePickers();
 };
 const onDocKeyDown=event=>{
  if(event.key==='Escape')closePickers();
 };
 if(typeof document!=='undefined'&&document.addEventListener){
  document.addEventListener('pointerdown',onDocPointerDown);
  document.addEventListener('keydown',onDocKeyDown);
  assistantPrefsUI.docCleanup=()=>{
   document.removeEventListener('pointerdown',onDocPointerDown);
   document.removeEventListener('keydown',onDocKeyDown);
  };
 }

 const refresh=()=>{
  count.textContent=`${form.instructions.value.length} / ${ASSISTANT_RULES_MAX}`;
  save.disabled=assistantPrefsUI.saving||!assistantPrefsDirty(form);
 };
 const activeCliId=()=>cliBtn?.dataset.activeCli||'';

 const paintCliAndModel=(cli,canonical,customMode)=>{
  if(!cliBtn||!modelBtn||!hiddenModel)return;
  const models=catalogData.modelsByEngine.get(cli)||[];
  const unconfigured=!!cli&&!catalogData.engines.some(e=>e.id===cli);
  const cliLabel=assistantReviewCliLabel(cli,catalogData,unconfigured);
  const countMeta=unconfigured?'需重新选择':(models.length?`${models.length} 个模型`:'手动输入');
  cliBtn.dataset.activeCli=cli;
  cliBtn.innerHTML=`${assistantReviewEngineIconHTML(cli,cliLabel)}<span class="asst-picker-trigger-label">${escapeHTML(cliLabel)}</span><span class="asst-picker-trigger-meta">${escapeHTML(countMeta)}</span>${ASSISTANT_CHEVRON_SVG}`;
  if(cliMenu)cliMenu.innerHTML=assistantReviewCliMenuHTML(cli,catalogData);

  const rawModel=canonical?assistantReviewSplitModel(canonical,catalogData).rawModel:(customInput?.value?.trim()||'');
  const effectiveCanonical=customMode?assistantReviewComposeModel(cli,rawModel):canonical;
  hiddenModel.value=effectiveCanonical;
  modelBtn.dataset.activeModel=effectiveCanonical;
  modelBtn.innerHTML=assistantReviewModelTriggerHTML(cli,effectiveCanonical,rawModel,catalogData);
  if(modelMenu)modelMenu.innerHTML=assistantReviewModelMenuHTML(cli,effectiveCanonical,catalogData);
  const matched=effectiveCanonical&&models.some(m=>m.id===effectiveCanonical);
  if(customWrap)customWrap.hidden=!(customMode||(!matched&&!models.length));
  if(customInput&&!customMode)customInput.value=rawModel;
 };

 const filterModelMenu=()=>{
  if(!modelMenu)return;
  const q=String(modelMenu.querySelector('#assistantPrefsReviewModelSearch')?.value||'').trim().toLowerCase();
  const options=[...modelMenu.querySelectorAll('#assistantPrefsReviewModelList [data-pick-model]')];
  let visible=0;
  for(const btn of options){
   const ok=!q||String(btn.dataset.search||'').includes(q);
   btn.hidden=!ok;
   if(ok)visible++;
  }
  const empty=modelMenu.querySelector('#assistantPrefsReviewModelNoMatch');
  if(empty)empty.hidden=!options.length||visible>0;
 };

 if(cliBtn&&cliMenu){
  cliBtn.addEventListener('click',event=>{
   event.stopPropagation();
   const willOpen=cliMenu.hidden;
   closePickers();
   cliMenu.hidden=!willOpen;
   cliBtn.setAttribute('aria-expanded',String(willOpen));
  });
  cliMenu.addEventListener('click',event=>{
   const item=event.target.closest?.('[data-pick-cli]');
   if(!item)return;
   const nextCli=item.dataset.pickCli||'';
   const models=catalogData.modelsByEngine.get(nextCli)||[];
   if(customInput)customInput.value='';
   paintCliAndModel(nextCli,models[0]?.id||'',!models.length);
   closePickers();
   if(!models.length&&customInput)customInput.focus();
   refresh();
  });
 }
 if(modelBtn&&modelMenu){
  modelBtn.addEventListener('click',event=>{
   event.stopPropagation();
   const willOpen=modelMenu.hidden;
   closePickers();
   modelMenu.hidden=!willOpen;
   modelBtn.setAttribute('aria-expanded',String(willOpen));
   if(willOpen){
    const search=modelMenu.querySelector('#assistantPrefsReviewModelSearch');
    if(search){search.value='';filterModelMenu();search.focus({preventScroll:true})}
   }
  });
  modelMenu.addEventListener('input',event=>{
   if(event.target?.id==='assistantPrefsReviewModelSearch')filterModelMenu();
  });
  modelMenu.addEventListener('keydown',event=>{
   if(event.target?.id==='assistantPrefsReviewModelSearch'&&event.key==='Enter'){
    event.preventDefault();
    const first=modelMenu.querySelector('#assistantPrefsReviewModelList [data-pick-model]:not([hidden])');
    first?.click();
   }
  });
  modelMenu.addEventListener('click',event=>{
   const item=event.target.closest?.('[data-pick-model]');
   if(!item)return;
   const picked=item.dataset.pickModel||'';
   const cli=activeCliId();
   if(picked===ASSISTANT_REVIEW_CUSTOM){
    paintCliAndModel(cli,hiddenModel?.value||'',true);
    closePickers();
    if(customInput)customInput.focus();
   }else{
    paintCliAndModel(cli,picked,false);
    closePickers();
   }
   refresh();
  });
 }
 if(customInput){
  customInput.addEventListener('input',()=>{
   const cli=activeCliId();
   const composed=assistantReviewComposeModel(cli,customInput.value);
   if(hiddenModel)hiddenModel.value=composed;
   if(modelBtn){
    modelBtn.dataset.activeModel=composed;
    modelBtn.innerHTML=assistantReviewModelTriggerHTML(cli,composed,customInput.value.trim(),catalogData);
   }
   refresh();
  });
 }

 const jumpToSettings=()=>{if(typeof switchView==='function')switchView('settings')};
 if(manageCliBtn)manageCliBtn.addEventListener('click',jumpToSettings);
 if(goSettingsBtn)goSettingsBtn.addEventListener('click',jumpToSettings);
 if(refreshCliBtn){
  refreshCliBtn.addEventListener('click',async()=>{
   refreshCliBtn.disabled=true;
   refreshCliBtn.textContent='刷新中…';
   const snapshot=captureAssistantPrefsRenderModel(form);
   try{
    if(typeof AgentFlowAssistantModels!=='undefined'&&AgentFlowAssistantModels?.load){
     await AgentFlowAssistantModels.load({refresh:true});
    }
    toast('已刷新 CLI 与模型列表');
   }catch{}
   renderAssistantSettings(snapshot);
  });
 }

 const syncModeVisibility=()=>{
  const mode=modeSel?.value||'follow';
  if(cliBox)cliBox.hidden=mode!=='cli';
  if(apiBox)apiBox.hidden=mode!=='api';
  closePickers();
  if(mode==='cli'&&cliBtn&&hiddenModel&&!hiddenModel.value){
   const cli=activeCliId()||(catalogData.engines[0]?.id||'');
   if(cli){
    const models=catalogData.modelsByEngine.get(cli)||[];
    paintCliAndModel(cli,models[0]?.id||'',!models.length);
   }
  }
 };
 const syncApiKeyPlaceholder=()=>{
  if(!apiKeyInput)return;
  const clear=apiKeyClearInput?.value==='1';
  if(clear){
   apiKeyInput.placeholder='保存后将清除已存 Key，或输入新 Key 替换';
   return;
  }
  const reuse=assistantReviewApiKeyReuseState(assistantPrefsUI.saved,apiProtoSel?.value||'chat',apiBaseInput?.value||'',false);
  if(reuse.reusable)apiKeyInput.placeholder='已配置，留空保持不变';
  else if(reuse.originChanged)apiKeyInput.placeholder='API 地址已变更，请重新填写 API Key';
  else apiKeyInput.placeholder='填写 API Key（仅用于帮我批准审查）';
 };
 const syncProtocolPlaceholders=()=>{
  const meta=assistantReviewProtocolMeta(apiProtoSel?.value||'chat');
  if(apiModelInput)apiModelInput.placeholder=meta.modelPlaceholder;
  if(apiBaseInput)apiBaseInput.placeholder=meta.basePlaceholder;
  syncApiKeyPlaceholder();
 };
 const clearApiProbe=()=>{
  assistantPrefsUI.probeToken=(assistantPrefsUI.probeToken||0)+1;
  assistantPrefsUI.probeModels=null;
  if(apiProbePanel){apiProbePanel.hidden=true;apiProbePanel.innerHTML=''}
 };
 const paintApiProbeSelection=()=>{
  if(!apiProbePanel||!assistantPrefsUI.probeModels)return;
  const current=String(apiModelInput?.value||'').trim();
  for(const btn of apiProbePanel.querySelectorAll('[data-api-model]')){
   const active=btn.dataset.apiModel===current;
   btn.classList.toggle('is-selected',active);
   const existingCheck=btn.querySelector('.asst-picker-check');
   if(active&&!existingCheck)btn.insertAdjacentHTML('beforeend',ASSISTANT_CHECK_SVG);
   else if(!active&&existingCheck)existingCheck.remove();
  }
 };
 const probeApiModels=async()=>{
  if(!apiProbePanel||!apiProbeBtn)return;
  const protocol=String(apiProtoSel?.value||'chat').trim()||'chat';
  const baseUrl=String(apiBaseInput?.value||'').trim();
  const apiKey=String(apiKeyInput?.value||'').trim();
  const apiKeyClear=apiKeyClearInput?.value==='1';
  if(baseUrl){
   try{const u=new URL(baseUrl);if(u.protocol!=='http:'&&u.protocol!=='https:')throw new Error('bad')}
   catch{toast('Base URL 必须以 http:// 或 https:// 开头');apiBaseInput?.focus();return}
  }
  const reuse=assistantReviewApiKeyReuseState(assistantPrefsUI.saved,protocol,baseUrl,apiKeyClear);
  if(!apiKey&&!reuse.reusable){
   toast(reuse.originChanged?'API 地址已跨源变更，请重新填写自定义审查 API Key':'请先填写自定义审查 API Key');
   apiKeyInput?.focus();
   return;
  }
  const token=(assistantPrefsUI.probeToken||0)+1;
  assistantPrefsUI.probeToken=token;
  assistantPrefsUI.probeModels=null;
  apiProbeBtn.disabled=true;
  apiProbeBtn.textContent='获取中…';
  apiProbePanel.hidden=false;
  apiProbePanel.innerHTML='<p class="asst-probe-status" role="status">正在获取可用模型…</p>';
  let result;
  try{
   result=await api('/api/assistant/approval-review/probe',{
    method:'POST',
    body:JSON.stringify({protocol,baseUrl,apiKey,apiKeyClear}),
    silent:true,
   });
  }catch(err){
   result={error:err.message||'获取模型列表失败'};
  }finally{
   apiProbeBtn.disabled=false;
   apiProbeBtn.textContent='获取可用模型';
  }
  if(assistantPrefsUI.probeToken!==token||!apiProbePanel.isConnected)return;
  if(result?.error||!result?.models?.length){
   apiProbePanel.innerHTML=`<div class="asst-probe-error"><p role="alert">${escapeHTML(result?.error||'服务没有返回可用模型，可以手动填写模型 ID')}</p><div class="asst-probe-error-actions"><button type="button" class="asst-link-btn" data-api-probe-retry>重试</button><button type="button" class="asst-link-btn" data-api-probe-close>关闭</button></div></div>`;
   return;
  }
  assistantPrefsUI.probeModels=result.models;
  apiProbePanel.innerHTML=assistantReviewApiProbeListHTML(result.models,String(apiModelInput?.value||'').trim());
  apiProbePanel.querySelector('#assistantPrefsApiProbeSearch')?.focus({preventScroll:true});
 };

 if(apiProbeBtn)apiProbeBtn.addEventListener('click',probeApiModels);
 if(apiProbePanel){
  apiProbePanel.addEventListener('click',event=>{
   if(event.target.closest?.('[data-api-probe-close]')){clearApiProbe();return}
   if(event.target.closest?.('[data-api-probe-retry]')){probeApiModels();return}
   const item=event.target.closest?.('[data-api-model]');
   if(!item||!apiModelInput)return;
   apiModelInput.value=item.dataset.apiModel||'';
   paintApiProbeSelection();
   refresh();
  });
  apiProbePanel.addEventListener('input',event=>{
   if(event.target?.id!=='assistantPrefsApiProbeSearch')return;
   const q=String(event.target.value||'').trim().toLowerCase();
   const items=[...apiProbePanel.querySelectorAll('[data-api-model]')];
   let visible=0;
   for(const btn of items){
    const ok=!q||String(btn.dataset.search||'').includes(q);
    btn.hidden=!ok;
    if(ok)visible++;
   }
   const empty=apiProbePanel.querySelector('#assistantPrefsApiProbeNoMatch');
   if(empty)empty.hidden=visible>0;
   const countEl=apiProbePanel.querySelector('#assistantPrefsApiProbeCount');
   if(countEl)countEl.textContent=q?`匹配 ${visible} / ${items.length} 个模型`:`共 ${items.length} 个可用模型`;
  });
 }
 if(apiModelInput){
  apiModelInput.addEventListener('input',()=>{paintApiProbeSelection();refresh()});
 }

 if(modeSel){
  modeSel.addEventListener('change',()=>{syncModeVisibility();refresh()});
 }
 if(apiProtoSel){
  apiProtoSel.addEventListener('change',()=>{syncProtocolPlaceholders();clearApiProbe();refresh()});
 }
 if(apiBaseInput){
  apiBaseInput.addEventListener('input',syncApiKeyPlaceholder);
  apiBaseInput.addEventListener('change',()=>{clearApiProbe();syncApiKeyPlaceholder()});
 }
 if(apiKeyClearBtn&&apiKeyClearInput&&apiKeyInput){
  apiKeyClearBtn.addEventListener('click',()=>{
   const next=apiKeyClearInput.value==='1'?'0':'1';
   apiKeyClearInput.value=next;
   apiKeyClearBtn.dataset.clearState=next;
   apiKeyClearBtn.textContent=next==='1'?'撤销清除':'清除已存 Key';
   syncApiKeyPlaceholder();
   refresh();
  });
 }
 if(apiKeyInput&&apiKeyClearInput&&apiKeyClearBtn){
  apiKeyInput.addEventListener('input',()=>{
   if(apiKeyInput.value.trim()&&apiKeyClearInput.value==='1'){
    apiKeyClearInput.value='0';
    apiKeyClearBtn.dataset.clearState='0';
    apiKeyClearBtn.textContent='清除已存 Key';
   }
  });
 }

 form.addEventListener('input',refresh);
 form.addEventListener('change',refresh);
 refresh();
 form.onsubmit=async event=>{
  event.preventDefault();
  if(assistantPrefsUI.saving||!assistantPrefsDirty(form))return;
  const draft=assistantPrefsDraft(form);
  const invalid=validateAssistantPrefsDraft(draft,assistantPrefsUI.saved);
  if(invalid){
   toast(invalid.message);
   form.querySelector(invalid.focus)?.focus?.();
   return;
  }
  assistantPrefsUI.saving=true;save.disabled=true;save.textContent='保存中…';
  try{
   assistantPrefsUI.saved=await api('/api/assistant/preferences',{method:'PUT',body:JSON.stringify(draft)});
   document.querySelector('#chatNextFrame')?.contentWindow?.postMessage({type:'agentflow-assistant-preferences-changed'},location.origin);
   toast('AI 助手设置已保存');
   assistantPrefsUI.saving=false;
   renderAssistantSettings();
   return;
  }catch(err){toast(err.message||'保存失败，请重试')}
  finally{
   if(assistantPrefsUI.saving){
    assistantPrefsUI.saving=false;save.textContent='保存';refresh();
   }
  }
 };
}
async function loadAssistantSettings(){
 if(!assistantPrefsSignedIn()){renderAssistantSettings();return}
 if(assistantPrefsUI.loading)return;
 const form=assistantPrefsForm();
 if(form&&assistantPrefsDirty(form))return;
 assistantPrefsUI.loading=true;assistantPrefsUI.error='';
 if(!assistantPrefsUI.saved)renderAssistantSettings();
 try{
  const loadModels=typeof AgentFlowAssistantModels!=='undefined'&&AgentFlowAssistantModels?.load?AgentFlowAssistantModels.load().catch(()=>null):Promise.resolve(null);
  const [saved]=await Promise.all([api('/api/assistant/preferences',{silent:true}),loadModels]);
  assistantPrefsUI.saved=saved;
 }
 catch(err){assistantPrefsUI.error=err.message||'读取设置失败'}
 finally{assistantPrefsUI.loading=false;renderAssistantSettings()}
}
