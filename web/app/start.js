function loadStudioModules(modules){
  // Activate styles in their original positions to preserve the cascade.
  const styles=Array.from(document.querySelectorAll('link[data-studio-view]:not([rel])'),link=>new Promise(resolve=>{
    link.onload=link.onerror=resolve;
    link.rel='stylesheet';
  }));
  const scripts=Array.from(modules.querySelectorAll('script[src]'),source=>new Promise((resolve,reject)=>{
    const script=document.createElement('script');
    script.async=false; // Classic scripts share globals and must execute in document order.
    script.fetchPriority='low';
    script.onload=resolve;
    script.onerror=()=>reject(new Error('无法加载 '+source.getAttribute('src')));
    script.src=source.getAttribute('src');
    document.head.append(script);
  }));
  return Promise.all([...styles,...scripts]);
}

function bindStudioControls(){
  const renderCatalog=renderSOPCatalog;
  renderSOPCatalog=function(){renderCatalog();const success=document.querySelector('.overview-metric.success span');if(success)success.textContent='成功完成的运行';const successSmall=document.querySelector('.overview-metric.success small');if(successSmall)successSmall.textContent='累计成功次数'};
  bind();
  renderProjectSwitcher();
  updateApprovalBadge();
}

// The real chat can be used before the console finishes downloading. Retain
// the user's latest rail choice until its normal handler is bound.
function retainStudioRailClick(){
  const rail=document.getElementById('platformRail');
  let pending;
  const retain=event=>{
    const control=event.target.closest('button,#userBadge');
    if(!control)return;
    if(event.type==='keydown'&&(control.id!=='userBadge'||!['Enter',' '].includes(event.key)))return;
    pending=control;
    event.preventDefault();
    event.stopImmediatePropagation();
  };
  rail.addEventListener('click',retain,true);
  rail.addEventListener('keydown',retain,true);
  const release=()=>{
    rail.removeEventListener('click',retain,true);
    rail.removeEventListener('keydown',retain,true);
  };
  studioBoot.then(()=>{
    release();
    if(pending?.isConnected)pending.click();
  },release);
}

const studioBoot=boot();
studioBoot.catch(e=>{
  finishBootLoader();
  if(deferShellActions&&document.body.classList.contains('chat-next-active')){
    const message=lfT('工作台菜单加载失败','Workbench menus failed to load');
    toastActions.set(message,{label:lfT('重新加载','Reload'),run:()=>location.reload()});
    toast(message,'err');
    return;
  }
  document.body.innerHTML=`<pre style="padding:30px">启动失败：${escapeHTML(e.message)}</pre>`;
});
