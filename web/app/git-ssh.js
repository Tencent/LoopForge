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

// Unknown HTTPS hosts use Git transport without identifying the hosting product.
globalThis.LoopForgeGitHTTPS = function parseHTTPSRepository(raw) {
  const address=String(raw||'').trim().replace(/^git clone\s+/i,'');
  if(!/^https:\/\//i.test(address)||/[\s\\]/.test(address))return null;
  try{
    const u=new URL(address),host=u.host.toLowerCase();
    if(u.username||u.password||u.search||u.hash||['github.com','git.example.com'].includes(u.hostname.toLowerCase()))return null;
    if(!/^[a-z0-9][a-z0-9.-]*(?::[0-9]+)?$/i.test(host)||u.hostname.includes('..')||/^\d+(?:\.\d+){3}$/.test(u.hostname))return null;
    const parts=u.pathname.replace(/^\/+|\/+$/g,'').split('/');
    if(parts.some(part=>!part||decodeURIComponent(part)==='.'||decodeURIComponent(part)==='..'))return null;
    const name=parts[parts.length-1].replace(/\.git$/,'');if(!name)return null;
    const cloneUrl='https://'+host+'/'+parts.join('/');
    return {provider:'https',host,project:host+'/'+parts.join('/').replace(/\.git$/,''),name,cloneUrl,webUrl:cloneUrl.replace(/\.git$/,'')};
  }catch{return null}
};

// Self-managed GitLab keeps the host inside the project identity so several
// instances can coexist: "gitlab:<host>/<group>/<repo>". Bare URLs are only
// recognised for private/internal hosts, matching the server's trust rule;
// a public self-hosted instance must paste the explicit prefix.
/**
 * --- @ty.aicoding@1795420649000 ---
 */
globalThis.LoopForgeGitLab = function parseGitLabRepository(raw) {
  const address = String(raw || '').trim().replace(/^['"]|['"]$/g, '').replace(/^git clone /, '').replace(/^['"]|['"]$/g, '').split(/\s+/)[0];
  if (!address || /[\s"'<>\\?#]/.test(address)) return null;
  const build = (scheme, host, path) => {
    let clean = path.replace(/^\/+|\/+$/g, '').replace(/\.git$/i, '');
    // Drop a sub-page suffix (merge request, tree view, issue...) before taking the repo path.
    for (const marker of ['/-/', '/tree/', '/blob/', '/commit/', '/commits/', '/merge_requests/', '/issues/', '/snippets/', '/wikis/', '/releases/', '/tags/']) {
      const at = clean.indexOf(marker);
      if (at > 0) clean = clean.slice(0, at);
    }
    if (!/^[a-z0-9][a-z0-9._-]*(?::[0-9]+)?$/i.test(host)) return null;
    const parts = clean.split('/');
    if (parts.length < 2 || parts.some(part => !/^[a-z0-9_.~-]+$/i.test(part) || part === '.' || part === '..')) return null;
    const base = `${scheme}://${host.toLowerCase()}/${parts.join('/')}`;
    return { provider: 'gitlab', project: `gitlab:${host.toLowerCase()}/${parts.join('/')}`, name: parts.at(-1), webUrl: base, cloneUrl: base + '.git' };
  };
  const prefixed = /^gitlab:([^/]+)\/(.+)$/i.exec(address);
  if (prefixed) return build('https', prefixed[1], prefixed[2]);
  const url = /^(https?):\/\/([^/]+)\/(.+)$/i.exec(address);
  if (!url) return null;
  const scheme = url[1].toLowerCase(), host = url[2].toLowerCase();
  if (host.includes('@') || /[\s\\]/.test(host)) return null;
  const hostname = host.replace(/:[0-9]+$/, '');
  if (['github.com', 'git.example.com'].includes(hostname)) return null;
  const internal = /^(127\.|10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.)/.test(hostname) || /\.(internal|local)$/i.test(hostname);
  return internal ? build(scheme, host, url[3]) : null;
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
