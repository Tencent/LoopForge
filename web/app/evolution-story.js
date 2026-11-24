// 问题 → 指令变化 → 同一输入的产物变化 → 整份修改的历史验证。
// 图标与组件沿用 Harness UI（MIT），见 harness-ui-LICENSE.txt。
function evoStoryBrief(value,limit=72){
  const text=String(value||'').trim().replace(/\s+/g,' ');
  const sentence=text.match(/^.*?[。！？](?:\s|$)/)?.[0]?.trim()||text;
  return [...sentence].length>limit?[...sentence].slice(0,limit).join('')+'…':sentence;
}
function evoStoryPoints(p){
  return (p?.review?.changes||[]).filter(x=>x&&x.before!==x.after&&
    (!x.before||p.baseContent?.includes(x.before))&&(!x.after||p.candidateContent?.includes(x.after))).slice(0,3);
}
function evoStoryCurrentIssues(m,p){
  const rows=evoGroupForAsset(p||m.row?.f)?.issues||[];
  return p?rows.filter(r=>r.f.id===p.findingId||r.f.proposalId===p.id):rows.filter(r=>r.f.id===m.row?.f.id);
}
function evoStoryProblemReplays(p){
  const seen=new Set();
  return (p?.replays||[]).filter(r=>r.sourceRunId&&['diagnosis','holdout'].includes(r.group)&&!seen.has(r.sourceRunId)&&seen.add(r.sourceRunId));
}
function evoStoryNodePickerHTML(ids,chosen,run){
  const nodes=run.workflowSnapshot?.nodes||[];
  const label=id=>{const node=nodes.find(n=>n.id===id);return node?.name&&node.name!==id?node.name:`执行节点 ${ids.indexOf(id)+1}`};
  if(ids.length<=1)return ids.length?`<p class="evo-story-note" data-tooltip="${escapeHTML(chosen)}">${escapeHTML(label(chosen))}</p>`:'';
  return `<label class="evo-evidence-node-label">执行节点<select class="kb-input" data-evo-evidence-node>${ids.map(id=>`<option value="${escapeHTML(id)}"${id===chosen?' selected':''}>${escapeHTML(label(id))}</option>`).join('')}</select></label>`;
}
function evoStorySampleName(m,replay,index){
  const point=evoStoryPoints(m.proposal).find(x=>x.sourceRunIds?.includes(replay.sourceRunId));
  const record=m.data?.items?.find(r=>r.runId===replay.sourceRunId);
  const label=record?.issueTitle||point?.title;
  return label&&!/^\d+$/.test(label)?evoStoryBrief(label,32):`${EVO_GROUPS[replay.group]?.[0]||'历史样例'} ${index+1}`;
}
function evoStoryProblemsHTML(m,p){
  const replays=evoStoryProblemReplays(p);
  if(replays.length)return replays.map((r,i)=>`<button type="button" class="evo-story-problem" data-evo-story-replay="${escapeHTML(r.id)}" aria-pressed="${m.evidence?.type==='replay'&&m.evidence.id===r.id}">${evoReviewIcon('alert')}<span><b>${escapeHTML(evoStorySampleName(m,r,i))}</b><small>${escapeHTML(evoHistoricalScore(r.sourceScore))} · ${escapeHTML(EVO_GROUPS[r.group]?.[0]||'问题样例')}</small></span></button>`).join('')+`<small class="evo-story-problem-scope">${replays.length} 条问题样例 · 不含后续建议</small>`;
  const issues=evoStoryCurrentIssues(m,p);
  return issues.map(issue=>`<button type="button" class="evo-story-problem" data-evo-related-finding="${escapeHTML(issue.f.id)}" data-cluster="${escapeHTML(issue.key)}" aria-pressed="${issue.f.id===m.row?.f.id&&issue.key===m.row?.key}">${evoReviewIcon('alert')}<span><b data-tooltip="${escapeHTML(issue.title)}">${escapeHTML(evoStoryBrief(issue.title,44))}</b><small>${issue.c.runs||0} 次运行</small></span></button>`).join('')||'<p class="evo-story-empty">暂无可用的问题样例</p>';
}
function evoStoryReplayTable(m){
  const all=m.proposal?.replays||[],list=all.filter(r=>m.compareFilter==='all'||r.outcome===m.compareFilter);
  if(!all.length)return '<p class="evo-story-empty">尚无历史验证样例</p>';
  const outcome={fixed:['改善','evo-good'],regressed:['退化','evo-bad'],unchanged:['保持','evo-muted'],changed:['结论变化','evo-review-warn'],blocked:['阻塞','evo-review-warn']};
  return `<table class="evo-story-results"><caption class="evo-review-sr">这份修改的全部历史验证，按验证时保存的运行评分比较</caption><thead><tr><th>样例</th><th>原运行</th><th>回放</th><th>结果</th></tr></thead><tbody>${list.map((r,i)=>{
    const ready=!!evoTime(r.finishedAt)&&!!r.outcome&&r.outcome!=='blocked',result=outcome[r.outcome]||['待判定','evo-muted'];
    const selected=m.evidence?.type==='replay'&&m.evidence.id===r.id;
    const score=(n,complete)=>`${evoReviewScoreHTML(n,complete)}<span>${complete?escapeHTML(evoHistoricalScore(n)):'未完成'}</span>`;
    return `<tr data-evo-story-row="${escapeHTML(r.id)}" class="${selected?'is-selected':''}"><td><button type="button" aria-controls="evoStoryBehavior" data-evo-story-replay="${escapeHTML(r.id)}" aria-pressed="${selected}" data-tooltip="${escapeHTML(r.sourceRunId||'原运行不可用')}">${evoReviewIcon(r.group==='pinned'?'pin':'doc')}<span>${escapeHTML(evoStorySampleName(m,r,all.indexOf(r)))}<small>${escapeHTML(EVO_GROUPS[r.group]?.[0]||r.group||'历史样例')}</small></span></button></td><td><span class="evo-story-score">${score(r.sourceScore,Number.isInteger(r.sourceScore)&&r.sourceScore>=0&&r.sourceScore<=2)}</span></td><td><span class="evo-story-score">${score(r.score,ready)}</span></td><td><span class="${result[1]}">${result[0]}</span></td></tr>`;
  }).join('')||'<tr><td colspan="4">没有此类结果</td></tr>'}</tbody></table>`;
}
function evoStoryValidationHTML(p){
  if(!p)return `<header class="evo-story-section-head"><h3>${evoReviewIcon('flask')}历史验证</h3></header><p class="evo-story-empty">生成修改稿后，系统会用历史运行验证。</p>`;
  const s=p?.summary||{},rows=p?.replays||[],complete=rows.filter(r=>evoTime(r.finishedAt)&&r.outcome&&r.outcome!=='blocked');
  const scored=rows.length>0&&rows.every(r=>Number.isInteger(r.sourceScore)&&r.sourceScore>=0&&r.sourceScore<=2)&&complete.length===rows.length;
  const counts=[['fixed','改善',s.fixed,'evo-good'],['unchanged','保持',s.unchanged,''],['regressed','退化',s.regressed,s.regressed?'evo-bad':'evo-muted'],['changed','结论变化',s.changed,'evo-review-warn'],['blocked','阻塞',s.blocked,'evo-review-warn']];
  return `<header class="evo-story-section-head"><h3>${evoReviewIcon('flask')}历史验证</h3><span class="evo-story-counts">${counts.filter(([k,,n])=>['fixed','unchanged','regressed'].includes(k)||n).map(([key,label,n,tone])=>`<button type="button" class="${tone}" data-evo-sample-filter="${key}" aria-pressed="${evo.modal?.compareFilter===key}">${label} ${n||0}</button>`).join('')}</span></header><div class="evo-story-validation-summary">${scored?`<span>原运行成功 <b>${rows.filter(r=>r.sourceScore>0).length}/${rows.length}</b></span>${evoReviewIcon('arrow-right')}<span>回放成功 <b>${complete.filter(r=>r.score>0).length}/${rows.length}</b></span>`:`<span>已完成 ${s.finished||0}/${s.total||rows.length}</span>`}${s.passed?kbBadge('验证通过','ok'):''}<span class="evo-story-summary-spacer"></span><button type="button" class="kb-btn quiet small" data-evo-sample-filter="all" aria-pressed="${(evo.modal?.compareFilter||'all')==='all'}">全部 ${rows.length}</button></div><div id="evoComparisonList"></div><footer class="evo-story-validation-foot"><details><summary>样例从哪来</summary><ul><li>诊断：从问题运行中选，供改进器参考。</li><li>留出：也来自问题运行，仅用于回放验证。</li><li>回归：从近期成功的运行中选。</li><li>钉选：在运行详情手动固定的成功运行。</li></ul></details><details><summary>判定口径</summary><p>比较创建验证时的运行评分：失败、返工后成功、一次成功。保持不表示产物相同；历史结果不代表线上效果。</p>${s.verdict?`<p>${escapeHTML(evoResultText(s.verdict))}</p>`:''}</details></footer>`;
}
const EVO_STORY_SOURCES=[
  ['node_failed','alert','失败案例'],['rework','refresh','返工案例'],
  ['user_feedback','user','用户反馈'],['eval_low','chart','评测低分']
];
function evoFindingPriorityHTML(finding){
  const label={high:'高',medium:'中',low:'低'}[finding?.severity];
  return label?`<span class="evo-priority" data-severity="${finding.severity}" aria-label="${label}优先级">${label}</span>`:'';
}
function evoStoryProblemOverviewHTML(m,p){
  const f=m.row?.f,t=f?.triage,issues=evoStoryCurrentIssues(m,p);
  const summary=f?.summary||p?.review?.problem||m.row?.title||p?.title;
  const runs=new Set([...(f?.sampleRunIds||[]),...issues.flatMap(r=>(r.c.occurrences||[]).map(o=>o.runId))].filter(Boolean));
  const kinds=EVO_STORY_SOURCES.filter(([k])=>t?.kinds?.[k]>0||issues.some(r=>r.c.kind===k));
  const failed=t?.kinds?.node_failed??(t?.kinds?0:undefined);
  const rate=t?.runs>0&&Number.isFinite(failed)&&failed>=0&&failed<=t.runs?evoPct(failed/t.runs):'';
  const facts=[
    runs.size?`<div><dt>问题运行</dt><dd>${runs.size} 次</dd></div>`:'',
    rate?`<div><dt title="本次诊断窗口内当前资产版本的失败运行占比">当前失败率</dt><dd>${rate}</dd></div>`:'',
    kinds.length?`<div><dt>主要影响</dt><dd class="evo-story-impact-tags">${kinds.map(([, ,label])=>kbBadge(label,'err')).join('')}</dd></div>`:''
  ].join('');
  if(!summary&&!facts)return '';
  return `<section id="evoProblemOverview" class="evo-story-section evo-story-overview">
    <div><h2>${evoReviewIcon('doc')}问题概述</h2>${summary?`<p data-tooltip="${escapeHTML(summary)}">${escapeHTML(evoStoryBrief(evoProblemText(summary),120))}</p>`:''}</div>
    ${facts?`<dl>${facts}</dl>`:''}
  </section>`;
}
function evoStorySourceExampleHTML(issues,kind){
  const issue=issues.find(r=>r.c.kind===kind);
  if(!issue)return '';
  const occurrence=issue.c.occurrences?.[0],runId=occurrence?.runId||issue.c.runId;
  return `<p>${escapeHTML(evoProblemText(occurrence?.detail||issue.title))}</p>${runId?evoEvidenceRunLink(runId,'打开完整运行 ↗'):''}`;
}
function evoStoryProblemEvidenceHTML(m,p){
  const issues=evoStoryCurrentIssues(m,p),kinds=m.row?.f.triage?.kinds||{};
  const cards=EVO_STORY_SOURCES.map(([kind,icon,label])=>{
    const matches=issues.filter(r=>r.c.kind===kind);
    const count=kinds[kind]??(matches.length===1?matches[0].c.runs:0);
    const example=matches[0]?.title||'';
    return count>0||example?`<article class="evo-story-signal" data-signal="${kind}">
      <h3>${evoReviewIcon(icon)}${label}${count>0?`<b>${Number(count)} 次运行</b>`:''}</h3>
      ${example?`<p data-tooltip="${escapeHTML(example)}">${escapeHTML(evoStoryBrief(example,50))}</p>`:''}
      ${example?`<button type="button" class="kb-btn quiet small" data-evo-source-example="${kind}" aria-expanded="${m.sourceKind===kind}" aria-controls="evoStorySourceExample">${kind==='user_feedback'?'查看反馈':'查看示例'} ${evoReviewIcon('arrow-right')}</button>`:
        `<button type="button" class="kb-btn quiet small" data-evo-open-sources aria-controls="evoSourceDetails">查看记录 ${evoReviewIcon('arrow-right')}</button>`}
    </article>`:'';
  }).filter(Boolean);
  if(!issues.length&&!cards.length)return '';
  const expanded=evoStorySourceExampleHTML(issues,m.sourceKind);
  return `<section class="evo-story-section evo-story-signal-section">
    <header class="evo-story-section-head"><h2>${evoReviewIcon('doc')}取证依据</h2></header>
    <div class="evo-story-signals">${cards.join('')||`<article class="evo-story-signal">
      <h3>${evoReviewIcon('alert')}关联问题 <b>${issues.length} 类</b></h3>
      <button type="button" class="kb-btn quiet small" data-evo-open-sources aria-controls="evoSourceDetails">查看记录 ${evoReviewIcon('arrow-right')}</button>
    </article>`}</div>
    <div id="evoStorySourceExample" class="evo-story-source-example"${expanded?'':' hidden'}>${expanded}</div>
  </section>`;
}
function evoStoryValidationCardsHTML(p){
  const s=p?.summary,c=p?.canary;
  const paired=(p?.replays||[]).filter(r=>evoTime(r.finishedAt)&&r.outcome&&r.outcome!=='blocked'&&
    [r.sourceScore,r.score].every(n=>Number.isInteger(n)&&n>=0&&n<=2));
  const passed=key=>paired.filter(r=>r[key]>0).length;
  const before=passed('sourceScore'),after=passed('score');
  const comparable=c?.control?.runs>0&&c?.canary?.runs>0&&[c.control.failed,c.canary.failed].every(Number.isFinite);
  return `<div class="evo-story-validation-cards">
    <article>
      <h3>${evoReviewIcon('history')}历史 SOP 回放 ${s?.passed?kbBadge('验证通过','ok'):''}</h3>
      ${paired.length?`<p title="只比较原运行和回放均有评分的样例">运行成功 <b>${before}/${paired.length}</b> ${evoReviewIcon('arrow-right')} <b class="${after>before?'evo-good':after<before?'evo-bad':''}">${after}/${paired.length}</b></p>`:`<p>${s?.total?`已完成 ${s.finished||0}/${s.total} 条`:'暂无回放结果'}</p>`}
    </article>
    <article>
      <h3>${evoReviewIcon('flask')}小范围试用 ${c?kbBadge(`${c.percent}% 流量`,'info'):''}</h3>
      ${comparable?`<p title="原版失败率与试用版失败率">失败率 <b>${evoPct(c.control.failed/c.control.runs)}</b> ${evoReviewIcon('arrow-right')} <b>${evoPct(c.canary.failed/c.canary.runs)}</b></p><p>原版 ${c.control.failed}/${c.control.runs} 失败 · 试用版 ${c.canary.failed}/${c.canary.runs} 失败</p>`:`<p>${c?`试用版 ${c.canary?.runs||0}/${c.minRuns} 次 · 原版 ${c.control?.runs||0}/${c.minRuns} 次`:'尚未试用'}</p>`}
    </article>
  </div>`;
}
function evoStoryReleaseHTML(p){
  const w=p?.watch,c=p?.canary,released=p?.releasedVersion>0||['released','rolled_back'].includes(p?.status);
  const releasedAt=released&&(evoTime(w?.startedAt)||(p.status==='released'&&evoTime(p.decidedAt)));
  const published=released?(p.releasedVersion?`v${p.releasedVersion} 已发布`:'已发布'):'发布';
  const observed=w?.status==='watching'?'观察中':EVO_WATCH_STATUS[w?.status]?.[0]||'观察';
  const observationDone=['healthy','ended'].includes(w?.status);
  const rolledBack=p?.status==='rolled_back'||w?.status==='rolled_back';
  const steps=[
    [published,released?'done':'todo',releasedAt?new Date(releasedAt).toLocaleDateString():''],
    [rolledBack?'已回滚':observed,rolledBack?'stop':observationDone?'done':w?.status==='watching'?'active':w?'stop':'todo',w?.minRuns?`${w.current?.runs||0}/${w.minRuns} 次`:''],
    ['完成',observationDone?'done':'todo','']
  ];
  const status=rolledBack?kbBadge('已回滚','err'):w?evoStatusBadge(EVO_WATCH_STATUS,w.status):p?.status==='canary'&&c?
    kbBadge(c.status==='passed'?'待确认发布':`试用中 ${c.percent}%`,'info'):released?kbBadge('已发布','ok'):kbBadge('尚未发布','');
  return `<section id="evoReleasePanel" class="evo-story-section evo-story-release">
    <header class="evo-story-section-head"><h2>${evoReviewIcon('history')}发布与观测</h2></header>
    <div class="evo-story-release-content">
      <ol class="evo-story-release-track" aria-label="发布与观察进展">${steps.map(([name,state,note])=>
        `<li data-state="${state}"${state==='active'?' aria-current="step"':''}><i aria-hidden="true"></i><b>${escapeHTML(name)}</b>${note?`<small>${escapeHTML(note)}</small>`:''}</li>`
      ).join('')}</ol>
      <div class="evo-story-release-state"><span>当前状态</span>${status}</div>
    </div>
    ${w?evoReviewTrialHTML(p):''}
  </section>`;
}
function evoStoryDetailHTML(m,p){
  const asset=p||m.row?.f,group=evoGroupForAsset(asset);
  const problems=evoStoryProblemEvidenceHTML(m,p)||(!p?`
    <section class="evo-story-section">
      <header class="evo-story-section-head"><h2>${evoReviewIcon('alert')}关联问题</h2></header>
      <div id="evoStoryProblems" class="evo-story-problems">${evoStoryProblemsHTML(m,p)}</div>
    </section>`:'');
  return `
    <div id="evoReviewWorkbench" class="evo-review-workbench evo-story-workbench">
      <div id="evoProblemBox" class="evo-review-status" role="status">${p?.error?escapeHTML(p.error):''}</div>
      ${evoStoryProblemOverviewHTML(m,p)}
      ${problems}
      <section id="evoFullChange" class="evo-story-section"><div id="evoLinkedChange">${evoReviewChangeHTML(m,p)}</div></section>
      <section id="evoValidationResults" class="evo-story-section evo-story-validation">
        <header class="evo-story-section-head"><h2>${evoReviewIcon('flask')}验证结果</h2></header>
        ${evoStoryValidationCardsHTML(p)}
        <details id="evoValidationDetails" class="evo-story-expand">
          <summary>${p?.replays?.length?'查看回放样例':'查看运行证据'}</summary>
          <div class="evo-story-evidence-grid">
        <section class="evo-story-section evo-story-behavior">
          <header class="evo-story-section-head">
            <h3>${evoReviewIcon('workflow')}输出前后对比</h3>
          </header>
          <p id="evoStorySelectedSample" class="evo-story-selected-sample" hidden></p>
          <div id="evoStoryBehavior" aria-live="polite">
            <p class="evo-story-empty">${p?.replays?.length?'选择历史样例查看变化':'暂无对比记录'}</p>
          </div>
          <details id="evoEvidenceDetails" class="evo-story-evidence-details">
            <summary>运行输入与原始记录</summary>
            <section class="evo-evidence-panel">
              <header><h4 id="evoEvidenceTitle">运行证据</h4><span id="evoEvidenceCount"></span></header>
              <div id="evoEvidenceBody" aria-live="polite"></div>
            </section>
          </details>
        </section>
        <section id="evoDetailPanel-validation" class="evo-story-section">${evoStoryValidationHTML(p)}</section>
          </div>
        </details>
        ${p?.canary&&!p.watch?`<details id="evoCanaryDetails" class="evo-story-expand"><summary>试用数据与流量</summary>${evoReviewTrialHTML(p)}</details>`:''}
      </section>
      ${evoStoryReleaseHTML(p)}
      <details id="evoSourceDetails" class="evo-story-section evo-story-sources">
        <summary>来源记录与诊断</summary>
        <div id="evoProblemRecords">${evoReviewProblemsHTML(m,p)}</div>
      </details>
      <footer class="evo-review-footer">
        <span>${evoReviewIcon('history')}<span id="evoLiveVersion">${p?'正在核对线上版本':'尚无正文修改'}</span></span>
        <div>${asset?`
          <button type="button" class="kb-btn quiet small" data-evo-queue="${escapeHTML(asset.assetKind)}" data-id="${escapeHTML(asset.assetId)}">${evoReviewIcon('queue')}后续建议 ${evoFollowupCount(group)}</button>
          <button type="button" class="kb-btn quiet small" data-evo-asset="${escapeHTML(asset.assetKind)}" data-id="${escapeHTML(asset.assetId)}">${evoReviewIcon('history')}版本</button>`:''}${p?`
          <button type="button" class="kb-btn quiet small" data-evo-process="${escapeHTML(p.id)}">${evoReviewIcon('queue')}处理过程</button>
          <button type="button" class="kb-btn quiet small" data-evo-detail-refresh aria-label="刷新状态">${evoReviewIcon('refresh')}</button>`:''}
        </div>
      </footer>
      ${group?.conflict?'<p class="evo-bad evo-conflict-note">同一对象存在并行修改，试用和发布已禁用。</p>':''}
      <div id="evoTrialHost"></div>
    </div>`;
}
function evoStoryUpdateSelection(m){
  const host=$('#evoStorySelectedSample');
  if(!host)return;
  const replays=m.proposal?.replays||[];
  const index=m.evidence?.type==='replay'?replays.findIndex(r=>r.id===m.evidence.id):-1;
  host.hidden=index<0;
  host.textContent=index<0?'':evoStorySampleName(m,replays[index],index);
}
function evoStoryParseJSON(text){
  if(typeof text!=='string'||text.length>32768)return null;
  try{const v=JSON.parse(text.trim().replace(/^```(?:json)?\s*\n([\s\S]*?)\n```$/i,'$1'));return v&&typeof v==='object'&&!Array.isArray(v)?v:null}catch{return null}
}
// A line diagram is only valid when both outputs point verbatim into the same
// input. No log wording or test names are used to infer correctness.
function evoStoryLogEvidence(input,before,after){
  const value=evoStoryParseJSON(input),a=evoStoryParseJSON(before),b=evoStoryParseJSON(after),lines=value?.lines;
  if(!Array.isArray(lines)||!lines.length||lines.length>2000||!lines.every(l=>typeof l==='string'))return null;
  const valid=v=>Number.isInteger(v?.evidence?.line)&&v.evidence.line>0&&v.evidence.line<=lines.length&&typeof v.evidence.text==='string'&&v.evidence.text===lines[v.evidence.line-1];
  return valid(a)&&valid(b)?{lines,before:a.evidence.line,after:b.evidence.line}:null;
}
function evoStoryChosenNode(m,data){
  const nodes=data.comparison?.nodes||[];
  return nodes.find(n=>n.id===m.evidenceNode)||nodes.find(n=>n.shaped&&!n.reused)||nodes.find(n=>n.changed)||nodes[0];
}
function evoStoryOutputs(m,data){
  const chosen=evoStoryChosenNode(m,data),id=chosen?.id;
  const artifact=data.artifactEvidence?.[id]?.pairs?.find(x=>x.before!==undefined&&x.after!==undefined);
  return {chosen,before:artifact?.before??data.source?.run?.nodes?.[id]?.output,after:artifact?.after??data.candidate?.run?.nodes?.[id]?.output,name:artifact?.name||'节点输出',artifact};
}
function evoStoryBehaviorHTML(m,data){
  if(data.record)return `<div class="evo-story-behavior-copy"><h3>问题运行</h3><p>${escapeHTML(evoStoryBrief(data.record.detail||'暂无问题摘要',140))}</p>${evoEvidenceRunLink(data.record.runId,'打开完整运行 ↗')}</div>`;
  const {chosen,before,after,name,artifact}=evoStoryOutputs(m,data),source=data.source||{},candidate=data.candidate||{};
  const changed=source.run&&(data.replay.sourceStatus&&source.run.status!==data.replay.sourceStatus||data.replay.sourceScore===0&&source.run.status==='succeeded');
  const sameInput=source.run&&candidate.run&&typeof source.run.input==='string'&&source.run.input===candidate.run.input;
  const warning=(changed?'<p class="evo-story-record-warning">原运行已重试；下方是当前读取的记录，历史评分保持原判定。</p>':'')+(source.run&&candidate.run&&!sameInput?'<p class="evo-story-record-warning">两次运行的输入不同，请展开原始输入核对。</p>':'');
  const log=sameInput&&!chosen?.reused&&!source.error&&!candidate.error?evoStoryLogEvidence(source.run.input,before,after):null;
  const context=`<div class="evo-story-evidence-meta"><span>${escapeHTML(data.comparison.workflowName||'历史样例')}${chosen?.name&&chosen.name!==chosen.id?' · '+escapeHTML(chosen.name):''}</span><span>${artifact?'产物 · ':''}${escapeHTML(name)}</span></div>`;
  const input=sameInput?`<details class="evo-story-sample-input"><summary>本次运行输入</summary><pre class="evo-output-excerpt">${escapeHTML(source.run.input.length>1200?source.run.input.slice(0,1200)+'…':source.run.input)}</pre></details>`:'';
  if(log){
    const keep=new Set();[log.before,log.after].forEach(n=>{for(let i=Math.max(1,n-1);i<=Math.min(log.lines.length,n+1);i++)keep.add(i)});
    let previous=0;const rows=[...keep].sort((a,b)=>a-b).map(n=>{const gap=n>previous+1?`<div class="evo-story-log-gap">… ${n-previous-1} 行</div>`:'';previous=n;const old=n===log.before,next=n===log.after;return gap+`<div class="evo-story-log-line ${old&&next?'is-both':old?'is-before':next?'is-after':''}"><span>${n}</span><code>${escapeHTML(log.lines[n-1])}</code><small>${old&&next?'前后相同':old?'原版定位':next?'修改后定位':''}</small></div>`}).join('');
    return `${context}${warning}<div class="evo-story-log-caption"><b>同一段输入日志</b><span><i class="evo-bad">L${log.before}</i>${log.before===log.after?'=':evoReviewIcon('arrow-right')}<i class="evo-good">L${log.after}</i></span></div><div class="evo-story-log">${rows}${previous<log.lines.length?`<div class="evo-story-log-gap">… ${log.lines.length-previous} 行</div>`:''}</div>`;
  }
  const delta=!source.error&&!candidate.error&&!chosen?.reused?evoReviewJSONChanges(before,after):null;
  if(delta)return `${context}${warning}${input}<div class="evo-story-fields">${delta.slice(0,3).map(row=>`<section><h3>${escapeHTML(row.path)}</h3><div class="is-before"><small>原运行</small><code>${escapeHTML(evoStoryBrief(row.before===undefined?'未设置':row.before,160))}</code></div>${evoReviewIcon('arrow-right')}<div class="is-after"><small>修改稿回放</small><code>${escapeHTML(evoStoryBrief(row.after===undefined?'未设置':row.after,160))}</code></div></section>`).join('')||'<p class="evo-story-empty">当前结构化输出相同，历史判定见下方验证表。</p>'}</div>${delta.length>3?`<p class="evo-story-note">另有 ${delta.length-3} 个字段变化，展开运行证据查看。</p>`:''}`;
  const output=(run,side)=>run?.nodes?.[chosen?.id]?evoReviewOutputHTML(run.nodes[chosen.id]):evoNodeSideHTML(side);
  return `${context}${warning}${input}<div class="evo-story-node-pair"><section><h3>原运行 · 最新</h3>${source.error?`<span class="evo-muted">${escapeHTML(source.error)}</span>`:output(source.run,chosen?.source)}</section>${evoReviewIcon('arrow-right')}<section><h3>修改稿回放</h3>${candidate.error?`<span class="evo-muted">${escapeHTML(candidate.error)}</span>`:chosen?.reused?'<span class="evo-muted">沿用原结果 · 此节点未重跑</span>':output(candidate.run,chosen?.replay)}</section></div><p class="evo-story-note">${data.artifactEvidence?.[chosen?.id]?.loading?'正在读取结构化产物…':artifact?'产物未形成可展示的结构化差异。':'展开运行证据查看输入、输出与判定理由。'}</p>`;
}
function evoStoryRenderBehavior(m,data){
  const host=$('#evoStoryBehavior');if(!host||evo.modal!==m)return;
  host.innerHTML=evoStoryBehaviorHTML(m,data);evoStoryUpdateSelection(m);
  const artifacts=$('#evoStoryArtifactEvidence');
  if(artifacts)artifacts.innerHTML=evoStoryArtifactEvidenceHTML(m,data);
  window.AgentFlowHarnessIcons?.hydrate($('#evoReviewWorkbench'));
}
function evoStoryArtifactEvidenceHTML(m,data){
  const slot=data.artifactEvidence?.[evoStoryChosenNode(m,data)?.id];
  if(!slot)return '';
  if(slot.loading)return '<p class="evo-muted" role="status">正在读取结构化产物…</p>';
  return slot.pairs.map(pair=>`<details class="evo-review-input"><summary>结构化产物 · ${escapeHTML(pair.name)}</summary><div class="evo-output-pair"><section><h3>原运行产物</h3>${pair.before===undefined?'<p class="evo-muted">产物不可用，请打开完整运行查看。</p>':`<pre class="evo-output-excerpt">${escapeHTML(pair.before)}</pre>`}</section><section><h3>回放产物</h3>${pair.after===undefined?'<p class="evo-muted">产物不可用，请打开完整运行查看。</p>':`<pre class="evo-output-excerpt">${escapeHTML(pair.after)}</pre>`}</section></div></details>`).join('');
}
function evoStoryArtifactPairs(source,candidate,nodeID){
  const artifacts=run=>(run?.artifacts||[]).filter(a=>a.id&&a.nodeId===nodeID&&((a.mediaType||'').split(';')[0]==='application/json'||/\.json$/i.test(a.path||''))&&Number.isFinite(a.size)&&a.size>=0&&a.size<=32768&&(!run.nodes?.[nodeID]?.attempt||a.attempt===run.nodes[nodeID].attempt));
  const pairs=[];
  for(const a of artifacts(source)){
    const matches=artifacts(candidate).filter(b=>a.name===b.name&&a.path===b.path);
    // Ambiguous artifact registrations are kept in the raw run view.
    if(matches.length!==1||artifacts(source).filter(x=>x.name===a.name&&x.path===a.path).length!==1)continue;
    pairs.push({name:a.path||a.name,source:a,candidate:matches[0]});if(pairs.length===3)break;
  }
  return pairs;
}
async function evoStoryReadArtifact(runID,artifact){
  const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),10000);
  try{
    const res=await fetch(`/api/runs/${encodeURIComponent(runID)}/artifacts/${encodeURIComponent(artifact.id)}`,{signal:controller.signal});
    if(!res.ok)throw new Error(`HTTP ${res.status}`);
    if(Number(res.headers.get('content-length'))>32768)throw new Error('产物过大');
    const reader=res.body?.getReader();if(!reader)throw new Error('当前浏览器无法读取产物');
    const chunks=[];let size=0;
    try{while(true){const {done,value}=await reader.read();if(done)break;size+=value.byteLength;if(size>32768)throw new Error('产物过大');chunks.push(value)}}finally{await reader.cancel().catch(()=>{});reader.releaseLock()}
    const bytes=new Uint8Array(size);let offset=0;for(const chunk of chunks){bytes.set(chunk,offset);offset+=chunk.byteLength}
    const text=new TextDecoder().decode(bytes);if(!evoStoryParseJSON(text))throw new Error('不是结构化 JSON 产物');return text;
  }finally{clearTimeout(timer)}
}
async function evoStoryLoadArtifacts(m,data){
  if(!data.replay||data.source?.error||data.candidate?.error)return;
  const chosen=evoStoryChosenNode(m,data);if(!chosen||chosen.reused)return;
  data.artifactEvidence||={};if(data.artifactEvidence[chosen.id])return;
  const pairs=evoStoryArtifactPairs(data.source.run,data.candidate.run,chosen.id);if(!pairs.length)return;
  const slot=data.artifactEvidence[chosen.id]={loading:true,pairs:[]};
  const current=()=>evo.modal===m&&m.projectId===evoProjectId()&&m.evidenceData===data&&m.evidenceNode===chosen.id&&evoActive();
  if(current())evoStoryRenderBehavior(m,data);
  slot.pairs=await Promise.all(pairs.map(async pair=>{
    const sides=await Promise.allSettled([evoStoryReadArtifact(data.source.run.id,pair.source),evoStoryReadArtifact(data.candidate.run.id,pair.candidate)]);
    return {name:pair.name,before:sides[0].status==='fulfilled'?sides[0].value:undefined,after:sides[1].status==='fulfilled'?sides[1].value:undefined};
  }));
  slot.loading=false;if(current())evoStoryRenderBehavior(m,data);
}
document.addEventListener('click',event=>{
  const trigger=event.target.closest?.('[data-evo-story-replay],[data-evo-story-row]');
  const m=evo.modal;
  if(!trigger||!m)return;
  const row=trigger.closest('[data-evo-story-row]');
  const button=row?.querySelector('[data-evo-story-replay]')||trigger;
  const id=button.dataset.evoStoryReplay;
  if(!id||button.disabled)return;
  const evidence=$('#evoStoryBehavior')?.closest('.evo-story-behavior');
  const history=row?.closest('#evoDetailPanel-validation');
  const stacked=evidence&&history&&evidence.getBoundingClientRect().top+1<history.getBoundingClientRect().top;
  button.focus({preventScroll:true});
  m.evidenceTouched=true;
  evoSelectEvidence({type:'replay',id},true);
  if(stacked){
    evidence.scrollIntoView?.({block:'nearest',behavior:matchMedia('(prefers-reduced-motion: reduce)').matches?'instant':'smooth'});
  }
});
