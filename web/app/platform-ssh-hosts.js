const platformSSHHostsEndpoint='/api/platform/git-ssh-hosts';

function platformSSHHostTrustHTML(){
  return `
    <section class="platform-admin-main-card" id="platformSSHHostTrustCard">
      <div class="platform-admin-head">
        <div class="platform-admin-head-meta">
          <h2>${lfT('受信任的 Git SSH 主机','Trusted Git SSH hosts','Các máy chủ Git SSH được tin cậy')}</h2>
          <p>${lfT('登记 Git 服务器的公钥，供全平台连接该主机时校验身份。','Register Git server public keys for use across the platform.','Đăng ký khóa công khai của máy chủ Git để toàn nền tảng kiểm tra danh tính khi kết nối máy chủ đó.')}</p>
        </div>
        <button type="button" class="button primary" data-ssh-host-add>${lfT('添加主机','Add host','Thêm máy chủ')}</button>
      </div>
      <form class="platform-shared-key-form platform-ssh-host-form" autocomplete="off" hidden>
        <label class="platform-field">
          <span>${lfT('SSH 主机','SSH host','Máy chủ SSH')}</span>
          <input name="host" placeholder="git.example.org:2222" maxlength="260" required spellcheck="false">
          <small class="platform-field-hint">${lfT('省略端口时使用 22。','The default port is 22.','Nếu bỏ trống cổng, mặc định là 22.')}</small>
        </label>
        <label class="platform-field">
          <span>${lfT('从哪里读取公钥','Read keys from','Đọc khóa công khai từ')}</span>
          <select name="runnerId">
            <option value="">${lfT('平台服务器','Platform server','Máy chủ nền tảng')}</option>
          </select>
          <small class="platform-field-hint">${lfT('内网主机可选择能访问它的个人执行机器。','For private hosts, select a personal Runner with access.','Với máy chủ nội bộ, hãy chọn máy thực thi cá nhân có thể truy cập được máy đó.')}</small>
        </label>
        <div class="platform-shared-key-actions">
          <button type="button" class="button ghost" data-ssh-host-cancel>${lfT('取消','Cancel','Hủy')}</button>
          <button type="submit" class="button primary" data-ssh-host-probe>${lfT('读取公钥并核对指纹','Read key and review fingerprint','Đọc khóa công khai và đối chiếu fingerprint')}</button>
        </div>
        <details class="platform-ssh-host-manual">
          <summary>${lfT('手动录入公钥','Enter public keys manually','Nhập khóa công khai thủ công')}</summary>
          <label class="platform-field">
            <span>${lfT('服务器主机公钥','Server host public keys','Khóa công khai của máy chủ')}</span>
            <textarea name="publicKeys" rows="4" maxlength="16384" placeholder="ssh-ed25519 …" spellcheck="false"></textarea>
            <small class="platform-field-hint">${lfT('粘贴 Git 服务方提供的 OpenSSH 公钥，每行一把。','Paste OpenSSH public keys from the Git provider, one per line.','Dán khóa công khai OpenSSH do nhà cung cấp Git cấp, mỗi dòng một khóa.')}</small>
          </label>
          <button type="submit" class="button ghost" data-ssh-host-manual>${lfT('核对已填公钥','Review entered keys','Đối chiếu khóa đã nhập')}</button>
        </details>
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
  const runnerField=form.elements.namedItem('runnerId');
  const keysField=form.elements.namedItem('publicKeys');
  const manualDetails=form.querySelector('details');
  let entries=[];
  let editing=null;
  let busy=false;

  const refreshRunners=async()=>{
    try{
      const runners=await api('/api/runners?mine=1',{silent:true});
      const selected=runnerField.value;
      runnerField.options.length=1;
      for(const runner of runners){
        if(runner.status==='online')runnerField.add(new Option(runner.name||runner.id,runner.id));
      }
      if(Array.from(runnerField.options).some(option=>option.value===selected))runnerField.value=selected;
    }catch{
      runnerField.options.length=1;
    }
  };
  const showForm=(entry)=>{
    editing=entry||null;
    hostField.value=editing?.host||'';
    hostField.readOnly=Boolean(editing);
    runnerField.value='';
    keysField.value=entry?.publicKeys||'';
    manualDetails.open=false;
    form.hidden=false;
    (editing?form.querySelector('[data-ssh-host-probe]'):hostField).focus();
    void refreshRunners();
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
    list.innerHTML=entries.length?entries.map((entry,index)=>`
      <div class="platform-admin-row">
        <div class="platform-admin-info platform-ssh-host-info" data-i18n-skip>
          <strong>${escapeHTML(entry.host||lfT('空主机名','Empty hostname','Tên máy chủ trống'))}</strong>
          ${entry.invalid
            ?`<span class="hui-badge" data-size="sm" data-tone="warn"><span class="hui-badge-label">${entry.invalidHost?lfT('主机格式无效','Invalid host','Định dạng máy chủ không hợp lệ'):lfT('公钥格式无效','Invalid public keys','Định dạng khóa công khai không hợp lệ')}</span></span>`
            :entry.fingerprints.map(value=>`<code>${escapeHTML(value)}</code>`).join('')}
        </div>
        <div class="platform-admin-actions">
          ${entry.invalidHost?'':`<button type="button" class="button ghost small" data-ssh-host-edit="${index}">${entry.invalid?lfT('修复公钥','Repair keys','Sửa khóa công khai'):lfT('编辑公钥','Edit keys','Sửa khóa công khai')}</button>`}
          <button type="button" class="button ghost small danger" data-ssh-host-remove="${index}">${lfT('移除','Remove','Gỡ')}</button>
        </div>
      </div>`).join(''):`<p class="prop-hint">${lfT('尚未登记 Git SSH 主机。','No Git SSH hosts registered.','Chưa đăng ký máy chủ Git SSH.')}</p>`;
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
    const manual=event.submitter?.hasAttribute('data-ssh-host-manual');
    const input={host:hostField.value.trim()};
    if(manual)input.publicKeys=keysField.value.trim();
    else input.probe={runnerId:runnerField.value};
    setBusy(true);
    try{
      const preview=await api(`${platformSSHHostsEndpoint}/preview`,{method:'POST',body:JSON.stringify(input)});
      const message=[
        preview.host,
        preview.fingerprints.join('\n'),
        lfT('请与 Git 服务方提供的指纹核对。确认后，全平台将信任这些公钥。','Compare these fingerprints with the Git provider before trusting these keys across the platform.','Hãy đối chiếu các fingerprint này với nhà cung cấp Git. Sau khi xác nhận, toàn nền tảng sẽ tin cậy các khóa công khai này.'),
        editing&&lfT('保存会替换这条记录原有的公钥。','Saving replaces the public keys in this registration.','Lưu sẽ thay khóa công khai hiện có của bản ghi này.'),
      ].filter(Boolean).join('\n\n');
      if(!await afConfirm({title:lfT('确认 SSH 主机指纹','Confirm SSH host fingerprints','Xác nhận fingerprint máy chủ SSH'),message,confirmText:lfT('确认信任','Trust host','Xác nhận tin cậy')}))return;
      const endpoint=editing?`${platformSSHHostsEndpoint}/${encodeURIComponent(editing.host)}`:platformSSHHostsEndpoint;
      const body=JSON.stringify({host:preview.host,publicKeys:preview.publicKeys});
      const options={method:editing?'PUT':'POST',body};
      if(editing)options.headers={'Content-Type':'application/json','If-Match':JSON.stringify(editing.revision)};
      const entry=await api(endpoint,options);
      entries=entries.filter(item=>item.host!==entry.host).concat(entry).sort((a,b)=>a.host.localeCompare(b.host));
      hideForm();
      render();
      toast(lfT('已保存 Git SSH 主机信任','Git SSH host trust saved','Đã lưu tin cậy máy chủ Git SSH'),'ok');
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
      showForm(entries[Number(edit.dataset.sshHostEdit)]);
      return;
    }
    const remove=event.target.closest('[data-ssh-host-remove]');
    if(!remove)return;
    const entry=entries[Number(remove.dataset.sshHostRemove)];
    if(!entry)return;
    const hostname=entry.host||lfT('空主机名','Empty hostname','Tên máy chủ trống');
    const endpoint=entry.invalid
      ?`${platformSSHHostsEndpoint}?host=${encodeURIComponent(entry.host)}`
      :`${platformSSHHostsEndpoint}/${encodeURIComponent(entry.host)}`;
    setBusy(true);
    try{
      const confirmed=await afConfirm({
        title:lfT('移除 SSH 主机信任','Remove SSH host trust','Gỡ tin cậy máy chủ SSH'),
        message:lfT(`移除 ${hostname} 的登记记录？`,`Remove the registration for ${hostname}?`,`Gỡ bản đăng ký của ${hostname}?`),
        confirmText:lfT('移除','Remove','Gỡ'),
        danger:true,
      });
      if(!confirmed)return;
      await api(endpoint,{
        method:'DELETE',
        headers:{'If-Match':JSON.stringify(entry.revision)},
      });
      entries=entries.filter(item=>item.host!==entry.host);
      if(editing?.host===entry.host)hideForm();
      render();
      toast(lfT('已移除 Git SSH 主机信任','Git SSH host trust removed','Đã gỡ tin cậy máy chủ Git SSH'),'ok');
    }catch(err){
      await showWriteError(err);
    }finally{
      setBusy(false);
    }
  };

  list.textContent=lfT('正在读取主机信任…','Loading trusted hosts…','Đang đọc danh sách máy chủ được tin cậy…');
  setBusy(true);
  try{
    await refresh();
  }catch(err){
    list.textContent=err.message;
  }finally{
    setBusy(false);
  }
}
