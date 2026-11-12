function percent(n){return `${Number(n||0).toFixed(0)}%`}
function formatTimestamp(value){
  if(!value)return '';
  const d=new Date(value);
  if(!Number.isFinite(d.getTime())||d.getFullYear()<1970)return '';
  return d.toLocaleString();
}
function duration(ms){ms=Number(ms||0);if(!ms)return '—';if(ms<1000)return `${ms}ms`;if(ms<60000)return `${(ms/1000).toFixed(1)}s`;if(ms<3600000)return `${(ms/60000).toFixed(1)}m`;return `${(ms/3600000).toFixed(1)}h`}
function metricsIssueSource(issue){return String(issue?.source||issue?.externalRef?.source||'').toLowerCase()}
function metricHealth(m, liveCount=0){if(liveCount)return {key:'active',label:'运行中',hint:`${liveCount} 个实例执行中`};if(!m?.totalRuns)return {key:'never',label:'从未运行',hint:'需要首次验证'};if(m.waitingRuns)return {key:'active',label:'待审批',hint:`${m.waitingRuns} 个实例等待门禁`};if(m.failedRuns&&m.lastRunStatus==='failed')return {key:'blocked',label:'有阻塞',hint:'最近运行失败'};if(m.succeededRuns)return {key:'closed',label:'已闭环',hint:`${percent(m.closureRate)} 闭环率`};return {key:'blocked',label:'未闭环',hint:'尚无成功运行'}}
