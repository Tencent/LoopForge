// 知识库模块：接入配置 / 节点使用 / 知识沉淀 / 检索试用。
// 依赖 app.js 的全局 state、api、$、$$、escapeHTML、toast、relativeTime、switchView。
const kb={tab:'setup',settings:null,sources:null,sourcesError:'',usage:null,candidates:[],submissions:[],subStatus:'pending',selected:null,draft:null,search:{query:'',sources:[],result:null,loading:false,error:''},loading:false};

const KB_TOOLS=[
  ['kb_search','按问题或关键词混合检索，结果带 source / commit / 行号'],
  ['kb_fetch','按 source + path + 行号读取 Markdown 原文'],
  ['kb_list_sources','列出当前项目可用的知识库来源'],
  ['kb_get_source','查看来源状态与文档清单'],
];

function kbProjectId(){return state.projectFilter||''}
function kbProjectName(){const p=(state.data?.projects||[]).find(x=>x.id===kbProjectId());return p?.name||'全部项目'}
function kbConsoleURL(){const u=kb.settings?.url||'';return u.replace(/\/mcp\/?$/,'/')}
function kbAllowed(){const s=kb.settings||{};const own=s.projectSources?.[kbProjectId()];return own!==undefined?own:(s.defaultSources||[])}
function kbHasOwnScope(){return kb.settings?.projectSources?.[kbProjectId()]!==undefined}
function kbStatusPill(status){const map={pending:['待审核','warn'],published:['已写回','ok'],rejected:['已驳回','err']};const [t,c]=map[status]||[status||'-',''];return `<span class="kb-pill ${c}">${escapeHTML(t)}</span>`}
function kbAgentName(id){return (state.data?.agents||[]).find(a=>a.id===id)?.name||id||'-'}

async function renderKnowledgeView(){
  const root=$('#knowledgeBody');
  if(!root)return;
  if(!kb.settings)root.innerHTML='<div class="kb-empty">正在加载知识库配置…</div>';
  await kbRefresh();
  kbRender();
}

async function kbRefresh(){
  const pid=encodeURIComponent(kbProjectId());
  const [settings,usage,subs,cands]=await Promise.all([
    api('/api/knowledge/settings',{silent:true}).catch(err=>({error:err.message})),
    api(`/api/knowledge/usage?projectId=${pid}`,{silent:true}).catch(()=>null),
    api(`/api/knowledge/submissions?projectId=${pid}`,{silent:true}).catch(()=>[]),
    api(`/api/knowledge/candidates?projectId=${pid}`,{silent:true}).catch(()=>[]),
  ]);
  kb.settings=settings;kb.usage=usage;kb.submissions=subs||[];kb.candidates=cands||[];
  if(settings?.ready){
    try{kb.sources=await api(`/api/knowledge/sources?projectId=${pid}`,{silent:true});kb.sourcesError=''}
    catch(err){kb.sources=null;kb.sourcesError=err.message}
  }else{kb.sources=null;kb.sourcesError=''}
}

function kbRender(){
  const root=$('#knowledgeBody');
  if(!root)return;
  const s=kb.settings||{};
  const pending=kb.submissions.filter(x=>x.status==='pending').length;
  const fresh=kb.candidates.filter(c=>!c.submissionId).length;
  const allowed=kbAllowed();
  const sourceCount=kb.sources?kb.sources.filter(x=>x.allowed).length:0;
  const conn=s.ready?(kb.sourcesError?['连接异常','bad']:['已接入','good']):(s.tokenConfigured||s.url?['未启用','warn']:['未接入','warn']);
  const link=$('#kbConsoleLink');
  if(link){link.hidden=!s.url;link.href=kbConsoleURL()}
  root.innerHTML=`
    <div class="kb-summary">
      <div class="kb-summary-main"><span>📚</span><div><b>团队知识库 · ${escapeHTML(kbProjectName())}</b><small>${escapeHTML(s.url||'尚未配置知识库地址')}</small></div></div>
      <div class="kb-fact ${conn[1]}"><span>连接状态</span><b>${conn[0]}</b></div>
      <div class="kb-fact"><span>本项目可用来源</span><b>${s.ready&&kb.sources?sourceCount:'-'}</b></div>
      <div class="kb-fact"><span>已接入角色</span><b>${kb.usage?.agents?.length??0}<small style="font-size:11px;color:var(--muted-2)"> / ${kb.usage?.totalAgents??0}</small></b></div>
      <div class="kb-fact ${pending?'warn':''}"><span>待审核沉淀</span><b>${pending}</b></div>
    </div>
    <div class="kb-tabs" id="kbTabs">
      <button type="button" data-kb-tab="setup" class="${kb.tab==='setup'?'active':''}">接入配置</button>
      <button type="button" data-kb-tab="usage" class="${kb.tab==='usage'?'active':''}">节点使用</button>
      <button type="button" data-kb-tab="distill" class="${kb.tab==='distill'?'active':''}">知识沉淀${pending+fresh?`<em>${pending+fresh}</em>`:''}</button>
      <button type="button" data-kb-tab="search" class="${kb.tab==='search'?'active':''}">检索试用</button>
    </div>
    <div id="kbTabBody"></div>`;
  root.querySelectorAll('[data-kb-tab]').forEach(b=>b.onclick=()=>{kb.tab=b.dataset.kbTab;kbRender()});
  const body=$('#kbTabBody');
  ({setup:kbRenderSetup,usage:kbRenderUsage,distill:kbRenderDistill,search:kbRenderSearch}[kb.tab]||kbRenderSetup)(body,allowed);
}

// ---------- 接入配置 ----------
function kbRenderSetup(body){
  const s=kb.settings||{};
  const ro=!s.canManage;
  body.innerHTML=`
    <div class="kb-grid">
      <div class="kb-stack">
        <form class="kb-card kb-form" id="kbSettingsForm">
          <div class="kb-card-title"><i>①</i>连接团队知识库<small>平台级 · 管理员配置一次</small></div>
          ${ro?'<div class="kb-readonly">只有平台管理员可以修改接入配置；你可以查看状态并在「节点使用」中为角色开启知识库。</div>':''}
          <p class="kb-hint">填写知识库的 MCP 地址和 Token。Token 只保存在平台服务端，<b>不会下发给 Agent 或执行机器</b>；Agent 运行时拿到的是平台签发的短期只读令牌。</p>
          <label>知识库 MCP 地址</label>
          <input type="url" name="url" placeholder="http://kb.example.com:8080/mcp" value="${escapeHTML(s.url||'')}" ${ro?'disabled':''}>
          <label>MCP Token ${s.tokenConfigured?'<span class="kb-pill ok">已配置</span>':'<span class="kb-pill warn">未配置</span>'}</label>
          <input type="password" name="token" autocomplete="new-password" placeholder="${s.tokenConfigured?'已配置，留空保持不变；填写则替换':'mcp_xxx（由知识库管理员申请）'}" ${ro?'disabled':''}>
          <label class="kb-toggle"><input type="checkbox" name="enabled" ${s.enabled?'checked':''} ${ro?'disabled':''}><div><b>启用知识库</b><small>启用后，开启了「接入团队知识库」的角色在运行时会自动获得只读检索工具和使用规范。</small></div></label>
          <label class="kb-toggle"><input type="checkbox" name="writeEnabled" ${s.writeEnabled?'checked':''} ${ro?'disabled':''}><div><b>允许审核后写回</b><small>打开后，「知识沉淀」中审核通过的条目会通过 kb_write_document 提交到知识库 Git 仓库并触发重建索引。Agent 始终不能直接写入。</small></div></label>
          <div class="kb-actions">
            ${ro?'':'<button type="button" class="button ghost" id="kbCheckBtn">检查连接</button><button type="submit" class="button primary">保存配置</button>'}
            <span class="kb-result" id="kbCheckResult">${s.updatedAt&&s.updatedBy?`上次由 ${escapeHTML(s.updatedBy)} 于 ${relativeTime(s.updatedAt)} 更新`:''}</span>
          </div>
        </form>
        <div class="kb-card">
          <div class="kb-card-title"><i>②</i>本项目可用的知识库<small>${escapeHTML(kbProjectName())}</small></div>
          <div id="kbScopeBody"></div>
        </div>
      </div>
      <div class="kb-card">
        <div class="kb-card-title"><i>?</i>接入后怎么工作</div>
        <ol class="kb-flow">
          <li><i>1</i><div><b>管理员连接知识库</b><p>平台保存知识库地址和 Token，并按项目限定可访问的知识库来源。</p></div></li>
          <li><i>2</i><div><b>角色开启「接入团队知识库」</b><p>在「角色定义 → 能力」中打开开关。节点运行时平台自动注入 <code>团队知识库</code> MCP 和一条「知识库使用规范」规则。</p></div></li>
          <li><i>3</i><div><b>Agent 只读检索</b><p>只开放 <code>kb_search</code>、<code>kb_fetch</code>、<code>kb_list_sources</code>、<code>kb_get_source</code>，并自动限定在本项目授权的来源内。</p></div></li>
          <li><i>4</i><div><b>新知识写 knowledge.md</b><p>Agent 把可复用的新知识写到工作区 <code>knowledge.md</code>，运行结束后出现在「知识沉淀」候选中。</p></div></li>
          <li><i>5</i><div><b>人工审核后写回</b><p>审核人编辑标题、目标来源和路径，确认后平台写回知识库并记录 commit。</p></div></li>
        </ol>
      </div>
    </div>`;
  kbRenderScope($('#kbScopeBody'));
  const form=$('#kbSettingsForm');
  form.onsubmit=async e=>{
    e.preventDefault();
    const f=form.elements;
    try{
      const saved=await api('/api/knowledge/settings',{method:'PUT',body:JSON.stringify({...kbSettingsPayload(),enabled:f.enabled.checked,url:f.url.value.trim(),token:f.token.value.trim(),writeEnabled:f.writeEnabled.checked})});
      kb.settings=saved;toast('知识库配置已保存','ok');await renderKnowledgeView();
    }catch(err){toast(err.message,'err')}
  };
  const check=$('#kbCheckBtn');
  if(check)check.onclick=async()=>{
    const out=$('#kbCheckResult');out.className='kb-result';out.textContent='检查中…';
    try{
      const r=await api('/api/knowledge/check',{method:'POST',body:JSON.stringify({url:form.elements.url.value.trim(),token:form.elements.token.value.trim()})});
      if(r.ok){out.className='kb-result ok';out.textContent=`✓ 连接成功 · ${r.latencyMs}ms · ${r.sources.length} 个来源 · 工具：${(r.tools||[]).join(', ')}`}
      else{out.className='kb-result err';out.textContent=`✗ ${r.error}`}
    }catch(err){out.className='kb-result err';out.textContent=`✗ ${err.message}`}
  };
}

function kbSettingsPayload(){
  const s=kb.settings||{};
  return {enabled:!!s.enabled,url:s.url||'',writeEnabled:!!s.writeEnabled,defaultSources:s.defaultSources||[],projectSources:s.projectSources||{}};
}

function kbRenderScope(host){
  const s=kb.settings||{};
  if(!s.ready){host.innerHTML='<div class="kb-empty">启用并保存连接后，这里会列出知识库中的全部来源，可按项目勾选授权范围。</div>';return}
  if(kb.sourcesError){host.innerHTML=`<div class="kb-empty">知识库暂时不可达：${escapeHTML(kb.sourcesError)}</div>`;return}
  const list=kb.sources||[];
  const own=kbHasOwnScope();
  const pid=kbProjectId();
  const ro=!s.canManage||!pid;
  const allowed=new Set(kbAllowed());
  host.innerHTML=`
    <p class="kb-hint">${pid?'授权范围决定本项目的 Agent 和检索试用能看到哪些来源；写回时也只能写入授权来源。':'请先在顶部切换到具体项目，再设置项目级授权范围。'}</p>
    <div class="kb-scope">
      <button type="button" data-kb-scope="all" class="${!own?'active':''}" ${ro?'disabled':''}>跟随平台默认（${(s.defaultSources||[]).length?`${s.defaultSources.length} 个来源`:'全部来源'}）</button>
      <button type="button" data-kb-scope="custom" class="${own?'active':''}" ${ro?'disabled':''}>为本项目指定来源</button>
    </div>
    <div class="kb-source-list">${list.map(src=>{
      const on=allowed.size===0||allowed.has(src.id);
      const st=src.status==='ready'?'<span class="kb-pill ok">就绪</span>':`<span class="kb-pill err">${escapeHTML(src.status||'-')}</span>`;
      return `<div class="kb-source ${on?'on':''} ${own&&!ro?'':'disabled'}" data-kb-source="${escapeHTML(src.id)}"><input type="checkbox" ${on?'checked':''} ${own&&!ro?'':'disabled'}><div><b>${escapeHTML(src.id)}</b><small>${src.doc_count} 篇文档</small></div>${st}</div>`;
    }).join('')||'<div class="kb-empty">知识库没有返回任何来源</div>'}</div>`;
  const persist=async next=>{
    const payload=kbSettingsPayload();
    payload.projectSources={...payload.projectSources};
    if(next===null)delete payload.projectSources[pid];else payload.projectSources[pid]=next;
    try{kb.settings=await api('/api/knowledge/settings',{method:'PUT',body:JSON.stringify(payload)});await kbRefresh();kbRender();toast('项目授权范围已更新','ok')}catch(err){toast(err.message,'err')}
  };
  host.querySelectorAll('[data-kb-scope]').forEach(b=>b.onclick=()=>{
    if(b.dataset.kbScope==='all'&&own)persist(null);
    if(b.dataset.kbScope==='custom'&&!own)persist(list.filter(x=>x.status==='ready').map(x=>x.id));
  });
  if(own&&!ro)host.querySelectorAll('[data-kb-source]').forEach(el=>el.onclick=()=>{
    const id=el.dataset.kbSource;const cur=new Set(kbAllowed());
    if(cur.has(id))cur.delete(id);else cur.add(id);
    if(!cur.size)return toast('至少保留一个来源；不限制请选「跟随平台默认」');
    persist([...cur]);
  });
}

// ---------- 节点使用 ----------
function kbRenderUsage(body){
  const s=kb.settings||{};
  const usage=kb.usage||{agents:[],activity:[]};
  const enabled=new Set(usage.agents.map(a=>a.id));
  const pid=kbProjectId();
  const candidates=(state.data?.agents||[]).filter(a=>!enabled.has(a.id)&&(!pid||a.projectId===pid)).slice(0,12);
  const rule=s.usageRule||'';
  body.innerHTML=`
    ${s.ready?'':'<div class="kb-readonly">知识库尚未启用：角色开关可以先打开，但在管理员完成「接入配置」前运行时不会注入知识库工具。</div>'}
    <div class="kb-grid">
      <div class="kb-stack">
        <div class="kb-card">
          <div class="kb-card-title"><i>⚙</i>三步让节点用上知识库</div>
          <ol class="kb-flow">
            <li><i>1</i><div><b>打开角色开关</b><p>「角色定义 → 编辑角色 → 能力 → 平台能力」中勾选 <b>接入团队知识库</b>；或直接在下方列表一键开启。</p></div></li>
            <li><i>2</i><div><b>把角色放进流程节点</b><p>在「流程设计」里，任何使用该角色的 Agent 节点都会自动生效，不需要逐个节点配置 MCP。</p></div></li>
            <li><i>3</i><div><b>运行即注入</b><p>节点启动时平台签发 48 小时有效、限定本项目的只读令牌，注入 MCP <code>团队知识库</code> 和规则「知识库使用规范」。</p></div></li>
          </ol>
          <div class="kb-actions"><button type="button" class="button ghost" id="kbGoAgents">去角色定义</button><button type="button" class="button ghost" id="kbGoFlow">去流程设计</button></div>
        </div>
        <div class="kb-card">
          <div class="kb-card-title"><i>✓</i>已接入知识库的角色<small>${usage.agents.length} 个</small></div>
          ${usage.agents.length?`<table class="kb-table"><thead><tr><th>角色</th><th>使用它的流程</th><th></th></tr></thead><tbody>${usage.agents.map(a=>`<tr><td><b>${escapeHTML(a.name)}</b>${a.shared?' <span class="kb-pill blue">共享</span>':''}<br><small class="mono">${escapeHTML(a.id)}</small></td><td>${a.workflows.length?a.workflows.map(w=>`<span class="kb-pill">${escapeHTML(w)}</span>`).join(' '):'<small>尚未放进任何流程</small>'}</td><td style="text-align:right"><button type="button" class="button ghost small" data-kb-toggle="${escapeHTML(a.id)}" data-kb-on="0">关闭</button></td></tr>`).join('')}</tbody></table>`:'<div class="kb-empty">还没有角色接入知识库。在下方为需要查资料的角色（需求澄清、技术方案、排障、评审）一键开启。</div>'}
          ${candidates.length?`<div class="kb-section-label">可以开启的角色</div><table class="kb-table"><tbody>${candidates.map(a=>`<tr><td><b>${escapeHTML(a.name)}</b><br><small class="mono">${escapeHTML(a.id)}</small></td><td>${escapeHTML((a.description||'').slice(0,60))}</td><td style="text-align:right"><button type="button" class="button primary small" data-kb-toggle="${escapeHTML(a.id)}" data-kb-on="1">开启</button></td></tr>`).join('')}</tbody></table>`:''}
        </div>
      </div>
      <div class="kb-stack">
        <div class="kb-card">
          <div class="kb-card-title"><i>⇄</i>运行时注入内容</div>
          <p class="kb-hint">MCP 名称 <code>团队知识库</code>（id <code>agentflow-knowledge</code>），经平台代理访问知识库：</p>
          <div class="kb-tools">${KB_TOOLS.map(([n,d])=>`<div class="kb-tool"><code>${n}</code><p>${d}</p></div>`).join('')}<div class="kb-tool off"><code>kb_write_document</code><p>不对 Agent 开放，写回只能经「知识沉淀」审核</p></div></div>
          <details class="kb-rule"><summary>查看自动注入的规则「知识库使用规范」</summary><pre>${escapeHTML(rule)}</pre></details>
        </div>
        <div class="kb-card">
          <div class="kb-card-title"><i>◷</i>最近的 Agent 检索<small>平台重启后清空</small></div>
          ${(usage.activity||[]).length?`<table class="kb-table"><thead><tr><th>时间</th><th>角色</th><th>工具 / 查询</th><th>结果</th></tr></thead><tbody>${usage.activity.slice(0,20).map(x=>`<tr><td>${relativeTime(x.at)}</td><td>${escapeHTML(kbAgentName(x.agentId))}</td><td><code>${escapeHTML(x.tool)}</code> ${escapeHTML(x.query||'')}</td><td>${x.ok?`<span class="kb-pill ok">${x.tool==='kb_search'?`${x.hits} 条`:'成功'}</span>`:`<span class="kb-pill err" title="${escapeHTML(x.error||'')}">失败</span>`} <small>${x.latencyMs}ms</small></td></tr>`).join('')}</tbody></table>`:'<div class="kb-empty">还没有 Agent 调用过知识库。开启角色并运行一次流程后，这里会显示检索记录。</div>'}
        </div>
      </div>
    </div>`;
  $('#kbGoAgents').onclick=()=>switchView('agents');
  $('#kbGoFlow').onclick=()=>switchView('flow');
  body.querySelectorAll('[data-kb-toggle]').forEach(b=>b.onclick=()=>kbToggleAgent(b.dataset.kbToggle,b.dataset.kbOn==='1'));
}

async function kbToggleAgent(id,on){
  try{
    const full=await api(`/api/agents/${encodeURIComponent(id)}`);
    const next={...full,knowledgeBase:on,version:(full.version||0)+1};
    await api('/api/agents',{method:'PUT',body:JSON.stringify(next)});
    const local=(state.data?.agents||[]).find(a=>a.id===id);
    if(local)local.knowledgeBase=on;
    toast(on?`已为「${full.name}」开启知识库`:`已关闭「${full.name}」的知识库`,'ok');
    await kbRefresh();kbRender();
  }catch(err){toast(err.message,'err')}
}

// ---------- 知识沉淀 ----------
function kbRenderDistill(body){
  const s=kb.settings||{};
  const fresh=kb.candidates.filter(c=>!c.submissionId);
  const subs=kb.submissions.filter(x=>x.status===kb.subStatus);
  const counts={pending:0,published:0,rejected:0};kb.submissions.forEach(x=>counts[x.status]=(counts[x.status]||0)+1);
  body.innerHTML=`
    ${s.writeEnabled?'':'<div class="kb-readonly">平台尚未开启「允许审核后写回」：可以先整理和审核条目，开启后才能写回知识库。</div>'}
    <div class="kb-sub-layout">
      <div class="kb-card">
        <div class="kb-card-title"><i>↥</i>沉淀队列<small><button type="button" class="button ghost small" id="kbNewSub">＋ 手动新建</button></small></div>
        <div class="kb-section-label">运行产出的候选（knowledge.md）</div>
        <div class="kb-list">${fresh.length?fresh.map(c=>`<div class="kb-item" data-kb-cand="${escapeHTML(c.artifact.id)}"><div class="kb-item-top"><span class="kb-pill blue">候选</span><b>${escapeHTML(c.artifact.path||c.artifact.name)}</b></div><small>运行 ${escapeHTML(c.artifact.runId)} · 节点 ${escapeHTML(c.artifact.nodeId)} · ${relativeTime(c.artifact.createdAt)}</small><button type="button" class="button ghost small" data-kb-queue="${escapeHTML(c.artifact.runId)}|${escapeHTML(c.artifact.id)}">加入审核</button></div>`).join(''):'<div class="kb-empty">暂无新的候选。开启知识库的角色在运行中写出 knowledge.md 后会出现在这里。</div>'}</div>
        <div class="kb-section-label">审核条目</div>
        <div class="kb-subtabs">${[['pending','待审核'],['published','已写回'],['rejected','已驳回']].map(([k,t])=>`<button type="button" data-kb-status="${k}" class="${kb.subStatus===k?'active':''}">${t} ${counts[k]||0}</button>`).join('')}</div>
        <div class="kb-list">${subs.length?subs.map(x=>`<div class="kb-item ${kb.selected===x.id?'active':''}" data-kb-sub="${escapeHTML(x.id)}"><div class="kb-item-top">${kbStatusPill(x.status)}<b>${escapeHTML(x.title)}</b></div><small>${escapeHTML(x.source||'未选来源')} · ${escapeHTML(x.path||'未填路径')} · ${relativeTime(x.updatedAt)}</small></div>`).join(''):'<div class="kb-empty">没有条目</div>'}</div>
      </div>
      <div class="kb-card" id="kbEditor"></div>
    </div>`;
  body.querySelectorAll('[data-kb-status]').forEach(b=>b.onclick=()=>{kb.subStatus=b.dataset.kbStatus;kbRender()});
  body.querySelectorAll('[data-kb-sub]').forEach(el=>el.onclick=()=>kbSelectSubmission(el.dataset.kbSub));
  body.querySelectorAll('[data-kb-queue]').forEach(b=>b.onclick=async e=>{
    e.stopPropagation();const [runId,artifactId]=b.dataset.kbQueue.split('|');
    try{const item=await api('/api/knowledge/submissions',{method:'POST',body:JSON.stringify({runId,artifactId})});toast('已加入审核队列','ok');kb.subStatus='pending';await kbRefresh();await kbSelectSubmission(item.id)}catch(err){toast(err.message,'err')}
  });
  $('#kbNewSub').onclick=()=>{kb.selected=null;kb.draft={id:'',title:'',source:(kbAllowed()[0]||''),path:'',content:'',status:'new'};kbRenderEditor()};
  kbRenderEditor();
}

async function kbSelectSubmission(id){
  try{kb.draft=await api(`/api/knowledge/submissions/${encodeURIComponent(id)}`);kb.selected=id;kb.subStatus=kb.draft.status;kbRender()}catch(err){toast(err.message,'err')}
}

function kbSourceOptions(selected){
  const all=(kb.sources||[]).filter(x=>x.allowed).map(x=>x.id);
  if(selected&&!all.includes(selected))all.unshift(selected);
  return `<option value="">选择知识库来源</option>${all.map(id=>`<option value="${escapeHTML(id)}" ${id===selected?'selected':''}>${escapeHTML(id)}</option>`).join('')}`;
}

function kbRenderEditor(){
  const host=$('#kbEditor');
  if(!host)return;
  const d=kb.draft;
  if(!d){host.innerHTML=`<div class="kb-card-title"><i>✎</i>审核与写回</div><ol class="kb-flow"><li><i>1</i><div><b>在沉淀队列中选一条</b><p>候选来自运行产物 knowledge.md；也可以手动新建。</p></div></li><li><i>2</i><div><b>编辑并确认</b><p>补全标题、目标知识库来源和路径（仓库内 .md 相对路径），修改正文。</p></div></li><li><i>3</i><div><b>审核通过并写回</b><p>平台调用知识库写入接口：提交到来源 Git 仓库并增量重建索引，记录 commit。</p></div></li></ol>`;return}
  const locked=d.status==='published';
  const isNew=d.status==='new';
  host.innerHTML=`
    <form class="kb-form" id="kbEditorForm">
      <div class="kb-card-title"><i>✎</i>${isNew?'手动新建沉淀':'审核与写回'} ${isNew?'':kbStatusPill(d.status)}</div>
      ${isNew?'':`<div class="kb-meta-row">${d.runId?`<a href="#/runs/${encodeURIComponent(d.runId)}" data-kb-run="${escapeHTML(d.runId)}">来源运行 ${escapeHTML(d.runId)}</a>`:'<span>手动创建</span>'}<span>创建人 ${escapeHTML(d.createdBy||'-')}</span>${d.reviewedBy?`<span>审核人 ${escapeHTML(d.reviewedBy)}</span>`:''}${d.commit?`<span>commit <code>${escapeHTML(d.commit.slice(0,10))}</code></span>`:''}</div>`}
      ${d.error?`<div class="kb-readonly">上次写回失败：${escapeHTML(d.error)}</div>`:''}
      <label>标题</label><input type="text" name="title" value="${escapeHTML(d.title||'')}" ${locked?'disabled':''} placeholder="一句话说明这条知识">
      <div class="kb-grid even" style="gap:10px">
        <div><label>写入知识库来源</label><select class="select" name="source" ${locked?'disabled':''}>${kbSourceOptions(d.source)}</select></div>
        <div><label>文档路径（.md）</label><input type="text" name="path" value="${escapeHTML(d.path||'')}" ${locked?'disabled':''} placeholder="guides/xxx.md"></div>
      </div>
      <label>正文（Markdown）</label><textarea name="content" ${locked?'disabled':''}>${escapeHTML(d.content||'')}</textarea>
      <label>审核备注</label><input type="text" name="reviewNote" value="${escapeHTML(d.reviewNote||'')}" ${locked?'disabled':''} placeholder="可选：驳回原因或修改说明">
      <div class="kb-actions">${locked?'<span class="kb-result ok">已写回知识库，不可再编辑。</span>':isNew?'<button type="submit" class="button primary">创建条目</button>':`<button type="button" class="button ghost" data-kb-act="save">保存</button><button type="button" class="button ghost" data-kb-act="reject">驳回</button><button type="button" class="button ghost" data-kb-act="delete">删除</button><button type="button" class="button primary" data-kb-act="publish">审核通过并写回</button>`}</div>
    </form>`;
  const form=$('#kbEditorForm');
  const values=()=>({title:form.elements.title.value.trim(),source:form.elements.source.value,path:form.elements.path.value.trim(),content:form.elements.content.value,reviewNote:form.elements.reviewNote.value.trim()});
  host.querySelector('[data-kb-run]')?.addEventListener('click',e=>{e.preventDefault();switchView('runs');showRunDetail(d.runId)});
  form.onsubmit=async e=>{
    e.preventDefault();
    if(!isNew)return;
    try{const item=await api('/api/knowledge/submissions',{method:'POST',body:JSON.stringify({...values(),projectId:kbProjectId()})});toast('已创建沉淀条目','ok');await kbRefresh();await kbSelectSubmission(item.id)}catch(err){toast(err.message,'err')}
  };
  const save=()=>api(`/api/knowledge/submissions/${encodeURIComponent(d.id)}`,{method:'PUT',body:JSON.stringify(values())});
  form.querySelectorAll('[data-kb-act]').forEach(b=>b.onclick=async()=>{
    const act=b.dataset.kbAct;
    try{
      if(act==='save'){kb.draft=await save();toast('已保存','ok')}
      if(act==='reject'){await save();kb.draft=await api(`/api/knowledge/submissions/${encodeURIComponent(d.id)}/reject`,{method:'POST',body:JSON.stringify({reviewNote:values().reviewNote})});toast('已驳回')}
      if(act==='delete'){if(!confirm('删除这条沉淀条目？'))return;await api(`/api/knowledge/submissions/${encodeURIComponent(d.id)}`,{method:'DELETE'});kb.draft=null;kb.selected=null;toast('已删除')}
      if(act==='publish'){
        const v=values();if(!v.source)return toast('请选择写入的知识库来源');if(!/\.md$/i.test(v.path))return toast('路径必须以 .md 结尾');
        if(!confirm(`确认写回知识库？\n\n来源：${v.source}\n路径：${v.path}\n\n将提交到知识库 Git 仓库并触发重建索引。`))return;
        await save();kb.draft=await api(`/api/knowledge/submissions/${encodeURIComponent(d.id)}/publish`,{method:'POST'});toast(`已写回知识库 · commit ${String(kb.draft.commit||'').slice(0,8)}`,'ok');
      }
      if(kb.draft)kb.subStatus=kb.draft.status;
      await kbRefresh();kbRender();
    }catch(err){toast(err.message,'err');await kbRefresh();kbRender()}
  });
}

// ---------- 检索试用 ----------
function kbRenderSearch(body){
  const s=kb.settings||{};
  const q=kb.search;
  const allowed=(kb.sources||[]).filter(x=>x.allowed);
  body.innerHTML=`
    <div class="kb-card">
      <div class="kb-card-title"><i>⌕</i>检索试用<small>与 Agent 调用 kb_search 的结果一致，范围为本项目授权来源</small></div>
      ${s.ready?'':'<div class="kb-readonly">知识库尚未启用，请先完成「接入配置」。</div>'}
      <form class="kb-search-bar" id="kbSearchForm"><input type="search" name="q" placeholder="例如：套餐售罄怎么处理 / EPP 2306 / 帕鲁 断连" value="${escapeHTML(q.query)}" ${s.ready?'':'disabled'}><button class="button primary" type="submit" ${s.ready?'':'disabled'}>检索</button></form>
      ${allowed.length?`<div class="kb-chips">${allowed.map(x=>`<button type="button" data-kb-chip="${escapeHTML(x.id)}" class="${q.sources.includes(x.id)?'on':''}">${escapeHTML(x.id)}</button>`).join('')}</div><p class="kb-hint" style="margin-top:6px">不选表示检索全部授权来源。</p>`:''}
    </div>
    <div style="margin-top:14px" id="kbHits">${kbHitsHTML()}</div>`;
  const form=$('#kbSearchForm');
  form.onsubmit=async e=>{e.preventDefault();q.query=form.elements.q.value.trim();if(!q.query)return;await kbDoSearch()};
  body.querySelectorAll('[data-kb-chip]').forEach(b=>b.onclick=()=>{const id=b.dataset.kbChip;q.sources=q.sources.includes(id)?q.sources.filter(x=>x!==id):[...q.sources,id];kbRender()});
  kbBindHits();
}

function kbHitsHTML(){
  const q=kb.search;
  if(q.loading)return '<div class="kb-empty">检索中…</div>';
  if(q.error)return `<div class="kb-empty">检索失败：${escapeHTML(q.error)}</div>`;
  if(!q.result)return '';
  const r=q.result.result;
  const head=`<div class="kb-meta-row"><span>${r.results.length} 条结果</span><span>模式 ${escapeHTML(r.mode||'-')}</span><span>${q.result.latencyMs}ms</span>${r.degraded?'<span class="kb-pill warn">语义检索降级为词法检索</span>':''}</div>`;
  if(!r.results.length)return head+'<div class="kb-empty">没有命中，换个关键词试试。</div>';
  return head+r.results.map((h,i)=>`<div class="kb-hit" data-kb-hit="${i}"><div class="kb-hit-top"><span class="kb-pill blue">${escapeHTML(h.source)}</span><b>${escapeHTML(h.title||h.path)}</b>${h.heading?`<small>· ${escapeHTML(h.heading)}</small>`:''}<span class="kb-score">score ${Number(h.score||0).toFixed(3)}</span></div><p>${escapeHTML(h.snippet||'')}</p><cite>${escapeHTML(h.citation||`${h.source}:${h.path}#L${h.start_line}`)}</cite></div>`).join('');
}

function kbBindHits(){
  $$('#kbHits [data-kb-hit]').forEach(el=>el.onclick=()=>kbOpenDocument(kb.search.result.result.results[Number(el.dataset.kbHit)]));
}

async function kbDoSearch(){
  const q=kb.search;q.loading=true;q.error='';$('#kbHits').innerHTML=kbHitsHTML();
  try{q.result=await api('/api/knowledge/search',{method:'POST',body:JSON.stringify({query:q.query,projectId:kbProjectId(),sources:q.sources})})}
  catch(err){q.error=err.message;q.result=null}
  q.loading=false;
  const hits=$('#kbHits');if(hits){hits.innerHTML=kbHitsHTML();kbBindHits()}
}

async function kbOpenDocument(hit){
  if(!hit)return;
  const modal=$('#kbDocModal');
  $('#kbDocTitle').textContent=hit.title||hit.path;
  $('#kbDocMeta').textContent=hit.citation||`${hit.source}:${hit.path}`;
  $('#kbDocBody').innerHTML='<div class="kb-empty">正在读取原文…</div>';
  modal.classList.add('open');
  try{
    const doc=await api(`/api/knowledge/document?source=${encodeURIComponent(hit.source)}&path=${encodeURIComponent(hit.path)}&projectId=${encodeURIComponent(kbProjectId())}`);
    $('#kbDocBody').innerHTML=`<div class="kb-doc">${(doc.lines||[]).map(l=>`<div class="${l.number>=hit.start_line&&l.number<=hit.end_line?'hl':''}" ${l.number===hit.start_line?'id="kbDocAnchor"':''}><span>${l.number}</span><code>${escapeHTML(l.text)}</code></div>`).join('')}</div>`;
    $('#kbDocAnchor')?.scrollIntoView({block:'center'});
  }catch(err){$('#kbDocBody').innerHTML=`<div class="kb-empty">读取失败：${escapeHTML(err.message)}</div>`}
}

document.addEventListener('click',e=>{
  if(e.target.closest('[data-kb-doc-close]')||e.target.id==='kbDocModal')$('#kbDocModal')?.classList.remove('open');
  if(e.target.closest('#kbRefreshBtn'))renderKnowledgeView();
});
