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

// Forgejo uses SSH for Git and a separate HTTPS address for opening PRs.
globalThis.LoopForgeForgejoRepository = function parseForgejoRepository(cloneURL, webURL) {
  const ssh = globalThis.LoopForgeGitSSH(cloneURL);
  const raw = String(webURL || '').trim().replace(/\/+$/, '').replace(/\.git$/, '');
  if (!ssh || /[\s%?#\\]/.test(raw)) return null;
  const match = /^https:\/\/([^/]+)\/(.+)$/.exec(raw);
  if (!match) return null;
  const parts = match[2].split('/');
  if (parts.length < 2 || parts.some(part => !/^[a-z0-9_][a-z0-9_.-]*$/i.test(part))) return null;
  try {
    const url = new URL(raw);
    if (url.username || url.password || url.hostname !== ssh.host.replace(/:\d+$/, '')) return null;
    const path = parts.slice(-2).join('/').toLowerCase();
    const sshPath = ssh.project.slice(ssh.project.indexOf('/') + 1).replace(/\.git$/, '').toLowerCase();
    if (path !== sshPath) return null;
    const base = parts.slice(0, -2).join('/');
    const webUrl = url.origin + '/' + (base ? base + '/' : '') + path;
    return {...ssh, provider:'forgejo', project:'forge:'+webUrl, webUrl};
  } catch {
    return null;
  }
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
