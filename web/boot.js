// Apply the language before paint; boot-theme.js owns the theme.
// i18n.js replaces this. Boot must still render if that script fails to load.
window.lfT = window.lfT || ((zh, en, ko) => zh);
(() => {
  const root = document.documentElement;
  let lang = 'zh';
  try {
    const saved = localStorage.getItem('loopforge.lang');
    if (saved === 'en' || saved === 'ko') lang = saved;
  } catch (_) { /* Restricted storage still permits the default interface. */ }
  root.dataset.lang = lang;
  root.lang = lang === 'en' ? 'en' : (lang === 'ko' ? 'ko-KR' : 'zh-CN');
  if (lang !== 'zh') root.classList.add('notranslate');
})();
