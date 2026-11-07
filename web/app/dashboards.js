function percent(n){return `${Number(n||0).toFixed(0)}%`}
function formatTimestamp(value){
  if(!value)return '';
  const d=new Date(value);
  if(!Number.isFinite(d.getTime())||d.getFullYear()<1970)return '';
  return d.toLocaleString();
}
function duration(ms){ms=Number(ms||0);if(!ms)return '—';if(ms<1000)return `${ms}ms`;if(ms<60000)return `${(ms/1000).toFixed(1)}s`;if(ms<3600000)return `${(ms/60000).toFixed(1)}m`;return `${(ms/3600000).toFixed(1)}h`}


const METRICS_WINDOWS={
  '7d':{days:7,label:'近 7 天',grain:'day'},
  '30d':{days:30,label:'近 30 天',grain:'day'},
  '90d':{days:90,label:'近 90 天',grain:'week'},
  '365d':{days:365,label:'近 1 年',grain:'month'},
};
function metricsDemoEnabled(){return new URLSearchParams(window.location.search).get('metricsDemo')==='1'}
function metricsDemoSnapshot(win=metricsWindow()){
  const projectId=currentProject()?.id||state.projectFilter||'platform',now=new Date(),days=[0,0,1,1,2,3,3,5,6,10,13,17,21,26,35,48,60,75,105,135,170,220,280,330],failed=new Set([5,12,18]),reworked=new Set([4,8,11,15,20]),human=new Set([3,4,8,11,15,20]),issues=[],deliveries=[],runTraces=[];
  days.forEach((daysAgo,index)=>{const n=index+1,finish=new Date(now);finish.setDate(finish.getDate()-daysAgo);finish.setMinutes(finish.getMinutes()-n*7);const cycleMin=42+(n*17)%108,start=new Date(finish.getTime()-cycleMin*60000),clarificationMin=5+(n*3)%11,developmentMin=28+(n*13)%73,testMin=7+(n*5)%18,isFailed=failed.has(n),issueId=`tapd-demo-${String(n).padStart(3,'0')}`,runId=`loop-demo-${String(n).padStart(3,'0')}`,title=['效能指标接入','测试环境稳定性','研发协作提效','工作流体验优化'][index%4]+` #${String(n).padStart(2,'0')}`,tokens=16800+(n*3700)%28900,cost=tokens/44000;
    const issue={id:issueId,projectId,title,status:isFailed?'blocked':'done',source:'tapd',workflowId:'loop-metrics-demo',latestTaskId:`task-demo-${n}`,externalRef:{source:'tapd',externalId:`116093${String(n).padStart(4,'0')}`},createdAt:start.toISOString(),updatedAt:finish.toISOString(),deliveryState:isFailed?'':'merged'};issues.push(issue);
    deliveries.push({issueId,externalId:issue.externalRef.externalId,title,source:'tapd',workflowId:'loop-metrics-demo',workflowName:'需求开发',runId,runStatus:isFailed?'failed':'succeeded',runCount:reworked.has(n)?2:1,failedRuns:reworked.has(n)||isFailed?1:0,reworks:reworked.has(n)?1:0,tokens,costCny:cost,costKnown:true,executionDurationMs:(clarificationMin+developmentMin+testMin)*60000,cycleDurationMs:cycleMin*60000,clarificationDurationMs:clarificationMin*60000,clarificationStartedAt:start.toISOString(),developmentStartedAt:new Date(start.getTime()+clarificationMin*60000).toISOString(),developmentDurationMs:developmentMin*60000,testDurationMs:testMin*60000,phaseSamples:{clarification:1,development:1,testing:1,inferred:0},firstRunAt:start.toISOString(),lastRunAt:finish.toISOString(),category:'requirement-development'});
    runTraces.push({projectId,issueId,issueSource:'tapd',externalId:issue.externalRef.externalId,workflowTaskId:`task-demo-${n}`,runId,traceId:`trace-demo-${n}`,status:isFailed?'failed':'succeeded',durationMs:cycleMin*60000,tokens,costCny:cost,costKnown:true,toolCalls:3,toolCallsSucceeded:isFailed?2:3,toolCallsFailed:isFailed?1:0,humanInterventions:human.has(n)?1:0,createdAt:start.toISOString(),finishedAt:finish.toISOString()});
  });
  [[1,''],[4,'1160950001']].forEach(([daysAgo,tapdId],index)=>{const finish=new Date(now);finish.setDate(finish.getDate()-daysAgo);const start=new Date(finish.getTime()-(index?118:92)*60000),developmentStart=new Date(start.getTime()+12*60000),issueId=`platform-demo-${index+1}`,runId=`platform-run-${index+1}`,title=index?'平台需求：手动关联 TAPD':'平台需求：未关联 TAPD',tokens=index?25300:19700,phaseEvidence=[{runId,nodeId:'clarification-to-development',stage:'clarification',source:'explicit',durationMs:12*60000,startedAt:start.toISOString(),finishedAt:developmentStart.toISOString()},...['development','testing'].map((stage,i)=>({runId,nodeId:`${stage}-${i}`,stage,source:'explicit',durationMs:[58,17][i]*60000}))];
    issues.push({id:issueId,projectId,title,kind:'requirement',status:'done',source:'local',tapdId,workflowId:'loop-metrics-demo',latestTaskId:`platform-task-${index+1}`,createdAt:start.toISOString(),updatedAt:finish.toISOString()});
    deliveries.push({issueId,externalId:tapdId,title,source:'local',workflowId:'loop-metrics-demo',workflowName:'平台需求开发',runId,runStatus:'succeeded',runCount:1,failedRuns:0,reworks:0,tokens,costCny:tokens/44000,costKnown:true,cycleDurationMs:finish-start,clarificationDurationMs:12*60000,clarificationStartedAt:start.toISOString(),developmentStartedAt:developmentStart.toISOString(),developmentDurationMs:58*60000,testDurationMs:17*60000,phaseSamples:{clarification:1,development:1,testing:1,inferred:0},phaseEvidence,firstRunAt:start.toISOString(),lastRunAt:finish.toISOString(),category:'requirement-development'});
    runTraces.push({projectId,issueId,issueSource:'local',externalId:tapdId,workflowTaskId:`platform-task-${index+1}`,runId,traceId:runId,status:'succeeded',durationMs:finish-start,tokens,costCny:tokens/44000,costKnown:true,toolCalls:3,toolCallsSucceeded:3,toolCallsFailed:0,humanInterventions:0,createdAt:start.toISOString(),finishedAt:finish.toISOString()});
  });
  [2,18,50,120].forEach((daysAgo,index)=>{const at=new Date(now);at.setDate(at.getDate()-daysAgo);issues.push({id:`tapd-demo-uncovered-${index+1}`,projectId,title:`待接入 AI 工作流 #${index+1}`,status:'open',source:'tapd',externalRef:{source:'tapd',externalId:`116094${String(index+1).padStart(4,'0')}`},createdAt:at.toISOString(),updatedAt:at.toISOString()})});
  const inWindow=value=>{const at=new Date(value);return at>=win.from&&at<=win.to},visibleDeliveries=deliveries.filter(item=>inWindow(item.lastRunAt)),visibleTraces=runTraces.filter(item=>inWindow(item.finishedAt||item.createdAt)),trendMap=new Map();
  visibleTraces.forEach(trace=>{const key=metricsBucketKey(trace.finishedAt||trace.createdAt,win.grain),point=trendMap.get(key)||{date:key,runs:0,succeeded:0,failed:0,reworks:0,tokens:0,costCny:0};point.runs++;point.tokens+=trace.tokens;point.costCny+=trace.costCny;if(trace.status==='succeeded')point.succeeded++;if(trace.status==='failed')point.failed++;trendMap.set(key,point)});
  const mrMerges=deliveries.filter(d=>d.runStatus==='succeeded').flatMap((d,index)=>Array.from({length:index%3===0?2:1},(_,repo)=>({mrProject:repo?'demo/backend':'demo/frontend',mrIid:index+1,mrUrl:`https://git.example.com/demo/${repo?'backend':'frontend'}/merge_requests/${index+1}`,targetBranch:'agentflow/integration',issueId:d.issueId,title:d.title,mergedAt:new Date(new Date(d.lastRunAt).getTime()+5*60000).toISOString()}))).filter(mr=>inWindow(mr.mergedAt));
  const succeeded=visibleTraces.filter(x=>x.status==='succeeded').length,failedRuns=visibleTraces.filter(x=>x.status==='failed').length,completed=visibleDeliveries.filter(x=>x.runStatus==='succeeded').length;
  return {issues,analytics:{mrMerges,summary:{mergedMrs:mrMerges.length,mergedMrsMissingTime:0,totalRuns:visibleTraces.length,succeededRuns:succeeded,failedRuns,generatedAt:new Date().toISOString()},trend:[...trendMap.values()].sort((a,b)=>a.date.localeCompare(b.date)),deliveries:visibleDeliveries,runTraces:visibleTraces,nodes:[],categories:[{key:'requirement-development',completedIssues:completed}],demo:{humanInterventions:visibleTraces.reduce((n,x)=>n+x.humanInterventions,0)}}};
}
function metricsWindow(){
  const spec=METRICS_WINDOWS[state.metricsRange]||METRICS_WINDOWS['30d'];
  if(state.metricsRange==='custom'&&/^\d{4}-\d{2}-\d{2}$/.test(state.metricsDateFrom)&&/^\d{4}-\d{2}-\d{2}$/.test(state.metricsDateTo)){
    let from=new Date(`${state.metricsDateFrom}T00:00:00`),to=new Date(`${state.metricsDateTo}T23:59:59.999`);
    if(from>to)[from,to]=[new Date(`${state.metricsDateTo}T00:00:00`),new Date(`${state.metricsDateFrom}T23:59:59.999`)];
    const days=Math.max(1,Math.ceil((to-from)/86400000)),grain=days<=45?'day':days<=180?'week':'month';
    const short=d=>d.toLocaleDateString([],{year:'numeric',month:'numeric',day:'numeric'});
    return {days,label:`${short(from)} – ${short(to)}`,grain,from,to,custom:true};
  }
  const to=new Date(),from=state.metricsRange==='365d'?new Date(to.getFullYear(),to.getMonth()-11,1):new Date(to.getFullYear(),to.getMonth(),to.getDate()-spec.days+1);
  from.setHours(0,0,0,0);return {...spec,from,to,custom:false};
}
function metricsDateValue(value){const d=new Date(value);return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`}
function metricsIssueSource(issue){return String(issue?.source||issue?.externalRef?.source||'').toLowerCase()}
function metricsRequirementIssue(issue){const source=metricsIssueSource(issue),kind=String(issue?.kind||'').toLowerCase();return !!issue&&(source==='local'||source==='tapd'||source==='gongfeng'||source===''||kind==='requirement'||kind==='issue')}
function metricsRequirementTAPDID(issue){const source=metricsIssueSource(issue);if(issue?.externalRef&&(source==='tapd'||String(issue.externalRef.source||'').toLowerCase()==='tapd'))return String(issue.externalRef.externalId||'').trim();if(source==='gongfeng'||issue?.externalRef)return '';return String(issue?.tapdId||'').trim()}
function metricsRequirementKey(issue){const tapdId=metricsRequirementTAPDID(issue);return tapdId?`tapd:${issue.projectId||''}:${tapdId}`:`issue:${issue.id||''}`}
function metricsIssueDone(issue){return ['done','closed','completed'].includes(String(issue?.status||'').toLowerCase())}
function metricsDateInWindow(value,win=metricsWindow()){const at=new Date(value);return Number.isFinite(at.getTime())&&at>=win.from&&at<=win.to}
function metricsISOWeekKey(value){
  const local=new Date(value),d=new Date(Date.UTC(local.getFullYear(),local.getMonth(),local.getDate()));d.setUTCDate(d.getUTCDate()+4-(d.getUTCDay()||7));const yearStart=new Date(Date.UTC(d.getUTCFullYear(),0,1));return `${d.getUTCFullYear()}-W${String(Math.ceil((((d-yearStart)/86400000)+1)/7)).padStart(2,'0')}`;
}
function metricsBucketKey(value,grain=metricsWindow().grain){const d=new Date(value);if(grain==='month')return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}`;if(grain==='week')return metricsISOWeekKey(d);return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`}
function metricsBuckets(win=metricsWindow()){
  const buckets=[];let cursor=new Date(win.from);
  if(win.grain==='month'){cursor=new Date(cursor.getFullYear(),cursor.getMonth(),1);while(cursor<=win.to){buckets.push({key:metricsBucketKey(cursor,win.grain),label:cursor.toLocaleDateString([],{year:'2-digit',month:'short'}),at:new Date(cursor)});cursor.setMonth(cursor.getMonth()+1)}}
  else if(win.grain==='week'){cursor.setHours(0,0,0,0);cursor.setDate(cursor.getDate()-((cursor.getDay()+6)%7));while(cursor<=win.to){buckets.push({key:metricsBucketKey(cursor,win.grain),label:cursor.toLocaleDateString([],{month:'numeric',day:'numeric'}),at:new Date(cursor)});cursor.setDate(cursor.getDate()+7)}}
  else{cursor.setHours(0,0,0,0);while(cursor<=win.to){buckets.push({key:metricsBucketKey(cursor,win.grain),label:cursor.toLocaleDateString([],{month:'numeric',day:'numeric'}),at:new Date(cursor)});cursor.setDate(cursor.getDate()+1)}}
  return buckets;
}
function metricsNodeStageSummary(nodes,kind){
  const patterns=kind==='frontend'?[/前端/i,/frontend/i,/\bweb\b/i,/\bui\b/i]:kind==='test'?[/测试/i,/质量/i,/验收/i,/\btest/i,/\bverify/i,/\bqa\b/i]:[/后端/i,/backend/i,/\bserver\b/i,/\bapi\b/i];let total=0,samples=0;
  (nodes||[]).forEach(node=>{const text=`${node.key||''} ${node.name||''}`;if(!patterns.some(pattern=>pattern.test(text)))return;const count=Number(node.nodeRuns||node.runs||0),avg=Number(node.averageExecutionDurationMs||0);if(count>0&&avg>0){samples+=count;total+=count*avg}});
  return {averageMs:samples?Math.round(total/samples):0,samples};
}
function metricsDeliveryPhases(delivery){
  const lanes=delivery?.lanes||[],laneDuration=names=>lanes.filter(l=>names.includes(String(l.lane||'').toLowerCase())).reduce((n,l)=>n+Number(l.executionDurationMs||0),0);
  const measured=!!delivery?.phaseSamples;
  const clarification=Number(delivery?.clarificationDurationMs||0)||(measured?0:laneDuration(['product','design']));
  const development=Number(delivery?.developmentDurationMs||0)||(measured?0:laneDuration(['frontend','backend','repair','other']));
  const testing=Number(delivery?.testDurationMs||0);
  const samples=measured?{clarification:Number(delivery.phaseSamples.clarification||0),development:Number(delivery.phaseSamples.development||0),testing:Number(delivery.phaseSamples.testing||0)}:{clarification:Number(clarification>0),development:Number(development>0),testing:Number(testing>0)};
  // Sum observed stages only; an entirely unmeasured delivery remains unknown.
  // The Run wall-clock cycle is a separate metric, not a fallback.
  samples.total=Number(samples.clarification>0||samples.development>0||samples.testing>0);
  return {clarification,development,testing,total:samples.total?clarification+development+testing:null,samples,inferred:Number(delivery?.phaseSamples?.inferred||0)};
}
function metricsAverage(values){const known=values.map(Number).filter(v=>v>0);return known.length?Math.round(known.reduce((a,b)=>a+b,0)/known.length):0}
function metricsPhaseAverage(items,key){const known=items.filter(x=>x.phases.samples[key]>0);return {value:known.length?Math.round(known.reduce((n,x)=>n+x.phases[key],0)/known.length):null,samples:known.length}}
function metricsDashboardModel(){
  const data=state.metricsData||{},mrMerges=data.mrMerges||[],mergedMRs=Number(data.summary?.mergedMrs||0),mergedMRsMissingTime=Number(data.summary?.mergedMrsMissingTime||0),issues=state.metricsIssues||[],deliveries=data.deliveries||[],traces=data.runTraces||[],issueMap=new Map(issues.map(i=>[i.id,i]));
  const projectId=currentProject()?.id||state.projectFilter||'',visibleIssues=issues.filter(i=>(!projectId||i.projectId===projectId)&&metricsRequirementIssue(i)&&metricsDateInWindow(i.updatedAt||i.createdAt));
  const aiIssueIds=new Set(deliveries.map(d=>d.issueId).filter(Boolean));
  const requirementGroups=new Map();visibleIssues.forEach(issue=>{const key=metricsRequirementKey(issue),bound=!!(issue.workflowId||issue.latestTaskId||aiIssueIds.has(issue.id)),prior=requirementGroups.get(key);if(!prior||bound&&!prior.bound)requirementGroups.set(key,{issue,bound})});
  const requirementIssues=[...requirementGroups.values()].map(x=>x.issue),covered=[...requirementGroups.values()].filter(x=>x.bound).map(x=>x.issue),linkedTAPD=requirementIssues.filter(i=>metricsRequirementTAPDID(i)).length;
  const doneRequirementKeys=new Set(issues.filter(i=>metricsRequirementIssue(i)&&metricsIssueDone(i)).map(metricsRequirementKey));
  const deliveryDone=d=>{const issue=issueMap.get(d.issueId);return metricsRequirementIssue(issue)&&doneRequirementKeys.has(metricsRequirementKey(issue))};
  const completed=deliveries.filter(deliveryDone);
  const firstPass=completed.filter(d=>Number(d.runCount||0)===1&&!Number(d.failedRuns||0)&&!Number(d.reworks||0));
  const reworked=completed.filter(d=>Number(d.runCount||0)>1||Number(d.failedRuns||0)>0||Number(d.reworks||0)>0||String(issueMap.get(d.issueId)?.deliveryState||'')==='reopened');
  const interventionByIssue=new Map();traces.forEach(t=>{if(t.issueId)interventionByIssue.set(t.issueId,(interventionByIssue.get(t.issueId)||0)+Number(t.humanInterventions||0))});
  const interventions=[...aiIssueIds].reduce((n,id)=>n+(interventionByIssue.get(id)||0),0),automated=completed.filter(d=>!(interventionByIssue.get(d.issueId)||0));
  const tokens=completed.reduce((n,d)=>n+Number(d.tokens||0),0),knownCosts=completed.filter(d=>d.costKnown),cost=knownCosts.reduce((n,d)=>n+Number(d.costCny||0),0);
  const terminal=Number(data.summary?.succeededRuns||0)+Number(data.summary?.failedRuns||0),frontend=metricsNodeStageSummary(data.nodes,'frontend'),backend=metricsNodeStageSummary(data.nodes,'backend'),test=metricsNodeStageSummary(data.nodes,'test'),win=metricsWindow();
  const mrMergeTrend=metricsBuckets().map(bucket=>({...bucket,mergedMrs:0})),mrByKey=new Map(mrMergeTrend.map(x=>[x.key,x]));
  mrMerges.forEach(mr=>{const bucket=mrByKey.get(metricsBucketKey(mr.mergedAt,win.grain));if(bucket)bucket.mergedMrs++});
  const trend=metricsBuckets().map(bucket=>({...bucket,items:[],runs:null})),byKey=new Map(trend.map(x=>[x.key,x]));
  completed.forEach(d=>{const bucket=byKey.get(metricsBucketKey(d.lastRunAt||d.firstRunAt,win.grain));if(bucket)bucket.items.push(d)});
  (data.trend||[]).forEach(point=>{const bucket=byKey.get(point.date);if(bucket)bucket.runs=point});
  trend.forEach(point=>{const list=point.items,phases=list.map(metricsDeliveryPhases),phaseItems=phases.map(phases=>({phases})),known=list.filter(x=>x.costKnown),runs=point.runs||{},passed=list.filter(d=>Number(d.runCount||0)===1&&!Number(d.failedRuns||0)&&!Number(d.reworks||0)),worked=list.filter(d=>Number(d.runCount||0)>1||Number(d.failedRuns||0)>0||Number(d.reworks||0)>0||String(issueMap.get(d.issueId)?.deliveryState||'')==='reopened'),auto=list.filter(d=>!(interventionByIssue.get(d.issueId)||0));point.deliveries=list.length;point.cycle=metricsPhaseAverage(phaseItems,'total').value;point.clarification=metricsPhaseAverage(phaseItems,'clarification').value;point.development=metricsPhaseAverage(phaseItems,'development').value;point.testing=metricsPhaseAverage(phaseItems,'testing').value;point.tokens=list.length?list.reduce((n,x)=>n+Number(x.tokens||0),0)/list.length:0;point.cost=known.length?known.reduce((n,x)=>n+Number(x.costCny||0),0)/known.length:0;point.firstPass=list.length?passed.length*100/list.length:0;point.rework=list.length?worked.length*100/list.length:0;point.automation=list.length?auto.length*100/list.length:0;const term=Number(runs.succeeded||0)+Number(runs.failed||0);point.runSuccess=term?Number(runs.succeeded||0)*100/term:0});
  const requirements=deliveries.map(d=>({...d,phases:metricsDeliveryPhases(d),issue:issueMap.get(d.issueId),requirementDone:deliveryDone(d)})).sort((a,b)=>new Date(b.lastRunAt||b.firstRunAt||0)-new Date(a.lastRunAt||a.firstRunAt||0)),phaseCompleted=requirements.filter(x=>x.requirementDone),phaseTotalValues=phaseCompleted.filter(x=>x.phases.samples.total).map(x=>x.phases.total);
  const phaseAverages={clarification:metricsPhaseAverage(phaseCompleted,'clarification'),development:metricsPhaseAverage(phaseCompleted,'development'),testing:metricsPhaseAverage(phaseCompleted,'testing'),total:metricsPhaseAverage(phaseCompleted,'total')};
  const phaseTrend=metricsBuckets({...win,grain:'day'}).map(bucket=>({...bucket,items:[]})),phaseByKey=new Map(phaseTrend.map(x=>[x.key,x]));
  phaseCompleted.forEach(item=>{const bucket=phaseByKey.get(metricsBucketKey(item.lastRunAt||item.firstRunAt,'day'));if(bucket)bucket.items.push(item)});
  phaseTrend.forEach(point=>{point.clarification=metricsPhaseAverage(point.items,'clarification').value;point.development=metricsPhaseAverage(point.items,'development').value;point.testing=metricsPhaseAverage(point.items,'testing').value;point.total=metricsPhaseAverage(point.items,'total').value;point.deliveries=point.items.length});
  return {data,mrMerges,mergedMRs,mergedMRsMissingTime,mrMergeTrend,issues,requirementIssues,linkedTAPD,aiIssueIds,covered,completed,firstPass,reworked,interventions,automated,phaseTotalValues,tokens,knownCosts,cost,terminal,frontend,backend,test,trend,requirements,phaseAverages,phaseTrend,
    delivered:completed.length,averagePhaseTotalMs:phaseAverages.total.value,firstPassRate:completed.length?firstPass.length*100/completed.length:0,reworkRate:completed.length?reworked.length*100/completed.length:0,averageTokens:completed.length?tokens/completed.length:0,averageCost:knownCosts.length?cost/knownCosts.length:0,averageInterventions:aiIssueIds.size?interventions/aiIssueIds.size:0,automationRate:completed.length?automated.length*100/completed.length:0,coverageRate:requirementIssues.length?covered.length*100/requirementIssues.length:0,runSuccessRate:terminal?Number(data.summary?.succeededRuns||0)*100/terminal:0};
}
function metricsDisplayModel(model){
  const deliveryCount=model.delivered;
  const story=`${metricsWindow().label}完成 ${deliveryCount} 个需求或 Issue，${model.completed.length?`${model.firstPass.length} 个一次通过`:'等待首个完整交付闭环'}。`;
  const storyFoot='指标来自当前项目的需求、Issue、Run 与 Trace。';
  const qualitySlices=[{label:'一次通过',value:model.firstPass.length},{label:'返工后完成',value:Math.max(0,model.completed.length-model.firstPass.length)},{label:'进行中',value:Math.max(0,model.aiIssueIds.size-model.completed.length)}];
  const categorySlices=(model.data.categories||[]).map(c=>({label:obsCategoryLabel(c.key),value:Number(c.completedIssues||0)})).filter(x=>x.value);
  return {...model,deliveryCount,phaseTotalMs:model.averagePhaseTotalMs,firstPassRate:model.completed.length?model.firstPassRate:null,reworkRate:model.completed.length?model.reworkRate:null,runSuccessRate:model.terminal?model.runSuccessRate:null,coverageRate:model.requirementIssues.length?model.coverageRate:null,averageTokens:model.completed.length?model.averageTokens:null,story,storyFoot,qualitySlices,categorySlices,
    qualityTitle:'交付质量',qualityCaption:'一次通过、返工完成与进行中',categoryTitle:'需求类型',categoryCaption:'按 SOP 统计标签归类',snapshotTotal:model.aiIssueIds.size};
}
function metricsMs(v){return Number(v)===0?'0ms':duration(v)}
const METRICS_SLICE_COLORS=['var(--c1)','var(--c2)','var(--c3)','var(--muted-2)','var(--c4)','var(--panel-3)'];
function metricsSliceColor(i){return METRICS_SLICE_COLORS[Math.min(i,METRICS_SLICE_COLORS.length-1)]}
function metricsDonut(slices,total,unit='需求'){
  const sum=Math.max(0,slices.reduce((n,x)=>n+Number(x.value||0),0));let cursor=0;
  const stops=sum?slices.map((x,i)=>{const start=cursor;cursor+=Number(x.value||0)*100/sum;return `${metricsSliceColor(i)} ${start.toFixed(1)}% ${cursor.toFixed(1)}%`}).join(','):'var(--panel-3) 0 100%';
  return `<div class="metrics-donut" style="--donut:${stops}"><div class="metrics-donut-center"><b>${total}</b><span>${unit}</span></div></div>`;
}
function metricsSliceLegend(slices){
  if(!slices.length)return '<div class="metrics-slices"><span class="metrics-no-data">待采集</span></div>';
  const sum=slices.reduce((n,x)=>n+Number(x.value||0),0);
  return `<div class="metrics-slices">${slices.map((x,i)=>`<div><i style="background:${metricsSliceColor(i)}"></i><span title="${escapeHTML(x.label)}">${escapeHTML(x.label)}</span><b>${Number(x.value||0)}</b><em>${sum?Math.round(Number(x.value||0)*100/sum):0}%</em></div>`).join('')}</div>`;
}
function metricsChartOpts(theme,reduced){return {theme,reduced,fmtTokens:formatTokens,fmtDuration:metricsMs,label:p=>p.label}}
function metricsDonutBlock(key,slices,total,unit){
  const chart=obsChartSlot(key,{empty:!slices.some(x=>Number(x.value)>0),emptyText:'待采集',option:(theme,reduced)=>obsSliceDonutOption(slices,{...metricsChartOpts(theme,reduced),center:{value:total,unit}}),fallback:()=>metricsDonut(slices,total,unit)},168);
  return `<div class="metrics-donut2"><div class="metrics-donut2-ring">${chart}</div>${metricsSliceLegend(slices)}</div>`;
}
function metricsTrendCard(key,title,sub,points,spec,fallback,o={}){
  const chart=obsChartSlot(key,{empty:!!o.empty,emptyText:o.emptyText||'暂无趋势数据',option:(theme,reduced)=>obsMetricTrendOption(points,spec,metricsChartOpts(theme,reduced)),fallback},o.height||220);
  return obsCard(key,title,sub,o.tools||'',chart+(o.after||''),{expand:!!o.expand,klass:o.klass||''});
}
// obsKpi 的数值与单位分开渲染，便于数字滚动；「待采集」单独弱化。
function metricsKpi(o){const text=String(o.value),m=text.match(/^(-?[\d.,]+)\s*([A-Za-z%]+)$/);return {...o,value:m?m[1]:text,unit:m?m[2]:'',missing:text==='待采集'}}
function metricsTrendSVG(points,metric){
  const meta={mergedMrs:{label:'MR 合入数量',color:'#06b6d4',get:p=>p.mergedMrs,format:v=>Number(v).toLocaleString([],{maximumFractionDigits:1})},deliveries:{label:'交付需求',color:'#5b8cff',get:p=>p.deliveries,format:v=>String(Math.round(v))},cycle:{label:'平均需求耗时',color:'#8b5cf6',get:p=>p.cycle,format:v=>duration(v)},tokens:{label:'单需求 Token',color:'#3b82f6',get:p=>p.tokens,format:v=>formatTokens(v)},cost:{label:'单需求 AI 费用',color:'#06b6d4',get:p=>p.cost,format:v=>costLabel(v,true)},runSuccess:{label:'Run 成功率',color:'#22c55e',get:p=>p.runSuccess,format:v=>`${Number(v||0).toFixed(0)}%`},firstPass:{label:'一次通过率',color:'#10b981',get:p=>p.firstPass,format:v=>`${Number(v||0).toFixed(0)}%`},rework:{label:'返工率',color:'#f59e0b',get:p=>p.rework,format:v=>`${Number(v||0).toFixed(0)}%`},automation:{label:'自动完成率',color:'#a855f7',get:p=>p.automation,format:v=>`${Number(v||0).toFixed(0)}%`}}[metric]||null;
  if(!meta||!points.length)return '<div class="metrics-trend-empty"><b>暂无趋势数据</b><span>有需求交付后按天汇总</span></div>';const values=points.map(meta.get),w=820,h=260,p={l:58,r:18,t:18,b:38},max=Math.max(1,...values),plotW=w-p.l-p.r,plotH=h-p.t-p.b,span=Math.max(1,points.length-1),x=i=>p.l+i*plotW/span,y=v=>p.t+plotH-(Number(v||0)/max)*plotH;
  const coords=values.map((v,i)=>[x(i),y(v)]),line=coords.map((pt,i)=>`${i?'L':'M'}${pt[0].toFixed(1)},${pt[1].toFixed(1)}`).join(' '),area=`${line} L${coords.at(-1)[0].toFixed(1)},${p.t+plotH} L${coords[0][0].toFixed(1)},${p.t+plotH} Z`,grids=[0,.5,1].map(n=>{const yy=p.t+plotH*(1-n);return `<line class="grid" x1="${p.l}" y1="${yy}" x2="${w-p.r}" y2="${yy}"/><text x="${p.l-8}" y="${yy+4}" text-anchor="end">${escapeHTML(meta.format(max*n))}</text>`}).join(''),step=Math.max(1,Math.ceil(points.length/7)),labels=points.map((point,i)=>(i===0||i===points.length-1||i%step===0)?`<text x="${x(i)}" y="${h-10}" text-anchor="middle">${escapeHTML(point.label)}</text>`:'').join(''),dots=coords.map((pt,i)=>`<circle class="dot" cx="${pt[0]}" cy="${pt[1]}" r="3"><title>${escapeHTML(points[i].label)} · ${escapeHTML(meta.format(values[i]))}</title></circle>`).join('');
  const gradient=`metricsArea-${metric}`;return `<svg viewBox="0 0 ${w} ${h}" role="img" aria-label="${meta.label}趋势"><defs><linearGradient id="${gradient}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" style="stop-color:var(--hui-accent)" stop-opacity=".18"/><stop offset="1" style="stop-color:var(--hui-accent)" stop-opacity=".02"/></linearGradient></defs>${grids}<path class="area" style="fill:url(#${gradient})" d="${area}"/><path class="line" d="${line}"/>${dots}${labels}</svg>`;
}
function metricsPhaseTrendSVG(points){
  if(!points.length||!points.some(point=>['clarification','development','testing','total'].some(key=>point[key]!=null)))return '<div class="metrics-trend-empty"><b>当前日期范围暂无样本</b><span>需求或 Issue 完成交付并采集到任一阶段后，这里会按天显示耗时</span></div>';
  const series=[['clarification','澄清','#f59e0b'],['development','研发','#8b5cf6'],['testing','测试','#0ea5e9'],['total','已采集耗时','#10b981']],w=960,h=292,p={l:62,r:18,t:18,b:42},max=Math.max(1,...points.flatMap(point=>series.map(([key])=>point[key]==null?0:Number(point[key])))),plotW=w-p.l-p.r,plotH=h-p.t-p.b,span=Math.max(1,points.length-1),x=i=>p.l+i*plotW/span,y=v=>p.t+plotH-(Number(v||0)/max)*plotH;
  const grids=[0,.25,.5,.75,1].map(n=>{const yy=p.t+plotH*(1-n);return `<line class="grid" x1="${p.l}" y1="${yy}" x2="${w-p.r}" y2="${yy}"/><text x="${p.l-8}" y="${yy+4}" text-anchor="end">${escapeHTML(duration(max*n))}</text>`}).join(''),step=Math.max(1,Math.ceil(points.length/8)),labels=points.map((point,i)=>(i===0||i===points.length-1||i%step===0)?`<text x="${x(i)}" y="${h-11}" text-anchor="middle">${escapeHTML(point.label)}</text>`:'').join('');
  const lines=series.map(([key,label,color])=>{let open=false;const path=points.map((point,i)=>{if(point[key]==null){open=false;return ''}const segment=`${open?'L':'M'}${x(i).toFixed(1)},${y(point[key]).toFixed(1)}`;open=true;return segment}).join(' '),dots=points.length<=100?points.map((point,i)=>point[key]!=null?`<circle cx="${x(i)}" cy="${y(point[key])}" r="2.7" fill="var(--panel)" stroke="${color}" stroke-width="2"><title>${escapeHTML(point.label)} · ${label} ${escapeHTML(point[key]===0?'0ms':duration(point[key]))}</title></circle>`:'').join(''):'';return `<path d="${path}" fill="none" stroke="${color}" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/>${dots}`}).join('');
  const legend=series.map(([,label,color])=>`<span><i style="background:${color}"></i>${label}</span>`).join('');
  return `<div class="metrics-phase-legend">${legend}</div><svg viewBox="0 0 ${w} ${h}" role="img" aria-label="需求与 Issue 阶段平均耗时日趋势">${grids}${lines}${labels}</svg>`;
}
function metricsRequirementTable(model){
  const phaseCell=(item,key,stage)=>{const p=item.phases;if(!p.samples[key])return `<span class="is-missing" title="${key==='clarification'?'缺少开始澄清或开始开发时间':'没有可计算的已完成阶段节点'}">待采集</span>`;const evidence=(item.phaseEvidence||[]).filter(e=>e.stage===stage),inferred=evidence.some(e=>e.source==='inferred'),boundary=key==='clarification'&&item.clarificationStartedAt&&item.developmentStartedAt,hint=boundary?`开始澄清 ${new Date(item.clarificationStartedAt).toLocaleString()} → 开始开发 ${new Date(item.developmentStartedAt).toLocaleString()}${inferred?'（含旧流程推断）':''}`:evidence.length?`${evidence.length} 个节点；${inferred?'包含旧流程名称推断':'SOP 显式标注'}`:`${p.samples[key]} 个节点`;return `<span title="${escapeHTML(hint)}">${p[key]===0?'0ms':duration(p[key])}${inferred?' *':''}</span>`};
  const rows=model.requirements.slice(0,80).map(item=>{const date=new Date(item.lastRunAt||item.firstRunAt||0),p=item.phases,status=item.requirementDone?'已完成':item.runStatus==='failed'?'执行失败':'进行中',tapdId=metricsRequirementTAPDID(item.issue)||item.externalId,project=(state.data.projects||[]).find(x=>x.id===item.issue?.projectId),localId=item.issue?.number?`${project?.key||'ISS'}-${item.issue.number}`:item.issueId,title=escapeHTML(item.title||item.issueId),gongfeng=metricsIssueSource(item.issue)==='gongfeng',issueURL=item.issue?.externalRef?.externalUrl||item.issueUrl,issueRef=item.issue?.externalRef?.externalIid||item.issue?.externalRef?.externalId||localId,identity=gongfeng?(issueURL?`<a href="${escapeHTML(safeURL(issueURL))}" target="_blank" rel="noopener noreferrer">Issue ${escapeHTML(issueRef)}</a>`:`<b>Issue ${escapeHTML(issueRef)}</b>`):tapdId?(item.issueUrl?`<a href="${escapeHTML(item.issueUrl)}" target="_blank" rel="noopener noreferrer">TAPD ${escapeHTML(tapdId)}</a>`:`<b>TAPD ${escapeHTML(tapdId)}</b>`):`<b>平台 ${escapeHTML(localId)}</b>`,run=item.runId?`<button type="button" class="button ghost small" data-metrics-run="${escapeHTML(item.runId)}">查看 Run</button>`:'';return `<tr><td><time>${Number.isFinite(date.getTime())?date.toLocaleDateString([],{month:'2-digit',day:'2-digit'}):'—'}</time></td><td><div class="metrics-requirement-name">${identity}<span title="${title}">${title}</span>${run}</div></td><td>${phaseCell(item,'clarification','clarification')}</td><td>${phaseCell(item,'development','development')}</td><td>${phaseCell(item,'testing','testing')}</td><td><strong>${p.samples.total?(p.total===0?'0ms':duration(p.total)):'待采集'}</strong></td><td><span class="metrics-requirement-status ${status==='已完成'?'done':status==='执行失败'?'failed':'active'}">${status}</span></td></tr>`}).join('');
  return `<div class="obs-tw metrics-tw"><table class="obs-table metrics-requirement-table"><thead><tr><th>完成日期</th><th>需求 / Issue</th><th>澄清耗时</th><th>研发耗时</th><th>测试耗时</th><th>已采集耗时</th><th>状态</th></tr></thead><tbody>${rows||'<tr><td colspan="7" class="metrics-table-empty">当前日期范围暂无需求或 Issue</td></tr>'}</tbody></table></div>`;
}
function metricsMRMergeTable(items){
  const rows=items.slice(0,80).map(mr=>`<tr><td><a href="${escapeHTML(safeURL(mr.mrUrl))}" target="_blank" rel="noopener noreferrer">${escapeHTML(mr.mrProject)} !${Number(mr.mrIid)}</a></td><td><code>${escapeHTML(mr.targetBranch||'—')}</code></td><td><div class="metrics-requirement-name"><span title="${escapeHTML(mr.title||mr.issueId)}">${escapeHTML(mr.title||mr.issueId)}</span></div></td><td><time>${escapeHTML(formatTimestamp(mr.mergedAt)||'—')}</time></td></tr>`).join('');
  return `<div class="obs-tw metrics-tw"><table class="obs-table metrics-requirement-table"><thead><tr><th>MR</th><th>目标分支</th><th>关联需求 / Issue</th><th>合入时间</th></tr></thead><tbody>${rows||'<tr><td colspan="4" class="metrics-table-empty">当前日期范围暂无已合入 MR</td></tr>'}</tbody></table></div>`;
}
function metricsSectionHead(kicker,title,description,id){return `<header class="metrics-sec-head"${id?` id="${id}"`:''}><span>${kicker}</span><h2>${title}</h2>${description?`<p>${description}</p>`:''}</header>`}
// 洞察只做确定性比较：条件不满足的条目不出，全部不满足时整条不渲染。
function metricsInsights(m,win){
  const items=[];
  if(m.deliveryCount>0)items.push({text:`${escapeHTML(win.label)}交付 <b>${m.deliveryCount}</b> 项${m.completed.length?`，<b>${m.firstPass.length}</b> 项一次通过`:''}`});
  const phases=[['clarification','澄清'],['development','研发'],['testing','测试']].map(([key,name])=>[name,m.phaseAverages[key]]).filter(([,a])=>a.samples);
  const sum=phases.reduce((n,[,a])=>n+Number(a.value||0),0);
  if(phases.length>=2&&sum){const [name,a]=phases.reduce((x,y)=>Number(y[1].value)>Number(x[1].value)?y:x);items.push({text:`${name}是耗时最长的阶段，占三阶段均值之和 <b>${Math.round(Number(a.value)*100/sum)}%</b>`,action:'data-metrics-jump="metricsPhase"',label:'查看'})}
  if(m.mergedMRs>0)items.push({text:`合入 MR <b>${m.mergedMRs}</b> 个${m.mergedMRsMissingTime?`，另有 ${m.mergedMRsMissingTime} 个缺少合入时间`:''}`});
  else if(m.completed.length&&m.tokens)items.push({text:`Token 平均每交付项 <b>${formatTokens(m.averageTokens)}</b>`});
  return obsInsightList(items);
}
function bindMetricsDateControls(){
  const win=metricsWindow(),from=$('#metricsDateFrom'),to=$('#metricsDateTo'),active=$('#metricsActiveWindow');
  document.querySelectorAll('[data-metrics-range]').forEach(b=>{b.classList.toggle('active',b.dataset.metricsRange===state.metricsRange);b.onclick=()=>{if(state.metricsRange===b.dataset.metricsRange)return;state.metricsRange=b.dataset.metricsRange;localStorage.setItem('agentflow.metricsRange',state.metricsRange);refreshMetrics()}});
  if(from)from.value=state.metricsRange==='custom'?state.metricsDateFrom:metricsDateValue(win.from);if(to)to.value=state.metricsRange==='custom'?state.metricsDateTo:metricsDateValue(win.to);
  if(active)active.textContent=`${win.label} · ${win.grain==='day'?'按日':win.grain==='week'?'按周':'按月'}`;
  const form=$('#metricsDateFilter');if(form)form.onsubmit=e=>{e.preventDefault();const start=from?.value||'',end=to?.value||'';if(!start||!end)return toast('请选择完整的开始和结束日期');state.metricsDateFrom=start;state.metricsDateTo=end;state.metricsRange='custom';localStorage.setItem('agentflow.metricsDateFrom',start);localStorage.setItem('agentflow.metricsDateTo',end);localStorage.setItem('agentflow.metricsRange','custom');refreshMetrics()};
}
function renderMetricsDashboard(){
  const body=$('#metricsBody'),live=$('#metricsLiveState');if(!body)return;bindMetricsDateControls();
  const stale=state.metricsDataKey!==metricsQueryKey();
  metricsReleaseCharts(body);
  if(state.metricsLoading&&(!state.metricsData||stale)){body.innerHTML='<div class="metrics-loading"><div><i></i><p>正在加载当前项目的研发效能数据…</p></div></div>';if(live)live.innerHTML='<i></i> 正在计算';return}
  if(state.metricsError){body.innerHTML=`<div class="metrics-empty"><div><b>看板加载失败</b><p>${escapeHTML(state.metricsError)}</p><button type="button" class="button primary" data-metrics-retry>重新加载</button></div></div>`;body.querySelector('[data-metrics-retry]').onclick=()=>refreshMetrics(true);if(live)live.innerHTML='<i></i> 加载失败';return}
  if(stale){body.innerHTML='<div class="metrics-loading"><div><i></i><p>正在切换到当前项目的数据…</p></div></div>';if(live)live.innerHTML='<i></i> 等待加载';return}
  if(!state.metricsData){body.innerHTML='<div class="metrics-empty">选择周期后查看研发效能指标</div>';return}
  const raw=metricsDashboardModel(),m=metricsDisplayModel(raw),win=metricsWindow(),missing='待采集',phaseTotalFoot=m.phaseTotalValues.length?`${m.phaseTotalValues.length} 条完成项有阶段耗时`:'等待阶段耗时采集',source=metricsDemoEnabled()?'演示数据':'项目实时';
  const grainLabel=win.grain==='day'?'按日':win.grain==='week'?'按周':'按月',known=list=>list.filter(v=>v!=null);
  const kpis=obsKpiGroups([
    obsKpiGroup('交付',[
      metricsKpi({label:'交付数（含 Issue）',value:String(m.deliveryCount),spark:obsSpark(m.trend.map(p=>p.deliveries)),foot:`${win.label}完成交付`}),
      metricsKpi({label:'MR 合入数量',value:String(m.mergedMRs),spark:obsSpark(m.mrMergeTrend.map(p=>p.mergedMrs)),foot:m.mergedMRsMissingTime?`${m.mergedMRsMissingTime} 个已合入 MR 缺少时间，未计入`:'按实际合入时间去重统计'}),
      metricsKpi({label:'平均已采集耗时',value:m.phaseTotalMs!=null?metricsMs(m.phaseTotalMs):missing,spark:obsSpark(known(m.phaseTrend.map(p=>p.total))),foot:phaseTotalFoot,tip:['已采集耗时 = 单条有记录的澄清 + 研发 + 测试之和；缺失阶段不计入，三项全缺为待采集。']}),
    ],escapeHTML(win.label)),
    obsKpiGroup('质量与成本',[
      metricsKpi({label:'Token 成本',value:m.completed.length?formatTokens(m.tokens):missing,spark:obsSpark(m.trend.map(p=>p.tokens)),foot:m.completed.length?`平均每交付项 ${formatTokens(m.averageTokens)}`:'等待 Token 归集'}),
      metricsKpi({label:'一次通过率',value:m.firstPassRate!=null?percent(m.firstPassRate):missing,meter:m.firstPassRate||0,foot:`${m.firstPass.length} / ${m.completed.length} 个完成项`}),
    ]),
  ]);
  const phaseKeys=[['clarification','澄清','平均澄清耗时'],['development','研发','平均研发耗时'],['testing','测试','平均测试耗时']];
  const phaseSum=phaseKeys.reduce((n,[key])=>n+(m.phaseAverages[key].samples?Number(m.phaseAverages[key].value||0):0),0);
  const phaseKpis=obsKpiGroup('阶段均值',phaseKeys.map(([key,name,label])=>{const a=m.phaseAverages[key],share=a.samples&&phaseSum?Number(a.value||0)*100/phaseSum:0;return metricsKpi({label,value:a.samples?metricsMs(a.value):missing,meter:share,foot:`${a.samples} 条完成项采集到${name}${a.samples&&phaseSum?` · 占 ${Math.round(share)}%`:''}`})}),'进度条 = 占三阶段均值之和');
  const phasePoints=m.phaseTrend.map(p=>({label:p.label,clarification:p.clarification,development:p.development,testing:p.testing,total:p.total,deliveries:p.deliveries,samples:Object.fromEntries(phaseKeys.map(([key])=>[key,metricsPhaseAverage(p.items,key).samples]))}));
  const phaseCard=obsCard('metrics-phase','阶段平均耗时趋势','按完成日聚合；柱高 = 当日三阶段均值之和，无样本日留空','',obsChartSlot('metrics-phase',{empty:!phasePoints.some(p=>phaseKeys.some(([key])=>p[key]!=null)),emptyText:'当前日期范围暂无样本',option:(theme,reduced)=>obsPhaseStackOption(phasePoints,metricsChartOpts(theme,reduced)),fallback:()=>metricsPhaseTrendSVG(m.phaseTrend)},260),{expand:true});
  const tableCard=obsCard('metrics-requirements','每条需求 / Issue 耗时明细','研发、测试为节点执行累计；* 表示旧流程推断，悬停单元格看计时依据',`<span class="metrics-card-total">${m.requirements.length} <small>条交付项</small></span>`,metricsRequirementTable(m));
  const deliveryCard=metricsTrendCard('metrics-delivery','交付趋势',`${grainLabel}统计完成的需求与 Issue`,m.trend,{name:'交付',kind:'bar',integer:true,get:p=>Number(p.deliveries||0),format:v=>String(Math.round(v))},()=>metricsTrendSVG(m.trend,'deliveries'),{empty:!m.trend.some(p=>Number(p.deliveries)>0),emptyText:'当前日期范围暂无交付',expand:true});
  const coverage=obsKpi(metricsKpi({label:'AI 工作项覆盖率',value:m.coverageRate!=null?percent(m.coverageRate):missing,meter:m.coverageRate||0,foot:m.requirementIssues.length?`${m.covered.length} / ${m.requirementIssues.length} 个需求与 Issue · ${m.linkedTAPD} 个关联 TAPD`:'等待工作项创建'}));
  const categoryCard=obsCard('metrics-category',m.categoryTitle,m.categoryCaption,'',metricsDonutBlock('metrics-category',m.categorySlices,m.completed.length,'交付项')+`<div class="metrics-split obs-ks">${coverage}</div>`);
  const mrSub=`${grainLabel}统计需求 / Issue 关联 MR；合入目标分支即计数，包含集成分支，同一 MR 只计一次${m.mergedMRsMissingTime?`。${m.mergedMRsMissingTime} 个已合入 MR 缺少合入时间，暂未计入日期统计`:''}`;
  const mrCard=metricsTrendCard('metrics-mr','MR 合入趋势',mrSub,m.mrMergeTrend,{name:'MR 合入',kind:'bar',integer:true,get:p=>Number(p.mergedMrs||0),format:v=>String(Math.round(v))},()=>metricsTrendSVG(m.mrMergeTrend,'mergedMrs'),{empty:!m.mrMergeTrend.some(p=>Number(p.mergedMrs)>0),emptyText:'当前日期范围暂无已合入 MR',expand:true,height:200,tools:`<span class="metrics-card-total">${m.mergedMRs} <small>个 MR</small></span>`,after:`<details class="metrics-mr-details"><summary>查看合入明细${m.mrMerges.length>80?'（最近 80 条）':''}</summary>${metricsMRMergeTable(m.mrMerges)}</details>`});
  const rework=obsKpi(metricsKpi({label:'返工率（代理）',value:m.reworkRate!=null?percent(m.reworkRate):missing,meter:m.reworkRate||0,foot:m.completed.length?`${m.reworked.length} / ${m.completed.length} 条完成需求；多 Run、失败、节点返工或重开`:'等待完成需求'}));
  const qualityCard=obsCard('metrics-quality',m.qualityTitle,m.qualityCaption,'',metricsDonutBlock('metrics-quality',m.qualitySlices,m.snapshotTotal,'需求')+`<div class="metrics-split obs-ks">${rework}</div>`);
  const deliveredRows=p=>[['完成项',`${p.deliveries||0} 条`]];
  const firstPassCard=metricsTrendCard('metrics-firstpass','一次通过率趋势',`${grainLabel}观察交付质量变化`,m.trend,{name:'一次通过率',kind:'line',percent:true,get:p=>p.deliveries?p.firstPass:null,format:v=>percent(v),emptyLabel:'无完成项',rows:deliveredRows},()=>metricsTrendSVG(m.trend,'firstPass'),{empty:!m.completed.length,emptyText:'等待完成需求'});
  const autoKpis=obsKpiGroup('人工与稳定性',[
    metricsKpi({label:'平均人工干预',value:m.aiIssueIds.size?m.averageInterventions.toFixed(2):missing,foot:m.aiIssueIds.size?`${m.interventions} 次人工决策`:'等待人工决策事件'}),
    metricsKpi({label:'自动完成率',value:m.completed.length?percent(m.automationRate):missing,meter:m.completed.length?m.automationRate:0,foot:m.completed.length?`${m.automated.length} 个零干预完成`:'等待完成需求'}),
    metricsKpi({label:'Run 成功率',value:m.runSuccessRate!=null?percent(m.runSuccessRate):missing,meter:m.runSuccessRate||0,foot:m.terminal?`${m.data.summary?.succeededRuns||0} / ${m.terminal} 个终态 Run`:'等待终态 Run'}),
  ]);
  const tokenCard=metricsTrendCard('metrics-tokens','Token 成本趋势',`${grainLabel}观察单需求 Token 消耗`,m.trend,{name:'单需求 Token',kind:'line',get:p=>p.deliveries?p.tokens:null,format:v=>formatTokens(v),emptyLabel:'无完成项',rows:deliveredRows},()=>metricsTrendSVG(m.trend,'tokens'),{empty:!m.completed.length,emptyText:'等待 Token 归集'});
  const automationCard=metricsTrendCard('metrics-automation','自动完成率趋势',`${grainLabel}观察零人工干预完成情况`,m.trend,{name:'自动完成率',kind:'line',percent:true,get:p=>p.deliveries?p.automation:null,format:v=>percent(v),emptyLabel:'无完成项',rows:deliveredRows},()=>metricsTrendSVG(m.trend,'automation'),{empty:!m.completed.length,emptyText:'等待完成需求'});
  body.innerHTML=`${metricsInsights(m,win)}${kpis}
  ${metricsSectionHead('流转效率','需求 / Issue 阶段耗时','按完成日看每天各阶段耗时与构成','metricsPhase')}${phaseKpis}${phaseCard}${tableCard}
  ${metricsSectionHead('产能','交付产能','需求与 Issue 的交付规模与构成')}<div class="obs-grid2 is-wide-left">${deliveryCard}${categoryCard}</div>${mrCard}
  ${metricsSectionHead('质量','交付质量','一次通过与返工分开看')}<div class="obs-grid2">${qualityCard}${firstPassCard}</div>
  ${metricsSectionHead('自动化','自动化与稳定性','人工介入、自动完成与执行稳定性')}${autoKpis}<div class="obs-grid2">${tokenCard}${automationCard}</div>`;
  body.querySelectorAll('[data-metrics-run]').forEach(button=>button.onclick=()=>openRunFromId(button.dataset.metricsRun));
  body.querySelectorAll('[data-metrics-jump]').forEach(link=>link.onclick=e=>{e.preventDefault();document.getElementById(link.dataset.metricsJump)?.scrollIntoView({behavior:obsReducedMotion()?'auto':'smooth',block:'start'})});
  body.querySelectorAll('[data-obs-expand]').forEach(b=>b.onclick=e=>{e.stopPropagation();const card=b.closest('[data-obs-card]');if(obsExpandState&&obsExpandState.card===card)obsCollapse();else obsExpand(card)});
  obsMountCharts(body);
  if(live)live.innerHTML=`<i></i> ${state.metricsLoading?'正在刷新':source}`;
}
function metricsReleaseCharts(body){
  if(typeof obsExpandState!=='undefined'&&obsExpandState?.card?.closest?.('#metricsView'))obsCollapse({silent:true});
  if(typeof obsDisposeCharts==='function')obsDisposeCharts(body);
}
let metricsRequestSeq=0,metricsPendingKey='';
function metricsProjectId(){return state.projectFilter||currentProject()?.id||''}
function metricsQueryKey(){return [metricsProjectId(),state.metricsRange,state.metricsDateFrom,state.metricsDateTo].join('|')}
function invalidateMetricsForProjectChange(){
  metricsRequestSeq++;metricsPendingKey='';
  state.metricsData=null;state.metricsDataKey='';state.metricsIssues=[];state.metricsLoading=false;state.metricsError='';
}
async function refreshMetrics(force=false){
  const seq=++metricsRequestSeq,key=metricsQueryKey();metricsPendingKey=key;const win=metricsWindow(),projectId=metricsProjectId(),q=new URLSearchParams({projectId,from:win.from.toISOString(),to:win.to.toISOString(),grain:win.grain});state.metricsLoading=true;state.metricsError='';renderMetricsDashboard();
  if(metricsDemoEnabled()){const demo=metricsDemoSnapshot(win);state.metricsData=demo.analytics;state.metricsDataKey=key;state.metricsIssues=demo.issues;state.metricsLoading=false;metricsPendingKey='';renderMetricsDashboard();return}
  try{const [analytics,issues]=await Promise.all([api('/api/analytics?'+q.toString(),force?{}:{silent:true}),api('/api/issues?projectId='+encodeURIComponent(projectId),{silent:true})]);if(seq!==metricsRequestSeq||key!==metricsQueryKey())return;state.metricsData=analytics;state.metricsDataKey=key;state.metricsIssues=issues}catch(e){if(seq===metricsRequestSeq&&key===metricsQueryKey())state.metricsError=e.message||'加载失败'}finally{if(seq===metricsRequestSeq){metricsPendingKey='';state.metricsLoading=false;renderMetricsDashboard()}}
}
function metricHealth(m, liveCount=0){if(liveCount)return {key:'active',label:'运行中',hint:`${liveCount} 个实例执行中`};if(!m?.totalRuns)return {key:'never',label:'从未运行',hint:'需要首次验证'};if(m.waitingRuns)return {key:'active',label:'待审批',hint:`${m.waitingRuns} 个实例等待门禁`};if(m.failedRuns&&m.lastRunStatus==='failed')return {key:'blocked',label:'有阻塞',hint:'最近运行失败'};if(m.succeededRuns)return {key:'closed',label:'已闭环',hint:`${percent(m.closureRate)} 闭环率`};return {key:'blocked',label:'未闭环',hint:'尚无成功运行'}}
