// Keep restoration and SOP execution distinct. Only the final control response
// means the run has restarted; a 202 means the background restore is underway.
const runActivationRequests=new Map();
const runRestoreWatchers=new Map();

function paintRunActivationProgress(id){
  const host=$('#runDetail');
  if(!host||host.dataset.runId!==id)return;
  const activation=runActivationRequests.get(id);
  const pending=activation||(runRestoreWatchers.get(id)?.status==='restoring'?{restoring:true}:null);
  let notice=host.querySelector('[data-run-activation-status]');
  if(pending){
    if(!notice){
      notice=document.createElement('div');
      notice.dataset.runActivationStatus='';
      notice.className='run-section-label';
      notice.setAttribute('role','status');
      notice.setAttribute('aria-live','polite');
      host.querySelector('.run-detail-header')?.after(notice);
    }
    notice.textContent=pending.restoring?(activation?'正在恢复工作区，完成后继续执行，请稍候…':'正在后台恢复工作区，完成后可继续操作，请稍候…'):'正在提交执行请求，请稍候…';
  }else notice?.remove();
  host.querySelectorAll('[data-rerun-from-here],[data-resume-run],[data-retry-node],[data-resume-paused]').forEach(button=>{
    if(pending&&!button.disabled){button.dataset.activationDisabled='true';button.disabled=true}
    else if(!pending&&button.dataset.activationDisabled){delete button.dataset.activationDisabled;button.disabled=false}
  });
}

// A page reload observes the existing restore without replaying the user's
// previous control decision or creating another restoration task.
function observeRunWorkspaceRestore(id){
  if(runRestoreWatchers.has(id))return;
  const watcher={status:'idle'};
  runRestoreWatchers.set(id,watcher);
  (async()=>{
    for(;;){
      if($('#runDetail')?.dataset.runId!==id)return;
      const status=await api(`/api/runs/${encodeURIComponent(id)}/workspace/restore`,{silent:true});
      watcher.status=status.status;
      paintRunActivationProgress(id);
      if(status.status==='failed')toast(status.error||'工作区恢复失败，请重新发起操作','err');
      if(status.status!=='restoring')return;
      await new Promise(resolve=>setTimeout(resolve,1000));
    }
  })().catch(()=>{}).finally(()=>{
    runRestoreWatchers.delete(id);
    paintRunActivationProgress(id);
  });
}

function requestRunActivation(id,action,body={}){
  const intent=JSON.stringify({action,body});
  const existing=runActivationRequests.get(id);
  if(existing)return existing.intent===intent?existing.promise:Promise.reject(new Error('已有执行请求正在处理，请等待完成后再操作'));
  // Freeze the viewed attempt before restoration starts. A delayed replay
  // cannot reset a newer attempt completed by another operator in the meantime.
  const node=body.nodeId?cachedRun(id)?.nodes?.[body.nodeId]:null;
  if(action==='retry'&&node&&!body.expected)body={...body,expected:{status:node.status,attempt:node.attempt||0,reworkCount:node.reworkCount||0}};
  const pending={restoring:false,promise:null,intent};
  runActivationRequests.set(id,pending);
  paintRunActivationProgress(id);
  pending.promise=(async()=>{
    for(;;){
      const result=await api(`/api/runs/${encodeURIComponent(id)}/${action}`,{method:'POST',headers:{'Content-Type':'application/json','Prefer':'respond-async'},body:JSON.stringify(body)});
      if(result.workspaceRestore?.status!=='restoring')return result;
      pending.restoring=true;
      paintRunActivationProgress(id);
      for(;;){
        await new Promise(resolve=>setTimeout(resolve,1000));
        const status=await api(`/api/runs/${encodeURIComponent(id)}/workspace/restore`,{silent:true});
        if(status.status==='failed')throw new Error(status.error||'工作区恢复失败，请重试');
        if(status.status==='ready')break;
        if(status.status!=='restoring')throw new Error('工作区恢复状态已变化，请重新发起操作');
      }
      pending.restoring=false;
      paintRunActivationProgress(id);
    }
  })().finally(()=>{
    runActivationRequests.delete(id);
    paintRunActivationProgress(id);
  });
  return pending.promise;
}
