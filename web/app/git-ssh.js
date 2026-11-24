// Canonicalize IPv6 spellings for comparisons with the server's host metadata.
globalThis.LoopForgeGitSSHHost = function normalizeSSHHost(endpoint) {
  const match = /^(\[[^\]]+\]|[^:]+):(\d+)$/.exec(String(endpoint || ''));
  if (!match) return '';
  let host = match[1].toLowerCase();
  if (host.startsWith('[')) {
    try { host = new URL(`http://${host}`).hostname; } catch { return ''; }
  }
  return `${host}:${Number(match[2])}`;
};

// Address preview only. The repository API validates and pins the SSH host.
globalThis.LoopForgeGitSSH = function parseSSHRepository(raw) {
  const address = String(raw || '').trim().replace(/^['"]|['"]$/g, '').replace(/^git clone /, '').replace(/^['"]|['"]$/g, '');
  if (!address || /[\s%?#\\]/.test(address)) return null;
  let user, host, port = '22', path, cloneUrl;
  if (address.startsWith('ssh://')) {
    const parsed = /^ssh:\/\/([^@]+)@(\[[^\]]+\]|[^:/]+)(?::(\d+))?(\/.+)$/.exec(address);
    if (!parsed) return null;
    [, user, host, , path] = parsed;
    if (parsed[3]) port = String(Number(parsed[3]));
    cloneUrl = `ssh://${user}@${host.toLowerCase()}:${port}${path}`;
  } else {
    const parsed = /^([^@]+)@(\[[^\]]+\]|[^:/]+):(.+)$/.exec(address);
    if (!parsed) return null;
    [, user, host, path] = parsed;
    cloneUrl = `${user}@${host.toLowerCase()}:${path}`;
  }
  if (!/^[a-z0-9_][a-z0-9_.-]*$/i.test(user) || !/^(?:[a-z0-9][a-z0-9.-]*|\[[a-f0-9:]+\])$/i.test(host) || host.includes('..') || Number(port) < 1 || Number(port) > 65535) return null;
  const segments = path.replace(/^\//, '').split('/');
  if (segments.some(segment => !/^[a-z0-9_.~-]+$/i.test(segment) || segment === '.' || segment === '..')) return null;
  const name = segments[segments.length - 1].replace(/\.git$/, '');
  if (!name) return null;
  const endpoint = globalThis.LoopForgeGitSSHHost(`${host}:${port}`);
  if (!endpoint) return null;
  // git@github.com: stays a GitHub repository (HTTPS + token, automatic PRs),
  // the same as on the server; only an explicit provider "ssh" uses SSH there.
  return { provider: 'ssh', host: endpoint, project: `${host.toLowerCase()}${port === '22' ? '' : ':' + port}/${segments.join('/')}`, name, cloneUrl, webUrl: '' };
};

// Use the URL parser to compress IPv6 and omit the HTTPS default port.
globalThis.LoopForgeGitHTTPSHost = function normalizeHTTPSHost(raw) {
  const input = String(raw || '').trim().toLowerCase();
  let authority = input.replace(/^https:\/\//, '').replace(/\/$/, '');
  if (!authority || /[\/@?#*%\s\\]/.test(authority)) return '';
  if (!input.startsWith('https://') && !authority.startsWith('[') && authority.split(':').length > 2) {
    authority = `[${authority}]`;
  }
  try {
    const url = new URL(`https://${authority}`);
    const hostname = url.hostname;
    if (hostname.startsWith('[')) {
      return /^\[::ffff:[a-f0-9]+:[a-f0-9]+\]$/.test(hostname) ? '' : url.host;
    }
    if (!/^[a-z0-9][a-z0-9.-]*$/.test(hostname) || hostname.includes('..') || /^\d+(?:\.\d+){3}$/.test(hostname)) return '';
    return url.host;
  } catch {
    return '';
  }
};

// Unknown HTTPS hosts use Git transport without identifying the hosting product.
globalThis.LoopForgeGitHTTPS = function parseHTTPSRepository(raw) {
  const address=String(raw||'').trim().replace(/^git clone\s+/i,'');
  if(!/^https:\/\//i.test(address)||/[\s\\]/.test(address))return null;
  try{
    const u=new URL(address),host=globalThis.LoopForgeGitHTTPSHost(`https://${u.host}`);
    if(!host||u.username||u.password||u.search||u.hash||['github.com','git.example.com'].includes(u.hostname.toLowerCase()))return null;
    const parts=u.pathname.replace(/^\/+|\/+$/g,'').split('/');
    if(parts.some(part=>!part||decodeURIComponent(part)==='.'||decodeURIComponent(part)==='..'))return null;
    const name=parts[parts.length-1].replace(/\.git$/,'');if(!name)return null;
    const cloneUrl='https://'+host+'/'+parts.join('/');
    return {provider:'https',host,project:host+'/'+parts.join('/').replace(/\.git$/,''),name,cloneUrl,webUrl:cloneUrl.replace(/\.git$/,'')};
  }catch{return null}
};

// The managed host is supplied by the deployment. Explicit old links remain
// readable so existing repositories can be opened and edited.
globalThis.LoopForgeGongfengRepository = function parseManagedRepository(raw, host = '') {
  const legacyHost = 'git.example.com';
  const managedHost = host || legacyHost;
  let value = String(raw || '').trim().replace(/^['"]+|['"]+$/g, '');
  if (/^git\s+clone\s+/i.test(value)) {
    value = value.replace(/^git\s+clone\s+/i, '').split(/\s+/)[0].replace(/^['"]+|['"]+$/g, '');
  }
  if (!value || /[\s"'<>\\?#]/.test(value)) return null;
  const ssh = /^(?:git@|ssh:\/\/git@)/i.test(value);
  const remote = value.replace(/^(?:https?:\/\/|ssh:\/\/)?(?:git@)?/i, '');
  const lower = remote.toLowerCase();
  const matched = [managedHost.toLowerCase(), legacyHost].find(candidate =>
    lower.startsWith(candidate + '/') || lower.startsWith(candidate + ':'));
  if (!matched) return null;
  const separator = remote[matched.length];
  let path = remote.slice(matched.length + 1);
  if (separator === ':') path = path.replace(/^\d+\//, '');
  for (const marker of ['/-/', '/tree/', '/blob/', '/commit/', '/commits/', '/merge_requests/', '/issues/', '/snippets/', '/wikis/', '/releases/', '/tags/']) {
    const at = path.indexOf(marker);
    if (at !== -1) path = path.slice(0, at);
  }
  path = path.replace(/\/+$/, '').replace(/\.git$/i, '');
  const segments = path.split('/');
  if (segments.length < 2 || path.includes('..') || segments.some(segment => !segment)) return null;
  const webUrl = `https://${managedHost}/${path}`;
  const cloneUrl = ssh && !managedHost.includes(':') ? `git@${managedHost}:${path}.git` : webUrl + '.git';
  return {provider:'gongfeng', project:path, name:segments.at(-1), webUrl, cloneUrl};
};
