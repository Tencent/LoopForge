// 进化审查工作台：问题、正文差异与逐条回放共用现有提案和证据接口。
// 图标来自 Harness UI（MIT），见 harness-ui-LICENSE.txt。
function evoReviewIcon(name){return `<svg class="agent-icon evo-review-icon harness-icon" data-icon="${name}" aria-hidden="true"></svg>`}
function evoReviewVersionHTML(asset,p){
  if(!asset)return '';
  const version=p?.baseVersion?`v${p.baseVersion}`:'修改前',after=p?.releasedVersion?`v${p.releasedVersion}`:'修改稿';
  const labels={drafting:'生成中',validating:'验证中',pending:'待试用',canary:'试用中',released:'已发布',rejected:'未采用',failed:'未通过',rolled_back:'已回滚'};
  return `<span>${escapeHTML(evoKindLabel(asset.assetKind))}</span>${p?`<span>${version}</span>${evoReviewIcon('arrow-right')}<span>${after}</span>${kbBadge(labels[p.status]||p.status,evoChangeState(p).tone)}`:''}`;
}
function evoReviewChangeHTML(m,p){
  const stats=p?.candidateContent?evoDiffStats(p.baseContent,p.candidateContent):{add:0,del:0};
  const lines=p?.candidateContent?evoLineDiff(p.baseContent,p.candidateContent):[];
  const long=lines.some(([,line])=>line.length>60)||lines.length>6;
  const body=!p?.candidateContent?`<div class="evo-review-empty">${p?.status==='drafting'?'正在生成修改…':'暂无正文修改'}</div>`:m.diffSplit?evoBeforeAfterHTML(p,!!m.diffContext,!m.diffContext):evoDiffHTML(p.baseContent,p.candidateContent,!!m.diffContext,long&&!m.diffExpanded,!m.diffContext);
  return `<header class="evo-review-filebar"><span>${evoReviewIcon('doc')}${p?.assetKind==='rule'?'规则正文':'角色指令'}</span><div><span class="evo-good">+${stats.add}</span><span class="evo-bad">−${stats.del}</span><button type="button" class="kb-btn quiet small evo-review-icon-button" data-evo-diff-split aria-label="${m.diffSplit?'合并对比':'并排对比'}" aria-pressed="${!!m.diffSplit}" data-tooltip="${m.diffSplit?'合并对比':'并排对比'}">${evoReviewIcon('panel-left')}</button></div></header>
    ${p?.candidateContent?`<button type="button" class="evo-review-context" data-evo-diff-context aria-pressed="${!!m.diffContext}">${evoReviewIcon('eye')}${m.diffContext?'收起未修改内容':'未修改内容'}</button>`:''}
    <div class="evo-review-diff${m.diffExpanded?' is-expanded':''}${m.diffSplit?' is-split':''}" id="evoReviewDiff">${body}</div>
    ${long?`<button type="button" class="evo-review-expand" data-evo-diff-expand aria-expanded="${!!m.diffExpanded}" aria-controls="evoReviewDiff">${evoReviewIcon('expand')}${m.diffExpanded?'收起修改':'展开修改'}</button>`:''}
    ${p?.title||p?.rationale?`<details class="evo-review-reason"><summary>修改说明</summary>${p.title?`<p>${escapeHTML(p.title)}</p>`:''}${p.rationale?`<p>${escapeHTML(p.rationale)}</p>`:''}</details>`:''}`;
}
function evoReviewProblemsHTML(m,p){
  const r=m.row,issues=evoStoryCurrentIssues(m,p);
  const list=issues.map(issue=>`<button type="button" class="evo-review-problem" data-tooltip="${escapeHTML(issue.title)}" data-evo-related-finding="${escapeHTML(issue.f.id)}" data-cluster="${escapeHTML(issue.key)}" aria-pressed="${issue.f.id===r?.f.id&&issue.key===r?.key}">${evoReviewIcon('alert')}<span><b>${escapeHTML(issue.title)}</b><small>${issue.c.runs||0} 次运行</small></span>${evoReviewIcon('arrow-right')}</button>`).join('');
  const records=r?`<details class="evo-detail-more" id="evoRunDetails"><summary>运行记录</summary><div id="evoRunPreview" class="evo-run-preview"></div><select class="kb-input" aria-label="运行结果" data-evo-record-status><option value="">全部结果</option><option value="failed">失败</option><option value="succeeded">成功</option><option value="reworked">返工后成功</option><option value="unavailable">记录不可用</option></select><div id="evoFindingRecords" aria-live="polite"></div></details><details class="evo-detail-more" id="evoDiagnosisDetails"><summary>诊断与处理</summary>${evoFindingStats(r)}<div class="evo-finding-actions">${evoIssueState(r.f)==='done'?`<span>${escapeHTML(r.f.closeNote||'已归档')}</span>`:`<button type="button" class="kb-btn small" data-evo-snooze="${escapeHTML(r.f.id)}" data-days="${evoSnoozed(r.f)?0:EVO_SNOOZE_DAYS}"${r.f.status!=='open'?' disabled':''}>${evoSnoozed(r.f)?'取消暂缓':'暂缓 7 天'}</button><button type="button" class="kb-btn quiet small" data-evo-dismiss="${escapeHTML(r.f.id)}">忽略</button>`}</div></details>`:'';
  return `<header class="evo-review-filebar"><span>${evoReviewIcon('alert')}问题</span><span class="evo-muted">${issues.length}</span></header>${list||'<div class="evo-review-empty">未关联问题</div>'}<div class="evo-review-records">${records}</div>`;
}
// 保留验证时的整次运行评分；待完成 / 阻塞不能被默认 score=0 画成失败。
function evoReviewScoreHTML(score,ready=true){
  const valid=ready&&Number.isInteger(score)&&score>=0&&score<=2;
  const [icon,tone,label]=!valid?['clock','','未完成或未记录']:score===2?['check','evo-good','一次成功']:score===1?['refresh','evo-review-warn','返工后成功']:['close','evo-bad','失败'];
  return `<span class="evo-review-result ${tone}" role="img" aria-label="${label}" data-tooltip="${label}">${evoReviewIcon(icon)}</span>`;
}
function evoReviewReplayTable(m,wide=false){
  const records=m.proposal?.replays||[],visible=records.filter(r=>m.compareFilter==='all'||r.outcome===m.compareFilter);
  if(!records.length)return '<div class="evo-review-empty">暂无验证样本</div>';
  return `<table class="evo-review-results"><caption class="evo-review-sr">验证时的原运行评分与回放评分</caption><thead><tr><th>样例</th><th data-tooltip="创建验证时保存的原运行评分">原运行</th><th>回放</th>${wide?'<th>结果</th>':''}</tr></thead><tbody>${visible.map((replay,index)=>{
    const record=m.data?.items?.find(r=>r.runId===replay.sourceRunId),label=record?.issueTitle||replay.sourceRunId?.slice(-10)||`样例 ${index+1}`;
    const ready=!!evoTime(replay.finishedAt)&&!!replay.outcome&&replay.outcome!=='blocked';
    return `<tr class="${m.evidence?.type==='replay'&&m.evidence.id===replay.id?'is-selected':''}"><td><button type="button" data-evo-evidence-replay="${escapeHTML(replay.id)}" aria-pressed="${m.evidence?.type==='replay'&&m.evidence.id===replay.id}" data-tooltip="${escapeHTML(label)} · ${escapeHTML(replay.sourceRunId||label)}">${evoReviewIcon('doc')}<span>${escapeHTML(label)}</span></button></td><td>${evoReviewScoreHTML(replay.sourceScore)}</td><td>${evoReviewScoreHTML(replay.score,ready)}</td>${wide?`<td>${replay.outcome?evoStatusBadge(EVO_OUTCOMES,replay.outcome):'<span class="evo-muted">待判定</span>'}</td>`:''}</tr>`;
  }).join('')||`<tr><td colspan="${wide?4:3}" class="evo-muted">无此类结果</td></tr>`}</tbody></table>`;
}
function evoReviewValidationHTML(p){
  const s=p?.summary||{},records=p?.replays||[];
  const counts=[['all','全部',records.length],['fixed','修复',s.fixed],['regressed','退化',s.regressed],['unchanged','保持',s.unchanged],['changed','结论改变',s.changed],['blocked','阻塞',s.blocked]].filter(([key,,n])=>key==='all'||n);
  return `<header class="evo-review-filebar"><span>${evoReviewIcon('flask')}历史验证</span><span class="${s.passed?'evo-good':'evo-muted'}">${s.passed?evoReviewIcon('check'):evoReviewIcon('clock')}${s.finished||0} / ${s.total||records.length}</span></header><div class="evo-sample-filters" role="group" aria-label="验证样本筛选">${counts.map(([key,label,n])=>`<button type="button" data-evo-sample-filter="${key}" aria-pressed="${(evo.modal?.compareFilter||'all')===key}">${label}<span>${n||0}</span></button>`).join('')}</div><div id="evoValidationList"></div><details class="evo-review-reason"><summary>验证口径</summary><p>评分为创建验证时的整次运行结果。评分改善表示本样本改善；“保持”不表示输出完全相同。</p><p>下方节点输出为当前可读取的记录，原运行重试后可能变化。</p>${s.verdict?`<p>${escapeHTML(evoResultText(s.verdict))}</p>`:''}${p?.error?`<p class="evo-bad">${escapeHTML(p.error)}</p>`:''}</details>`;
}
function evoReviewTrialHTML(p){
  const c=p?.canary,w=p?.watch,trial=!!c&&!w,activeTrial=p?.status==='canary'&&trial;
  if(!trial&&!w)return '';
  const percent=trial?Number(c.percent)||0:100,arms=trial?[[c.control,'原版'],[c.canary,'试用版']]:[[w.baseline,'发布前'],[w.current,'发布后']];
  return `<section class="evo-review-trial"><header>${evoReviewIcon('flask')}<b>${trial?'试用':'发布后观察'}</b>${evoStatusBadge(trial?EVO_CANARY_STATUS:EVO_WATCH_STATUS,trial?c.status:w.status)}</header><div id="evoTrafficBlock"${activeTrial?'':' hidden'}><div class="evo-traffic-track" role="img" aria-label="${percent}% 新运行使用${trial?'修改稿':'新版本'}"><span style="width:${Math.max(0,Math.min(100,percent))}%"></span></div><div class="evo-traffic-legend"><span>${trial?'试用版':'新版'} ${percent}%</span><span>原版 ${100-percent}%</span></div>${activeTrial&&c.status==='running'?`<div class="evo-traffic-setting"><select id="evoTrafficPercent" class="kb-input" aria-label="灰度流量">${EVO_CANARY_PERCENTS.map(v=>`<option value="${v}"${v===percent?' selected':''}>${v}%</option>`).join('')}</select><button type="button" class="kb-btn quiet small" data-evo-traffic-apply="${escapeHTML(p.id)}" disabled>调整比例</button></div>`:''}</div><table><thead><tr><th></th><th>运行</th><th>一次成功</th></tr></thead><tbody>${arms.map(([a,label])=>`<tr><td>${label}</td><td>${a?.runs||0}${trial?` / ${c.minRuns}`:''}</td><td>${a?.runs>0&&Number.isFinite(a.clean)?`${a.clean}/${a.runs}`:'—'}</td></tr>`).join('')}</tbody></table>${!trial&&!w.baselineReady?'<small class="evo-muted">发布前基线未就绪</small>':''}${(trial?c.verdict:w.verdict)?`<details><summary>判定</summary><p>${escapeHTML(trial?c.verdict:w.verdict)}</p></details>`:''}</section>`;
}
function evoReviewDetailHTML(m,p){return evoStoryDetailHTML(m,p)}

function evoReviewOutputHTML(node,error){
  if(error)return `<p class="evo-muted">记录不可用：${escapeHTML(error)}</p>`;
  if(!node)return '<p class="evo-muted">未跑到此节点或无记录</p>';
  const text=node.output||node.error||node.gateResult?.summary||node.feedback||'',long=text.length>400||text.split('\n').length>8;
  return `${evoNodeSideHTML({status:node.status,rework:node.reworkCount,decision:node.gateResult?.decision})}${text?`<div class="evo-review-output"><pre class="evo-output-excerpt">${escapeHTML(text)}</pre>${long?'<button type="button" class="evo-review-expand" data-evo-output-expand aria-expanded="false">展开记录</button>':''}</div>`:'<p class="evo-muted">未记录输出</p>'}`;
}
// 结构化产出只对比真实字段值；无法完整枚举时回退到原记录，不推断业务含义。
function evoReviewJSONChanges(before,after){
  function fields(text){
    if(typeof text!=='string'||text.length>20000)return null;
    let value;try{value=JSON.parse(text)}catch{return null}
    if(!value||typeof value!=='object'||Array.isArray(value))return null;
    const out=new Map();let overflow=false;
    function visit(item,path){
      if(item&&typeof item==='object'&&!Array.isArray(item)&&Object.keys(item).length&&path.length<5){for(const [key,v] of Object.entries(item))visit(v,[...path,key]);return}
      if(out.size>=24){overflow=true;return}
      out.set(JSON.stringify(path),{path:path.join('.'),value:JSON.stringify(item),text:typeof item==='string'?item:JSON.stringify(item)});
    }
    visit(value,[]);return overflow?null:out;
  }
  const a=fields(before),b=fields(after);if(!a||!b)return null;
  const rows=[];
  for(const key of new Set([...a.keys(),...b.keys()])){const old=a.get(key),next=b.get(key);if(old?.value!==next?.value){const sameText=old?.text===next?.text;rows.push({path:(next||old).path,before:sameText?old?.value:old?.text,after:sameText?next?.value:next?.text})}}
  return rows.length<=12?rows:null;
}
function evoReviewEvidenceHTML(m,data){
  if(data.record){
    const {record,run}=data,nodes=run.nodes&&!Array.isArray(run.nodes)?run.nodes:{},ids=Object.keys(nodes);
    const chosen=ids.includes(m.evidenceNode)?m.evidenceNode:ids.includes(record.nodeId)?record.nodeId:ids.find(id=>nodes[id].status==='failed')||ids[0]||'';m.evidenceNode=chosen;
    return `<div class="evo-review-evidence-meta">${evoEvidenceRunLink(record.runId,'完整运行 ↗')}</div><div class="evo-evidence-signal"><p>${escapeHTML(evoProblemText(record.detail||'无信号摘要'))}</p></div>${run.input?`<details class="evo-review-input"><summary>输入</summary><pre class="evo-output-excerpt">${escapeHTML(run.input)}</pre></details>`:''}${evoStoryNodePickerHTML(ids,chosen,run)}<section>${evoReviewOutputHTML(nodes[chosen])}</section>`;
  }
  const {replay,comparison,source,candidate}=data,nodes=comparison.nodes,chosen=nodes.find(n=>n.id===m.evidenceNode)||nodes.find(n=>n.shaped&&!n.reused)||nodes.find(n=>n.changed)||nodes[0];m.evidenceNode=chosen?.id||'';
  const before=source.run?.nodes?.[chosen?.id],after=candidate.run?.nodes?.[chosen?.id],changed=source.run&&(replay.sourceStatus&&source.run.status!==replay.sourceStatus||replay.sourceScore===0&&source.run.status==='succeeded');
  const raw=`<div class="evo-output-pair"><section><h4>原运行 · 最新${evoEvidenceRunLink(replay.sourceRunId,'↗')}</h4>${evoReviewOutputHTML(before,source.error)}</section><section><h4>回放${chosen?.reused?' · 沿用':''}${evoEvidenceRunLink(replay.runId,'↗')}</h4>${evoReviewOutputHTML(after,candidate.error)}</section></div>`;
  const delta=!source.error&&!candidate.error?evoReviewJSONChanges(before?.output,after?.output):null;
  const output=delta?`<div class="evo-review-json-diff"><div class="evo-review-delta-label">当前记录差异</div>${delta.map(row=>`<section class="${Math.max(row.before?.length||0,row.after?.length||0)>40?'is-wide':''}"><h4>${escapeHTML(row.path)}</h4><div class="del"><span>−</span><code>${escapeHTML(row.before===undefined?'未设置':row.before)}</code></div><div class="add"><span>+</span><code>${escapeHTML(row.after===undefined?'未设置':row.after)}</code></div></section>`).join('')||'<p class="evo-muted">当前输出相同</p>'}</div><details class="evo-review-input"><summary>原始记录</summary>${raw}</details>`:raw;
  const picker=nodes.length>1?`<label class="evo-evidence-node-label">节点<select class="kb-input" data-evo-evidence-node>${nodes.map(n=>`<option value="${escapeHTML(n.id)}"${n.id===chosen?.id?' selected':''}>${escapeHTML(n.name||n.id)}${n.reused?' · 沿用':''}</option>`).join('')}</select></label>`:'';
  const input=`<details class="evo-review-input"><summary>完整输入</summary><div class="evo-output-pair"><section><h4>原运行 · 最新</h4><pre class="evo-output-excerpt">${escapeHTML(source.run?.input||'未记录输入')}</pre></section><section><h4>回放</h4><pre class="evo-output-excerpt">${escapeHTML(candidate.run?.input||'未记录输入')}</pre></section></div></details>`;
  return `<div class="evo-review-evidence-meta">${evoStatusBadge(EVO_OUTCOMES,replay.outcome)}<span>${escapeHTML(EVO_GROUPS[replay.group]?.[0]||'历史样本')}</span></div>${changed?'<p class="evo-review-warn">原运行已重试；下方为最新记录。</p>':''}${replay.error?`<p class="evo-bad">${escapeHTML(replay.error)}</p>`:''}${input}${picker}${output}<div id="evoStoryArtifactEvidence"></div><details class="evo-review-reason"><summary>判定依据</summary>${evoReplayJudgmentHTML(replay,source)}${chosen?.source?.reason?`<p>${escapeHTML(chosen.source.reason)}</p>`:''}${chosen?.replay?.reason?`<p>${escapeHTML(chosen.replay.reason)}</p>`:''}</details>`;
}
document.addEventListener('click',event=>{
  const button=event.target.closest?.('[data-evo-diff-split],[data-evo-diff-context],[data-evo-diff-expand],[data-evo-output-expand],[data-evo-trial-close]');if(!button||button.disabled||!evo.modal)return;
  const m=evo.modal;
  if(button.hasAttribute('data-evo-trial-close')){$('#evoDetailCanary')?.close();return}
  if(button.hasAttribute('data-evo-output-expand')){const expanded=button.getAttribute('aria-expanded')!=='true';button.closest('.evo-review-output').classList.toggle('is-expanded',expanded);button.setAttribute('aria-expanded',String(expanded));button.textContent=expanded?'收起记录':'展开记录';return}
  const key=button.hasAttribute('data-evo-diff-split')?'diffSplit':button.hasAttribute('data-evo-diff-context')?'diffContext':'diffExpanded';m[key]=!m[key];
  const selector=button.hasAttribute('data-evo-diff-split')?'[data-evo-diff-split]':button.hasAttribute('data-evo-diff-context')?'[data-evo-diff-context]':'[data-evo-diff-expand]';
  $('#evoLinkedChange').innerHTML=evoReviewChangeHTML(m,m.proposal);window.AgentFlowHarnessIcons?.hydrate($('#evoLinkedChange'));$(selector)?.focus({preventScroll:true});
});
