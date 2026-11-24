// 任务说明、历史验证及逐条样例。
// 图标与组件沿用 Harness UI（MIT），见 harness-ui-LICENSE.txt。
function evoStoryBrief(value,limit=72){
  const text=String(value||'').trim().replace(/\s+/g,' ');
  const sentence=text.match(/^.*?[。！？]/u)?.[0]?.trim()||text;
  return [...sentence].length>limit?[...sentence].slice(0,limit).join('')+'…':sentence;
}
function evoStoryProblemText(m,p){
  const row=m.row;
  const raw=[p?.review?.problem,row?.title,row?.f?.summary,p?.title].find(value=>value?.trim()&&!/^\s*[·•]/.test(value));
  if(!/^agent submitted terminal failure:/i.test(raw?.trim()||''))return evoProblemText(raw);
  const check=evoStoryCurrentIssues(m,p).find(issue=>/^verification\s+.+?\s+command(?:\s|$)/i.test(issue.c.label||''));
  return check?.title||EVO_SIGNALS[row?.c?.kind]||'节点执行失败';
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
    return `<tr data-evo-story-row="${escapeHTML(r.id)}" class="${selected?'is-selected':''}"><td><button type="button" aria-haspopup="dialog" aria-controls="evoEvidenceDialog" data-evo-story-replay="${escapeHTML(r.id)}" aria-pressed="${selected}" data-tooltip="${escapeHTML(r.sourceRunId||'原运行不可用')}">${evoReviewIcon(r.group==='pinned'?'pin':'doc')}<span>${escapeHTML(evoStorySampleName(m,r,all.indexOf(r)))}<small>${escapeHTML(EVO_GROUPS[r.group]?.[0]||r.group||'历史样例')}</small></span></button></td><td><span class="evo-story-score">${score(r.sourceScore,Number.isInteger(r.sourceScore)&&r.sourceScore>=0&&r.sourceScore<=2)}</span></td><td><span class="evo-story-score">${score(r.score,ready)}</span></td><td><span class="${result[1]}">${result[0]}</span></td></tr>`;
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
  const summary=evoStoryProblemText(m,p);
  const runs=new Set([...(f?.sampleRunIds||[]),...issues.flatMap(r=>(r.c.occurrences||[]).map(o=>o.runId))].filter(Boolean));
  const failed=t?.kinds?.node_failed??(t?.kinds?0:undefined);
  const rate=t?.runs>0&&Number.isFinite(failed)&&failed>=0&&failed<=t.runs?evoPct(failed/t.runs):'';
  const facts=[
    runs.size?`<div><dt>问题运行</dt><dd>${runs.size} 次</dd></div>`:'',
    rate?`<div><dt title="本次诊断窗口内当前资产版本的失败运行占比">当前失败率</dt><dd>${rate}</dd></div>`:''
  ].join('');
  const short=evoStoryBrief(summary,34),detail=evoStoryBrief(summary,72);
  if(detail===short&&!facts)return '';
  return `<section id="evoProblemOverview" class="evo-story-section evo-story-overview">
    <div><h2>${evoReviewIcon('doc')}问题与影响</h2>${detail!==short?`<p>${escapeHTML(detail)}</p>`:''}</div>
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
function evoStoryReplayResultHTML(p){
  const s=p?.summary,rows=p?.replays||[],total=s?.total||rows.length;
  const finished=s?.finished??rows.filter(r=>evoTime(r.finishedAt)).length;
  const paired=rows.filter(r=>evoTime(r.finishedAt)&&r.outcome&&r.outcome!=='blocked'&&
    [r.sourceScore,r.score].every(n=>Number.isInteger(n)&&n>=0&&n<=2));
  const before=paired.filter(r=>r.sourceScore>0).length,after=paired.filter(r=>r.score>0).length;
  const status=s?.passed?kbBadge('回放通过','ok'):p?.status==='validating'?kbBadge('回放中','info'):
    total&&finished>=total&&s?kbBadge('回放未通过','err'):'';
  const facts=[['改善',s?.fixed,'evo-good'],['退化',s?.regressed,'evo-bad'],['保持',s?.unchanged,''],['阻塞',s?.blocked,'evo-review-warn'],['结论变化',s?.changed,'']].filter(([,count])=>count>0);
  return `<div class="evo-story-replay-result">
    ${paired.length?`<div class="evo-story-replay-pair" aria-label="原版与修改稿的回放结果">
      <span>原版 <b>${before}/${paired.length}</b> 成功</span>
      ${evoReviewIcon('arrow-right')}
      <span>修改稿 <b class="${after>before?'evo-good':after<before?'evo-bad':''}">${after}/${paired.length}</b> 成功</span>
    </div>`:`<span class="evo-muted">${total?'暂无可对比的回放结果':'暂无回放结果'}</span>`}
    <div class="evo-story-replay-facts">${status}
      ${total&&(!paired.length||paired.length!==total)?`<span class="evo-muted">完成 ${finished}/${total} 条</span>`:''}
      ${facts.map(([label,count,tone])=>`<span class="${tone}">${label} ${count}</span>`).join('')}
    </div>
  </div>`;
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
      ${problems||m.row?`<details id="evoProblemEvidenceDetails" class="evo-story-expand">
        <summary>查看问题证据</summary>
        ${problems}
        ${m.row?`<details id="evoSourceDetails" class="evo-detail-more">
          <summary>查看关联运行</summary>
          <div id="evoProblemRecords">${evoReviewProblemsHTML(m,p)}</div>
        </details>`:''}
      </details>`:''}
      <section id="evoFullChange" class="evo-story-section"><div id="evoLinkedChange">${evoReviewChangeHTML(m,p)}</div></section>
      <section id="evoValidationResults" class="evo-story-section evo-story-validation">
        <header class="evo-story-section-head"><h2>${evoReviewIcon('flask')}回放结果</h2></header>
        ${evoStoryReplayResultHTML(p)}
        ${p?.replays?.length?`<details id="evoValidationDetails" class="evo-story-expand">
          <summary>查看回放样例</summary>
          <section id="evoDetailPanel-validation">${evoStoryValidationHTML(p)}</section>
        </details>`:''}
      </section>
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
function evoStoryEvidenceDialogHTML(){
  return `<dialog id="evoEvidenceDialog" aria-labelledby="evoEvidenceTitle">
    <div class="evo-story-dialog-inner">
      <header class="evo-story-dialog-head">
        <div><span id="evoEvidenceCount"></span><h2 id="evoEvidenceTitle"></h2></div>
        <button type="button" class="kb-btn quiet small" data-evo-evidence-close aria-label="关闭样例">${evoReviewIcon('close')}</button>
      </header>
      <div id="evoStoryBehavior" aria-live="polite"></div>
      <details id="evoEvidenceDetails" class="evo-story-evidence-details">
        <summary>运行输入与原始记录</summary>
        <div id="evoEvidenceBody" class="evo-evidence-panel" aria-live="polite"></div>
      </details>
    </div>
  </dialog>`;
}
function evoStoryUpdateSelection(m){
  const host=$('#evoEvidenceTitle');if(!host)return;
  const selected=m.evidence,replays=m.proposal?.replays||[];
  const index=selected?.type==='replay'?replays.findIndex(r=>r.id===selected.id):-1;
  const record=selected?.type==='run'?m.data?.items?.find(r=>r.runId===selected.id):null;
  host.textContent=index>=0?evoStorySampleName(m,replays[index],index):record?.issueTitle||'问题运行';
}
function evoOpenStoryEvidence(selection,button){
  const m=evo.modal,dialog=$('#evoEvidenceDialog');if(!m||!dialog||dialog.open)return;
  m.evidenceOpener=button||document.activeElement;
  $('#evoEvidenceDetails').open=false;
  m.evidenceNode='';
  dialog.showModal();
  $('[data-evo-evidence-close]')?.focus();
  evoSelectEvidence(selection);
}
function evoCloseStoryEvidence(m){
  if(evo.modal!==m||$('#evoEvidenceDialog')?.open)return;
  const selected=m.evidence,opener=m.evidenceOpener;
  m.evidence=null;m.evidenceRequest=null;m.evidenceData=null;m.evidenceOpener=null;
  evoRenderComparisonList(m);
  const row=selected?.type==='replay'&&$('#evoComparisonList')?.querySelector(`[data-evo-story-replay="${CSS.escape(selected.id)}"]`);
  (opener?.isConnected?opener:row||$('#evoValidationDetails > summary')||$('#evoProblemEvidenceDetails > summary'))?.focus({preventScroll:true});
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
function evoStorySampleSummary(m,data){
  const {replay,record,source,candidate}=data;
  if(record)return evoStoryBrief(evoProblemText(record.detail?.replace(/^agent submitted terminal failure:\s*/i,'')||record.issueTitle),80)||'暂无问题摘要';
  if(replay.outcome==='blocked')return '这条样例未能完成回放。';
  if(!evoTime(replay.finishedAt)||!replay.outcome)return '回放结果待判定。';
  const {chosen,before,after}=evoStoryOutputs(m,data);
  const input=source.run?.input;
  const sameInput=typeof input==='string'&&input===candidate.run?.input;
  const current=!evoReplaySourceChanged(replay,source);
  if(chosen&&!chosen.reused&&sameInput&&current&&!source.error&&!candidate.error){
    const log=evoStoryLogEvidence(input,before,after);
    if(log&&log.before!==log.after)return `日志定位从第 ${log.before} 行移到第 ${log.after} 行：${evoStoryBrief(log.lines[log.after-1],52)}`;
    const changes=evoReviewJSONChanges(before,after);
    const field=changes?.find(change=>change.path==='reason'&&change.after)||changes?.find(change=>!change.path.startsWith('evidence.'));
    if(field){
      const label={reason:'判定理由',decision:'判定结论',category:'分类',queue:'分流队列'}[field.path]||`「${field.path}」`;
      const brief=value=>evoStoryBrief(value,30);
      if(field.before===undefined)return `补充了${label}：${brief(field.after)}`;
      if(field.after===undefined)return `移除了${label}。`;
      return `${label}从「${brief(field.before)}」改为「${brief(field.after)}」。`;
    }
    if(changes?.length===0)return '这条样例的结构化输出保持一致。';
  }
  const point=evoStoryPoints(m.proposal).find(change=>change.title&&change.sourceRunIds?.includes(replay.sourceRunId));
  if(point&&replay.outcome==='fixed')return `修改要点：${evoStoryBrief(point.title,48)}`;
  return {
    fixed:'这条样例的运行结果改善。',
    unchanged:'这条样例的运行结果保持一致。',
    changed:'回放改变了判定结论。',
    regressed:'这条样例的运行结果退化。'
  }[replay.outcome]||'暂无可确认的修复摘要。';
}
function evoStorySampleResultHTML(replay){
  if(!replay)return '';
  const before=evoHistoricalScore(replay.sourceScore);
  const after=replay?.outcome&&evoTime(replay.finishedAt)&&replay.outcome!=='blocked'?evoHistoricalScore(replay.score):'未完成';
  return `<div class="evo-story-dialog-result">
    <span>原运行 <b>${escapeHTML(before)}</b></span>${evoReviewIcon('arrow-right')}<span>回放 <b>${escapeHTML(after)}</b></span>
  </div>`;
}
function evoStoryRenderBehavior(m,data){
  const host=$('#evoStoryBehavior');if(!host||evo.modal!==m)return;
  host.innerHTML=`${evoStorySampleResultHTML(data.replay)}<h3>${data.replay?'变化摘要':'问题摘要'}</h3><p>${escapeHTML(evoStorySampleSummary(m,data))}</p>`;
  evoStoryUpdateSelection(m);
  const artifacts=$('#evoStoryArtifactEvidence');
  if(artifacts)artifacts.innerHTML=evoStoryArtifactEvidenceHTML(m,data);
  window.AgentFlowHarnessIcons?.hydrate(host);
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
  const current=()=>evo.modal===m&&m.projectId===evoProjectId()&&m.evidenceData===data&&m.evidenceNode===chosen.id&&evoActive()&&$('#evoEvidenceDialog')?.open!==false;
  if(current())evoStoryRenderBehavior(m,data);
  slot.pairs=await Promise.all(pairs.map(async pair=>{
    const sides=await Promise.allSettled([evoStoryReadArtifact(data.source.run.id,pair.source),evoStoryReadArtifact(data.candidate.run.id,pair.candidate)]);
    return {name:pair.name,before:sides[0].status==='fulfilled'?sides[0].value:undefined,after:sides[1].status==='fulfilled'?sides[1].value:undefined};
  }));
  slot.loading=false;if(current())evoStoryRenderBehavior(m,data);
}
document.addEventListener('click',event=>{
  const dialog=$('#evoEvidenceDialog');
  if(dialog?.open&&(event.target===dialog||event.target.closest?.('[data-evo-evidence-close]'))){
    dialog.close();return;
  }
  const trigger=event.target.closest?.('[data-evo-story-replay],[data-evo-story-row]');
  if(!trigger||!evo.modal)return;
  const row=trigger.closest('[data-evo-story-row]');
  const button=row?.querySelector('[data-evo-story-replay]')||trigger;
  if(!button.disabled)evoOpenStoryEvidence({type:'replay',id:button.dataset.evoStoryReplay},button);
});
