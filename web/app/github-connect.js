function githubConnectHTML(){
  return '<div class="github-connect" data-github-connect><p class="github-connect-intro">在 GitHub 确认授权后，即可选择你的仓库。无需复制令牌。</p><button type="button" class="credential-btn primary" data-github-start disabled>连接 GitHub</button><div class="github-connect-status" role="status" aria-live="polite" data-github-status>正在检查授权服务…</div><div data-github-setup></div></div>';
}

async function hydrateGitHubConnections(root=document){
  const hosts=[...(root.matches?.('[data-github-connect]')?[root]:[]),...root.querySelectorAll('[data-github-connect]')];
  if(!hosts.length)return;
  try{
    const capability=await api('/api/auth/github',{silent:true});
    for(const host of hosts){
      if(!host.isConnected||host.dataset.busy==='true')continue;
      host.dataset.enabled=String(!!capability.enabled);
      const button=host.querySelector('[data-github-start]'),status=host.querySelector('[data-github-status]'),setup=host.querySelector('[data-github-setup]');
      button.hidden=!capability.enabled;button.disabled=!capability.enabled;
      button.textContent='连接 GitHub';
      status.textContent=capability.enabled?'授权仅用于当前账号，可随时撤销。':capability.canConfigure?'此实例尚未启用 GitHub 授权。完成一次设置后，所有成员都可使用。':'此实例尚未启用 GitHub 授权。请联系平台管理员，或在个人配置中使用「访问令牌」。';
      setup.replaceChildren();
      if(!capability.canConfigure)continue;
      setup.innerHTML='<details class="github-admin"><summary>管理员设置'+(capability.enabled?'':' · 启用 GitHub 授权')+'</summary><p>只需设置一次；成员不需要创建应用或申请 PAT。</p><ol><li><a href="https://github.com/settings/applications/new" target="_blank" rel="noopener noreferrer">在 GitHub 创建 OAuth App ↗</a>，Homepage URL 和回调地址填写当前站点地址。</li><li>勾选 <b>Enable Device Flow</b>，保持令牌过期选项关闭，复制 Client ID（不是 Client Secret）。</li></ol><label class="credential-field"><span>站点地址</span><input class="pa-input" data-github-site readonly></label><label class="credential-field"><span>Client ID</span><input class="pa-input" data-github-client placeholder="例如 Ov23li…" autocomplete="off" spellcheck="false"></label><button type="button" class="credential-btn secondary" data-github-save>保存设置</button><p data-github-config-status role="status"></p></details>';
      setup.querySelector('[data-github-site]').value=location.origin+'/';
      setup.querySelector('[data-github-client]').value=capability.clientId||'';
      const save=setup.querySelector('[data-github-save]');
      if(capability.managed){save.disabled=true;setup.querySelector('[data-github-client]').readOnly=true;setup.querySelector('[data-github-config-status]').textContent='此实例由部署环境变量管理。'}
      save.onclick=async()=>{
        save.disabled=true;
        const message=setup.querySelector('[data-github-config-status]');
        try{
          const clientId=setup.querySelector('[data-github-client]').value.trim();
          if(!clientId&&!capability.enabled){message.textContent='请先填写 Client ID';return}
          await api('/api/auth/github/config',{method:'PUT',body:JSON.stringify({clientId})});
          await hydrateGitHubConnections(document);
          toast(clientId?'GitHub 应用设置已保存。请连接账号完成授权验证。':'GitHub 授权已停用。','ok');
        }catch(error){message.textContent=error.message}
        finally{save.disabled=false}
      };
    }
  }catch(error){
    for(const host of hosts){host.querySelector('[data-github-status]').textContent='暂时无法检查授权服务，请稍后重试。';const button=host.querySelector('[data-github-start]');button.hidden=false;button.disabled=false;button.textContent='重新检查';host.dataset.enabled='false'}
  }
}

async function startGitHubConnect(host){
  if(host.dataset.busy==='true')return;
  if(host.dataset.enabled!=='true'){await hydrateGitHubConnections(host);return}
  const button=host.querySelector('[data-github-start]'),status=host.querySelector('[data-github-status]');
  const actor=Number(state.currentUser?.id);
  host.dataset.busy='true';button.disabled=true;
  let timer,flow,deadline,active=true;
  const finish=()=>{active=false;clearTimeout(timer);host.dataset.busy='false';button.disabled=false};
  const fail=error=>{status.textContent=error.message;finish()};
  const poll=async()=>{
    if(!active)return;
    if(!host.isConnected||Number(state.currentUser?.id)!==actor){finish();return}
    if(Date.now()>=deadline){fail(new Error('授权已过期，请重新连接'));return}
    try{
      const result=await api('/api/auth/github/poll',{method:'POST',body:JSON.stringify({flowId:flow.flowId})});
      if(!active)return;
      if(!host.isConnected||Number(state.currentUser?.id)!==actor){finish();return}
      if(result.status==='connected'){
        if(typeof storeCredentialUser==='function')storeCredentialUser(result.user);
        if(typeof notifyCredentialsChanged==='function')notifyCredentialsChanged();
        state.gitTokenVerify=null;
        status.textContent=`已连接 GitHub · ${result.login}`;
        finish();
        host.dispatchEvent(new CustomEvent('github-connected',{bubbles:true}));
        if(typeof credentialWizard!=='undefined'&&credentialWizard?.kind==='gitToken')closeCredentialWizard(true);
        if(document.querySelector('#credentialPanel')&&typeof renderCredentialPanel==='function')renderCredentialPanel();
        toast(`GitHub 授权已保存 · ${result.login}`,'ok');
        return;
      }
      timer=setTimeout(poll,Math.max(5,result.interval||5)*1000);
    }catch(error){fail(error)}
  };
  try{
    status.textContent='正在获取 GitHub 授权码…';
    flow=await api('/api/auth/github/device',{method:'POST',body:'{}'});
    if(!host.isConnected||Number(state.currentUser?.id)!==actor){finish();return}
    deadline=Date.now()+flow.expiresIn*1000;
    status.replaceChildren();
    const instruction=document.createElement('p');instruction.textContent='复制授权码，然后在 GitHub 确认：';
    const code=document.createElement('strong');code.className='github-device-code';code.textContent=flow.userCode;instruction.append(code);
    const copy=document.createElement('button');copy.type='button';copy.className='button ghost small';copy.textContent='复制授权码';
    copy.onclick=async()=>{try{await navigator.clipboard.writeText(flow.userCode);copy.textContent='已复制'}catch{copy.textContent='请选中授权码复制'}};
    const link=document.createElement('a');link.href='https://github.com/login/device';link.target='_blank';link.rel='noopener noreferrer';link.className='button primary small';link.textContent='前往 GitHub 授权';
    const cancel=document.createElement('button');cancel.type='button';cancel.className='button ghost small';cancel.textContent='取消';cancel.onclick=()=>{finish();status.replaceChildren()};
    status.append(instruction,copy,link,cancel);
    timer=setTimeout(poll,Math.max(5,flow.interval)*1000);
  }catch(error){fail(error)}
}

function mountGitHubRepoPicker(host,onSelect){
  if(!host)return;
  const connected=!!userById(state.currentUser?.id)?.gitTokenConfigured;
  host.innerHTML=(connected?'':githubConnectHTML())+`<button type="button" class="button ghost small" data-github-load${connected?'':' hidden'}>选择我的 GitHub 仓库</button><p data-github-repo-hint${connected?' hidden':''}>连接后可选择自己的仓库；公开仓库也可直接填写地址。访问令牌可在个人配置中设置。</p><div data-github-repo-list></div>`;
  hydrateGitHubConnections(host);
  const load=host.querySelector('[data-github-load]'),list=host.querySelector('[data-github-repo-list]');
  let repositories=[],page=1,busy=false;
  const paint=hasMore=>{
    list.innerHTML='<label class="pa-field"><span>搜索已加载的仓库</span><input class="pa-input" type="search" data-github-search placeholder="仓库名 / 组织名"></label><label class="pa-field"><span>选择仓库</span><select class="pa-input" data-github-repo aria-label="我的 GitHub 仓库"><option value="">请选择仓库</option></select></label><p data-github-repo-empty hidden>未找到仓库，可手动填写地址；组织仓库可能需要管理员批准授权。</p>'+(hasMore?'<button type="button" class="button ghost small" data-github-more>加载更多仓库</button>':'');
    const select=list.querySelector('[data-github-repo]'),search=list.querySelector('[data-github-search]'),empty=list.querySelector('[data-github-repo-empty]');
    const filter=()=>{
      select.replaceChildren(new Option('请选择仓库',''));
      const visible=repositories.filter(repo=>repo.full_name.toLowerCase().includes(search.value.toLowerCase()));
      for(const repository of visible){const option=document.createElement('option');option.value=repository.full_name;option.textContent=repository.full_name+(repository.private?' · 私有':'');select.append(option)}
      empty.hidden=visible.length>0;
    };
    search.oninput=filter;
    select.onchange=()=>{const repository=repositories.find(repo=>repo.full_name===select.value);if(repository)onSelect(repository)};
    const more=list.querySelector('[data-github-more]');if(more)more.onclick=()=>fetchPage(page+1);
    filter();
  };
  const fetchPage=async requested=>{
    if(busy)return;
    busy=true;load.disabled=true;
    const actor=Number(state.currentUser?.id);
    const more=list.querySelector('[data-github-more]');if(more)more.disabled=true;
    try{
      const result=await api('/api/auth/github/repositories?page='+requested);
      if(!host.isConnected||Number(state.currentUser?.id)!==actor)return;
      page=requested;repositories=requested===1?result.repositories:repositories.concat(result.repositories);
      paint(result.hasMore);
    }catch(error){list.textContent=error.message}
    finally{busy=false;load.disabled=false;if(more)more.disabled=false}
  };
  load.onclick=()=>fetchPage(1);
  host.addEventListener('github-connected',()=>{
    host.querySelector('[data-github-connect]')?.remove();
    host.querySelector('[data-github-repo-hint]').hidden=true;
    load.hidden=false;
    fetchPage(1);
  });
}

document.addEventListener('click',event=>{
  const start=event.target.closest('[data-github-start]');
  if(start)startGitHubConnect(start.closest('[data-github-connect]'));
});
