// Apply the language before paint; boot-theme.js owns the theme.
// i18n.js replaces lfT. Boot must still render if that script fails to load.
// Visible UI is Vietnamese by default, or English. A leftover Chinese
// localStorage value is migrated so a hard refresh no longer stays on 中文.
window.lfT = window.lfT || ((zh) => zh);
(() => {
  const root = document.documentElement;
  let lang = 'vi';
  try {
    const saved = localStorage.getItem('loopforge.lang');
    if (saved === 'en') lang = 'en';
    else {
      lang = 'vi';
      if (saved && saved !== 'vi') {
        try { localStorage.setItem('loopforge.lang', 'vi'); } catch (_) { /* ignore */ }
      }
    }
  } catch (_) { /* Restricted storage still permits the default interface. */ }
  root.dataset.lang = lang;
  root.lang = lang;
  document.write('<script src="/i18n-' + lang + '.js?v=vi9"><\/script>');
})();
