package web

import (
	"io/fs"
	"os"
	"os/exec"
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
	for _, want := range []string{"https://github.com/settings/tokens", "https://www.codebuddy.ai/", "credential-description", "git-token-verify"} {
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
const requirementNeedsClarification = () => false;
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

func TestMetricsPhaseAverageExcludesMissingSamples(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
eval(code.slice(code.indexOf('function metricsDeliveryPhases('), code.indexOf('function metricsDashboardModel(')));
const rows = [
  {phases:{testing:0,samples:{testing:0}}},
  {phases:{testing:60000,samples:{testing:1}}},
  {phases:{testing:0,samples:{testing:1}}},
];
assert.deepEqual(metricsPhaseAverage(rows, 'testing'), {value:30000,samples:2});
assert.deepEqual(metricsPhaseAverage(rows.slice(0,1), 'testing'), {value:null,samples:0});
const missingStage=metricsDeliveryPhases({clarificationDurationMs:3600000,developmentDurationMs:1200000,cycleDurationMs:9000000,phaseSamples:{clarification:1,development:1}});
assert.deepEqual(missingStage.samples, {clarification:1,development:1,testing:0,total:0});
assert.equal(missingStage.total, null);
const complete=metricsDeliveryPhases({clarificationDurationMs:3600000,developmentDurationMs:1200000,testDurationMs:600000,cycleDurationMs:9000000,phaseSamples:{clarification:1,development:1,testing:1}});
assert.equal(complete.total, complete.clarification+complete.development+complete.testing);
assert.equal(complete.total, 5400000);
assert.deepEqual(metricsPhaseAverage([{phases:missingStage},{phases:complete}], 'total'), {value:5400000,samples:1});
const observedZero=metricsDeliveryPhases({phaseSamples:{clarification:1,development:1,testing:1}});
assert.equal(observedZero.total, 0);
assert.equal(metricsPhaseAverage([{phases:observedZero}], 'total').value, 0);
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("phase sample behavior failed: %v\n%s", err, out)
	}
}

func TestMetricsDashboardTotalUsesCompletePhaseSamples(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
const METRICS_WINDOWS = {'7d':{days:7,label:'近 7 天',grain:'day'}};
const now = new Date().toISOString();
const issues = [1,2].map(n=>({id:'issue-'+n,projectId:'project',source:'local',kind:'requirement',status:'done',createdAt:now,updatedAt:now}));
const deliveries = [
  {issueId:'issue-1',lastRunAt:now,firstRunAt:now,clarificationDurationMs:600000,developmentDurationMs:1200000,testDurationMs:1800000,cycleDurationMs:7200000,phaseSamples:{clarification:1,development:1,testing:1}},
  {issueId:'issue-2',lastRunAt:now,firstRunAt:now,clarificationDurationMs:2400000,developmentDurationMs:3000000,cycleDurationMs:18000000,phaseSamples:{clarification:1,development:1,testing:0}},
];
const state = {metricsRange:'7d',metricsData:{deliveries,runTraces:[],nodes:[],summary:{}},metricsIssues:issues,metricsDeploymentData:null};
function currentProject(){return {id:'project'}}
eval(code.slice(code.indexOf('function metricsWindow('), code.indexOf('function metricsSnapshotTrend(')));
const model = metricsDashboardModel();
assert.equal(model.delivered, 2);
assert.deepEqual(model.phaseTotalValues, [3600000]);
assert.equal(model.averagePhaseTotalMs, 3600000);
assert.deepEqual(model.phaseAverages.total, {value:3600000,samples:1});
assert.equal(model.phaseAverages.total.value, model.phaseAverages.clarification.value+model.phaseAverages.development.value+model.phaseAverages.testing.value);
assert.equal(model.requirements.find(item=>item.issueId==='issue-2').phases.total, null);
assert.equal(model.phaseTrend.at(-1).total, 3600000);
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("dashboard phase total check failed: %v\n%s", err, out)
	}
}

func TestMetricsSnapshotAndCachedDataStayWithinSelectedProject(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
const state = {
  projectFilter:'lightvela', metricsRange:'30d', metricsData:{deliveries:[]},
  metricsDataKey:'skillhub|30d||', metricsLoading:false, metricsError:'',
  metricsBenchmark:{projectId:'skillhub',gongfeng:{weeklyMerged:{'2026-W40':425},totals:{merged:425}},loop:{totalRuns:10,firstPassRate:64,issues:10,uniqueIssues:8,completedIssues:5,totalTokens:500}},
};
function metricsProjectId(){return state.projectFilter}
function metricsQueryKey(){return [state.projectFilter,state.metricsRange,'',''].join('|')}
function metricsWindow(){return {label:'近 30 天'}}
function bindMetricsDateControls(){}
function obsCategoryLabel(value){return value}
const body={innerHTML:''},live={innerHTML:''};
function $(selector){return selector==='#metricsBody'?body:selector==='#metricsLiveState'?live:null}
function metricsDashboardModel(){throw new Error('stale data must not be rendered')}
eval(code.slice(code.indexOf('function metricsSnapshotTrend('),code.indexOf('function metricsPrimaryKPI(')));
const empty={delivered:0,requirementIssues:[],aiIssueIds:new Set(),trend:[],completed:[],firstPass:[],terminal:0,deployment:{samples:0},averagePhaseTotalMs:null,firstPassRate:0,runSuccessRate:0,coverageRate:0,averageTokens:0,reworkRate:0,data:{categories:[]}};
const lightvela=metricsDisplayModel(empty);
assert.equal(lightvela.isSnapshot,false);
assert.equal(lightvela.deliveryCount,0);
assert.equal(lightvela.firstPassRate,null);
assert.equal(lightvela.coverageRate,null);
assert.equal(lightvela.averageTokens,null);
state.projectFilter='skillhub';
const skillhub=metricsDisplayModel(empty);
assert.equal(skillhub.isSnapshot,true);
assert.equal(skillhub.deliveryCount,425);
state.projectFilter='lightvela';
eval(code.slice(code.indexOf('function renderMetricsDashboard('),code.indexOf('let metricsRequestSeq=')));
renderMetricsDashboard();
assert.match(body.innerHTML,/正在切换到当前项目的数据/);
state.metricsLoading=true;
renderMetricsDashboard();
assert.match(body.innerHTML,/正在加载当前项目的研发效能数据/);
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("metrics project isolation failed: %v\n%s", err, out)
	}
}

func TestMetricsRefreshIgnoresPreviousProjectResponse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
const state={projectFilter:'lightvela',metricsRange:'30d',metricsDateFrom:'',metricsDateTo:'',metricsData:null,metricsIssues:[],metricsBenchmark:null,metricsLoading:false,metricsError:'',metricsDeploymentData:null,metricsDeploymentLoading:false};
function currentProject(){return {id:state.projectFilter}}
function metricsWindow(){return {from:new Date('2026-09-01T00:00:00Z'),to:new Date('2026-10-01T00:00:00Z'),grain:'day'}}
function renderMetricsDashboard(){}
function metricsDemoEnabled(){return false}
function canShowSkillhubEfficiency(){return true}
function deploymentRepositories(){return []}
const calls=[];let resolveOld;
function api(path){calls.push(path);if(path.startsWith('/api/analytics?')&&path.includes('projectId=lightvela'))return new Promise(resolve=>{resolveOld=resolve});if(path.startsWith('/api/analytics?'))return Promise.resolve({deliveries:[]});if(path.startsWith('/api/issues?'))return Promise.resolve([]);return Promise.resolve({projectId:'skillhub',gongfeng:{weeklyMerged:{'2026-W40':425}}})}
eval(code.slice(code.indexOf('let metricsRequestSeq='),code.indexOf('function metricHealth(')));
async function check(){
  const old=refreshMetrics();
  assert.equal(calls.some(path=>path.startsWith('/api/analytics/skillhub')),false);
  state.projectFilter='skillhub';invalidateMetricsForProjectChange();
  resolveOld({deliveries:[{issueId:'old-project'}]});
  await old;
  assert.equal(state.metricsData,null);
  await refreshMetrics();
  assert.equal(state.metricsBenchmark.projectId,'skillhub');
  state.projectFilter='lightvela';invalidateMetricsForProjectChange();
  assert.equal(state.metricsBenchmark,null);
  assert.equal(state.metricsData,null);
}
check().catch(error=>{console.error(error);process.exitCode=1});
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("metrics request isolation failed: %v\n%s", err, out)
	}
}

func TestPlatformMetricsRequirementIdentity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
eval(code.slice(code.indexOf('function metricsIssueSource('), code.indexOf('function metricsIssueDone(')));
const unbound = {id:'local-1',projectId:'p',source:'local',kind:'requirement'};
const bound = {id:'local-2',projectId:'p',source:'local',kind:'requirement',tapdId:'1001'};
const synced = {id:'tapd-1',projectId:'p',source:'tapd',kind:'requirement',externalRef:{source:'tapd',externalId:'1001'}};
assert.equal(metricsRequirementIssue(unbound), true);
assert.equal(metricsRequirementKey(unbound), 'issue:local-1');
assert.equal(metricsRequirementKey(bound), metricsRequirementKey(synced));
assert.equal(metricsRequirementIssue({id:'bug',source:'gongfeng',kind:'issue'}), false);
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("platform requirement metrics behavior failed: %v\n%s", err, out)
	}
	page, err := fs.ReadFile(Files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name="syncTarget" value="local"`, `name="tapdId"`} {
		if !strings.Contains(string(page), want) {
			t.Fatalf("platform requirement editor is missing %q", want)
		}
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

func TestAssistantJSParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping assistant.js parse check")
	}
	out, err := exec.Command(node, "--check", "assistant.js").CombinedOutput()
	if err != nil {
		t.Fatalf("assistant.js failed to parse: %v\n%s", err, out)
	}
}

func TestAssistantSidebarResize(t *testing.T) {
	js, err := os.ReadFile("assistant.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile("assistant.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`class="ai-resize"`,
		`agentflow.assistantWidth`,
		`setPointerCapture`,
		`--ai-panel-w`,
		`collapseThinking`,
		`pushThought`,
	} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("assistant.js missing %q", want)
		}
	}
	for _, want := range []string{
		`.ai-resize`,
		`cursor:col-resize`,
		`body.ai-assistant-resizing`,
		`var(--ai-panel-w)`,
	} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("assistant.css missing %q", want)
		}
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
	} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("rule global picker is missing %q", want)
		}
	}
	for _, want := range []string{".rule-global-block", ".rule-global-input:checked", ".rule-global-sops"} {
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
const fragment = '"knot": {\u00a0"url": "[http://mcp.example.com/open/mcp](http://mcp.example.com/open/mcp)", "headers": {"x-api-token": "secret"}}';
const pasted = mcpImportCandidates(mcpParseJSONText(fragment));
assert.equal(pasted[0].id, 'knot');
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

func TestMCPClientConfigsAreClientNative(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping MCP client config check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
eval(code.slice(code.indexOf('function accessMcpClientSpec('), code.indexOf('// 对外展示的固定接入地址')));
const base = 'http://loopforge.example.com';
const token = '<YOUR_PERSONAL_TOKEN>';
const claude = accessMcpClientSpec('claude', base, token);
assert.equal(claude.location, 'Claude CLI（用户级配置写入 ~/.claude.json）');
assert.ok(claude.code.includes('claude mcp add --transport http --scope user'));
assert.ok(claude.code.includes('--header "Authorization: Bearer <YOUR_PERSONAL_TOKEN>"'));
const codex = accessMcpClientSpec('codex', base, token);
assert.equal(codex.location, '~/.codex/config.toml');
assert.ok(codex.code.includes('[mcp_servers.agentflow]'));
assert.ok(codex.code.includes('http_headers = { Authorization ='));
assert.ok(!codex.code.includes('mcpServers'));
const cursor = accessMcpClientSpec('cursor', base, token);
assert.ok(cursor.code.includes('"mcpServers"'));
const prompt = accessMcpAgentPrompt(codex, base, token);
assert.equal(prompt, '');
const readyPrompt = accessMcpAgentPrompt(codex, base, 'afp_test_token');
assert.ok(readyPrompt.includes('使用 curl 下载 安装流程文件, 禁止使用 fetch：http://loopforge.example.com/mcp-setup.txt'));
assert.ok(!readyPrompt.includes('当前客户端'));
assert.ok(readyPrompt.includes('API_TOKEN = afp_test_token'));
assert.ok(readyPrompt.includes('ENDPOINT = http://loopforge.example.com/mcp'));
assert.ok(code.includes('async function createQuickAccessPrompt('));
assert.ok(code.includes('MCP 快速接入 · ${spec.label}'));
assert.ok(code.includes('expiresInDays:90'));
assert.ok(code.includes("data-create-token-first]')?.addEventListener('click',e=>createQuickAccessPrompt"));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("MCP client config check failed: %v\n%s", err, out)
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
	embedSource, err := os.ReadFile("embed.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(embedSource), "mcp-setup.txt") {
		t.Fatal("mcp-setup.txt is not embedded in the web bundle")
	}
}

func TestAccessCapabilitiesGroupingIsCallableAndFailuresAreVisible(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping capabilities UI check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
assert.ok(!code.includes('+function accessGroup('));
eval(code.slice(code.indexOf('function accessGroup('), code.indexOf('function accessCode(')));
assert.equal(accessGroup({name: 'runs_get'}), '运行与审批');
assert.equal(accessGroup({name: 'projects_list'}), '项目与仓库');
assert.ok(code.includes('能力清单加载失败'));
assert.ok(code.includes('data-retry-capabilities'));
assert.ok(code.includes('正在加载能力清单'));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("capabilities UI check failed: %v\n%s", err, out)
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
	for _, want := range []string{`id="guideView"`, `data-access-tab="mcp">MCP 快速接入`, `data-access-tab="tokens"`, `data-access-tab="capabilities"`, `data-access-tab="api"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("unified guide is missing %q", want)
		}
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

func TestLocalhostDoesNotAutoPassportRedirect(t *testing.T) {
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
const loginUrl = "https://passport.example.com/modules/passport/signin.ashx";
const cases = [
  [{isGuest:true, hostname:"localhost", loginUrl}, false],
  [{isGuest:true, hostname:"127.0.0.1", loginUrl}, false],
  [{isGuest:true, hostname:"[::1]", loginUrl}, false],
  [{isGuest:true, hostname:"dns-afp.example.com", loginUrl}, false],
  [{isGuest:true, hostname:"loopforge.example.com", loginUrl}, false],
  [{isGuest:true, hostname:"dns-afp.example.com", loginUrl, bypassAutoAuth:true}, false],
  [{isGuest:true, hostname:"dns-afp.example.com", loginUrl, authErr:"exchange_failed"}, false],
  [{isGuest:false, hostname:"dns-afp.example.com", loginUrl}, false],
];
for (const [input, want] of cases) {
  const got = shouldAutoPassportRedirect(input);
  if (got !== want) {
    console.error(JSON.stringify(input), "got", got, "want", want);
    process.exit(1);
  }
}
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("localhost passport redirect check failed: %v\n%s", err, out)
	}
}

func TestCredentialClearButtons(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping credential UI check")
	}
	script := `
const assert = require('node:assert/strict');
const code = require('node:fs').readFileSync('` + appBundlePath(t) + `', 'utf8');
eval(code.slice(code.indexOf('function credentialClearAction('), code.indexOf('async function renderPersonalSettings(')));
for (const name of ['taihuPat', 'gitToken', 'codeBuddyApiKey', 'codexApiKey', 'claudeCodeApiKey']) {
  for (const configured of [false, true]) {
    const html = credentialClearAction(name, configured);
    assert.ok(html.includes('type="button"'));
    assert.ok(!html.includes('checkbox'));
    const input = {value: 'unsaved-draft', disabled: false};
    const status = {dataset: {configured: String(configured)}, textContent: ''};
    const button = {
      dataset: {clearCredential: name, clearLabel: 'API Key', pendingClear: 'false'}
    };
    const submit = {disabled: false};
    const form = {
      elements: {[name]: input},
      querySelectorAll() { return [button]; },
      querySelector(selector) {
        if (selector === 'button[type="submit"]') return submit;
        return selector.includes('data-credential-status') ? status : button;
      }
    };
    bindCredentialClearButtons(form);
    assert.equal(credentialClearPending(form, name), false);
    button.onclick();
    assert.equal(credentialClearPending(form, name), true);
    assert.equal(input.disabled, true);
    assert.equal(status.textContent, '待清除，保存后生效');
    assert.equal(button.textContent, '撤销清除');
    submit.disabled = true;
    button.onclick();
    assert.equal(credentialClearPending(form, name), true, 'save in progress must freeze clear state');
    submit.disabled = false;
    button.onclick();
    assert.equal(credentialClearPending(form, name), false);
    assert.equal(input.disabled, false);
    assert.equal(input.value, 'unsaved-draft', 'undo must preserve the draft');
    assert.equal(status.textContent, configured ? '当前已配置' : '尚未配置');
    assert.equal(button.textContent, '清除 API Key');
  }
}
assert.ok(!code.includes('通常包含 /v1'));
assert.ok(!code.includes('不含 /v1/messages'));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("credential clear buttons failed: %v\n%s", err, out)
	}
}

// Generic templates must tell users up front that the 发起 MR node needs a 工蜂
// Git Token and a project-scoped 工蜂 MCP (+ 太湖 personal token).
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
  mcpMarket: [{id: '61', aliases: ['gongfeng'], url: 'https://mcpgw.knot.example.com/gongfeng'}],
  data: {users: [{id: 7, gitTokenConfigured: true, taihuPatConfigured: false}], mcpServers: []}
};
global.userById = id => state.data.users.find(u => Number(u.id) === Number(id));
eval(code.slice(code.indexOf('const GENERIC_REQUIREMENT_CHECKS='), code.indexOf('function genericRequirementChips(')));
const workflow = {requirements: [
  {key: 'gongfeng-git-token', nodeId: 'mr', label: '工蜂 Git Token', hint: '推送分支'},
  {key: 'taihu-pat', nodeId: 'mr', label: '太湖个人令牌', hint: '太湖认证'},
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
state.data.mcpServers = [{scope: 'project', projectId: 'alpha', name: 'Gongfeng MCP', url: 'https://mcpgw.knot.example.com/gongfeng'}];
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
		`value="gongfeng">只同步工蜂`,
		`value="tapd">只同步 TAPD`,
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
assert.equal(scheduleSourceLabel({}), '工蜂和 TAPD');
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
    repositories: [{id:'repo-skillhub', projectId:'platform'}],
    runs: [
      {id:'run-afp', status:'waiting', executionContext:{responsibleUserId:7}},
      {id:'run-align', status:'waiting', workflowId:'scheduled-baseline-align', executionContext:{projectId:'platform', responsibleUserId:7}, repository:{name:'SkillHub', repositoryId:'repo-skillhub'}},
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
const web = parseGongfengRepoURL('https://git.example.com/dns-ai/skillhub-agent-flow');
assert.equal(web.project, 'dns-ai/skillhub-agent-flow');
assert.equal(web.cloneUrl, 'https://git.example.com/dns-ai/skillhub-agent-flow.git');
const ssh = parseGongfengRepoURL('git@git.example.com:dns-ai/skillhub-agent-flow.git');
assert.equal(ssh.project, 'dns-ai/skillhub-agent-flow');
assert.equal(ssh.cloneUrl, 'git@git.example.com:dns-ai/skillhub-agent-flow.git');
const cloned = parseGongfengRepoURL('git clone git@git.example.com:dns-ai/skillhub-agent-flow.git');
assert.equal(cloned.project, 'dns-ai/skillhub-agent-flow');
assert.equal(cloned.cloneUrl, 'git@git.example.com:dns-ai/skillhub-agent-flow.git');
assert.equal(web.name, 'skillhub-agent-flow');
const http = parseGongfengRepoURL('http://git.example.com/dns-ai/skillhub-agent-flow.git');
assert.equal(http.project, 'dns-ai/skillhub-agent-flow');
assert.equal(http.name, 'skillhub-agent-flow');
assert.equal(ssh.name, 'skillhub-agent-flow');
assert.ok(code.includes("integrateBranch:''"));
assert.ok(code.includes('集成分支用于检查多个 SOP 执行后工作分支之间是否存在合并冲突'));
assert.ok(!code.includes("integrateBranch:'agentflow/integration'"));
assert.ok(code.includes('项目名称'));
assert.ok(code.includes('data-edit-repo'));
assert.ok(code.includes('repo-cell-link'));
assert.ok(code.includes('target="_blank"'));
assert.ok(code.includes('repoRowWebHref'));
assert.ok(!code.includes('data-edit-col'));
assert.ok(!code.includes('data-save-col'));
assert.ok(code.includes('repoAdminRowHTML'));
assert.ok(code.includes('savedRepoOptions=repoRows.filter(r=>r.id)'));
assert.ok(!code.includes('（待保存）'));
assert.ok(code.includes("if(!row.querySelector('[data-r]'))"));
eval(code.slice(code.indexOf('function repoDisplayURL(r){'), code.indexOf('function repoPayloadFromRow(')));
var state = {data:{repositories:[{id:'skillhub',name:'skillhub',gongfengProjectId:'example/team-project',webUrl:'https://git.example.com/example/team-project',cloneUrl:'git@git.example.com:example/team-project.git',defaultBranch:'master',integrateBranch:'agentflow/integration'}]}};
const displayRow = {
  dataset:{repoRow:'0',repoId:'skillhub'},
  querySelector(sel){return sel==='[data-r]'||sel==='[data-r="repoUrl"]'?null:null},
  querySelectorAll(){return []}
};
const draft = {};
captureRepoRows({querySelectorAll(sel){return sel==='[data-repo-row]'?[displayRow]:[]}}, draft);
assert.equal(draft.repoRows[0].id, 'skillhub');
assert.equal(draft.repoRows[0].gongfengProjectId, 'example/team-project');
assert.equal(draft.repoRows[0].webUrl, 'https://git.example.com/example/team-project');
assert.ok(String(draft.repoRows[0].repoUrl||'').includes('team-project'));
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
	for _, want := range []string{`id="chatCreateDirect"`, `直接创建`, `id="chatCreateLocal"`, `平台需求`, `id="chatSend"`, `id="reqWorkflow"`, `SOP 流程`} {
		if !strings.Contains(page, want) {
			t.Fatalf("requirement composer UI is missing %q", want)
		}
	}
	sendIdx := strings.Index(page, `id="chatSend"`)
	directIdx := strings.Index(page, `id="chatCreateDirect"`)
	if sendIdx < 0 || directIdx < 0 || sendIdx > directIdx {
		t.Fatal("直接创建 must sit to the right of 开始分析")
	}
	if !strings.Contains(page, `nav-label">项目配置`) {
		t.Fatal("sidebar must label the projects page 项目配置")
	}
	if !strings.Contains(page, `<script src="/app/composer.js"></script>`) {
		t.Fatal("index.html must load the requirement composer script")
	}
	if !strings.Contains(page, `data-add="sop"`) {
		t.Fatal("flow designer must offer SOP as a node type")
	}
	if !strings.Contains(page, `class="req-composer-actions"`) {
		t.Fatal("composer actions must be grouped so 直接创建 cannot wrap under the hint")
	}
	if strings.Contains(page, `id="chatCreateGongfeng"`) {
		t.Fatal("requirement composer must not offer 工蜂 as a create destination")
	}
	reqStart := strings.Index(page, `id="requirementModal"`)
	reqEnd := strings.Index(page, `id="importTAPDModal"`)
	if reqStart >= 0 && reqEnd > reqStart {
		composer := page[reqStart:reqEnd]
		if strings.Contains(composer, `需求套餐`) {
			t.Fatal("create requirement modal must not show 需求套餐")
		}
		if !strings.Contains(composer, `id="reqWorkflow"`) || !strings.Contains(composer, `SOP 流程`) {
			t.Fatal("create requirement must show a visible SOP 流程 select")
		}
		if strings.Contains(composer, `id="reqSopRow"`) || strings.Contains(composer, `id="reqPlaybooks"`) || strings.Contains(composer, `hidden></select>`) {
			t.Fatal("create requirement must not hide SOP behind playbook chips")
		}
	}
	if !strings.Contains(page, `问清再建单`) {
		t.Fatal("placeholder must say 问清再建单")
	}
	if strings.Contains(page, "再建模") {
		t.Fatal("placeholder must not say 再建模")
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
	for _, want := range []string{`state.reqCreateMode='direct'`, `reqCreateDirect`, `不会立刻启动`, `persistRequirementComposer`, `未配置`, `if(modal.id==='requirementModal')return`, `resumeChatClarify`, `jobId:chatClarifyJobId`, `正在接上分析`, `从空白开始`, `stripClarifyReason`, `renderClarifyPanel(JSON.parse(lastAssistant.content))`, `assigneeUserId:Number(state.currentUser?.id)||0`, `fillReqWorkflow`, `selectedReqWorkflowId`, `workflowId:selectedReqWorkflowId()`, `composerProgressHTML`, `继续分析`, `先建单，详情里继续澄清`, `跳过分析直接建单`, `按当前结论建单`, `分析还没完成，仍要建单`, `没有固定轮数`, `composerCreateReady`, `setComposerRetryMode`, `composerClarifyRetry`} {
		if !strings.Contains(code, want) {
			t.Fatalf("direct-create dest picker is missing %q", want)
		}
	}
	if strings.Contains(code, `renderClarifyPanel(JSON.parse(lastAssistant.content), reconnect?'':interruptHint)`) {
		t.Fatal("restored analysis must not treat the interrupt hint as a render error")
	}
}

func TestDeploymentDurationDashboardIsEmbedded(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile("deployment-dashboard.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-view="deployments" hidden`, `id="deploymentsView"`, `/deployment-dashboard.css?v=20260920r2`, `自动展示 SkillHub 最近 7 天部署耗时`} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("deployment dashboard markup is missing %q", want)
		}
	}
	for _, want := range []string{"renderDeploymentDashboard", "/api/deployment-metrics?", "11867454", "deploymentChartSVG", "if(branch)q.set('branch',branch)", "最近 7 天与正在部署"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("deployment dashboard behavior is missing %q", want)
		}
	}
	if !strings.Contains(string(css), ".deployment-kpis") || !strings.Contains(string(css), ".deployment-chart") {
		t.Fatal("deployment dashboard styles are incomplete")
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
	daily := strings.Index(page, `>日常工作<`)
	runsNav := strings.Index(page, `data-view="runs"`)
	projectsNav := strings.Index(page, `data-view="projects"`)
	flowConfig := strings.Index(page, `>流程配置<`)
	insights := strings.Index(page, `>数据洞察<`)
	if daily < 0 || runsNav < 0 || insights < 0 || !(daily < runsNav && runsNav < insights) {
		t.Fatal("运行记录 must sit under 日常工作, not 数据洞察")
	}
	if projectsNav < 0 || flowConfig < 0 || !(daily < projectsNav && projectsNav < flowConfig) {
		t.Fatal("项目配置 must sit under 日常工作, before 流程配置")
	}
	js, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	code := string(js)
	for _, want := range []string{`/api/issues/import-tapd`, `openImportTAPDModal`, `fillImportTAPDWorkflow`, `fillImportTAPDRepository`, `selectedImportTAPDRepositoryIds`, `renderImportTAPDRepositoryPicker`, `repositoryIds`, `请至少选择一个代码仓库`, `importTAPDStory`, `requirementNeedsClarification`, `clarificationNeedsConfirm`, `data-dtab="clarify"`, `确认结论`, `clarification?.gated`, `autoStartClarify`, `issueClarifyJobs`, `attachIssueClarifyStream`, `关闭网页也不会中断`, `clarifyOptionLabel`, `clarifyQuestionOptions`, `createIssueDirect`, `projectTAPDReady`, `chatCreateDirect`, `chatCreateLocal`, `pushIssueToTAPDFromDetail`, `dashImportTAPD`, `run-id-copy`, `data-export-logs`, `从这里重跑`, `重新触发`, `data-retrigger-issue`, `data-rerun-from-here`, `canRetriggerIssue`, `openRerunFromNode`, `executionContext?.issueId`, `run?.status==='succeeded'`, `/api/issues/${encodeURIComponent(issueId)}/requirement`, `inferSOPLane`, `req-lane-card`, `issueLaneDots`, `.filter(l=>!l.orphan)`, `lanes.length<2`, `collapseSingleSOPLane`, `issueSOPLabel`, `actionWorkflow`, `data-workflow`, `workflowSOPLane`, `需求开发`, `saveIssueSOPS`, `/sops`, `effectiveIssueSOPBindings`, `projectSOPBindings`, `fillReqWorkflow`, `plannedSOPNodes`, `sopWorkflowId`, `sopWorkflowChoices`, `syncReqComposerLayout`} {
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

func TestUsageAnalysisUI(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{`data-obs-tab="overview">用量`, `data-obs-tab="delivery">交付`, `data-obs-tab="skillhub"`, `id="exportObsReport"`} {
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
	for _, want := range []string{"ensureObsUserFilter", "obsRunMatchesUser", "exportObsReport", "buildObsReportHTML", "userDisplay(user)", "assigneeUserId", "各模型用量", "每人每日用量", "成员汇总", "data-obs-scope", "setObsScope", "需求类型分布", "SOP 版本对比", "节点质量明细", "setObsTrendGrain", "obsTrendChart", "该责任人在各项目", "data-obs-grain", "grain=", "全部", "obsApiGrain", "obsRangeCaption", "obsPeriodPicker", "setObsPeriod", "obsScopedView", "data-obs-period", "不含暂停/恢复", "Loop 指标", "workflow_task_id", "averageTokensPerRun", "toolSuccessRate", "runTraces", "obs-traces", "isSkillHubProject", "canShowSkillhubEfficiency", "isPreviewHost", "showDirectRunButton", "obsSkillhub", "obsSkillhubInsights", "obsSkillhubChart", "obsSkillhubCharts", "weeklyLegacy", "sh-chart-hero", "obsSkillhubDeployCard", "obsSkillhubDeployCharts", "dailySample", "countedPipelines", "前端测试环境", "AI 分析", "weeklyMerged", `nav[data-view="deployments"]`, "/api/analytics/skillhub", "/api/deployment-metrics"} {
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
const issue = {number: 42, title: '全量 PHPUnit-回归', description: '覆盖 DNSPod 主流程', externalRef: {externalId: 'TAPD-100'}};
const project = {key: 'DP'};
assert.equal(issueMatchesQuery(issue, project, 'php unit'), true);
assert.equal(issueMatchesQuery(issue, project, '回归 php'), true);
assert.equal(issueMatchesQuery(issue, project, 'PHPUNIT回归'), true);
assert.equal(issueMatchesQuery(issue, project, 'DP-42'), true);
assert.equal(issueMatchesQuery(issue, project, 'DNSPod'), true);
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
assert.equal(sourceControlRootPath('dnspod-web'), '/dnspod-web');
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
global.location = { origin: 'http://localhost' };
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
global.localStorage = { getItem: () => null, setItem: () => {} };
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
global.localStorage = { getItem: () => null, setItem: () => {} };
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
	// 入口只对平台或业务域管理员渲染，未授权的 #/platform 要被挡回去；
	// 后端的 403 是底线，但不能靠它来当第一道门。
	if !strings.Contains(source, "group.hidden=!canAccessPlatformManagement()") {
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
func TestResearchEfficiencyDashboardUI(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{`data-view="metrics"`, `id="metricsView"`, `id="metricsBody"`, `data-metrics-range="7d"`, `data-metrics-range="30d"`, `data-metrics-range="90d"`, `data-metrics-range="365d"`, `id="metricsDateFrom"`, `id="metricsDateTo"`, `metrics-dashboard.css`} {
		if !strings.Contains(page, want) {
			t.Fatalf("研发效能看板缺少 %q", want)
		}
	}
	js, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	code := string(js)
	for _, want := range []string{"renderMetricsDashboard", "refreshMetrics", "metricsDisplayModel", "metricsTrendSVG", "metricsPhaseTrendSVG", "metricsRequirementTable", "metricsDonut", "metricsDemoEnabled", "metricsDemoSnapshot", "演示数据", "/api/analytics/skillhub", "当前项目暂无完整需求闭环", "需求交付数", "平均需求耗时", "总耗时", "每条需求耗时明细", "平均澄清耗时", "平均研发耗时", "平均测试耗时", "测试环境部署耗时", "AI 需求覆盖率", "Run 成功率"} {
		if !strings.Contains(code, want) {
			t.Fatalf("研发效能看板脚本缺少 %q", want)
		}
	}
	if strings.Contains(code, "第一阶段指标口径") {
		t.Fatal("研发效能看板不应展示指标口径表")
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

	// Verify req clarify panel option unselect on second click
	if !strings.Contains(source, "const wasSelected=btn.classList.contains('selected');") {
		t.Fatal("req clarify panel option does not track wasSelected to toggle off on repeat click")
	}
	if !strings.Contains(source, "if(!wasSelected)btn.classList.add('selected');") {
		t.Fatal("req clarify panel option does not conditionally add selected")
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

func TestCollabSurfacesParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping collab parse check")
	}
	for _, name := range []string{"collaboration.js", "assistant.js", "orchestrate.js"} {
		out, err := exec.Command(node, "--check", name).CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed to parse: %v\n%s", name, err, out)
		}
	}
	app, err := readAppBundle()
	if err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), `id="roomsView"`) || strings.Contains(string(html), `data-view="rooms"`) || strings.Contains(string(html), `/rooms.js`) || strings.Contains(string(html), `id="roomComposer"`) || strings.Contains(string(html), `class="rooms-chat-pane"`) {
		t.Fatal("obsolete group chat is still exposed")
	}
	orch, err := os.ReadFile("orchestrate.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`collaboration:'AI 工作台'`} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
	for _, want := range []string{"collaborationView", "aiOrchestrator", "playWorkflowOnCanvas"} {
		if !strings.Contains(string(html), want) && !strings.Contains(string(orch), want) {
			t.Fatalf("collab UI missing %q", want)
		}
	}
	if !strings.Contains(string(orch), "ai-just-built") || !strings.Contains(string(orch), "ai-just-wired") {
		t.Fatal("canvas playback does not mark nodes and edges as they are built")
	}
	script := `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const code = fs.readFileSync('orchestrate.js','utf8');
const start = code.indexOf('function layoutAIGraph');
const end = code.indexOf('function normalizeAIWorkflow');
eval(code.slice(start, end));
const nodes = [{id:'a',name:'澄清'},{id:'b',name:'实现'},{id:'c',name:'评审'}];
const edges = [{source:'a',target:'b'},{source:'b',target:'c'}];
layoutAIGraph(nodes, edges);
assert.ok(nodes[0].position.x < nodes[1].position.x);
assert.ok(nodes[1].position.x < nodes[2].position.x);
assert.equal(nodes[0].position.y, nodes[1].position.y);
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("layout check failed: %v\n%s", err, out)
	}
	editScript := `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const code = fs.readFileSync('orchestrate.js','utf8');
const cut = (from, to) => code.slice(code.indexOf(from), code.indexOf(to));
function uid(prefix){ return prefix + '-new'; }
eval(cut('function normalizeAINode', 'function aiDecodePartialString'));
eval(cut('function aiClone', 'function aiOrchestratePrompt'));
assert.equal(aiWantsFreshWorkflow('清空当前画布，重新搭一条'), true);
assert.equal(aiWantsFreshWorkflow('补上构建静态站点，其它不要动'), false);
assert.equal(aiWantsPreviewRemoved('去掉构建静态站点'), true);
assert.equal(aiWantsPreviewRemoved('怎么没有构建静态站点节点'), false);
assert.equal(aiWantsPreviewRemoved('补上构建静态站点，其它不要动'), false);
const base = {id:'sop-1', name:'产品交付', nodes:[
  {id:'design', name:'方案', type:'agent', script:'keep-me'},
  {id:'af-preview-build', name:'构建静态站点', type:'agent', agentId:'af-preview-builder'}
], edges:[{id:'e1', source:'design', target:'af-preview-build', when:'success'}], preview:{enabled:true}};
const merged = mergeAIEdit(base, {name:'产品交付', nodes:[{id:'design', name:'方案设计', type:'agent'}], edges:[]}, '把方案改个名字', []);
assert.equal(merged.nodes.find(n => n.id==='design').name, '方案设计');
assert.equal(merged.nodes.find(n => n.id==='design').script, 'keep-me');
assert.ok(merged.nodes.some(n => n.id==='af-preview-build'));
assert.ok(merged.edges.some(e => e.target==='af-preview-build'));
assert.equal(merged.preview.enabled, true);
`
	out, err = exec.Command(node, "-e", editScript).CombinedOutput()
	if err != nil {
		t.Fatalf("orchestrator edit check failed: %v\n%s", err, out)
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
eval(code.slice(code.indexOf('function duration('), code.indexOf('function isSkillHubDeploymentRepository(')));
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
