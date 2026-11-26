const GIT_HTTP_PLATFORMS = [['github', 'GitHub / GitHub Enterprise'], ['gitlab', 'GitLab'], ['other', 'Nền tảng khác']];

// Mirrors gitwork.HTTPSHostScope closely enough to prefill the form; the server
// remains the authority on what a host scope is.
function gitHTTPHostScope(raw) {
  let host = String(raw || '').trim().toLowerCase().replace(/^https:\/\//, '').replace(/\/$/, '');
  if (host.endsWith(':443')) host = host.slice(0, -4);
  return host;
}

function gitHTTPDefaultUsername(platform) {
  return platform === 'gitlab' ? 'oauth2' : 'x-access-token';
}

function openGitHTTPCredentials() {
  const user = state.currentUser;
  if (!user?.id || user.isGuest) return;

  const previous = $('#gitHTTPCredentialDialog');
  if (previous?.open) return;
  const opener = document.activeElement;
  const endpoint = `/api/users/${encodeURIComponent(user.id)}/git-http-credentials`;
  let creds = [];
  let editing = null;
  let deleting = '';
  let busy = false;

  const dialog = document.createElement('dialog');
  dialog.id = 'gitHTTPCredentialDialog';
  dialog.className = 'git-ssh-dialog git-http-dialog';
  dialog.setAttribute('aria-labelledby', 'gitHTTPCredentialTitle');
  dialog.innerHTML = `
    <header class="git-ssh-dialog-head">
      <h2 id="gitHTTPCredentialTitle">Git access token (theo máy chủ)</h2>
      <button type="button" class="credential-btn ghost icon" data-http-cred-close aria-label="Đóng">${credentialGlyph('close', 16)}</button>
    </header>
    <p class="git-ssh-description">Mỗi máy chủ HTTPS dùng một personal access token. Khi nền tảng và workspace truy cập máy chủ đó, hệ thống dùng thông tin xác thực của người phụ trách hoặc người đang nói trong lượt này.</p>
    <p class="git-ssh-status" role="status">Đang đọc thông tin xác thực…</p>
    <div class="git-ssh-key-list"></div>
    <button type="button" class="credential-btn primary" data-http-cred-add disabled>Thêm token</button>
    <form class="git-ssh-key-form" hidden>
      <h3></h3>
      ${credentialFieldHTML({label:'Máy chủ HTTPS', name:'host', type:'text', placeholder:'github.com hoặc gitlab.example.org:8443', hint:'Chỉ điền tên máy chủ, có thể kèm cổng; token chỉ dùng cho máy chủ này.'})}
      <label class="credential-field">
        <span>Nền tảng</span>
        <div class="credential-input is-plain">
          <select name="platform" class="git-http-select" aria-label="Nền tảng" aria-describedby="gitHTTPCredentialPlatformHint">
            ${GIT_HTTP_PLATFORMS.map(([value, label]) => `<option value="${value}">${escapeHTML(label)}</option>`).join('')}
          </select>
        </div>
        <small id="gitHTTPCredentialPlatformHint">GitHub và GitLab hỗ trợ xác thực gh / glab và đồng bộ trạng thái merge PR/MR. Các nền tảng khác vẫn có thể để Agent tạo PR/MR; hãy xem trạng thái merge trên nền tảng lưu trữ.</small>
      </label>
      ${credentialFieldHTML({label:'Tên', name:'name', type:'text', placeholder:'Ví dụ GitLab công ty', hint:null})}
      ${credentialFieldHTML({label:'Tên người dùng xác thực Git', name:'username', type:'text', placeholder:'x-access-token', hint:'Điền theo yêu cầu của nền tảng lưu trữ; đây là tên người dùng xác thực HTTPS, không phải tên tác giả commit, thường không suy ra được từ Token.'})}
      ${credentialFieldHTML({label:'Personal access token', name:'token', placeholder:'Dán personal access token', hint:'Sau khi lưu sẽ không hiện lại; khi sửa hãy để trống nếu muốn giữ token hiện tại.'})}
      <footer class="git-ssh-form-actions">
        <button type="button" class="credential-btn secondary" data-http-cred-cancel>Hủy</button>
        <button type="submit" class="credential-btn primary">Lưu token</button>
      </footer>
    </form>`;

  const list = dialog.querySelector('.git-ssh-key-list');
  const status = dialog.querySelector('.git-ssh-status');
  const add = dialog.querySelector('[data-http-cred-add]');
  const form = dialog.querySelector('form');
  const field = key => form.elements.namedItem(key);
  const [host, platform, name, username, token] = ['host', 'platform', 'name', 'username', 'token'].map(field);
  host.maxLength = 255;
  name.maxLength = 80;
  username.maxLength = 128;
  token.maxLength = 4096;
  host.required = name.required = true;

  function setStatus(message = '', error = false) {
    status.textContent = message;
    status.classList.toggle('is-error', error);
    status.hidden = !message;
  }

  function credentialSummary(cred) {
    const platformLabel = (GIT_HTTP_PLATFORMS.find(([value]) => value === cred.platform) || [, cred.platform])[1];
    const tags = [platformLabel, cred.managed ? 'Nền tảng lưu trữ' : ''].filter(Boolean);
    return `<div class="git-ssh-key-summary"><b>${escapeHTML(cred.name)}</b><small><code>${escapeHTML(cred.host)}</code> · ${tags.map(escapeHTML).join(' · ')}</small></div>`;
  }

  function renderList() {
    list.innerHTML = creds.map(cred => {
      const id = escapeHTML(cred.id);
      const verify = cred.platform !== 'other' || cred.managed ? `<button type="button" class="credential-btn ghost" data-http-cred-verify="${id}">Kiểm tra token</button>` : '';
      const actions = cred.managed
        ? verify
        : deleting === cred.id
          ? `<span>Xóa token này?</span><button type="button" class="credential-btn ghost" data-http-cred-keep>Hủy</button><button type="button" class="credential-btn danger-solid" data-http-cred-remove="${id}">Xác nhận xóa</button>`
          : `${verify}<button type="button" class="credential-btn ghost" data-http-cred-edit="${id}">Sửa</button><button type="button" class="credential-btn ghost" data-http-cred-delete="${id}">Xóa</button>`;
      return `<div class="git-ssh-key-row">${credentialSummary(cred)}${actions}</div>`;
    }).join('');
  }

  function openForm(cred) {
    editing = cred;
    deleting = '';
    renderList();
    form.reset();
    form.hidden = false;
    add.hidden = true;
    form.querySelector('h3').textContent = cred ? 'Sửa token' : 'Thêm token';
    host.value = cred?.host || '';
    host.readOnly = !!cred;
    platform.value = cred?.platform || 'github';
    name.value = cred?.name || '';
    username.value = cred?.username || '';
    username.placeholder = gitHTTPDefaultUsername(platform.value);
    token.required = !cred;
    token.placeholder = cred ? 'Để trống để giữ token hiện tại' : 'Dán personal access token';
    (cred ? name : host).focus();
  }

  function closeForm() {
    form.reset();
    form.hidden = true;
    add.hidden = false;
    editing = null;
    add.focus();
  }

  function setBusy(on) {
    busy = on;
    dialog.toggleAttribute('aria-busy', on);
    form.querySelector('button[type="submit"]').disabled = on;
  }

  async function request(method, body, id) {
    if (busy) return;
    setBusy(true);
    setStatus(method === 'PUT' ? 'Đang lưu…' : 'Đang xóa…');
    try {
      creds = await api(id ? `${endpoint}/${encodeURIComponent(id)}` : endpoint, {
        method, silent:true, ...(body ? {body:JSON.stringify(body)} : {}),
      });
      storeCredentialUser({...credentialUser(), id:user.id, gitHttpCredentialsConfigured:creds.length > 0, gitHubCredentialConfigured:creds.some(credential=>credential.host==='github.com')});
      renderCredentialPanel();
      notifyCredentialsChanged();
      deleting = '';
      renderList();
      if (method === 'PUT') closeForm();
      setStatus(method === 'PUT' ? 'Đã lưu token' : 'Đã xóa token');
    } catch (error) {
      setStatus(error.message, true);
    } finally {
      setBusy(false);
    }
  }

  async function verify(id) {
    if (busy) return;
    setBusy(true);
    setStatus('Đang kiểm tra…');
    try {
      const result = await api(`${endpoint}/${encodeURIComponent(id)}/verify`, {method:'POST', silent:true});
      if (result?.valid) setStatus(result.login ? `Token còn hiệu lực, tài khoản ${result.login}` : 'Token còn hiệu lực');
      else setStatus(result?.error || 'Token không vượt qua kiểm tra', true);
    } catch (error) {
      setStatus(error.message, true);
    } finally {
      setBusy(false);
    }
  }

  dialog.onclick = event => {
    const button = event.target.closest('button');
    if (!button || busy) return;
    const action = button.dataset;
    if ('httpCredClose' in action) return dialog.close();
    if ('httpCredCancel' in action) return closeForm();
    if ('httpCredAdd' in action) return openForm(null);
    if (action.httpCredVerify) return verify(action.httpCredVerify);
    if (action.httpCredEdit) return openForm(creds.find(cred => cred.id === action.httpCredEdit));
    if (action.httpCredRemove) return request('DELETE', null, action.httpCredRemove);
    if (action.httpCredDelete || 'httpCredKeep' in action) {
      deleting = action.httpCredDelete || '';
      renderList();
      (list.querySelector('[data-http-cred-remove]') || add).focus();
    }
  };
  host.addEventListener('change', () => {
    if (editing) return;
    const scope = gitHTTPHostScope(host.value);
    if (scope === 'github.com') platform.value = 'github';
    else if (/(^|\.)gitlab\./.test(scope)) platform.value = 'gitlab';
    username.placeholder = gitHTTPDefaultUsername(platform.value);
    if (!name.value.trim()) name.value = scope;
  });
  platform.addEventListener('change', () => { username.placeholder = gitHTTPDefaultUsername(platform.value); });
  form.onsubmit = event => {
    event.preventDefault();
    const body = {
      host:host.value, platform:platform.value, name:name.value,
      username:username.value.trim() || gitHTTPDefaultUsername(platform.value),
      token:token.value,
    };
    if (editing) body.id = editing.id;
    return request('PUT', body);
  };
  dialog.addEventListener('keydown', event => {
    if (event.key === 'Escape' || event.metaKey || event.ctrlKey) event.stopPropagation();
  });
  dialog.addEventListener('cancel', event => { if (busy) event.preventDefault(); });
  dialog.addEventListener('close', () => {
    form.reset();
    dialog.remove();
    if (opener?.isConnected) opener.focus({preventScroll:true});
    else $('#credentialPanel [data-credential-row="gitHttpCredentials"] [data-credential-act="setup"]')?.focus({preventScroll:true});
  });
  document.body.append(dialog);
  dialog.showModal();
  hydrateCredentialIcons(dialog);
  api(endpoint, {silent:true}).then(items => {
    if (!dialog.open) return;
    creds = Array.isArray(items) ? items : [];
    add.disabled = false;
    setStatus(creds.length ? '' : 'Chưa cấu hình Git access token');
    renderList();
  }).catch(error => setStatus(error.message, true));
}
