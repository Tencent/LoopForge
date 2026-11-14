package engine

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// This test spends real provider tokens. Run only with an explicitly configured
// OpenAI-compatible Magpie gateway and the installed Codex CLI.
func TestMagpieModelNativeGate(t *testing.T) {
	if os.Getenv("LOOPFORGE_MAGPIE_REAL_TEST") != "1" {
		t.Skip("set LOOPFORGE_MAGPIE_REAL_TEST=1 to use a real model")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(os.Getenv("LOOPFORGE_MAGPIE_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("LOOPFORGE_MAGPIE_MODEL"))
	if base == "" || model == "" {
		t.Fatal("set LOOPFORGE_MAGPIE_BASE_URL and LOOPFORGE_MAGPIE_MODEL")
	}
	e, store := newRecoveryEngine(t)
	if err := store.PutUser(domain.User{ID: 3, LoginName: "magpie-gate-probe"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateUserCredentials(3, repo.UserCredentialUpdate{CodexAPIKey: "local-gateway", CodexBaseURL: &base}); err != nil {
		t.Fatal(err)
	}
	agent := domain.AgentDefinition{ID: "magpie-gate", Name: "Magpie gate probe", ExecutorID: "codex", Model: model, Prompt: "你负责执行一个最小化验证任务。按任务说明使用可用的原生 MCP 工具提交节点终态，不要运行 shell 命令。"}
	if err := store.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "magpie-gate", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "n", Name: "门禁验证", Type: "agent", AgentID: agent.ID, MaxAttempts: 1}}}
	run := domain.Run{ID: "magpie-real-gate", WorkflowID: wf.ID, Status: "running", WorkflowSnapshot: wf, Input: "计算 2 + 2。请调用可用的 submit_gate_result MCP 工具，以 pass 提交结果并说明 2+2=4。不要使用 Bash 或 shell。", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 3}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "pending"}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if resumed := e.ResumeInterruptedRuns(); len(resumed) != 1 {
		t.Fatalf("resumed=%v", resumed)
	}
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.Run(run.ID)
		n := got.Nodes["n"]
		if got.Status == "succeeded" {
			if n == nil || n.GateResult == nil || n.GateResult.Decision != "pass" {
				t.Fatalf("missing gate: %+v", n)
			}
			toolCalls, shell := []string{}, false
			for _, ev := range got.Telemetry {
				if ev.Tool != nil && ev.Tool.Name != "" {
					toolCalls = append(toolCalls, ev.Tool.Name)
					if strings.Contains(strings.ToLower(ev.Tool.Name), "shell") {
						shell = true
					}
				}
			}
			native := false
			for _, name := range toolCalls {
				if strings.Contains(name, "submit_gate_result") {
					native = true
				}
			}
			if !native || shell {
				t.Fatalf("gate=%+v tools=%v", n.GateResult, toolCalls)
			}
			t.Logf("model=%s status=%s attempt=%d gate=%s reason=%q tools=%v", model, got.Status, n.Attempt, n.GateResult.Decision, n.GateResult.Reason, toolCalls)
			return
		}
		if got.Status == "failed" || got.Status == "waiting" {
			for _, ev := range got.Telemetry {
				if ev.Level == "error" || ev.Name == "process.stderr" {
					t.Logf("telemetry %s: %s", ev.Name, ev.Message)
				}
			}
			for _, log := range got.Logs {
				if log.Level == "error" || log.Stream == "stderr" {
					t.Logf("log: %s", log.Body)
				}
			}
			t.Fatalf("status=%s node=%+v", got.Status, n)
		}
		time.Sleep(100 * time.Millisecond)
	}
	got, _ := store.Run(run.ID)
	t.Fatalf("timeout status=%s node=%+v", got.Status, got.Nodes["n"])
}
