import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test as base, expect } from '@playwright/test';
import { cfg } from '../lib/env.mjs';
import { identityFile } from './global-setup.mjs';

const identity = () => JSON.parse(fs.readFileSync(identityFile, 'utf8'));

// Noise that is not a product defect: aborted polling when the page navigates,
// the avatar CDN that the test network cannot reach, the resource lines that
// duplicate an HTTP error already recorded, and VS Code probing optional
// .vscode/* settings files in the run workspace. WeCom account bindings only
// accept a browser login and refuse any Bearer token by design; this browser
// carries the suite's token, so the settings page shows that refusal inline.
// An open AI conversation polls its generation, which is 404 until the first
// reply is requested (the workbench treats that as "idle").
const IGNORED = [
  /net::ERR_ABORTED/,
  /Failed to load resource: net::ERR_(NAME_NOT_RESOLVED|CONNECTION|TIMED_OUT|ADDRESS_UNREACHABLE)/,
  /Failed to load resource: the server responded with a status of/,
  /\/api\/auth\/photo/,
  /r\.hrc\.example\.com|rhrc\.example\.com/,
  /\/workspace\/file\?path=\.vscode%2F/,
  /^HTTP 401 GET \/api\/bot-bindings$/,
  /^HTTP 404 GET \/api\/assistant\/sessions\/[^/]+\/generation\?after=\d+$/,
];

// AGENTFLOW_E2E_KNOWN_ERRORS: a regex for errors already fixed but not yet
// deployed to the target environment, so the rest of the suite stays useful.
const KNOWN = process.env.AGENTFLOW_E2E_KNOWN_ERRORS ? [new RegExp(process.env.AGENTFLOW_E2E_KNOWN_ERRORS)] : [];

export function watchErrors(page, { allow = [] } = {}) {
  const errors = [];
  const skip = text => [...IGNORED, ...KNOWN, ...allow].some(re => re.test(text));
  page.on('pageerror', e => errors.push(`pageerror: ${e.message}`));
  page.on('console', m => { if (m.type() === 'error' && !skip(m.text())) errors.push(`console: ${m.text()}`); });
  page.on('response', r => {
    const url = new URL(r.url());
    const line = `HTTP ${r.status()} ${r.request().method()} ${url.pathname}${url.search}`;
    if (r.status() >= 400 && url.origin === new URL(page.url() || r.url()).origin && !skip(line)) errors.push(line);
  });
  return errors;
}

// The browser context already carries a real password session (setup-auth);
// only pin the active project on the first page load.
async function signIn(page, { projectId }) {
  await page.addInitScript(({ projectId }) => {
    if (location.protocol === 'about:') return;
    if (!sessionStorage.getItem('e2e-init')) {
      sessionStorage.setItem('e2e-init', '1');
      localStorage.setItem('agentflow.activeProject', projectId);
    }
  }, { projectId });
}

// AGENTFLOW_E2E_JS_COVERAGE=1 records Chromium's V8 coverage of the platform's
// own web/ scripts; e2e/coverage/web-merge.mjs merges it with the webtest/ unit
// coverage. Third-party bundles are left out.
const COVERAGE_DIR = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'test-results', 'web-coverage');
const THIRD_PARTY = /^\/(vendor|monaco|vscode-web|extensions|native-chat|chatbot)\//;

async function startCoverage(page) {
  if (process.env.AGENTFLOW_E2E_JS_COVERAGE !== '1') return async () => {};
  await page.coverage.startJSCoverage({ resetOnNavigation: false });
  return async () => {
    const entries = await page.coverage.stopJSCoverage().catch(() => []);
    const origin = new URL(cfg.base).origin;
    const kept = [];
    for (const e of entries) {
      let url;
      try { url = new URL(e.url); } catch { continue; }
      if (url.origin !== origin || !url.pathname.endsWith('.js') || THIRD_PARTY.test(url.pathname)) continue;
      kept.push({ path: url.pathname, sourceLength: e.source?.length ?? null, functions: e.functions });
    }
    if (!kept.length) return;
    fs.mkdirSync(COVERAGE_DIR, { recursive: true });
    fs.writeFileSync(path.join(COVERAGE_DIR, `${Date.now()}-${crypto.randomUUID()}.json`), JSON.stringify(kept));
  };
}

export const test = base.extend({
  identity: async ({}, use) => use(identity()),
  page: async ({ page, identity }, use) => {
    await signIn(page, identity);
    const stopCoverage = await startCoverage(page);
    await use(page);
    await stopCoverage();
  },
});

// openPageAs opens the app in a separate browser context signed in as another
// user (their API token and identity); close the returned context when done.
export async function openPageAs(browser, { token, user, projectId }) {
  const context = await browser.newContext({
    baseURL: cfg.base,
    extraHTTPHeaders: { Authorization: `Bearer ${token}` },
    viewport: { width: 1440, height: 900 },
    locale: 'zh-CN',
  });
  const page = await context.newPage();
  // This context has no password session; answer /api/auth/current with the
  // token owner so the page renders as that user.
  await page.route('**/api/auth/current**', route => route.fulfill({
    json: { user: { ...user, isGuest: false }, authRequired: true, appDomain: '' },
  }));
  await signIn(page, { projectId });
  const stopCoverage = await startCoverage(page);
  const close = context.close.bind(context);
  context.close = async (...args) => {
    await stopCoverage();
    return close(...args);
  };
  return { context, page };
}

export async function openView(page, view) {
  await page.goto(`/#/${view}`);
  await page.waitForLoadState('networkidle');
}

export { expect };
