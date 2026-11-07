// Apply the language before paint; boot-theme.js owns the theme.
// i18n.js replaces this. Boot must still render if that script fails to load.
window.lfT = window.lfT || ((zh) => zh);
(() => {
  const root = document.documentElement;
  let lang = 'zh';
  try {
    lang = localStorage.getItem('loopforge.lang') === 'en' ? 'en' : 'zh';
  } catch (_) { /* Restricted storage still permits the default interface. */ }
  root.dataset.lang = lang;
  root.lang = lang === 'en' ? 'en' : 'zh-CN';
})();
