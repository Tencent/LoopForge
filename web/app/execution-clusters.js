const executionClustersPath='/api/execution-clusters';
let executionSettingsRequest=0;
const executionText=text=>window.lfTranslate?.(text)||text;

function executionClusterQuery(projectId){
  return projectId?'?projectId='+encodeURIComponent(projectId):'';
}

function executionTargetValue(target){
  if(target?.pool==='runner')return 'runner:'+target.runnerId;
  if(target?.pool==='cluster')return 'cluster:'+target.clusterId;
  return target?.pool||'';
}

function executionTargetFromValue(value){
  if(!value)return undefined;
  if(value.startsWith('runner:'))return {pool:'runner',runnerId:value.slice(7)};
  if(value.startsWith('cluster:'))return {pool:'cluster',clusterId:value.slice(8)};
  return {pool:value};
}

async function executionChoices(projectId,manage=false){
  const query=executionClusterQuery(projectId);
  const [connection,machines]=await Promise.all([
    api(executionClustersPath+query+(manage?(query?'&':'?')+'manage=1':''),{silent:true}),
    projectId?api('/api/runners'+query,{silent:true}):Promise.resolve([]),
  ]);
  return {...connection,projectId,machines:(machines||[]).filter(machine=>machine.canUseInProject)};
}

function executionTargetOptions(choices,policy=false){
  const {available=[],machines=[]}=choices;
  const option=(value,label,disabled=false)=>
    `<option value="${escapeHTML(value)}"${disabled?' disabled':''}>${escapeHTML(executionText(label))}</option>`;
  const group=(label,rows)=>`<optgroup label="${escapeHTML(executionText(label))}">${rows.join('')}</optgroup>`;
  const runners=[
    ...(choices.projectId?[option('machine-auto','自动分配执行机器')]:[]),
    option('cloud','平台执行机器'),
    ...machines.map(machine=>option('runner:'+machine.id,machine.name||machine.id,!!runnerDefaultBlocker(machine,choices.minRunnerVersion))),
  ];
  const clusters=[
    option('cluster-auto','自动分配 K8S 集群',!available.length),
    ...available.map(cluster=>option('cluster:'+cluster.id,cluster.name)),
  ];
  return (policy?option('personal','沿用个人默认'):option('','按项目调度'))+
    group('执行机器',runners)+group('K8S 集群',clusters);
}

async function loadRunExecutionTargets(projectId){
  const select=$('#runExecutionCluster'),confirm=$('#confirmRun');
  select.innerHTML='<option value="">'+escapeHTML(executionText('按项目调度'))+'</option>';
  select.disabled=true;
  confirm.disabled=true;
  const request=(select.executionTargetsRequest||0)+1;
  select.executionTargetsRequest=request;
  try{
    const choices=await executionChoices(projectId);
    if(select.executionTargetsRequest!==request||projectId!==(currentProject()?.id||''))return;
    select.innerHTML=executionTargetOptions(choices);
  }catch(error){
    if(select.executionTargetsRequest===request)toast(error.message,'err');
  }finally{
    if(select.executionTargetsRequest===request){
      select.disabled=false;
      confirm.disabled=!$('#runWorkflowChoice').value||confirm.getAttribute('aria-busy')==='true';
    }
  }
}

function projectExecutionDescription(target){
  switch(target){
    case 'cluster-auto':return '自动选择可连接的 K8S 集群，优先使用沙箱较少的集群。';
    case 'machine-auto':return '在兼容的执行机器之间分配任务。';
    case 'cloud':return '新任务使用平台执行机器。';
    case 'personal':return '根据项目成员的执行偏好分配新任务。';
    case '':return '新任务沿用成员原有的执行设置。';
    default:return '新任务使用所选执行位置。';
  }
}

function renderProjectExecution(choices,projectId){
  $$('[data-project-execution]').forEach(host=>{
    host.hidden=!projectId;
    if(!projectId)return;
    const value=executionTargetValue(choices.projectTarget);
    host.innerHTML=`
      <div class="execution-policy-main">
        <span class="execution-settings-icon" aria-hidden="true">${huiIcon('workflow')}</span>
        <div>
          <label for="${host.id}Choice">${escapeHTML(executionText('项目调度'))}</label>
          <p>${escapeHTML(executionText(projectExecutionDescription(value)))}</p>
        </div>
      </div>
      <select id="${host.id}Choice" class="select execution-policy-select">${executionTargetOptions(choices,true)}</select>`;
    const select=host.querySelector('select');
    if(!Array.from(select.options).some(option=>option.value===value)){
      const option=new Option(executionText(value?'原执行位置不可用':'沿用原有设置'),value,true,true);
      option.disabled=true;
      select.add(option);
    }
    select.value=value;
    select.disabled=!choices.canSetDefault;
    select.onchange=async()=>{
      select.disabled=true;
      try{
        await api('/api/projects/'+encodeURIComponent(projectId)+'/execution',{
          method:'PUT',body:JSON.stringify(executionTargetFromValue(select.value)),
        });
        notifyExecutionSettingsChanged();
        toast(executionText('已更新项目调度'),'ok');
      }catch(error){toast(error.message,'err')}
      finally{await refreshExecutionClusters()}
    };
    window.AgentFlowHarnessIcons?.hydrate?.(host);
  });
}

function notifyExecutionSettingsChanged(){
  $('#chatNextFrame')?.contentWindow?.postMessage({type:'agentflow-environments-changed'},location.origin);
}

async function refreshExecutionClusters(){
  const projectId=currentProject()?.id||'';
  const request=++executionSettingsRequest;
  const list=$('#executionClusterList');
  list.setAttribute('aria-busy','true');
  try{
    const data=await executionChoices(projectId,true);
    if(request!==executionSettingsRequest||projectId!==(currentProject()?.id||''))return;
    renderProjectExecution(data,projectId);
    const add=$('#addExecutionCluster');
    add.hidden=!data.canManage;
    add.onclick=()=>openExecutionClusterForm(null);
    const clusters=data.canManage?data.clusters||[]:data.available||[];
    list.innerHTML=clusters.length?clusters.map(cluster=>`
      <article class="execution-cluster-row">
        <div class="execution-cluster-name">
          <span class="execution-settings-icon" aria-hidden="true">${huiIcon('layers')}</span>
          <div>
            <b>${escapeHTML(cluster.name)}</b>
            ${data.canManage?`<small title="${escapeHTML(cluster.url)}">${escapeHTML(cluster.url)}</small>`:''}
          </div>
        </div>
        ${data.canManage?`<span class="execution-cluster-scope">${escapeHTML(cluster.projectIds?.length
          ?cluster.projectIds.map(id=>(state.data.projects||[]).find(p=>p.id===id)?.name||id).join('、')
          :executionText('所有项目'))}</span>`:''}
        <span class="execution-cluster-status${cluster.enabled===false?' is-disabled':''}">${escapeHTML(executionText(cluster.enabled===false?'已停用':'已启用'))}</span>
        ${data.canManage?`<button class="button ghost small" data-configure-cluster="${escapeHTML(cluster.id)}" type="button">${escapeHTML(executionText('编辑'))}</button>`:''}
      </article>`).join(''):`<div class="execution-cluster-empty">${huiIcon('layers')}<b>${escapeHTML(executionText('还没有可用的 K8S 集群'))}</b></div>`;
    list.querySelectorAll('[data-configure-cluster]').forEach(button=>{
      button.onclick=()=>openExecutionClusterForm(clusters.find(cluster=>cluster.id===button.dataset.configureCluster));
    });
    window.AgentFlowHarnessIcons?.hydrate?.(list);
  }catch(error){
    if(request!==executionSettingsRequest)return;
    list.textContent=error.message;
    $$('[data-project-execution]').forEach(host=>{host.hidden=true});
  }finally{
    if(request===executionSettingsRequest)list.removeAttribute('aria-busy');
  }
}

function openExecutionClusterForm(cluster){
  const modal=$('#executionClusterModal'),form=$('#executionClusterForm');
  const fields=form.querySelector('fieldset'),feedback=$('#executionClusterFeedback');
  const {name,url,apiKey,enabled}=form.elements;
  const projectOptions=$('#executionClusterShareOptions');
  const projectSummary=$('#executionClusterShareSummary');
  form.reset();
  fields.disabled=false;
  $('#executionClusterFormTitle').textContent=executionText(cluster?'编辑 K8S 集群':'添加 K8S 集群');
  name.value=cluster?.name||'';
  url.value=cluster?.url||'';
  url.readOnly=!!cluster;
  apiKey.placeholder=executionText(cluster?.hasApiKey?'留空保留密钥':'如服务启用认证，请填写 API Key');
  enabled.checked=cluster?.enabled!==false;
  const sharedProjects=cluster?.projectIds||[];
  const projects=(state.data.projects||[]).filter(project=>!project.archivedAt||sharedProjects.includes(project.id));
  for(const id of sharedProjects){
    if(!projects.some(project=>project.id===id))projects.push({id,name:id});
  }
  projectOptions.innerHTML=projects.map(project=>
    `<label><input type="checkbox" name="projectId" value="${escapeHTML(project.id)}"${sharedProjects.includes(project.id)?' checked':''}><span>${escapeHTML(project.name||project.id)}</span></label>`).join('');
  const selectedProjects=()=>[...projectOptions.querySelectorAll('input:checked')].map(input=>input.value);
  function updateSummary(){
    const selected=selectedProjects();
    projectSummary.textContent=selected.length?projects.filter(project=>selected.includes(project.id)).map(project=>project.name||project.id).join('、'):executionText('所有项目和个人工作区');
  }
  projectOptions.onchange=updateSummary;
  $('#executionClusterSharing').open=false;
  updateSummary();
  function showFeedback(message,error=false){
    feedback.textContent=message;
    feedback.dataset.error=String(error);
    feedback.hidden=!message;
  }
  function close(){
    modal.classList.remove('open');
  }
  showFeedback('');
  form.querySelectorAll('[data-close-execution-cluster]').forEach(button=>{button.onclick=close});
  modal.onkeydown=event=>{if(event.key==='Escape'){event.preventDefault();close()}};
  async function submit(test){
    if(fields.disabled)return;
    const body={name:name.value.trim(),url:url.value.trim(),enabled:enabled.checked,projectIds:selectedProjects()};
    if(cluster)body.id=cluster.id;
    if(apiKey.value.trim())body.apiKey=apiKey.value.trim();
    fields.disabled=true;
    showFeedback(executionText(test?'正在测试连接…':'正在保存…'));
    try{
      await api(executionClustersPath+(test?'/test':cluster?'/'+encodeURIComponent(cluster.id):''),{
        method:test||!cluster?'POST':'PUT',body:JSON.stringify(body),silent:true,
      });
      if(test)showFeedback(executionText('OpenSandbox API 连接成功'));
      else{
        close();
        notifyExecutionSettingsChanged();
        await refreshExecutionClusters();
        toast(executionText('已保存 K8S 集群'),'ok');
      }
    }catch(error){showFeedback(error.message,true)}
    finally{fields.disabled=false}
  }
  $('#testExecutionCluster').onclick=()=>{if(url.reportValidity())void submit(true)};
  form.onsubmit=event=>{event.preventDefault();void submit(false)};
  modal.classList.add('open');
  name.focus();
}

async function chooseIssueExecutionTarget(projectId){
  const data=await executionChoices(projectId).catch(()=>null);
  if(!data?.available?.length&&!data?.machines?.length)return undefined;
  const value=await afOpenDialog(()=>{
    $('#confirmTitle').textContent=executionText('选择 LOOP 执行位置');
    $('#confirmMessage').textContent='';
    const wrap=$('#confirmPromptWrap'),label=$('#confirmPromptLabel');
    const select=document.createElement('select');
    select.id='issueExecutionTarget';
    select.className='select';
    select.innerHTML=executionTargetOptions(data);
    wrap.hidden=false;
    $('#confirmPromptInput').hidden=true;
    $('#confirmPromptMultiline').hidden=true;
    label.htmlFor=select.id;
    label.textContent=executionText('执行位置');
    wrap.append(select);
    const ok=$('#confirmOkBtn');
    ok.textContent=executionText('开始运行');
    ok.className='button primary';
    $('#confirmCancelBtn').textContent=executionText('取消');
    setConfirmIcon('info','server');
    return {focus:select,value:ok=>{const value=select.value;select.remove();return ok?value:null}};
  });
  return value===null?null:executionTargetFromValue(value);
}
