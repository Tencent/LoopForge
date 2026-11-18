const langOrder = ['zh', 'en', 'ko'];
const langBtn = document.getElementById('loginLang');
const curLang = window.lfLang();
if (langBtn) {
  langBtn.textContent = curLang === 'ko' ? '한국어' : (curLang === 'en' ? 'English' : '中 / EN / KR');
  langBtn.onclick = () => {
    const cur = window.lfLang();
    const nextIdx = (langOrder.indexOf(cur) + 1) % langOrder.length;
    window.lfSetLang(langOrder[nextIdx]);
  };
}

document.getElementById('loginTheme').onclick = () => {
  const next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
  document.documentElement.dataset.theme = next;
  localStorage.setItem('agentflow.theme.v3', next);
};

const $ = id => document.getElementById(id);
const t = window.lfT || ((zh) => zh);
let mode = 'login', signup = false;

function render() {
  const creating = mode !== 'login';
  $('loginSubtitle').textContent = mode === 'setup' ? t('创建管理员账号', 'Create the administrator account', '관리자 계정 생성') : creating ? t('创建你的账号', 'Create your account', '계정 만들기') : t('登录你的交付工作台', 'Sign in to your delivery workspace', 'LoopForge 워크스페이스 로그인');
  $('setupNotice').hidden = mode !== 'setup';
  $('confirmField').hidden = !creating;
  $('passwordRule').hidden = !creating;
  $('loginForm').elements.confirm.required = creating;
  $('loginForm').elements.password.autocomplete = creating ? 'new-password' : 'current-password';
  $('loginSubmit').textContent = mode === 'setup' ? t('创建并进入', 'Create and continue', '생성 후 시작') : creating ? t('注册并进入', 'Sign up', '회원가입') : t('登录', 'Sign in', '로그인');
  $('loginSwitch').hidden = mode === 'setup' || !signup;
  $('switchText').textContent = creating ? t('已有账号？', 'Already have an account?', '이미 계정이 있으신가요?') : t('还没有账号？', 'No account yet?', '아직 계정이 없으신가요?');
  $('switchMode').textContent = creating ? t('登录', 'Sign in', '로그인') : t('注册', 'Sign up', '회원가입');
  $('loginContact').hidden = mode === 'setup' || signup;
  $('loginError').textContent = '';
}

$('switchMode').onclick = () => { mode = mode === 'login' ? 'register' : 'login'; render(); };

fetch('/api/auth/setup').then(r => r.ok ? r.json() : {}).then(state => {
  signup = !!state.signupEnabled;
  if (state.needsSetup) mode = 'setup';
  else if (location.hash === '#register' && signup) mode = 'register';
  render();
}).catch(() => {});

$('loginForm').onsubmit = async event => {
  event.preventDefault();
  const form = event.currentTarget, button = $('loginSubmit'), error = $('loginError');
  const creating = mode !== 'login';
  if (creating && form.elements.password.value !== form.elements.confirm.value) {
    error.textContent = t('两次输入的密码不一致', 'Passwords do not match', '비밀번호가 일치하지 않습니다');
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
    if (!response.ok) throw new Error(payload.error || t('操作失败', 'Request failed', '작업 실패'));
    location.replace('/');
  } catch (err) { error.textContent = err.message; button.disabled = false; }
};
