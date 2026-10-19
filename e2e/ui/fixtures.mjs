import fs from 'node:fs';
import { test as base, expect } from '@playwright/test';
import { identityFile } from './global-setup.mjs';

const identity = () => JSON.parse(fs.readFileSync(identityFile, 'utf8'));

// Noise that is not a product defect: aborted polling when the page navigates,
// the avatar CDN that the test network cannot reach, the resource lines that
// duplicate an HTTP error already recorded, and VS Code probing optional
// .vscode/* settings files in the run workspace.
const IGNORED = [
  /net::ERR_ABORTED/,
  /Failed to load resource: net::ERR_(NAME_NOT_RESOLVED|CONNECTION|TIMED_OUT|ADDRESS_UNREACHABLE)/,
  /Failed to load resource: the server responded with a status of/,
  /\/api\/auth\/photo/,
  /r\.hrc\.example\.com|rhrc\.example\.com/,
  /\/workspace\/file\?path=\.vscode%2F/,
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

export const test = base.extend({
  identity: async ({}, use) => use(identity()),
  page: async ({ page, identity }, use) => {
    await page.addInitScript(({ projectId }) => {
      if (!sessionStorage.getItem('e2e-init')) {
        sessionStorage.setItem('e2e-init', '1');
        localStorage.setItem('agentflow.activeProject', projectId);
      }
    }, { projectId: identity.projectId });
    await use(page);
  },
});

export async function openView(page, view) {
  await page.goto(`/#/${view}`);
  await page.waitForLoadState('networkidle');
}

export { expect };
