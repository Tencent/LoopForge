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

async function testSession(studioProtocol, sessionURL) {
  const run = {id: 'preview-run', workflowSnapshot: {preview: {enabled: true}}};
  const calls = [], probes = [], copied = [], opened = [], notifications = [];
  const renewedURL = sessionURL.replace('/token/', '/renewed/');
  const clicks = {};
  let frame = null;
  const pane = {
    closest: () => ({dataset: {runId: run.id}}),
    querySelector: selector => selector.includes('iframe') ? frame : {
      addEventListener: (event, callback) => { clicks[selector] = callback; }
    },
    set innerHTML(html) {
      const src = html.match(/<iframe[^>]+src="([^"]+)"/);
      frame = src ? {
        getAttribute: name => name === 'src' ? src[1] : run.id,
        replaceWith: existing => { frame = existing; }
      } : null;
    }
  };
  const context = {
    state: {activeRun: run},
    window: {location: {protocol: studioProtocol, href: studioProtocol + '//studio.example/'}, open: (...args) => opened.push(args)},
    $: () => ({querySelector: () => pane}),
    escapeHTML: value => String(value),
    readPreviewBackend: () => '', persistPreviewBackend() {}, schedulePreviewPoll() {},
    copyText: value => copied.push(value),
    afPrompt: async () => 'https://backend.example',
    toast: (...args) => notifications.push(args),
    // A real studio page only allows connect-src 'self'; probes must not occur.
    fetch: async url => { probes.push(url); throw new Error('cross-origin connection denied'); },
    api: async (path, options) => {
      calls.push([path, options]);
      if (path.endsWith('/session')) return {previewId: 'p', url: sessionURL, backendUrl: ''};
      if (path.endsWith('/backend')) return {
        preview: {id: 'p', status: 'ready', backendUrl: 'https://backend.example'},
        session: {previewId: 'p', url: renewedURL, backendUrl: 'https://backend.example'}
      };
      return {id: 'p', status: 'ready', fileCount: 2, backendUrl: ''};
    }
  };
  vm.createContext(context);
  for (const name of ['runPreviewDeclared', 'previewPaneHTML', 'loadRunPreview', 'renderPreviewPane', 'bindPreviewPane', 'editPreviewBackend']) {
    vm.runInContext(extract(name), context);
  }
  await context.loadRunPreview(run.id);
  assert.equal(context.state.runPreview.error, '');
  assert.deepEqual(probes, []);
  assert.equal(context.state.runPreview.session.url, sessionURL);
  assert.equal(frame.getAttribute('src'), sessionURL);
  assert.deepEqual(calls.map(([path]) => path), ['/api/runs/preview-run/preview', '/api/runs/preview-run/preview/session']);
  assert.equal(calls[1][1].method, 'POST');
  clicks['[data-preview-copy]']();
  clicks['[data-preview-open]']();
  assert.deepEqual(copied, [sessionURL]);
  assert.deepEqual(opened, [[sessionURL, '_blank', 'noopener']]);

  // Refreshing metadata should preserve the mounted iframe and its state.
  const mounted = frame;
  await context.loadRunPreview(run.id, {silent: true});
  assert.equal(frame, mounted);
  assert.equal(calls.length, 3, 'the existing session should be reused');
  assert.deepEqual(probes, []);

  await context.editPreviewBackend(run);
  assert.equal(context.state.runPreview.session.url, renewedURL);
  assert.equal(frame.getAttribute('src'), renewedURL);
  assert.notEqual(frame, mounted, 'a renewed session must load a new frame');
  assert.equal(calls[3][0], '/api/runs/preview-run/preview/backend');
  assert.equal(calls[3][1].method, 'PUT');
  assert.deepEqual(JSON.parse(calls[3][1].body), {backendUrl: 'https://backend.example'});
  assert.deepEqual(probes, []);
  assert.equal(notifications[0][1], 'ok');
  clicks['[data-preview-copy]']();
  clicks['[data-preview-open]']();
  assert.equal(copied[1], renewedURL);
  assert.deepEqual(opened[1], [renewedURL, '_blank', 'noopener']);
}

// A failed session (isolation not configured: 409; a backend stored before the
// upgrade on the studio host: 400) must keep the ready archive. Download and
// backend repair stay available and the pane explains the specific problem.
async function testSessionFailure({code, status, message, hint, storedBackend, repair}) {
  const run = {id: 'preview-run', workflowSnapshot: {preview: {enabled: true}}};
  const calls = [], notifications = [], persisted = [], downloads = [];
  const clicks = {};
  let html = '';
  const pane = {
    closest: () => ({dataset: {runId: run.id}}),
    // Only buttons that were rendered can be bound and clicked.
    querySelector: selector => {
      if (selector.includes('iframe')) return null;
      return html.includes(selector.slice(1, -1)) ? {addEventListener: (event, callback) => { clicks[selector] = callback; }} : null;
    },
    set innerHTML(value) { html = value; for (const key of Object.keys(clicks)) delete clicks[key]; }
  };
  const repairedURL = 'https://preview.example/preview/repaired/';
  const context = {
    state: {activeRun: run},
    window: {open() {}},
    $: () => ({querySelector: () => pane}),
    escapeHTML: value => String(value),
    readPreviewBackend: () => '', persistPreviewBackend: (...args) => persisted.push(args), schedulePreviewPoll() {}, stopPreviewPoll() {},
    copyText() {}, downloadRunPreview: (...args) => downloads.push(args),
    afPrompt: async () => '',
    toast: (...args) => notifications.push(args),
    api: async (path, options) => {
      calls.push([path, options]);
      if (path.endsWith('/session')) throw Object.assign(new Error(message), {code});
      if (path.endsWith('/backend')) return repair === 'session'
        ? {preview: {id: 'p', status: 'ready', fileCount: 2, backendUrl: ''}, session: {previewId: 'p', url: repairedURL, backendUrl: ''}}
        : {preview: {id: 'p', status: 'ready', fileCount: 2, backendUrl: ''}, sessionError: {error: message, code}};
      return {id: 'p', status: 'ready', fileCount: 2, backendUrl: storedBackend};
    }
  };
  vm.createContext(context);
  for (const name of ['runPreviewDeclared', 'previewPaneHTML', 'loadRunPreview', 'renderPreviewPane', 'bindPreviewPane', 'editPreviewBackend']) {
    vm.runInContext(extract(name), context);
  }
  const label = `${status} ${code}`;
  const assertArchiveActions = () => {
    assert.equal(context.state.runPreview.info?.status, 'ready', `${label}: archive info dropped`);
    assert.equal(context.state.runPreview.error, '', `${label}: whole pane failed`);
    assert.equal(context.state.runPreview.session, null);
    for (const action of ['[data-preview-download]', '[data-preview-backend-edit]', '[data-preview-refresh]']) {
      assert.ok(clicks[action], `${label}: ${action} missing`);
    }
    for (const action of ['[data-preview-copy]', '[data-preview-open]']) {
      assert.equal(clicks[action], undefined, `${label}: ${action} offered without a session`);
    }
    assert.doesNotMatch(html, /<iframe/);
    assert.doesNotMatch(html, /预览加载失败/);
    assert.match(html, new RegExp(`data-preview-session-error="${code}"`));
    assert.ok(html.includes(hint), `${label}: hint missing`);
  };

  await context.loadRunPreview(run.id);
  assert.deepEqual(calls.map(([path, options]) => [path, options?.method]), [
    ['/api/runs/preview-run/preview', undefined], ['/api/runs/preview-run/preview/session', 'POST']]);
  assert.deepEqual(JSON.parse(calls[1][1].body), {backendUrl: storedBackend});
  assertArchiveActions();
  assert.ok(html.includes(message), `${label}: server detail missing`);
  assert.equal(context.state.runPreview.sessionErrorCode, code);
  assert.deepEqual(persisted, [], `${label}: a rejected backend must not be remembered`);
  if (storedBackend) assert.ok(html.includes(storedBackend), `${label}: stored backend hidden`);
  clicks['[data-preview-download]']({currentTarget: 'button'});
  assert.deepEqual(downloads, [[run.id, 'button']]);

  // A background refresh retries the session and still keeps the archive.
  await context.loadRunPreview(run.id, {silent: true});
  assert.equal(calls.length, 4);
  assert.equal(calls[3][0], '/api/runs/preview-run/preview/session');
  assertArchiveActions();

  // Repair from the pane: clear the backend.
  await context.editPreviewBackend(run);
  const put = calls[calls.length - 1];
  assert.equal(put[0], '/api/runs/preview-run/preview/backend');
  assert.equal(put[1].method, 'PUT');
  assert.deepEqual(JSON.parse(put[1].body), {backendUrl: ''});
  assert.deepEqual(notifications[notifications.length - 1], ['已清除预览后端', 'ok']);
  if (repair === 'session') {
    assert.equal(context.state.runPreview.session.url, repairedURL);
    assert.equal(context.state.runPreview.sessionError, '');
    assert.match(html, new RegExp(`<iframe[^>]+src="${repairedURL}"`));
    assert.ok(clicks['[data-preview-open]'] && clicks['[data-preview-copy]']);
  } else {
    // The backend was saved but no session followed: explain instead of spinning.
    assertArchiveActions();
    assert.doesNotMatch(html, /正在签发预览会话/);
  }
}

// Metadata failures still use the whole-pane error with a retry button.
async function testMetadataFailure() {
  const run = {id: 'preview-run', workflowSnapshot: {preview: {enabled: true}}};
  let html = '';
  const pane = {
    closest: () => ({dataset: {runId: run.id}}),
    querySelector: () => null,
    set innerHTML(value) { html = value; }
  };
  const context = {
    state: {activeRun: run}, $: () => ({querySelector: () => pane}), escapeHTML: value => String(value),
    readPreviewBackend: () => '', persistPreviewBackend() {}, schedulePreviewPoll() {}, stopPreviewPoll() {},
    api: async () => { throw new Error('preview metadata unavailable'); }
  };
  vm.createContext(context);
  for (const name of ['runPreviewDeclared', 'previewPaneHTML', 'loadRunPreview', 'renderPreviewPane', 'bindPreviewPane']) {
    vm.runInContext(extract(name), context);
  }
  await context.loadRunPreview(run.id);
  assert.equal(context.state.runPreview.info, null);
  assert.match(html, /预览加载失败/);
  assert.match(html, /data-preview-refresh/);
}

(async () => {
  for (const studio of ['http:', 'https:']) {
    for (const preview of ['http://preview.example:8090/preview/token/', 'https://preview.example:8443/preview/token/']) {
      await testSession(studio, preview);
    }
  }
  await testSessionFailure({
    code: 'preview_isolation_required', status: 409, storedBackend: '', repair: 'sessionError',
    message: 'preview requires AGENTFLOW_PREVIEW_PUBLIC_URL on a separate hostname (AGENTFLOW_PREVIEW_FRAME_ANCESTORS is empty)',
    hint: '管理员尚未配置独立预览域名'
  });
  await testSessionFailure({
    code: 'preview_backend_on_studio', status: 400, storedBackend: 'https://studio.example', repair: 'session',
    message: 'invalid preview backend URL: preview backend must not use a studio hostname',
    hint: '后端地址指向工作台域名'
  });
  await testMetadataFailure();
})().catch(error => { console.error(error); process.exit(1); });
