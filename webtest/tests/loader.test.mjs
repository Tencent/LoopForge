import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { loadApp, WEB_ROOT } from '../lib/load-app.mjs';

test('loads every index script in document order using absolute filenames', async (t) => {
  const app = await loadApp({ dispatchReady: false });
  t.after(app.close);
  assert.equal(app.files.length, app.document.querySelectorAll('script[src]').length);
  assert.ok(app.files.every((file) => path.isAbsolute(file) && file.startsWith(WEB_ROOT + path.sep)));
  assert.deepEqual(app.files.slice(0, 5), ['boot-theme.js', 'boot.js', 'i18n-en.js', 'i18n.js', 'app.js'].map((file) => path.join(WEB_ROOT, file)));
  // The shared model catalog must load right after app.js and before every picker
  // script (flows, agents, bots, sync-schedule, composer, runs) that reads
  // window.AgentFlowAssistantModels; otherwise those pickers silently stop binding.
  assert.equal(app.files[5], path.join(WEB_ROOT, 'app', 'assistant-models.js'));
  assert.equal(app.files.at(-1), path.join(WEB_ROOT, 'orchestrate.js'));
  assert.equal(typeof app.evaluate('api'), 'function');
  assert.equal(typeof app.window.AgentFlowHarnessIcons.render, 'function');
});

test('programmable fetch router records parsed requests', async (t) => {
  const app = await loadApp({
    dispatchReady: false,
    routes: [{ method: 'POST', path: '/api/example', body: { id: 'ok' } }],
  });
  t.after(app.close);
  const value = await app.window.fetch('/api/example', {
    method: 'POST',
    body: JSON.stringify({ name: 'sample' }),
  }).then((res) => res.json());
  assert.deepEqual(value, { id: 'ok' });
  assert.deepEqual(app.router.calls.at(-1).body, { name: 'sample' });
});
