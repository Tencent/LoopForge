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
  models.renderCodeBuddyList(menu,value.value,id=>{value.value=id;models.paint(toggle,id);captureBotDraft(host);closeBotModelPicker()});
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
const botUI={projectId:'',items:[],deliveries:[],selected:null,sessions:[],query:'',loading:false,error:'',busy:false,busyAction:'',revision:0,tab:'list',step:1,recordsTab:'deliveries',interactionBotId:'',interaction:null,interactionLoading:false,interactionError:'',testResult:'',flashId:'',motion:null,animations:[],shownCount:null,shownError:'',watchTimer:0,watchCount:0,deletingId:''};
const BOT_TABS=['list','edit','records'],BOT_STEPS=[[1,'填写凭据'],[2,'选择接收会话'],[3,'发送测试']],BOT_DEFAULT_MODEL='glm-5.3-flash-ioa';
// In-progress states breathe; terminal states stay still.
const BOT_LIVE_STATES=['pending','sending','connecting','reconnecting','queued','running','ready'];
const BOT_CONNECTION={connected:['已连接','success'],connecting:['连接中','info'],reconnecting:['重连中','info'],error:['连接异常','error'],offline:['未连接','neutral']};
const BOT_MESSAGE={queued:['等待处理','info'],running:['处理中','info'],ready:['等待回复','info'],replied:['已回复','success'],reply_failed:['回复失败','error']};
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
function botStepAllowed(bot,step){return step===1||(step===2&&!!bot?.id)||(step===3&&!!bot?.id&&!!bot?.target&&!bot._targetChanged)}
function botStepDone(bot,step){return step===1?!!bot?.id:step===2?botStepAllowed(bot,3):false}
function botStoredBot(bot){return bot?.id?botUI.items.find(b=>b.id===bot.id):null}
function botCredentialsDirty(bot){const stored=botStoredBot(bot);if(!stored)return !!bot;return !!bot._secret||(bot.name||'')!==(stored.name||'')||(bot.botId||'')!==(stored.botId||'')||(bot.model||BOT_DEFAULT_MODEL)!==(stored.model||BOT_DEFAULT_MODEL)||!!bot.enabled!==!!stored.enabled||!!bot.interactionEnabled!==!!stored.interactionEnabled}

function botIcon(name){return `<span class="bot-icon" data-bot-icon="${name}" aria-hidden="true"></span>`}
// Harness StatusBadge: in-progress states breathe, settled states carry a glyph so colour is never the only cue.
const BOT_BADGE_GLYPHS={success:'check',error:'alert',warn:'user',info:'clock',neutral:'minus'};
const BOT_BADGE_OVERRIDES={unchecked:'clock',cancelled:'close',expired:'clock'};
function botBadgeIcon(tone,state){return BOT_BADGE_OVERRIDES[state]||BOT_BADGE_GLYPHS[tone]||'minus'}
// The vendored Harness set has no plain minus, so it is drawn inline with the same stroke.
function botGlyphHTML(name){return name==='minus'?'<span class="bot-icon hui-badge-glyph" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 12h12"/></svg></span>':`<span class="bot-icon hui-badge-glyph" data-bot-icon="${name}" aria-hidden="true"></span>`}
function botBadge(label,tone,live,state){tone=tone||'neutral';return `<span class="hui-badge" data-size="sm" data-tone="${tone}">${live?'<span class="hui-lamp" aria-hidden="true"><i></i></span>':botGlyphHTML(botBadgeIcon(tone,state))}<span class="hui-badge-label">${escapeHTML(label)}</span></span>`}
function botStatusTone(status){return ({sent:'success',failed:'error',target_unavailable:'error',awaiting_authorization:'warn',pending:'info',sending:'info'})[status]||'neutral'}
function botStatusBadge(status){return botBadge(botStatusLabel(status),botStatusTone(status),BOT_LIVE_STATES.includes(status),status)}
function botHealthBadge(health){return botBadge(botHealthLabel(health),({ready:'success',error:'error',awaiting_authorization:'warn'})[health]||'neutral',false,health||'unchecked')}
function botEnabledBadge(enabled){return botBadge(enabled?'已启用':'已停用',enabled?'success':'neutral',false)}
function botConnectionBadge(connection){const [label,tone]=BOT_CONNECTION[connection]||[connection,'neutral'];return botBadge('对话连接 · '+label,tone,BOT_LIVE_STATES.includes(connection),connection)}
function botMessageBadge(status){const [label,tone]=BOT_MESSAGE[status]||[status,'neutral'];return botBadge(label,tone,BOT_LIVE_STATES.includes(status),status)}
function botBusyLabel(action,icon,label,busyLabel){return botUI.busy&&botUI.busyAction===action?`<span class="bot-icon bot-spin" data-bot-icon="spinner" aria-hidden="true"></span>${busyLabel}`:`${icon?botIcon(icon):''}${label}`}

// Only list and records are tabs; editing swaps the panel head for a breadcrumb (botCrumbHTML).
function botTabsHTML(tab,count){
 const n=Number(count)||0,on=tab==='records'?'records':'list';
 const item=(id,label,extra='')=>`<button type="button" role="tab" aria-selected="${on===id}" class="${on===id?'active':''}" data-bot-tab="${id}" ${botUI.busy&&tab!==id?'aria-disabled="true"':''}><span>${label}</span>${extra}</button>`;
 return `<div class="bot-tabs" role="tablist" aria-label="通道管理">${item('list','机器人',n?`<span class="bot-tab-count sliding-tab-count" data-bot-count>${n}</span>`:'')}${item('records','记录')}</div>`;
}
function botCrumbHTML(bot){
 const stored=botStoredBot(bot),manage=!!bot.id&&canManageBot(bot),current=bot.id?escapeHTML(stored?.name||bot.name||'未命名 Bot'):'新建 Bot';
 return `<nav class="bot-crumbs" aria-label="位置"><button type="button" class="bot-crumb-link" data-bot-back ${botUI.busy?'disabled':''}>${botIcon('robot')}<span>机器人</span></button>${botIcon('chevron-right')}<span class="bot-crumb-current" aria-current="page">${current}</span></nav><span class="bot-head-spacer"></span>${manage?`<button type="button" class="button small danger-outline" id="botDelete" ${botUI.busy?'disabled':''}>${botIcon('trash')}删除 Bot</button>`:''}`;
}
function botPanelHeadHTML(){
 if(botUI.tab==='edit'&&botUI.selected)return `<div class="bot-panel-head is-crumb">${botCrumbHTML(botUI.selected)}</div>`;
 const loading=botUI.loading;
 return `<div class="bot-panel-head">${botTabsHTML(botUI.tab,botUI.items.length)}<span class="bot-head-spacer"></span><button type="button" class="hui-icon-btn" data-bot-refresh title="刷新" aria-label="刷新" ${loading?'disabled':''}>${loading?'<span class="bot-icon bot-spin" data-bot-icon="spinner" aria-hidden="true"></span>':botIcon('refresh')}</button></div>`;
}
function botTargetHTML(target){return target?`<span class="bot-target"><span class="bot-chip">${botTypeLabel(target.type)}</span><span class="bot-target-name">${escapeHTML(target.name)}</span></span>`:'<span class="bot-target is-empty">尚未选择接收会话</span>'}
function botRowHTML(bot){
 const id=escapeHTML(bot.id),name=escapeHTML(bot.name),manage=canManageBot(bot),target=botTargetHTML(bot.target);
 const sub=escapeHTML(bot.model||BOT_DEFAULT_MODEL)+(bot.interactionEnabled?' · 已启用对话':'');
 return `<tr data-bot-row="${id}"><td><div class="bot-row-main"><span class="bot-glyph">${botIcon('robot')}</span><span class="bot-row-text"><b class="bot-row-name">${name}</b><small class="bot-row-sub">${sub}</small><small class="bot-row-target">${target}</small></span></div></td><td class="bot-col-target">${target}</td><td>${botEnabledBadge(bot.enabled)}</td><td class="bot-col-sub">${botHealthBadge(bot.health)}</td><td class="bot-col-actions"><div class="bot-row-actions"><button type="button" class="hui-icon-btn" data-bot-edit="${id}" aria-label="编辑 ${name}" title="编辑">${botIcon('edit')}</button>${manage?`<button type="button" class="hui-icon-btn is-danger" data-bot-delete="${id}" aria-label="删除 ${name}" title="删除">${botIcon('trash')}</button>`:''}</div></td></tr>`;
}
function botListHTML(items){
 if(!items?.length)return botGuideHTML();
 return `<div class="bot-table-wrap"><table class="bot-table bot-list-table"><thead><tr><th>名称</th><th class="bot-col-target">接收会话</th><th>启用状态</th><th class="bot-col-sub">连接健康</th><th class="bot-col-actions"><span class="bot-sr">操作</span></th></tr></thead><tbody>${items.map(botRowHTML).join('')}</tbody></table></div>`;
}
function botGuideHTML(){
 const project=typeof currentProject==='function'?currentProject():null;
 const steps=[['① 填写凭据','名称、Bot ID 与 Secret'],['② 选择接收会话','绑定单聊或群聊'],['③ 发送测试','确认消息能送达']];
 return `<div class="bot-guide"><div class="bot-guide-icon">${botIcon('robot')}</div><h2>让任务进展抵达企业微信</h2><p class="muted">还没有机器人。三步完成配置，随后在 SOP 设置或 AI 定时任务中选择这个 Bot。</p><ol>${steps.map(([title,text],i)=>`<li style="--i:${i}"><b>${title}</b><span>${text}</span></li>`).join('')}</ol>${project?.name?`<p class="muted">当前项目：${escapeHTML(project.name)}</p>`:''}<button type="button" class="button primary" data-bot-new ${canCreateBot()?'':'disabled'}>${botIcon('plus')}开始添加</button></div>`;
}
function botStepsHTML(bot,step){
 return `<div class="bot-steps" style="--bot-progress:${(step-1)/2}"><div class="bot-step-track" aria-hidden="true"><i></i></div>${BOT_STEPS.map(([n,label])=>{const done=n!==step&&botStepDone(bot,n);return `<button type="button" class="bot-step${n===step?' active':''}${done?' done':''}" data-bot-step="${n}" ${botStepAllowed(bot,n)&&!botUI.busy?'':'disabled'} ${n===step?'aria-current="step"':''}><span class="bot-step-n">${done?botIcon('check-draw'):n}</span><span class="bot-step-label">${label}</span></button>`}).join('')}</div>`;
}
function botEditTabHTML(bot){
 if(!bot)return botListHTML(botUI.items);
 const manage=!bot.id||canManageBot(bot),step=botUI.step;
 const pane=step===2?botTargetPaneHTML(bot,manage):step===3?botTestPaneHTML(bot,manage):botCredentialPaneHTML(bot,manage);
 return `<div class="bot-editor">${botStepsHTML(bot,step)}<div class="bot-step-fold"><div class="bot-step-pane" data-bot-pane="${step}">${pane}</div></div>${!manage?'<p class="muted bot-hint">仅创建者或项目管理员可修改凭据和接收目标。</p>':''}</div>`;
}
function botCredentialPaneHTML(bot,manage){
 return `<form id="botForm" novalidate><fieldset ${!manage||botUI.busy?'disabled':''}><div class="bot-fields"><label>名称<input class="input" name="name" required maxlength="80" value="${escapeHTML(bot.name)}" placeholder="例如：研发进展通知"></label><label>Bot ID<input class="input" name="botId" required maxlength="191" value="${escapeHTML(bot.botId)}" autocomplete="off" placeholder="企业微信智能机器人 Bot ID"></label><label>Secret<input class="input" name="secret" type="password" value="${escapeHTML(bot._secret||'')}" maxlength="512" ${bot.id?'':'required'} autocomplete="new-password" placeholder="${bot.secretConfigured?'已配置 · 留空保留，填写则替换':'填写机器人 Secret'}"></label><label>AI 模型（CodeBuddy）${botModelPickerHTML(bot)}<small class="muted">用于对话和完成汇报；对话使用发送者绑定的平台账号凭据，SOP 汇报使用本次执行者凭据。</small></label><label class="bot-check"><input name="enabled" type="checkbox" role="switch" ${bot.enabled?'checked':''}>启用此 Bot</label><label class="bot-check"><input name="interactionEnabled" type="checkbox" role="switch" ${bot.interactionEnabled?'checked':''}>启用对话（自动执行）</label></div><p class="muted bot-hint">首次对话请按 Bot 提示关联平台账号；新单聊和群聊会自动建立上下文。发送 /new 或“开启新会话”开始新的对话。</p><div class="bot-save-row"><button type="submit" class="button primary">${botBusyLabel('save','','保存并下一步','保存中…')}</button></div></fieldset></form>`;
}
function botTargetPaneHTML(bot,manage){
 const changed=!!bot._targetChanged;
 return `<fieldset ${!manage||botUI.busy?'disabled':''}><div class="bot-bar"><b>接收会话</b><button type="button" class="button ghost small" id="botRefreshSessions" ${bot.id?'':'disabled'}>${botBusyLabel('sessions','refresh','刷新可发送列表','刷新中…')}</button></div><p class="muted bot-hint">只展示机器人当前可通知的最近会话。找不到目标时，请先在企业微信与 Bot 互动，再刷新。</p><div class="bot-current">${bot.target?`当前目标：<span class="bot-chip">${botTypeLabel(bot.target.type)}</span> ${escapeHTML(bot.target.name)}<button type="button" class="button ghost small" id="botClearTarget">清除目标</button>`:'尚未绑定接收会话'}</div><input class="input" id="botSessionSearch" type="search" placeholder="搜索已获取的联系人或群名" value="${escapeHTML(botUI.query)}"><div id="botSessionList" class="bot-sessions">${botSessionHTML()}</div><div class="bot-save-row"><button type="button" class="button primary" id="botSaveTarget" ${changed||bot.target?'':'disabled'}>${changed?botBusyLabel('target','','保存接收会话','保存中…'):'下一步'}</button></div></fieldset>`;
}
function botTestPaneHTML(bot,manage){
 const ready=!!(bot.enabled&&bot.target);
 const result=botUI.testResult?`<div class="bot-result-fold"><div class="bot-result" role="status">${botIcon('check-draw')}<span>测试通知已发送到 ${escapeHTML(botUI.testResult)}，可在「记录 → 发送记录」查看。</span></div></div>`:'';
 return `<div class="bot-summary"><span class="muted">Bot</span><b>${escapeHTML(bot.name)}</b><span class="muted">接收会话</span><span>${bot.target?`${botTypeLabel(bot.target.type)} · ${escapeHTML(bot.target.name)}`:'尚未绑定接收会话'}</span><span class="muted">状态</span><span>${botEnabledBadge(bot.enabled)}</span></div>${bot.enabled?'':'<p class="muted bot-hint">Bot 未启用，请回到第①步启用并保存后再发送测试。</p>'}${result}<div class="bot-save-row"><button type="button" class="button primary" id="botSendTest" ${manage&&ready&&!botUI.busy?'':'disabled'}>${botBusyLabel('test','send','发送测试通知','发送中…')}</button><button type="button" class="button ghost" data-bot-back ${botUI.busy?'disabled':''}>完成，返回列表</button></div>`;
}
function botSessionHTML(){
 const chats=filteredBotSessions(botUI.sessions,botUI.query);
 if(!botUI.sessions.length)return '<p class="muted bot-empty">点击刷新获取可发送会话；列表为空时，先与 Bot 互动。</p>';
 if(!chats.length)return '<p class="muted bot-empty">没有匹配的会话，尝试其他名称。</p>';
 return '<span class="bot-session-hl" aria-hidden="true"></span>'+chats.map(chat=>`<label class="bot-session"><input type="radio" name="targetId" value="${escapeHTML(chat.id)}" ${botUI.selected?.target?.id===chat.id?'checked':''}><span><b>${escapeHTML(chat.name||'未命名会话')}</b><small>${escapeHTML(chat.lastMessageTime||'')}</small></span><span class="bot-chip">${botTypeLabel(chat.type)}</span></label>`).join('');
}
function botRecordTabsHTML(active){const item=(id,label)=>{const on=id==='interactions'?active==='interactions':active!=='interactions';return `<button type="button" role="tab" aria-selected="${on}" class="${on?'active':''}" data-bot-records="${id}">${label}</button>`};return `<div class="bot-record-tabs" role="tablist" aria-label="记录类型">${item('deliveries','发送记录')}${item('interactions','对话记录')}</div>`}
function botRecordsHTML(){
 const deliveries=botUI.recordsTab!=='interactions';
 return `<div class="bot-subbar">${botRecordTabsHTML(botUI.recordsTab)}${deliveries?'<span class="bot-subbar-hint">保留最近 100 条</span>':''}</div><div class="bot-record-pane">${deliveries?botDeliveryHTML():botInteractionPanelHTML()}</div>`;
}
function botDeliveryExtraHTML(item){return `${item.reportModel?`<small class="bot-source-label">CodeBuddy · ${escapeHTML(item.reportModel)}</small>`:''}${item.reportError?`<small class="bot-record-error">${escapeHTML(item.reportError)}</small>`:''}${item.lastError?`<small class="bot-record-error">${escapeHTML(item.lastError)}</small>`:''}`}
function botDeliveryActionHTML(item){const bot=botUI.items.find(b=>b.id===item.botId);return ['failed','awaiting_authorization','target_unavailable'].includes(item.status)&&item.contentSource!=='invalid'&&bot&&canManageBot(bot)?`<button type="button" class="button small hui-btn-soft" data-delivery-retry="${escapeHTML(item.id)}">${botIcon('refresh')}重试</button>`:''}
function botDeliveryHTML(){
 if(!botUI.deliveries.length)return '<p class="bot-empty">暂无发送记录。Agent 节点触发通知后，会在这里展示发送状态。</p>';
 return `<div class="bot-table-wrap"><table class="bot-table bot-record-table"><thead><tr><th>时间</th><th class="bot-col-sub">Bot</th><th class="bot-col-sub">接收会话</th><th>来源 / 触发条件</th><th>状态</th><th></th></tr></thead><tbody>${botUI.deliveries.map(item=>{const bot=botUI.items.find(b=>b.id===item.botId);return `<tr data-delivery-row="${escapeHTML(item.id)}"><td class="bot-nowrap bot-time">${escapeHTML(new Date(item.createdAt).toLocaleString())}</td><td class="bot-col-sub">${escapeHTML(bot?.name||'已删除的 Bot')}</td><td class="bot-col-sub">${escapeHTML(item.targetName)}</td><td>${escapeHTML(item.sourceName||'')}<small class="bot-source-label">${escapeHTML(({schedule_job:'定时任务',assistant_task:'AI 助手任务',run:'流程'})[item.sourceType]||'流程')} · ${escapeHTML(({succeeded:'完成',failed:'失败',waiting:'人工处理',test:'测试通知'})[item.eventType]||item.eventType)}</small>${item.contentSource&&item.contentSource!=='template'?`<small class="bot-source-label">${escapeHTML(({ai:'AI 内容',simulated_ai:'模拟 AI 内容',result_fallback:'执行结果回退',context_fallback:'状态摘要回退',invalid:'内容格式异常',fallback:'标准消息回退'})[item.contentSource]||item.contentSource)}</small>`:''}</td><td data-delivery-status>${botStatusBadge(item.status)}<span data-delivery-extra>${botDeliveryExtraHTML(item)}</span></td><td class="bot-nowrap" data-delivery-action>${botDeliveryActionHTML(item)}</td></tr>`}).join('')}</tbody></table></div>`;
}
function botInteractionBots(){return botUI.items.filter(bot=>canManageBot(bot))}
function botInteractionPanelHTML(){
 const bots=botInteractionBots();
 if(!bots.length)return '<p class="bot-empty">暂无可查看对话的 Bot。仅创建者或项目管理员可查看对话记录。</p>';
 const bot=bots.find(b=>b.id===botUI.interactionBotId)||bots[0],data=botUI.interaction,connection=data?.connection?.state||'offline';
 const messages=(data?.messages||[]).filter(m=>m.action!=='notification').slice(0,12);
 const count=data?`<span class="bot-subbar-hint">最近对话 · ${data.conversations?.length||0} 个聊天</span>`:'';
 const body=botUI.interactionError?`<p class="bot-record-error">${escapeHTML(botUI.interactionError)}</p>`:!data&&botUI.interactionLoading?botSkeletonHTML(2):messages.map(m=>`<div class="bot-entry"><div class="bot-entry-meta"><small>${escapeHTML(new Date(m.createdAt).toLocaleString())} · ${escapeHTML(m.senderId||'')}</small>${botMessageBadge(m.status)}</div><p>${escapeHTML(m.content||m.action||'')}</p>${m.lastError?`<small class="bot-record-error">${escapeHTML(m.lastError)}</small>`:''}${m.reply?`<p class="bot-entry-reply">${escapeHTML(m.reply.slice(0,240))}</p>`:''}</div>`).join('')||'<p class="bot-empty">还没有对话记录</p>';
 return `<div class="bot-conv"><div class="bot-conv-bar"><span class="bot-interaction-pick"><label><span class="bot-sr">Bot</span><select class="select" id="botInteractionBot" aria-label="选择 Bot">${bots.map(b=>`<option value="${escapeHTML(b.id)}" ${b.id===bot.id?'selected':''}>${escapeHTML(b.name)}</option>`).join('')}</select></label>${botConnectionBadge(connection)}</span><span class="bot-head-spacer"></span>${count}<button type="button" class="hui-icon-btn" id="botRefreshInteractions" title="刷新对话记录" aria-label="刷新对话记录" ${botUI.interactionLoading?'disabled':''}>${botUI.interactionLoading?'<span class="bot-icon bot-spin" data-bot-icon="spinner" aria-hidden="true"></span>':botIcon('refresh')}</button></div>${data?.connection?.lastError?`<p class="bot-record-error">${escapeHTML(data.connection.lastError)}</p>`:''}${body}</div>`;
}
function botSkeletonHTML(rows=3){return `<div class="bot-skeleton-rows" aria-hidden="true">${'<div class="bot-skeleton"></div>'.repeat(rows)}</div>`}
function botLoadingShell(){return `<section class="bot-panel" data-bot-panel="${botUI.tab}">${botPanelHeadHTML()}<div class="bot-panel-body is-loading" role="status" aria-label="正在读取机器人">${botSkeletonHTML()}</div></section>`}

// One-shot motion: state transitions register an intent, the next render consumes it.
// Ordinary re-renders (typing, busy toggles, background refreshes) carry no intent and never replay entrances.
function botMotion(kind,payload=true){(botUI.motion||(botUI.motion={}))[kind]=payload}
function botReducedMotion(){return !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches}
function botToken(name,fallback){const host=document.querySelector('#botsContent');return (host&&getComputedStyle(host).getPropertyValue(name).trim())||fallback}
function botEase(name){return botToken('--bot-'+name,'ease')}
function botDuration(name){return parseFloat(botToken('--bot-t-'+name,'280ms'))||280}
function botTrack(animation){botUI.animations.push(animation);animation.finished?.then(()=>{botUI.animations=botUI.animations.filter(a=>a!==animation)},()=>{});return animation}
function botCancelMotion(){botUI.animations.splice(0).forEach(a=>{try{a.cancel()}catch{}})}
function botPlayIcon(svg){if(!svg)return;svg.dataset.iconMotion='idle';requestAnimationFrame(()=>{if(!svg.isConnected)return;svg.dataset.iconMotion='once';setTimeout(()=>{if(svg.dataset.iconMotion==='once')svg.dataset.iconMotion='idle'},1500)})}
function renderBotIcons(host){const icons=window.AgentFlowHarnessIcons;if(icons)host.querySelectorAll('[data-bot-icon]').forEach(el=>icons.render(el,el.dataset.botIcon))}
function botPlaceSessionHighlight(host){
 const list=host.querySelector('#botSessionList'),hl=list?.querySelector('.bot-session-hl');if(!hl)return;
 const label=list.querySelector('input[name="targetId"]:checked')?.closest('.bot-session');
 if(!label){hl.style.opacity='0';delete hl.dataset.y;return}
 hl.style.opacity='1';hl.style.height=label.offsetHeight+'px';hl.style.transform=`translateY(${label.offsetTop}px)`;hl.dataset.y=label.offsetTop;
}
function botFoldOpen(el){
 if(!el?.animate)return;const height=el.offsetHeight;el.classList.add('is-folding');
 const animation=botTrack(el.animate([{height:'0px',opacity:0},{height:height+'px',opacity:1}],{duration:botDuration('fold'),easing:botEase('fold')}));
 const done=()=>el.classList.remove('is-folding');animation.onfinish=done;animation.oncancel=done;
}
function botAnimateStep(host,{dir,from},oldHeight){
 host.querySelector('.bot-step-pane')?.classList.add(dir>0?'bot-enter-r':'bot-enter-l');
 const fill=host.querySelector('.bot-step-track i'),fold=host.querySelector('.bot-step-fold');
 if(fill?.animate&&from)botTrack(fill.animate([{width:`${(from-1)/2*100}%`},{width:`${(botUI.step-1)/2*100}%`}],{duration:botDuration('indicator'),easing:botEase('spring')}));
 if(!fold?.animate||!oldHeight)return;
 const height=fold.offsetHeight;if(Math.abs(height-oldHeight)<2)return;
 fold.classList.add('is-folding');
 const animation=botTrack(fold.animate([{height:oldHeight+'px'},{height:height+'px'}],{duration:botDuration('fold'),easing:botEase('fold')}));
 const done=()=>fold.classList.remove('is-folding');animation.onfinish=done;animation.oncancel=done;
}
function botAnimateErrorExit(host,text){
 const ghost=document.createElement('div');ghost.className='bot-message-bar bot-error is-folding';ghost.setAttribute('aria-hidden','true');ghost.textContent=text;host.prepend(ghost);
 const style=getComputedStyle(ghost);if(!ghost.animate){ghost.remove();return}
 const animation=botTrack(ghost.animate([{height:ghost.offsetHeight+'px',marginBottom:style.marginBottom,opacity:1},{height:'0px',paddingTop:'0px',paddingBottom:'0px',marginBottom:'0px',opacity:0}],{duration:botDuration('fold'),easing:botEase('exit')}));
 const done=()=>ghost.remove();animation.onfinish=done;animation.oncancel=done;
}
function applyBotMotion(host,motion,before){
 botPlaceSessionHighlight(host);
 const count=botUI.items.length,bump=!botUI.loading&&botUI.shownCount!==null&&botUI.shownCount!==count;
 if(!botUI.loading)botUI.shownCount=count;
 const error=botUI.error;botUI.shownError=error;
 if(botReducedMotion())return;
 const add=(el,name)=>el?.classList.add(name),stagger=(items,name)=>items.forEach((el,i)=>{el.style.setProperty('--i',Math.min(i,7));el.classList.add(name)});
 if(bump)add(host.querySelector('[data-bot-count]'),'bump');
 if(motion.tab!==undefined)add(host.querySelector('.bot-panel-body'),motion.tab>0?'bot-enter-r':motion.tab<0?'bot-enter-l':'bot-enter-f');
 if(motion.rows){stagger(host.querySelectorAll('.bot-panel tbody tr'),'bot-row-in');const guide=host.querySelector('.bot-guide');if(guide){guide.classList.add('bot-guide-in');botPlayIcon(guide.querySelector('.bot-guide-icon svg'))}}
 if(motion.flash)add([...host.querySelectorAll('[data-bot-row]')].find(row=>row.dataset.botRow===motion.flash),'bot-row-flash');
 if(motion.records)add(host.querySelector('.bot-record-pane'),'bot-enter-f');
 if(motion.entries)stagger(host.querySelectorAll('.bot-entry'),'bot-entry-in');
 if(motion.sessions)stagger(host.querySelectorAll('.bot-session'),'bot-row-in');
 if(motion.step)botAnimateStep(host,motion.step,before.fold);
 if(motion.check)add(host.querySelector(`[data-bot-step="${motion.check}"] .bot-step-n`),'draw');
 if(motion.result){const fold=host.querySelector('.bot-result-fold');botFoldOpen(fold);add(fold?.querySelector('.bot-result'),'draw')}
 const hl=host.querySelector('.bot-session-hl');
 if(motion.pick&&hl?.animate&&before.hl!==undefined&&hl.dataset.y!==undefined&&Number(hl.dataset.y)!==before.hl)botTrack(hl.animate([{transform:`translateY(${before.hl}px)`},{transform:`translateY(${hl.dataset.y}px)`}],{duration:botDuration('select'),easing:botEase('flow')}));
 if(error&&!before.error)botFoldOpen(host.querySelector('.bot-message-bar.bot-error'));
 else if(!error&&before.error)botAnimateErrorExit(host,before.error);
}
function botShake(inputs){
 inputs.forEach(input=>{input.classList.remove('bot-shake');input.setAttribute('aria-invalid','true');void input.offsetWidth;input.classList.add('bot-shake')});
 inputs[0]?.focus({preventScroll:true});
}
function botCollapseRow(row){
 if(!row?.animate||botReducedMotion())return Promise.resolve();
 const duration=botDuration('fold'),easing=botEase('fold'),animations=[];
 for(const cell of row.children){
  const style=getComputedStyle(cell),inner=document.createElement('div');inner.className='bot-collapse';inner.append(...cell.childNodes);cell.append(inner);
  animations.push(inner.animate([{height:inner.offsetHeight+'px'},{height:'0px'}],{duration,easing,fill:'forwards'}),cell.animate([{paddingTop:style.paddingTop,paddingBottom:style.paddingBottom},{paddingTop:'0px',paddingBottom:'0px',borderBottomColor:'transparent'}],{duration,easing,fill:'forwards'}));
 }
 animations.push(row.animate([{opacity:1},{opacity:0}],{duration:botDuration('exit'),easing:botEase('exit'),fill:'forwards'}));
 return Promise.allSettled(animations.map(a=>botTrack(a).finished)).then(()=>{});
}

function botResetProject(){
 clearTimeout(botUI.watchTimer);
 Object.assign(botUI,{items:[],deliveries:[],selected:null,sessions:[],query:'',tab:'list',step:1,recordsTab:'deliveries',interactionBotId:'',interaction:null,interactionLoading:false,interactionError:'',testResult:'',flashId:'',motion:null,shownCount:null,watchTimer:0,watchCount:0});
}
function botReconcileSelection(fresh){
 const selected=botUI.selected;
 if(selected?.id){
  const stored=botUI.items.find(b=>b.id===selected.id);
  if(!stored){botUI.selected=null;botUI.step=1;botUI.testResult='';if(botUI.tab==='edit')botUI.tab='list'}
  else if(fresh){botUI.selected=structuredClone(stored);if(!botStepAllowed(botUI.selected,botUI.step))botUI.step=1}
 }
 if(botUI.interactionBotId&&!botUI.items.some(b=>b.id===botUI.interactionBotId)){botUI.interactionBotId='';botUI.interaction=null}
}
async function loadBots(options={}){
 const projectId=state.projectFilter||'',changed=projectId!==botUI.projectId,silent=!!options.silent&&!changed,revision=++botUI.revision;
 if(changed)botResetProject();
 botUI.projectId=projectId;
 if(!silent){botUI.loading=true;botUI.error='';renderBots()}
 if(!projectId){botUI.loading=false;renderBots();return}
 try{
  const [_,bots,deliveries]=await Promise.all([loadBotModels().catch(()=>{}),api(`/api/notification-bots?projectId=${encodeURIComponent(projectId)}`,{silent}),api(`/api/notification-deliveries?projectId=${encodeURIComponent(projectId)}`,{silent})]);
  if(revision!==botUI.revision||state.projectFilter!==projectId)return;
  botUI.items=bots;botUI.deliveries=deliveries;botReconcileSelection(!silent);
  if(!silent)botMotion('rows');
 }catch(e){if(revision===botUI.revision&&!silent)botUI.error=e.message}
 finally{
  if(revision===botUI.revision){
   botUI.loading=false;
   if(!silent&&botUI.tab==='records'&&botUI.recordsTab==='interactions')loadBotInteractions();
   renderBots();botWatchDeliveries();
  }
 }
}
function renderBots(){
 const host=$('#botsContent');if(!host)return;
 const motion=botUI.motion||{};botUI.motion=null;
 const list=host.querySelector('#botSessionList'),hl=list?.querySelector('.bot-session-hl');
 const before={fold:motion.step?host.querySelector('.bot-step-fold')?.offsetHeight||0:0,hl:hl?.dataset.y!==undefined?Number(hl.dataset.y):undefined,sessionScroll:list?.scrollTop||0,error:botUI.shownError};
 botCancelMotion();
 if(botUI.tab==='edit'&&!botUI.selected)botUI.tab='list';
 renderBotHeading();
 if(!state.projectFilter){host.innerHTML='<div class="empty-properties"><b>请先选择项目</b><p>机器人和通知目标按项目管理。</p></div>';botUI.shownError='';return}
 const error=botUI.error?`<div class="bot-message-bar bot-error" role="alert">${escapeHTML(botUI.error)}</div>`:'';
 if(botUI.loading){host.innerHTML=error+botLoadingShell();renderBotIcons(host);bindBots(host);applyBotMotion(host,{},before);return}
 const content=botUI.tab==='edit'?botEditTabHTML(botUI.selected):botUI.tab==='records'?botRecordsHTML():botListHTML(botUI.items);
 host.innerHTML=`${error}<section class="bot-panel" data-bot-panel="${botUI.tab}">${botPanelHeadHTML()}<div class="bot-panel-body">${content}</div></section>`;
 renderBotIcons(host);
 bindBots(host);
 const sessions=host.querySelector('#botSessionList');if(sessions&&before.sessionScroll)sessions.scrollTop=before.sessionScroll;
 applyBotMotion(host,motion,before);
}

// The heading owns the page's solid "添加 Bot"; on an empty list the guide's "开始添加" replaces it.
// The editor has its own primary action, and reopening it here would drop unsaved edits.
function renderBotHeading(){
 const add=$('#botNew');if(!add)return;
 add.hidden=!state.projectFilter||botUI.tab==='edit'||(!botUI.items.length&&!botUI.loading);
 add.disabled=botUI.busy||botUI.loading||!canCreateBot();
 add.onclick=()=>botOpenEditor(null);
}
function botSwitchTab(tab){
 if(!BOT_TABS.includes(tab)||tab===botUI.tab||botUI.busy)return;
 const from=BOT_TABS.indexOf(botUI.tab),to=BOT_TABS.indexOf(tab);
 botUI.tab=tab;botUI.error='';botMotion('tab',to>from?1:-1);
 if(tab==='list'){botMotion('rows');if(botUI.flashId){botMotion('flash',botUI.flashId);botUI.flashId=''}}
 if(tab==='records'){botMotion('rows');if(botUI.recordsTab==='interactions'){botMotion('entries');loadBotInteractions()}}
 renderBots();
}
function botOpenEditor(bot){
 if(botUI.busy||(!bot&&!canCreateBot()))return;
 botUI.selected=bot?structuredClone(bot):{name:'',botId:'',enabled:true,projectId:state.projectFilter};
 Object.assign(botUI,{step:1,sessions:[],query:'',testResult:'',error:''});
 if(botUI.tab==='edit'){botMotion('tab',0);renderBots()}else botSwitchTab('edit');
}
function botGoStep(step,options={}){
 const bot=botUI.selected;if(!bot||botUI.busy||step===botUI.step)return false;
 if(!botStepAllowed(bot,step)){toast(bot.id?'请先选择并保存接收会话':'请先保存凭据','warn');return false}
 if(botUI.step===1&&bot.id&&botCredentialsDirty(bot)){toast('凭据有未保存的修改，请先保存','warn');return false}
 botMotion('step',{dir:step>botUI.step?1:-1,from:botUI.step});botUI.step=step;botUI.testResult='';
 if(options.render!==false)renderBots();
 return true;
}
function botSwitchRecords(tab){
 if(tab===botUI.recordsTab)return;
 botUI.recordsTab=tab;botMotion('records');botMotion('rows');
 if(tab==='interactions'){botMotion('entries');loadBotInteractions()}
 renderBots();
}
function bindBots(host){
 host.querySelectorAll('[data-bot-tab]').forEach(el=>el.onclick=()=>botSwitchTab(el.dataset.botTab));
 host.querySelectorAll('[data-bot-back]').forEach(el=>el.onclick=()=>botSwitchTab('list'));
 host.querySelector('[data-bot-refresh]')?.addEventListener('click',()=>loadBots());
 host.querySelectorAll('[data-bot-new]').forEach(el=>el.onclick=()=>botOpenEditor(null));
 host.querySelectorAll('[data-bot-edit]').forEach(el=>el.onclick=()=>{const bot=botUI.items.find(b=>b.id===el.dataset.botEdit);if(bot)botOpenEditor(bot)});
 host.querySelectorAll('[data-bot-delete]').forEach(el=>el.onclick=()=>{const bot=botUI.items.find(b=>b.id===el.dataset.botDelete);if(bot)botDelete(bot,el.closest('tr'))});
 host.querySelectorAll('[data-bot-step]').forEach(el=>el.onclick=()=>botGoStep(Number(el.dataset.botStep)));
 host.querySelectorAll('[data-bot-records]').forEach(el=>el.onclick=()=>botSwitchRecords(el.dataset.botRecords));
 bindBotRetry(host);
 bindBotEditor(host);
 host.querySelector('#botInteractionBot')?.addEventListener('change',event=>{botUI.interactionBotId=event.target.value;botUI.interaction=null;botMotion('entries');loadBotInteractions();renderBots()});
 host.querySelector('#botRefreshInteractions')?.addEventListener('click',()=>{loadBotInteractions();renderBots()});
}
function captureBotDraft(host){const form=host.querySelector('#botForm');if(!form||!botUI.selected)return;botUI.selected.name=form.elements.name.value;botUI.selected.botId=form.elements.botId.value;botUI.selected.enabled=form.elements.enabled.checked;botUI.selected.model=form.elements.model.value;botUI.selected.interactionEnabled=form.elements.interactionEnabled.checked;botUI.selected._secret=form.elements.secret.value}
function botSaveSelected(saved){const index=botUI.items.findIndex(b=>b.id===saved.id);if(index<0)botUI.items.push(saved);else botUI.items[index]=saved;botUI.selected=structuredClone(saved);botUI.error='';nodeBotCache.delete(botUI.projectId)}
function bindBotEditor(host){
 host.querySelector('#botDelete')?.addEventListener('click',()=>botDelete(botStoredBot(botUI.selected)||botUI.selected,null));
 const form=host.querySelector('#botForm');
 if(form){
  bindBotModelPicker(host);
  form.oninput=event=>{event.target.removeAttribute?.('aria-invalid');captureBotDraft(host)};form.onchange=()=>captureBotDraft(host);
  form.onsubmit=async event=>{
   event.preventDefault();if(botUI.busy)return;captureBotDraft(host);
   const bot=botUI.selected,projectId=botUI.projectId,revision=botUI.revision,creating=!bot.id;
   const missing=['name','botId',...(creating?['secret']:[])].map(name=>form.elements[name]).filter(input=>!input.value.trim());
   if(missing.length){botShake(missing);toast(creating?'请填写名称、Bot ID 和 Secret':'请填写名称和 Bot ID','warn');return}
   const payload={projectId,name:bot.name,botId:bot.botId,enabled:bot.enabled,model:bot.model,interactionEnabled:bot.interactionEnabled,secret:form.elements.secret.value};
   if(bot.id&&!payload.secret&&bot.botId===botStoredBot(bot)?.botId&&bot._targetChanged)payload.targetId=bot.target?.id||'';
   if(bot._targetChanged&&(payload.secret||!bot.id)){toast('请先保存凭据，再刷新并选择目标','warn');return}
   botUI.busy=true;botUI.busyAction='save';renderBots();
   let saved=null;
   try{
    saved=await api(bot.id?`/api/notification-bots/${encodeURIComponent(bot.id)}`:'/api/notification-bots',{method:bot.id?'PUT':'POST',body:JSON.stringify(payload)});
    if(revision!==botUI.revision||projectId!==state.projectFilter){saved=null;return}
    botSaveSelected(saved);if(creating)botUI.flashId=saved.id;toast('机器人设置已保存','ok');
   }catch(e){if(revision===botUI.revision)botUI.error=e.message}
   finally{botUI.busy=false;botUI.busyAction='';if(saved&&botUI.tab==='edit'&&botUI.selected?.id===saved.id&&botGoStep(2,{render:false}))botMotion('check',1);renderBots()}
  };
 }
 host.querySelector('#botSessionSearch')?.addEventListener('input',event=>{botUI.query=event.target.value;host.querySelector('#botSessionList').innerHTML=botSessionHTML();bindBotSessionSelection(host);botPlaceSessionHighlight(host)});
 bindBotSessionSelection(host);
 host.querySelector('#botRefreshSessions')?.addEventListener('click',async()=>{
  if(botUI.busy)return;
  if(botCredentialsDirty(botUI.selected)){toast('请先保存新的凭据','warn');return}
  const id=botUI.selected.id,revision=botUI.revision;botUI.busy=true;botUI.busyAction='sessions';botUI.sessions=[];renderBots();
  try{const result=await api(`/api/notification-bots/${encodeURIComponent(id)}/sessions`);if(revision!==botUI.revision||botUI.selected?.id!==id)return;botUI.sessions=result.sessions;botUI.error='';botUI.selected.health='ready';const stored=botUI.items.find(b=>b.id===id);if(stored)stored.health='ready';botMotion('sessions');toast(`已刷新 ${result.total} 个可发送会话`,'ok')}
  catch(e){if(revision===botUI.revision)botUI.error=e.message}
  finally{botUI.busy=false;botUI.busyAction='';renderBots()}
 });
 host.querySelector('#botClearTarget')?.addEventListener('click',()=>{botUI.selected.target=null;botUI.selected._targetChanged=!!botStoredBot(botUI.selected)?.target;renderBots()});
 host.querySelector('#botSaveTarget')?.addEventListener('click',async()=>{
  const bot=botUI.selected;if(botUI.busy||!bot?.id)return;
  if(!bot._targetChanged){botGoStep(3);return}
  const stored=botStoredBot(bot),projectId=botUI.projectId,revision=botUI.revision;if(!stored)return;
  if(botCredentialsDirty(bot)){toast('请先保存凭据，再刷新并选择目标','warn');return}
  botUI.busy=true;botUI.busyAction='target';renderBots();
  let saved=null;
  try{
   saved=await api(`/api/notification-bots/${encodeURIComponent(bot.id)}`,{method:'PUT',body:JSON.stringify({projectId,name:stored.name,botId:stored.botId,enabled:stored.enabled,model:stored.model,interactionEnabled:stored.interactionEnabled,secret:'',targetId:bot.target?.id||''})});
   if(revision!==botUI.revision||projectId!==state.projectFilter){saved=null;return}
   botSaveSelected(saved);toast(saved.target?'接收会话已保存':'已清除接收会话','ok');
  }catch(e){if(revision===botUI.revision)botUI.error=e.message}
  finally{botUI.busy=false;botUI.busyAction='';if(saved?.target&&botUI.tab==='edit'&&botUI.selected?.id===saved.id&&botGoStep(3,{render:false}))botMotion('check',2);renderBots()}
 });
 host.querySelector('#botSendTest')?.addEventListener('click',async()=>{
  const bot=botUI.selected;if(botUI.busy||!bot?.id)return;
  if(bot._targetChanged||botCredentialsDirty(bot)){toast('请先保存设置，再发送测试通知','warn');return}
  const id=bot.id,projectId=botUI.projectId,revision=botUI.revision;botUI.busy=true;botUI.busyAction='test';botUI.testResult='';renderBots();
  try{const result=await api(`/api/notification-bots/${encodeURIComponent(id)}/test`,{method:'POST'});toast(`测试通知已发送到 ${result.targetName}`,'ok');if(revision===botUI.revision&&botUI.selected?.id===id){botUI.testResult=result.targetName||bot.target?.name||'';botUI.error='';botMotion('result')}try{const records=await api(`/api/notification-deliveries?projectId=${encodeURIComponent(projectId)}`,{silent:true});if(revision===botUI.revision){botUI.deliveries=records;botUI.watchCount=0;botWatchDeliveries()}}catch{}}
  catch(e){if(revision===botUI.revision)botUI.error=e.message}finally{botUI.busy=false;botUI.busyAction='';renderBots()}
 });
}
function bindBotSessionSelection(host){host.querySelectorAll('[name="targetId"]').forEach(el=>el.onchange=()=>{botUI.selected.target=structuredClone(botUI.sessions.find(chat=>chat.id===el.value));botUI.selected._targetChanged=botStoredBot(botUI.selected)?.target?.id!==el.value;botMotion('pick');renderBots()})}
async function botDelete(bot,row){
 if(!bot?.id||botUI.busy||botUI.deletingId===bot.id)return;
 if(!await afConfirm(`确定删除「${bot.name}」？已有节点绑定将无法继续发送。`))return;
 if(botUI.deletingId===bot.id)return;
 const projectId=botUI.projectId,revision=botUI.revision;botUI.deletingId=bot.id;
 try{await botDeleteRequest(bot,row,projectId,revision)}
 finally{if(botUI.deletingId===bot.id)botUI.deletingId=''}
}
async function botDeleteRequest(bot,row,projectId,revision){
 try{await api(`/api/notification-bots/${encodeURIComponent(bot.id)}`,{method:'DELETE'})}
 catch(e){if(revision===botUI.revision){botUI.error=e.message;renderBots()}return}
 nodeBotCache.delete(projectId);toast('机器人已删除','ok');
 if(revision!==botUI.revision)return;
 botUI.error='';
 if(row?.isConnected){row.inert=true;await botCollapseRow(row)}
 botUI.items=botUI.items.filter(b=>b.id!==bot.id);
 if(botUI.selected?.id===bot.id){botUI.selected=null;botUI.step=1;botUI.testResult='';if(botUI.tab==='edit'){botUI.tab='list';botMotion('tab',-1)}}
 if(botUI.interactionBotId===bot.id){botUI.interactionBotId='';botUI.interaction=null}
 renderBots();loadBots({silent:true});
}
function bindBotRetry(host){host.querySelectorAll('[data-delivery-retry]').forEach(el=>el.onclick=()=>botRetryDelivery(el.dataset.deliveryRetry,el))}
async function botRetryDelivery(id,button){
 button.disabled=true;
 try{
  await api(`/api/notification-deliveries/${encodeURIComponent(id)}/retry?projectId=${encodeURIComponent(state.projectFilter)}`,{method:'POST'});toast('已加入重试队列','ok');
  const item=botUI.deliveries.find(d=>d.id===id);if(item){item.status='pending';item.lastError=''}
  const host=$('#botsContent');if(host&&item)botPatchDeliveryRow(host,item);
  botUI.watchCount=0;botWatchDeliveries();
 }catch(e){toast(e.message,'err');button.disabled=false}
}
// Status changes patch the badge in place so its colour can transition instead of being replaced.
function botPatchDeliveryRow(host,item){
 const row=[...host.querySelectorAll('[data-delivery-row]')].find(el=>el.dataset.deliveryRow===item.id);if(!row)return;
 const badge=row.querySelector('[data-delivery-status] .hui-badge');
 if(badge&&badge.querySelector('.hui-badge-label')?.textContent!==botStatusLabel(item.status)){
  const fresh=document.createElement('template');fresh.innerHTML=botStatusBadge(item.status);const next=fresh.content.firstElementChild;
  badge.className=next.className;badge.dataset.tone=next.dataset.tone;badge.replaceChildren(...next.childNodes);renderBotIcons(badge);
 }
 const extra=row.querySelector('[data-delivery-extra]'),action=row.querySelector('[data-delivery-action]'),actionHTML=botDeliveryActionHTML(item);
 if(extra)extra.innerHTML=botDeliveryExtraHTML(item);
 if(action&&action.dataset.html!==actionHTML){action.dataset.html=actionHTML;action.innerHTML=actionHTML;renderBotIcons(action);bindBotRetry(action)}
}
function botWatchDeliveries(){
 clearTimeout(botUI.watchTimer);botUI.watchTimer=0;
 if(!botUI.deliveries.some(d=>['pending','sending'].includes(d.status))||botUI.watchCount>=24)return;
 botUI.watchTimer=setTimeout(async()=>{
  botUI.watchTimer=0;botUI.watchCount++;
  const projectId=botUI.projectId,revision=botUI.revision;
  if(!projectId||!$('#botsView')?.classList.contains('active'))return;
  try{
   const next=await api(`/api/notification-deliveries?projectId=${encodeURIComponent(projectId)}`,{silent:true});
   if(revision!==botUI.revision||projectId!==botUI.projectId)return;
   const same=next.length===botUI.deliveries.length&&next.every((d,i)=>d.id===botUI.deliveries[i].id),host=$('#botsContent');
   botUI.deliveries=next;
   if(same&&host)next.forEach(item=>botPatchDeliveryRow(host,item));else if(botUI.tab==='records'&&!botUI.busy)renderBots();
  }catch{}
  botWatchDeliveries();
 },2500);
}
async function loadBotInteractions(){
 const bots=botInteractionBots(),bot=bots.find(b=>b.id===botUI.interactionBotId)||bots[0];
 if(!bot){botUI.interaction=null;return}
 const revision=botUI.revision;botUI.interactionBotId=bot.id;botUI.interactionLoading=true;botUI.interactionError='';
 const current=()=>revision===botUI.revision&&botUI.interactionBotId===bot.id;
 try{const result=await api(`/api/notification-bots/${encodeURIComponent(bot.id)}/interactions`,{silent:true});if(!current())return;if(!botUI.interaction)botMotion('entries');botUI.interaction=result}
 catch(e){if(current())botUI.interactionError=e.message}
 finally{if(current()){botUI.interactionLoading=false;renderBots()}}
}
