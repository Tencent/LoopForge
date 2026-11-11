package web

import (
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestCredentialBrandAssetsEmbedded(t *testing.T) {
	for _, name := range []string{"gongfeng.svg", "codebuddy.svg", "codex.svg", "claude.svg"} {
		data, err := fs.ReadFile(Files, "images/credentials/"+name)
		if err != nil {
			t.Fatalf("credential logo %s is not embedded: %v", name, err)
		}
		if !strings.Contains(string(data), "<svg") {
			t.Fatalf("credential logo %s is not SVG", name)
		}
	}
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://github.com/settings/tokens", "https://www.codebuddy.ai/profile/keys", "https://copilot.tencent.com/profile/", "credential-description", "git-token-verify"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("credential UI is missing %q", want)
		}
	}
}

// A parse error anywhere in the app bundle blanks the whole SPA, and `go test` alone
// cannot catch it, so gate the bundle on node's parser when node is available.
func TestClarifyModelPickerAndMCPPrompt(t *testing.T) {
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"agentflow.clarifyModel",
		"selectedClarifyModel",
		"data-clarify-model",
		"model:selectedClarifyModel()",
	} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("clarify model picker is missing %q", want)
		}
	}
}

func TestAppJSParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping app.js parse check")
	}
	for _, name := range appBundleFiles(t) {
		out, err := exec.Command(node, "--check", name).CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed to parse: %v\n%s", name, err, out)
		}
	}
}

func TestIssueRunRecoveryActions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
const state = {data:{workflows:[]}};
const escapeHTML = value => String(value);
const runContext = () => ({issue:{}});
const gongfengIssueClosed = () => false;
const issueExecution = issue => ({run:issue.run});
const issueCurrentStage = issue => ({key:issue.stage});
const issueRunHealth = () => 'attention';
const issueManuallyClosed = () => false;
const issueTrackerClosed = () => false;
const deliveryLocked = () => false;
const issueClarifying = () => false;
const clarificationNeedsConfirm = () => false;
const conversationWorkspaceIssue = () => false;
const isScriptWorkflowNode = node => node?.type==='script'||node?.type==='command';
const cachedRun = () => null;
eval(code.slice(code.indexOf('function canRerunFromNode('), code.indexOf('function afRerunFromNode(')));
eval(code.slice(code.indexOf('function issueActionButtons('), code.indexOf('function setIssueRunFilter(')));
eval(code.slice(code.indexOf('function refreshRunSOPButtonHTML('), code.indexOf('function pausedRunPanelHTML(')));
const run = {status:'failed', workflowId:'wf', workflowSnapshot:{id:'wf',nodes:[{id:'a'}]}, nodes:{a:{status:'failed'}}};
for (const status of ['waiting','failed','cancelled','succeeded']) {
  assert.equal(canRerunFromNode({...run,status}), true, status);
}
assert.equal(canRerunFromNode({...run,status:'running'}), false);
assert.equal(canRerunFromNode({...run,status:'waiting',nodes:{a:{status:'running'}}}), false);
assert.equal(runNodeAllowsContext({id:'a',type:'script'}), false);
assert.equal(runNodeAllowsContext({id:'a',type:'command'}), false);
assert.equal(runNodeAllowsContext({id:'a',type:'agent'}), true);
assert.equal(runNodeAllowsContext({id:'a'}), false);
assert.equal(runNodeChoices({...run,workflowSnapshot:{id:'wf',nodes:[{id:'a',type:'command'}]}})[0].type, 'command');
const chained={...run,workflowSnapshot:{id:'wf',nodes:[{id:'first',type:'agent'},{id:'second',type:'agent'}],edges:[{source:'first',target:'second'}]},nodes:{first:{status:'failed'},second:{status:'pending'}}};
assert.deepEqual(runNodeChoices(chained).map(n=>n.id),['first']);
assert.equal(canRerunFromHere(chained,'first'),true);
assert.equal(canRerunFromHere(chained,'second'),false);
assert.deepEqual(runNodeChoices({...chained,status:'waiting',nodes:{first:{status:'waiting_approval'},second:{status:'pending'}}}).map(n=>n.id),['first']);
assert.equal(canRerunFromHere({...chained,status:'waiting',nodes:{first:{status:'waiting_approval'},second:{status:'pending'}}},'second'),false);
assert.deepEqual(runNodeChoices({...chained,nodes:{first:{status:'succeeded'},second:{status:'failed'}}}).map(n=>n.id),['first','second']);
assert.equal(canRerunFromHere({...chained,nodes:{first:{status:'succeeded'},second:{status:'failed'}}},'second'),true);
const failed = issueActionButtons({id:'issue-1',run,stage:'failed'});
assert.equal((failed.match(/class="issue-act /g)||[]).length, 1);
assert.ok(failed.includes('处理失败'));
assert.ok(!failed.includes('data-resume-run') && !failed.includes('data-pass-node'));
const waiting = issueActionButtons({id:'issue-1',run:{...run,status:'waiting'},stage:'waiting'});
assert.ok(waiting.includes('去确认'));
assert.equal((waiting.match(/class="issue-act /g)||[]).length, 1);
state.data.workflows = [{id:'wf',version:2,revision:1}];
assert.equal(refreshRunSOPButtonHTML({...run,workflowSnapshot:{}}), '');
assert.ok(refreshRunSOPButtonHTML({...run,workflowSnapshot:{id:'wf',version:1,revision:1}}).includes('更新为最新 SOP'));
assert.equal(refreshRunSOPButtonHTML({...run,workflowSnapshot:{id:'wf',version:2,revision:1}}), '');
assert.ok(!code.includes('function issueMoreActionsHTML('));
assert.ok(code.includes('data-rerun-from-here'));
assert.ok(!code.includes('data-rerun-from-node-issue'));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("issue recovery controls failed: %v\n%s", err, out)
	}
}

func TestNodeShowsRoleValidation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping unified validation behavior check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
eval(code.slice(code.indexOf('function effectiveNodeArtifacts('), code.indexOf('function nodeGatePane(')));
const agent = {
  artifactOutputs: [{name:'report',path:'role.md',required:true},{name:'evidence',path:'evidence.md'}],
  verification: [{id:'shared',name:'role shared'},{id:'legacy',name:'legacy submit',mode:'submit'},{id:'role-only',name:'role only'}]
};
const workflowNode = {artifactOutputs: [{name:'report',path:'node.md',required:true}]};
assert.deepEqual(effectiveNodeArtifacts(workflowNode, agent).map(x=>x.path), ['node.md','evidence.md']);
assert.deepEqual(roleGates(agent).map(x=>x.name), ['role shared','role only']);
assert.ok(code.includes('Agent 节点先提交结构化终态，随后校验产物，最后执行可选门禁命令'));
assert.ok(code.includes('pass | rework | fail | human'));
assert.ok(!code.includes('node?.verification'));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("unified validation behavior check failed: %v\n%s", err, out)
	}
}

func TestRuleGlobalSOPPicker(t *testing.T) {
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile("platform.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"作为全局规则",
		"ruleGlobalFields",
		"bindRuleGlobalPicker",
		"selectedRuleWorkflowIds",
		"该 SOP 的每个节点执行时都会带上这条规则",
		"workflowIds:global?selectedRuleWorkflowIds():[]",
		`class="rule-global-input" role="switch"`,
	} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("rule global picker is missing %q", want)
		}
	}
	for _, want := range []string{".rule-global-block", ".rule-global-sops"} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("rule global picker style is missing %q", want)
		}
	}
}

func TestAssetSaveKeepsSelectedIdentity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping asset save behavior check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
eval(code.slice(code.indexOf('function resolveAssetSaveIdentity('), code.indexOf('function renderAssetForm(')));
const items = [{id:'existing',name:'Existing'}];
const edit = resolveAssetSaveIdentity(items, 'existing', 'accidental-new-id');
assert.equal(edit.id, 'existing');
assert.equal(edit.existing, items[0]);
assert.equal(edit.created, false);
assert.match(resolveAssetSaveIdentity(items, '__new__', 'existing').error, /已存在/);
assert.deepEqual(resolveAssetSaveIdentity(items, '__new__', 'new-id'), {id:'new-id',existing:null,created:true});
assert.ok(code.includes('readonly aria-readonly="true"'));
assert.ok(code.includes("identity.created?'资产已创建':'资产修改已保存'"));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("asset save behavior check failed: %v\n%s", err, out)
	}
}

func TestMCPLogoHasBrokenImageFallback(t *testing.T) {
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mcp-logo-fallback", "bindMCPLogoFallbacks", "naturalWidth===0", "logo-load-failed"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("MCP logo fallback is missing %q", want)
		}
	}
	css, err := os.ReadFile("platform.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".mcp-card-logo .mcp-logo-fallback{display:inline", ".mcp-card-logo img+.mcp-logo-fallback{display:none"} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("MCP initial avatar style is missing %q", want)
		}
	}
}

func TestMCPJSONImportParser(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping mcp.json import parser check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
eval(code.slice(code.indexOf('function mcpImportPlainObject('), code.indexOf('function mcpApplyImportedServer(')));
const stdio = mcpImportCandidates({mcpServers:{playwright:{command:'npx',args:['@playwright/mcp@latest'],env:{TOKEN:'secret'},cwd:'tools',pathEntries:['custom/bin']}}});
assert.equal(stdio.length, 1);
assert.deepEqual(stdio[0], {id:'playwright',name:'playwright',description:'',transport:'stdio',timeoutSec:60,url:'',authType:'',headers:{},command:'npx',args:['@playwright/mcp@latest'],installScript:'',workingDirectory:'tools',pathEntries:['custom/bin'],environment:{TOKEN:'secret'}});
const http = mcpImportCandidates({servers:{github:{type:'streamable-http',url:'https://api.githubcopilot.com/mcp/',headers:{Authorization:'Bearer token'},timeoutSec:30}}});
assert.equal(http[0].transport, 'http');
assert.equal(http[0].url, 'https://api.githubcopilot.com/mcp/');
assert.deepEqual(http[0].headers, {Authorization:'Bearer token'});
assert.equal(http[0].timeoutSec, 30);
const direct = mcpImportCandidates({id:'nested',name:'Nested',transport:'stdio',stdio:{command:'node',args:['server.js'],environment:{PORT:3000}}});
assert.equal(direct[0].command, 'node');
assert.deepEqual(direct[0].environment, {PORT:'3000'});
const fragment = '"gateway": {\u00a0"url": "[http://mcp.example.com/open/mcp](http://mcp.example.com/open/mcp)", "headers": {"x-api-token": "secret"}}';
const pasted = mcpImportCandidates(mcpParseJSONText(fragment));
assert.equal(pasted[0].id, 'gateway');
assert.equal(pasted[0].transport, 'http');
assert.equal(pasted[0].url, 'http://mcp.example.com/open/mcp');
assert.deepEqual(pasted[0].headers, {'x-api-token':'secret'});
const ticks = String.fromCharCode(96).repeat(3);
assert.equal(mcpImportCandidates(mcpParseJSONText(ticks+'json\n{"mcpServers":{"demo":{"command":"node"}}}\n'+ticks))[0].command, 'node');
assert.equal(mcpImportCandidates({mcpServers:{one:{command:'a'},two:{url:'https://example.com/mcp'}}}).length, 2);
assert.throws(()=>mcpImportCandidates({mcpServers:{bad:{command:'node',args:'server.js'}}}), /args 必须是数组/);
assert.throws(()=>mcpImportCandidates({mcpServers:{bad:{command:'node',pathEntries:'bin'}}}), /pathEntries 必须是 JSON 数组/);
assert.throws(()=>mcpImportCandidates({mcpServers:{bad:{type:'websocket',url:'wss:\/\/example.com'}}}), /不支持的 MCP transport/);
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("mcp.json import parser check failed: %v\n%s", err, out)
	}
}

func TestMCPJSONImportIsCollapsedByDefault(t *testing.T) {
	index, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)
	if !strings.Contains(html, `<details id="mcpJsonImport" class="mcp-json-import"><summary>从 mcp.json 粘贴导入</summary>`) {
		t.Fatal("mcp.json paste importer must use a collapsed details entry")
	}
	if strings.Contains(html, `<details id="mcpJsonImport" class="mcp-json-import" open>`) {
		t.Fatal("mcp.json paste importer must be collapsed by default")
	}
}

func TestMCPUploadPathHelpIsPrepared(t *testing.T) {
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"prepareMCPFormHints", "文件路径相对于上传内容根目录", "上传文件时默认以内容根目录启动", "通常留空；例如 packages/my-mcp", "额外 PATH 目录", "常见目录已由平台自动加入 PATH"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("MCP upload path help is missing %q", want)
		}
	}
}

func TestMCPSetupInstructionsAreEmbedded(t *testing.T) {
	setup, err := os.ReadFile("mcp-setup.txt")
	if err != nil {
		t.Fatal(err)
	}
	text := string(setup)
	for _, want := range []string{"Cursor", "Claude Code", "CodeBuddy", "Codex", "API_TOKEN", "ENDPOINT", "tools/list", "失败排查", "汇报", "时间戳的备份"} {
		if !strings.Contains(text, want) {
			t.Fatalf("MCP setup instructions are missing %q", want)
		}
	}
	if strings.Contains(text, "AgentFlow") {
		t.Fatal("MCP setup instructions should name the product LoopForge, not AgentFlow")
	}
	embedSource, err := os.ReadFile("embed.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(embedSource), "mcp-setup.txt") {
		t.Fatal("mcp-setup.txt is not embedded in the web bundle")
	}
}

func TestAccessGuideIsTheSingleOnboardingEntry(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	if strings.Contains(page, `id="accessView"`) || strings.Contains(page, `data-view="access"`) {
		t.Fatal("legacy access page or navigation entry is still present")
	}
	for _, want := range []string{`id="guideView"`, `<h1>访问密钥</h1>`, `id="accessBody"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("token management page is missing %q", want)
		}
	}
	if strings.Contains(page, `data-access-tab=`) {
		t.Fatal("接入指南 collapsed into 访问密钥; MCP/能力/API tabs must not come back")
	}
	if strings.Contains(page, `data-access-tab="stdio"`) {
		t.Fatal("stdio bridge must not be advertised in the onboarding UI")
	}
	js, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	code := string(js)
	if !strings.Contains(code, `if(hash==='access')return 'guide'`) || !strings.Contains(code, `if(name==='access')name='guide'`) {
		t.Fatal("legacy #/access links must redirect to #/guide")
	}
}

func TestRunnerBindCommandBootstrapsWithProtocolFallback(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{"https://", "http://", "bindHttpsOrigin", "bindHttpOrigin", "--server", "bindServerHost"} {
		if !strings.Contains(page, want) {
			t.Fatalf("runner bind command is missing %q", want)
		}
	}
}

func TestRenderUserBadgeScope(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping check")
	}
	script := `
const fs = require("fs");
const appCode = fs.readFileSync("` + appBundlePath(t) + `", "utf8");
global.window = { location: { href: "http://loopforge.example.com/", pathname: "/", search: "", hash: "", hostname: "loopforge.example.com" } };
global.sessionStorage = { _s: {}, getItem(k){return this._s[k]||null;}, setItem(k,v){this._s[k]=String(v);}, removeItem(k){delete this._s[k];} };
const makeEl = () => ({
  classList: { add(){}, remove(){} },
  title: "",
  textContent: "",
  innerHTML: "",
  onclick: null,
  querySelector(){ return makeEl(); },
  querySelectorAll(){ return []; }
});
const elements = {};
global.document = {
  querySelector(sel) {
    if (!elements[sel]) elements[sel] = makeEl();
    return elements[sel];
  },
  querySelectorAll() { return []; }
};
global.$ = s => global.document.querySelector(s);
global.escapeHTML = s => String(s);
global.state = { currentUser: { isGuest: true }, loginUrl: "" };
const fnStart = appCode.indexOf("function renderUserProfilePopover()");
const fnEnd = appCode.indexOf("function renderDashboard()");
eval(appCode.slice(fnStart, fnEnd));

// Guest mode
renderUserBadge();
sessionStorage.setItem("agentflow_auth_err", "exchange_failed");
renderUserBadge();

// Authenticated mode
state.currentUser = { isGuest: false, chineseName: "测试员工", loginName: "testuser", staffId: 1001, deptName: "技术架构部" };
renderUserBadge();
`
	cmd := exec.Command(node, "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("renderUserBadge failed: %v\n%s", err, out)
	}
}

func TestLocalhostDoesNotAutoSSORedirect(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping check")
	}
	script := `
const fs = require("fs");
const appCode = fs.readFileSync("` + appBundlePath(t) + `", "utf8");
const start = appCode.indexOf("function isLoopbackHost(");
const end = appCode.indexOf("async function boot()");
if (start < 0 || end < 0) process.exit(2);
eval(appCode.slice(start, end));
const loginUrl = "https://sso.example.com/signin";
const cases = [
  [{isGuest:true, hostname:"localhost", loginUrl}, false],
  [{isGuest:true, hostname:"127.0.0.1", loginUrl}, false],
  [{isGuest:true, hostname:"[::1]", loginUrl}, false],
  [{isGuest:true, hostname:"loopforge.example.com", loginUrl}, false],
  [{isGuest:true, hostname:"loopforge.example.com", loginUrl}, false],
  [{isGuest:true, hostname:"loopforge.example.com", loginUrl, bypassAutoAuth:true}, false],
  [{isGuest:true, hostname:"loopforge.example.com", loginUrl, authErr:"exchange_failed"}, false],
  [{isGuest:false, hostname:"loopforge.example.com", loginUrl}, false],
];
for (const [input, want] of cases) {
  const got = shouldAutoSSORedirect(input);
  if (got !== want) {
    console.error(JSON.stringify(input), "got", got, "want", want);
    process.exit(1);
  }
}
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("localhost SSO redirect check failed: %v\n%s", err, out)
	}
}

func TestCredentialWizardCSSKeepsToastsAnimating(t *testing.T) {
	cssBytes, err := os.ReadFile("platform.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	if strings.Contains(css, "body.credential-wizard-open") {
		t.Fatal("wizard pause must not hang off body: it restyles the whole page (and freezes toasts) on open")
	}
	if !strings.Contains(css, ".credential-wizard-open .hui-lamp::after,.credential-wizard-open .credential-spin{animation-play-state:paused}") {
		t.Fatal("wizard should pause only the looping credential animations under #personalSettingsContent")
	}
	code, err := os.ReadFile("app/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(code), "$('#personalSettingsContent')?.classList.add('credential-wizard-open')") {
		t.Fatal("openCredentialWizard should put the pause class on #personalSettingsContent")
	}
	parts := strings.SplitN(css, ".credential-scrim::before{", 2)
	if len(parts) < 2 {
		t.Fatal("missing .credential-scrim::before rule")
	}
	before := strings.SplitN(parts[1], "}", 2)[0]
	if strings.Contains(before, "transition") {
		t.Fatal(".credential-scrim::before should not fade; a full-viewport opacity transition drops the entrance frame rate")
	}
}

func TestCredentialListAndWizard(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping credential UI check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
const user = {id: 1, gitTokenConfigured: true, codexApiKeyConfigured: true, codexBaseUrl: 'https://proxy.example.com/v1'};
const state = {currentUser: {id: 1}, data: {users: [user]}};
const userById = id => state.data.users.find(u => u.id === id);
const escapeHTML = v => String(v), toast = () => {};
const calls = [];
const api = async (path, options = {}) => {
  calls.push({path, method: options.method || 'GET', body: options.body && JSON.parse(options.body)});
  return path.endsWith('/credentials') ? {...user, ...state.nextUser} : {valid: false, error: '令牌无效或已过期'};
};
const $ = () => null, setTimeout = () => {}, location = {origin: 'http://localhost'};
const document = {activeElement: null, querySelectorAll: () => []};
eval(code.slice(code.indexOf('const CREDENTIAL_GROUPS='), code.indexOf('// 谁能改通用模板')) +
  ';globalThis.cred={CREDENTIAL_KINDS,CREDENTIAL_GROUPS,kind:credentialKind,get wizard(){return credentialWizard},set wizard(v){credentialWizard=v}};');
const count = (html, needle) => html.split(needle).length - 1;

// Two groups list all four kinds in a fixed order.
assert.deepEqual(cred.CREDENTIAL_GROUPS.map(g => g.id), ['platform', 'model']);
assert.deepEqual(cred.CREDENTIAL_KINDS.filter(k => k.group === 'platform').map(k => k.key), ['gitToken']);
assert.deepEqual(cred.CREDENTIAL_KINDS.filter(k => k.group === 'model').map(k => k.key), ['codeBuddyApiKey', 'codexApiKey', 'claudeCodeApiKey', 'agentCli.opencode', 'agentCli.cursor', 'agentCli.agy', 'agentCli.kimi', 'agentCli.pi', 'agentCli.dsh', 'agentCli.zcode']);

// Progress, next-step hint, filter counts and per-group counts.
const html = credentialPanelHTML(user);
assert.ok(html.includes('<b>2</b> / 11 已配置'));
assert.ok(html.includes('data-credential-setup="codeBuddyApiKey"'), 'next step follows Git → CodeBuddy');
assert.ok(html.includes('1 / 1') && html.includes('1 / 10'));
assert.ok(html.includes('已配置<span>2</span>') && html.includes('未配置<span>9</span>'));
assert.equal(count(html, 'data-credential-row='), 11);
assert.ok(!html.includes('credential-grid') && !html.includes('credential-savebar') && !html.includes('userCredentialForm'));
// Unconfigured rows only offer 去配置; configured rows carry an inline editor with delete.
assert.equal(count(html, 'data-credential-act="setup"'), 9);
assert.equal(count(html, 'credential-editor-form'), 2);
assert.equal(count(html, 'data-credential-act="delete"'), 2);
for (const key of ['codeBuddyApiKey', 'claudeCodeApiKey']) {
  const row = credentialRowHTML(cred.kind(key), user);
  assert.ok(row.includes('去配置') && !row.includes('credential-editor-form') && !row.includes('is-configured'), key);
  // The whole row is a hoverable button that opens the wizard; it has nothing to expand.
  assert.ok(row.includes('class="credential-row-main credential-ink" role="button" tabindex="0">'), key);
}
assert.ok(credentialRowHTML(cred.kind('gitToken'), user).includes('class="credential-row-main credential-ink" role="button" tabindex="0" aria-expanded="false"'));
assert.ok(!html.includes('点击任意行配置或编辑') && !html.includes('点击已配置的行展开编辑'));
assert.ok(credentialRowHTML(cred.kind('gitToken'), user).includes('git-token-verify'), 'Git row keeps 重新校验');
assert.ok(credentialPanelHTML({...user, codeBuddyApiKeyConfigured: true}).includes('推荐凭证已全部就绪'));
assert.equal(credentialNextKind({}).key, 'gitToken');

// Each kind saves and deletes only its own fields.
assert.deepEqual(credentialSavePayload(cred.kind('gitToken'), {secret: ' ghp_1 '}), {gitToken: 'ghp_1'});
assert.deepEqual(credentialSavePayload(cred.kind('codeBuddyApiKey'), {secret: '', extra: 'ioa'}), {codeBuddyApiKey: '', codeBuddyInternetEnvironment: 'ioa'});
assert.deepEqual(credentialSavePayload(cred.kind('codeBuddyApiKey'), {secret: '', extra: ''}), {codeBuddyApiKey: '', codeBuddyInternetEnvironment: ''});
assert.deepEqual(credentialSavePayload(cred.kind('codexApiKey'), {secret: 'sk-1'}), {codexApiKey: 'sk-1'});
assert.deepEqual(credentialSavePayload(cred.kind('claudeCodeApiKey'), {secret: '', extra: '', models: [{id: ' hy4 ', name: '混元 4'}, {id: 'glm-5', name: 'glm-5'}]}), {claudeCodeApiKey: '', claudeCodeBaseUrl: '', claudeCodeModels: [{id: 'hy4', name: '混元 4'}, {id: 'glm-5'}]});
assert.deepEqual(credentialSavePayload(cred.kind('codexApiKey'), {secret: '', extra: '', models: []}), {codexApiKey: '', codexBaseUrl: '', codexModels: []}, '恢复默认 sends an empty catalog');
assert.deepEqual(credentialSavePayload(cred.kind('agentCli.opencode'), {secret: 'k', extra: 'https://gw.example/v1', protocol: 'messages', models: [{id: 'a', name: 'A'}]}), {agentCliCredentials: {opencode: {apiKey: 'k', baseUrl: 'https://gw.example/v1', protocol: 'messages', models: [{id: 'a', name: 'A'}]}}});
assert.equal(cred.kind('agentCli.cursor').models, undefined, 'Cursor stays on its official models');
assert.equal(cred.kind('agentCli.zcode').models.engine, 'zcode');
assert.ok(credentialSubline(cred.kind('codexApiKey'), {codexApiKeyConfigured: true, codexModels: [{id: 'hy4'}, {id: 'gpt-5.5'}]}).includes('2 个自定义模型'));
assert.deepEqual(credentialClearPayload(cred.kind('codexApiKey')), {codexApiKeyClear: true, codexBaseUrl: '', codexModels: []});
assert.deepEqual(credentialClearPayload(cred.kind('claudeCodeApiKey')), {claudeCodeApiKeyClear: true, claudeCodeBaseUrl: '', claudeCodeModels: []});
assert.deepEqual(credentialClearPayload(cred.kind('agentCli.opencode')), {agentCliCredentials: {opencode: {apiKeyClear: true, baseUrl: '', protocol: '', models: []}}});
assert.deepEqual(credentialClearPayload(cred.kind('agentCli.cursor')), {agentCliCredentials: {cursor: {apiKeyClear: true, baseUrl: '', protocol: ''}}});
const catalogUser = {codexApiKeyConfigured: true, codexBaseUrl: 'https://gw.example/v1', codexModels: [{id: 'hy4', name: '混元 4'}, {id: 'gpt-5.5'}]};
const catalogHTML = credentialCatalogHTML(cred.kind('codexApiKey'), catalogUser);
assert.equal(count(catalogHTML, 'data-catalog-row'), 2);
assert.ok(catalogHTML.indexOf('value="hy4"') < catalogHTML.indexOf('value="gpt-5.5"') && catalogHTML.includes('value="混元 4"'));
assert.ok(catalogHTML.includes('data-catalog-act="probe"') && catalogHTML.includes('data-catalog-act="add"') && catalogHTML.includes('data-catalog-grip'));
assert.ok(catalogHTML.includes('data-catalog-source="https://gw.example/v1"'));
assert.equal(credentialCatalogSource(' https://gw.example/v1/ '), credentialCatalogSource('https://gw.example/v1'));
assert.ok(catalogHTML.includes('data-catalog-empty hidden') && catalogHTML.includes('2 个模型') && catalogHTML.includes('data-catalog-body>') && !catalogHTML.includes('data-catalog-body hidden'));
const emptyCatalog = credentialCatalogHTML(cred.kind('claudeCodeApiKey'), {claudeCodeBaseUrl: 'https://gw.example'});
assert.ok(emptyCatalog.includes('跟随服务发布的模型') && emptyCatalog.includes('/v1/models') && emptyCatalog.includes('data-catalog-foot hidden') && emptyCatalog.includes('未自定义'));
const officialCatalog = credentialCatalogHTML(cred.kind('codexApiKey'), {codexModels: [{id: 'hy4'}]});
assert.ok(officialCatalog.includes('value="hy4"') && officialCatalog.includes('data-catalog-act="probe"') && officialCatalog.includes('改回使用自带的模型') && !officialCatalog.includes('data-catalog-body hidden'), 'an official key keeps its custom catalog editable');
const officialEmpty = credentialCatalogHTML(cred.kind('codexApiKey'), {});
assert.ok(officialEmpty.includes('使用 Codex 自带的模型') && officialEmpty.includes('data-catalog-act="add"') && !officialEmpty.includes('data-catalog-body hidden'));
const geminiCatalog = credentialCatalogHTML(cred.kind('agentCli.agy'), {agentCliCredentials: {agy: {baseUrl: 'https://gw.example/v1', protocol: 'gemini'}}});
assert.ok(geminiCatalog.includes('使用 Antigravity 自带的模型') && geminiCatalog.includes('data-catalog-act="probe"') && !geminiCatalog.includes('data-catalog-body hidden'), 'Gemini official API can still keep a custom catalog');
assert.ok(credentialEditorHTML(cred.kind('codexApiKey'), catalogUser).includes('data-credential-catalog') && credentialEditorHTML(cred.kind('codexApiKey'), catalogUser).includes('has-catalog'));
assert.ok(!credentialEditorHTML(cred.kind('agentCli.cursor'), {agentCliCredentials: {cursor: {apiKeyConfigured: true}}}).includes('data-credential-catalog'));
assert.equal(credentialCatalogKey([{id: 'a', name: 'a'}, {id: '', name: ''}]), credentialCatalogKey([{id: 'a'}]));
assert.equal(credentialCatalogError([{id: 'a'}, {id: 'b', name: 'B'}]), null);
assert.deepEqual(credentialCatalogError([{id: 'a'}, {id: '', name: 'x'}]), {index: 1, field: 'id', error: '模型 ID 不能为空'});
assert.ok(credentialCatalogError([{id: 'a b'}]).error.includes('空格'));
assert.ok(credentialCatalogError([{id: 'a'}, {id: 'a'}]).error.includes('重复'));
assert.equal(credentialCatalogError([{id: 'a', name: 'n'.repeat(65)}]).field, 'name');
assert.deepEqual(credentialClearPayload(cred.kind('codeBuddyApiKey')), {codeBuddyApiKeyClear: true});
assert.deepEqual(credentialClearPayload(cred.kind('gitToken')), {gitTokenClear: true});
assert.equal(credentialSecretError(cred.kind('gitToken'), 'github_pat_1'), '');
assert.ok(credentialExtraError(cred.kind('codexApiKey'), 'ftp://proxy'));
assert.equal(credentialExtraError(cred.kind('codexApiKey'), ''), '');

// CodeBuddy: the network environment is a fixed choice and the key link follows it.
// The CLI gets the stored text as-is, so only exact values are recognized; aliases only guess what an old value meant.
assert.deepEqual(['', ' internal ', 'ioa'].map(v => codeBuddyEnvironment(v)?.value), ['', 'internal', 'ioa']);
assert.deepEqual(['public', 'INTL', 'iOA', '国内站', 'china', 'cn', 'private-net'].map(v => codeBuddyEnvironment(v)), [null, null, null, null, null, null, null]);
assert.deepEqual(['', 'public', 'INTL', ' iOA ', 'internal', '国内站', 'china'].map(v => codeBuddyEnvironmentIntent(v)?.value), ['', '', '', 'ioa', 'internal', 'internal', 'internal']);
assert.equal(codeBuddyEnvironmentIntent('private-net'), null);
assert.deepEqual(['', 'internal', 'china', 'iOA', 'private-net'].map(codeBuddyEnvironmentLegacy), [false, false, true, true, true]);
assert.equal(codeBuddyKeyURL(''), 'https://www.codebuddy.ai/profile/keys');
assert.equal(codeBuddyKeyURL('internal'), 'https://copilot.tencent.com/profile/');
assert.equal(codeBuddyKeyURL('ioa'), 'https://tencent.sso.copilot.tencent.com/profile/keys');
assert.equal(codeBuddyKeyURL('typo'), 'https://www.codebuddy.ai/profile/keys');
const cbKind = cred.kind('codeBuddyApiKey');
assert.equal(credentialExtraValue(cbKind, 'iOA'), 'ioa');
assert.equal(credentialExtraValue(cbKind, 'private-net'), '', 'an unknown value shows as 国际站, which is what the CLI uses');
assert.equal(credentialExtraValue(cred.kind('codexApiKey'), 'https://p.example'), 'https://p.example');
const cbEditor = credentialEditorHTML(cbKind, {codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'internal'});
assert.ok(cbEditor.includes('<select name="extra">') && !cbEditor.includes('<input name="extra"'));
assert.ok(cbEditor.includes('data-original="internal"') && cbEditor.includes('<option value="internal" selected>'));
assert.ok(cbEditor.includes('href="https://copilot.tencent.com/profile/"') && cbEditor.includes('前往国内站获取 API Key'));
const legacyEditor = credentialEditorHTML(cbKind, {codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'private-net'});
assert.ok(legacyEditor.includes('data-original=""') && legacyEditor.includes('<option value="" selected>') && legacyEditor.includes('「private-net」无法识别'));
assert.ok(legacyEditor.includes('data-legacy="true"') && !legacyEditor.includes('data-credential-act="save" disabled'), 'a legacy value can be saved as 国际站 without retyping the key');
assert.ok(!cbEditor.includes('data-legacy') && cbEditor.includes('data-credential-act="save" disabled'));
// An old alias preselects what it meant and stays savable: the CLI would otherwise keep connecting to 国际站.
const chinaEditor = credentialEditorHTML(cbKind, {codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'china'});
assert.ok(chinaEditor.includes('data-original="internal"') && chinaEditor.includes('<option value="internal" selected>') && chinaEditor.includes('「china」无法识别'));
assert.ok(chinaEditor.includes('data-legacy="true"') && !chinaEditor.includes('data-credential-act="save" disabled'));
assert.ok(credentialSubline(cbKind, {codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'china'}).includes('「china」无法识别'));
assert.ok(credentialSubline(cbKind, {codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'ioa'}).includes('>iOA<'));
assert.ok(credentialSubline(cbKind, {codeBuddyApiKeyConfigured: true}).includes('>国际站<'));
assert.ok(credentialSubline(cbKind, {codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'private-net'}).includes('无法识别'));
// Third-party keys are flagged but never blocked.
assert.equal(credentialSecretWarning(cbKind, 'ck_abc'), '');
assert.match(credentialSecretWarning(cbKind, 'sk-abc'), /第三方/);
assert.match(credentialSecretWarning(cbKind, 'abc'), /ck_/);
assert.equal(credentialSecretWarning(cred.kind('codexApiKey'), 'sk-abc'), '');
assert.equal(credentialSecretError(cbKind, 'sk-abc'), '');

// Delete swaps the action bar for the confirm bar without sending anything.
const pane = () => ({classList: {on: new Set(), toggle(name, on) { on ? this.on.add(name) : this.on.delete(name) }}});
const foot = pane(), confirm = pane();
const row = {dataset: {credentialRow: 'codexApiKey'}, querySelector: s => s.includes('foot') ? foot : s.includes('confirm') ? confirm : null};
credentialRowAction('delete', row);
assert.ok(foot.classList.on.has('is-off') && !confirm.classList.on.has('is-off') && foot.inert === true);
credentialRowAction('undelete', row);
assert.ok(!foot.classList.on.has('is-off') && confirm.classList.on.has('is-off'));
assert.equal(calls.length, 0);

(async () => {
  await deleteCredential('codexApiKey');
  assert.deepEqual(calls.at(-1).body, {codexApiKeyClear: true, codexBaseUrl: '', codexModels: []});

  // 添加凭证 starts at step 1 with configured kinds disabled.
  cred.wizard = {step: 1, kind: null, direct: false, secret: '', extra: '', guide: {}, error: ''};
  const one = credentialWizardStepOneHTML(user);
  assert.ok(one.includes('data-wizard-kind="gitToken" disabled') && one.includes('data-wizard-kind="codexApiKey" disabled'));
  assert.ok(!one.includes('data-wizard-kind="codeBuddyApiKey" disabled'));
  assert.ok(credentialWizardFootHTML().includes('data-wizard-next disabled'));
  // Going back to step 1 and picking another kind drops what was typed for the previous one.
  cred.wizard = {step: 1, kind: 'codeBuddyApiKey', direct: false, secret: 'cb_CARRY', extra: '', guide: {0: true}, error: 'x'};
  selectCredentialWizardKind('claudeCodeApiKey');
  assert.deepEqual([cred.wizard.kind, cred.wizard.secret, cred.wizard.guide, cred.wizard.error], ['claudeCodeApiKey', '', {}, '']);
  cred.wizard.secret = 'sk-KEEP';
  selectCredentialWizardKind('claudeCodeApiKey');
  assert.equal(cred.wizard.secret, 'sk-KEEP', 're-selecting the same kind keeps the input');
  assert.ok(!credentialFieldHTML({label: 'k', name: 'secret', value: 'sk-LEAK'}).includes('sk-LEAK'), 'secrets are never written into markup');
  assert.ok(credentialFieldHTML({label: 'u', name: 'extra', type: 'url', value: 'https://p.example'}).includes('value="https://p.example"'));
  // 去配置 jumps straight to step 2 with no back button.
  cred.wizard = {step: 2, kind: 'codeBuddyApiKey', direct: true, secret: '', extra: 'ioa', guide: {}, error: ''};
  const cbStepTwo = credentialWizardStepTwoHTML(cred.kind('codeBuddyApiKey'));
  assert.ok(cbStepTwo.indexOf('<select name="extra">') < cbStepTwo.indexOf('name="secret"'), 'the network environment is picked before the key');
  assert.ok(cbStepTwo.includes('href="https://tencent.sso.copilot.tencent.com/profile/keys" target="_blank" rel="noopener noreferrer" data-wizard-link="1" data-environment-link>前往iOA获取 API Key'));
  assert.ok(!cbStepTwo.includes('https://www.codebuddy.ai/"'));
  cred.wizard.extra = '';
  assert.ok(credentialWizardStepTwoHTML(cred.kind('codeBuddyApiKey')).includes('credential-description'));
  assert.ok(!credentialWizardFootHTML().includes('data-wizard-back'));
  assert.ok(credentialWizardFootHTML().includes('data-wizard-next disabled'), 'an empty key blocks next');
  cred.wizard.direct = false;
  assert.ok(credentialWizardFootHTML().includes('data-wizard-back'));
  // Step 3 names the environment and repeats the third-party key warning without echoing the key.
  cred.wizard = {step: 3, kind: 'codeBuddyApiKey', direct: true, secret: 'sk-third', extra: 'internal', guide: {}, error: ''};
  const cbStepThree = credentialWizardStepThreeHTML(cred.kind('codeBuddyApiKey'));
  assert.ok(cbStepThree.includes('国内站 · copilot.tencent.com') && cbStepThree.includes('credential-vline is-warn') && !cbStepThree.includes('sk-third'));
  // Step 3 never echoes the secret.
  cred.wizard = {step: 3, kind: 'codeBuddyApiKey', direct: true, secret: 'cb_SECRET', extra: '', guide: {}, error: ''};
  assert.ok(!credentialWizardStepThreeHTML(cred.kind('codeBuddyApiKey')).includes('SECRET'));
  await advanceCredentialWizard();
  assert.deepEqual(calls.at(-1), {path: '/api/users/1/credentials', method: 'PUT', body: {codeBuddyApiKey: 'cb_SECRET', codeBuddyInternetEnvironment: ''}});
  assert.ok(cred.wizard.done && cred.wizard.secret === '');
  // A failed Git token verification in the wizard saves nothing.
  const before = calls.filter(c => c.method === 'PUT').length;
  cred.wizard = {step: 3, kind: 'gitToken', direct: true, secret: 'bad', extra: '', guide: {}, error: ''};
  await advanceCredentialWizard();
  assert.equal(calls.filter(c => c.method === 'PUT').length, before);
  assert.ok(cred.wizard.error.includes('令牌无效或已过期') && cred.wizard.error.includes('未保存'));
  assert.ok(!cred.wizard.done);
  assert.ok(!code.includes('通常包含 /v1'));
  assert.ok(!code.includes('不含 /v1/messages'));
})().catch(error => { console.error(error); process.exit(1); });
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("credential list and wizard failed: %v\n%s", err, out)
	}
}

func TestGotoCredentialBranches(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping credential navigation check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
let view = 'settings', now = 0, settingsRouteTransition = null;
const switchView = () => {}, activeViewName = () => view;
const performance = {now: () => (now += 100)};
const requestAnimationFrame = fn => fn();
let configured = {gitToken: true}, visible = true;
const panel = {
  getClientRects: () => visible ? [{}] : [],
  querySelector(selector) {
    const name = /data-credential-row="(\w+)"/.exec(selector)[1];
    return {classList: {contains: cls => cls === 'is-configured' && !!configured[name]}};
  }
};
const document = {querySelector: selector => selector === '#personalSettingsContent #credentialPanel' ? panel : null};
const opened = [], revealed = [];
const openCredentialWizard = key => opened.push(key), revealCredentialRow = key => revealed.push(key);
eval(code.slice(code.indexOf('let credentialFocusRequest=0;'), code.indexOf('const CREDENTIAL_RULES=')));
gotoCredential('gitToken');
assert.deepEqual(revealed, ['gitToken'], 'configured credentials expand in place');
gotoCredential('codeBuddyApiKey');
assert.deepEqual(opened, ['codeBuddyApiKey'], 'missing credentials open the wizard on step 2');
visible = false;
gotoCredential('codexApiKey');
view = 'issues'; visible = true;
gotoCredential('codexApiKey');
assert.deepEqual(opened, ['codeBuddyApiKey'], 'hidden panel or a different route does nothing');
assert.deepEqual(revealed, ['gitToken']);
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("gotoCredential branches failed: %v\n%s", err, out)
	}
}

func TestCredentialRowActivation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping credential row activation check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
const opened = [], toggled = [], acted = [];
const openCredentialWizard = key => opened.push(key), toggleCredentialRow = key => toggled.push(key);
const credentialRowAction = act => acted.push(act);
eval(code.slice(code.indexOf('function activateCredentialRow('), code.indexOf('// gotoCredential lands here')));
const panel = {};
bindCredentialPanel(panel);
const el = matches => ({closest: s => matches[s] ?? null});
const rowOf = (key, configured) => ({dataset: {credentialRow: key}, classList: {contains: c => c === 'is-configured' && configured}});
const main = row => {
  const node = {closest: s => s === '[data-credential-row]' ? row : s === '.credential-row-main' || s === '.credential-row-main[role="button"]' ? node : null};
  return node;
};
const click = target => panel.onclick({target, stopPropagation() {}});
const key = (target, k) => { let prevented = false; panel.onkeydown({target, key: k, preventDefault() { prevented = true }}); return prevented; };

const empty = rowOf('codeBuddyApiKey', false), done = rowOf('gitToken', true);
click(main(empty));
assert.deepEqual(opened, ['codeBuddyApiKey'], 'clicking an unconfigured row opens the wizard');
click(main(done));
assert.deepEqual(toggled, ['gitToken'], 'clicking a configured row toggles its editor');
const emptyMain = main(empty);
assert.ok(key(emptyMain, 'Enter') && key(emptyMain, ' '));
assert.deepEqual(opened, ['codeBuddyApiKey', 'codeBuddyApiKey', 'codeBuddyApiKey'], 'Enter and Space open the wizard too');
key(main(done), 'Enter');
assert.deepEqual(toggled, ['gitToken', 'gitToken']);
// 去配置 runs its own action once and never falls through to the row.
const setup = {disabled: false, dataset: {credentialAct: 'setup'}};
click(el({'[data-credential-row]': empty, '[data-credential-act]': setup, '.credential-row-main': {}}));
assert.deepEqual(acted, ['setup']);
assert.equal(opened.length, 3);
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("credential row activation failed: %v\n%s", err, out)
	}
}

// Generic templates must tell users up front that the 发起 MR node needs a 工蜂
// Git Token and a project-scoped 工蜂 MCP.
func TestGenericTemplateRequirementPreflight(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping generic template preflight check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
global.escapeHTML = value => String(value);
global.state = {
  currentUser: {id: 7}, projectFilter: 'alpha',
  mcpMarket: [{id: '61', aliases: ['gongfeng'], url: 'https://mcp-gateway.example.com/gongfeng'}],
  data: {users: [{id: 7, gitTokenConfigured: true}], mcpServers: []}
};
global.userById = id => state.data.users.find(u => Number(u.id) === Number(id));
eval(code.slice(code.indexOf('const GENERIC_REQUIREMENT_CHECKS='), code.indexOf('function genericRequirementChips(')));
const workflow = {requirements: [
  {key: 'gongfeng-git-token', nodeId: 'mr', label: '工蜂 Git Token', hint: '推送分支'},
  {key: 'tapd-mcp', nodeId: 'mr', label: 'TAPD MCP', hint: '回写 TAPD'},
  {key: 'gongfeng-mcp', nodeId: 'mr', label: '工蜂 MCP', hint: '创建 MR'}
]};
let rows = genericRequirementRows(workflow);
assert.deepEqual(rows.map(r => r.ok), [true, false, false]);
assert.equal(rows[0].fix, 'settings');
assert.equal(rows[2].fix, 'mcp');
assert.equal(rows[2].nodeId, 'mr');
state.data.mcpServers = [{scope: 'project', projectId: 'alpha', marketId: '61'}];
rows = genericRequirementRows(workflow);
assert.deepEqual(rows.map(r => r.ok), [true, false, true]);
// An install owned by another project must not satisfy the dependency.
state.data.mcpServers = [{scope: 'project', projectId: 'beta', marketId: '61'}];
assert.equal(genericRequirementRows(workflow)[2].ok, false);
// Without market metadata the name/url fallback still recognises 工蜂.
state.mcpMarket = [];
state.data.mcpServers = [{scope: 'project', projectId: 'alpha', name: 'Gongfeng MCP', url: 'https://mcp-gateway.example.com/gongfeng'}];
assert.equal(genericRequirementRows(workflow)[2].ok, true);
assert.ok(code.includes('通用模板为只读'));
assert.ok(code.includes('data-adopt-generic'));
assert.ok(code.includes('sop-generic'));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("generic template preflight check failed: %v\n%s", err, out)
	}
}

func TestLegacyExecutorIsNotSilentlyDisplayedAsCodeBuddy(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping executor UI check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
global.escapeHTML = value => String(value);
global.state = {data: {executors: [
  {id: 'codebuddy', name: 'CodeBuddy'},
  {id: 'codex', name: 'Codex'},
  {id: 'claude', name: 'Claude Code'}
]}};
eval(code.slice(code.indexOf('function executorOptions('), code.indexOf('function selectAgent(')));
const legacy = executorOptions('legacy-codex');
assert.ok(legacy.includes('历史执行器 legacy-codex'));
assert.ok(legacy.includes('value="legacy-codex" selected disabled'));
assert.ok(!legacy.includes('value="codebuddy" selected'));
assert.ok(executorOptions('codex').includes('value="codex" selected'));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("legacy executor UI check failed: %v\n%s", err, out)
	}
}

func TestScheduleIssueStatusPicker(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name="issueStatus" value="backlog" checked`,
		`name="issueStatus" value="todo" checked`,
		`name="issueStatus" value="in_progress"`,
		`触发哪些状态`,
		`name="source" aria-label="同步哪些来源"`,
		`value="gongfeng" data-legacy-tracker hidden disabled>只同步工蜂`,
		`value="tapd" data-legacy-tracker hidden disabled>只同步 TAPD`,
		`同步所有已配置来源`,
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("schedule form missing %q", want)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping schedule status helper check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
eval(code.slice(code.indexOf('function scheduleSourceLabel(job){'), code.indexOf('function scheduleModelAPI(')));
assert.equal(scheduleSourceLabel({}), '所有已配置来源');
assert.equal(scheduleSourceLabel({source:'gongfeng'}), '只同步工蜂');
assert.equal(scheduleSourceLabel({source:'tapd'}), '只同步 TAPD');
assert.deepEqual(scheduleIssueStatusValues({}), ['backlog','todo']);
assert.deepEqual(scheduleIssueStatusValues({id:'job-1'}), ['backlog','todo','in_progress','in_review','blocked']);
assert.deepEqual(scheduleIssueStatusValues({id:'job-1',skipDispatch:true}), ['backlog','todo']);
assert.deepEqual(scheduleIssueStatusValues({issueStatuses:['in_progress','blocked']}), ['in_progress','blocked']);
eval(code.slice(code.indexOf('function scheduleJobRunning(job){'), code.indexOf('function scheduleIntervalLabel(sec){')));
assert.equal(scheduleJobRunning({lastStatus:'running'}), true);
assert.equal(scheduleJobRunning({lastStatus:'success'}), false);
assert.equal(scheduleJobStatusText({lastStatus:'running'}), '正在运行… 关闭页面不会中断');
assert.equal(scheduleRunningCount([{projectId:'dp',lastStatus:'running'},{projectId:'platform',lastStatus:'success'}],'platform'), 0);
assert.equal(scheduleRunningCount([{projectId:'dp',lastStatus:'running'}],'dp'), 1);
assert.ok(!code.includes('if(!active)fillScheduleJobForm()'));
assert.ok(code.includes('已在服务端启动，关闭页面不会中断'));
assert.ok(code.includes('startScheduleJobPoll'));
assert.ok(code.includes("lastStatus:'running'"));
assert.ok(code.includes('请至少选择一个触发状态'));
const openModal=code.slice(code.indexOf('async function openScheduleJobModal'), code.indexOf('function findAlignRun'));
assert.ok(openModal.indexOf('resetScheduleJobForm()') < openModal.indexOf('await Promise.all'), 'opening the modal must reset the form before network, not after');
assert.ok(!/await Promise\.all\([^;]*\);\s*resetScheduleJobForm/.test(openModal), 'awaiting status must not wipe the AI tab');
const poller=code.slice(code.indexOf('function startScheduleJobPoll(){'), code.indexOf('async function runScheduleJob('));
assert.ok(poller.includes('scheduleRunningCount(state.data.scheduleJobs, state.projectFilter)'));
assert.ok(!poller.includes('.some(scheduleJobRunning)'));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("schedule status helper check failed: %v\n%s", err, out)
	}
}

func TestApprovalsAreProjectScoped(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping approvals project scope check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
global.state = {
  currentUser: {id: 7},
  projectFilter: 'gslb',
  data: {
    issues: [{id:'iss-afp', projectId:'platform'}],
    tasks: [{runId:'run-afp', issueId:'iss-afp'}],
    repositories: [{id:'repo-platform', projectId:'platform'}],
    runs: [
      {id:'run-afp', status:'waiting', executionContext:{responsibleUserId:7}},
      {id:'run-align', status:'waiting', workflowId:'scheduled-baseline-align', executionContext:{projectId:'platform', responsibleUserId:7}, repository:{name:'SkillHub', repositoryId:'repo-platform'}},
      {id:'run-gslb', status:'waiting', executionContext:{projectId:'gslb', responsibleUserId:7}},
      {id:'run-other-user', status:'waiting', executionContext:{projectId:'gslb', responsibleUserId:99}},
    ]
  }
};
eval(code.slice(code.indexOf('function runContext(r){'), code.indexOf('function taskTriggerInfo(')));
eval(code.slice(code.indexOf('function runProjectId(r){'), code.indexOf('function preferredWorkflow(')));
eval(code.slice(code.indexOf('function approvalItems(){'), code.indexOf('async function cancelRunFlow(')));
assert.deepEqual(approvalItems().map(r => r.id), ['run-gslb']);
state.projectFilter = 'platform';
assert.deepEqual(approvalItems().map(r => r.id), ['run-afp']);
state.data.runs[1].executionContext = {responsibleUserId:7};
assert.equal(runProjectId(state.data.runs[1]), 'platform');
assert.ok(code.includes('renderApprovals(true);'));
assert.ok(code.includes('value:approvalItems().length'));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("approvals project scope check failed: %v\n%s", err, out)
	}
}

func TestRepositoryFormUsesSingleURL(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name="repoUrl"`,
		`集成分支（可选）`,
		`检查多个 SOP 执行后工作分支之间是否存在合并冲突`,
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("repository form missing %q", want)
		}
	}
	for _, not := range []string{
		`工蜂 Web URL`,
		`name="integrateBranch" value="agentflow/integration" required`,
	} {
		if strings.Contains(string(html), not) {
			t.Fatalf("repository form still has %q", not)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping repository URL helper check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
eval(code.slice(code.indexOf('function parseGongfengRepoURL(raw){'), code.indexOf('function fillRepositoryFromLink(')));
const web = parseGongfengRepoURL('https://git.example.com/example/agent-flow');
assert.equal(web.project, 'example/agent-flow');
assert.equal(web.cloneUrl, 'https://git.example.com/example/agent-flow.git');
const ssh = parseGongfengRepoURL('git@git.example.com:example/agent-flow.git');
assert.equal(ssh.project, 'example/agent-flow');
assert.equal(ssh.cloneUrl, 'git@git.example.com:example/agent-flow.git');
const cloned = parseGongfengRepoURL('git clone git@git.example.com:example/agent-flow.git');
assert.equal(cloned.project, 'example/agent-flow');
assert.equal(cloned.cloneUrl, 'git@git.example.com:example/agent-flow.git');
assert.equal(web.name, 'agent-flow');
const http = parseGongfengRepoURL('http://git.example.com/example/agent-flow.git');
assert.equal(http.project, 'example/agent-flow');
assert.equal(http.name, 'agent-flow');
assert.equal(ssh.name, 'agent-flow');
const html = require('node:fs').readFileSync('index.html', 'utf8');
assert.ok(html.includes('集成分支用于检查多个 SOP 执行后工作分支之间是否存在合并冲突'));
assert.ok(/<input[^>]*name="integrateBranch"[^>]*>/.test(html));
assert.ok(!/<input[^>]*name="integrateBranch"[^>]*value="agentflow\/integration"/.test(html));
assert.ok(!code.includes("integrateBranch:'agentflow/integration'"));
assert.ok(code.includes('项目名称'));
assert.ok(code.includes('data-repo-edit'));
assert.ok(code.includes('data-repo-delete'));
assert.ok(code.includes('target="_blank"'));
assert.ok(!code.includes('data-edit-col'));
assert.ok(!code.includes('data-save-col'));
assert.ok(!code.includes('（待保存）'));
assert.ok(!code.includes('function repoAdminRowHTML('));
assert.ok(!code.includes('function captureRepoRows('));
assert.ok(!code.includes('function persistRepoRow('));
assert.ok(code.includes("integrateBranch:f.elements.integrateBranch.value.trim()"));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("repository URL helper check failed: %v\n%s", err, out)
	}
}

func TestRequirementDirectCreateUI(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	if !strings.Contains(page, `nav-label">项目配置`) {
		t.Fatal("sidebar must label the projects page 项目配置")
	}
	if !strings.Contains(page, `<script src="/app/composer.js"></script>`) {
		t.Fatal("index.html must load the requirement composer script")
	}
	if !strings.Contains(page, `data-add="sop"`) {
		t.Fatal("flow designer must offer SOP as a node type")
	}
	if !strings.Contains(page, `href="/app/requirement-wizard.css`) {
		t.Fatal("index.html must load the requirement wizard stylesheet")
	}
	reqStart := strings.Index(page, `id="requirementModal"`)
	reqEnd := strings.Index(page, `id="importTAPDModal"`)
	if reqStart < 0 || reqEnd < reqStart {
		t.Fatal("requirement modal must sit before the TAPD import modal")
	}
	composer := page[reqStart:reqEnd]
	for _, want := range []string{
		`id="reqSteps"`, `data-req-step="1"`, `data-req-step="2"`, `data-req-step="3"`, `>描述<`, `>澄清<`, `>确认<`, `id="reqStepSkipTag"`,
		`id="reqStepDescribe"`, `id="reqStepClarify"`, `id="reqStepConfirm"`, `id="reqFooter"`,
		`id="chatInput"`, `placeholder="要做什么？例如：`, `id="reqRepoChip"`, `id="reqRepoPopover"`, `id="reqRepository"`, `id="reqBaseBranch"`, `id="reqClarifyModelPicker"`,
		`id="reqThinking"`, `id="reqTraceToggle"`, `已用 0 个工具`, `id="reqClarifyPanel"`, `id="reqAnswerHistory"`,
		`id="reqTitle"`, `id="reqSummary"`, `id="reqConfirmRepoSlot"`, `id="reqWorkflow"`, `SOP 流程`, `id="reqTarget"`, `data-req-target="local"`, `data-req-target="tapd"`, `id="reqOutcome"`,
		`id="reqRepoChangedAlert"`, `id="reqReclarify"`,
		`id="reqBack"`, `id="chatStop"`, `id="reqSkip"`, `跳过澄清`, `id="reqNext"`, `开始澄清`, `id="reqGoCreate"`, `直接去创建`, `id="reqCreate"`, `先写一句需求`,
	} {
		if !strings.Contains(composer, want) {
			t.Fatalf("requirement wizard UI is missing %q", want)
		}
	}
	for _, gone := range []string{`id="chatSend"`, `id="chatCreateDirect"`, `id="chatCreateLocal"`, `id="chatCreateTAPD"`, `id="reqDestinations"`, `id="reqSetupRow"`, `id="reqContextLine"`, `id="chatCreateGongfeng"`, `需求套餐`, `id="reqSopRow"`, `id="reqPlaybooks"`, `问清再建单`, `再建模`, `已全部选择`} {
		if strings.Contains(composer, gone) {
			t.Fatalf("requirement wizard must not keep %q", gone)
		}
	}
	skipIdx := strings.Index(composer, `id="reqSkip"`)
	nextIdx := strings.Index(composer, `id="reqNext"`)
	if skipIdx < 0 || nextIdx < 0 || skipIdx > nextIdx {
		t.Fatal("跳过澄清 must sit to the left of the primary 开始澄清")
	}
	js, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	code := string(js)
	if strings.Contains(code, "直接创建会同步到 TAPD") {
		t.Fatal("直接创建 must not auto-sync TAPD; user chooses dest")
	}
	if !strings.Contains(code, "await syncRunControlState(runId)") || strings.Contains(code, "patchCachedRun(runId,{pauseRequested:false,status:'running'})") {
		t.Fatal("resume must refresh authoritative state instead of forcing running")
	}
	if !strings.Contains(code, "pauseRequested:Boolean(incoming.pauseRequested)") {
		t.Fatal("live run merge must treat omitted pauseRequested as false")
	}
	if !strings.Contains(code, "function runRetryNeedsContext(run)") {
		t.Fatal("script retry must skip the context prompt")
	}
	for _, want := range []string{
		`function goReqStep(`, `function renderReqSteps(`, `function renderReqFooter(`, `function renderReqConfirm(`, `function renderReqRepoChip(`, `function pickReqOption(`, `function submitReqAnswers(`, `function reclarifyRequirement(`,
		`reqCreateDirect`, `reqCreateAccepted`, `可在详情里触发 SOP`, `persistRequirementComposer`, `agentflow.requirementComposer`, `未配置`, `if(modal.id==='requirementModal')return`,
		`resumeChatClarify`, `jobId:chatClarifyJobId`, `正在接上分析`, `从空白开始`, `stripClarifyReason`, `renderClarifyPanel(JSON.parse(lastAssistant.content))`,
		`assigneeUserId:Number(state.currentUser?.id)||0`, `fillReqWorkflow`, `selectedReqWorkflowId`, `workflowId:selectedReqWorkflowId()`, `composerProgressHTML`,
		`已问清，可以创建`, `至少回答一题，或点「直接去创建」`, `提交回答`, `跳过此题`, `上一题`, `你的回答`, `全部用推荐答案`, `下一步：确认创建`, `澄清基于原仓库`, `重新澄清`, `· 已跳过`,
		`questionIndex:`, `answers:`, `repoSnapshot:`, `target:`, `step:`,
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("requirement wizard behavior is missing %q", want)
		}
	}
	for _, gone := range []string{`查看完整度详情`, `准入线`, `composerAssessmentView`, `invalidateRequirementAnalysis`, `没有固定轮数`, `composerCreateReady`, `setComposerRetryMode`, `composerClarifyRetry`, `state.reqCreateMode`, `createIssueDirect`, `setChatCreateEnabled`, `syncReqComposerLayout`} {
		if strings.Contains(code, gone) {
			t.Fatalf("requirement wizard must not keep %q", gone)
		}
	}
	createStart := strings.Index(code, "async function createIssueFromChat(")
	if createStart < 0 {
		t.Fatal("createIssueFromChat must exist")
	}
	createEnd := strings.Index(code[createStart+1:], "\nasync function ")
	if next := strings.Index(code[createStart+1:], "\nfunction "); next >= 0 && (createEnd < 0 || next < createEnd) {
		createEnd = next
	}
	if createEnd < 0 {
		t.Fatal("createIssueFromChat must be followed by another function")
	}
	if strings.Contains(code[createStart:createStart+1+createEnd], "afConfirm") {
		t.Fatal("creating a requirement must be one click, without afConfirm")
	}
	if strings.Contains(code, `renderClarifyPanel(JSON.parse(lastAssistant.content), reconnect?'':interruptHint)`) {
		t.Fatal("restored analysis must not treat the interrupt hint as a render error")
	}
}

func TestDeploymentDurationDashboardIsNotShipped(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{`data-view="deployments"`, `id="deploymentsView"`, `/deployment-dashboard.css`, `data-obs-tab="skillhub"`} {
		if strings.Contains(string(html), unwanted) {
			t.Fatalf("index.html still ships removed dashboard markup %q", unwanted)
		}
	}
	for _, unwanted := range []string{"renderDeploymentDashboard", "/api/deployment-metrics", "refreshSkillhubEfficiency"} {
		if strings.Contains(string(app), unwanted) {
			t.Fatalf("app bundle still ships removed dashboard behavior %q", unwanted)
		}
	}
	if _, err := os.Stat("deployment-dashboard.css"); err == nil {
		t.Fatal("deployment-dashboard.css should not be shipped")
	}
}

func TestTAPDImportUI(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{`id="importTAPDIssue"`, `id="dashImportTAPD"`, `id="importTAPDModal"`, `id="importTAPDURL"`, `id="importTAPDWorkflow"`, `id="importTAPDRepository"`, `代码仓库（可多选）`, `repo-picker`} {
		if !strings.Contains(page, want) {
			t.Fatalf("UI is missing %q", want)
		}
	}
	dashImport := strings.Index(page, `id="dashImportTAPD"`)
	dashNew := strings.Index(page, `id="dashNewRequirement"`)
	if dashImport < 0 || dashNew < 0 || dashImport > dashNew {
		t.Fatal("工作总览 must place 导入 TAPD to the left of 新建需求")
	}
	projectGroup := strings.Index(page, `<p>项目</p>`)
	flowGroup := strings.Index(page, `<p>流程与任务</p>`)
	agentGroup := strings.Index(page, `<p>Agent 能力</p>`)
	runsNav := strings.Index(page, `data-view="runs"`)
	projectsNav := strings.Index(page, `data-view="projects"`)
	if flowGroup < 0 || agentGroup < 0 || !(flowGroup < runsNav && runsNav < agentGroup) {
		t.Fatal("运行记录 must sit under 流程与任务")
	}
	if projectGroup < 0 || !(projectGroup < projectsNav && projectsNav < flowGroup) {
		t.Fatal("项目配置 must sit under 项目, before 流程与任务")
	}
	js, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	code := string(js)
	for _, want := range []string{`/api/issues/import-tapd`, `openImportTAPDModal`, `fillImportTAPDWorkflow`, `fillImportTAPDRepository`, `selectedImportTAPDRepositoryIds`, `renderImportTAPDRepositoryPicker`, `repositoryIds`, `请至少选择一个代码仓库`, `importTAPDStory`, `clarificationNeedsConfirm`, `data-dtab="clarify"`, `确认结论`, `issueClarifyJobs`, `attachIssueClarifyStream`, `关闭网页也不会中断`, `clarifyOptionLabel`, `clarifyQuestionOptions`, `createIssueFromChat`, `projectTAPDReady`, `reqCreate`, `setReqTarget`, `pushIssueToTAPDFromDetail`, `dashImportTAPD`, `run-id-copy`, `data-export-logs`, `从这里重跑`, `重新触发`, `data-retrigger-issue`, `data-rerun-from-here`, `canRetriggerIssue`, `openRerunFromNode`, `executionContext?.issueId`, `run?.status==='succeeded'`, `/api/issues/${encodeURIComponent(issueId)}/requirement`, `inferSOPLane`, `req-lane-card`, `issueLaneDots`, `.filter(l=>!l.orphan)`, `lanes.length<2`, `collapseSingleSOPLane`, `issueSOPLabel`, `actionWorkflow`, `data-workflow`, `workflowSOPLane`, `需求开发`, `saveIssueSOPS`, `/sops`, `effectiveIssueSOPBindings`, `projectSOPBindings`, `fillReqWorkflow`, `plannedSOPNodes`, `sopWorkflowId`, `sopWorkflowChoices`, `renderReqConfirm`} {
		if !strings.Contains(code, want) {
			t.Fatalf("app.js is missing %q", want)
		}
	}
	if strings.Contains(code, `projectPlaybookHTML`) {
		t.Fatal("playbook editor must not live in 项目成员")
	}
	if strings.Contains(code, `在「项目成员」里配`) || strings.Contains(code, `可在项目成员里改`) {
		t.Fatal("requirement playbooks must not be configured from 项目成员")
	}
	if strings.Contains(code, `data-open-sops-playbook`) {
		t.Fatal("playbook config links must not send users to 流程模板")
	}
	if strings.Contains(code, `data-issue-playbook`) || strings.Contains(code, `assignIssuePlaybook`) || strings.Contains(code, `去改套餐`) {
		t.Fatal("执行进展 must not configure or switch playbooks")
	}
	if strings.Contains(code, `交接产物`) {
		t.Fatal("SOP pane must not ask users to type 交接产物")
	}
	if strings.Contains(code, `并指定每一步的责任人`) {
		t.Fatal("SOP pane must not ask users to configure an owner for every step")
	}
	if strings.Contains(code, `从节点重跑`) {
		t.Fatal("run header must not keep 从节点重跑; use node-local 从这里重跑")
	}
	if strings.Contains(code, `data-rerun-from-node`) {
		t.Fatal("run header must not keep data-rerun-from-node")
	}
	if strings.Contains(code, `data-copy-run>复制 Run ID`) {
		t.Fatal("run detail must not keep a header 复制 Run ID button")
	}
	if strings.Contains(code, `run-eval-header-btn`) {
		t.Fatal("run detail header must not keep 智能评测")
	}
	headerExport := strings.Index(code, `run-header-actions`)
	sectionExport := strings.Index(code, `run-section-label-actions`)
	logsExport := strings.Index(code, `data-export-logs title="下载本次运行全部节点的 Worker 日志">导出全部日志`)
	workerLogs := strings.Index(code, `worker-log-heading-actions`)
	if headerExport < 0 || sectionExport < 0 || logsExport < 0 {
		t.Fatal("run detail must still export all-node logs from 执行详情")
	}
	if logsExport < sectionExport || logsExport < headerExport {
		t.Fatal("导出全部日志 should sit with 执行详情, not the top header")
	}
	if workerLogs >= 0 && strings.Contains(code[workerLogs:workerLogs+180], `data-export-logs`) {
		t.Fatal("导出全部日志 must not sit inside Worker 日志")
	}
}

func TestPlaybooksPageRemoved(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, leftover := range []string{`data-view="playbooks"`, `id="playbooksView"`, `id="sopPlaybooks"`, `需求套餐`} {
		if strings.Contains(page, leftover) {
			t.Fatalf("需求套餐 page must be removed, still has %q", leftover)
		}
	}
	js, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	code := string(js)
	for _, leftover := range []string{`function renderSOPPlaybooks`, `function addProjectPlaybook`, `需求套餐`} {
		if strings.Contains(code, leftover) {
			t.Fatalf("playbook editor must be removed from app.js, still has %q", leftover)
		}
	}
}

func TestObsDeliveryBadgeShowsWithoutFullScan(t *testing.T) {
	src, err := os.ReadFile("app/observability.js")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	start := strings.Index(code, "const badge=$('#obsDeliveryCount');")
	if start < 0 {
		t.Fatal("delivery badge render missing")
	}
	end := strings.Index(code[start:], "\n  const reopen=")
	if end < 0 {
		t.Fatal("delivery badge render block not found")
	}
	block := code[start : start+end]
	if strings.Contains(block, "obsAnalyticsFull") {
		t.Fatalf("delivery badge must not wait for the full delivery scan: %s", block)
	}
	if !strings.Contains(block, "state.analytics?.delivery?.issues") || !strings.Contains(block, "badge.hidden=!n") {
		t.Fatalf("delivery badge should read overview delivery count and hide at zero: %s", block)
	}
}

func TestUsageAnalysisUI(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{`data-obs-tab="overview"><svg class="agent-icon harness-icon" data-icon="token" width="14" height="14" aria-hidden="true"></svg>用量`, `data-obs-tab="delivery"><svg class="agent-icon harness-icon" data-icon="flag" width="14" height="14" aria-hidden="true"></svg>交付`, `id="exportObsReport"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("usage analysis UI missing %q", want)
		}
	}
	for _, not := range []string{`data-obs-tab="quality"`, `data-obs-tab="cost"`, `data-obs-tab="agents"`, `data-obs-tab="trace"`} {
		if strings.Contains(page, not) {
			t.Fatalf("usage analysis still has extra tab %q", not)
		}
	}
	js, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	code := string(js)
	for _, want := range []string{"ensureObsUserFilter", "obsRunMatchesUser", "exportObsReport", "buildObsReportHTML", "userDisplay(user)", "assigneeUserId", "各模型用量", "每人每日用量", "成员汇总", "data-obs-scope", "setObsScope", "需求类型分布", "SOP 版本对比", "节点质量明细", "setObsTrendGrain", "obsTrendChart", "该责任人在各项目", "data-obs-grain", "grain=", "全部", "obsApiGrain", "obsRangeCaption", "obsPeriodPicker", "setObsPeriod", "obsScopedView", "data-obs-period", "不含暂停/恢复", "Loop 指标", "workflow_task_id", "averageTokensPerRun", "toolSuccessRate", "runTraces", "obs-traces", "isPreviewHost", "showDirectRunButton"} {
		if !strings.Contains(code, want) {
			t.Fatalf("app.js is missing %q", want)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping usage analysis helper check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
global.state = {
  currentUser: {id: 7, loginName: 'alice', chineseName: '爱丽丝'},
  obsUserFilter: '',
  obsUserFilterReady: false,
  obsScope: 'user',
  data: {
    users: [{id: 7, loginName: 'alice', chineseName: '爱丽丝'}, {id: 8, loginName: 'bob', chineseName: '鲍勃'}],
    issues: [{id: 'iss-1', assigneeUserId: 7, source: 'gongfeng'}],
    tasks: [{runId: 'run-assignee', issueId: 'iss-1'}],
  }
};
function userById(id){return (state.data.users||[]).find(u=>Number(u.id)===Number(id))}
function userDisplay(user){return user?(user.chineseName&&user.loginName?user.loginName+'('+user.chineseName+')':user.loginName||user.chineseName||('用户 '+user.id)):''}
eval(code.slice(code.indexOf('function ensureObsUserFilter(){'), code.indexOf('async function switchObsTab')));
eval(code.slice(code.indexOf('function obsUserName(id){'), code.indexOf('function obsUserChoices(){')));
assert.equal(obsUserName(7), 'alice(爱丽丝)');
assert.equal(obsRunMatchesUser({executionContext:{responsibleUserId:7}}, 7), true);
assert.equal(obsRunMatchesUser({id:'run-assignee', executionContext:{}}, 7), true);
assert.equal(obsRunMatchesUser({id:'run-assignee', executionContext:{}}, 8), false);
ensureObsUserFilter();
assert.equal(state.obsUserFilter, '7');
state.obsUserFilterReady=false;state.obsUserFilter='';state.obsScope='project';
ensureObsUserFilter();
assert.equal(state.obsUserFilter, '', 'project dimension must not default to current user');
assert.ok(code.includes("api('/api/analytics'+analyticsQuery"));
assert.ok(code.includes("obsScope()==='user'&&state.obsUserFilter"));
assert.ok(code.includes('grain='));
assert.ok(code.includes('crossProject'));
assert.ok(!code.includes('function obsQuality('));
eval(code.slice(code.indexOf('function obsTrendGrain(){'), code.indexOf('function ensureObsUserFilter')));
function formatTokens(n){return String(n)}
function escapeHTML(s){return String(s)}
assert.equal(obsWeekLabel('2026-W37'), '9/7–9/13');
assert.equal(obsTrendLabel({date:'2026-09-16'}), '09/16');
assert.equal(obsTrendValue({tokens:10,runs:2,issues:3,costUsd:1.5},'issues'), 3);
const delta=obsPeriodDelta([{date:'2026-W36',tokens:200},{date:'2026-W37',tokens:100}],'tokens');
assert.equal(delta.delta, -100);
assert.ok(obsDeltaChip(delta,true).includes('good'));
assert.ok(obsTrendChart([{date:'2026-W36',tokens:20},{date:'2026-W37',tokens:10}],'tokens').includes('obs-chart-line'));
state.obsTrendGrain='all';
assert.equal(obsApiGrain(), 'month');
assert.equal(obsRangeCaption(), '累计');
assert.equal(obsTrendWindow([{date:'2020-01'},{date:'2026-09'}]).length, 2);
state.obsTrendGrain='day';
state.obsPeriodKey='2026-09-20';
assert.equal(obsTrendWindow(Array.from({length:40},()=>({date:'x'}))).length, 30);
assert.equal(obsStepPeriodKey('2026-09-20','day',1), '2026-09-21');
assert.equal(obsPeriodRange('2026-09-20','day').to.toISOString().slice(0,10), '2026-09-21');
assert.equal(obsDateToWeekKey(new Date(Date.UTC(2026,8,16))), '2026-W38');
assert.equal(obsPeriodRange('2026-W37','week').label, '9/7–9/13');
state.analytics={runTraces:[
  {status:'succeeded',tokens:100,humanInterventions:0,issueId:'a',createdAt:'2026-09-20T08:00:00Z',durationMs:1000,toolCallsSucceeded:2,toolCallsFailed:0},
  {status:'failed',tokens:50,humanInterventions:1,issueId:'b',createdAt:'2026-09-21T08:00:00Z',durationMs:2000,toolCallsSucceeded:0,toolCallsFailed:1}
]};
const scoped=obsScopedView();
assert.equal(scoped.total, 1);
assert.equal(scoped.tokens, 100);
assert.equal(scoped.demo.humanInterventions, 0);
assert.ok(!code.slice(code.indexOf('async function refreshObs(){'), code.indexOf('function runCost(')).includes("api('/api/runs"));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("usage analysis helper check failed: %v\n%s", err, out)
	}
}

func TestListPaginationUI(t *testing.T) {
	js, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	code := string(js)
	for _, want := range []string{
		"function paginateList(",
		"function pagerHTML(",
		"function goListPage(",
		"function bindPager(",
		"paginateList('issues'",
		"paginateList('runs'",
		"paginateList('mcp'",
		"obs-users",
		"obs-deliveries",
		"data-page-key",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("app.js is missing pagination %q", want)
		}
	}
	for _, not := range []string{"paginateList('sops'", "paginateList('agents'", "paginateList('assets'", "paginateList('approvals'", "paginateList('sync'", "obs-sops", "obs-models"} {
		if strings.Contains(code, not) {
			t.Fatalf("app.js still paginates %q", not)
		}
	}
	css, err := os.ReadFile("platform.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".pager{") && !strings.Contains(string(css), ".pager {") {
		t.Fatal("platform.css is missing .pager styles")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping pagination helper check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
global.state = {listPages:{}};
function escapeHTML(v){return String(v??'')}
eval(code.slice(code.indexOf('const LIST_PAGE_SIZE='), code.indexOf('function goListPage')));
const items = Array.from({length:45}, (_,i)=>i+1);
const p1 = paginateList('issues', items);
assert.equal(p1.size, 20);
assert.equal(p1.page, 1);
assert.equal(p1.pages, 3);
assert.deepEqual(p1.items, items.slice(0,20));
assert.equal(pagerHTML('issues', p1).includes('data-page-key="issues"'), true);
assert.equal(pagerHTML('issues', p1).includes('第 1–20 条 / 共 45 条'), true);
state.listPages.issues = 3;
const p3 = paginateList('issues', items);
assert.equal(p3.page, 3);
assert.deepEqual(p3.items, items.slice(40,45));
assert.equal(pagerHTML('issues', {total:10,size:20}).trim(), '');
resetListPage('issues');
assert.equal(state.listPages.issues, 1);
const obs = paginateList('obs-users', items);
assert.equal(obs.size, 20);
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("pagination helper check failed: %v\n%s", err, out)
	}
}

func TestIssueTitleFuzzySearch(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "模糊搜索标题") {
		t.Fatal("issue list must advertise title fuzzy search")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping issue title fuzzy-search behavior check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
eval(code.slice(code.indexOf('function normalizeIssueTitleSearch('), code.indexOf('function filteredIssues(')));
const issue = {number: 42, title: '全量 PHPUnit-回归', description: '覆盖 DemoDNS 主流程', externalRef: {externalId: 'TAPD-100'}};
const project = {key: 'DP'};
assert.equal(issueMatchesQuery(issue, project, 'php unit'), true);
assert.equal(issueMatchesQuery(issue, project, '回归 php'), true);
assert.equal(issueMatchesQuery(issue, project, 'PHPUNIT回归'), true);
assert.equal(issueMatchesQuery(issue, project, 'DP-42'), true);
assert.equal(issueMatchesQuery(issue, project, 'DemoDNS'), true);
assert.equal(issueMatchesQuery(issue, project, '不存在的标题'), false);
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("issue title fuzzy-search behavior check failed: %v\n%s", err, out)
	}
}

func TestWorkspaceBrowserDefaultsAndLanguageHighlighting(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping workspace language helper check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('vscode.js', 'utf8');
const app = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
assert.ok(code.includes("workbench.web.main"), 'workspace page must boot vscode-web');
assert.ok(code.includes("agentflow-workspace"), 'workspace page must load the run filesystem extension');
assert.ok(code.includes("scheme: 'agentflow'"), 'workspace page must open an agentflow virtual folder');
assert.ok(app.includes('data-vscode-frame'), 'run workspace must embed vscode iframe');
assert.ok(app.includes('vscode.html'), 'run workspace must load vscode workbench page');
assert.ok(app.includes('openVSCodeWindow'), 'run workspace must allow a detached vscode window');
assert.ok(app.includes('当前无法浏览工作区'), 'finished runs must show the deleted-workspace empty state');
assert.ok(app.includes('loadRunWorkspace'), 'workspace tab must probe availability before embedding vscode');
assert.ok(app.includes('runWorkspaceWasCleaned'), 'year-1 JSON timestamps must not be treated as a cleaned workspace');
eval(app.slice(app.indexOf('function runWorkspaceWasCleaned('), app.indexOf('function workspaceIsAvailable(')));
assert.equal(runWorkspaceWasCleaned({status:'running', workspaceCleanedAt:'0001-01-01T00:00:00Z'}), false);
assert.equal(runWorkspaceWasCleaned({status:'succeeded', workspaceCleanedAt:'0001-01-01T00:00:00Z'}), false);
assert.equal(runWorkspaceWasCleaned({status:'succeeded', workspaceCleanedAt:'1970-01-01T00:00:00Z'}), false);
assert.equal(runWorkspaceWasCleaned({status:'succeeded', workspaceCleanedAt:'2026-09-17T04:00:00Z'}), true);
assert.equal(runWorkspaceWasCleaned({status:'running'}), false);
assert.ok(code.includes("window.open(vscodeWorkbenchURL(id), vscodeWindowName(id)"), 'vscode workbench must open a named window');
assert.ok(code.includes('info.available'), 'standalone vscode must refuse to boot when the workspace is gone');
assert.ok(code.includes("'git.enabled': false"), 'vscode-web has no git CLI; keep builtin git off against the virtual folder');
assert.ok(code.includes("params.set('changes'"), 'vscode-web must pass workspace git changes into the workspace folder query');
assert.ok(code.includes('info.repositories'), 'vscode-web must use backend git roots instead of treating every top-level path as a repo');
assert.ok(!code.includes("entry.type === 'directory'"), 'vscode-web must not treat workspace folders as nested git repositories');
const extension = require('node:fs').readFileSync('extensions/agentflow-workspace/extension.js', 'utf8');
const pkg = require('node:fs').readFileSync('extensions/agentflow-workspace/package.json', 'utf8');
assert.ok(code.includes("'scm.defaultViewMode': 'list'"), 'vscode-web must keep native SCM list/tree toggle defaults');
assert.ok(code.includes("'scm.experimental.showHistoryGraph': false"), 'vscode-web must keep the experimental git graph off so commits expand to files');
assert.ok(code.includes("'scm.showOutgoingChanges': 'auto'"), 'vscode-web must show outgoing commits that can expand to a file list');
assert.ok(code.includes("'scm.showChangesSummary': false"), 'vscode-web must not insert an All Changes summary above commit files');
assert.ok(extension.includes('gitRefQuery'), 'commit file URIs must use JSON ref queries so native vscode.diff can open them');
assert.ok(extension.includes('createSourceControl'), 'workspace must use vscode native SCM providers for nested git');
assert.ok(extension.includes("createSourceControl(id, 'Git'"), 'SCM provider label must stay Git so the repo name is not duplicated');
assert.ok(extension.includes('createResourceGroup'), 'workspace SCM must use native staged/changes/untracked groups');
assert.ok(extension.includes('historyProvider'), 'workspace SCM must expose native git history');
assert.ok(extension.includes('agentflow.refresh'), 'workspace SCM must provide a native refresh command');
assert.ok(extension.includes('startStatusAutoRefresh'), 'workspace SCM must auto-refresh git status like vscode');
assert.ok(extension.includes('setTimeout'), 'workspace SCM must keep a git status poll loop');
assert.ok(extension.includes('onDidChangeWindowState'), 'workspace SCM must refresh git status when the window is focused');
assert.ok(extension.includes('ahead: commits.length'), 'history provider must resolve an ancestor so commits appear as an expandable list');
assert.ok(pkg.includes('scmHistoryProvider'), 'workspace extension must enable the SCM history proposal');
assert.ok(pkg.includes('view == workbench.scm'), 'refresh must sit on the SCM view title once, not per repository');
assert.ok(pkg.includes('"scm/title"'), 'refresh must use scm/title because the SCM pane does not use view/title');
assert.ok(pkg.includes('$(refresh)'), 'workspace extension must contribute a refresh icon on the SCM title');
assert.ok(!extension.includes('SOP 更改'), 'workspace must not invent a custom SOP source control');
assert.ok(!extension.includes('mountGitChanges'), 'workspace must not paint a custom git HTML overlay');
assert.ok(extension.includes('agentflow-base'), 'workspace extension must open diffs against HEAD');
assert.ok(extension.includes("command: 'agentflow.openChange'"), 'SCM resources must open a file preview on click');
assert.ok(extension.includes("'vscode.diff'"), 'modified SCM resources must open the native diff editor');
assert.ok(extension.includes("'vscode.open'"), 'untracked SCM resources must open a file preview');
eval(extension.slice(extension.indexOf('function changeCode('), extension.indexOf('function changeDecoration(')));
eval(extension.slice(extension.indexOf('function shouldDiffChange('), extension.indexOf('function changeResourceState(')));
eval(extension.slice(extension.indexOf('function workspaceRepoName('), extension.indexOf('class AgentFlowFS')));
{
  const vscodeStub = { EventEmitter: class { constructor() { this.event = () => ({ dispose() {} }); } fire() {} }, Disposable: class {} };
  const src = extension.slice(extension.indexOf('class AgentFlowFS'), extension.indexOf('function workspaceUri('));
  const providers = new Function('vscode', src + '; return [AgentFlowFS, AgentFlowBaseFS];')(vscodeStub);
  for (const Provider of providers) {
    assert.equal(typeof new Provider('run-1').onDidChangeFile, 'function', Provider.name + ' must expose onDidChangeFile or registerFileSystemProvider aborts activation');
  }
}
assert.equal(changeCode({index:'M', worktree:' '}), 'M');
assert.equal(changeCode({index:' ', worktree:'?'}), 'U');
assert.equal(shouldDiffChange({index:'M', worktree:' '}), true);
assert.equal(shouldDiffChange({index:' ', worktree:'?'}), false);
const grouped = repositoriesFromChanges([{path:'a/main.go', repository:'a'}, {path:'b/app.ts', repository:'b'}], [{path:'a', type:'directory'}, {path:'b', type:'directory'}]);
assert.equal(grouped.length, 2);
assert.equal(grouped[0].name, 'a');
assert.equal(grouped[1].name, 'b');
const rootGit = repositoriesFromChanges([{path:'e2e-cases.md'}, {path:'docs/readme.md'}], [{path:'docs', type:'directory'}, {path:'e2e-cases.md', type:'file'}]);
assert.equal(rootGit.length, 1);
assert.equal(rootGit[0].name, 'workspace');
assert.equal(sourceControlRootPath('workspace'), '/');
assert.equal(sourceControlRootPath('.'), '/');
assert.equal(sourceControlRootPath('demodns-web'), '/demodns-web');
eval(extension.slice(extension.indexOf('function changesForRepo('), extension.indexOf('function changesSignature(')));
assert.equal(changesForRepo([{path:'e2e-cases.md'}, {path:'a/main.go', repository:'a'}], 'workspace').length, 1);
assert.equal(changesForRepo([{path:'e2e-cases.md'}, {path:'a/main.go', repository:'a'}], 'a').length, 1);
eval(extension.slice(extension.indexOf('function changesSignature('), extension.indexOf('function applyRepoChanges(')));
assert.equal(changesSignature([{path:'b',index:'M',worktree:' '},{path:'a',index:' ',worktree:'M'}]), changesSignature([{path:'a',index:' ',worktree:'M'},{path:'b',index:'M',worktree:' '}]));
assert.notEqual(changesSignature([{path:'a',index:' ',worktree:'M'}]), changesSignature([]));
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("workspace language helper check failed: %v\n%s", err, out)
	}
	css, err := os.ReadFile("observability.css")
	if err != nil {
		t.Fatal(err)
	}
	styles := string(css)
	for _, want := range []string{".run-workspace.vscode-embed{display:block", "height:clamp(520px,calc(100dvh - 230px),760px)", ".run-workspace.vscode-embed iframe{width:100%;height:100%", ".run-workspace.vscode-embed.maximized{position:fixed"} {
		if !strings.Contains(styles, want) {
			t.Fatalf("workspace independent scrolling CSS is missing %q", want)
		}
	}
}

func TestVSCodeJSParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping vscode.js parse check")
	}
	out, err := exec.Command(node, "--check", "vscode.js").CombinedOutput()
	if err != nil {
		t.Fatalf("vscode.js failed to parse: %v\n%s", err, out)
	}
}

func TestIssueFilterAndManualCloseUI(t *testing.T) {
	htmlBytes, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	for _, want := range []string{
		`id="issueStatusFilter"`,
		`id="issueAssigneeFilter"`,
		`<option value="backlog">待规划</option>`,
		`<option value="todo">待处理 / 待执行</option>`,
		`<option value="done">已完成</option>`,
		`<option value="closed">已关闭</option>`,
		`data-dtab="operations"`,
		`未指定责任人`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html is missing %q", want)
		}
	}

	appBytes, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	app := string(appBytes)
	for _, want := range []string{
		"issueStatusFilter:''",
		"issueAssigneeFilter:''",
		"$('#issueStatusFilter')",
		"$('#issueAssigneeFilter')",
		"renderIssueAssigneeFilter",
		"closeIssueById",
		"data-close-issue",
		"/api/issues/${encodeURIComponent(issueId)}/close",
		"data-issue-menu",
		"showIssueActionMenu",
		"ensureIssueMenuPopover",
		"issue-action-popover",
		"编辑需求",
		"issueMatchesStatusFilter",
		"issueWaitingToExecute",
		"['closed','Closed']",
		"operationsPane",
		"function runRouteId()",
		"function flowRouteId()",
		"function restoreOpenWorkflow()",
	} {
		if !strings.Contains(app, want) {
			t.Fatalf("app.js is missing %q", want)
		}
	}
	if strings.Contains(app, "submit.textContent=isNew?'同步并保存':'保存 Issue'") {
		t.Fatalf("app.js still contains '保存 Issue' in submit button")
	}

	node, err := exec.LookPath("node")
	if err == nil {
		nodeScript := `
const fs = require('node:fs');
const code = fs.readFileSync('` + appBundlePath(t) + `', 'utf8');
const mockEl = () => ({
  classList: { toggle: () => {}, contains: () => false, add: () => {}, remove: () => {} },
  style: {},
  dataset: {},
  innerHTML: '',
  appendChild: () => {},
  addEventListener: () => {},
  querySelector: () => mockEl(),
  querySelectorAll: () => []
});
global.window = global;
global.addEventListener = () => {};
global.removeEventListener = () => {};
global.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
global.location = { origin: 'http://localhost', hash: '', href: 'http://localhost/', search: '' };
global.document = {
  hidden: false,
  addEventListener: () => {},
  documentElement: { dataset: {} },
  querySelector: () => mockEl(),
  querySelectorAll: () => [],
  getElementById: () => mockEl(),
  body: mockEl()
};
global.$ = () => mockEl();
global.$$ = () => [];
global.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
global.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
eval(code + '; global.state = state; global.renderIssues = renderIssues; global.issueMatchesStatusFilter = issueMatchesStatusFilter; global.issueFinished = issueFinished; global.issueRunHealth = issueRunHealth; global.issueStatusBucket = issueStatusBucket; global.deliveryLockedLabel = deliveryLockedLabel; global.reopenDeliveredButton = reopenDeliveredButton; global.canRetriggerIssue = canRetriggerIssue; global.issueOutcome = issueOutcome; global.issueCurrentStage = issueCurrentStage; global.issueListProgress = issueListProgress;');
state.data = { issues: [], projects: [], repositories: [], workflows: [], runs: [], tasks: [] };
state.listPages = {};
renderIssues();
state.data.issues = [{ id: 'iss-1', projectId: 'p1', number: 1, title: 'test', status: 'backlog' }];
renderIssues();
const noRunCases = [
  ['in_progress', 'in_progress'],
  ['in_review', 'in_review'],
  ['blocked', 'blocked']
];
for (const [status, expected] of noRunCases) {
  const issue = { id: 'case-' + status, status };
  if (!issueMatchesStatusFilter(issue, expected) || issueMatchesStatusFilter(issue, 'todo')) {
    throw new Error(status + ' without a run was swallowed by the todo filter');
  }
}
if (!issueMatchesStatusFilter({ id: 'backlog-case', status: 'backlog' }, 'backlog') || issueMatchesStatusFilter({ id: 'backlog-case', status: 'backlog' }, 'todo')) {
  throw new Error('backlog must follow its persisted status instead of being folded into todo');
}
const namedNodeIssue = { id: 'named-node', status: 'in_progress', workflowId: 'wf' };
state.data.workflows = [{ id: 'wf', nodes: [{ id: 'node', name: '已完成' }] }];
state.data.runs = [{ id: 'named-node-run', status: 'running', executionContext: { issueId: namedNodeIssue.id }, nodes: { node: { status: 'running' } } }];
if (issueFinished(namedNodeIssue) || issueMatchesStatusFilter(namedNodeIssue, 'done')) {
  throw new Error('a running node named 已完成 must not mark its issue complete');
}
const trackerClosed = { id: 'tapd-closed', source: 'tapd', status: 'closed', closeSource: 'external', externalRef: { externalState: 'closed' } };
if (issueRunHealth(trackerClosed) !== 'closed' || issueStatusBucket(trackerClosed) !== 'closed' || !issueMatchesStatusFilter(trackerClosed, 'closed') || issueMatchesStatusFilter(trackerClosed, 'done')) {
  throw new Error('closed external work item without delivery must use the closed filter');
}
if (issueCurrentStage(trackerClosed).label !== '已关闭' || issueListProgress(trackerClosed, null, null).pct !== 0) {
  throw new Error('externally closed item without a run must display closed with zero SOP progress');
}
const gongfengClosed = { id: 'gf-closed', source: 'gongfeng', status: 'closed', closeSource: 'external', externalRef: { externalState: 'closed' } };
if (issueStatusBucket(gongfengClosed) !== 'closed' || issueCurrentStage(gongfengClosed).label !== '工蜂已关闭') {
  throw new Error('closed gongfeng issue without delivery must stay in the closed filter');
}
const gongfengMerged = { id: 'gf-merged', source: 'gongfeng', status: 'closed', closeSource: 'external', externalRef: { externalState: 'closed' }, deliveryState: 'merged', deliveryMrState: 'merged', deliveryMrUrl: 'https://git.example.com/org/repo/merge_requests/9' };
if (issueCurrentStage(gongfengMerged).label !== 'MR 已合入' || issueOutcome(gongfengMerged).title !== 'MR 已合入' || issueStatusBucket(gongfengMerged) !== 'done' || issueMatchesStatusFilter(gongfengMerged, 'closed')) {
  throw new Error('a merged MR must outrank a closed Gongfeng issue');
}
const gongfengIntegrated = { id: 'gf-integrated', source: 'gongfeng', status: 'closed', closeSource: 'external', externalRef: { externalState: 'closed' }, deliveryState: 'merged', deliveryMrState: 'integrated', deliveryMrUrl: 'https://git.example.com/org/repo/merge_requests/10' };
if (issueCurrentStage(gongfengIntegrated).label !== '已合入基线' || issueStatusBucket(gongfengIntegrated) !== 'done') {
  throw new Error('baseline merge must outrank a closed Gongfeng issue');
}
const delivered = { id: 'delivered', source: 'tapd', status: 'closed', closeSource: 'external', deliveryState: 'merged', deliveryMrState: 'opened', externalRef: { externalState: 'closed' } };
if (issueStatusBucket(delivered) !== 'closed' || issueMatchesStatusFilter(delivered, 'done') || issueCurrentStage(delivered).label !== '已关闭' || reopenDeliveredButton(delivered)) {
  throw new Error('external closure must remain closed even after local delivery');
}
if (canRetriggerIssue(delivered, { status: 'succeeded' }) || issueOutcome(delivered).title !== '外部工作项已关闭') {
  throw new Error('externally closed TAPD delivery must not offer a local retrigger');
}
const closedMR = { id: 'closed-mr', status: 'done', deliveryState: 'merged', deliveryMrState: 'closed' };
if (issueStatusBucket(closedMR) !== 'closed' || !issueMatchesStatusFilter(closedMR, 'closed') || issueMatchesStatusFilter(closedMR, 'done')) {
  throw new Error('a closed MR marker without a link must leave the completed filter');
}
if (issueCurrentStage(closedMR).label !== '已关闭' || issueListProgress(closedMR, null, null).pct !== 0) {
  throw new Error('manual close forged as a closed MR must display closed with zero progress');
}
const linkedClosedMR = { id: 'linked-closed-mr', status: 'done', deliveryState: 'merged', deliveryMrState: 'closed', deliveryMrUrl: 'https://git.example.com/org/repo/merge_requests/2' };
if (issueCurrentStage(linkedClosedMR).label !== 'MR 已关闭' || issueStatusBucket(linkedClosedMR) !== 'closed' || issueListProgress(linkedClosedMR, null, null).pct !== 100) {
  throw new Error('a linked closed MR must stay MR closed in the single stage column');
}
const mergedMR = { id: 'merged-mr', status: 'done', deliveryState: 'merged', deliveryMrState: 'merged', deliveryMrUrl: 'https://git.example.com/org/repo/merge_requests/1' };
if (issueStatusBucket(mergedMR) !== 'done' || issueCurrentStage(mergedMR).label !== 'MR 已合入' || issueMatchesStatusFilter(mergedMR, 'closed')) {
  throw new Error('merged MR must stay in the completed filter under the existing stage column');
}
if (issueOutcome(mergedMR).title !== 'MR 已合入' || !canRetriggerIssue(mergedMR, { status: 'succeeded' })) {
  throw new Error('a delivered but externally open item must keep preview retrigger behavior');
}
const manuallyClosed = { id: 'manually-closed', status: 'closed', deliveryState: 'merged', deliveryMrState: 'integrated' };
if (issueStatusBucket(manuallyClosed) !== 'closed' || issueMatchesStatusFilter(manuallyClosed, 'done')) {
  throw new Error('persisted closed status must be authoritative');
}
if (deliveryLockedLabel(manuallyClosed) !== '关闭' || !reopenDeliveredButton(manuallyClosed).includes('data-reopen-issue')) {
  throw new Error('manual close must display as closed and remain reopenable without fake delivery state');
}
const succeededIssue = { id: 'succeeded-issue', status: 'done' };
state.data.tasks.push({ id: 'succeeded-task', issueId: succeededIssue.id, runId: 'succeeded-run', status: 'completed', createdAt: '2026-09-23T00:00:00Z' });
state.data.runs.push({ id: 'succeeded-run', status: 'succeeded', nodes: {}, createdAt: '2026-09-23T00:00:00Z' });
if (issueCurrentStage(succeededIssue).label !== '交付闭环' || issueListProgress(succeededIssue, state.data.runs.at(-1), null).pct !== 100) {
  throw new Error('persisted completion with a successful run must display delivery closure');
}
const reopenedReview = { id: 'reopened-review', status: 'in_review', deliveryState: 'reopened' };
state.data.tasks.push({ id: 'reopened-task', issueId: reopenedReview.id, runId: 'reopened-run', status: 'completed', createdAt: '2026-09-23T00:00:00Z' });
state.data.runs.push({ id: 'reopened-run', status: 'succeeded', nodes: {}, createdAt: '2026-09-23T00:00:00Z' });
if (issueCurrentStage(reopenedReview).label !== '评审中' || issueStatusBucket(reopenedReview) !== 'in_review' || issueMatchesStatusFilter(reopenedReview, 'done')) {
  throw new Error('a reopened issue must follow its current status instead of the old succeeded run');
}
const statusMatrix = [
  ['backlog', 'backlog'],
  ['todo', 'todo'],
  ['in_progress', 'in_progress'],
  ['in_review', 'in_review'],
  ['done', 'done'],
  ['closed', 'closed'],
  ['blocked', 'blocked'],
  ['cancelled', 'cancelled']
];
for (const [stored, bucket] of statusMatrix) {
  const issue = { id: 'matrix-' + stored, status: stored };
  const matches = ['', 'backlog', 'todo', 'in_progress', 'in_review', 'done', 'closed', 'blocked', 'cancelled'].filter(filter => issueMatchesStatusFilter(issue, filter));
  if (issueStatusBucket(issue) !== bucket || matches.length !== 2 || !matches.includes('') || !matches.includes(bucket)) {
    throw new Error('status bucket is not exclusive for ' + stored + ': ' + matches.join(','));
  }
}
`
		if out, err := exec.Command(node, "-e", nodeScript).CombinedOutput(); err != nil {
			t.Fatalf("renderIssues node execution check failed: %v\n%s", err, out)
		}
	}
}

func TestClarificationConclusionAndWriteBackUX(t *testing.T) {
	appBytes, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	appCode := string(appBytes)

	for _, want := range []string{
		`needs-confirm`,
		`需要确认结论`,
		`确认写回原单（追加评论）`,
		`clarify-next-step-callout`,
		`去确认结论`,
		`data-dispatch-issue`,
	} {
		if !strings.Contains(appCode, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}

	cssBytes, err := os.ReadFile("platform.css")
	if err != nil {
		t.Fatal(err)
	}
	cssCode := string(cssBytes)
	for _, want := range []string{
		`.issue-table-row.needs-confirm`,
		`.kanban-card.needs-confirm`,
		`.issue-progress-cell.confirm`,
	} {
		if !strings.Contains(cssCode, want) {
			t.Fatalf("platform.css missing %q", want)
		}
	}

	node, err := exec.LookPath("node")
	if err == nil {
		nodeScript := `
const fs = require('fs');
const code = fs.readFileSync('` + appBundlePath(t) + `', 'utf8');
const mockEl = () => ({
  dataset: {},
  classList: { add: () => {}, remove: () => {}, toggle: () => {}, contains: () => false },
  appendChild: () => {},
  addEventListener: () => {},
  setAttribute: () => {},
  style: {},
  querySelector: () => mockEl(),
  querySelectorAll: () => []
});
global.window = {
  addEventListener: () => {},
  removeEventListener: () => {},
  innerWidth: 1200,
  innerHeight: 800
};
global.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
global.document = {
  hidden: false,
  addEventListener: () => {},
  documentElement: { dataset: {} },
  querySelector: () => mockEl(),
  querySelectorAll: () => [],
  getElementById: () => mockEl(),
  body: mockEl()
};
global.$ = () => mockEl();
global.$$ = () => [];
global.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
eval(code + '; global.issueRow = issueRow; global.clarifyPane = clarifyPane; global.state = state;');

// 1. Issue with pending confirm
const pendingIssue = {
  id: 'iss-test-1',
  kind: 'requirement',
  source: 'tapd',
  externalRef: { source: 'tapd', externalId: '1001' },
  clarification: { status: 'ready', writeBackStatus: 'pending', score: 84, summary: '测试结论' }
};
state.data = { projects: [], repositories: [], workflows: [], runs: [], tasks: [] };
const rowHtml = issueRow(pendingIssue);
if (!rowHtml.includes('needs-confirm')) {
  console.error('issueRow does not contain needs-confirm:', rowHtml);
  process.exit(1);
}
if (!rowHtml.includes('需要确认结论')) {
  console.error('issueRow does not contain 需要确认结论:', rowHtml);
  process.exit(1);
}

// 2. Clarify pane before write back
const pendingPaneHtml = clarifyPane(pendingIssue, null);
if (!pendingPaneHtml.includes('确认写回原单（追加评论）')) {
  console.error('clarifyPane pending does not contain 确认写回原单（追加评论）:', pendingPaneHtml);
  process.exit(1);
}

// 3. Clarify pane after write back
const succeededIssue = {
  id: 'iss-test-2',
  kind: 'requirement',
  source: 'tapd',
  externalRef: { source: 'tapd', externalId: '1002' },
  clarification: { status: 'ready', writeBackStatus: 'succeeded', score: 84, summary: '测试结论' }
};
const readyPaneHtml = clarifyPane(succeededIssue, null);
if (!readyPaneHtml.includes('data-dispatch-issue="iss-test-2"')) {
  console.error('clarifyPane ready does not contain data-dispatch-issue:', readyPaneHtml);
  process.exit(1);
}
if (!readyPaneHtml.includes('触发需求')) {
  console.error('clarifyPane ready does not contain 触发需求:', readyPaneHtml);
  process.exit(1);
}
// 4. Dispatched task active state handling
const dispatchedIssue = {
  id: 'iss-test-3',
  kind: 'requirement',
  source: 'tapd',
  externalRef: { source: 'tapd', externalId: '1003' },
  clarification: { status: 'ready', writeBackStatus: 'succeeded', score: 84, summary: '测试结论' }
};
const activeTask = { id: 'task-test-3', issueId: 'iss-test-3', status: 'running' };
state.data.tasks = [activeTask];
state.data.runs = [];

const health = issueRunHealth(dispatchedIssue);
if (health !== 'active') {
  console.error('issueRunHealth should be active for running task, got:', health);
  process.exit(1);
}
const stage = issueCurrentStage(dispatchedIssue);
if (stage.key !== 'active') {
  console.error('issueCurrentStage key should be active for running task, got:', stage);
  process.exit(1);
}
`
		if out, err := exec.Command(node, "-e", nodeScript).CombinedOutput(); err != nil {
			t.Fatalf("clarification UX node test failed: %v\n%s", err, out)
		}
	}
}

func TestGenericTemplateEditorUsesBundledRoles(t *testing.T) {
	appBytes, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	app := string(appBytes)
	for _, want := range []string{
		"genericTemplateAgents",
		"hydrateGenericWorkflowRoles",
		"refreshGenericWorkflowRoles",
		"/api/workflows/${encodeURIComponent(id)}/agents",
		"/api/workflows/${encodeURIComponent(state.workflow.id)}/agents",
		"这些角色打包在通用模板里",
		"该角色不属于当前通用模板",
		"通用模板角色",
		"agentWizardSubject",
		"agentWizardForeign",
		"genericTemplateMCPOptions",
		"cloneBtn.hidden=catalogGeneric(state.workflow)||!foreign",
	} {
		if !strings.Contains(app, want) {
			t.Fatalf("app.js is missing generic template role editor wiring %q", want)
		}
	}
	if strings.Contains(app, "function workflowAgents(){const projectId=state.workflow?.projectId||state.projectFilter||'';return (state.data?.agents||[]).filter") {
		t.Fatal("workflowAgents still binds generic templates to the current project catalog")
	}
	if strings.Contains(app, "const foreign=catalogForeign((state.data.agents.find(x=>x.id===state.selectedAgent)||state.agentDraft||{}).projectId)") {
		t.Fatal("agent wizard still treats a same-id project clone as a foreign generic role")
	}
	for _, want := range []string{"renderPlatformAdminPanel", "platformAdminPanel", "谁可以改通用模板"} {
		if !strings.Contains(app, want) {
			t.Fatalf("app.js is missing platform admin panel wiring %q", want)
		}
	}
	if strings.Contains(app, `id="platformAdminCard"`) {
		t.Fatal("platform admin management is still embedded in personal settings")
	}
	htmlBytes, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlBytes), `id="platformAdminPanel"`) {
		t.Fatal("index.html is missing the generic-template platform admin panel")
	}
}

func TestAgentPageListAndTabbedModal(t *testing.T) {
	appBytes, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	app := string(appBytes)
	for _, want := range []string{
		"function agentConfigSummary(",
		"function openAgentRowMenu(",
		"function agentSlugFromName(",
		"function setAgentTab(",
		"function setAgentCreateStep(",
		"function syncTabIndicator(",
		"function persistAgent(",
		"function openAgentWizard(agent=null,initialStep=1)",
		"state.genericTemplateAgents",
		"openAgentWizard(agent,2)",
	} {
		if !strings.Contains(app, want) {
			t.Fatalf("app bundle is missing agent page wiring %q", want)
		}
	}
	if strings.Contains(app, "agent-row-avatar") {
		t.Fatal("agent list rows should not render a text avatar")
	}
	htmlBytes, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	for _, want := range []string{
		`id="agentList" class="agent-list-table"`,
		`class="agent-tabs" data-scope-for="agent"`,
		`data-scope-count="agent-project"`,
		`data-wizard-steps="agent-create"`,
		`data-wizard-steps="agent"`,
		`data-agent-tab="task"`,
		`data-agent-tab="gov"`,
		`id="agentGovCount"`,
		`id="agentAssetsCount"`,
		`id="agentIdPreview"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html is missing agent page markup %q", want)
		}
	}
	cssBytes, err := os.ReadFile("product-v4.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	start := strings.Index(css, "#agentsView .agent-tabs")
	if start < 0 {
		t.Fatal("product-v4.css is missing the agent tab strip styles")
	}
	for _, want := range []string{"#agentsView .agent-row", "#agentModal .agent-tab-ind", ":focus-visible"} {
		if !strings.Contains(css, want) {
			t.Fatalf("product-v4.css is missing agent page style %q", want)
		}
	}
	if strings.Contains(css, ".agent-row-avatar") {
		t.Fatal("product-v4.css still styles the removed agent row avatar")
	}
	if !strings.Contains(css, "#agentsView .agent-row-model.none{color:var(--hu-faint)}") {
		t.Fatal("unselected agent model should use --hu-faint like other empty states")
	}
}

// 管理员名单是平台级权限，既不属于某个项目，也不属于个人凭据，
// 所以它有自己的导航入口，而不是挂在流程模板页上。
func TestPlatformAdminLivesOnItsOwnView(t *testing.T) {
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	source, markup := string(app), string(html)

	for _, want := range []string{
		`id="platformView"`,
		`id="platformNavGroup"`,
		`data-view="platform"`,
	} {
		if !strings.Contains(markup, want) {
			t.Fatalf("index.html is missing the platform admin view wiring %q", want)
		}
	}
	if strings.Contains(markup, `<div id="platformAdminPanel" class="platform-admin-panel" hidden></div>`) {
		t.Fatal("platform admin panel is still embedded in the SOP catalog page")
	}
	for _, want := range []string{
		"platform:'平台管理'",
		"if(name==='platform')renderPlatformAdminPanel()",
		"renderPlatformNav",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("app.js is missing the platform view wiring %q", want)
		}
	}
	// 入口只对平台管理员渲染，未授权的 #/platform 要被挡回去；
	// 后端的 403 是底线，但不能靠它来当第一道门。
	if !strings.Contains(source, "group.hidden=!isPlatformAdmin()") {
		t.Fatal("the platform nav entry is rendered for non-admins")
	}
	if !strings.Contains(source, "if(name==='platform'&&!isPlatformAdmin())name='collaboration'") {
		t.Fatal("a hand-typed #/platform is not blocked for non-admins")
	}
	if strings.Contains(source, "state.sopScope==='generic'&&isPlatformAdmin()") {
		t.Fatal("platform admin visibility is still tied to the generic SOP tab")
	}
}

// 加载失败时若把模板 id 记成「已加载」，那个标记会让后续请求全部短路，
// 门禁就会一直显示成没有配置——这正是它在界面上消失的原因。
func TestGenericRoleHydrationRetriesAfterFailure(t *testing.T) {
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	source := string(app)

	start := strings.Index(source, "async function hydrateGenericWorkflowRoles(")
	if start < 0 {
		t.Fatal("hydrateGenericWorkflowRoles is gone")
	}
	body := source[start:]
	if end := strings.Index(body, "\nasync function refreshGenericWorkflowRoles("); end > 0 {
		body = body[:end]
	}
	failure := strings.Index(body, "}catch(")
	if failure < 0 {
		t.Fatal("hydrateGenericWorkflowRoles no longer handles load failures")
	}
	if strings.Contains(body[failure:], "state.genericTemplateAgentWorkflowId=id") {
		t.Fatal("a failed role load is cached as hydrated, so the gates never come back")
	}
	if !strings.Contains(body[failure:], "clearGenericTemplateAgents()") {
		t.Fatal("a failed role load must reset the hydration marker so it can retry")
	}
}

// 通用模板角色走乐观并发：提交的必须是这次编辑所基于的版本。
// 多加一个 +1 会让服务端每次都判成冲突，而且只有两个管理员同时改时才暴露。
func TestGenericRoleSaveSendsTheVersionItEdited(t *testing.T) {
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	source := string(app)
	if strings.Contains(source, "a.version=(bundled.version||0)+1") {
		t.Fatal("generic role save sends a bumped version, which the store reads as a stale write")
	}
	if !strings.Contains(source, "a.version=bundled.version||0") {
		t.Fatal("generic role save no longer sends the version it based the edit on")
	}
}

func TestRunRetryContextSkipsScriptNodes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping run retry context check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
const start = code.indexOf('function failedNodeId(run){');
const end = code.indexOf('function canManuallyPassFailedNode(');
assert.ok(start >= 0 && end > start);
eval(code.slice(start, end));
global.state = {data:{runs:[],workflows:[{id:'script-wf',nodes:[{id:'script',type:'command'}]}]}};
const scriptFail = {id:'run-1',status:'failed',workflowId:'script-wf',workflowSnapshot:{nodes:[{id:'script',type:'command'}]},nodes:{script:{status:'failed'}}};
const agentFail = {id:'run-2',status:'failed',workflowSnapshot:{nodes:[{id:'agent',type:'agent'}]},nodes:{agent:{status:'failed'}}};
const cancelled = {id:'run-3',status:'cancelled',workflowSnapshot:{nodes:[{id:'agent',type:'agent'}]},nodes:{agent:{status:'failed'}}};
assert.equal(runRetryNeedsContext(scriptFail), false);
assert.equal(runRetryNeedsContext(agentFail), true);
assert.equal(runRetryNeedsContext(cancelled), false);
assert.ok(code.includes("if(!context && !fromEl?.closest?.('[data-human-context-root]') && runRetryNeedsContext(run))"));
assert.ok(code.includes("r.status==='failed'&&!gongfengIssueClosed(ctx.issue)"));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("run retry context check failed: %v\n%s", err, out)
	}
}

func TestPreviewIframeHttpsFallback(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping preview https fallback test")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
assert.ok(code.includes('function previewCandidateHttpsURL('), 'must declare previewCandidateHttpsURL');
assert.ok(code.includes('async function resolvePreviewSessionURL('), 'must declare resolvePreviewSessionURL');
assert.ok(code.includes('session.url=await resolvePreviewSessionURL(session.url)'), 'loadRunPreview must resolve session url');

// Test previewCandidateHttpsURL logic
const start = code.indexOf('function previewCandidateHttpsURL(');
const end = code.indexOf('async function resolvePreviewSessionURL(');
assert.ok(start >= 0 && end > start);
eval(code.slice(start, end));

global.window = { location: { href: 'https://loopforge-test.example.com/runs', protocol: 'https:' } };
assert.equal(previewCandidateHttpsURL('http://loopforge-test.example.com/preview/token/'), 'https://loopforge-test.example.com/preview/token/');
assert.equal(previewCandidateHttpsURL('http://loopforge-test.example.com:80/preview/token/'), 'https://loopforge-test.example.com/preview/token/');
assert.equal(previewCandidateHttpsURL('https://loopforge-test.example.com/preview/token/'), 'https://loopforge-test.example.com/preview/token/');
assert.equal(previewCandidateHttpsURL(''), '');
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("preview https fallback check failed: %v\n%s", err, out)
	}
}
func TestClarifyOptionToggleLogic(t *testing.T) {
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	source := string(app)

	// Verify issue clarify option unselect on second click
	if !strings.Contains(source, "const wasSelected=b.classList.contains('selected');") {
		t.Fatal("issue clarify option does not track wasSelected to toggle off on repeat click")
	}
	if !strings.Contains(source, "if(!wasSelected)b.classList.add('selected');") {
		t.Fatal("issue clarify option does not conditionally add selected")
	}

	// Verify the requirement wizard question card toggles an option off on repeat click
	for _, want := range []string{
		"if(q.multiSelect)selected=selected.includes(option)?selected.filter(x=>x!==option):[...selected,option];",
		"else selected=selected.includes(option)?[]:[option];",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("req question option does not toggle off on repeat click: missing %q", want)
		}
	}

	// Verify CSS distinguishes hover from selected so cancellation is visually obvious
	v4css, err := os.ReadFile("product-v4.css")
	if err != nil {
		t.Fatal(err)
	}
	v4style := string(v4css)
	if strings.Contains(v4style, ".issue-clarify-option:hover,.issue-clarify-option.selected") {
		t.Fatal("issue-clarify-option must not share identical hover and selected styles")
	}
	if !strings.Contains(v4style, ".issue-clarify-option:hover{") || !strings.Contains(v4style, ".issue-clarify-option.selected{") {
		t.Fatal("issue-clarify-option must define separate hover and selected rules")
	}
}

func TestPlatformAdminEnhancedUI(t *testing.T) {
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile("platform.css")
	if err != nil {
		t.Fatal(err)
	}
	source, style := string(app), string(css)

	// Key elements in app.js
	for _, want := range []string{
		"platform-metric-grid",
		"platformMetricTotal",
		"platformMetricLocked",
		"platformMetricGranted",
		"platform-admin-layout",
		"platformAdminSearch",
		"platformFilterTabs",
		"platform-admin-side",
		"userAvatarHTML(row.loginName",
		"data-platform-nav",
		"switchView(target)",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("app.js missing platform admin enhancement: %q", want)
		}
	}

	// Key styles in platform.css
	for _, want := range []string{
		".platform-metric-grid",
		".platform-metric-card",
		".platform-admin-layout",
		".platform-admin-avatar",
		".platform-source-badge",
		".platform-admin-side",
		".platform-locked-tag",
		".platform-revoke-btn",
	} {
		if !strings.Contains(style, want) {
			t.Fatalf("platform.css missing style: %q", want)
		}
	}

	// Verify widescreen full-width adaptive support
	if strings.Contains(style, ".platform-admin-panel{max-width:1380px") || strings.Contains(style, "max-width:1380px;") && strings.Contains(style, ".platform-admin-panel") {
		t.Fatal("platform-admin-panel must not be locked to 1380px fixed max width")
	}
	v4css, err := os.ReadFile("product-v4.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v4css), ".platform-admin-panel") || !strings.Contains(string(v4css), ".platform-admin-layout") {
		t.Fatal("product-v4.css must include platform-admin-panel and platform-admin-layout in full-width adaptive rules")
	}
}

func TestCSSBracesBalanced(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".css") {
			continue
		}
		content, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("failed to read %s: %v", entry.Name(), err)
		}
		open := 0
		for _, b := range content {
			if b == '{' {
				open++
			} else if b == '}' {
				open--
			}
		}
		if open != 0 {
			t.Fatalf("unbalanced braces in %s: open count is %d", entry.Name(), open)
		}
	}
}

func TestRunElapsedShowsExecutionTimeNotWallClock(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
eval(code.slice(code.indexOf('function duration('), code.indexOf('function metricsIssueSource(')));
eval(code.slice(code.indexOf('function runFinishedAt('), code.indexOf('function relativeTime(')));
const hour = 3600000, created = new Date(Date.now() - 45 * hour).toISOString();
const settled = {status:'succeeded', createdAt:created, timing:{executionMs:5*hour, wallMs:45*hour, waitingMs:40*hour, asOf:new Date(Date.now()-hour).toISOString()}};
assert.equal(runElapsed(settled), '5.0h');
assert.match(runWallNote(settled), /端到端 45.0h（等待 40.0h）/);
const waiting = {status:'waiting', createdAt:created, timing:{executionMs:2*hour, wallMs:10*hour, asOf:new Date(Date.now()-hour).toISOString()}};
const w = runTiming(waiting);
assert.equal(w.execution, 2*hour);
assert.ok(w.wall >= 11*hour - 1000, 'wall clock keeps growing while the run is open');
const running = {status:'running', createdAt:created, timing:{executionMs:60000, wallMs:60000, executing:true, asOf:new Date(Date.now()-60000).toISOString()}};
assert.ok(runTiming(running).execution >= 119000, 'a running node keeps accruing execution time');
assert.equal(runWallNote({status:'succeeded', createdAt:created, timing:{executionMs:hour, wallMs:hour+1000, asOf:created}}), '');
const legacy = {status:'succeeded', createdAt:new Date(0).toISOString(), nodes:{a:{finishedAt:new Date(90000).toISOString()}}};
assert.equal(runElapsed(legacy), '1.5m');
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("run timing display failed: %v\n%s", err, out)
	}
}

func TestHumanGateDisclosureOnDecisionSurfaces(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
const escapeHTML = value => String(value??'').replace(/[&<>'"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const isScriptWorkflowNode = node => node?.type==='script'||node?.type==='command';
const cachedRun = () => null;
eval(code.slice(code.indexOf('function turnsExhausted('), code.indexOf('function failedNodeId(')));
eval(code.slice(code.indexOf('function approvalCalloutHTML('), code.indexOf('async function retryWaitingNode(')));
const node = {id:'summary', name:'交付汇总', type:'agent'};
const nr = {status:'waiting_approval', activity:'等待人工确认', gateResult:{
  decision:'human',
  actionRequired:'请打开流水线 <build> 判断失败原因',
  reason:'合流检查 block=true',
  summary:'MR 559 未合并',
  findings:[{title:'合流检查失败', evidence:'build 241981168'}]
}};
const html = humanDecisionPanelHTML('run-1','summary',nr,node,[node]);
assert.match(html, /human-gate-brief/);
assert.match(html, /需要确认/);
assert.match(html, /请打开流水线 &lt;build&gt; 判断失败原因/);
assert.match(html, /Agent 判断依据/);
assert.match(html, /合流检查 block=true/);
assert.match(html, /摘要/);
assert.match(html, /MR 559 未合并/);
assert.match(html, /待确认项与证据/);
assert.match(html, /合流检查失败/);
assert.match(html, /build 241981168/);
assert.match(html, /确认通过，继续下游/);
const callout = approvalCalloutHTML(node, nr, [node]);
assert.match(callout, /需要确认/);
assert.match(callout, /请打开流水线 &lt;build&gt; 判断失败原因/);
assert.match(callout, /等待人工确认/);
const bare = humanDecisionPanelHTML('run-1','summary',{status:'waiting_approval'},node,[node]);
assert.match(bare, /判断依据/);
assert.match(bare, /门禁未通过。请看产物和日志后决定。/);
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("human gate disclosure failed: %v\n%s", err, out)
	}
}

func TestApprovalCardLayout(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
const escapeHTML = value => String(value??'').replace(/[&<>'"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const isScriptWorkflowNode = node => node?.type==='script'||node?.type==='command';
const relativeTime = () => '8 小时';
const runs = {};
const cachedRun = id => runs[id]||null;
const state = {data:{tasks:[{runId:'run-1',issueId:'iss-1'}], issues:[{id:'iss-1',title:'移除多余分组标题',projectId:'p1',number:31}], projects:[{id:'p1',key:'AF'}], workflows:[]}};
eval(code.slice(code.indexOf('function huiIcon('), code.indexOf('// stage.key alone is not enough')));
eval(code.slice(code.indexOf('function waitingNodeId('), code.indexOf('function failedNodeId(')));
eval(code.slice(code.indexOf('function restoredSOPResumeNode('), code.indexOf('function approvalCalloutHTML(')));
eval(code.slice(code.indexOf('function approvalStatusBadgeHTML('), code.indexOf('function closeApprovalMoreMenus(')));
const between = (html, from, to) => html.slice(html.indexOf(from), html.indexOf(to, html.indexOf(from)));
const node = {id:'gate', name:'confirm-brief', type:'agent'};
const waiting = {id:'run-1', status:'waiting', workflowSnapshot:{nodes:[node]}, nodes:{gate:{status:'waiting_approval'}}};
runs['run-1'] = waiting;
const html = approvalCardHTML(waiting);
const head = between(html, 'class="ap-card-head"', 'class="ap-body"');
const side = between(html, 'class="ap-side"', 'class="ap-body"');
const body = html.slice(html.indexOf('class="ap-body"'));
assert.match(side, /data-open-issue="iss-1"/);
assert.match(side, /class="ap-more-menu" role="menu" hidden/);
assert.match(between(side, 'class="ap-more-menu"', '</div>'), /data-cancel-run="run-1"/);
assert.match(side, /data-ap-more[^>]*aria-haspopup="menu" aria-expanded="false"/);
assert.ok(!body.includes('data-cancel-run'), 'cancel lives in the ⋯ menu, not the body');
assert.match(body, /data-approve="run-1"/);
assert.ok(!head.includes('data-approve'), 'decision buttons stay in the body');
assert.match(head, /class="hui-badge" data-tone="warn"/);
assert.match(head, /data-icon="pending-review"/);
assert.match(head, /<code class="ap-key">AF-31<\/code>/);
assert.match(head, /卡在「confirm-brief」/);
const paused = {id:'run-2', status:'waiting', pauseRequested:true, workflowSnapshot:{nodes:[node]}, nodes:{gate:{status:'waiting_approval', activity:'用户暂停'}}};
runs['run-2'] = paused;
const pausedHTML = approvalCardHTML(paused);
assert.match(pausedHTML, /class="hui-badge" data-tone="neutral"[^>]*><svg[^>]*data-icon="pause"/);
assert.match(pausedHTML, /已暂停/);
assert.match(pausedHTML, /data-open-logs="run-2"/);
assert.match(pausedHTML, /class="ap-body"><div class="approval-callout paused-run-callout"/);
const queued = {id:'run-3', status:'queued', workflowSnapshot:{nodes:[node]}, nodes:{}};
runs['run-3'] = queued;
const queuedHTML = approvalCardHTML(queued);
assert.ok(!queuedHTML.includes('ap-body'), 'queued runs have no decision body');
assert.match(queuedHTML, /data-cancel-run="run-3"/);
state.approvalItems=[waiting,{id:'run-4',status:'waiting',workflowSnapshot:{nodes:[node]},nodes:{gate:{status:'waiting_approval'}}}];
assert.match(approvalCardHTML(waiting), /data-ap-select="run-1"/);
assert.ok(!pausedHTML.includes('data-ap-select'), 'paused runs are not batch-selectable');
const batchBar=approvalBatchBarHTML(state.approvalItems);
assert.match(batchBar, /data-ap-select-all/);
assert.match(batchBar, /批量通过/);
assert.match(batchBar, /data-ap-batch-approve disabled/);
approvalSelection().add(approvalKey('run-1','gate'));
const selectedBar=approvalBatchBarHTML(state.approvalItems);
assert.match(selectedBar, /已选 1 条/);
assert.match(selectedBar, /data-ap-batch-approve>/);
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("approval card layout failed: %v\n%s", err, out)
	}
}

func TestApprovalsStylesheetStaysScopedToHarnessTokens(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`<link rel="stylesheet" href="([^"?]+)`).FindAllStringSubmatch(string(html), -1)
	if len(links) == 0 || links[len(links)-1][1] != "/app/approvals-harness.css" {
		t.Fatalf("/app/approvals-harness.css must load after every other stylesheet, got %v", links)
	}
	css, err := os.ReadFile("app/approvals-harness.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(css), "\n") {
		selector := strings.TrimSpace(line)
		if !strings.HasSuffix(selector, "{") || strings.HasPrefix(selector, "@") || strings.HasPrefix(selector, "from") || strings.HasPrefix(selector, "to") || strings.Contains(selector, "%") {
			continue
		}
		for _, part := range splitSelectorList(strings.TrimSuffix(selector, "{")) {
			if part != "" && !strings.HasPrefix(part, "#approvalsView") && !strings.HasPrefix(part, `html[data-theme="dark"] #approvalsView`) {
				t.Fatalf("approvals-harness.css rule %q escapes #approvalsView", part)
			}
		}
	}
	body := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(css), "")
	if hex := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`).FindString(body); hex != "" {
		t.Fatalf("approvals-harness.css hard-codes %s; use --hui-* tokens", hex)
	}
	for _, want := range []string{"#approvalsView .ap-buttons .button.primary { order: 1; }", "justify-content: flex-end", "@media (max-width: 960px)", "prefers-reduced-motion"} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("approvals-harness.css is missing %q", want)
		}
	}
}

func TestNotificationEditorsSeparateNodeRemindersAndSOPReports(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
 const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
 vm.runInThisContext(fs.readFileSync('app/notification-editor.js','utf8'));
 global.escapeHTML=x=>String(x);
 nodeBotCache.set('p',{bots:[{id:'b',name:'研发群',enabled:true,target:{name:'研发群',type:'group'}}]});
 const binding={botId:'b',scope:'node',events:['waiting','failed']};
 const wf={nodes:[{notifications:[{...binding,scope:'run',events:['succeeded'],contentMode:'ai'}]}]};
 normalizeWorkflowNotifications(wf);
 assert.equal(wf.nodes[0].notifications,undefined);assert.equal(wf.notifications.length,1);
 const sop=sopNotificationHTML(wf.notifications,{sourceType:'run',kind:'sop',projectId:'p'});
 assert.match(sop,/汇总所有节点/);assert.ok(!sop.includes("data-sop-report-model"));assert.match(sop,/本次 SOP 执行身份/);assert.match(sop,/data-sop-notification-bot/);assert.match(sop,/自定义完成汇报/);assert.ok(!sop.includes('data-notification-mode'));assert.ok(!sop.includes('<details open'));
 assert.match(sop,/接收者看到的消息示例/);assert.match(sop,/＋ 需求名称/);assert.match(sop,/用推荐格式/);
 assert.equal(recommendedSOPNotificationTemplate,'需求「{{issue.title}}」已完成\n\n{{ai.body}}\n\n{{url}}');
 const saved=sopNotificationHTML([{botId:'b',scope:'run',template:recommendedSOPNotificationTemplate}],{projectId:'p'});
 assert.ok(saved.includes('{{issue.title}}'));assert.ok(saved.includes('{{ai.body}}'));assert.match(saved,/＋ 需求名称/);
 const disabled=sopNotificationHTML([],{projectId:'p'});assert.match(disabled,/不启用通知/);assert.ok(!disabled.includes('data-sop-notification-custom'));
 const legacy={nodes:[{notifications:[binding]}]};normalizeWorkflowNotifications(legacy);assert.equal(legacy.notifications[0].botId,'b');assert.equal(legacy.notifications[0].contentMode,'ai');
 const explicit={notifications:[{botId:'b',scope:'run'}],nodes:[{notifications:[{botId:'other',scope:'node'}]}]};normalizeWorkflowNotifications(explicit);assert.equal(explicit.notifications.length,1);assert.equal(explicit.nodes[0].notifications,undefined);
 assert.ok(!sop.includes('data-notification-event='));assert.ok(!sop.includes('data-notification-scope'));
 const task=notificationBindingsHTML([{botId:'b',events:['succeeded','failed']}],{sourceType:'schedule_job',projectId:'p'});
 assert.match(task,/执行成功/);assert.match(task,/执行失败/);assert.match(task,/data-notification-mode/);
 `
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
}

func TestCodexStyleLeftMiniRailLayout(t *testing.T) {
	pageBytes, err := Files.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageBytes)

	// 1. 验证左侧小栏 platformRail 存在，且位于 app-shell 第一项
	railIdx := strings.Index(page, `id="platformRail"`)
	sidebarIdx := strings.Index(page, `class="platform-sidebar"`)
	if railIdx < 0 {
		t.Fatal("index.html must have #platformRail as left mini sidebar")
	}
	if sidebarIdx < 0 || railIdx > sidebarIdx {
		t.Fatal("#platformRail must sit before .platform-sidebar in DOM")
	}

	// 2. 左侧小栏只留 4 个日常入口和设置；流程模板、运行记录、定时任务收进设置导航。
	for _, view := range []string{"collaboration", "observability", "issues", "approvals", "settings"} {
		needle := `data-view="` + view + `"`
		sub := page[railIdx:sidebarIdx]
		if !strings.Contains(sub, needle) {
			t.Fatalf("#platformRail must contain %s nav button", needle)
		}
	}
	for _, view := range []string{"sops", "runs", "aiSchedules"} {
		needle := `data-view="` + view + `"`
		if strings.Contains(page[railIdx:sidebarIdx], needle) {
			t.Fatalf("#platformRail must not contain %s; it lives in the settings navigation", needle)
		}
		if !strings.Contains(page[sidebarIdx:], needle) {
			t.Fatalf("settings navigation must contain %s", needle)
		}
	}
	// 图标栏第 2 位仍是统计；需求列表紧跟其后，两者都是日常工作入口。
	railNav := page[railIdx:sidebarIdx]
	if !strings.Contains(railNav, `data-view="issues" data-tooltip="需求列表" aria-label="需求列表"`) {
		t.Fatal("#platformRail must contain the issues entry labelled 需求列表")
	}
	if !strings.Contains(railNav, `data-view="observability" data-tooltip="统计" aria-label="统计"`) {
		t.Fatal("#platformRail observability button must be labelled 统计")
	}
	if strings.Index(railNav, `data-view="observability"`) > strings.Index(railNav, `data-view="issues"`) {
		t.Fatal("统计 must stay before 需求列表 in #platformRail")
	}
	if strings.Index(railNav, `data-view="issues"`) > strings.Index(railNav, `data-view="approvals"`) {
		t.Fatal("需求列表 must sit before 待我确认 in #platformRail")
	}
	if strings.Index(railNav, `data-view="observability"`) > strings.Index(railNav, `data-view="approvals"`) {
		t.Fatal("统计 must sit before 待我确认 in #platformRail")
	}
	if strings.Contains(page[sidebarIdx:], `data-view="observability"`) {
		t.Fatal("settings navigation must not duplicate the 统计 entry")
	}
	if !strings.Contains(page[sidebarIdx:], `data-view="runners"`) {
		t.Fatal("settings navigation must contain the runners entry")
	}

	// 3. 待我确认按钮包含审批徽标，且底部包含完整个人信息与弹窗入口
	railContent := page[railIdx:sidebarIdx]
	if !strings.Contains(railContent, `id="approvalNavBadge"`) {
		t.Fatal("#platformRail approvals button must contain #approvalNavBadge")
	}
	if !strings.Contains(railContent, `id="userProfileWrap"`) || !strings.Contains(railContent, `id="userProfilePopover"`) {
		t.Fatal("#platformRail must host #userProfileWrap and #userProfilePopover at bottom")
	}

	// 4. 验证 CSS 定义了 --rail-w 且 app-shell 拥有三列布局
	cssBytes, err := Files.ReadFile("product-v4.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	if !strings.Contains(css, "--rail-w") {
		t.Fatal("product-v4.css must define --rail-w")
	}
	if !strings.Contains(css, "grid-template-columns: var(--rail-w) var(--sidebar-w) minmax(0, 1fr)") {
		t.Fatal("product-v4.css .app-shell must start with var(--rail-w)")
	}
	if !strings.Contains(css, ".platform-rail") {
		t.Fatal("product-v4.css must contain .platform-rail styles")
	}

	// 5. 验证已移除冗余的配置与管理 overview 页面与中间栏按钮，以及设置/执行机器的无侧栏模式
	if strings.Contains(page, `id="managementView"`) {
		t.Fatal("index.html must not contain #managementView overview page")
	}
	middleSidebarContent := page[sidebarIdx:]
	mainIdx := strings.Index(middleSidebarContent, `<main>`)
	if mainIdx > 0 && strings.Contains(middleSidebarContent[:mainIdx], `data-view="management"`) {
		t.Fatal(".platform-sidebar must not contain data-view=\"management\" entry")
	}
	if !strings.Contains(css, "body.no-sidebar .platform-sidebar") {
		t.Fatal("product-v4.css must support body.no-sidebar to hide platform-sidebar in settings/runners")
	}

	// 6. 验证个人信息弹窗已精简：仅保留退出登录按钮
	workbenchBytes, err := Files.ReadFile("app/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	wb := string(workbenchBytes)
	if strings.Contains(wb, "openPersonalSettings") {
		t.Fatal("user profile popover should not have redundant openPersonalSettings button")
	}
	if strings.Contains(wb, "uppReAuthBtn") {
		t.Fatal("user profile popover should not have redundant uppReAuthBtn button")
	}
	if !strings.Contains(wb, "uppLogoutBtn") {
		t.Fatal("user profile popover must retain uppLogoutBtn")
	}

	// 7. 验证中间侧边栏移除了与左上角重复的 Logo，且 app.js 在需求列表与运行记录等视图中自动隐藏对话侧栏
	if strings.Contains(middleSidebarContent[:mainIdx], `class="brand"`) {
		t.Fatal(".platform-sidebar must not contain .brand (duplicated with mini rail logo)")
	}
	appJsBytes, err := Files.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	appJs := string(appJsBytes)
	if !strings.Contains(appJs, "const noSidebarViews = ['issues', 'approvals', 'observability'];") {
		t.Fatal("app.js must hide the sidebar on the rail-only pages: 需求列表, 待我确认, 统计")
	}
	managed := appJs[strings.Index(appJs, "const settingsManagementViews="):]
	managed = managed[:strings.Index(managed, "\n")]
	if strings.Contains(managed, "'observability'") {
		t.Fatal("统计 must not enter settings management mode")
	}
	for _, view := range []string{"'sops'", "'runs'", "'aiSchedules'", "'bots'"} {
		if !strings.Contains(managed, view) {
			t.Fatalf("%s moved into the settings sidebar and must enter settings management mode", view)
		}
	}
	if !strings.Contains(appJs, "observability:'统计'") {
		t.Fatal("viewTitles.observability must be 统计")
	}
	accessBytes, err := Files.ReadFile("app/access.js")
	if err != nil {
		t.Fatal(err)
	}
	access := string(accessBytes)
	for _, want := range []string{"['observability','统计','用量分析 · Token 与质量']", "['issues','需求列表'"} {
		if !strings.Contains(access, want) {
			t.Fatalf("command palette is missing %q", want)
		}
	}
}

func TestApprovalBadgeRefreshesOutsideApprovals(t *testing.T) {
	read := func(name string) string {
		data, err := Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if !strings.Contains(read("app.js"), "switchView(view);\n  settleBootLoader();\n  if(!['approvals','issues','runs','sops'].includes(state.currentView))preloadApprovalBadge();") {
		t.Fatal("boot must preload the approval badge when the first view does not fetch runs")
	}
	if !strings.Contains(read("app/approvals.js"), "async function preloadApprovalBadge(){try{const runs=await api('/api/runs',{silent:true});") {
		t.Fatal("preloadApprovalBadge must fetch /api/runs silently")
	}
	for file, want := range map[string]string{
		"app/issues.js": "state.data.runs=runs;updateApprovalBadge();renderIssues();",
		"app/runs.js":   "state.data.runs=list;updateApprovalBadge();renderRuns();",
		"app/sops.js":   "state.data.runs=runs;updateApprovalBadge();renderSOPCatalog()",
	} {
		if !strings.Contains(read(file), want) {
			t.Fatalf("%s must refresh the approval badge after loading runs: missing %q", file, want)
		}
	}
}
