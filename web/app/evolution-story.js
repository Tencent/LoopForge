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
const EVO_STORY_OUTCOMES=[
  ['fixed','改善','ok','arrow-up'],['unchanged','保持','','arrow-right'],
  ['regressed','退化','err','arrow-up'],['changed','结论变化','info','refresh'],
  ['blocked','阻塞','warn','alert']
];
function evoStoryHasScore(score){return Number.isInteger(score)&&score>=0&&score<EVO_SCORES.length}
function evoStoryEffectHTML(p){
  const rows=p?.replays||[];
  if(!rows.length)return '';
  const pairs=rows.filter(r=>evoTime(r.finishedAt)&&r.outcome&&r.outcome!=='blocked'&&
    evoStoryHasScore(r.sourceScore)&&evoStoryHasScore(r.score));
  const number=value=>new Intl.NumberFormat('zh-CN',{maximumFractionDigits:1}).format(value);
  const token=value=>value>=1000?`${number(value/1000)}k`:number(value);
  const duration=ms=>{
    const seconds=ms/1000;
    return seconds<60?`${number(seconds)}s`:`${Math.floor(seconds/60)}m ${Math.floor(seconds%60)}s`;
  };
  const metrics=[
    ['一次成功率',r=>Number(r.sourceScore===2),r=>Number(r.score===2),n=>`${number(n*100)}%`,true],
    ['平均 Token',r=>r.sourceMeasurements?.tokens,r=>r.replayMeasurements?.tokens,token],
    ['平均工具调用',r=>r.sourceMeasurements?.toolCalls,r=>r.replayMeasurements?.toolCalls,n=>`${number(n)} 次`],
    ['平均执行耗时',r=>r.sourceMeasurements?.executionMs,r=>r.replayMeasurements?.executionMs,duration]
  ];
  const average=values=>values.reduce((sum,n)=>sum+n,0)/values.length;
  const width=(value,scale)=>Math.max(0,Math.min(100,value/scale*100));
  const cards=metrics.map(([label,before,after,format,rate])=>{
    const measured=pairs.map(r=>[before(r),after(r)]).filter(([a,b])=>Number.isFinite(a)&&Number.isFinite(b)&&a>=0&&b>=0);
    if(!measured.length)return `<article class="evo-effect-card"><h3>${label}</h3><div class="evo-effect-empty">暂无</div></article>`;
    const a=average(measured.map(row=>row[0])),b=average(measured.map(row=>row[1]));
    const change=rate?(b-a)*100:a?(b/a-1)*100:null;
    const sign=change===null?null:Math.round(change*10)/10;
    const tone=sign&&((rate?sign:-sign)>0)?'ok':sign?'err':'';
    const scale=rate?1:Math.max(1,a,b);
    return `<article class="evo-effect-card">
      <header><h3>${label}</h3>${sign!==null?kbBadge(`${sign>0?'+':''}${number(sign)}${rate?' pp':'%'}`,tone):''}</header>
      <div class="evo-effect-value" aria-label="原运行 ${format(a)}，回放 ${format(b)}"><span>${format(a)}</span><span aria-hidden="true">→</span><b>${format(b)}</b></div>
      <div class="evo-effect-bars" data-tone="${tone}" aria-hidden="true"><i style="width:${width(a,scale)}%"></i><i style="width:${width(b,scale)}%"></i></div>
      ${measured.length<pairs.length?`<small>${measured.length} 条有记录</small>`:''}
    </article>`;
  }).join('');
  return `<section id="evoStoryEffects" class="evo-story-section" aria-labelledby="evoEffectTitle">
    <header class="evo-story-section-head"><h2 id="evoEffectTitle">${evoReviewIcon('chart')}修改效果</h2><span class="evo-muted" data-tooltip="消耗和耗时按回放执行范围对比，复用节点在两侧均排除。">${pairs.length}/${rows.length} 条可对比 · 回放</span></header>
    <div class="evo-effect-grid">${cards}</div>
  </section>`;
}
function evoStoryReplayTable(m){
  const all=m.proposal?.replays||[],list=all.filter(r=>m.compareFilter==='all'||r.outcome===m.compareFilter);
  if(!list.length)return '<p class="evo-story-empty">暂无</p>';
  return `<table class="evo-story-results">
    <caption class="evo-review-sr">验证时保存的原运行与回放结果</caption>
    <thead><tr><th scope="col">案例</th><th scope="col">结果</th></tr></thead>
    <tbody>${list.map(r=>{
      const [key,label,tone,icon]=EVO_STORY_OUTCOMES.find(([key])=>key===r.outcome)||['pending','待判定','','clock'];
      const selected=m.evidence?.type==='replay'&&m.evidence.id===r.id;
      return `<tr data-evo-story-row="${escapeHTML(r.id)}" class="${selected?'is-selected':''}">
        <td><button type="button" aria-haspopup="dialog" aria-controls="evoEvidenceDialog" data-evo-story-replay="${escapeHTML(r.id)}" aria-pressed="${selected}">
          <span class="evo-story-case-mark">${evoReviewIcon(r.group==='pinned'?'pin':'doc')}</span>
          <span><b>${escapeHTML(evoStorySampleName(m,r,all.indexOf(r)))}</b></span>
        </button></td>
        <td class="evo-story-outcome" data-outcome="${key}">${kbBadge(`${evoReviewIcon(icon)}${label}`,tone)}</td>
      </tr>`;
    }).join('')}</tbody>
  </table>`;
}
function evoStoryValidationHTML(p,filter='all'){
  const title=`<h2 id="evoReplayTitle" tabindex="-1"><span class="evo-story-case-mark">${evoReviewIcon('flask')}</span>回放案例</h2>`;
  const s=p?.summary,rows=p?.replays||[];
  if(!rows.length)return `${title}<p class="evo-story-empty">暂无</p>`;
  const status=s?.passed?kbBadge('验证通过','ok'):p.status==='validating'?kbBadge('回放中','info'):
    s&&s.finished>0&&s.finished>=s.total?kbBadge('验证未通过','err'):'';
  const counts=EVO_STORY_OUTCOMES.map(([key,label,tone,icon])=>[key,label,tone,icon,s?.[key]??rows.filter(r=>r.outcome===key).length]);
  return `${title}
    <div class="evo-story-validation-toolbar">
      <div class="evo-story-validation-summary">
        ${!status||p.status==='validating'?`<span>已完成 <b>${s?.finished??rows.filter(r=>evoTime(r.finishedAt)).length}/${s?.total||rows.length}</b></span>`:''}
        ${status}
      </div>
      <div class="evo-story-counts" role="group" aria-label="按回放结果筛选">
        ${[['all','全部','','',rows.length],...counts.filter(([key,,,,count])=>['fixed','unchanged','regressed'].includes(key)||count||filter===key)].map(([key,label,,icon,count])=>
          `<button type="button" data-evo-sample-filter="${key}" data-outcome="${key}"${count?'':' data-empty'} aria-pressed="${filter===key}">
            ${icon?evoReviewIcon(icon):''}${label} ${count}
          </button>`
        ).join('')}
      </div>
    </div>
    <div id="evoComparisonList"></div>
    <footer class="evo-story-validation-foot">
      <details><summary>案例从哪来</summary><ul><li>诊断：从问题运行中选，供改进器参考。</li><li>留出：也来自问题运行，仅用于回放验证。</li><li>回归：从近期成功的运行中选。</li><li>钉选：在运行详情手动固定的成功运行。</li></ul></details>
      <details><summary>判定口径</summary><p>比较创建验证时的运行评分：失败、返工后成功、一次成功。保持不表示产物相同；历史结果不代表线上效果。</p>${s?.verdict?`<p>${escapeHTML(evoResultText(s.verdict))}</p>`:''}</details>
    </footer>`;
}
const EVO_STORY_SOURCES=[
  ['node_failed','alert','失败案例'],['rework','refresh','返工案例'],
  ['user_feedback','user','用户反馈'],['eval_low','chart','评测低分'],['sop_detection','alert','SOP 自动检测']
];
function evoFindingPriorityHTML(finding){
  const label={high:'高',medium:'中',low:'低'}[finding?.severity];
  return label?`<span class="evo-priority" data-severity="${finding.severity}" aria-label="${label}优先级">${label}</span>`:'';
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
    <div class="evo-story-signals">${cards.join('')||`<article class="evo-story-signal">
      <h3>${evoReviewIcon('alert')}关联问题 <b>${issues.length} 类</b></h3>
      <button type="button" class="kb-btn quiet small" data-evo-open-sources aria-controls="evoSourceDetails">查看记录 ${evoReviewIcon('arrow-right')}</button>
    </article>`}</div>
    <div id="evoStorySourceExample" class="evo-story-source-example"${expanded?'':' hidden'}>${expanded}</div>
  </section>`;
}
function evoStoryReleaseAdvice(m,p){
  const scores=p.releaseEvaluation?.scores,canary=p.canary,version=m.liveVersion;
  const review=scores&&(scores.after<scores.threshold?'修改稿低于评测通过线。':scores.after<scores.before?'修改稿评分低于原版。':'');
  if(p.status==='released')return p.watch?.status==='degraded'?['err','建议回滚','发布后检测到退化。']:['ok','已发布',''];
  if(p.status==='rolled_back')return ['','已回滚',''];
  if(p.status==='rejected')return ['','未采用',''];
  if(p.status==='failed')return ['err','暂不发布',canary?.status==='failed'?'试用检测到退化。':'查看上方失败原因。'];
  if(p.status==='drafting'||p.status==='validating')return ['info','等待回放验证',''];
  if(!p.summary?.passed)return ['warn','暂不发布','回放尚未通过。'];
  if(!p.candidateContent)return ['warn','暂不发布','暂无修改稿。'];
  if(evoGroupForAsset(p)?.conflict)return ['warn','暂不发布','此对象有并行修改。'];
  if(version?.liveHash&&version.liveHash!==p.baseHash)return ['err','暂不发布','线上正文已变化，请重新生成修改。'];
  if(p.status==='canary'&&canary?.status!=='passed'){
    return ['warn','继续试用',canary?.minRuns>0?`每组达到 ${canary.minRuns} 次有效运行后判定。`:'等待试用结果。'];
  }
  if(p.summary.regressed>0)return ['warn','建议复核回放','回放中有退化案例。'];
  if(review)return ['warn','建议复核评分',review];
  if(p.status==='pending')return ['info','建议先试用','回放已通过，可用新运行验证。'];
  if(!version?.liveHash)return ['warn',m.liveVersionError?'请刷新版本':'正在核对版本',''];
  return scores?['ok','建议发布','试用通过，评测达标。']:['ok','试用通过',''];
}
function evoStoryReleaseAdviceHTML(m,p){
  const [tone,title,note]=evoStoryReleaseAdvice(m,p);
  const icon=tone==='ok'?'check':tone==='err'?'alert':'clock';
  return `<div class="evo-release-advice" data-tone="${tone}"><strong>${evoReviewIcon(icon)}${title}</strong>${note?`<p>${note}</p>`:''}</div>`;
}
function evoStoryReleaseHTML(m,p){
  const evaluation=p.releaseEvaluation,scores=evaluation?.scores;
  const replay=p.replays?.find(r=>r.id===evaluation?.replayId);
  const sample=replay?evoStorySampleName(m,replay,p.replays.indexOf(replay)):'';
  const links=evaluation?`<div class="evo-release-eval-links">${[['原运行智能评测',evaluation.sourceRunId],['回放智能评测',evaluation.replayRunId]].map(([label,id])=>
    `<a href="#/runs/${encodeURIComponent(id)}" data-evo-record-run="${escapeHTML(id)}" data-evo-run-eval>${label} ↗</a>`).join('')}</div>`:'';
  const delta=scores?scores.after-scores.before:0;
  return `<section id="evoStoryRelease" class="evo-story-section evo-story-card" aria-labelledby="evoReleaseTitle">
    <header class="evo-story-section-head"><h2 id="evoReleaseTitle">${evoReviewIcon('sparkles')}发布建议</h2></header>
    <div id="evoReleaseAdvice" role="status">${evoStoryReleaseAdviceHTML(m,p)}</div>
    <div class="evo-release-eval">
      <div class="evo-release-eval-head"><b>智能评测</b>${scores?`<span>通过线 ${scores.threshold} 分</span>`:''}</div>
      ${scores?`<div class="evo-release-score">
        <span>原版 <b>${scores.before}</b></span><span aria-hidden="true">→</span><span>修改稿 <strong>${scores.after}</strong> 分</span>
        ${kbBadge(`${delta>0?'+':''}${delta} 分`,delta>0?'ok':delta<0?'err':'')}
      </div>
      <details id="evoReleaseEvalDetails">
        <summary>查看评分明细</summary>
        <p>${[sample,scores.model].filter(Boolean).map(escapeHTML).join(' · ')}</p>
        <table><thead><tr><th scope="col">维度</th><th scope="col">权重</th><th scope="col">原版</th><th scope="col">修改稿</th></tr></thead>
          <tbody>${scores.dimensions.map(d=>`<tr><th scope="row">${escapeHTML(d.name)}</th><td>${d.weight}%</td><td>${d.before}</td><td>${d.after}</td></tr>`).join('')}</tbody>
        </table>
        ${links}
      </details>`:`<p>暂无可对比评分${links?'，请用同一模型评测两侧运行。':''}</p>${links}`}
    </div>
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
      <div class="evo-story-layout">
        <div class="evo-story-main">
          <section id="evoFullChange" class="evo-story-section evo-story-card"><div id="evoLinkedChange">${evoReviewChangeHTML(m,p)}</div></section>
          <section id="evoValidationResults" class="evo-story-validation" aria-labelledby="evoReplayTitle">
            ${evoStoryValidationHTML(p,m.compareFilter)}
          </section>
          ${problems||m.row?`<details id="evoProblemEvidenceDetails" class="evo-story-expand">
            <summary>查看问题证据</summary>
            ${problems}
            ${m.row?`<details id="evoSourceDetails" class="evo-detail-more">
              <summary>查看关联运行</summary>
              <div id="evoProblemRecords">${evoReviewProblemsHTML(m,p)}</div>
            </details>`:''}
          </details>`:''}
        </div>
        ${p?`<aside class="evo-story-rail" aria-label="修改效果与发布建议">${evoStoryEffectHTML(p)}${evoStoryReleaseHTML(m,p)}</aside>`:''}
      </div>
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
        <div id="evoEvidenceBody" aria-live="polite"></div>
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
  (opener?.isConnected?opener:row||(selected?.type==='replay'?$('#evoReplayTitle'):$('#evoProblemEvidenceDetails > summary')))?.focus({preventScroll:true});
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
  return slot.pairs.map(pair=>`<details class="evo-review-input"><summary>结构化产物 · ${escapeHTML(pair.name)}</summary><div class="evo-output-pair"><section><h3>原运行产物</h3>${pair.before===undefined?'<p class="evo-muted">产物不可用，请打开完整运行查看。</p>':`<pre class="evo-output-excerpt" tabindex="0">${escapeHTML(pair.before)}</pre>`}</section><section><h3>回放产物</h3>${pair.after===undefined?'<p class="evo-muted">产物不可用，请打开完整运行查看。</p>':`<pre class="evo-output-excerpt" tabindex="0">${escapeHTML(pair.after)}</pre>`}</section></div></details>`).join('');
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
