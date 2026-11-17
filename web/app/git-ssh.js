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
