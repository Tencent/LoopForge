const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const code = fs.readFileSync(require('node:path').join(__dirname, 'app/runs.js'), 'utf8');
function extract(name) {
  const start = code.indexOf(`function ${name}(`);
  assert.ok(start >= 0, name);
  const end = code.indexOf('\n}', start) + 2;
  return (code.slice(start - 6, start) === 'async ' ? 'async ' : '') + code.slice(start, end);
}

async function testDownload({status = 200, type = 'application/zip', message = ''} = {}) {
  const calls = [], notifications = [], timers = [];
  const button = {disabled: false, textContent: '下载静态文件'};
  const anchor = {click() { calls.push(['click', this.href, this.download]); }, remove() { calls.push(['remove']); }};
  let release;
  const waiting = new Promise(resolve => { release = resolve; });
  const context = {
    fetch: async path => {
      calls.push(['fetch', path]);
      await waiting;
      return {
        ok: status === 200, status,
        headers: {get: () => type},
        json: async () => ({error: message}),
        blob: async () => 'zip-content'
      };
    },
    document: {createElement: () => anchor, body: {appendChild() {}}},
    URL: {
      createObjectURL(blob) { assert.equal(blob, 'zip-content'); return 'blob:download'; },
      revokeObjectURL(url) { calls.push(['revoke', url]); }
    },
    toast: (...args) => notifications.push(args),
    setTimeout: callback => timers.push(callback)
  };
  vm.createContext(context);
  vm.runInContext(extract('downloadRunPreview'), context);
  const task = context.downloadRunPreview('run?test', button);
  assert.equal(button.disabled, true);
  assert.equal(button.textContent, '正在打包…');
  await context.downloadRunPreview('run?test', button);
  assert.equal(calls.length, 1, 'repeated click must not start another request');
  release();
  await task;
  assert.equal(button.disabled, false);
  assert.equal(button.textContent, '下载静态文件');
  assert.deepEqual(calls[0], ['fetch', '/api/runs/run%3Ftest/preview/download']);
  timers.forEach(callback => callback());
  return {calls, notifications};
}

(async () => {
  const context = {
    state: {runPreview: {runId: 'run-1', info: {status: 'ready', fileCount: 3}, session: {url: 'https://preview.test/'}}},
    runPreviewDeclared: () => true,
    escapeHTML: value => String(value),
    window: {location: {protocol: 'http:'}}
  };
  vm.createContext(context);
  vm.runInContext(extract('previewPaneHTML'), context);
  const run = {id: 'run-1'};
  assert.match(context.previewPaneHTML(run), /data-preview-download/);
  assert.match(context.previewPaneHTML(run), /下载静态文件/);
  for (const status of ['pending', 'archiving', 'failed', 'expired', 'deleted']) {
    context.state.runPreview.info.status = status;
    assert.doesNotMatch(context.previewPaneHTML(run), /data-preview-download/, status);
  }
  let result = await testDownload();
  assert.deepEqual(result.calls.slice(1), [
    ['click', 'blob:download', 'preview-run?test.zip'], ['remove'], ['revoke', 'blob:download']
  ]);
  assert.equal(result.notifications[0][1], 'ok');
  for (const status of [401, 404, 409, 410, 500]) {
    result = await testDownload({status, message: '下载不可用'});
    assert.equal(result.calls.length, 1, 'failed requests must not download an error body');
    assert.deepEqual(result.notifications, [['下载不可用', 'err']]);
  }
  result = await testDownload({type: 'text/html'});
  assert.equal(result.calls.length, 1, 'a redirected login page is not a ZIP');
  assert.equal(result.notifications[0][1], 'err');
})().catch(error => { console.error(error); process.exit(1); });
