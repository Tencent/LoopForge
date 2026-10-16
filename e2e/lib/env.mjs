// Live end-to-end suite configuration. Everything that talks to a real
// deployment reads from here so a single place decides the target and the
// credentials. Secrets are never stored in the repository: the token comes from
// AGENTFLOW_E2E_TOKEN. No other application configuration is read.

import fs from 'node:fs';
const DEFAULT_BASE = 'http://127.0.0.1:8090';
const PROD_HOSTS = [];

function resolveBase() {
  const base = (process.env.AGENTFLOW_E2E_BASE || DEFAULT_BASE).replace(/\/+$/, '');
  const host = new URL(base).hostname;
  if (PROD_HOSTS.some(re => re.test(host)) && process.env.AGENTFLOW_E2E_ALLOW_PROD !== '1') {
    throw new Error(`refusing to run the live e2e suite against production (${host}); set AGENTFLOW_E2E_ALLOW_PROD=1 to override`);
  }
  return base;
}

function flag(name, fallback) {
  const v = process.env[name];
  if (v === undefined || v === '') return fallback;
  return !/^(0|false|no|off)$/i.test(v);
}

const stamp = new Date().toISOString().replace(/[-:TZ.]/g, '').slice(2, 14);

export const cfg = {
  base: resolveBase(),
  token: (process.env.AGENTFLOW_E2E_TOKEN || (process.env.AGENTFLOW_E2E_TOKEN_FILE ? fs.readFileSync(process.env.AGENTFLOW_E2E_TOKEN_FILE,'utf8') : '')).trim(),
  // Every resource the suite creates carries this prefix so leftovers from a
  // crashed run are easy to spot and `npm run cleanup` can sweep them.
  prefix: 'e2e-auto',
  runTag: process.env.AGENTFLOW_E2E_RUN_TAG || stamp,
  projectId: process.env.AGENTFLOW_E2E_PROJECT || 'e2e-auto',
  // Real-model scenarios are opt-in because they consume model credits.
  model: flag('AGENTFLOW_E2E_MODEL', false),
  executor: process.env.AGENTFLOW_E2E_EXECUTOR || 'codebuddy',
  modelID: process.env.AGENTFLOW_E2E_MODEL_ID || '',
  sandbox: process.env.AGENTFLOW_E2E_SANDBOX || 'local',
  keep: flag('AGENTFLOW_E2E_KEEP', false),
};

export function requireToken() {
  if (!cfg.token) {
    throw new Error('no API token: export AGENTFLOW_E2E_TOKEN=afp_... (create one under 个人设置 → API Token)');
  }
  return cfg.token;
}

export function uid(label) {
  const rand = Math.random().toString(36).slice(2, 6);
  return `${cfg.prefix}-${label}-${cfg.runTag}-${rand}`.toLowerCase();
}
