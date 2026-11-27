const platformPublicURLEndpoint = '/api/platform/public-url';

// 平台管理 → 外部访问地址。这个地址会被平台写进发到站外的链接里：IM 的账号
// 绑定链接、写操作确认链接，以及通知里的运行详情链接。没有它，Bot 只能回复
// 「生成不了绑定链接」。
//
// 「填入当前访问地址」取的是浏览器自己的 location.origin，不读也不信任服务端
// 的 Host 头：这个值会被拼进带一次性 token 的绑定链接，可被伪造就是钓鱼面。
function platformPublicURLHTML() {
  return `
    <section class="platform-admin-main-card" id="platformPublicURLCard">
      <div class="platform-admin-head">
        <div class="platform-admin-head-meta">
          <h2>${lfT('平台外部访问地址', 'Platform public address')}</h2>
          <p>${lfT(
            '用户打开平台用的地址。平台会把它写进发到站外的链接：IM 账号绑定链接、写操作确认链接，以及通知里的运行详情链接。',
            'The address users open this platform on. It goes into the links the platform sends elsewhere: chat account binding, write confirmation, and the run detail link in notifications.',
          )}</p>
        </div>
      </div>
      <div class="platform-admin-row" data-public-url-current>
        <div class="platform-admin-info" data-i18n-skip></div>
      </div>
      <form class="platform-shared-key-form" autocomplete="off">
        <label class="platform-field">
          <span>${lfT('访问地址', 'Public address')}</span>
          <input name="value" type="url" placeholder="https://loopforge.example.com" maxlength="512" spellcheck="false" autocapitalize="off" required>
          <small class="platform-field-hint">${lfT(
            '只填到主机和端口，例如 https://loopforge.example.com 或 http://203.0.113.10:8090。必须是用户手机上的 IM 能打开的地址。',
            'Host and port only, e.g. https://loopforge.example.com or http://203.0.113.10:8090. It must be reachable from the phone that opens the chat link.',
          )}</small>
        </label>
        <div class="platform-shared-key-actions">
          <button type="button" class="button ghost" data-public-url-fill>${lfT('填入当前访问地址', 'Use the address I opened')}</button>
          <button type="button" class="button ghost danger" data-public-url-clear hidden>${lfT('清除', 'Clear')}</button>
          <button type="submit" class="button primary">${lfT('保存', 'Save')}</button>
        </div>
      </form>
    </section>`;
}

async function loadPlatformPublicURL(host) {
  const card = host.querySelector('#platformPublicURLCard');
  if (!card) return;
  const form = card.querySelector('form');
  const field = form.elements.namedItem('value');
  const current = card.querySelector('[data-public-url-current] .platform-admin-info');
  const fill = card.querySelector('[data-public-url-fill]');
  const clear = card.querySelector('[data-public-url-clear]');
  const save = form.querySelector('button[type="submit"]');
  let busy = false;

  function sourceBadge(view) {
    if (view.source === 'settings') return `<span class="platform-source-badge source-default">${lfT('平台设置', 'Platform setting')}</span>`;
    if (view.source === 'env') return `<span class="platform-source-badge source-env">${lfT('环境变量', 'Environment')}</span>`;
    return `<span class="platform-source-badge source-builtin">${lfT('未配置', 'Not configured')}</span>`;
  }

  function render(view) {
    const lines = [`<strong>${view.value ? escapeHTML(view.value) : lfT('未配置，IM 绑定链接和确认链接都生成不了', 'Not configured: chat binding and confirmation links cannot be built')}</strong>`, sourceBadge(view)];
    // 页面设置优先于环境变量，所以被盖掉的那个要讲清楚，不然运维会以为改了没生效。
    if (view.source === 'settings' && view.envValue && view.envValue !== view.value) {
      lines.push(`<small>${lfT('环境变量 AGENTFLOW_PUBLIC_URL 当前是', 'AGENTFLOW_PUBLIC_URL is currently')} <code>${escapeHTML(view.envValue)}</code>${lfT('，已被这里的设置覆盖。', ', overridden by this setting.')}</small>`);
    }
    if (view.source === 'env') {
      lines.push(`<small>${lfT('来自部署时的环境变量；在这里保存会覆盖它。', 'From the deployment environment; saving here overrides it.')}</small>`);
    }
    if (view.warning) lines.push(`<small class="bot-record-error">${escapeHTML(view.warning)}</small>`);
    if (view.updatedBy) lines.push(`<small>${lfT('最后修改', 'Last changed by')}：${escapeHTML(view.updatedBy)}${view.updatedAt ? ` · ${escapeHTML(new Date(view.updatedAt).toLocaleString())}` : ''}</small>`);
    current.innerHTML = lines.join('');
    field.value = view.stored || '';
    field.placeholder = view.envValue || 'https://loopforge.example.com';
    clear.hidden = !view.stored;
  }

  async function change(action) {
    if (busy) return;
    busy = true;
    [save, clear, fill].forEach(button => { button.disabled = true; });
    try {
      render(await action());
    } catch (error) {
      toast(error.message, 'err');
    } finally {
      [save, clear, fill].forEach(button => { button.disabled = false; });
      busy = false;
    }
  }

  fill.onclick = () => { field.value = location.origin; field.focus(); };
  form.onsubmit = event => {
    event.preventDefault();
    change(async () => {
      const view = await api(platformPublicURLEndpoint, {method: 'PUT', body: JSON.stringify({value: field.value.trim()})});
      toast(view.warning || lfT('外部访问地址已保存', 'Public address saved'), view.warning ? 'err' : 'ok');
      return view;
    });
  };
  clear.onclick = async () => {
    if (busy || !await afConfirm({
      title: lfT('清除外部访问地址', 'Clear public address'),
      message: lfT(
        '清除后将使用部署环境变量中的地址。如果环境变量未配置，平台将无法生成 IM 绑定和确认链接。',
        'Clearing the setting uses the deployment environment address. If it is unset, the platform cannot generate chat binding or confirmation links.',
      ),
      confirmText: lfT('清除', 'Clear'),
      danger: true,
    }) || !card.isConnected || clear.hidden) return;
    return change(async () => {
      const view = await api(platformPublicURLEndpoint, {method: 'DELETE'});
      toast(view.source === 'env'
        ? lfT('已清除，改回使用环境变量', 'Cleared. The environment address now applies')
        : lfT('已清除，外部访问地址未配置', 'Cleared. No public address is configured'), 'ok');
      return view;
    });
  };

  try {
    render(await api(platformPublicURLEndpoint, {silent: true}));
  } catch (error) {
    current.innerHTML = `<small class="bot-record-error">${escapeHTML(error.message)}</small>`;
  }
}
