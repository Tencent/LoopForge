const renderSOPCatalogBase=renderSOPCatalog;
renderSOPCatalog=function(){renderSOPCatalogBase();const success=document.querySelector('.overview-metric.success span');if(success)success.textContent='成功完成的运行';const successSmall=document.querySelector('.overview-metric.success small');if(successSmall)successSmall.textContent='累计成功次数'};
(() => {
  if (window.loopforgeBootstrap) return;
  const bootPromise = Promise.resolve().then(boot).catch(error => {
    finishBootLoader();
    document.body.innerHTML = `<pre style="padding:30px">启动失败：${escapeHTML(error.message)}</pre>`;
    return false;
  });
  const root = document.getElementById('chatRoot');
  const loading = document.getElementById('chatWorkbenchLoading');
  const failure = document.getElementById('chatWorkbenchFailure');
  let mountWorkbench;
  window.loopforgeBootstrap = Object.freeze({ ready: bootPromise, registerWorkbench(mount) { mountWorkbench = mount; } });
  if (!root || !loading || !failure) return;
  if (window.lfLang?.() === 'en') {
    loading.textContent = 'Loading workbench…';
    failure.querySelector('[role=alert]').textContent = 'Workbench could not load';
    failure.querySelector('button').textContent = 'Reload workbench';
  }
  failure.querySelector('button').onclick = () => location.reload();
  const entry = document.querySelector('link[data-workbench-entry]')?.getAttribute('href');
  const styles = document.querySelector('link[data-workbench-style]')?.getAttribute('href');
  let stylesheet;
  const loadStylesheet = () => new Promise((resolve, reject) => {
    const link = document.createElement('link');
    stylesheet = link;
    link.rel = 'stylesheet';
    link.href = styles;
    link.onload = resolve;
    link.onerror = reject;
    document.head.append(link);
  });
  let timeout;
  const assets = entry && styles ? Promise.race([
    Promise.all([import(entry), loadStylesheet()]),
    new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error('Workbench assets timed out')), 8000); }),
  ]).catch(error => { stylesheet?.remove(); throw error; }).finally(() => clearTimeout(timeout)) : Promise.reject(new Error('Workbench bundle not built'));
  Promise.all([bootPromise, assets]).then(([started]) => {
    if (started) mountWorkbench(root, () => { loading.hidden = true; });
  }).catch(() => {
    loading.hidden = true;
    failure.hidden = false;
  });
})();
