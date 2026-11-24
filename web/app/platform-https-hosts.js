function platformHTTPHostTrustHTML(protocol) {
  const scheme = protocol.toUpperCase();
  const plain = protocol === 'http';
  return `
    <section class="platform-admin-main-card" id="platform${scheme}HostTrustCard">
      <div class="platform-admin-head">
        <div class="platform-admin-head-meta">
          <h2>${lfT(`Git ${scheme} 主机授权`, `Git ${scheme} host authorization`)}</h2>
          <p>${plain
            ? lfT('HTTP 会明文传输 Git 令牌。请仅授权可信网络中的仓库主机。', 'HTTP sends Git tokens in cleartext. Authorize hosts on trusted networks only.')
            : lfT('授权平台访问自建 Git 实例，用于校验令牌和跟踪合并请求。', 'Authorize access to self-hosted Git instances for token verification and merge request tracking.')}</p>
        </div>
        <button type="button" class="button primary" data-https-host-add>${lfT(`添加 ${scheme} 主机`, `Add ${scheme} host`)}</button>
      </div>
      <form class="platform-shared-key-form platform-https-host-form" autocomplete="off" hidden>
        <label class="platform-field">
          <span>${lfT(`${scheme} 主机`, `${scheme} host`)}</span>
          <input name="host" placeholder="${plain ? 'http://git.example.org:8080' : 'gitlab.example.org:8443'}" maxlength="${plain ? 267 : 260}" required spellcheck="false">
          <small class="platform-field-hint">${plain ? lfT(
            '填写域名、IPv4 或 IPv6，默认端口为 80。IPv6 带端口用 [fd00::2]:8080。',
            'Enter a hostname, IPv4 or IPv6 address. Port 80 is the default. Use [fd00::2]:8080 for IPv6 with a port.',
          ) : lfT(
            '填写域名或 IPv6。默认端口为 443，IPv6 带端口用 [fd00::2]:8443。IPv4 服务器请填域名，在「手动编辑地址」里填 IPv4。',
            'Enter a hostname or IPv6 address. Port 443 is the default. Use [fd00::2]:8443 for IPv6 with a port. For an IPv4 server, enter its hostname and put the IP under Edit addresses manually.',
          )}</small>
        </label>
        <label class="platform-field"${plain ? ' hidden' : ''}>
          <span>${lfT('托管平台', 'Git provider')}</span>
          <select name="platform">
            ${GIT_HTTP_PLATFORMS.map(([value, label]) => `<option value="${value}">${escapeHTML(label)}</option>`).join('')}
          </select>
        </label>
        <div class="platform-shared-key-actions">
          <button type="button" class="button ghost" data-https-host-cancel>${lfT('取消', 'Cancel')}</button>
          <button type="submit" class="button primary">${lfT('解析并核对地址', 'Resolve and review addresses')}</button>
        </div>
        <details class="platform-host-manual">
          <summary>${lfT('手动编辑地址', 'Edit addresses manually')}</summary>
          <label class="platform-field">
            <span>${lfT('允许连接的 IP 地址', 'Approved IP addresses')}</span>
            <textarea name="addresses" rows="4" maxlength="8192" spellcheck="false"></textarea>
            <small class="platform-field-hint">${lfT('每行填写一个 IPv4 或 IPv6 地址。', 'Enter one IPv4 or IPv6 address per line.')}</small>
          </label>
          <button type="submit" class="button ghost" data-https-host-manual>${lfT('核对已填地址', 'Review entered addresses')}</button>
        </details>
      </form>
      <div class="platform-admin-list" data-https-host-list aria-live="polite"></div>
    </section>`;
}

async function loadPlatformHTTPHosts(host, protocol) {
  const scheme = protocol.toUpperCase();
  const plain = protocol === 'http';
  const endpoint = `/api/platform/git-${protocol}-hosts`;
  const card = host.querySelector(`#platform${scheme}HostTrustCard`);
  if (!card) return;
  const form = card.querySelector('form');
  const add = card.querySelector('[data-https-host-add]');
  const list = card.querySelector('[data-https-host-list]');
  const hostField = form.elements.namedItem('host');
  const platformField = form.elements.namedItem('platform');
  const addressesField = form.elements.namedItem('addresses');
  const manualDetails = form.querySelector('details');
  const submitButtons = [...form.querySelectorAll('button[type="submit"]')];
  let entries = [];
  let editing = null;
  let busy = false;
  const displayHost = authority => plain ? `http://${authority}` : authority;

  function showForm(entry) {
    editing = entry;
    hostField.value = entry?.host || '';
    hostField.readOnly = !!entry;
    platformField.value = entry?.platform || (plain ? 'other' : 'gitlab');
    addressesField.value = entry?.addresses.join('\n') || '';
    manualDetails.open = !!entry;
    form.hidden = false;
    add.hidden = true;
    (entry ? addressesField : hostField).focus();
  }

  function hideForm() {
    editing = null;
    form.hidden = true;
    add.hidden = false;
    add.focus();
  }

  function render() {
    list.innerHTML = entries.length ? entries.map((entry, index) => `
      <div class="platform-admin-row" data-git-host-entry>
        <div class="platform-admin-info platform-ssh-host-info" data-i18n-skip>
          <strong>${escapeHTML(displayHost(entry.host))}</strong>
          ${entry.addresses.map(address => `<code>${escapeHTML(address)}</code>`).join('')}
        </div>
        <div class="platform-admin-actions">
          <button type="button" class="button ghost small" data-https-host-edit="${index}">${lfT('更新授权', 'Update authorization')}</button>
          <button type="button" class="button ghost small danger" data-https-host-remove="${index}">${lfT('撤销授权', 'Revoke')}</button>
        </div>
      </div>`).join('') : `<p class="prop-hint">${lfT(`尚未授权 Git ${scheme} 主机。`, `No Git ${scheme} hosts are authorized.`)}</p>`;
    setPlatformTabCount(host, 'gitHostTrust', host.querySelectorAll('[data-git-host-entry]').length);
  }

  async function refresh() {
    const items = await api(endpoint, {silent:true});
    entries = Array.isArray(items) ? items : [];
    render();
  }

  async function change(action) {
    busy = true;
    submitButtons.forEach(button => { button.disabled = true; });
    try {
      await action();
    } catch (error) {
      toast(error.message, 'err');
      if (error.code === `${protocol}_host_trust_changed`) {
        hideForm();
        try {
          await refresh();
        } catch (refreshError) {
          toast(refreshError.message, 'err');
        }
      }
    } finally {
      submitButtons.forEach(button => { button.disabled = false; });
      busy = false;
    }
  }

  add.onclick = () => { if (!busy) showForm(null); };
  form.querySelector('[data-https-host-cancel]').onclick = () => { if (!busy) hideForm(); };
  form.onsubmit = event => {
    event.preventDefault();
    if (busy) return;
    const previous = editing;
    const input = { host:hostField.value.trim(), platform:platformField.value };
    if (event.submitter?.hasAttribute('data-https-host-manual')) {
      input.addresses = addressesField.value.split(/\s+/).filter(Boolean);
    }
    return change(async () => {
      const preview = await api(`${endpoint}/preview`, {method:'POST', body:JSON.stringify(input)});
      if (!card.isConnected) return;
      const review = [displayHost(preview.host), preview.addresses.join('\n')];
      if (previous) {
        const added = preview.addresses.filter(address => !previous.addresses.includes(address));
        const removed = previous.addresses.filter(address => !preview.addresses.includes(address));
        if (added.length) review.push([lfT('新增地址', 'Added addresses'), ...added].join('\n'));
        if (removed.length) review.push([lfT('移除地址', 'Removed addresses'), ...removed].join('\n'));
      }
      review.push(lfT('核对以上地址属于该 Git 实例。平台仅连接已确认的地址。', 'Verify that these addresses belong to the Git instance. The platform will connect only to approved addresses.'));
      const confirmed = await afConfirm({
        title:lfT(`确认 Git ${scheme} 主机授权`, `Confirm Git ${scheme} host authorization`),
        message:review.join('\n\n'),
        confirmText:lfT('确认授权', 'Authorize host'),
      });
      if (!confirmed || !card.isConnected) return;
      const target = previous ? `${endpoint}/${encodeURIComponent(previous.host)}` : endpoint;
      const headers = previous ? {'Content-Type':'application/json', 'If-Match':JSON.stringify(previous.revision)} : undefined;
      const entry = await api(target, {
        method:previous ? 'PUT' : 'POST', headers,
        body:JSON.stringify({host:preview.host, platform:preview.platform, addresses:preview.addresses}),
      });
      entries = [...entries.filter(item => item.host !== entry.host), entry].sort((a, b) => a.host.localeCompare(b.host));
      hideForm();
      render();
      toast(lfT(`已保存 ${scheme} 主机授权`, `${scheme} host authorization saved`), 'ok');
    });
  };
  list.onclick = event => {
    const button = event.target.closest('button');
    if (!button || busy) return;
    const {httpsHostEdit, httpsHostRemove} = button.dataset;
    if (httpsHostEdit !== undefined) return showForm(entries[Number(httpsHostEdit)]);
    if (httpsHostRemove === undefined) return;
    const entry = entries[Number(httpsHostRemove)];
    return change(async () => {
      if (!await afConfirm({
        title:lfT(`撤销 ${scheme} 主机授权`, `Revoke ${scheme} host authorization`),
        message:displayHost(entry.host),
        confirmText:lfT('撤销授权', 'Revoke'),
      }) || !card.isConnected) return;
      await api(`${endpoint}/${encodeURIComponent(entry.host)}`, {
        method:'DELETE',
        headers:{'If-Match':JSON.stringify(entry.revision)},
      });
      entries = entries.filter(item => item.host !== entry.host);
      render();
      toast(lfT(`已撤销 ${scheme} 主机授权`, `${scheme} host authorization revoked`), 'ok');
    });
  };
  try {
    await refresh();
  } catch (error) {
    toast(error.message, 'err');
  }
}
