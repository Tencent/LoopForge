document.getElementById('loginLang').onclick = () => window.lfSetLang(window.lfLang() === 'en' ? 'zh' : 'en');
document.getElementById('loginTheme').onclick = () => {
  const next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
  document.documentElement.dataset.theme = next;
  localStorage.setItem('agentflow.theme.v3', next);
};

const $ = id => document.getElementById(id);
const t = window.lfT || ((zh) => zh);
let mode = 'login', signup = false;
const query = new URLSearchParams(location.search);
const oidcErrors = new Map([
  ['invalid', t('企业登录未完成或已过期，请重新登录。', 'Enterprise sign-in did not complete or has expired. Try again.')],
  ['unlinked', t('此企业账号尚未关联，请联系管理员。', 'This enterprise identity is not linked. Contact your administrator.')],
  ['unavailable', t('企业登录暂时不可用，请稍后重试。', 'Enterprise sign-in is temporarily unavailable. Try again later.')],
]);
const oidcError = oidcErrors.get(query.get('oidc_error'));
if (oidcError) {
  $('oidcError').textContent = oidcError;
  $('oidcError').hidden = false;
}

function render() {
  const creating = mode !== 'login';
  $('loginSubtitle').textContent = mode === 'setup' ? t('创建管理员账号', 'Create the administrator account') : creating ? t('创建你的账号', 'Create your account') : t('登录你的交付工作台', 'Sign in to your delivery workspace');
  $('setupNotice').hidden = mode !== 'setup';
  $('confirmField').hidden = !creating;
  $('passwordRule').hidden = !creating;
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

const setupURL = query.has('return_url')
  ? '/api/auth/setup?return_url=' + encodeURIComponent(query.get('return_url'))
  : '/api/auth/setup';
fetch(setupURL).then(r => r.ok ? r.json() : {}).then(state => {
  signup = !!state.signupEnabled;
  if (state.oidcLoginUrl) {
    $('oidcLogin').href = state.oidcLoginUrl;
    $('oidcLogin').textContent = t('企业登录', 'Enterprise sign-in');
    $('oidcLogin').hidden = false;
  }
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
  try {
    const response = await fetch(creating ? '/api/auth/register' : '/api/auth/password', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({login: form.elements.login.value.trim(), password: form.elements.password.value}),
    });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error || t('操作失败', 'Request failed'));
    location.replace('/');
  } catch (err) { error.textContent = err.message; button.disabled = false; }
};
