(() => {
  const host = $('#personalSettingsContent');
  async function load() {
    host.setAttribute('aria-busy', 'true');
    try {
      const auth = await api('/api/auth/current', {silent: true});
      if (!auth.user || auth.user.isGuest) {
        host.textContent = '请登录后配置 Git 和执行器。';
        return;
      }
      const users = await api('/api/users', {silent: true});
      state.currentUser = auth.user;
      state.currentView = 'settings';
      state.data = {users};
      await renderPersonalSettings();
    } catch {
      const message = document.createElement('p');
      message.setAttribute('role', 'alert');
      message.textContent = '无法读取个人配置，请重试。';
      const retry = document.createElement('button');
      retry.type = 'button';
      retry.className = 'credential-btn secondary';
      retry.textContent = '重试';
      retry.onclick = () => {
        retry.disabled = true;
        void load();
      };
      host.replaceChildren(message, retry);
    } finally {
      host.removeAttribute('aria-busy');
    }
  }
  window.addEventListener('keydown', event => {
    if (event.key !== 'Escape' || event.defaultPrevented ||
        document.querySelector('dialog[open], #confirmModal.open, #credentialWizard.is-open, .hui-select-panel.is-open')) return;
    window.parent.postMessage({type: 'agentflow-credentials-close'}, location.origin);
  });
  listenForGongfengOAuth();
  void load();
})();
