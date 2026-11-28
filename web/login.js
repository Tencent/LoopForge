document.getElementById('loginTheme').onclick = () => {
  const next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
  document.documentElement.dataset.theme = next;
  localStorage.setItem('agentflow.theme.v3', next);
};

const $ = id => document.getElementById(id);
const t = window.lfT || ((zh) => zh);
let mode = 'login', signup = false;

// The password endpoints answer a wrong password and the rate limit in English
// only; show them in the page language like every other message here.
function authErrorMessage(status, payload, creating) {
  if (status === 429) return t('尝试次数过多，请一分钟后再试', 'Too many attempts. Try again in a minute.');
  if (!creating && status === 401) return t('用户名或密码错误', 'Invalid username or password');
  return payload?.error || t('操作失败', 'Request failed');
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

fetch('/api/auth/setup')
  .then(response => response.ok ? response.json() : null)
  .catch(() => null)
  .then(state => {
    if (!state) {
      $('loginContact').hidden = false;
      return;
    }
    signup = !!state.signupEnabled;
    if (state.needsSetup) mode = 'setup';
    else if (location.hash === '#register' && signup) mode = 'register';
    render();
  });

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
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(authErrorMessage(response.status, payload, creating));
    location.replace('/');
  } catch (err) { error.textContent = err.message; button.disabled = false; }
};
