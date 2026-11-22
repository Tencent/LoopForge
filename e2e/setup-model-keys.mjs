// Optional post-deploy setup for dedicated E2E accounts.
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

const keyFields = new Map([
  ['codebuddy', 'codeBuddyApiKey'],
  ['codex', 'codexApiKey'],
  ['claude-code', 'claudeCodeApiKey'],
]);
const agentCLIs = new Set(['agy', 'cursor', 'dsh', 'kimi', 'opencode', 'pi', 'zcode']);
const codeBuddyEnvironments = new Set(['', 'internal', 'ioa']);
const githubHost = 'github.com';

function isObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function readAccounts(file) {
  let raw;
  try {
    raw = readFileSync(file, 'utf8').trim();
  } catch (error) {
    throw new Error(`Test credentials: cannot read configuration file (${error.code || 'file error'}).`);
  }
  if (!raw) return [];

  let config;
  try {
    config = JSON.parse(raw);
  } catch {
    throw new Error('Test credentials: configuration file contains invalid JSON.');
  }
  if (!Array.isArray(config?.accounts)) {
    throw new Error('Test credentials: configuration must contain an accounts array.');
  }

  const accounts = [];
  const seen = new Set();
  for (const [index, account] of config.accounts.entries()) {
    const label = `Test credentials: account #${index + 1}`;
    if (!isObject(account) || (account.apiKeys !== undefined && !isObject(account.apiKeys))) {
      throw new Error(`${label} must be an object with an optional apiKeys object.`);
    }
    if (account.gitToken != null && typeof account.gitToken !== 'string') {
      throw new Error(`${label} gitToken must be a string or null.`);
    }
    const gitToken = account.gitToken?.trim() ?? '';
    const update = {};
    let keyCount = 0;
    for (const [provider, value] of Object.entries(account.apiKeys ?? {})) {
      if (value === null) continue;
      if (typeof value !== 'string') {
        throw new Error(`${label} apiKeys values must be strings or null.`);
      }
      const apiKey = value.trim();
      if (!apiKey) continue;
      const field = keyFields.get(provider);
      if (field) {
        update[field] = apiKey;
      } else if (agentCLIs.has(provider)) {
        update.agentCliCredentials ??= {};
        update.agentCliCredentials[provider] = { apiKey };
      } else {
        throw new Error(`${label} has an unsupported provider ID. Use a LoopForge CLI ID.`);
      }
      keyCount++;
    }
    if (keyCount === 0 && !gitToken) continue;

    const { login, passwordEnv, codeBuddyInternetEnvironment } = account;
    if (typeof login !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_.-]{1,63}$/.test(login)) {
      throw new Error(`${label} must specify a valid local login.`);
    }
    const identity = login.toLowerCase();
    if (seen.has(identity)) throw new Error(`${label} repeats the login ${login}.`);
    seen.add(identity);
    if (typeof passwordEnv !== 'string' || !/^[A-Za-z_][A-Za-z0-9_]*$/.test(passwordEnv)) {
      throw new Error(`${label} must specify a password environment variable name in passwordEnv.`);
    }
    const password = process.env[passwordEnv];
    if (!password) {
      throw new Error(`${label} (${login}) needs password environment variable ${passwordEnv}.`);
    }
    if (update.codeBuddyApiKey && codeBuddyInternetEnvironment !== undefined) {
      if (!codeBuddyEnvironments.has(codeBuddyInternetEnvironment)) {
        throw new Error(`${label} codeBuddyInternetEnvironment must be "", "internal", or "ioa".`);
      }
      update.codeBuddyInternetEnvironment = codeBuddyInternetEnvironment;
    }
    accounts.push({ login, password, update, keyCount, gitToken });
  }
  return accounts;
}

function testBase(raw) {
  let base;
  try {
    base = new URL(raw);
  } catch {
    throw new Error('Test credentials: set AGENTFLOW_E2E_BASE to the test deployment URL.');
  }
  const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(base.hostname);
  if (base.username || base.password || base.search || base.hash ||
      !(base.protocol === 'https:' || (base.protocol === 'http:' && loopback))) {
    throw new Error('Test credentials: the test URL must use HTTPS or loopback HTTP, with no credentials, query or fragment.');
  }
  return base;
}

async function request(base, path, method, data, action, cookie) {
  const headers = { Origin: base.origin, 'Content-Type': 'application/json' };
  if (cookie) headers.Cookie = cookie;
  let response;
  try {
    response = await fetch(base.origin + base.pathname.replace(/\/+$/, '') + path, {
      method, headers, body: JSON.stringify(data),
      redirect: 'error',
      signal: AbortSignal.timeout(15_000),
    });
  } catch {
    throw new Error(`Test credentials: ${action} failed (network error, timeout or redirect).`);
  }
  if (!response.ok) {
    await response.body?.cancel().catch(() => {});
    throw new Error(`Test credentials: ${action} failed (HTTP ${response.status}).`);
  }
  return response;
}

async function saveGitHubToken(base, path, cookie, login, token) {
  const response = await request(base, path, 'GET', undefined, `read Git credentials for ${login}`, cookie);
  const credentials = await response.json().catch(() => null);
  if (!Array.isArray(credentials)) {
    throw new Error(`Test credentials: unexpected Git credentials response for ${login}.`);
  }

  // An existing personal credential must be updated by ID. The list carries
  // only metadata, so we can preserve its settings while replacing the token.
  const existing = credentials.find(credential => credential?.host === githubHost && !credential.managed);
  const update = existing
    ? { id: existing.id, name: existing.name, platform: existing.platform, username: existing.username }
    : { name: 'GitHub E2E', platform: 'github' };
  const saved = await request(
    base, path, 'PUT', { ...update, host: githubHost, token }, `save GitHub token for ${login}`, cookie,
  );
  await saved.body?.cancel().catch(() => {});
  console.log(`Test credentials: saved GitHub token for ${login}.`);
}

export async function setupModelKeys(
  file = process.env.AGENTFLOW_E2E_MODEL_KEYS_FILE,
  rawBase = process.env.AGENTFLOW_E2E_BASE,
) {
  if (!file?.trim()) return;
  const accounts = readAccounts(file.trim());
  if (accounts.length === 0) {
    console.log('Test credentials: nothing configured, skipped.');
    return;
  }
  const base = testBase(rawBase);

  // Validate every account before writing through the personal settings APIs.
  const sessions = [];
  for (const account of accounts) {
    const { login, password } = account;
    const response = await request(base, '/api/auth/password', 'POST', { login, password }, `login for ${login}`);
    const cookie = response.headers.getSetCookie()
      .find(value => value.startsWith('agentflow_user='))?.split(';', 1)[0];
    const user = (await response.json().catch(() => null))?.user;
    if (!cookie || cookie === 'agentflow_user=' || !Number.isSafeInteger(user?.id) || user.id <= 0 ||
        typeof user.loginName !== 'string' || user.loginName.toLowerCase() !== login.toLowerCase()) {
      throw new Error(`Test credentials: login for ${login} did not return a matching user session.`);
    }
    sessions.push({ account, id: user.id, cookie });
  }

  for (const { account, id, cookie } of sessions) {
    const { login, update, keyCount, gitToken } = account;
    const userPath = `/api/users/${id}`;
    if (keyCount > 0) {
      const response = await request(base, userPath + '/credentials', 'PUT', update, `save model keys for ${login}`, cookie);
      await response.body?.cancel().catch(() => {});
      console.log(`Test credentials: saved ${keyCount} model key(s) for ${login}.`);
    }
    if (gitToken) {
      await saveGitHubToken(base, userPath + '/git-http-credentials', cookie, login, gitToken);
    }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    await setupModelKeys();
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
