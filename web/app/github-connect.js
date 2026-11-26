function githubConnectHTML(){
  return '<div class="github-connect" data-github-connect><p class="github-connect-intro">Sau khi xác nhận ủy quyền trên GitHub, bạn có thể chọn repository. Không cần sao chép token.</p><button type="button" class="credential-btn primary" data-github-start disabled>Kết nối GitHub</button><div class="github-connect-status" role="status" aria-live="polite" data-github-status>Đang kiểm tra dịch vụ ủy quyền…</div></div>';
}

async function hydrateGitHubConnections(root=document){
  const hosts=[...(root.matches?.('[data-github-connect]')?[root]:[]),...root.querySelectorAll('[data-github-connect]')];
  if(!hosts.length)return;
  try{
    const capability=await api('/api/auth/github',{silent:true});
    for(const host of hosts){
      if(!host.isConnected||host.dataset.busy==='true')continue;
      host.dataset.enabled=String(!!capability.enabled);
      const button=host.querySelector('[data-github-start]'),status=host.querySelector('[data-github-status]');
      button.hidden=!capability.enabled;button.disabled=!capability.enabled;
      button.textContent='Kết nối GitHub';
      status.textContent=capability.enabled?'Ủy quyền chỉ dùng cho tài khoản hiện tại và có thể thu hồi bất cứ lúc nào.':'Phiên bản này đã tắt ủy quyền GitHub; bạn có thể dùng「Access token」để kết nối repository.';
    }
  }catch(error){
    for(const host of hosts){host.querySelector('[data-github-status]').textContent='Tạm thời không kiểm tra được dịch vụ ủy quyền, hãy thử lại sau.';const button=host.querySelector('[data-github-start]');button.hidden=false;button.disabled=false;button.textContent='Kiểm tra lại';host.dataset.enabled='false'}
  }
}

// The async Clipboard API only exists in secure contexts, so installs opened as
// http://SERVER_IP fall back to copying from a hidden field inside the dialog.
async function copyGitHubDeviceCode(value,trigger){
  if(navigator.clipboard&&window.isSecureContext){try{await navigator.clipboard.writeText(value);return true}catch{}}
  const field=document.createElement('textarea');
  field.value=value;field.readOnly=true;field.tabIndex=-1;field.setAttribute('aria-hidden','true');
  field.style.cssText='position:fixed;top:0;left:0;width:1px;height:1px;padding:0;border:0;opacity:0';
  (trigger?.parentNode||document.body).append(field);
  field.focus({preventScroll:true});field.select();
  let copied=false;
  try{copied=document.execCommand('copy')}catch{copied=false}
  field.remove();
  trigger?.focus?.({preventScroll:true});
  return copied;
}

function selectGitHubDeviceCode(code){
  const range=document.createRange();range.selectNodeContents(code);
  const selection=window.getSelection();selection.removeAllRanges();selection.addRange(range);
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
    if(Date.now()>=deadline){fail(new Error('Ủy quyền đã hết hạn, hãy kết nối lại'));return}
    try{
      const result=await api('/api/auth/github/poll',{method:'POST',body:JSON.stringify({flowId:flow.flowId})});
      if(!active)return;
      if(!host.isConnected||Number(state.currentUser?.id)!==actor){finish();return}
      if(result.status==='connected'){
        if(typeof storeCredentialUser==='function')storeCredentialUser(result.user);
        if(typeof notifyCredentialsChanged==='function')notifyCredentialsChanged();
        state.gitTokenVerify=null;
        status.textContent=`Đã kết nối GitHub · ${result.login}`;
        finish();
        host.dispatchEvent(new CustomEvent('github-connected',{bubbles:true}));
        if(typeof credentialWizard!=='undefined'&&credentialWizard?.kind==='gitToken')closeCredentialWizard(true);
        if(document.querySelector('#credentialPanel')&&typeof renderCredentialPanel==='function')renderCredentialPanel();
        toast(`Đã lưu ủy quyền GitHub · ${result.login}`,'ok');
        return;
      }
      timer=setTimeout(poll,Math.max(5,result.interval||5)*1000);
    }catch(error){fail(error)}
  };
  try{
    status.textContent='Đang lấy mã ủy quyền GitHub…';
    flow=await api('/api/auth/github/device',{method:'POST',body:'{}'});
    if(!host.isConnected||Number(state.currentUser?.id)!==actor){finish();return}
    deadline=Date.now()+flow.expiresIn*1000;
    status.replaceChildren();
    const instruction=document.createElement('p');instruction.textContent='Sao chép mã ủy quyền, rồi xác nhận trên GitHub:';
    const code=document.createElement('strong');code.className='github-device-code';code.textContent=flow.userCode;instruction.append(code);
    const copy=document.createElement('button');copy.type='button';copy.className='button ghost small';copy.textContent='Sao chép mã ủy quyền';
    const copyCode=async trigger=>{
      if(await copyGitHubDeviceCode(flow.userCode,trigger)){copy.textContent='Đã sao chép';return}
      selectGitHubDeviceCode(code);copy.textContent='Đã chọn, nhấn Ctrl/⌘+C để sao chép';
    };
    copy.onclick=()=>copyCode(copy);
    const link=document.createElement('a');link.href='https://github.com/login/device';link.target='_blank';link.rel='noopener noreferrer';link.className='button primary small';link.textContent='Sao chép và mở trang ủy quyền GitHub';link.onclick=()=>{void copyCode(link)};
    const cancel=document.createElement('button');cancel.type='button';cancel.className='button ghost small';cancel.textContent='Hủy';cancel.onclick=()=>{finish();status.replaceChildren()};
    status.append(instruction,copy,link,cancel);
    timer=setTimeout(poll,Math.max(5,flow.interval)*1000);
  }catch(error){fail(error)}
}

function mountGitHubRepoPicker(host,onSelect){
  if(!host)return;
  const connected=!!userById(state.currentUser?.id)?.gitHubCredentialConfigured;
  host.innerHTML=(connected?'':githubConnectHTML())+`<button type="button" class="button ghost small" data-github-load${connected?'':' hidden'}>Chọn repository GitHub của tôi</button><p data-github-repo-hint${connected?' hidden':''}>Sau khi kết nối, bạn có thể chọn repository của mình; repository công khai cũng có thể điền trực tiếp địa chỉ. Access token có thể đặt trong cài đặt cá nhân.</p><div data-github-repo-list></div>`;
  hydrateGitHubConnections(host);
  const load=host.querySelector('[data-github-load]'),list=host.querySelector('[data-github-repo-list]');
  let repositories=[],page=1,busy=false;
  const paint=hasMore=>{
    list.innerHTML='<label class="pa-field"><span>Tìm trong các repository đã tải</span><input class="pa-input" type="search" data-github-search placeholder="Tên repository / tổ chức"></label><label class="pa-field"><span>Chọn repository</span><select class="pa-input" data-github-repo aria-label="Repository GitHub của tôi"><option value="">Hãy chọn repository</option></select></label><p data-github-repo-empty hidden>Không tìm thấy repository, bạn có thể điền địa chỉ thủ công; repository của tổ chức có thể cần quản trị viên phê duyệt ủy quyền.</p>'+(hasMore?'<button type="button" class="button ghost small" data-github-more>Tải thêm repository</button>':'');
    const select=list.querySelector('[data-github-repo]'),search=list.querySelector('[data-github-search]'),empty=list.querySelector('[data-github-repo-empty]');
    const filter=()=>{
      select.replaceChildren(new Option('Hãy chọn repository',''));
      const visible=repositories.filter(repo=>repo.full_name.toLowerCase().includes(search.value.toLowerCase()));
      for(const repository of visible){const option=document.createElement('option');option.value=repository.full_name;option.textContent=repository.full_name+(repository.private?' · Riêng tư':'');select.append(option)}
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
