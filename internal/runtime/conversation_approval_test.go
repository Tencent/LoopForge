package runtime

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestNativeHookForwardsExactInputAndFailsClosed(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	var received struct {
		Params struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"params"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"allow":false,"reason":"denied exact action"}}`))
	}))
	defer server.Close()
	script := filepath.Join(t.TempDir(), "hook.py")
	if err := os.WriteFile(script, []byte(conversationApprovalPython), 0600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"tool_name":"Bash","tool_input":{"command":"printf exact"}}`,
		`{"toolName":"Bash","toolInput":{"command":"printf exact"}}`,
		`{"toolCall":{"name":"Bash","args":{"command":"printf exact"}}}`,
	} {
		cmd := exec.Command("python3", script)
		cmd.Env = append(os.Environ(), "AGENTFLOW_NATIVE_APPROVAL_URL="+server.URL)
		cmd.Stdin = strings.NewReader(input)
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), `"permissionDecision": "deny"`) {
			t.Fatalf("hook failed open: %s %v", output, err)
		}
		if received.Params.Name != "Bash" || !bytes.Contains(received.Params.Input, []byte("printf exact")) {
			t.Fatalf("lost exact input: %+v", received)
		}
	}
	cmd := exec.Command("python3", script)
	cmd.Env = append(os.Environ(), "AGENTFLOW_NATIVE_APPROVAL_URL=invalid-url")
	cmd.Stdin = strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"unsafe"}}`)
	if err := cmd.Run(); err == nil {
		t.Fatal("unavailable gateway failed open")
	}
}

func TestConversationSupportsEveryRegisteredCLI(t *testing.T) {
	for _, kind := range append([]string{"codebuddy", "codex", "claude-code"}, domain.AgentCLIExecutorKinds...) {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			command := CommandSpec{Argv: []string{conversationBinary(kind), "--prompt", "task"}, Provider: kind}
			if _, err := InstallConversationApproval(root, kind, command); err != nil {
				t.Fatal(err)
			}
			if _, err := conversationCLISetup(kind, conversationBinary(kind)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestZCodeConversationSetupRequiresOfficialPreinstallation(t *testing.T) {
	setup, err := conversationCLISetup(AgentCLIZCode, conversationBinary(AgentCLIZCode))
	if err != nil || strings.Contains(setup, "npm install") || !strings.Contains(setup, "预装官方 ZCode CLI") {
		t.Fatalf("ZCode must use its official preinstalled binary: %s: %v", setup, err)
	}
}

func TestZCodeConversationApprovalPreservesMCPAndCredentialCleanup(t *testing.T) {
	root := t.TempDir()
	home := AgentCLIHome{Base: root, Rel: "home"}
	if err := WriteAgentCLIMCPConfig(AgentCLIZCode, home, map[string]any{"agentflow_session": map[string]any{"type": "http", "url": "http://127.0.0.1/mcp"}}, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := addZCodeKeyCleanupHook(home); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallConversationApproval(root, AgentCLIZCode, CommandSpec{Argv: []string{"zcode"}}); err != nil {
		t.Fatal(err)
	}
	config := readJSONFile(t, filepath.Join(root, "home", zcodeConfigPath))
	hooks := config["hooks"].(map[string]any)
	events := hooks["events"].(map[string]any)
	if hooks["enabled"] != true || events["SessionStart"] == nil || events["PreToolUse"] == nil || config["mcp"] == nil {
		t.Fatalf("lost approval, MCP or credential cleanup: %+v", config)
	}
	guard := events["PreToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if guard["timeout"] != float64(conversationApprovalHookWatchdog) {
		t.Fatal("ZCode must not time out human approval after one minute")
	}
	if guard["onError"] != "deny" {
		t.Fatal("ZCode hook failures must block native effects")
	}
	// Binding happens after approval installation and must retain the guard.
	if _, _, err := ApplyAgentCLICredential(AgentCLIZCode, ProviderCredential{APIKey: "test-key"}, AgentCLIOptions{Model: "test-model"}, home, filepath.Join(root, "home"), []string{"zcode"}); err != nil {
		t.Fatal(err)
	}
	config = readJSONFile(t, filepath.Join(root, "home", zcodeConfigPath))
	hooks = config["hooks"].(map[string]any)
	events = hooks["events"].(map[string]any)
	if hooks["enabled"] != true || events["PreToolUse"] == nil || events["SessionStart"] == nil {
		t.Fatal("credential binding replaced the native approval guard")
	}
}

func TestNativeHookRetriesTransportAndPollsOneInvocation(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	var mu sync.Mutex
	var ids []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Params struct {
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		ids = append(ids, request.Params.ID)
		count := len(ids)
		mu.Unlock()
		var compact bytes.Buffer
		_ = json.Compact(&compact, request.Params.Input)
		if request.Params.Name != "Write" || compact.String() != `{"path":"waiting.txt"}` {
			t.Error("callback changed exact operation")
		}
		if count == 1 {
			http.Error(w, "temporary relay disconnect", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"pending": count < 4, "allow": count >= 4}})
	}))
	defer server.Close()
	script := filepath.Join(t.TempDir(), "hook.py")
	if err := os.WriteFile(script, []byte(conversationApprovalPython), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", script)
	cmd.Env = append(os.Environ(), "AGENTFLOW_NATIVE_APPROVAL_URL="+server.URL)
	cmd.Stdin = strings.NewReader(`{"tool_name":"Write","tool_input":{"path":"waiting.txt"}}`)
	if output, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(output), `"permissionDecision": "allow"`) {
		t.Fatalf("polling hook: %s: %v", output, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 4 || ids[0] == "" {
		t.Fatal("callback did not poll", ids)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatal("network retry created another invocation", ids)
		}
	}
}

func TestJavaScriptApprovalPollsAndHonorsCancellation(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	var mu sync.Mutex
	ids := map[string][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Params struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		ids[request.Params.Name] = append(ids[request.Params.Name], request.Params.ID)
		count := len(ids[request.Params.Name])
		mu.Unlock()
		if count == 1 && request.Params.Name == "write" {
			http.Error(w, "reconnect", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		allow := request.Params.Name == "write" && count >= 3
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"pending": !allow, "allow": allow}})
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "approval.mjs"), []byte(conversationPiApproval), 0600); err != nil {
		t.Fatal(err)
	}
	probe := `import assert from 'node:assert/strict';
import install from './approval.mjs';
let hook; install({on:(_,handler)=>{hook=handler}});
assert.equal(await hook({toolName:'write',input:{path:'waiting.txt'}},{}),undefined);
const controller=new AbortController();setTimeout(()=>controller.abort(),200);
const cancelled=await hook({toolName:'cancelled-write',input:{path:'never.txt'}},{signal:controller.signal});
assert.equal(cancelled.block,true);assert.match(cancelled.reason,/本轮已停止/);
`
	path := filepath.Join(dir, "probe.mjs")
	if err := os.WriteFile(path, []byte(probe), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", path)
	cmd.Env = append(os.Environ(), "AGENTFLOW_NATIVE_APPROVAL_URL="+server.URL)
	cmd.WaitDelay = 3 * time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("polling plugin: %s: %v", output, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids["write"]) != 3 {
		t.Fatal("plugin did not retry and poll", ids)
	}
	for _, id := range ids["write"] {
		if id == "" || id != ids["write"][0] {
			t.Fatal("plugin lost invocation identity", ids)
		}
	}
}

func TestPiApprovalChecksConcreteToolsInsideCodemode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	var mu sync.Mutex
	var tools []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			t.Error(err)
		}
		mu.Lock()
		tools = append(tools, rpc.Params.Name)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"allow": strings.HasPrefix(rpc.Params.Name, "mcp__agentflow_session__"), "reason": "native write denied"}})
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "approval.mjs"), []byte(conversationPiApproval), 0600); err != nil {
		t.Fatal(err)
	}
	// Pi dispatches nested codemode tools through the same hook. Exercise that
	// contract with the generated extension, including a denied concrete write.
	probe := `import assert from 'node:assert/strict';
import install from './approval.mjs';
let hook; install({on:(event,handler)=>{assert.equal(event,'tool_call');hook=handler;}});
assert.equal(await hook({toolName:'codemode',input:{code:'tools.write(...)'}},{}),undefined);
assert.equal(await hook({toolName:'mcp__agentflow_session__workbench_tool_schema',input:{name:'projects_list'}},{}),undefined);
const denied=await hook({toolName:'write',input:{path:'effect.txt',content:'blocked'}},{});
assert.equal(denied.block,true);assert.match(denied.reason,/native write denied/);
`
	path := filepath.Join(dir, "probe.mjs")
	if err := os.WriteFile(path, []byte(probe), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", path)
	cmd.Env = append(os.Environ(), "AGENTFLOW_NATIVE_APPROVAL_URL="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Pi nested approval: %s: %v", output, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(tools, ",") != "mcp__agentflow_session__workbench_tool_schema,write" {
		t.Fatalf("wrong concrete approvals: %v", tools)
	}
}
