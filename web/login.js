document.getElementById('loginTheme').onclick = () => {
  const next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
  document.documentElement.dataset.theme = next;
  localStorage.setItem('agentflow.theme.v3', next);
};

const $ = id => document.getElementById(id);
const t = window.lfT || ((zh) => zh);
let mode = 'login', signup = false, githubLogin = false;

const githubErrors = {
  unavailable: () => t('GitHub 登录未启用，请联系部署管理员', 'GitHub sign-in is not enabled. Ask your deployment administrator.'),
  denied: () => t('已取消 GitHub 授权', 'GitHub authorization was cancelled'),
  expired: () => t('登录已失效，请重新点击「使用 GitHub 登录」', 'The sign-in expired. Choose Continue with GitHub again.'),
  setup: () => t('请先创建管理员账号，再使用 GitHub 登录', 'Create the administrator account before signing in with GitHub'),
  signup_closed: () => t('该 GitHub 账号尚未注册，且自助注册已关闭，请联系管理员', 'This GitHub account has no LoopForge account and sign-up is closed. Ask your administrator.'),
  failed: () => t('GitHub 登录失败，请稍后重试', 'GitHub sign-in failed. Try again later.'),
};
const githubError = new URLSearchParams(location.search).get('github_error');
if (githubError) history.replaceState(null, '', location.pathname + location.hash);

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
  $('githubLogin').hidden = mode === 'setup' || !githubLogin;
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
    githubLogin = !!state.githubLogin;
    if (state.needsSetup) mode = 'setup';
    else if (location.hash === '#register' && signup) mode = 'register';
    render();
  })
  .then(() => {
    if (githubError) $('loginError').textContent = (githubErrors[githubError] || githubErrors.failed)();
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
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error || t('操作失败', 'Request failed'));
    location.replace('/');
  } catch (err) { error.textContent = err.message; button.disabled = false; }
};
