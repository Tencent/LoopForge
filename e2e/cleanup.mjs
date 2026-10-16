// Sweeps what a crashed or interrupted run left behind. Everything the suite
// creates lives in the dedicated e2e project or carries the e2e-auto prefix;
// resources of other projects are never touched.
//
//   AGENTFLOW_E2E_RUN_TAG=<tag> node cleanup.mjs  delete this invocation
//   node cleanup.mjs --all       sweep the dedicated test project (no concurrent suites)
//   node cleanup.mjs --dry-run  only list them
import { api, get, del, post, list } from './lib/api.mjs';
import { cfg, requireToken } from './lib/env.mjs';

requireToken();
const dry = process.argv.includes('--dry-run');
const all=process.argv.includes('--all');
const tag=process.env.AGENTFLOW_E2E_RUN_TAG;
if(!all&&!tag){console.log('No run tag supplied; nothing deleted. Use AGENTFLOW_E2E_RUN_TAG, or --all for an idle test project.');process.exit(0)}
const ours=item=>!!item&&(all?item.projectId===cfg.projectId:[item.id,item.name,item.title,item.workflowId,item.workflowSnapshot?.id].some(value=>String(value||'').includes(tag)));
const failures = [];
let removed = 0;

async function sweep(label, items, remove) {
  for (const item of items) {
    const id = item.id;
    if (dry) { console.log(`[dry-run] ${label} ${id}`); continue; }
    try {
      await remove(item);
      removed++;
      console.log(`deleted ${label} ${id}`);
    } catch (err) {
      failures.push(`${label} ${id}: ${err.message}`);
    }
  }
}

async function sweepCollection(label, path, filter = ours) {
  const items = list(await get(path)).filter(item => item && !item.builtin && filter(item));
  await sweep(label, items, item => del(`${path}/${encodeURIComponent(item.id)}`));
}

const LIVE = new Set(['queued', 'pending', 'running', 'waiting', 'waiting_approval']);
const runs = list(await get(`/api/runs?projectId=${encodeURIComponent(cfg.projectId)}&limit=500`))
  .filter(ours);
await sweep('run', runs, async run => {
  if (LIVE.has(run.status)) await post(`/api/runs/${run.id}/cancel`, {}, { expect: [200, 202, 400, 409] });
  await api('DELETE', `/api/runs/${run.id}`, undefined, { expect: [200, 204, 400, 404, 409] });
});

// Open issues keep their SOP binding and block the workflow delete.
const openIssues = list(await get(`/api/issues?projectId=${encodeURIComponent(cfg.projectId)}&limit=500`))
  .filter(issue => issue.status !== 'closed' && ours(issue));
await sweep('issue (close)', openIssues, issue => post(`/api/issues/${issue.id}/close`, {}, { expect: [200, 404, 409] }));

await sweepCollection('schedule job', '/api/schedule-jobs');
await sweepCollection('assistant task', '/api/assistant/tasks');
await sweepCollection('workflow', '/api/workflows');
await sweepCollection('agent', '/api/agents', item => ours(item) && !item.shared);
await sweepCollection('rule', '/api/rules', item => ours(item) && !item.shared);
await sweepCollection('skill', '/api/skills', item => ours(item) && !item.shared);
// mcp-<n>-project-<p> are the platform defaults, re-provisioned for every project.
await sweepCollection('mcp server', '/api/mcp-servers', item => ours(item) && !item.shared && !/^mcp-\d+-project-/.test(item.id));
await sweepCollection('sandbox', '/api/sandboxes');
await sweepCollection('environment', '/api/environments');
await sweepCollection('dockerfile', '/api/dockerfiles');
await sweepCollection('repository', '/api/repositories');
// Runner-mode scenarios bind throwaway machines; a killed daemon cannot
// deregister itself, so its offline record is removed here.
const staleRunners = list(await get('/api/runners?mine=1')).filter(r => ours(r) && r.status !== 'online');
await sweep('runner', staleRunners, r => api('DELETE', `/api/runners/${encodeURIComponent(r.id)}`, undefined, { expect: [200, 404, 409] }));

console.log(dry ? 'dry run finished' : `removed ${removed} item(s)`);
if (failures.length) {
  console.error(`could not remove:\n  ${failures.join('\n  ')}`);
  process.exitCode = 1;
}
