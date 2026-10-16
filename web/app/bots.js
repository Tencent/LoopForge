function loadBotModels(){return window.AgentFlowAssistantModels?.load?.()||Promise.resolve()}
function botModelPickerHTML(bot){return `<div class="schedule-model-field bot-model-field" id="botModelPicker"><input type="hidden" name="model" value="${escapeHTML(bot.model||'default')}"><button type="button" class="ai-model-toggle" data-model-toggle aria-label="AI 模型（CodeBuddy）" aria-haspopup="listbox" aria-expanded="false"><span class="ai-model-icon" data-family="glm">智</span><span data-model-label>选择模型</span><span class="ai-model-caret" aria-hidden="true"></span></button><div class="ai-model-menu" hidden role="listbox" aria-label="CodeBuddy 模型"></div></div>`}
function closeBotModelPicker(){const picker=document.querySelector('#botModelPicker');if(!picker)return;picker.querySelector('.ai-model-menu').hidden=true;picker.querySelector('[data-model-toggle]').setAttribute('aria-expanded','false')}
function bindBotModelPicker(host){
 const picker=host.querySelector('#botModelPicker'),models=window.AgentFlowAssistantModels;
 if(!picker||!models)return;
 const toggle=picker.querySelector('[data-model-toggle]'),menu=picker.querySelector('.ai-model-menu'),value=picker.querySelector('[name="model"]');
 models.paint(toggle,value.value);
 toggle.onclick=event=>{
  event.stopPropagation();if(toggle.disabled||toggle.closest('fieldset')?.disabled)return;
  const open=menu.hidden;closeBotModelPicker();if(!open)return;
  models.renderList(menu,value.value,id=>{value.value=id;models.paint(toggle,id);captureBotDraft(host);closeBotModelPicker()});
  const rect=toggle.getBoundingClientRect(),width=Math.min(Math.max(rect.width,320),window.innerWidth-24),height=Math.min(320,window.innerHeight-24),below=window.innerHeight-rect.bottom-12,above=rect.top-12;
  menu.style.width=width+'px';menu.style.left=Math.max(12,Math.min(rect.left,window.innerWidth-width-12))+'px';
  const down=below>=Math.min(height,above);const available=down?below:above;
  menu.style.maxHeight=Math.min(height,available)+'px';menu.style.top=(down?rect.bottom+6:Math.max(12,rect.top-Math.min(height,available)-6))+'px';
  menu.hidden=false;toggle.setAttribute('aria-expanded','true');
 };
 host.querySelector('fieldset').onscroll=closeBotModelPicker;
 if(!bindBotModelPicker.docBound){bindBotModelPicker.docBound=true;document.addEventListener('click',event=>{if(!event.target.closest('#botModelPicker'))closeBotModelPicker()});document.addEventListener('keydown',event=>{if(event.key==='Escape')closeBotModelPicker()});window.addEventListener('resize',closeBotModelPicker)}
}
// Project-scoped enterprise chat notifications.
const botUI={projectId:'',items:[],deliveries:[],selected:null,sessions:[],query:'',loading:false,error:'',busy:false,revision:0,interaction:null,interactionExpanded:false};
function botTypeLabel(type){return type==='group'?'群聊':'单聊'}
function botHealthLabel(health){return ({ready:'消息权限可用',unchecked:'待验证',error:'连接异常',awaiting_authorization:'需要重新授权'})[health]||'待验证'}
function botStatusLabel(status){return ({pending:'等待发送',sending:'发送中',sent:'已发送',failed:'发送失败',awaiting_authorization:'等待授权',target_unavailable:'会话不可用',cancelled:'已取消',expired:'已过期'})[status]||status}
function filteredBotSessions(items,query){const text=String(query||'').trim().toLocaleLowerCase();return(items||[]).filter(chat=>!text||String(chat.name||'').toLocaleLowerCase().includes(text))}
function canCreateBot(){if(!state.authRequired||!state.currentUser||isPlatformAdmin())return true;const user=Number(state.currentUser.id),project=currentProject();return Number(project?.ownerId)===user||(project?.members||[]).some(m=>Number(m.userId)===user&&m.role!=='viewer')}
function canManageBot(bot){
 if(!state.authRequired||!state.currentUser||isPlatformAdmin())return true;
 const user=Number(state.currentUser.id),project=currentProject();
 return Number(bot?.ownerId)===user||Number(project?.ownerId)===user||(project?.members||[]).some(m=>Number(m.userId)===user&&m.role==='maintainer');
}
async function loadBots(){
 const projectId=state.projectFilter||'',revision=++botUI.revision;
 botUI.projectId=projectId;botUI.loading=true;botUI.error='';botUI.selected=null;botUI.interaction=null;botUI.sessions=[];botUI.items=[];botUI.deliveries=[];renderBots();
 if(!projectId){botUI.loading=false;renderBots();return}
 try{
  const [_,bots,deliveries]=await Promise.all([loadBotModels().catch(()=>{}),api(`/api/notification-bots?projectId=${encodeURIComponent(projectId)}`),api(`/api/notification-deliveries?projectId=${encodeURIComponent(projectId)}`)]);
  if(revision!==botUI.revision||state.projectFilter!==projectId)return;
  botUI.items=bots;botUI.deliveries=deliveries;
 }catch(e){if(revision===botUI.revision)botUI.error=e.message}
 finally{if(revision===botUI.revision){botUI.loading=false;renderBots()}}
}
function renderBots(){
 const host=$('#botsContent');if(!host)return;
 const editorScroll=host.querySelector('.bot-editor fieldset')?.scrollTop||0;
 const selected=botUI.selected,project=currentProject();
 if(botUI.loading){host.innerHTML=botLoadingShell();return}
 if(!state.projectFilter){host.innerHTML='<div class="empty-properties"><b>请先选择项目</b><p>机器人和通知目标按项目管理。</p></div>';return}
 const cards=botUI.items.map(bot=>`<button type="button" class="bot-card ${selected?.id===bot.id?'selected':''}" data-bot-select="${escapeHTML(bot.id)}"><div><strong>${escapeHTML(bot.name)}</strong><span class="bot-chip ${bot.enabled?'':'muted'}">${bot.enabled?'已启用':'已停用'}</span></div><p>${bot.target?`${botTypeLabel(bot.target.type)} · ${escapeHTML(bot.target.name)}`:'尚未选择接收会话'}</p><small>${escapeHTML(botHealthLabel(bot.health))}</small></button>`).join('');
 const editor=selected?botEditorHTML(selected):`<div class="bot-welcome"><div class="bot-welcome-icon">♧</div><h2>让任务进展抵达企业微信</h2><p>选择或新建机器人，绑定单聊或群聊。随后在 SOP 设置或 AI 定时任务中选择这个 Bot。</p><p class="muted">当前项目：${escapeHTML(project?.name||'')}</p><button type="button" class="button" id="botWelcomeNew" ${canCreateBot()?'':'disabled'}>新建 Bot</button></div>`;
 host.innerHTML=`<div class="bot-message-bar ${botUI.error?'bot-error':''}" role="status">${botUI.error?escapeHTML(botUI.error):'选择项目机器人，配置接收聊天；发送结果会记录在下方。'}</div><div class="bot-layout"><aside class="bot-list"><div class="bot-list-head"><strong>项目机器人 <small>${botUI.items.length}</small></strong><button type="button" class="button ghost small" id="botNew" ${canCreateBot()?'':'disabled'}>＋ 新建</button></div>${cards||'<div class="bot-empty">还没有机器人<br>添加一个企业微信 Bot 开始接收通知</div>'}</aside><div class="bot-editor">${editor}</div></div><div class="bot-history"><div class="bot-list-head"><h2>最近发送记录</h2><span class="muted">保留最近 100 条</span></div>${botDeliveryHTML()}</div>`;
 host.querySelectorAll('[data-bot-select]').forEach(el=>el.onclick=()=>{if(botUI.busy)return;botUI.selected=structuredClone(botUI.items.find(b=>b.id===el.dataset.botSelect));botUI.sessions=[];botUI.query='';botUI.error='';botUI.interaction=null;renderBots();loadBotInteractions()});
 for(const id of ['botNew','botWelcomeNew'])host.querySelector('#'+id)?.addEventListener('click',()=>{if(botUI.busy)return;botUI.selected={name:'',botId:'',enabled:true,projectId:state.projectFilter};botUI.sessions=[];botUI.interaction=null;botUI.error='';renderBots()});
 bindBotEditor(host);
 host.querySelectorAll('[data-delivery-retry]').forEach(el=>el.onclick=async()=>{el.disabled=true;try{await api(`/api/notification-deliveries/${encodeURIComponent(el.dataset.deliveryRetry)}/retry?projectId=${encodeURIComponent(state.projectFilter)}`,{method:'POST'});toast('已加入重试队列','ok');await loadBots()}catch(e){toast(e.message,'err');el.disabled=false}});
}
function botEditorHTML(bot){
 const manage=!bot.id||canManageBot(bot),disabled=!manage||botUI.busy;
 return `<form id="botForm"><div class="bot-editor-head"><div><h2>${bot.id?'机器人设置':'新建企业微信 Bot'}</h2><p>保存凭据后可开启对话；主动通知请另选接收目标。</p></div>${bot.id&&manage?'<button type="button" class="button ghost danger small" id="botDelete">删除</button>':''}</div><fieldset ${disabled?'disabled':''}><div class="bot-fields"><label>名称<input class="input" name="name" required maxlength="80" value="${escapeHTML(bot.name)}" placeholder="例如：研发进展通知"></label><label>Bot ID<input class="input" name="botId" required maxlength="191" value="${escapeHTML(bot.botId)}" autocomplete="off" placeholder="企业微信智能机器人 Bot ID"></label><label>Secret<input class="input" name="secret" type="password" value="${escapeHTML(bot._secret||'')}" maxlength="512" ${bot.id?'':'required'} autocomplete="new-password" placeholder="${bot.secretConfigured?'已配置 · 留空保留，填写则替换':'填写机器人 Secret'}"></label><label>AI 模型（CodeBuddy）${botModelPickerHTML(bot)}<small class="muted">用于对话和完成汇报；对话使用发送者绑定的平台账号凭据，SOP 汇报使用本次执行者凭据。</small></label><label class="bot-check"><input name="enabled" type="checkbox" ${bot.enabled?'checked':''}>启用此 Bot</label><label class="bot-check"><input name="interactionEnabled" type="checkbox" ${bot.interactionEnabled?'checked':''}>启用对话（自动执行）</label></div><p class="muted bot-interaction-hint">首次对话请按 Bot 提示关联平台账号；新单聊和群聊会自动建立上下文。发送 /new 或“开启新会话”开始新的对话。</p>${botInteractionHTML(bot)}<div class="bot-target"><div class="bot-list-head"><h3>接收会话</h3><button type="button" class="button ghost small" id="botRefreshSessions" ${bot.id?'':'disabled'}>刷新可发送列表</button></div><p class="muted">只展示机器人当前可通知的最近会话。找不到目标时，请先在企业微信与 Bot 互动，再刷新。</p><div class="bot-current">${bot.target?`当前目标：<span class="bot-chip">${botTypeLabel(bot.target.type)}</span> ${escapeHTML(bot.target.name)}`:'尚未绑定接收会话'}</div><input class="input" id="botSessionSearch" type="search" placeholder="搜索已获取的联系人或群名" value="${escapeHTML(botUI.query)}"><div id="botSessionList">${botSessionHTML()}</div>${bot.target?'<button type="button" class="button ghost small" id="botClearTarget">清除目标</button>':''}</div><div class="bot-save-row"><button type="submit" class="button">${botUI.busy?'处理中…':'保存设置'}</button><button type="button" class="button ghost" id="botSendTest" ${bot.id&&bot.target&&bot.enabled?'':'disabled'}>发送测试通知</button></div></fieldset>${!manage?'<p class="muted">仅创建者或项目管理员可修改凭据和接收目标。</p>':''}</form>`;
}
function botSessionHTML(){
 const chats=filteredBotSessions(botUI.sessions,botUI.query);
 if(!botUI.sessions.length)return '<p class="muted bot-empty">点击刷新获取可发送会话；列表为空时，先与 Bot 互动。</p>';
 if(!chats.length)return '<p class="muted bot-empty">没有匹配的会话，尝试其他名称。</p>';
 return chats.map(chat=>`<label class="bot-session"><input type="radio" name="targetId" value="${escapeHTML(chat.id)}" ${botUI.selected?.target?.id===chat.id?'checked':''}><span><b>${escapeHTML(chat.name||'未命名会话')}</b><small>${escapeHTML(chat.lastMessageTime||'')}</small></span><span class="bot-chip">${botTypeLabel(chat.type)}</span></label>`).join('');
}
function botDeliveryHTML(){
 if(!botUI.deliveries.length)return '<p class="bot-empty">暂无发送记录。Agent 节点触发通知后，会在这里展示发送状态。</p>';
 return `<div class="bot-table-wrap"><table class="bot-table"><thead><tr><th>时间</th><th>Bot</th><th>接收会话</th><th>来源 / 触发条件</th><th>状态</th><th></th></tr></thead><tbody>${botUI.deliveries.map(item=>{const bot=botUI.items.find(b=>b.id===item.botId);return `<tr><td>${escapeHTML(new Date(item.createdAt).toLocaleString())}</td><td>${escapeHTML(bot?.name||'已删除的 Bot')}</td><td>${escapeHTML(item.targetName)}</td><td>${escapeHTML(item.sourceName||'')}<small class="bot-source-label">${escapeHTML(({schedule_job:'定时任务',assistant_task:'AI 助手任务',run:'流程'})[item.sourceType]||'流程')} · ${escapeHTML(({succeeded:'完成',failed:'失败',waiting:'人工处理',test:'测试通知'})[item.eventType]||item.eventType)}</small>${item.contentSource&&item.contentSource!=='template'?`<small class="bot-source-label">${escapeHTML(({ai:'AI 内容',simulated_ai:'模拟 AI 内容',result_fallback:'执行结果回退',context_fallback:'状态摘要回退',invalid:'内容格式异常',fallback:'标准消息回退'})[item.contentSource]||item.contentSource)}</small>`:''}</td><td><span class="bot-chip">${escapeHTML(botStatusLabel(item.status))}</span>${item.reportModel?`<small class="bot-source-label">CodeBuddy · ${escapeHTML(item.reportModel)}</small>`:''}${item.reportError?`<small class="bot-record-error">${escapeHTML(item.reportError)}</small>`:''}${item.lastError?`<small class="bot-record-error">${escapeHTML(item.lastError)}</small>`:''}</td><td>${['failed','awaiting_authorization','target_unavailable'].includes(item.status)&&item.contentSource!=='invalid'&&bot&&canManageBot(bot)?`<button class="button ghost small" data-delivery-retry="${escapeHTML(item.id)}">重试</button>`:''}</td></tr>`}).join('')}</tbody></table></div>`;
}
function captureBotDraft(host){const form=host.querySelector('#botForm');if(!form||!botUI.selected)return;botUI.selected.name=form.elements.name.value;botUI.selected.botId=form.elements.botId.value;botUI.selected.enabled=form.elements.enabled.checked;botUI.selected.model=form.elements.model.value;botUI.selected.interactionEnabled=form.elements.interactionEnabled.checked;botUI.selected._secret=form.elements.secret.value}
function bindBotEditor(host){
 const form=host.querySelector('#botForm');if(!form)return;
 bindBotModelPicker(host);
 host.querySelector('#botRefreshInteractions')?.addEventListener('click',()=>loadBotInteractions());host.querySelector('.bot-interaction-records details')?.addEventListener('toggle',event=>{botUI.interactionExpanded=event.target.open});
 form.oninput=()=>captureBotDraft(host);form.onchange=()=>captureBotDraft(host);
 host.querySelector('#botSessionSearch').oninput=event=>{botUI.query=event.target.value;host.querySelector('#botSessionList').innerHTML=botSessionHTML();bindBotSessionSelection(host)};
 bindBotSessionSelection(host);
 form.onsubmit=async event=>{
  event.preventDefault();if(botUI.busy)return;captureBotDraft(host);
  const bot=botUI.selected,projectId=botUI.projectId,revision=botUI.revision;
  const payload={projectId,name:bot.name,botId:bot.botId,enabled:bot.enabled,model:bot.model,interactionEnabled:bot.interactionEnabled,secret:form.elements.secret.value};
  if(bot.id&&!payload.secret&&bot.botId===botUI.items.find(b=>b.id===bot.id)?.botId&&bot._targetChanged)payload.targetId=bot.target?.id||'';
  if(bot._targetChanged&&(payload.secret||!bot.id)){toast('请先保存凭据，再刷新并选择目标','warn');return}
  botUI.busy=true;form.querySelector('fieldset').disabled=true;
  try{
   const saved=await api(bot.id?`/api/notification-bots/${encodeURIComponent(bot.id)}`:'/api/notification-bots',{method:bot.id?'PUT':'POST',body:JSON.stringify(payload)});
   if(revision!==botUI.revision||projectId!==state.projectFilter)return;
   const index=botUI.items.findIndex(b=>b.id===saved.id);if(index<0)botUI.items.push(saved);else botUI.items[index]=saved;
   botUI.selected=structuredClone(saved);botUI.error='';toast('机器人设置已保存','ok');nodeBotCache.delete(projectId);
  }catch(e){if(revision===botUI.revision)botUI.error=e.message}
  finally{botUI.busy=false;renderBots()}
 };
 host.querySelector('#botRefreshSessions')?.addEventListener('click',async()=>{
  if(botUI.busy)return;
  if(form.elements.secret.value||botUI.selected.botId!==botUI.items.find(b=>b.id===botUI.selected.id)?.botId){toast('请先保存新的凭据','warn');return}
  const id=botUI.selected.id,revision=botUI.revision;botUI.busy=true;form.querySelector('fieldset').disabled=true;botUI.sessions=[];
  try{const result=await api(`/api/notification-bots/${encodeURIComponent(id)}/sessions`);if(revision!==botUI.revision||botUI.selected?.id!==id)return;botUI.sessions=result.sessions;botUI.error='';botUI.selected.health='ready';const stored=botUI.items.find(b=>b.id===id);if(stored)stored.health='ready';toast(`已刷新 ${result.total} 个可发送会话`,'ok')}
  catch(e){if(revision===botUI.revision)botUI.error=e.message}
  finally{botUI.busy=false;renderBots()}
 });
 host.querySelector('#botClearTarget')?.addEventListener('click',()=>{botUI.selected.target=null;botUI.selected._targetChanged=true;renderBots()});
 host.querySelector('#botSendTest')?.addEventListener('click',async()=>{
  if(botUI.busy)return;if(botUI.selected._targetChanged||form.elements.secret.value||botUI.selected.botId!==botUI.items.find(b=>b.id===botUI.selected.id)?.botId||botUI.selected.enabled!==botUI.items.find(b=>b.id===botUI.selected.id)?.enabled){toast('请先保存设置，再发送测试通知','warn');return}
  const id=botUI.selected.id,projectId=botUI.projectId,revision=botUI.revision;botUI.busy=true;form.querySelector('fieldset').disabled=true;
  try{const result=await api(`/api/notification-bots/${encodeURIComponent(id)}/test`,{method:'POST'});toast(`测试通知已发送到 ${result.targetName}`,'ok');try{const records=await api(`/api/notification-deliveries?projectId=${encodeURIComponent(projectId)}`,{silent:true});if(revision===botUI.revision)botUI.deliveries=records}catch{}}
  catch(e){botUI.error=e.message}finally{botUI.busy=false;renderBots()}
 });
 host.querySelector('#botDelete')?.addEventListener('click',async()=>{
  if(botUI.busy)return;if(!await afConfirm(`确定删除「${botUI.selected.name}」？已有节点绑定将无法继续发送。`))return;
  try{await api(`/api/notification-bots/${encodeURIComponent(botUI.selected.id)}`,{method:'DELETE'});nodeBotCache.delete(botUI.projectId);toast('机器人已删除','ok');await loadBots()}catch(e){botUI.error=e.message;renderBots()}
 });
}
function bindBotSessionSelection(host){host.querySelectorAll('[name="targetId"]').forEach(el=>el.onchange=()=>{botUI.selected.target=structuredClone(botUI.sessions.find(chat=>chat.id===el.value));botUI.selected._targetChanged=true;renderBots()})}


function botLoadingShell(){return '<div class="bot-message-bar" role="status">正在读取机器人…</div><div class="bot-layout"><aside class="bot-list"><div class="bot-list-head"><strong>项目机器人</strong></div><div class="bot-skeleton"></div><div class="bot-skeleton"></div></aside><div class="bot-editor"><div class="bot-welcome"><h2>正在读取设置</h2><p>机器人和接收聊天按项目管理</p></div></div></div><div class="bot-history"><h2>最近发送记录</h2><p class="bot-empty">正在读取记录…</p></div>'}

async function loadBotInteractions(){const bot=botUI.selected,revision=botUI.revision;if(!bot?.id||!canManageBot(bot))return;try{const result=await api(`/api/notification-bots/${encodeURIComponent(bot.id)}/interactions`,{silent:true});if(revision!==botUI.revision||botUI.selected?.id!==bot.id)return;botUI.interaction=result;renderBots()}catch(e){if(revision===botUI.revision&&botUI.selected?.id===bot.id){botUI.error=e.message;renderBots()}}}
function botInteractionHTML(bot){if(!bot.id||!canManageBot(bot))return '';const data=botUI.interaction;const state=data?.connection?.state||'offline';const label=({connected:'已连接',connecting:'连接中',reconnecting:'重连中',error:'连接异常',offline:'未连接'})[state]||state;return `<div class="bot-interaction-records"><div class="bot-list-head"><strong>对话连接 · ${escapeHTML(label)}</strong><button type="button" class="button ghost small" id="botRefreshInteractions">刷新对话记录</button></div>${data?.connection?.lastError?`<p class="bot-record-error">${escapeHTML(data.connection.lastError)}</p>`:''}<details ${botUI.interactionExpanded?'open':''}><summary>最近对话 · ${data?.conversations?.length||0} 个聊天</summary>${(data?.messages||[]).filter(m=>m.action!=='notification').slice(0,12).map(m=>`<div class="bot-interaction-entry"><small>${escapeHTML(new Date(m.createdAt).toLocaleString())} · ${escapeHTML(m.senderId||'')} · ${escapeHTML(({queued:'等待处理',running:'处理中',ready:'等待回复',replied:'已回复',reply_failed:'回复失败'})[m.status]||m.status)}</small><p>${escapeHTML(m.content||m.action||'')}</p>${m.lastError?`<small class="bot-record-error">${escapeHTML(m.lastError)}</small>`:''}${m.reply?`<p class="muted">${escapeHTML(m.reply.slice(0,240))}</p>`:''}</div>`).join('')||'<p class="muted">还没有对话记录</p>'}</details></div>`}
