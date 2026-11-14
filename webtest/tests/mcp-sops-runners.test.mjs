import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('MCP import accepts common formats, validates bad input, and saves HTTP configuration', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const parsed = app.evaluate(`
    mcpImportCandidates(mcpParseJSONText(\`\\\`\\\`\\\`json
      {"mcpServers":{"docs":{"type":"streamable-http","url":"[Docs](https://mcp.example.test/api)",
      "headers":{"Authorization":"Bearer x"},"timeoutSec":90}}}
    \\\`\\\`\\\`\`))[0]
  `);
  assert.equal(parsed.id, 'docs');
  assert.equal(parsed.transport, 'http');
  assert.equal(parsed.url, 'https://mcp.example.test/api');
  assert.equal(JSON.stringify(parsed.headers), JSON.stringify({ Authorization: 'Bearer x' }));
  assert.throws(() => app.evaluate(`mcpNormalizeImportedServer({id:'bad',transport:'stdio'})`), /缺少 command/);
  assert.throws(() => app.evaluate(`mcpImportStringMap({A:{}},'headers')`), /必须是字符串/);

  app.router.post('/api/mcp-servers/custom', (call) => ({
    ...call.body.server, id: call.body.server.id, scope: call.body.scope, projectId: call.body.projectId,
  }));
  app.evaluate(`openCustomMCPModal();mcpApplyImportedServer(${JSON.stringify(parsed)})`);
  const form = app.document.querySelector('#customMcpForm');
  form.elements.scope.value = 'project';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  const call = app.router.calls.find(c => c.path === '/api/mcp-servers/custom');
  assert.equal(call.method, 'POST');
  assert.equal(call.body.server.headers.Authorization, 'Bearer x');
  assert.equal(call.body.projectId, 'project-1');
});

test('generic SOP requirements reflect credentials and installed MCPs, then clone through API', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const generic = {
    id: 'generic-delivery', name: '通用交付', description: 'standard', generic: true, version: 2,
    nodes: [{ id: 'mr', name: '发起 MR', type: 'agent' }, { id: 'gate', name: '验收', type: 'approval' }],
    requirements: [
      { key: 'gongfeng-git-token', nodeId: 'mr' },
      { key: 'tapd-mcp', nodeId: 'mr' },
      { key: 'gongfeng-mcp', nodeId: 'mr' },
    ],
  };
  app.router.post('/api/workflows/generic-delivery/clone', (call) => ({
    ...generic, ...call.body, generic: false, tags: ['generic-source:generic-delivery'],
  }));
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner',isPlatformAdmin:false};
    state.data.users=[{id:1,loginName:'owner',gitTokenConfigured:true}];
    state.mcpMarket=[{id:'market-gf',name:'Gongfeng',aliases:['gongfeng'],url:'https://gongfeng'}];
    state.data.mcpServers=[{id:'gf',scope:'project',projectId:'project-1',marketId:'market-gf'}];
    state.data.workflows=[${JSON.stringify(generic)}];
    state.sopScope='generic';
    openGenericTemplateModal('generic-delivery');
  `);
  const rows = app.document.querySelector('#genericTemplateRequirements').textContent;
  assert.match(rows, /工蜂 Git Token.*已配置/s);
  assert.match(rows, /TAPD MCP.*未配置/s);
  assert.match(rows, /工蜂 MCP.*已配置/s);
  const sopHTML = app.evaluate(`({
    card:genericSOPCard(state.data.workflows[0]),
    chips:genericRequirementChips(state.data.workflows[0]),
    gongfeng:gongfengMCPInstalled(),
    tapd:tapdMCPInstalled(),
    node:workflowNodeName(state.data.workflows[0],'mr')
  })`);
  assert.match(sopHTML.card, /平台维护/);
  assert.match(sopHTML.chips, /sop-badge tone-success[^>]*>.*工蜂 Git Token/);
  assert.match(sopHTML.chips, /sop-badge tone-warn[^>]*>.*TAPD MCP/);
  assert.equal(sopHTML.gongfeng, true);
  assert.equal(sopHTML.tapd, false);
  assert.equal(sopHTML.node, '发起 MR');
  app.evaluate(`openGenericRequirementTarget('gongfeng-mcp')`);
  assert.equal(app.evaluate(`state.mcpQuery`), 'gongfeng');
  app.evaluate(`openGenericTemplateModal('generic-delivery')`);
  await app.evaluate(`adoptGenericTemplate()`);
  await settle(8);
  const clone = app.router.calls.find(c => c.path === '/api/workflows/generic-delivery/clone');
  assert.equal(clone.method, 'POST');
  assert.equal(clone.body.projectId, 'project-1');
  assert.equal(app.evaluate(`state.data.workflows[0].generic`), false);
});

test('runner cards expose runtime controls and persist limits, defaults, and auth mode', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const runner = {
    id: 'runner-local', name: 'Laptop', ownerUserId: 1, status: 'online', os: 'darwin', arch: 'arm64',
    runLimit: 2, lastSeenAt: new Date().toISOString(),
    runtimes: [{ provider: 'codex', status: 'online', authMode: 'local', version: '1.0' }],
  };
  app.router.put('/api/runners/runner-local/run-limit', { ...runner, runLimit: 5 });
  app.router.put('/api/runners/default', { message: 'default updated' });
  app.router.put('/api/runners/runtimes', { ok: true });
  app.router.delete('/api/runners/runner-local', { ok: true });
  app.router.post('/api/runners/bind-codes', { code: 'BIND123', expiresAt: '2099-01-01T00:00:00Z' });
  app.router.get('/api/runners', [runner]);
  app.router.get('/api/runners/default', { runnerId: '', cloudPool: { configured: true, online: 2 } });
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    state.data.users=[{id:1,loginName:'owner',codexApiKeyConfigured:true}];
    state.runners=[${JSON.stringify(runner)}];state.defaultRunnerId='';state.cloudPool={configured:true,online:2};
    renderRunnersView();
  `);
  const list = app.document.querySelector('#runnersList');
  assert.match(list.textContent, /Laptop/);
  const limit = list.querySelector('[data-runner-run-limit]');
  assert.equal(limit.value, '2');
  assert.match(app.evaluate(`runnerConcurrencyLabel(state.runners[0])`), /最多同时运行 2 条 SOP/);
  limit.value = '5';
  limit.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await settle(6);
  assert.deepEqual(app.router.calls.find(c => c.path.endsWith('/run-limit')).body, { runLimit: 5 });
  await app.evaluate(`setDefaultRunner('runner-local');setRunnerRuntimeAuth('runner-local','codex','personal')`);
  await settle(6);
  assert.ok(app.router.calls.some(c => c.path === '/api/runners/default' && c.body.runnerId === 'runner-local'));
  assert.ok(app.router.calls.some(c => c.path === '/api/runners/runtimes' && c.body.authMode === 'personal'));
  await app.evaluate(`toggleRunnerRuntime('runner-local','codex',false)`);
  assert.ok(app.router.calls.some(c => c.path === '/api/runners/runtimes' && c.body.status === 'disabled'));
  app.evaluate(`window.__removeRunner=removeRunner('runner-local')`);
  await settle(2);
  app.document.querySelector('#confirmOkBtn').click();
  await app.evaluate(`window.__removeRunner`);
  assert.ok(app.router.calls.some(c => c.method === 'DELETE' && c.path === '/api/runners/runner-local'));
});

test('runner onboarding defaults to Unix even on Windows and preserves manual platform selection', async (t) => {
  /** 独立接入页面与请求夹具。 */
  const app = await loadApp();
  t.after(app.close);
  /** 记录复制内容以校验平台命令。 */
  const copied = [];
  Object.defineProperty(app.window.navigator, 'userAgentData', { configurable: true, value: { platform: 'Windows' } });
  app.window.navigator.clipboard.writeText = async (text) => { copied.push(text); };
  app.evaluate(`runnerInstallPlatform='';state.runnerBindCode={code:'bind-ui-fixture',expiresAt:'2099-01-01T00:00:00Z'};renderRunnersView()`);
  /** Windows 接入标签。 */
  const windowsTab = app.document.querySelector('#runnerPlatformWindows');
  /** macOS 与 Linux 接入标签。 */
  const unixTab = app.document.querySelector('#runnerPlatformUnix');
  /** 当前系统的接入命令展示节点。 */
  const command = app.document.querySelector('#bindCliCommand');
  assert.equal(unixTab.getAttribute('aria-selected'), 'true');
  assert.equal(windowsTab.getAttribute('aria-selected'), 'false');
  assert.equal(app.document.querySelector('#runnerInstallPlatform [role="tab"]').id, unixTab.id);
  assert.equal(command.dataset.shell, 'bash');
  windowsTab.click();
  assert.equal(windowsTab.getAttribute('aria-selected'), 'true');
  assert.equal(unixTab.getAttribute('aria-selected'), 'false');
  assert.match(command.textContent, /Invoke-WebRequest -UseBasicParsing/);
  assert.match(command.textContent, /'http:\/\/127\.0\.0\.1\/runners\/install\.ps1'/);
  assert.match(command.textContent, /powershell -NoProfile -ExecutionPolicy Bypass -File \$lfInstaller -ServerUrl 'http:\/\/127\.0\.0\.1' -BindCode 'bind-ui-fixture'/);
  assert.doesNotMatch(command.textContent, /\|\||bash -s|Invoke-Expression/);
  assert.match(app.document.querySelector('#runnerShellHint').textContent, /PowerShell 5\.1.*PowerShell 7/);
  assert.match(app.document.querySelector('#runnerMaintenanceLabel').textContent, /停止.*保留数据/);
  assert.match(app.document.querySelector('#runnerMaintenanceCommand').textContent, /-Stop$/);
  assert.doesNotMatch(app.document.querySelector('#runnerMaintenanceCommand').textContent, /BindCode|--uninstall/);
  assert.equal(app.document.querySelector('#runnerRestartControls').hidden, false);
  assert.doesNotMatch(app.document.querySelector('#runnerRestartCommand').textContent, /BindCode|-Stop|--uninstall/);
  app.document.querySelector('#copyBindCommand').click();
  await settle();
  assert.equal(copied[0], command.textContent);

  unixTab.click();
  assert.equal(windowsTab.getAttribute('aria-selected'), 'false');
  assert.equal(unixTab.getAttribute('aria-selected'), 'true');
  assert.match(command.textContent, /curl -fsSL --connect-timeout 5 'https:\/\/127\.0\.0\.1\/runners\/install\.sh' \|\| curl -fsSL 'http:\/\/127\.0\.0\.1\/runners\/install\.sh'/);
  assert.match(command.textContent, /\| bash -s -- --server '127\.0\.0\.1' --token 'bind-ui-fixture' --service/);
  assert.match(app.document.querySelector('#runnerMaintenanceCommand').textContent, /--uninstall$/);
  assert.equal(app.document.querySelector('#runnerRestartControls').hidden, true);
  app.evaluate('renderRunnersView()');
  assert.equal(unixTab.getAttribute('aria-selected'), 'true');
  assert.equal(app.document.querySelector('#runnerInstallPanel').getAttribute('aria-labelledby'), unixTab.id);
  app.document.querySelector('#copyBindCommand').click();
  await settle();
  assert.equal(copied[1], command.textContent);
  windowsTab.click();
  app.document.querySelector('#copyUninstallCommand').click();
  await settle();
  assert.match(copied[2], /-Stop$/);
  app.document.querySelector('#copyRunnerRestartCommand').click();
  await settle();
  assert.match(copied[3], /-ServerUrl 'http:\/\/127\.0\.0\.1'$/);
});

test('runner platform tabs support keyboard selection and preserve focus across refreshes', async (t) => {
  /** 包含真实 SlidingTabs 控制器的隔离页面。 */
  const app = await loadApp({hash:'/runners'});
  t.after(app.close);
  /** 可见标签的布局，补足 jsdom 不提供的几何信息。 */
  const tabs = [...app.document.querySelectorAll('[data-runner-platform]')];
  for (const [index, tab] of tabs.entries()) {
    tab.getClientRects = () => [{left:index * 160, top:0, width:160, height:28}];
    tab.getBoundingClientRect = () => ({left:index * 160, top:0, width:160, height:28});
  }
  app.evaluate(`runnerInstallPlatform='unix';state.runnerBindCode={code:'keyboard-fixture',expiresAt:'2099-01-01T00:00:00Z'};renderRunnersView()`);
  tabs[0].focus();
  assert.equal(tabs[0].tabIndex, 0);
  assert.equal(tabs[1].tabIndex, -1);
  for (const [key, expected] of [['ArrowRight',1],['ArrowRight',0],['End',1],['Home',0],['ArrowLeft',1],['Home',0]]) {
    app.document.activeElement.dispatchEvent(new app.window.KeyboardEvent('keydown', {key,bubbles:true,cancelable:true}));
    await settle();
    assert.equal(app.document.activeElement, tabs[expected]);
    assert.equal(tabs[expected].getAttribute('aria-selected'), 'true');
    assert.equal(tabs[expected].tabIndex, 0);
    assert.equal(tabs[1 - expected].tabIndex, -1);
    assert.equal(app.document.querySelector('#runnerInstallPanel').getAttribute('aria-labelledby'), tabs[expected].id);
    assert.equal(app.document.querySelector('#bindCliCommand').dataset.shell, expected === 1 ? 'powershell' : 'bash');
  }
  app.evaluate('renderRunnersView()');
  assert.equal(app.document.activeElement, tabs[0]);
  assert.equal(tabs[0].getAttribute('aria-selected'), 'true');
  assert.equal(app.document.querySelectorAll('#runnerInstallPlatform [data-sliding-indicator]').length, 1);
  assert.equal(app.document.querySelector('#runnerRestartControls').hidden, true);
});

test('runner onboarding rejects absent or expired binding codes and recovers after refresh', async (t) => {
  /** 独立接入页面与请求夹具。 */
  const app = await loadApp();
  t.after(app.close);
  /** 记录复制内容以校验平台命令。 */
  const copied = [];
  app.window.navigator.clipboard.writeText = async (text) => { copied.push(text); };
  /** 受绑定码有效期保护的复制入口。 */
  const copyButton = app.document.querySelector('#copyBindCommand');
  for (const binding of [null, {code:'',expiresAt:'2099-01-01T00:00:00Z'}, {code:'old-fixture',expiresAt:'2000-01-01T00:00:00Z'}, {code:'bad-date',expiresAt:'invalid'}]) {
    app.evaluate(`state.runnerBindCode=${JSON.stringify(binding)};renderRunnersView()`);
    assert.equal(copyButton.disabled, true);
    copyButton.click();
    assert.equal(copied.length, 0);
    assert.doesNotMatch(app.document.querySelector('#bindCliCommand').textContent, /--token|-BindCode/);
  }
  app.evaluate(`state.runnerBindCode={code:'expires-while-open',expiresAt:'2099-01-01T00:00:00Z'};renderRunnersView();state.runnerBindCode.expiresAt='2000-01-01T00:00:00Z'`);
  assert.equal(copyButton.disabled, false);
  copyButton.click();
  assert.equal(copyButton.disabled, true);
  assert.equal(copied.length, 0);

  app.router.post('/api/runners/bind-codes', {code:'renewed-fixture',expiresAt:'2099-01-01T00:00:00Z'});
  await app.evaluate('refreshRunners(true)');
  assert.equal(copyButton.disabled, false);
  copyButton.click();
  await settle();
  assert.match(copied[0], /renewed-fixture/);
});

test('runner command builders quote shell-sensitive values as literal arguments', async (t) => {
  /** 独立接入页面与请求夹具。 */
  const app = await loadApp();
  t.after(app.close);
  /** 包含 Shell 元字符的隔离绑定码。 */
  const token = "fixture'$(echo unsafe);&";
  app.evaluate(`state.runnerBindCode={code:${JSON.stringify(token)},expiresAt:'2099-01-01T00:00:00Z'}`);
  /** PowerShell 命令构建结果。 */
  const windows = app.evaluate('runnerWindowsCommand()');
  /** Bash 命令构建结果。 */
  const unix = app.evaluate('runnerUnixCommand()');
  assert.ok(windows.endsWith("-BindCode 'fixture''$(echo unsafe);&'"));
  assert.ok(unix.endsWith("--token 'fixture'\\''$(echo unsafe);&' --service"));
  assert.equal(app.evaluate(`runnerBashQuote("abc'def")`), "'abc'\\''def'");
});
