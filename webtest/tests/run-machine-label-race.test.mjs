import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

for (const entry of ['showRunDetail', 'selectRun']) {
  test(`${entry} ignores machine metadata after switching runs`, async (t) => {
    const app = await loadApp();
    t.after(app.close);
    const base = app.evaluate('structuredClone(state.data.runs[0])');
    const first = { ...base, status: 'succeeded', executionContext: { executionMode: 'runner', executionRunnerId: 'runner-first' } };
    const second = { ...base, id: 'run-2', status: 'succeeded', executionContext: {} };
    let release;
    app.router.get('/api/runs/run-1', first);
    app.router.get('/api/runs/run-2', second);
    app.router.get('/api/runners?mine=1', () => new Promise((resolve) => { release = resolve; }));
    app.evaluate(`state.runners=[];state.data.runs=${JSON.stringify([first, second])};${entry}('run-1')`);
    await settle(4);
    assert.equal(typeof release, 'function');
    app.evaluate(`${entry}('run-2')`);
    await settle(4);
    release([{ id: 'runner-first', name: 'Stale machine', ownerUserId: 42 }]);
    await settle(4);
    assert.equal(app.evaluate('state.activeRun.id'), 'run-2');
    assert.equal(app.evaluate('state.runners.length'), 0);
    assert.doesNotMatch(app.document.querySelector('#runsDetailSection').textContent, /Stale machine/);
  });
}
