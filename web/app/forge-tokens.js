async function openForgeTokens() {
  const user=state.currentUser;
  if(!user?.id||user.isGuest||$('#forgeTokenDialog')?.open)return;
  const endpoint=`/api/users/${encodeURIComponent(user.id)}/forge-tokens`;
  const opener=document.activeElement;
  const dialog=document.createElement('dialog');
  dialog.id='forgeTokenDialog';
  dialog.className='git-ssh-dialog';
  dialog.setAttribute('aria-labelledby','forgeTokenTitle');
  dialog.innerHTML=`
    <header class="git-ssh-dialog-head">
      <h2 id="forgeTokenTitle">Forgejo 访问令牌</h2>
      <button type="button" class="credential-btn ghost icon" data-forge-close aria-label="关闭">${credentialGlyph('close',16)}</button>
    </header>
    <p class="git-ssh-description">在 Forgejo「设置 → 应用」生成令牌。校验身份需要 <code>read:user</code>，创建和更新 PR 需要 <code>write:repository</code>。</p>
    <p class="git-ssh-status" role="status">正在读取配置…</p>
    <div class="git-ssh-key-list"></div>
    <form class="git-ssh-key-form" hidden>
      <label class="pa-field"><span class="pa-field-l">Forgejo 实例</span><select class="pa-input" name="instance" required></select></label>
      ${credentialFieldHTML({label:'个人访问令牌',name:'token',placeholder:'粘贴新令牌'})}
      <footer class="git-ssh-form-actions">
        <button type="submit" class="credential-btn primary">校验并保存</button>
      </footer>
    </form>`;
  const status=dialog.querySelector('[role="status"]'),list=dialog.querySelector('.git-ssh-key-list');
  const form=dialog.querySelector('form'),select=form.elements.instance,token=form.elements.token;
  token.required=true;
  token.maxLength=512;

  function render({instances,tokens}) {
    const previous=select.value;
    select.replaceChildren(...instances.map(instance=>new Option(instance,instance)));
    if(instances.includes(previous))select.value=previous;
    form.hidden=instances.length===0;
    list.innerHTML=tokens.map(({id,instance})=>`
      <div class="git-ssh-key-row">
        <div class="git-ssh-key-summary"><b>${escapeHTML(instance)}</b></div>
        <button type="button" class="credential-btn ghost" data-forge-rotate="${escapeHTML(instance)}"${instances.includes(instance)?'':' disabled'}>更换</button>
        <button type="button" class="credential-btn ghost" data-forge-remove="${escapeHTML(id)}">移除</button>
      </div>`).join('');
    if(state.currentUser?.id===user.id){
      storeCredentialUser({...credentialUser(),id:user.id,forgeTokenConfigured:tokens.length>0});
      renderCredentialPanel();
      notifyCredentialsChanged();
    }
    status.textContent=form.hidden?'请管理员先启用至少一个 Forgejo 实例。':'';
  }

  let busy=false;
  async function request(method,path,body) {
    if(busy)return;
    busy=true;
    dialog.setAttribute('aria-busy','true');
    const save=form.querySelector('[type="submit"]');
    save.disabled=true;
    status.classList.remove('is-error');
    try{
      const data=await api(path,{method,silent:true,...(body?{body:JSON.stringify(body)}:{})});
      if(!dialog.open)return;
      render(data);
      if(method==='PUT'){
        token.value='';
        status.textContent='令牌已保存。';
      }
    }catch(err){
      status.classList.add('is-error');
      status.textContent=err.message;
    }finally{
      busy=false;
      save.disabled=false;
      dialog.removeAttribute('aria-busy');
    }
  }
  form.onsubmit=event=>{
    event.preventDefault();
    return request('PUT',endpoint,{instance:select.value,token:token.value.trim()});
  };
  dialog.onclick=event=>{
    const button=event.target.closest('button');
    if(!button)return;
    if(button.hasAttribute('data-forge-close'))return dialog.close();
    if(button.hasAttribute('data-credential-eye'))return toggleCredentialSecret(button);
    if(busy)return;
    if(button.dataset.forgeRotate){
      select.value=button.dataset.forgeRotate;
      token.focus();
    }else if(button.dataset.forgeRemove){
      return request('DELETE',`${endpoint}/${encodeURIComponent(button.dataset.forgeRemove)}`);
    }
  };
  dialog.addEventListener('close',()=>{
    token.value='';
    dialog.remove();
    const fallback=$('#credentialPanel [data-credential-row="forgeToken"] [data-credential-act="setup"]');
    (opener?.isConnected?opener:fallback)?.focus();
  },{once:true});
  document.body.append(dialog);
  hydrateCredentialIcons(dialog);
  dialog.showModal();
  await request('GET',endpoint);
}
