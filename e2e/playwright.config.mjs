import {fileURLToPath} from 'node:url';
import { defineConfig } from '@playwright/test';
import { cfg, requireToken } from './lib/env.mjs';

// The browser uses a real password session prepared by global setup.
// API fixtures use the separately supplied token; authentication is never mocked.
export default defineConfig({
  testDir: './ui',
  testMatch: /.*\.spec\.mjs$/,
  globalSetup: './ui/global-setup.mjs',
  workers: 1,
  fullyParallel: false,
  timeout: 180_000,
  expect: { timeout: 20_000 },
  retries: 0,
  outputDir: './test-results/ui',
  reporter: [['list'], ['html', { open: 'never', outputFolder: './test-results/ui-report' }]],
  use: {
    baseURL: cfg.base,
    storageState: fileURLToPath(new URL('./.auth/browser.json',import.meta.url)),
    viewport: { width: 1440, height: 900 },
    locale: 'zh-CN',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
    launchOptions: process.env.E2E_CHROMIUM ? { executablePath: process.env.E2E_CHROMIUM, args: ['--no-sandbox'] } : { args: ['--no-sandbox'] },
  },
});
