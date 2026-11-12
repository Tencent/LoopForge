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

(async () => {
  for (const studio of ['http:', 'https:']) {
    for (const preview of ['http://preview.example:8090/preview/token/', 'https://preview.example:8443/preview/token/']) {
      await testSession(studio, preview);
    }
  }
})().catch(error => { console.error(error); process.exit(1); });
