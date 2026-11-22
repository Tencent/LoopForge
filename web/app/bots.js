function loadBotModels(){return window.AgentFlowAssistantModels?.load?.()||Promise.resolve()}
function botModelPickerHTML(){return `<div class="feature-model-picker" id="botModelPicker">${globalThis.AgentFlowAssistantModels?.featurePickerHTML('model')||''}</div>`}
// Project-scoped enterprise chat notifications.
const botUI={projectId:'',items:[],deliveries:[],selected:null,sessions:[],query:'',loading:false,error:'',busy:false,busyAction:'',revision:0,tab:'list',step:1,recordsTab:'deliveries',interactionBotId:'',interaction:null,interactionLoading:false,interactionError:'',testResult:'',flashId:'',motion:null,animations:[],shownCount:null,shownError:'',watchTimer:0,watchCount:0,deletingId:''};
const BOT_TABS=['list','edit','records'],BOT_STEPS=[[1,'填写凭据'],[2,'选择接收会话'],[3,'发送测试']],BOT_TENANT_STEPS=[[1,'填写凭据'],[2,'企业确认'],[3,'通知接收人']];
// Channels: WeCom keeps the three-step notification setup; Feishu replaces
// target selection with enterprise confirmation followed by a bound member
// who passed a trial proactive send.
const BOT_CHANNELS={wecom:{name:'企业微信',app:'Bot ID',secret:'Secret',appHint:'企业微信智能机器人 Bot ID',ready:true},feishu:{name:'飞书',app:'App ID',secret:'App Secret',appHint:'飞书企业自建应用 App ID（cli_ 开头）',ready:true},dingtalk:{name:'钉钉',app:'Client ID',secret:'Client Secret',appHint:'钉钉企业内部应用 Client ID（AppKey）',ready:true}};
const BOT_TENANT={awaiting_confirmation:['企业待确认','warn'],locked:['企业已确认','success'],conflict:['企业冲突','error']};
function botChannel(bot){return BOT_CHANNELS[bot?.channel]?bot.channel:'wecom'}
function botChannelInfo(bot){return BOT_CHANNELS[botChannel(bot)]}
function botSteps(bot){return botChannel(bot)==='wecom'?BOT_STEPS:BOT_TENANT_STEPS}
function botNewKey(){return (crypto.randomUUID?.()||String(Date.now())+Math.random().toString(16).slice(2)).replace(/-/g,'').slice(0,32)}
// In-progress states breathe; terminal states stay still.
const BOT_LIVE_STATES=['pending','sending','connecting','reconnecting','queued','running','ready'];
const BOT_CONNECTION={connected:['已连接','success'],connecting:['连接中','info'],reconnecting:['重连中','info'],error:['连接异常','error'],offline:['未连接','neutral']};
const BOT_MESSAGE={queued:['等待处理','info'],running:['处理中','info'],ready:['等待回复','info'],sending:['发送中','info'],replied:['已回复','success'],reply_failed:['回复失败','error'],reply_unknown:['送达待核对','warn'],cancelled:['已取消','neutral']};
const BOT_TERMINAL_REASON={undelivered_exhausted:'多次发送未成功',rejected:'平台拒绝',delivery_unknown:'平台未确认送达',window_expired:'超过平台去重窗口',route_expired:'会话回复授权已过期',bot_disabled:'机器人已停用',bot_deleted:'机器人已删除',bot_disabled_during_send:'发送中停用',bot_deleted_during_send:'发送中删除'};
function botTypeLabel(type){return type==='group'?'群聊':'单聊'}
function botHealthLabel(health){return ({ready:'消息权限可用',unchecked:'待验证',error:'连接异常',awaiting_authorization:'需要重新授权'})[health]||'待验证'}
function botStatusLabel(status){return ({pending:'等待发送',sending:'发送中',sent:'已发送',failed:'发送失败',send_unknown:'送达待核对',awaiting_authorization:'等待授权',target_unavailable:'会话不可用',cancelled:'已取消',expired:'已过期'})[status]||status}
function filteredBotSessions(items,query){const text=String(query||'').trim().toLocaleLowerCase();return(items||[]).filter(chat=>!text||String(chat.name||'').toLocaleLowerCase().includes(text))}
function canCreateBot(){if(!state.authRequired||!state.currentUser||isPlatformAdmin())return true;const user=Number(state.currentUser.id),project=currentProject();return Number(project?.ownerId)===user||(project?.members||[]).some(m=>Number(m.userId)===user&&m.role!=='viewer')}
function canManageBot(bot){
 if(!state.authRequired||!state.currentUser||isPlatformAdmin())return true;
 const user=Number(state.currentUser.id),project=currentProject();
 return Number(bot?.ownerId)===user||Number(project?.ownerId)===user||(project?.members||[]).some(m=>Number(m.userId)===user&&m.role==='maintainer');
}
function botStepAllowed(bot,step){if(botChannel(bot)!=='wecom')return step===1||(step===2&&!!bot?.id)||(step===3&&!!bot?.id&&bot?.tenantState==='locked');return step===1||(step===2&&!!bot?.id)||(step===3&&!!bot?.id&&!!bot?.target&&!bot._targetChanged)}
function botStepDone(bot,step){if(botChannel(bot)!=='wecom')return step===1?!!bot?.id:step===2?bot?.tenantState==='locked':step===3&&!!bot?.target?.verifiedAt&&!bot._targetChanged;return step===1?!!bot?.id:step===2?botStepAllowed(bot,3):false}
function botStoredBot(bot){return bot?.id?botUI.items.find(b=>b.id===bot.id):null}
function botCredentialsDirty(bot){const stored=botStoredBot(bot);if(!stored)return !!bot;const modelID=value=>(value||'').replace(/^codebuddy:/,'');return !!bot._secret||(bot.name||'')!==(stored.name||'')||(bot.botId||'')!==(stored.botId||'')||modelID(bot.model)!==modelID(stored.model)||!!bot.enabled!==!!stored.enabled||!!bot.interactionEnabled!==!!stored.interactionEnabled||botWriteApproval(bot)!==botWriteApproval(stored)||(bot._tenantKey!==undefined&&bot._tenantKey.trim()!==(stored.tenantExpected||''))||(bot._cardTemplateId!==undefined&&bot._cardTemplateId.trim()!==(stored.cardTemplateId||''))}
// Legacy WeCom records without the field keep automatic execution.
function botWriteApproval(bot){return bot?.writeApproval||(botChannel(bot)==='wecom'&&bot?.id?'auto':'readonly')}

function botIcon(name){return `<span class="bot-icon" data-bot-icon="${name}" aria-hidden="true"></span>`}
// Harness StatusBadge: in-progress states breathe, settled states carry a glyph so colour is never the only cue.
const BOT_BADGE_GLYPHS={success:'check',error:'alert',warn:'user',info:'clock',neutral:'minus'};
const BOT_BADGE_OVERRIDES={unchecked:'clock',cancelled:'close',expired:'clock'};
function botBadgeIcon(tone,state){return BOT_BADGE_OVERRIDES[state]||BOT_BADGE_GLYPHS[tone]||'minus'}
// The vendored Harness set has no plain minus, so it is drawn inline with the same stroke.
function botGlyphHTML(name){return name==='minus'?'<span class="bot-icon hui-badge-glyph" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 12h12"/></svg></span>':`<span class="bot-icon hui-badge-glyph" data-bot-icon="${name}" aria-hidden="true"></span>`}
function botBadge(label,tone,live,state){tone=tone||'neutral';return `<span class="hui-badge" data-size="sm" data-tone="${tone}">${live?'<span class="hui-lamp" aria-hidden="true"><i></i></span>':botGlyphHTML(botBadgeIcon(tone,state))}<span class="hui-badge-label">${escapeHTML(label)}</span></span>`}
function botStatusTone(status){return ({sent:'success',failed:'error',target_unavailable:'error',awaiting_authorization:'warn',send_unknown:'warn',pending:'info',sending:'info'})[status]||'neutral'}
function botStatusBadge(status){return botBadge(botStatusLabel(status),botStatusTone(status),BOT_LIVE_STATES.includes(status),status)}
function botHealthBadge(health){return botBadge(botHealthLabel(health),({ready:'success',error:'error',awaiting_authorization:'warn'})[health]||'neutral',false,health||'unchecked')}
function botEnabledBadge(enabled){return botBadge(enabled?'已启用':'已停用',enabled?'success':'neutral',false)}
function botTenantBadge(state){const [label,tone]=BOT_TENANT[state]||BOT_TENANT.awaiting_confirmation;return botBadge(label,tone,false,state)}
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
 const id=escapeHTML(bot.id),name=escapeHTML(bot.name),manage=canManageBot(bot),wecom=botChannel(bot)==='wecom';
 const target=wecom||bot.target?botTargetHTML(bot.target):`<span class="bot-target">${botTenantBadge(bot.tenantState)}</span>`;
 const model=globalThis.AgentFlowAssistantModels?.concreteModelId(bot.model)??bot.model;
 const sub=[wecom?'':escapeHTML(botChannelInfo(bot).name),escapeHTML(model||''),bot.interactionEnabled?({readonly:'已启用对话（只读）',confirm:'已启用对话（网页确认）'}[botWriteApproval(bot)]||'已启用对话'):''].filter(Boolean).join(' · ');
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
 const steps=botSteps(bot);
 return `<div class="bot-steps" style="--bot-progress:${(step-1)/Math.max(1,steps.length-1)}"><div class="bot-step-track" aria-hidden="true"><i></i></div>${steps.map(([n,label])=>{const done=n!==step&&botStepDone(bot,n);return `<button type="button" class="bot-step${n===step?' active':''}${done?' done':''}" data-bot-step="${n}" ${botStepAllowed(bot,n)&&!botUI.busy?'':'disabled'} ${n===step?'aria-current="step"':''}><span class="bot-step-n">${done?botIcon('check-draw'):n}</span><span class="bot-step-label">${label}</span></button>`}).join('')}</div>`;
}
function botEditTabHTML(bot){
 if(!bot)return botListHTML(botUI.items);
 const manage=!bot.id||canManageBot(bot),step=botUI.step;
 const wecom=botChannel(bot)==='wecom';
 const pane=step===2?(wecom?botTargetPaneHTML(bot,manage):botTenantPaneHTML(bot,manage)):step===3?(wecom?botTestPaneHTML(bot,manage):botPushTargetPaneHTML(bot,manage)):botCredentialPaneHTML(bot,manage);
 return `<div class="bot-editor">${botStepsHTML(bot,step)}<div class="bot-step-fold"><div class="bot-step-pane" data-bot-pane="${step}">${pane}</div></div>${!manage?'<p class="muted bot-hint">仅创建者或项目管理员可修改凭据和接收目标。</p>':''}</div>`;
}
function botChannelFieldHTML(bot){
 if(bot.id)return `<label>渠道<span class="bot-channel-fixed"><span class="bot-chip">${escapeHTML(botChannelInfo(bot).name)}</span><small class="muted">创建后不可修改</small></span></label>`;
 return `<label>渠道<select class="select" name="channel">${Object.entries(BOT_CHANNELS).map(([id,c])=>`<option value="${id}" ${botChannel(bot)===id?'selected':''} ${c.ready?'':'disabled'}>${escapeHTML(c.name)}${c.ready?'':'（开发中）'}</option>`).join('')}</select></label>`;
}
function botWriteFieldHTML(bot){
 const mode=botWriteApproval(bot);
 // Feishu and DingTalk: writes only through web confirmation.
 if(botChannel(bot)!=='wecom')return `<label>执行权限<select class="select" name="writeApproval"><option value="readonly" ${mode==='readonly'?'selected':''}>只读查询（写操作到平台页面完成）</option><option value="confirm" ${mode==='confirm'?'selected':''}>网页确认（发送者在平台页面确认后执行）</option></select></label>`;
 return `<label>执行权限<select class="select" name="writeApproval"><option value="readonly" ${mode==='readonly'?'selected':''}>只读查询（写操作到平台页面完成）</option><option value="confirm" ${mode==='confirm'?'selected':''}>网页确认（发送者在平台页面确认后执行）</option><option value="auto" ${mode==='auto'?'selected':''}>自动执行（沿用发送者权限直接写入）</option></select></label>`;
}
function botCredentialPaneHTML(bot,manage){
 const c=botChannelInfo(bot),plaintext=bot.credentialFormat==='plaintext';
 return `<form id="botForm" novalidate><fieldset ${!manage||botUI.busy?'disabled':''}><div class="bot-fields"><label>名称<input class="input" name="name" required maxlength="80" value="${escapeHTML(bot.name)}" placeholder="例如：研发进展通知"></label>${botChannelFieldHTML(bot)}<label>${escapeHTML(c.app)}<input class="input" name="botId" required maxlength="191" value="${escapeHTML(bot.botId)}" autocomplete="off" placeholder="${escapeHTML(c.appHint)}" ${bot.id&&botChannel(bot)!=='wecom'?'readonly':''}></label><label>${escapeHTML(c.secret)}<input class="input" name="secret" type="password" value="${escapeHTML(bot._secret||'')}" maxlength="512" ${bot.id?'':'required'} autocomplete="new-password" placeholder="${bot.secretConfigured?'已配置 · 留空保留，填写则替换':'填写'+escapeHTML(c.secret)}">${plaintext?'<small class="bot-record-error">Secret 仍为旧格式存储，等待管理员执行加密迁移。</small>':''}</label>${botCorpFieldHTML(bot)}<div class="bot-model-field"><span>执行器与模型</span>${botModelPickerHTML()}<small class="muted">Bot 回答对话和生成完成汇报时使用；对话使用发送者绑定的平台账号凭据，SOP 汇报使用本次执行者凭据。</small></div>${botWriteFieldHTML(bot)}<label class="bot-check"><input name="enabled" type="checkbox" role="switch" ${bot.enabled?'checked':''}>启用此 Bot</label><label class="bot-check"><input name="interactionEnabled" type="checkbox" role="switch" ${bot.interactionEnabled?'checked':''}>启用对话</label></div><p class="muted bot-hint">${botChannel(bot)==='wecom'?'首次对话请按 Bot 提示关联平台账号；新单聊和群聊会自动建立上下文。发送 /new 或“开启新会话”开始新的对话。':botChannel(bot)==='dingtalk'?'处理单聊和群聊中 @ Bot 的文字消息。保存时会校验凭据；首条消息的企业与 CorpId 一致时自动确认企业。':'处理单聊和群聊中 @ Bot 的文字消息。保存时会校验凭据；首次收到消息后，需在第②步确认所属企业。'}</p><div class="bot-save-row"><button type="submit" class="button primary">${botBusyLabel('save','','保存并下一步','保存中…')}</button></div></fieldset></form>`;
}
// DingTalk: the administrator enters the expected CorpId; it can be
// corrected until the first matching message locks it.
function botCorpFieldHTML(bot){
 if(botChannel(bot)!=='dingtalk')return '';
 const locked=bot.tenantState==='locked',value=bot._tenantKey??bot.tenantExpected??'';
 return `<label>企业 CorpId<input class="input" name="tenantKey" ${bot.id?'':'required'} maxlength="191" autocomplete="off" value="${escapeHTML(value)}" placeholder="钉钉管理后台“企业信息”中的 CorpId（ding 开头）" ${locked?'readonly':''}><small class="muted">${locked?'企业已确认，更换企业请新建配置。':'首条单聊消息的企业与此一致时自动确认；不一致时停止处理并提示冲突。'}</small></label><label>互动卡片模板 ID（可选）<input class="input" name="cardTemplateId" maxlength="128" autocomplete="off" value="${escapeHTML(bot._cardTemplateId??bot.cardTemplateId??'')}" placeholder="开发者后台 → 卡片平台中已发布的模板 ID"><small class="muted">填写后，待处理和失败的通知以带按钮的卡片发送，可直接通过、重试或退回；留空则发送文字通知。模板变量见使用手册。</small></label>`;
}
// Enterprise confirmation: the candidate comes from the first trusted event
// of the authenticated connection; the platform query is shown alongside.
function botTenantPaneHTML(bot,manage){
 if(botChannel(bot)==='dingtalk')return botDingTalkTenantPaneHTML(bot,manage);
 const info=botUI.tenant?.botId===bot.id?botUI.tenant:null,candidate=bot.tenantCandidate,state=bot.tenantState||'awaiting_confirmation';
 const queried=info?.queried?`${escapeHTML(info.queried.tenantName||'未返回名称')} · ${escapeHTML(info.queried.tenantKey||'未返回企业标识')}`:info?.queryError?`<span class="bot-record-error">${escapeHTML(info.queryError)}</span>`:'<span class="muted">读取中…</span>';
 const match=candidate&&info?.queried?.tenantKey?(candidate.tenantKey===info.queried.tenantKey?'<span class="bot-chip">与平台查询一致</span>':'<span class="bot-record-error">与平台查询不一致，请核实</span>'):'';
 let body;
 if(state==='locked')body=`<p>已确认企业：<b>${escapeHTML(bot.tenantName||'')}</b> <code>${escapeHTML(bot.tenantKey||'')}</code></p><p class="muted bot-hint">只接收该企业的消息。更换应用或企业请新建配置。</p><div class="bot-save-row"><button type="button" class="button primary" data-bot-step="3" ${botUI.busy?'disabled':''}>下一步：通知接收人</button></div>`;
 else if(state==='conflict')body=`<p class="bot-record-error">收到了两个不同企业的消息：<code>${escapeHTML(candidate?.tenantKey||'')}</code> 与 <code>${escapeHTML(candidate?.conflict||'')}</code>。请核实应用发布范围后重新发现。</p><div class="bot-save-row"><button type="button" class="button ghost" id="botTenantRediscover" ${manage&&!botUI.busy?'':'disabled'}>${botBusyLabel('tenant','refresh','重新发现','处理中…')}</button></div>`;
 else if(!candidate)body='<p>等待第一条消息。请让测试成员在飞书中给机器人发送一条单聊消息，收到后这里会显示候选企业。</p><p class="muted bot-hint">确认前机器人只回复“正在完成企业确认”的提示，不会处理业务。</p>';
 else body=`<p>候选企业标识：<code>${escapeHTML(candidate.tenantKey)}</code> ${match}</p><p class="muted bot-hint">来自 ${escapeHTML(new Date(candidate.receivedAt).toLocaleString())} 收到的消息。确认后只接收该企业的消息。</p><div class="bot-save-row"><button type="button" class="button primary" id="botTenantConfirm" ${manage&&!botUI.busy?'':'disabled'}>${botBusyLabel('tenant','check','确认企业','确认中…')}</button><button type="button" class="button ghost" id="botTenantRediscover" ${manage&&!botUI.busy?'':'disabled'}>重新发现</button></div>`;
 return `<div class="bot-summary"><span class="muted">状态</span><span>${botTenantBadge(state)}</span><span class="muted">平台查询</span><span>${queried}</span></div>${body}<div class="bot-save-row"><button type="button" class="hui-icon-btn" id="botTenantRefresh" title="刷新" aria-label="刷新企业信息">${botIcon('refresh')}</button><button type="button" class="button ghost" data-bot-back ${botUI.busy?'disabled':''}>返回列表</button></div>`;
}
function botDingTalkTenantPaneHTML(bot,manage){
 const state=bot.tenantState||'awaiting_confirmation',candidate=bot.tenantCandidate;
 let body;
 if(state==='locked')body=`<p>已确认企业 <code>${escapeHTML(bot.tenantKey||'')}</code>，只接收该企业的消息。</p><p class="muted bot-hint">更换应用或企业请新建配置。</p><div class="bot-save-row"><button type="button" class="button primary" data-bot-step="3" ${botUI.busy?'disabled':''}>下一步：通知接收人</button></div>`;
 else if(state==='conflict')body=`<p class="bot-record-error">收到的消息来自企业 <code>${escapeHTML(candidate?.conflict||'')}</code>，与填写的 CorpId <code>${escapeHTML(bot.tenantExpected||'')}</code> 不一致，已停止处理。</p><p class="muted bot-hint">核实后在第①步改正 CorpId 并保存，或点“重新发现”重新检查下一条消息。</p><div class="bot-save-row"><button type="button" class="button ghost" id="botTenantRediscover" ${manage&&!botUI.busy?'':'disabled'}>${botBusyLabel('tenant','refresh','重新发现','处理中…')}</button></div>`;
 else body=`<p>等待第一条消息。请让测试成员在钉钉中给机器人发一条单聊消息；企业与 <code>${escapeHTML(bot.tenantExpected||'')}</code> 一致时自动确认，这条消息也会正常处理。</p>`;
 return `<div class="bot-summary"><span class="muted">状态</span><span>${botTenantBadge(state)}</span><span class="muted">填写的 CorpId</span><span><code>${escapeHTML(bot.tenantExpected||'')}</code></span></div>${body}<div class="bot-save-row"><button type="button" class="hui-icon-btn" id="botTenantRefresh" title="刷新" aria-label="刷新企业信息">${botIcon('refresh')}</button><button type="button" class="button ghost" data-bot-back ${botUI.busy?'disabled':''}>返回列表</button></div>`;
}
function botTargetPaneHTML(bot,manage){
 const changed=!!bot._targetChanged;
 return `<fieldset ${!manage||botUI.busy?'disabled':''}><div class="bot-bar"><b>接收会话</b><button type="button" class="button ghost small" id="botRefreshSessions" ${bot.id?'':'disabled'}>${botBusyLabel('sessions','refresh','刷新可发送列表','刷新中…')}</button></div><p class="muted bot-hint">只展示机器人当前可通知的最近会话。找不到目标时，请先在企业微信与 Bot 互动，再刷新。</p><div class="bot-current">${bot.target?`当前目标：<span class="bot-chip">${botTypeLabel(bot.target.type)}</span> ${escapeHTML(bot.target.name)}<button type="button" class="button ghost small" id="botClearTarget">清除目标</button>`:'尚未绑定接收会话'}</div><input class="input" id="botSessionSearch" type="search" placeholder="搜索已获取的联系人或群名" value="${escapeHTML(botUI.query)}"><div id="botSessionList" class="bot-sessions">${botSessionHTML()}</div><div class="bot-save-row"><button type="button" class="button primary" id="botSaveTarget" ${changed||bot.target?'':'disabled'}>${changed?botBusyLabel('target','','保存接收会话','保存中…'):'下一步'}</button></div></fieldset>`;
}
// Feishu/DingTalk notifications go to a member who bound a platform account
// in a private chat with this Bot; saving sends a trial message first.
function botPushTargetPaneHTML(bot,manage){
 const changed=!!bot._targetChanged,verified=!!bot.target?.verifiedAt&&!changed,name=botChannelInfo(bot).name;
 const current=bot.target?`当前接收人：${escapeHTML(bot.target.name)} ${verified?'<span class="bot-chip">试发成功</span>':'<span class="bot-record-error">未验证或已解绑，请重新试发</span>'}<button type="button" class="button ghost small" id="botClearTarget">清除</button>`:'尚未选择通知接收人';
 const result=botUI.testResult?`<div class="bot-result-fold"><div class="bot-result" role="status">${botIcon('check-draw')}<span>试发消息已发送到 ${escapeHTML(botUI.testResult)}，可在「记录 → 发送记录」查看。</span></div></div>`:'';
 return `<fieldset ${!manage||botUI.busy?'disabled':''}><div class="bot-bar"><b>通知接收人</b><button type="button" class="button ghost small" id="botRefreshSessions" ${bot.id?'':'disabled'}>${botBusyLabel('sessions','refresh','刷新成员列表','刷新中…')}</button></div><p class="muted bot-hint">只列出在${escapeHTML(name)}中与本 Bot 单聊并完成账号绑定的成员。保存时先向该成员发送一条试发消息，平台确认送达后才会保存；SOP 的完成、失败和待处理通知会以单聊发送给他。关闭“启用对话”后通知仍可发送。</p><div class="bot-current">${current}</div><input class="input" id="botSessionSearch" type="search" placeholder="搜索成员" value="${escapeHTML(botUI.query)}"><div id="botSessionList" class="bot-sessions">${botSessionHTML()}</div>${result}<div class="bot-save-row"><button type="button" class="button primary" id="botSaveTarget" ${changed?'':'disabled'}>${bot.target?botBusyLabel('target','send','试发并保存','试发中…'):botBusyLabel('target','','保存','保存中…')}</button><button type="button" class="button ghost" id="botSendTest" ${verified&&bot.enabled&&!botUI.busy?'':'disabled'}>${botBusyLabel('test','send','重新试发','发送中…')}</button><button type="button" class="button ghost" data-bot-back ${botUI.busy?'disabled':''}>返回列表</button></div></fieldset>`;
}
function botTestPaneHTML(bot,manage){
 const ready=!!(bot.enabled&&bot.target);
 const result=botUI.testResult?`<div class="bot-result-fold"><div class="bot-result" role="status">${botIcon('check-draw')}<span>测试通知已发送到 ${escapeHTML(botUI.testResult)}，可在「记录 → 发送记录」查看。</span></div></div>`:'';
 return `<div class="bot-summary"><span class="muted">Bot</span><b>${escapeHTML(bot.name)}</b><span class="muted">接收会话</span><span>${bot.target?`${botTypeLabel(bot.target.type)} · ${escapeHTML(bot.target.name)}`:'尚未绑定接收会话'}</span><span class="muted">状态</span><span>${botEnabledBadge(bot.enabled)}</span></div>${bot.enabled?'':'<p class="muted bot-hint">Bot 未启用，请回到第①步启用并保存后再发送测试。</p>'}${result}<div class="bot-save-row"><button type="button" class="button primary" id="botSendTest" ${manage&&ready&&!botUI.busy?'':'disabled'}>${botBusyLabel('test','send','发送测试通知','发送中…')}</button><button type="button" class="button ghost" data-bot-back ${botUI.busy?'disabled':''}>完成，返回列表</button></div>`;
}
function botSessionHTML(){
 const chats=filteredBotSessions(botUI.sessions,botUI.query);
 if(!botUI.sessions.length)return botChannel(botUI.selected)==='wecom'?'<p class="muted bot-empty">点击刷新获取可发送会话；列表为空时，先与 Bot 互动。</p>':'<p class="muted bot-empty">点击刷新获取成员；列表为空时，请成员先在单聊中向 Bot 发送“绑定账号”完成绑定。</p>';
 if(!chats.length)return '<p class="muted bot-empty">没有匹配的会话，尝试其他名称。</p>';
 return '<span class="bot-session-hl" aria-hidden="true"></span>'+chats.map(chat=>`<label class="bot-session"><input type="radio" name="targetId" value="${escapeHTML(chat.id)}" ${botUI.selected?.target?.id===chat.id?'checked':''}><span><b>${escapeHTML(chat.name||'未命名会话')}</b><small>${escapeHTML(chat.lastMessageTime||'')}</small></span><span class="bot-chip">${chat.verifiedAt?'已试发 · ':''}${botTypeLabel(chat.type)}</span></label>`).join('');
}
function botRecordTabsHTML(active){const item=(id,label)=>{const on=id==='interactions'?active==='interactions':active!=='interactions';return `<button type="button" role="tab" aria-selected="${on}" class="${on?'active':''}" data-bot-records="${id}">${label}</button>`};return `<div class="bot-record-tabs" role="tablist" aria-label="记录类型">${item('deliveries','发送记录')}${item('interactions','对话记录')}</div>`}
function botRecordsHTML(){
 const deliveries=botUI.recordsTab!=='interactions';
 return `<div class="bot-subbar">${botRecordTabsHTML(botUI.recordsTab)}${deliveries?'<span class="bot-subbar-hint">保留最近 100 条</span>':''}</div><div class="bot-record-pane">${deliveries?botDeliveryHTML():botInteractionPanelHTML()}</div>`;
}
function botDeliveryExtraHTML(item){return `${item.reportModel?`<small class="bot-source-label">${escapeHTML(item.reportModel)}</small>`:''}${item.reportError?`<small class="bot-record-error">${escapeHTML(item.reportError)}</small>`:''}${item.lastError?`<small class="bot-record-error">${escapeHTML(item.lastError)}</small>`:''}`}
// send_unknown: the administrator checks the chat, then confirms receipt or
// sends again (a new generation and key).
function botDeliveryActionHTML(item){const bot=botUI.items.find(b=>b.id===item.botId);if(!bot||!canManageBot(bot)||item.contentSource==='invalid'||item.contentSource==='purged')return '';const id=escapeHTML(item.id);if(item.status==='send_unknown')return `<button type="button" class="button small hui-btn-soft" data-delivery-confirm="${id}">${botIcon('check')}确认已收到</button><button type="button" class="button small hui-btn-soft" data-delivery-retry="${id}">${botIcon('refresh')}再次发送</button>`;return ['failed','awaiting_authorization','target_unavailable'].includes(item.status)?`<button type="button" class="button small hui-btn-soft" data-delivery-retry="${id}">${botIcon('refresh')}重试</button>`:''}
function botDeliveryHTML(){
 if(!botUI.deliveries.length)return '<p class="bot-empty">暂无发送记录。Agent 节点触发通知后，会在这里展示发送状态。</p>';
 return `<div class="bot-table-wrap"><table class="bot-table bot-record-table"><thead><tr><th>时间</th><th class="bot-col-sub">Bot</th><th class="bot-col-sub">接收会话</th><th>来源 / 触发条件</th><th>状态</th><th></th></tr></thead><tbody>${botUI.deliveries.map(item=>{const bot=botUI.items.find(b=>b.id===item.botId);return `<tr data-delivery-row="${escapeHTML(item.id)}"><td class="bot-nowrap bot-time">${escapeHTML(new Date(item.createdAt).toLocaleString())}</td><td class="bot-col-sub">${escapeHTML(bot?.name||'已删除的 Bot')}</td><td class="bot-col-sub">${escapeHTML(item.targetName)}</td><td>${escapeHTML(item.sourceName||'')}<small class="bot-source-label">${escapeHTML(({schedule_job:'定时任务',assistant_task:'AI 助手任务',run:'流程'})[item.sourceType]||'流程')} · ${escapeHTML(({succeeded:'完成',failed:'失败',waiting:'人工处理',test:'测试通知'})[item.eventType]||item.eventType)}</small>${item.contentSource&&item.contentSource!=='template'?`<small class="bot-source-label">${escapeHTML(({ai:'AI 内容',simulated_ai:'模拟 AI 内容',result_fallback:'执行结果回退',context_fallback:'状态摘要回退',invalid:'内容格式异常',fallback:'标准消息回退',purged:'正文已按保留期清理'})[item.contentSource]||item.contentSource)}</small>`:''}</td><td data-delivery-status>${botStatusBadge(item.status)}<span data-delivery-extra>${botDeliveryExtraHTML(item)}</span></td><td class="bot-nowrap" data-delivery-action>${botDeliveryActionHTML(item)}</td></tr>`}).join('')}</tbody></table></div>`;
}
function botInteractionBots(){return botUI.items.filter(bot=>canManageBot(bot))}
function botInteractionPanelHTML(){
 const bots=botInteractionBots();
 if(!bots.length)return '<p class="bot-empty">暂无可查看对话的 Bot。仅创建者或项目管理员可查看对话记录。</p>';
 const bot=bots.find(b=>b.id===botUI.interactionBotId)||bots[0],data=botUI.interaction,connection=data?.connection?.state||'offline';
 const messages=(data?.messages||[]).filter(m=>m.action!=='notification').slice(0,12);
 const count=data?`<span class="bot-subbar-hint">最近对话 · ${data.conversations?.length||0} 个聊天</span>`:'';
 const body=botUI.interactionError?`<p class="bot-record-error">${escapeHTML(botUI.interactionError)}</p>`:!data&&botUI.interactionLoading?botSkeletonHTML(2):messages.map(m=>`<div class="bot-entry"><div class="bot-entry-meta"><small>${escapeHTML(new Date(m.createdAt).toLocaleString())} · ${escapeHTML(m.senderId||'')}</small>${botMessageBadge(m.status)}</div><p>${m.bodyPurgedAt?'<span class="muted">正文已按保留期清理</span>':escapeHTML(m.content||m.action||'')}</p>${m.terminalReason&&BOT_TERMINAL_REASON[m.terminalReason]?`<small class="bot-source-label">${escapeHTML(BOT_TERMINAL_REASON[m.terminalReason])}${m.receiptSource==='manual'?' · 人工确认':''}</small>`:m.receiptSource==='manual'?'<small class="bot-source-label">人工确认已收到</small>':''}${m.lastError?`<small class="bot-record-error">${escapeHTML(m.lastError)}</small>`:''}${m.reply?`<p class="bot-entry-reply">${escapeHTML(m.reply.slice(0,240))}</p>`:''}${botInteractionActionsHTML(m,bot)}</div>`).join('')||'<p class="bot-empty">还没有对话记录</p>';
 return `<div class="bot-conv"><div class="bot-conv-bar"><span class="bot-interaction-pick"><label><span class="bot-sr">Bot</span><select class="select" id="botInteractionBot" aria-label="选择 Bot">${bots.map(b=>`<option value="${escapeHTML(b.id)}" ${b.id===bot.id?'selected':''}>${escapeHTML(b.name)}</option>`).join('')}</select></label>${botConnectionBadge(connection)}</span><span class="bot-head-spacer"></span>${count}<button type="button" class="hui-icon-btn" id="botRefreshInteractions" title="刷新对话记录" aria-label="刷新对话记录" ${botUI.interactionLoading?'disabled':''}>${botUI.interactionLoading?'<span class="bot-icon bot-spin" data-bot-icon="spinner" aria-hidden="true"></span>':botIcon('refresh')}</button></div>${data?.connection?.lastError?`<p class="bot-record-error">${escapeHTML(data.connection.lastError)}</p>`:''}${body}</div>`;
}
// Manual handling of a reply whose delivery is unknown or failed. The
// action key makes a repeated click return the same result.
function botInteractionActionsHTML(m,bot){
 if(!canManageBot(bot))return '';
 const id=escapeHTML(m.id),buttons=[];
 // Text removed by the retention cleanup cannot be sent again.
 if(m.bodyPurgedAt)return '';
 if(m.status==='reply_unknown')buttons.push(`<button type="button" class="button small hui-btn-soft" data-interaction-confirm="${id}">${botIcon('check')}确认已收到</button>`);
 if((m.status==='reply_unknown'||m.status==='reply_failed')&&m.terminalReason!=='route_expired'&&!String(m.terminalReason||'').startsWith('bot_deleted'))buttons.push(`<button type="button" class="button small hui-btn-soft" data-interaction-resend="${id}">${botIcon('refresh')}再次发送</button>`);
 // An expired DingTalk reply can go out as a proactive private message to
 // its original sender once that member passed a trial send.
 if(botChannel(bot)==='dingtalk'&&m.status==='reply_failed'&&m.terminalReason==='route_expired')buttons.push(m.pushResendAvailable?`<button type="button" class="button small hui-btn-soft" data-interaction-push-resend="${id}">${botIcon('send')}改用主动单聊发送</button>`:`<button type="button" class="button small hui-btn-soft" data-interaction-push-verify="${escapeHTML(m.senderId||'')}">${botIcon('send')}试发给原发送者</button>`);
 return buttons.length?`<div class="bot-entry-actions">${buttons.join('')}</div>`:'';
}
async function botInteractionAction(id,kind,button){
 if(kind==='resend'&&!await afConfirm({message:'再次发送会把已保存的回复重新发给对方，对方可能收到重复消息。确定继续？',confirmText:'再次发送'}))return;
 if(kind==='push-resend'&&!await afConfirm({message:'会话回复授权已过期。改用主动单聊，会把已保存的回复以 Bot 单聊消息发给原发送者。确定继续？',confirmText:'改用主动单聊发送'}))return;
 button.disabled=true;
 try{
  await api(`/api/bot-interactions/${encodeURIComponent(id)}/${kind}`,{method:'POST',body:JSON.stringify({actionKey:botNewKey(),reason:kind==='resend'?'记录页再次发送':kind==='push-resend'?'会话回复已过期，记录页改用主动单聊':''})});
  toast(kind==='confirm'?'已标记为已收到':'已重新加入发送队列','ok');loadBotInteractions();
 }catch(e){toast(e.message,'err');button.disabled=false}
}
async function botVerifySender(sender,button){
 const bot=botInteractionBots().find(b=>b.id===botUI.interactionBotId)||botInteractionBots()[0];if(!bot)return;
 button.disabled=true;
 try{await api(`/api/notification-bots/${encodeURIComponent(bot.id)}/targets/verify`,{method:'POST',body:JSON.stringify({targetId:sender})});toast('试发成功，现在可以改用主动单聊发送','ok');loadBotInteractions()}
 catch(e){toast(e.message,'err');button.disabled=false}
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
 const span=Math.max(1,botSteps(botUI.selected).length-1);
 host.querySelector('.bot-step-pane')?.classList.add(dir>0?'bot-enter-r':'bot-enter-l');
 const fill=host.querySelector('.bot-step-track i'),fold=host.querySelector('.bot-step-fold');
 if(fill?.animate&&from)botTrack(fill.animate([{width:`${(from-1)/span*100}%`},{width:`${(botUI.step-1)/span*100}%`}],{duration:botDuration('indicator'),easing:botEase('spring')}));
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
 Object.assign(botUI,{items:[],deliveries:[],selected:null,sessions:[],query:'',tab:'list',step:1,recordsTab:'deliveries',interactionBotId:'',interaction:null,interactionLoading:false,interactionError:'',testResult:'',flashId:'',motion:null,shownCount:null,watchTimer:0,watchCount:0,tenant:null});
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
 botUI.selected=bot?structuredClone(bot):{name:'',botId:'',channel:'wecom',writeApproval:'readonly',enabled:true,projectId:state.projectFilter};
 Object.assign(botUI,{step:1,sessions:[],query:'',testResult:'',error:''});
 if(botUI.tab==='edit'){botMotion('tab',0);renderBots()}else botSwitchTab('edit');
}
function botGoStep(step,options={}){
 const bot=botUI.selected;if(!bot||botUI.busy||step===botUI.step)return false;
 if(step===2&&botChannel(bot)!=='wecom'&&bot.id)loadBotTenant(bot.id);
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
 host.querySelectorAll('[data-interaction-confirm]').forEach(el=>el.onclick=()=>botInteractionAction(el.dataset.interactionConfirm,'confirm',el));
 host.querySelectorAll('[data-interaction-resend]').forEach(el=>el.onclick=()=>botInteractionAction(el.dataset.interactionResend,'resend',el));
 host.querySelectorAll('[data-interaction-push-resend]').forEach(el=>el.onclick=()=>botInteractionAction(el.dataset.interactionPushResend,'push-resend',el));
 host.querySelectorAll('[data-interaction-push-verify]').forEach(el=>el.onclick=()=>botVerifySender(el.dataset.interactionPushVerify,el));
}
function captureBotDraft(host){const form=host.querySelector('#botForm');if(!form||!botUI.selected)return;const bot=botUI.selected,channel=form.elements.channel?.value;if(channel&&channel!==botChannel(bot)){bot.channel=channel;bot.writeApproval='readonly';botUI.motion=null;queueMicrotask(renderBots)}bot.name=form.elements.name.value;bot.botId=form.elements.botId.value;bot.enabled=form.elements.enabled.checked;bot.model=form.elements.model?.value||bot.model||'';bot.interactionEnabled=form.elements.interactionEnabled.checked;bot._secret=form.elements.secret.value;if(form.elements.writeApproval)bot.writeApproval=form.elements.writeApproval.value;if(form.elements.tenantKey)bot._tenantKey=form.elements.tenantKey.value;if(form.elements.cardTemplateId)bot._cardTemplateId=form.elements.cardTemplateId.value}
function botSaveSelected(saved){const index=botUI.items.findIndex(b=>b.id===saved.id);if(index<0)botUI.items.push(saved);else botUI.items[index]=saved;botUI.selected=structuredClone(saved);botUI.error='';nodeBotCache.delete(botUI.projectId)}
function bindBotEditor(host){
 host.querySelector('#botDelete')?.addEventListener('click',()=>botDelete(botStoredBot(botUI.selected)||botUI.selected,null));
 host.querySelector('#botTenantConfirm')?.addEventListener('click',()=>botTenantAction(false));
 host.querySelector('#botTenantRediscover')?.addEventListener('click',()=>botTenantAction(true));
 host.querySelector('#botTenantRefresh')?.addEventListener('click',()=>{if(botUI.selected?.id)loadBotTenant(botUI.selected.id)});
 const form=host.querySelector('#botForm');
 if(form){
  const ready=window.AgentFlowAssistantModels?.bindFeaturePicker(form.querySelector('#botModelPicker'),{
    model:botUI.selected?.model,keepSaved:true,readOnly:form.querySelector('fieldset').disabled
  });
  form.oninput=event=>{event.target.removeAttribute?.('aria-invalid');captureBotDraft(host)};form.onchange=()=>captureBotDraft(host);
  form.onsubmit=async event=>{
   event.preventDefault();
   await ready;
   if(!form.isConnected||botUI.busy)return;
   captureBotDraft(host);
   const bot=botUI.selected,projectId=botUI.projectId,revision=botUI.revision,creating=!bot.id;
   const dingtalk=botChannel(bot)==='dingtalk';
   const missing=['name','botId',...(creating?['secret']:[]),...(creating&&dingtalk?['tenantKey']:[])].map(name=>form.elements[name]).filter(input=>input&&!input.value.trim());
   if(missing.length){botShake(missing);const c=botChannelInfo(bot);toast(creating?`请填写名称、${c.app}、${c.secret}${dingtalk?' 和企业 CorpId':''}`:`请填写名称和 ${c.app}`,'warn');return}
   if(bot.interactionEnabled&&!botStoredBot(bot)?.interactionEnabled&&!window.AgentFlowAssistantModels?.concreteModelId(bot.model)){
    form.querySelector('#botModelPicker [data-feature-model]')?.focus();
    toast('请先选择模型','warn');return;
   }
   const stored=botStoredBot(bot);
   const payload={projectId,name:bot.name,channel:botChannel(bot),botId:bot.botId,enabled:bot.enabled,model:bot.model,interactionEnabled:bot.interactionEnabled,writeApproval:botWriteApproval(bot),secret:form.elements.secret.value};
   if(stored)payload.configVersion=stored.configVersion;
   if(dingtalk){payload.tenantKey=(form.elements.tenantKey?.value||'').trim();payload.cardTemplateId=(form.elements.cardTemplateId?.value||'').trim()}
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
  let saved=null;const wecom=botChannel(stored)==='wecom';
  const put=pendingTargetAction=>api(`/api/notification-bots/${encodeURIComponent(bot.id)}`,{method:'PUT',body:JSON.stringify({projectId,name:stored.name,channel:botChannel(stored),botId:stored.botId,enabled:stored.enabled,model:stored.model,interactionEnabled:stored.interactionEnabled,writeApproval:botWriteApproval(stored),secret:'',targetId:bot.target?.id||'',configVersion:stored.configVersion,pendingTargetAction})});
  try{
   try{saved=await put('')}
   catch(e){
    if(e.code!=='pending_deliveries')throw e;
    // Queued notifications of the previous recipient need an explicit choice.
    const action=await afConfirm({title:'原接收人还有待发通知',message:e.message+'。改发后这些通知会发给新接收人。',confirmText:'改发到新接收人',cancelText:'不改发'})?'migrate':await afConfirm({danger:true,title:'取消待发通知',message:'不改发时，原接收人的待发通知会被取消，并在发送记录中注明原因。',confirmText:'取消这些通知并切换',cancelText:'返回'})?'cancel':'';
    if(!action)return;
    saved=await put(action);
   }
   if(revision!==botUI.revision||projectId!==state.projectFilter){saved=null;return}
   botSaveSelected(saved);toast(saved.target?(wecom?'接收会话已保存':`试发成功，已保存接收人 ${saved.target.name}`):(wecom?'已清除接收会话':'已清除接收人'),'ok');
   if(!wecom&&saved.target)botUI.testResult=saved.target.name;
  }catch(e){if(revision===botUI.revision)botUI.error=e.message}
  finally{botUI.busy=false;botUI.busyAction='';if(wecom&&saved?.target&&botUI.tab==='edit'&&botUI.selected?.id===saved.id&&botGoStep(3,{render:false}))botMotion('check',2);renderBots()}
 });
 host.querySelector('#botSendTest')?.addEventListener('click',async()=>{
  const bot=botUI.selected;if(botUI.busy||!bot?.id)return;
  if(bot._targetChanged||botCredentialsDirty(bot)){toast('请先保存设置，再发送测试通知','warn');return}
  const id=bot.id,projectId=botUI.projectId,revision=botUI.revision;botUI.busy=true;botUI.busyAction='test';botUI.testResult='';renderBots();
  try{const result=await api(`/api/notification-bots/${encodeURIComponent(id)}/test`,{method:'POST'});toast(`测试通知已发送到 ${result.targetName}`,'ok');if(revision===botUI.revision&&botUI.selected?.id===id){botUI.testResult=result.targetName||bot.target?.name||'';botUI.error='';botMotion('result')}try{const records=await api(`/api/notification-deliveries?projectId=${encodeURIComponent(projectId)}`,{silent:true});if(revision===botUI.revision){botUI.deliveries=records;botUI.watchCount=0;botWatchDeliveries()}}catch{}}
  catch(e){if(revision===botUI.revision)botUI.error=e.message}finally{botUI.busy=false;botUI.busyAction='';renderBots()}
 });
}
async function loadBotTenant(id){
 const revision=botUI.revision;
 try{const info=await api(`/api/notification-bots/${encodeURIComponent(id)}/tenant`,{silent:true});if(revision!==botUI.revision||botUI.selected?.id!==id)return;botUI.tenant={...info,botId:id};const bot=botUI.selected;bot.tenantState=info.tenantState;bot.tenantCandidate=info.candidate;const stored=botStoredBot(bot);if(stored){stored.tenantState=info.tenantState;stored.tenantCandidate=info.candidate;stored.configVersion=info.configVersion}}
 catch(e){if(revision===botUI.revision)botUI.tenant={botId:id,queryError:e.message}}
 finally{if(revision===botUI.revision)renderBots()}
}
async function botTenantAction(rediscover){
 const bot=botUI.selected,stored=botStoredBot(bot);if(botUI.busy||!bot?.id||!stored)return;
 const projectId=botUI.projectId,revision=botUI.revision;botUI.busy=true;botUI.busyAction='tenant';renderBots();
 try{
  const saved=await api(`/api/notification-bots/${encodeURIComponent(bot.id)}/tenant/confirm`,{method:'POST',body:JSON.stringify({tenantKey:bot.tenantCandidate?.tenantKey||'',configVersion:stored.configVersion,rediscover})});
  if(revision!==botUI.revision||projectId!==state.projectFilter)return;
  botSaveSelected(saved);toast(rediscover?'已重新开始企业发现':'企业已确认','ok');
 }catch(e){if(revision===botUI.revision)botUI.error=e.message}
 finally{botUI.busy=false;botUI.busyAction='';renderBots()}
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
function bindBotRetry(host){host.querySelectorAll('[data-delivery-retry]').forEach(el=>el.onclick=()=>botRetryDelivery(el.dataset.deliveryRetry,el));host.querySelectorAll('[data-delivery-confirm]').forEach(el=>el.onclick=()=>botConfirmDelivery(el.dataset.deliveryConfirm,el))}
async function botConfirmDelivery(id,button){
 button.disabled=true;
 try{
  await api(`/api/notification-deliveries/${encodeURIComponent(id)}/confirm?projectId=${encodeURIComponent(state.projectFilter)}`,{method:'POST'});toast('已记录为收到','ok');
  const item=botUI.deliveries.find(d=>d.id===id);if(item){item.status='sent';item.receiptSource='manual';item.lastError='管理员已确认收到'}
  const host=$('#botsContent');if(host&&item)botPatchDeliveryRow(host,item);
 }catch(e){toast(e.message,'err');button.disabled=false}
}
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
