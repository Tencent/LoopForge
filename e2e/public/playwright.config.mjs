import {defineConfig} from '@playwright/test';
export default defineConfig({
  testDir: '.', testMatch: '*.spec.mjs', workers: 1, timeout: 60_000,
  outputDir: '../test-results/public', reporter: [['list']],
  use: {
    baseURL: process.env.LOOPFORGE_TEST_URL || 'http://127.0.0.1:8090',
    viewport: {width:1600,height:1000}, trace:'retain-on-failure', screenshot:'only-on-failure',
    launchOptions:{args:['--no-sandbox']},
  },
});
