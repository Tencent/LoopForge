// 云效 Codeup 仓库授权：PAT + HTTPS 克隆账号校验 → 组织选择 → 仓库选择器。
// Mirrors github-connect.js, but Codeup has no OAuth device flow: the personal
// access token is verified and saved as the per-host Git HTTPS credential for
// codeup.aliyun.com (platform=codeup) before repositories can be listed.
const CODEUP_HOST='codeup.aliyun.com';

function codeupCredentialRecordForPicker(){
  const user=state.currentUser;
  if(!user?.id||user.isGuest)return null;
  if(typeof codeupCredentialRecord==='function')return codeupCredentialRecord(user);
  const cached=state.codeupCredential;
  return cached&&Number(cached.userId)===Number(user.id)?cached.record||null:null;
}
function codeupPickerConfigured(){
  return !!(userById(state.currentUser?.id)?.codeupCredentialConfigured||codeupCredentialRecordForPicker());
}
function codeupOrganizationKey(){return 'agentflow.codeup.organization.'+(state.currentUser?.id||'')}
function codeupStoredOrganization(){try{return localStorage.getItem(codeupOrganizationKey())||''}catch{return ''}}
function codeupRememberOrganization(id){try{id?localStorage.setItem(codeupOrganizationKey(),id):localStorage.removeItem(codeupOrganizationKey())}catch{}}

function codeupConnectHTML(){
  return `<div class="github-connect codeup-connect" data-codeup-connect>
    <p class="github-connect-intro">使用云效 Codeup 的个人访问令牌（PAT）与 HTTPS 克隆账号连接，即可选择组织内的仓库。令牌在「个人设置 → 个人访问令牌」创建，克隆账号在「个人设置 → HTTPS 密码」查看。</p>
    <label class="pa-field"><span>HTTPS 克隆账号</span><input class="pa-input" type="text" data-codeup-username placeholder="云效 HTTPS 克隆账号" autocomplete="off" spellcheck="false"></label>
    <label class="pa-field"><span>个人访问令牌（PAT）</span><input class="pa-input" type="password" data-codeup-token placeholder="粘贴云效个人访问令牌" autocomplete="new-password"></label>
    <button type="button" class="credential-btn primary" data-codeup-start>校验并连接</button>
    <div class="github-connect-status" role="status" aria-live="polite" data-codeup-status></div>
  </div>`;
}

function mountCodeupRepoPicker(host,onSelect){
  if(!host)return;
  const connected=codeupPickerConfigured();
  host.innerHTML=(connected?'':codeupConnectHTML())
    +`<div data-codeup-org-row${connected?'':' hidden'}><label class="pa-field"><span>组织</span><select class="pa-input" data-codeup-org aria-label="云效组织"><option value="">请选择组织</option></select></label><p class="github-connect-status" data-codeup-org-status role="status"></p></div>`
    +`<button type="button" class="button ghost small" data-codeup-load${connected?'':' hidden'}>选择云效仓库</button>`
    +`<p data-codeup-repo-hint${connected?' hidden':''}>连接后可选择组织内的仓库；也可以直接粘贴 https://codeup.aliyun.com/组织ID/仓库.git 地址。</p>`
    +'<div data-codeup-repo-list></div>';
  const orgRow=host.querySelector('[data-codeup-org-row]'),orgSelect=host.querySelector('[data-codeup-org]'),orgStatus=host.querySelector('[data-codeup-org-status]');
  const load=host.querySelector('[data-codeup-load]'),list=host.querySelector('[data-codeup-repo-list]');
  let repositories=[],page=1,busy=false,organizationId='';

  // The Codeup OpenAPI returns pathWithNamespace/webUrl/httpUrlToRepo; normalize
  // to the GitHub-picker shape (full_name/html_url/clone_url/default_branch).
  const normalize=repo=>{
    let full=String(repo.full_name||repo.pathWithNamespace||repo.path||repo.name||'');
    if(organizationId&&!full.startsWith(organizationId+'/'))full=organizationId+'/'+full;
    return {...repo,full_name:full,html_url:repo.html_url||repo.webUrl||`https://${CODEUP_HOST}/${full}`,clone_url:repo.clone_url||repo.httpUrlToRepo||`https://${CODEUP_HOST}/${full}.git`,default_branch:repo.default_branch||repo.defaultBranch||'main'};
  };
  const paint=hasMore=>{
    list.innerHTML='<label class="pa-field"><span>搜索已加载的仓库</span><input class="pa-input" type="search" data-codeup-search placeholder="仓库名 / 分组名"></label><label class="pa-field"><span>选择仓库</span><select class="pa-input" data-codeup-repo aria-label="云效仓库"><option value="">请选择仓库</option></select></label><p data-codeup-repo-empty hidden>未找到仓库，可手动填写地址；请确认令牌有组织内仓库的读取权限。</p>'+(hasMore?'<button type="button" class="button ghost small" data-codeup-more>加载更多仓库</button>':'');
    const select=list.querySelector('[data-codeup-repo]'),search=list.querySelector('[data-codeup-search]'),empty=list.querySelector('[data-codeup-repo-empty]');
    const filter=()=>{
      select.replaceChildren(new Option('请选择仓库',''));
      const visible=repositories.filter(repo=>String(repo.full_name||'').toLowerCase().includes(search.value.toLowerCase()));
      for(const repository of visible){const option=document.createElement('option');option.value=repository.full_name;option.textContent=repository.full_name+(repository.private?' · 私有':'');select.append(option)}
      empty.hidden=visible.length>0;
    };
    search.oninput=filter;
    select.onchange=()=>{
      const repository=repositories.find(repo=>repo.full_name===select.value);
      if(repository)onSelect({...repository,clone_url:repository.clone_url||`https://${CODEUP_HOST}/${repository.full_name}.git`});
    };
    const more=list.querySelector('[data-codeup-more]');if(more)more.onclick=()=>fetchPage(page+1);
    filter();
  };
  const fetchPage=async requested=>{
    if(busy||!organizationId)return;
    busy=true;load.disabled=true;
    const actor=Number(state.currentUser?.id);
    const more=list.querySelector('[data-codeup-more]');if(more)more.disabled=true;
    try{
      const result=await api(`/api/auth/codeup/repositories?organizationId=${encodeURIComponent(organizationId)}&page=`+requested);
      if(!host.isConnected||Number(state.currentUser?.id)!==actor)return;
      const rows=(Array.isArray(result?.repositories)?result.repositories:[]).map(normalize);
      page=requested;repositories=requested===1?rows:repositories.concat(rows);
      paint(!!result?.hasMore);
    }catch(error){list.textContent=error.message}
    finally{busy=false;load.disabled=false;if(more)more.disabled=false}
  };
  const chooseOrganization=id=>{
    organizationId=String(id||'').trim();
    codeupRememberOrganization(organizationId);
    repositories=[];page=1;list.innerHTML='';
    if(organizationId)fetchPage(1);
  };
  const orgManualHTML=message=>`<label class="pa-field"><span>组织 ID</span><input class="pa-input" type="text" data-codeup-org-manual placeholder="组织管理后台 → 基本信息中的组织 ID" autocomplete="off" spellcheck="false"></label><button type="button" class="button ghost small" data-codeup-org-apply>加载仓库</button><p class="github-connect-status" data-codeup-org-status role="status">${escapeHTML(message||'')}</p>`;
  const bindManualOrg=()=>{
    const input=orgRow.querySelector('[data-codeup-org-manual]'),apply=orgRow.querySelector('[data-codeup-org-apply]');
    if(!input||!apply)return;
    const status=orgRow.querySelector('[data-codeup-org-status]');
    apply.onclick=()=>{
      const id=input.value.trim();
      if(!id){if(status)status.textContent='请填写组织 ID';return}
      chooseOrganization(id);
    };
  };
  const fetchOrganizations=async()=>{
    const actor=Number(state.currentUser?.id);
    orgStatus.textContent='正在读取组织…';
    try{
      const result=await api('/api/auth/codeup/organizations',{silent:true});
      if(!host.isConnected||Number(state.currentUser?.id)!==actor)return;
      const organizations=Array.isArray(result)?result:(Array.isArray(result?.organizations)?result.organizations:[]);
      if(!organizations.length)throw new Error('没有可用的组织');
      orgSelect.replaceChildren(new Option('请选择组织',''));
      for(const organization of organizations){
        const id=String(organization.id??organization.organizationId??'');
        if(!id)continue;
        orgSelect.append(new Option(organization.name?`${organization.name}（${id}）`:id,id));
      }
      orgStatus.textContent='';
      const stored=codeupStoredOrganization();
      if(stored&&[...orgSelect.options].some(option=>option.value===stored)){orgSelect.value=stored;chooseOrganization(stored)}
      orgSelect.onchange=()=>chooseOrganization(orgSelect.value);
    }catch(error){
      if(!host.isConnected||Number(state.currentUser?.id)!==actor)return;
      // The organization listing may be unavailable; a manually pasted organization ID keeps the picker usable.
      orgRow.innerHTML=orgManualHTML(`组织列表暂不可用（${error.message}），可手动填写组织 ID。`);
      bindManualOrg();
    }
  };
  load.onclick=()=>{if(organizationId)fetchPage(1);else fetchOrganizations()};

  const connect=host.querySelector('[data-codeup-connect]');
  if(connect){
    const button=connect.querySelector('[data-codeup-start]'),status=connect.querySelector('[data-codeup-status]');
    const username=connect.querySelector('[data-codeup-username]'),token=connect.querySelector('[data-codeup-token]');
    button.onclick=async()=>{
      if(button.disabled)return;
      const account=username.value.trim(),pat=token.value.trim();
      if(!account||!pat){status.textContent='请填写 HTTPS 克隆账号和个人访问令牌';return}
      button.disabled=true;status.textContent='正在校验令牌…';
      try{
        const outcome=typeof verifyAndSaveCodeupToken==='function'
          ?await verifyAndSaveCodeupToken(pat,account)
          :{saved:false,result:{error:'凭据模块尚未加载'}};
        if(!outcome?.saved){status.textContent=(outcome?.result&&(outcome.result.error||gitTokenFailure(outcome.result)))||'云效令牌校验未通过';button.disabled=false;return}
        token.value='';
        connect.remove();
        host.querySelector('[data-codeup-repo-hint]').hidden=true;
        orgRow.hidden=false;load.hidden=false;
        status.textContent='';
        toast('云效 Codeup 授权已保存','ok');
        host.dispatchEvent(new CustomEvent('codeup-connected',{bubbles:true}));
        fetchOrganizations();
      }catch(error){status.textContent=error.message;button.disabled=false}
    };
  }else if(connected){
    fetchOrganizations();
  }
}