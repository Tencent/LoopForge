// Apply preferences before paint, using the same keys as the runtime controls.
(() => {
  const root = document.documentElement;
  let theme = 'light', lang = 'zh';
  try {
    theme = localStorage.getItem('loopforge.theme.v3') === 'dark' ? 'dark' : 'light';
    lang = localStorage.getItem('loopforge.lang') === 'en' ? 'en' : 'zh';
  } catch (_) { /* Restricted storage still permits the default interface. */ }
  root.dataset.theme = theme;
  root.dataset.lang = lang;
  root.lang = lang === 'en' ? 'en' : 'zh-CN';
})();
