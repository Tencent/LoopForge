package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func openPassTestStore(t *testing.T) *repo.Store {
	t.Helper()
	if dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")); dsn != "" {
		s, err := repo.Open(dsn)
		if err != nil {
			t.Fatalf("open mysql: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func waitRunStatus(t *testing.T, s *repo.Store, runID, want string) domain.Run {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var current domain.Run
	for time.Now().Before(deadline) {
		current, _ = s.Run(runID)
		if current.Status == want {
			return current
		}
		time.Sleep(25 * time.Millisecond)
	}
	details := map[string]string{}
	for nodeID, node := range current.Nodes {
		details[nodeID] = node.Status + ": " + node.Error
	}
	t.Fatalf("run status = %s, want %s; nodes=%+v", current.Status, want, details)
	return current
}

func TestPassFailedScriptContinuesDownstreamHTTP(t *testing.T) {
	root := t.TempDir()
	s := openPassTestStore(t)
	suffix := time.Now().UTC().UnixNano()
	wfID := fmt.Sprintf("pass-http-%d", suffix)
	wf := domain.WorkflowDefinition{ID: wfID, Name: "pass http", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "hard", Name: "hard", Type: "script", ScriptLanguage: "shell", Script: "exit 1", OnFailure: "fail", MaxAttempts: 1},
		{ID: "next", Name: "next", Type: "script", ScriptLanguage: "shell", Script: "printf 'ok\\n'", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "hard", Target: "next", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cancel(run.ID) })
	waitRunStatus(t, s, run.ID, "failed")
	resp := requestJSON[map[string]any](t, h, http.MethodPost, "/api/runs/"+run.ID+"/pass", map[string]any{"nodeId": "hard", "context": "脚本误报"}, http.StatusOK)
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("pass response = %+v", resp)
	}
	final := waitRunStatus(t, s, run.ID, "succeeded")
	if final.Nodes["hard"].Status != "succeeded" || final.Nodes["next"].Status != "succeeded" {
		t.Fatalf("nodes after pass: hard=%+v next=%+v", final.Nodes["hard"], final.Nodes["next"])
	}
	if final.HumanContext != "脚本误报" {
		t.Fatalf("humanContext = %q", final.HumanContext)
	}
}

func TestPassRejectsAgentNodeHTTP(t *testing.T) {
	s := openPassTestStore(t)
	now := time.Now().UTC()
	runID := fmt.Sprintf("pass-agent-%d", now.UnixNano())
	if err := s.PutRun(domain.Run{
		ID: runID, Status: "failed", CreatedAt: now, UpdatedAt: now,
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID:    "wf",
			Nodes: []domain.WorkflowNode{{ID: "agent", Name: "提交工蜂 MR", Type: "agent"}},
		},
		Nodes: map[string]*domain.NodeRun{"agent": {NodeID: "agent", Status: "failed"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(t.TempDir())), tracker.NewService(), "", "")
	resp := requestJSON[map[string]any](t, h, http.MethodPost, "/api/runs/"+runID+"/pass", map[string]any{"nodeId": "agent"}, http.StatusBadRequest)
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "script") {
		t.Fatalf("unexpected error: %+v", resp)
	}
}

func TestPassRejectsWaitingRunHTTP(t *testing.T) {
	s := openPassTestStore(t)
	now := time.Now().UTC()
	runID := fmt.Sprintf("pass-waiting-%d", now.UnixNano())
	if err := s.PutRun(domain.Run{
		ID: runID, Status: "waiting", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{"gate": {NodeID: "gate", Status: "waiting_approval"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(t.TempDir())), tracker.NewService(), "", "")
	resp := requestJSON[map[string]any](t, h, http.MethodPost, "/api/runs/"+runID+"/pass", map[string]any{"nodeId": "gate"}, http.StatusBadRequest)
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "still active") {
		t.Fatalf("unexpected error: %+v", resp)
	}
}
