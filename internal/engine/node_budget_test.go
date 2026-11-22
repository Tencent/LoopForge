package engine

import (
	"errors"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestNodeBudgetEvaluateThresholds(t *testing.T) {
	budget := NodeBudget{ToolWarn: 300, ToolLimit: 800, TokenWarn: 500_000, TokenLimit: 1_000_000}
	if state := budget.evaluate(0, 0); state.Warn || state.Exceeded {
		t.Fatalf("fresh node = %+v", state)
	}
	if state := budget.evaluate(300, 499_999); !state.Warn || state.Exceeded {
		t.Fatalf("tool warn = %+v", state)
	}
	if state := budget.evaluate(800, 0); !state.Exceeded || !strings.Contains(state.reason(), "工具调用 800") {
		t.Fatalf("tool limit = %+v (%s)", state, state.reason())
	}
	if state := budget.evaluate(0, 1_000_000); !state.Exceeded || !strings.Contains(state.reason(), "Token 1000000") {
		t.Fatalf("token limit = %+v (%s)", state, state.reason())
	}
	if state := (NodeBudget{}).evaluate(10_000, 10_000_000); state.Warn || state.Exceeded {
		t.Fatalf("disabled budget = %+v", state)
	}
}

func TestNodeBudgetFromEnvOverrides(t *testing.T) {
	t.Setenv("AGENTFLOW_NODE_TOOL_WARN", "10")
	t.Setenv("AGENTFLOW_NODE_TOOL_LIMIT", "20")
	t.Setenv("AGENTFLOW_NODE_TOKEN_WARN", "1000")
	t.Setenv("AGENTFLOW_NODE_TOKEN_LIMIT", "2000")
	budget := NodeBudgetFromEnv()
	want := NodeBudget{ToolWarn: 10, ToolLimit: 20, TokenWarn: 1000, TokenLimit: 2000}
	if budget != want {
		t.Fatalf("budget = %+v, want %+v", budget, want)
	}
}

func TestNodeBudgetExceededParksNodeForApproval(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "budget-owner")
	e.SetNodeBudget(NodeBudget{ToolLimit: 5, TokenLimit: 10})
	r := leaseRun(t, s, "run-budget-exceeded", "running", "running")
	nr := r.Nodes["node"]
	nr.Attempt = 1
	nr.ToolCalls = 9
	nr.Usage = domain.TokenUsage{TotalTokens: 99}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	owned, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	e.nodeFailedAt(r.ID, domain.WorkflowNode{ID: "node", MaxAttempts: 5}, 1, owned.Generation, errors.New("exit status 1"), false)
	got, _ := s.RunState(r.ID)
	nr = got.Nodes["node"]
	if nr.Status != "waiting_approval" {
		t.Fatalf("status = %q, want waiting_approval (%s)", nr.Status, nr.Error)
	}
	if !strings.Contains(nr.Error, "预算超限") || !strings.Contains(nr.Error, "工具调用 9") {
		t.Fatalf("error = %q", nr.Error)
	}
	if n := nodeEvents(got, "node", "node.budget.exceeded"); n != 1 {
		t.Fatalf("budget event = %d", n)
	}
}

func TestNodeBudgetWarningKeepsRetry(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "budget-warn-owner")
	e.SetNodeBudget(NodeBudget{ToolWarn: 5, TokenWarn: 5})
	r := leaseRun(t, s, "run-budget-warn", "running", "running")
	nr := r.Nodes["node"]
	nr.Attempt = 1
	nr.ToolCalls = 9
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	owned, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	e.nodeFailedAt(r.ID, domain.WorkflowNode{ID: "node", MaxAttempts: 5}, 1, owned.Generation, errors.New("exit status 1"), false)
	got, _ := s.RunState(r.ID)
	nr = got.Nodes["node"]
	if nr.Status != "pending" {
		t.Fatalf("status = %q, want pending retry (%s)", nr.Status, nr.Error)
	}
	if n := nodeEvents(got, "node", "node.budget.warning"); n != 1 {
		t.Fatalf("budget warning event = %d", n)
	}
}
