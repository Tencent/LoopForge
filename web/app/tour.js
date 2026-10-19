// Onboarding helpers: the one-click demo run in the welcome project and a short
// coach-mark tour of the run detail page.
const DEMO_PROJECT_ID='welcome';
let runTourTimer=0;

function demoProject(){return (state.data?.projects||[]).find(p=>p.id===DEMO_PROJECT_ID)}
function useProject(id){
  const switcher=$('#projectSwitcher');
  if(!switcher||state.projectFilter===id)return;
  switcher.value=id;switcher.dispatchEvent(new Event('change',{bubbles:true}));
}
async function accessibleRuns(){
  const data=await api('/api/runs',{silent:true}).catch(()=>[]);
  return Array.isArray(data)?data:(data?.items||[]);
}

async function startDemoRun(){
  const project=demoProject();
  const workflow=(state.data?.workflows||[]).find(w=>w.id===(project?.workflowId||'welcome-workflow'));
  if(!project||!workflow){toast(lfT('没有找到体验项目，可以在需求列表里新建需求并选择 SOP。','The demo project is missing. Create a requirement in Requirements and pick an SOP.'));return switchView('issues')}
  useProject(project.id);
  toast(lfT('正在体验项目里启动示例 SOP…','Starting the sample SOP in the demo project…'));
  try{
    const issue=await api('/api/issues',{method:'POST',body:JSON.stringify({projectId:project.id,title:lfT('体验 LoopForge：生成报告并人工验收','Try LoopForge: generate a report and approve it'),description:lfT('一键体验创建。SOP 会先用脚本生成验收报告，然后停下来等你审批，通过后继续执行。','Created by the one-click demo. The SOP generates a report with a script, waits for your approval, then continues.'),priority:'medium',kind:'requirement',status:'todo',workflowId:workflow.id})});
    await api(`/api/issues/${encodeURIComponent(issue.id)}/dispatch`,{method:'POST',body:JSON.stringify({workflowId:workflow.id})});
    let runId='';
    for(let i=0;i<30&&!runId;i++){
      await new Promise(r=>setTimeout(r,1000));
      const runs=await api(`/api/issues/${encodeURIComponent(issue.id)}/runs`,{silent:true}).catch(()=>[]);
      const list=Array.isArray(runs)?runs:(runs?.items||[]);
      runId=list[0]?.id||list[0]?.runId||'';
    }
    if(!runId){toast(lfT('SOP 已派发，稍后在运行记录里查看。','The SOP was dispatched. Check Runs in a moment.'));return switchView('runs')}
    openRunFromId(runId);
    startRunTour(runId,true);
  }catch(err){toast(err.message||String(err))}
}

async function openExampleRun(){
  const runs=await accessibleRuns();
  const done=runs.filter(r=>r.status==='succeeded');
  const example=done.find(r=>r.projectId===DEMO_PROJECT_ID)||done[0];
  if(!example)return startDemoRun();
  openRunFromId(example.id);
  startRunTour(example.id,false);
}

function runTourSteps(withApproval){
  const steps=[
    {sel:'.run-graph-section',title:lfT('节点图','Step graph'),text:lfT('这条 SOP 的每一步都在这里：Agent、脚本或人工审批。点一个节点，可以看它的输出、日志和产物。','Every step of the SOP is here: an Agent, a script or a human approval. Click a step to see its output, logs and artifacts.')},
    {sel:'.node-artifacts',title:lfT('产物','Artifacts'),text:lfT('节点产出的文档、代码和报告都会保存成不可变版本，可以在这里查看和下载。','Documents, code and reports produced by a step are kept as immutable versions. View and download them here.')},
  ];
  if(withApproval)steps.push({sel:'[data-approve="approve"]',wait:90000,title:lfT('待我确认','Needs review'),text:lfT('流程停在人工审批时，会出现在侧栏「待我确认」里。确认通过后，SOP 继续往下执行。','When the SOP pauses for approval it shows up under “Needs review” in the sidebar. Approve it and the SOP continues.')});
  return steps;
}

function startRunTour(runId,withApproval){
  clearTimeout(runTourTimer);
  const steps=runTourSteps(withApproval);
  let index=0;
  const show=(started=Date.now())=>{
    closeRunTour();
    if(!location.hash.includes(runId))return;
    const step=steps[index];if(!step)return;
    const target=[...document.querySelectorAll(step.sel)].find(el=>el.getClientRects().length);
    if(!target){
      if(Date.now()-started<(step.wait||15000)){runTourTimer=setTimeout(()=>show(started),800);return}
      index++;return show();
    }
    target.scrollIntoView({block:'center',behavior:'smooth'});
    target.classList.add('lf-coach-target');
    const bubble=document.createElement('div');bubble.className='lf-coach';bubble.setAttribute('role','dialog');
    const last=index===steps.length-1;
    bubble.innerHTML=`<small>${index+1} / ${steps.length}</small><b>${escapeHTML(step.title)}</b><p>${escapeHTML(step.text)}</p><div><button type="button" class="button ghost small" data-coach-skip>${lfT('跳过','Skip')}</button><button type="button" class="button primary small" data-coach-next>${last?lfT('知道了','Got it'):lfT('下一步','Next')}</button></div>`;
    document.body.append(bubble);
    const place=()=>{const r=target.getBoundingClientRect(),w=bubble.offsetWidth,h=bubble.offsetHeight;let top=r.bottom+10;if(top+h>innerHeight-8)top=Math.max(8,r.top-h-10);bubble.style.top=top+'px';bubble.style.left=Math.max(8,Math.min(innerWidth-w-8,r.left))+'px'};
    setTimeout(place,350);place();
    bubble.querySelector('[data-coach-skip]').onclick=closeRunTour;
    bubble.querySelector('[data-coach-next]').onclick=()=>{index++;show()};
  };
  runTourTimer=setTimeout(show,1200);
}
function closeRunTour(){
  clearTimeout(runTourTimer);
  document.querySelectorAll('.lf-coach').forEach(el=>el.remove());
  document.querySelectorAll('.lf-coach-target').forEach(el=>el.classList.remove('lf-coach-target'));
}
window.addEventListener?.('hashchange',()=>{if(document.querySelector('.lf-coach')&&!/#\/runs\//.test(location.hash))closeRunTour()});
window.LoopForgeTour={startDemoRun,openExampleRun,closeRunTour};
