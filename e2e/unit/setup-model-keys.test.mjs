import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { setupModelKeys } from '../setup-model-keys.mjs';

const base = 'http://127.0.0.1:8090';
const passwordEnv = 'LOOPFORGE_TEST_MODEL_KEYS_FIXTURE_PASSWORD';
const password = 'fixture-password';

function configFile(t, contents) {
  const dir = mkdtempSync(join(tmpdir(), 'loopforge-model-keys-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const file = join(dir, 'keys.json');
  writeFileSync(file, typeof contents === 'string' ? contents : JSON.stringify(contents), { mode: 0o600 });
  return file;
}

function setFixturePassword(t) {
  const previous = process.env[passwordEnv];
  process.env[passwordEnv] = password;
  t.after(() => {
    if (previous === undefined) delete process.env[passwordEnv];
    else process.env[passwordEnv] = previous;
  });
}

test('skips credential setup without a file or any nonblank credential', async t => {
  const fetch = t.mock.method(globalThis, 'fetch');
  await setupModelKeys('');
  await setupModelKeys(configFile(t, '   \n'));
  await setupModelKeys(configFile(t, { accounts: [] }));
  await setupModelKeys(configFile(t, { accounts: [
    { gitToken: '  ', apiKeys: { codex: '  ', 'claude-code': null } },
    { gitToken: null },
  ] }));
  assert.equal(fetch.mock.callCount(), 0);
});

test('updates each test account with its own model keys, CodeBuddy edition, and Git token', async t => {
  setFixturePassword(t);
  const file = configFile(t, { accounts: [
    {
      login: 'e2e-models', passwordEnv, codeBuddyInternetEnvironment: 'ioa', gitToken: 'fake-github-token',
      apiKeys: {
        dsh: ' fake-dsh ', codebuddy: 'fake-ioa', cursor: 'fake-cursor',
        kimi: 'fake-kimi', zcode: 'fake-zcode', codex: '', 'claude-code': 'fake-claude',
      },
    },
    {
      login: 'e2e-codebuddy-cn', passwordEnv, codeBuddyInternetEnvironment: 'internal',
      apiKeys: { codebuddy: 'fake-cn', codex: 'fake-codex', 'claude-code': null },
    },
  ] });
  const requests = [];
  const users = { 'e2e-models': 41, 'e2e-codebuddy-cn': 42 };
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    assert.equal(options.redirect, 'error');
    assert.equal(options.headers.Origin, base);
    const body = options.body === undefined ? undefined : JSON.parse(options.body);
    requests.push([options.method, url, options.headers.Cookie, body]);
    if (options.method === 'GET') return Response.json([]);
    if (options.method === 'PUT') return Response.json({});
    const id = users[body.login];
    return Response.json({ user: { id, loginName: body.login } }, {
      headers: { 'Set-Cookie': `agentflow_user=session-${id}; Path=/; HttpOnly` },
    });
  });

  await setupModelKeys(file, base);
  assert.deepEqual(requests, [
    ['POST', base + '/api/auth/password', undefined, { login: 'e2e-models', password }],
    ['POST', base + '/api/auth/password', undefined, { login: 'e2e-codebuddy-cn', password }],
    ['PUT', base + '/api/users/41/credentials', 'agentflow_user=session-41', {
      codeBuddyApiKey: 'fake-ioa', codeBuddyInternetEnvironment: 'ioa', claudeCodeApiKey: 'fake-claude',
      agentCliCredentials: {
        dsh: { apiKey: 'fake-dsh' }, cursor: { apiKey: 'fake-cursor' },
        kimi: { apiKey: 'fake-kimi' }, zcode: { apiKey: 'fake-zcode' },
      },
    }],
    ['GET', base + '/api/users/41/git-http-credentials', 'agentflow_user=session-41', undefined],
    ['PUT', base + '/api/users/41/git-http-credentials', 'agentflow_user=session-41', {
      host: 'github.com', name: 'GitHub E2E', platform: 'github', token: 'fake-github-token',
    }],
    ['PUT', base + '/api/users/42/credentials', 'agentflow_user=session-42', {
      codeBuddyApiKey: 'fake-cn', codeBuddyInternetEnvironment: 'internal', codexApiKey: 'fake-codex',
    }],
  ]);
});

test('imports a GitHub-only token and replaces an existing credential by ID', async t => {
  setFixturePassword(t);
  const login = 'e2e-git';
  const path = base + '/api/users/43/git-http-credentials';
  const account = { login, passwordEnv };
  const otherHost = { id: 'gitlab-id', host: 'gitlab.example.com', name: 'GitLab', platform: 'gitlab' };
  const existingGitHub = {
    id: 'github-id', host: 'github.com', name: 'Existing GitHub', platform: 'github', username: 'octo',
  };
  let credentials = [otherHost];
  const requests = [];
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    if (url === base + '/api/auth/password') {
      return Response.json({ user: { id: 43, loginName: login } }, {
        headers: { 'Set-Cookie': 'agentflow_user=session-43; Path=/' },
      });
    }
    assert.equal(url, path);
    assert.equal(options.headers.Cookie, 'agentflow_user=session-43');
    requests.push([options.method, options.body === undefined ? undefined : JSON.parse(options.body)]);
    return Response.json(credentials);
  });

  await setupModelKeys(configFile(t, { accounts: [{ ...account, gitToken: ' fake-github-first ' }] }), base);
  credentials = [otherHost, existingGitHub];
  await setupModelKeys(configFile(t, { accounts: [{ ...account, gitToken: 'fake-github-rotated' }] }), base);
  assert.deepEqual(requests, [
    ['GET', undefined],
    ['PUT', { name: 'GitHub E2E', platform: 'github', host: 'github.com', token: 'fake-github-first' }],
    ['GET', undefined],
    ['PUT', { ...existingGitHub, token: 'fake-github-rotated' }],
  ]);
});

test('rejects unreadable, invalid and duplicate input before making requests', async t => {
  setFixturePassword(t);
  const fetch = t.mock.method(globalThis, 'fetch');
  const missing = configFile(t, '') + '.missing';
  await assert.rejects(setupModelKeys(missing, base), /cannot read configuration file/);
  await assert.rejects(setupModelKeys(configFile(t, '{ "apiKeys": "fake-private"'), base), {
    message: 'Test credentials: configuration file contains invalid JSON.',
  });
  const account = { login: 'e2e-models', passwordEnv, apiKeys: { dsh: 'fake-key' } };
  const duplicate = configFile(t, { accounts: [account, { ...account, login: 'E2E-models' }] });
  await assert.rejects(setupModelKeys(duplicate, base), /repeats the login/);
  const unsupported = configFile(t, { accounts: [{ ...account, apiKeys: { deepseek: 'fake-key' } }] });
  await assert.rejects(setupModelKeys(unsupported, base), /unsupported provider ID/);
  const invalidGitToken = configFile(t, { accounts: [{ ...account, gitToken: 123 }] });
  await assert.rejects(setupModelKeys(invalidGitToken, base), /gitToken must be a string or null/);
  const valid = configFile(t, { accounts: [account] });
  await assert.rejects(setupModelKeys(valid, 'http://test.example.invalid'), /HTTPS or loopback HTTP/);
  assert.equal(fetch.mock.callCount(), 0);
});

test('a failed login blocks every save and reports only the HTTP status', async t => {
  setFixturePassword(t);
  const file = configFile(t, { accounts: [
    { login: 'e2e-models', passwordEnv, apiKeys: { dsh: 'fake-dsh' } },
    { login: 'e2e-codebuddy-cn', passwordEnv, apiKeys: { codebuddy: 'fake-cn' } },
  ] });
  const fetch = t.mock.method(globalThis, 'fetch', async (url, { method, body }) => {
    assert.equal(method, 'POST');
    const { login } = JSON.parse(body);
    if (login !== 'e2e-models') return new Response('fake-secret-in-error', { status: 401 });
    return Response.json({ user: { id: 41, loginName: login } }, {
      headers: { 'Set-Cookie': 'agentflow_user=session-41; Path=/' },
    });
  });
  await assert.rejects(setupModelKeys(file, base), {
    message: 'Test credentials: login for e2e-codebuddy-cn failed (HTTP 401).',
  });
  assert.equal(fetch.mock.callCount(), 2);
});
