const platformSSHHostsEndpoint='/api/platform/git-ssh-hosts';

function platformSSHHostTrustHTML(){
  return `
    <section class="platform-admin-main-card" id="platformSSHHostTrustCard">
      <div class="platform-admin-head">
        <div class="platform-admin-head-meta">
          <h2>${lfT('受信任的 Git SSH 主机','Trusted Git SSH hosts')}</h2>
          <p>${lfT('登记 Git 服务器的公钥，供全平台连接该主机时校验身份。','Register Git server public keys for use across the platform.')}</p>
        </div>
        <button type="button" class="button primary" data-ssh-host-add>${lfT('添加主机','Add host')}</button>
      </div>
      <form class="platform-shared-key-form platform-ssh-host-form" autocomplete="off" hidden>
        <label class="platform-field">
          <span>${lfT('SSH 主机','SSH host')}</span>
          <input name="host" placeholder="git.example.org:2222" maxlength="260" required spellcheck="false">
          <small class="platform-field-hint">${lfT('省略端口时使用 22。','The default port is 22.')}</small>
        </label>
        <label class="platform-field">
          <span>${lfT('服务器主机公钥','Server host public keys')}</span>
          <textarea name="publicKeys" rows="4" maxlength="16384" placeholder="ssh-ed25519 …" required spellcheck="false"></textarea>
          <small class="platform-field-hint">${lfT('粘贴 Git 服务方提供的 OpenSSH 公钥，每行一把。','Paste OpenSSH public keys from the Git provider, one per line.')}</small>
        </label>
        <div class="platform-shared-key-actions">
          <button type="button" class="button ghost" data-ssh-host-cancel>${lfT('取消','Cancel')}</button>
          <button type="submit" class="button primary">${lfT('核对指纹','Review fingerprints')}</button>
        </div>
      </form>
      <div class="platform-admin-list" data-ssh-host-list aria-live="polite"></div>
    </section>`;
}

async function loadPlatformSSHHosts(host){
  const card=host.querySelector('#platformSSHHostTrustCard');
  if(!card)return;
  const list=card.querySelector('[data-ssh-host-list]');
  const form=card.querySelector('form');
  const add=card.querySelector('[data-ssh-host-add]');
  const hostField=form.elements.namedItem('host');
  const keysField=form.elements.namedItem('publicKeys');
  let entries=[];
  let editing=null;
  let busy=false;

  const showForm=(entry)=>{
    editing=entry||null;
    hostField.value=editing?.host||'';
    hostField.readOnly=Boolean(editing);
    keysField.value=entry?.publicKeys||'';
    form.hidden=false;
    (editing?keysField:hostField).focus();
  };
  const hideForm=()=>{
    editing=null;
    form.reset();
    form.hidden=true;
  };
  const setBusy=(value)=>{
    busy=value;
    add.disabled=value;
    for(const field of form.elements)field.disabled=value;
    for(const button of list.querySelectorAll('button'))button.disabled=value;
  };
  const render=()=>{
    setPlatformTabCount(host,'sshTrust',entries.length);
    list.innerHTML=entries.length?entries.map(entry=>`
      <div class="platform-admin-row">
        <div class="platform-admin-info platform-ssh-host-info" data-i18n-skip>
          <strong>${escapeHTML(entry.host)}</strong>
          ${entry.fingerprints.map(value=>`<code>${escapeHTML(value)}</code>`).join('')}
        </div>
        <div class="platform-admin-actions">
          <button type="button" class="button ghost small" data-ssh-host-edit="${escapeHTML(entry.host)}">${lfT('编辑公钥','Edit keys')}</button>
          <button type="button" class="button ghost small danger" data-ssh-host-remove="${escapeHTML(entry.host)}">${lfT('移除','Remove')}</button>
        </div>
      </div>`).join(''):`<p class="prop-hint">${lfT('尚未登记 Git SSH 主机。','No Git SSH hosts registered.')}</p>`;
  };

  const refresh=async()=>{
    entries=await api(platformSSHHostsEndpoint,{silent:true});
    render();
  };
  const showWriteError=async(err)=>{
    toast(err.message,'err');
    if(err.code==='ssh_host_trust_changed'){
      try{
        await refresh();
      }catch(refreshError){
        toast(refreshError.message,'err');
      }
    }
  };

  add.onclick=()=>showForm();
  form.querySelector('[data-ssh-host-cancel]').onclick=()=>{
    hideForm();
    add.focus();
  };
  form.onsubmit=async(event)=>{
    event.preventDefault();
    if(busy)return;
    const body=JSON.stringify({host:hostField.value.trim(),publicKeys:keysField.value.trim()});
    setBusy(true);
    try{
      const preview=await api(`${platformSSHHostsEndpoint}/preview`,{method:'POST',body});
      const message=[
        preview.host,
        preview.fingerprints.join('\n'),
        lfT('请与 Git 服务方提供的指纹核对。确认后，全平台将信任这些公钥。','Compare these fingerprints with the Git provider before trusting these keys across the platform.'),
      ].join('\n\n');
      if(!await afConfirm({title:lfT('确认 SSH 主机指纹','Confirm SSH host fingerprints'),message,confirmText:lfT('确认信任','Trust host')}))return;
      const endpoint=editing?`${platformSSHHostsEndpoint}/${encodeURIComponent(editing.host)}`:platformSSHHostsEndpoint;
      const options={method:editing?'PUT':'POST',body};
      if(editing)options.headers={'Content-Type':'application/json','If-Match':JSON.stringify(editing.revision)};
      const entry=await api(endpoint,options);
      entries=entries.filter(item=>item.host!==entry.host).concat(entry).sort((a,b)=>a.host.localeCompare(b.host));
      hideForm();
      render();
      toast(lfT('已保存 Git SSH 主机信任','Git SSH host trust saved'),'ok');
    }catch(err){
      await showWriteError(err);
    }finally{
      setBusy(false);
    }
  };
  list.onclick=async(event)=>{
    if(busy)return;
    const edit=event.target.closest('[data-ssh-host-edit]');
    if(edit){
      showForm(entries.find(entry=>entry.host===edit.dataset.sshHostEdit));
      return;
    }
    const remove=event.target.closest('[data-ssh-host-remove]');
    if(!remove)return;
    const entry=entries.find(item=>item.host===remove.dataset.sshHostRemove);
    if(!entry)return;
    const endpoint=entry.host;
    setBusy(true);
    try{
      const confirmed=await afConfirm({
        title:lfT('移除 SSH 主机信任','Remove SSH host trust'),
        message:lfT(`移除在页面登记的 ${endpoint} 公钥？`,`Remove the keys registered here for ${endpoint}?`),
        confirmText:lfT('移除','Remove'),
        danger:true,
      });
      if(!confirmed)return;
      await api(`${platformSSHHostsEndpoint}/${encodeURIComponent(endpoint)}`,{
        method:'DELETE',
        headers:{'If-Match':JSON.stringify(entry.revision)},
      });
      entries=entries.filter(item=>item.host!==endpoint);
      if(editing?.host===endpoint)hideForm();
      render();
      toast(lfT('已移除 Git SSH 主机信任','Git SSH host trust removed'),'ok');
    }catch(err){
      await showWriteError(err);
    }finally{
      setBusy(false);
    }
  };

  list.textContent=lfT('正在读取主机信任…','Loading trusted hosts…');
  setBusy(true);
  try{
    await refresh();
  }catch(err){
    list.textContent=err.message;
  }finally{
    setBusy(false);
  }
}
