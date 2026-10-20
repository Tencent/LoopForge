package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestGateMCPSubmitWritesStructuredResult(t *testing.T) {
	workspace := t.TempDir()
	configRoot := filepath.Join(workspace, ".agentflow", "config", "reviewer")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	resultPath := ".agentflow/gate-results/review/attempt-1.json"
	server, err := materializeGateMCP(workspace, configRoot, ".agentflow/config/reviewer/gate-mcp", resultPath, "local")
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"submit_gate_result","arguments":{"decision":"human","reason":"ambiguous evidence"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"submit_gate_result","arguments":{"decision":"rework","reason":"blocking test failure","actionRequired":"fix the failed case","findings":[{"title":"case failed","evidence":"go test ./..."}]}}}`,
	}, "\n") + "\n"
	cmd := exec.Command(server.Command, server.Args...)
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(), "AGENTFLOW_GATE_RESULT_PATH="+resultPath)
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gate MCP: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "submit_gate_result") || !strings.Contains(string(output), "actionRequired is required") || !strings.Contains(string(output), "Gate result submitted") {
		t.Fatalf("unexpected MCP output: %s", output)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(resultPath)))
	if err != nil {
		t.Fatal(err)
	}
	var result domain.GateResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Decision != "rework" || result.Reason != "blocking test failure" || len(result.Findings) != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestMaterializeInvocationInjectsGateMCP(t *testing.T) {
	workspace := t.TempDir()
	compiled, err := materializeInvocation(
		workspace,
		"invoke-gate",
		domain.ExecutorDefinition{Kind: "codebuddy"},
		domain.AgentDefinition{ID: "reviewer", Name: "Reviewer"},
		domain.AgentAssetBundle{},
		InvocationOptions{GateResultPath: ".agentflow/gate-results/review/attempt-1.json"},
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(compiled.AgentRoot), "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"agentflow-gate", "gate-mcp", "AGENTFLOW_GATE_RESULT_PATH", "attempt-1.json"} {
		if !strings.Contains(text, want) {
			t.Fatalf("mcp config missing %q:\n%s", want, text)
		}
	}
	if info, err := os.Stat(filepath.Join(workspace, ".agentflow", "config", "reviewer", "gate-mcp")); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatal(err)
	}
}

func TestMockGateDecisionSupportsAttemptSequence(t *testing.T) {
	agent := domain.AgentDefinition{Instructions: "demo [[mock:gate=rework,pass]]"}
	if got := mockGateDecision(agent, ".agentflow/gate-results/review/attempt-1.json"); got != "rework" {
		t.Fatalf("attempt 1 decision=%q", got)
	}
	if got := mockGateDecision(agent, ".agentflow/gate-results/review/attempt-2.json"); got != "pass" {
		t.Fatalf("attempt 2 decision=%q", got)
	}
	if got := mockGateDecision(agent, ".agentflow/gate-results/review/attempt-3.json"); got != "pass" {
		t.Fatalf("later attempt should retain final decision, got %q", got)
	}
}

func TestMockGateResultSupportsRealisticDetails(t *testing.T) {
	agent := domain.AgentDefinition{Instructions: strings.Join([]string{
		"[[mock:gate=human]]",
		"[[mock:reason=自动化证据无法覆盖业务例外]]",
		"[[mock:action=请产品负责人确认是否接受该例外]]",
		"[[mock:summary=核心路径通过，例外策略待确认]]",
		"[[mock:finding=warning|退款策略存在歧义|需求文档未定义超过 30 天退款的处理方式]]",
	}, "\n")}
	result := mockGateResult(agent, ".agentflow/gate-results/review/attempt-1.json")
	if result.Decision != "human" || result.ActionRequired != "请产品负责人确认是否接受该例外" || len(result.Findings) != 1 {
		t.Fatalf("result=%+v", result)
	}
	if result.Findings[0].Severity != "warning" || !strings.Contains(result.Findings[0].Evidence, "30 天") {
		t.Fatalf("finding=%+v", result.Findings[0])
	}
}

func TestMaterializeGateMCPSelectsLinuxForDocker(t *testing.T) {
	dir := t.TempDir()
	pattern := filepath.Join(dir, "agentflow-gate-mcp-{os}-{arch}")
	host := expandPlatformPath(pattern, goruntime.GOOS, goruntime.GOARCH)
	linux := expandPlatformPath(pattern, "linux", goruntime.GOARCH)
	if err := os.WriteFile(host, []byte("host"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linux, []byte("linux"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", pattern)
	configRoot := filepath.Join(dir, "config")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeGateMCP(dir, configRoot, ".agentflow/config/reviewer/gate-mcp", "result.json", "docker"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(configRoot, "gate-mcp"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "linux" {
		t.Fatalf("docker gate helper = %q, want linux artifact", raw)
	}
}
