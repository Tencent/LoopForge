// Codex ChatGPT sign-in. The platform keeps the login and refreshes it; the
// browser only ever sees the account email and status.
function codexChatGPTStatusText(user,email=''){
  if(!user?.codexChatGptLoginConfigured)return '尚未登录 ChatGPT';
  const account=email?` · ${email}`:'';
  return user.codexChatGptLoginStatus==='needs_relogin'?`登录已失效，请重新登录${account}`:`已登录 ChatGPT${account}`;
}

function openCodexChatGPTLogin(){
  const user=state.currentUser;
  if(!user?.id||user.isGuest)return;
  if($('#codexLoginDialog')?.open)return;
  const opener=document.activeElement;
  const actor=Number(user.id);
  let busy=false,timer=0,flow=null,deadline=0,email='';

  const dialog=document.createElement('dialog');
  dialog.id='codexLoginDialog';
  dialog.className='git-ssh-dialog codex-login-dialog';
  dialog.setAttribute('aria-labelledby','codexLoginTitle');
  dialog.innerHTML=`
    <header class="git-ssh-dialog-head">
      <h2 id="codexLoginTitle">Codex · ChatGPT 登录</h2>
      <button type="button" class="credential-btn ghost icon" data-codex-close aria-label="关闭">${credentialGlyph('close',16)}</button>
    </header>
    <p class="git-ssh-description">使用 ChatGPT 订阅运行 Codex。登录保存在平台并自动续期，执行时只下发不含 refresh token 的副本；已登录时优先于 Codex API Key。执行机器的 Codex 认证方式需选择「API Key」（个人凭证）。</p>
    <p class="git-ssh-status" data-codex-status role="status" aria-live="polite"></p>
    <div class="git-ssh-form-actions" data-codex-actions>
      <button type="button" class="credential-btn primary" data-codex-device>用 ChatGPT 登录</button>
      <button type="button" class="credential-btn secondary" data-codex-paste>粘贴 auth.json</button>
      <button type="button" class="credential-btn ghost danger" data-codex-signout hidden>退出登录</button>
    </div>
    <div class="github-connect-status" data-codex-device-panel hidden></div>
    <form class="git-ssh-key-form" data-codex-paste-form hidden>
      ${credentialFieldHTML({label:'auth.json',name:'authJson',multiline:true,placeholder:'粘贴本机执行 codex login 后生成的 ~/.codex/auth.json 全文',hint:'需要包含 refresh_token；保存后平台负责续期，请勿再在其他地方使用这份登录。'})}
      <footer class="git-ssh-form-actions">
        <button type="button" class="credential-btn secondary" data-codex-paste-cancel>取消</button>
        <button type="submit" class="credential-btn primary">保存</button>
      </footer>
    </form>`;

  const status=dialog.querySelector('[data-codex-status]');
  const actions=dialog.querySelector('[data-codex-actions]');
  const signOut=dialog.querySelector('[data-codex-signout]');
  const panel=dialog.querySelector('[data-codex-device-panel]');
  const form=dialog.querySelector('[data-codex-paste-form]');
  const authJSON=form.elements.namedItem('authJson');

  const current=()=>userById(actor)||user;
  const live=()=>dialog.open&&Number(state.currentUser?.id)===actor;
  function setStatus(message,error=false){status.textContent=message;status.classList.toggle('is-error',error)}
  function paint(){
    const fresh=current();
    setStatus(codexChatGPTStatusText(fresh,email),fresh?.codexChatGptLoginStatus==='needs_relogin');
    signOut.hidden=!fresh?.codexChatGptLoginConfigured;
    dialog.querySelector('[data-codex-device]').textContent=fresh?.codexChatGptLoginConfigured?'重新登录':'用 ChatGPT 登录';
  }
  function saved(result){
    email=result.email||'';
    storeCredentialUser(result.user);
    notifyCredentialsChanged();
    renderCredentialPanel();
    paint();
  }
  function stopDevice(){
    clearTimeout(timer);timer=0;flow=null;
    panel.hidden=true;panel.replaceChildren();
    actions.hidden=false;
  }
  async function run(work){
    if(busy)return;
    busy=true;dialog.setAttribute('aria-busy','true');
    try{await work()}catch(error){setStatus(error.message,true)}
    finally{busy=false;dialog.removeAttribute('aria-busy')}
  }

  async function poll(){
    timer=0;
    if(!flow||!live())return;
    if(Date.now()>=deadline){stopDevice();setStatus('登录已过期，请重新发起',true);return}
    try{
      const result=await api('/api/auth/codex/poll',{method:'POST',silent:true,body:JSON.stringify({flowId:flow.flowId})});
      if(!flow||!live())return;
      if(result.status==='connected'){
        stopDevice();saved(result);
        toast(`ChatGPT 登录已保存${result.email?' · '+result.email:''}`,'ok');
        return;
      }
      timer=setTimeout(poll,Math.max(3,result.interval||5)*1000);
    }catch(error){stopDevice();setStatus(error.message,true)}
  }
  function showDevice(started){
    flow=started;deadline=Date.now()+started.expiresIn*1000;
    actions.hidden=true;form.hidden=true;panel.hidden=false;
    const instruction=document.createElement('p');instruction.textContent='复制登录码，然后在 OpenAI 页面确认：';
    const code=document.createElement('strong');code.className='github-device-code';code.textContent=started.userCode;instruction.append(code);
    const copy=document.createElement('button');copy.type='button';copy.className='button ghost small';copy.textContent='复制登录码';
    const copyCode=async trigger=>{
      if(await copyGitHubDeviceCode(started.userCode,trigger)){copy.textContent='已复制';return}
      selectGitHubDeviceCode(code);copy.textContent='已选中，按 Ctrl/⌘+C 复制';
    };
    copy.onclick=()=>copyCode(copy);
    const link=document.createElement('a');link.href=started.verificationUri;link.target='_blank';link.rel='noopener noreferrer';link.className='button primary small';link.textContent='复制并前往 OpenAI 登录';link.onclick=()=>{void copyCode(link)};
    const cancel=document.createElement('button');cancel.type='button';cancel.className='button ghost small';cancel.textContent='取消';cancel.dataset.codexDeviceCancel='';
    panel.replaceChildren(instruction,copy,link,cancel);
    setStatus('等待在 OpenAI 页面确认…');
    timer=setTimeout(poll,Math.max(3,started.interval||5)*1000);
  }

  dialog.onclick=event=>{
    const button=event.target.closest('button');
    if(!button)return;
    const action=button.dataset;
    if('codexClose' in action)return dialog.close();
    if('codexDeviceCancel' in action){
      stopDevice();paint();
      void api('/api/auth/codex/device',{method:'DELETE',silent:true}).catch(()=>{});
      return;
    }
    if(busy)return;
    if('codexDevice' in action)return run(async()=>{
      setStatus('正在获取登录码…');
      const started=await api('/api/auth/codex/device',{method:'POST',silent:true,body:'{}'});
      if(live())showDevice(started);
    });
    if('codexPaste' in action){form.hidden=false;actions.hidden=true;authJSON.focus();return}
    if('codexPasteCancel' in action){form.reset();form.hidden=true;actions.hidden=false;return}
    if('codexSignout' in action)return run(async()=>{
      setStatus('正在退出登录…');
      const result=await api('/api/auth/codex',{method:'DELETE',silent:true});
      saved(result);
      setStatus('已退出 ChatGPT 登录');
    });
  };
  form.onsubmit=event=>{
    event.preventDefault();
    return run(async()=>{
      setStatus('正在保存…');
      const result=await api('/api/auth/codex',{method:'PUT',silent:true,body:JSON.stringify({authJson:authJSON.value})});
      form.reset();form.hidden=true;actions.hidden=false;
      saved(result);
      toast('ChatGPT 登录已保存','ok');
    });
  };
  dialog.addEventListener('keydown',event=>{if(event.key==='Escape'||event.metaKey||event.ctrlKey)event.stopPropagation()});
  dialog.addEventListener('cancel',event=>{if(busy)event.preventDefault()});
  dialog.addEventListener('close',()=>{
    if(flow)void api('/api/auth/codex/device',{method:'DELETE',silent:true}).catch(()=>{});
    clearTimeout(timer);flow=null;
    form.reset();
    dialog.remove();
    if(opener?.isConnected)opener.focus({preventScroll:true});
    else $('#credentialPanel [data-credential-row="codexChatGptLogin"] [data-credential-act="setup"]')?.focus({preventScroll:true});
  });
  document.body.append(dialog);
  dialog.showModal();
  hydrateCredentialIcons(dialog);
  paint();
  if(current()?.codexChatGptLoginConfigured)void api('/api/auth/codex',{silent:true}).then(result=>{
    if(!live()||busy||flow)return;
    email=result.email||'';
    paint();
  }).catch(()=>{});
}
