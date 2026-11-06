document.getElementById('loginLang').onclick = () => window.lfSetLang(window.lfLang() === 'en' ? 'zh' : 'en');
document.getElementById('loginTheme').onclick = () => {
  const next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
  document.documentElement.dataset.theme = next;
  localStorage.setItem('agentflow.theme.v3', next);
};

const $ = id => document.getElementById(id);
const t = window.lfT || ((zh) => zh);
let mode = 'login', signup = false, joinable = [];

function renderProjects() {
  const host = $('projectOptions');
  host.replaceChildren(...joinable.map(p => {
    const row = document.createElement('label');
    row.className = 'login-project';
    const box = document.createElement('input');
    box.type = 'checkbox';
    box.name = 'projectIds';
    box.value = p.id;
    const mark = document.createElement('span');
    mark.className = 'login-project-mark';
    mark.textContent = String(p.key || p.name || p.id).slice(0, 2).toUpperCase();
    if (/^#[0-9a-f]{3,8}$/i.test(p.color || '')) mark.style.background = p.color;
    const text = document.createElement('span');
    text.className = 'login-project-text';
    const name = document.createElement('b');
    name.textContent = p.name || p.id;
    text.append(name);
    if (p.description) {
      const desc = document.createElement('small');
      desc.textContent = p.description;
      text.append(desc);
    }
    row.append(box, mark, text);
    return row;
  }));
}

function render() {
  const creating = mode !== 'login';
  $('loginSubtitle').textContent = mode === 'setup' ? t('创建管理员账号', 'Create the administrator account') : creating ? t('创建你的账号', 'Create your account') : t('登录你的交付工作台', 'Sign in to your delivery workspace');
  $('setupNotice').hidden = mode !== 'setup';
  $('confirmField').hidden = !creating;
  $('passwordRule').hidden = !creating;
  $('projectField').hidden = mode !== 'register' || !joinable.length;
  $('loginForm').elements.confirm.required = creating;
  $('loginForm').elements.password.autocomplete = creating ? 'new-password' : 'current-password';
  $('loginSubmit').textContent = mode === 'setup' ? t('创建并进入', 'Create and continue') : creating ? t('注册并进入', 'Sign up') : t('登录', 'Sign in');
  $('loginSwitch').hidden = mode === 'setup' || !signup;
  $('switchText').textContent = creating ? t('已有账号？', 'Already have an account?') : t('还没有账号？', 'No account yet?');
  $('switchMode').textContent = creating ? t('登录', 'Sign in') : t('注册', 'Sign up');
  $('loginContact').hidden = mode === 'setup' || signup;
  $('loginError').textContent = '';
}

$('switchMode').onclick = () => { mode = mode === 'login' ? 'register' : 'login'; render(); };

fetch('/api/auth/setup').then(r => r.ok ? r.json() : {}).then(state => {
  signup = !!state.signupEnabled;
  joinable = Array.isArray(state.joinableProjects) ? state.joinableProjects.filter(p => p && p.id) : [];
  renderProjects();
  if (state.needsSetup) mode = 'setup';
  else if (location.hash === '#register' && signup) mode = 'register';
  render();
}).catch(() => {});

$('loginForm').onsubmit = async event => {
  event.preventDefault();
  const form = event.currentTarget, button = $('loginSubmit'), error = $('loginError');
  const creating = mode !== 'login';
  if (creating && form.elements.password.value !== form.elements.confirm.value) {
    error.textContent = t('两次输入的密码不一致', 'Passwords do not match');
    return;
  }
  button.disabled = true;
  error.textContent = '';
  const body = {login: form.elements.login.value.trim(), password: form.elements.password.value};
  if (mode === 'register') body.projectIds = [...form.querySelectorAll('input[name="projectIds"]:checked')].map(box => box.value);
  try {
    const response = await fetch(creating ? '/api/auth/register' : '/api/auth/password', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(body),
    });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error || t('操作失败', 'Request failed'));
    if (body.projectIds?.length) localStorage.setItem('agentflow.activeProject', body.projectIds[0]);
    location.replace('/');
  } catch (err) { error.textContent = err.message; button.disabled = false; }
};
