package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// This fixture simulates the documented Qoder stream and MCP interaction. It
// checks LoopForge's integration without pretending to exercise a real model.
const fakeQoderCLI = `#!/usr/bin/python3
import json, os, sys, urllib.request
args = sys.argv[1:]
def value(flag):
    return args[args.index(flag) + 1]
assert os.environ['QODER_PERSONAL_ACCESS_TOKEN'] == 'fixture-pat'
assert '--strict-mcp-config' in args
config = json.load(open(value('--mcp-config')))
server = config['mcpServers']['probe']
assert server['type'] == 'http'
home = os.environ['QODER_CONFIG_DIR']
session = 'fixture-session-1'
state = os.path.join(home, 'fixture-session.json')
def emit(event):
    event['session_id'] = session
    print(json.dumps(event), flush=True)
emit({'type':'system','subtype':'init','model':'auto'})
if '--resume' in args:
    assert value('--resume') == session
    answer = json.load(open(state))['answer']
else:
    payload = json.dumps({'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'read_secret','arguments':{}}}).encode()
    request = urllib.request.Request(server['url'], data=payload, headers={'Content-Type':'application/json'})
    response = json.load(urllib.request.urlopen(request))
    answer = response['result']['content'][0]['text'].split('=')[1]
    json.dump({'answer':answer}, open(state, 'w'))
    emit({'type':'assistant','message':{'role':'assistant','content':[{'type':'tool_use','id':'call-1','name':'mcp__probe__read_secret','input':{}}]}})
    emit({'type':'user','message':{'role':'user','content':[{'type':'tool_result','tool_use_id':'call-1','content':'secret='+answer}]}})
emit({'type':'assistant','message':{'role':'assistant','content':[{'type':'text','text':answer}]}})
emit({'type':'result','subtype':'success','is_error':False,'result':answer,'usage':{'output_tokens':1}})
`

func TestQoderSimulatedNodeMCPAndResume(t *testing.T) {
	mcp, calls := probeMCPServer(t)
	workspace := t.TempDir()
	binary := filepath.Join(t.TempDir(), "qodercli")
	if err := os.WriteFile(binary, []byte(fakeQoderCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	sandbox := credentialSandbox{
		localSandbox: &localSandbox{id: "qoder-fixture", workspace: workspace},
		credentials:  RunCredentials{AgentCLI: map[string]ProviderCredential{AgentCLIQoder: {APIKey: "fixture-pat"}}},
	}
	executor := domain.ExecutorDefinition{ID: AgentCLIQoder, Kind: AgentCLIQoder, Binary: binary, Protocol: "stream-json", Model: "auto"}
	agent := domain.AgentDefinition{ID: "probe", Name: "probe", Prompt: "Follow the task."}
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{{ID: "probe", Name: "probe", Transport: "http", URL: mcp.URL + "/mcp"}}}
	run := func(resume string) (string, []domain.TelemetryEvent) {
		t.Helper()
		var mu sync.Mutex
		var events []domain.TelemetryEvent
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		output, err := NewExecutorRunner().Run(ctx, sandbox, "fixture", executor, agent, assets, "Return the number.", InvocationOptions{ResumeSessionID: resume}, func(event domain.TelemetryEvent) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, event)
		})
		if err != nil {
			t.Fatalf("simulated invocation: %v; output: %s", err, output)
		}
		mu.Lock()
		defer mu.Unlock()
		return output, append([]domain.TelemetryEvent(nil), events...)
	}
	output, events := run("")
	var session string
	toolSeen := false
	for _, event := range events {
		if event.SessionID != "" {
			session = event.SessionID
		}
		if event.Tool != nil && strings.Contains(event.Tool.Name, "read_secret") {
			toolSeen = true
		}
	}
	if output != "42" || calls() != 1 || session != "fixture-session-1" || !toolSeen {
		t.Fatalf("first turn: output=%q calls=%d session=%q tool=%v", output, calls(), session, toolSeen)
	}
	output, events = run(session)
	if output != "42" || calls() != 1 {
		t.Fatalf("resumed turn: output=%q calls=%d", output, calls())
	}
	if len(events) == 0 {
		t.Fatal("resumed turn produced no events")
	}
}

func TestQoderOnDemandInstall(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "npm.log")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_LOG", log)
	t.Setenv("NPM_TEST_BIN", "qodercli")
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "loopforge-platform:tested")
	useFakeCommands(t, map[string]string{"npm": genericFakeNPM})
	sandbox := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	path, err := sandbox.onDemandCLIPath(context.Background(), CommandSpec{Provider: AgentCLIQoder, Argv: []string{"qodercli", "-p", "work"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(root, "qoder", "current", "node_modules", ".bin") {
		t.Fatalf("installed path: %s", path)
	}
	custom := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: "FROM custom\n"}}
	if got, err := custom.onDemandCLIPath(context.Background(), CommandSpec{Provider: AgentCLIQoder, Argv: []string{"qodercli"}}); err != nil || got != "" {
		t.Fatalf("custom image: %q %v", got, err)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "@qoder-ai/qodercli@1.1.88") {
		t.Fatalf("npm invocation: %s", calls)
	}
}

func TestQoderInvocationAndCredentialIsolation(t *testing.T) {
	home := t.TempDir()
	entries := map[string]any{"example": map[string]any{"url": "https://example.test/mcp", "headers": map[string]string{"Authorization": "Bearer test"}}}
	if err := WriteAgentCLIMCPConfig(AgentCLIQoder, HostAgentCLIHome(home), entries, AgentCLIMCPOptions{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(home, ".qoder", "agentflow-mcp.json"))
	if err != nil || !strings.Contains(string(config), `"type": "http"`) {
		t.Fatalf("Qoder MCP config: %s, %v", config, err)
	}
	command := AgentCLICommand(AgentCLIQoder, AgentCLICommandOptions{Chat: true, Model: "auto", Resume: "session-1", Prompt: "hello"})
	argv, env, err := ApplyAgentCLICredential(AgentCLIQoder, ProviderCredential{APIKey: "test-pat"}, command.AgentCLI, HostAgentCLIHome(home), "/private", command.Argv)
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"qodercli", "--tools", "--strict-mcp-config", "/private/.qoder/agentflow-mcp.json", "--resume", "session-1", "-p"} {
		if !slices.Contains(argv, arg) {
			t.Errorf("missing %q: %q", arg, argv)
		}
	}
	if i := slices.Index(argv, "--tools"); i < 0 || argv[i+1] != "" {
		t.Fatalf("built-in tools are not disabled: %q", argv)
	}
	if env["QODER_PERSONAL_ACCESS_TOKEN"] != "test-pat" || env["QODER_CONFIG_DIR"] != "/private/.qoder" || strings.Contains(strings.Join(argv, " "), "test-pat") || strings.Contains(string(config), "test-pat") {
		t.Fatalf("unexpected credential binding: argv=%q env=%v", argv, env)
	}
	if !slices.Contains(agentCLIHostEnv, "QODER_PERSONAL_ACCESS_TOKEN") || !slices.Contains(agentCLIHostEnv, "QODER_CONFIG_DIR") {
		t.Fatal("host credentials and configuration must be cleared")
	}
	sop := AgentCLICommand(AgentCLIQoder, AgentCLICommandOptions{Prompt: "work"})
	if slices.Contains(sop.Argv, "--tools") || !slices.Contains(sop.Argv, "bypass_permissions") {
		t.Fatalf("SOP argv: %q", sop.Argv)
	}
}

func TestQoderStreamSuccessAndFailure(t *testing.T) {
	translate := NewAgentCLITranslator(AgentCLIQoder)
	for _, line := range []string{
		`{"type":"system","subtype":"init","session_id":"session-1"}`,
		`{"type":"assistant","session_id":"session-1","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"result","subtype":"success","session_id":"session-1","is_error":false,"result":"done"}`,
	} {
		if got := translate(line); len(got) != 1 || got[0] != line {
			t.Fatalf("event lost: %q", got)
		}
	}
	got := translate(`{"type":"result","subtype":"error_during_execution","session_id":"session-1","errors":["authentication failed"]}`)
	var event struct {
		IsError   bool   `json:"is_error"`
		Result    string `json:"result"`
		SessionID string `json:"session_id"`
	}
	if len(got) != 1 || json.Unmarshal([]byte(got[0]), &event) != nil || !event.IsError || event.Result != "authentication failed" || event.SessionID != "session-1" {
		t.Fatalf("failure: %q", got)
	}
}
