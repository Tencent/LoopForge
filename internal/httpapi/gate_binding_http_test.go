package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func bindingServer(t *testing.T) (*repo.Store, *gin.Engine) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := engine.New(s, nil)
	e.ConfigureExecution("api", "test", time.Minute, false)
	h := &runsHandlers{deps: &deps{repo: s, engine: e}}
	gin.SetMode(gin.TestMode)
	g := gin.New()
	g.POST("/api/runs/:id/approve", h.run)
	g.POST("/api/runs/:id/pass", h.run)
	return s, g
}

func postControl(t *testing.T, g *gin.Engine, path string, body any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestApproveHTTPRejectsStaleArtifactWithConflict(t *testing.T) {
	s, g := bindingServer(t)
	wf := domain.WorkflowDefinition{
		ID: "http-binding-wf", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "verify", Type: "script", ScriptLanguage: "shell", Script: "true", MaxAttempts: 1}, {ID: "gate", Type: "approval"}},
	}
	run := domain.Run{
		ID: "http-binding-run", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "waiting",
		Nodes: map[string]*domain.NodeRun{
			"verify": {NodeID: "verify", Status: "succeeded", Attempt: 1},
			"gate":   {NodeID: "gate", Status: "waiting_approval", Attempt: 1},
		},
		Artifacts: []domain.Artifact{{ID: "a", RunID: "http-binding-run", NodeID: "verify", Attempt: 1, Name: "verify", Path: "verify.json", SHA256: "aaaa", CreatedAt: time.Now().UTC()}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}

	code, body := postControl(t, g, "/api/runs/http-binding-run/approve", map[string]any{
		"nodeId": "gate",
		"expected": map[string]any{
			"status": "waiting_approval", "attempt": 1, "artifactName": "verify", "artifactSha256": "bbbb",
		},
	})
	if code != http.StatusConflict {
		t.Fatalf("stale approve = %d %s, want 409", code, body)
	}
	if !strings.Contains(body, "bbbb") || !strings.Contains(body, "aaaa") {
		t.Fatalf("conflict body %q must name expected and actual hash", body)
	}
	stored, _ := s.RunState(run.ID)
	if stored.Nodes["gate"].Status != "waiting_approval" {
		t.Fatalf("gate status = %q, want waiting_approval", stored.Nodes["gate"].Status)
	}
}

func TestPassHTTPRequiresOverrideForVerificationNode(t *testing.T) {
	s, g := bindingServer(t)
	wf := domain.WorkflowDefinition{
		ID: "http-pass-wf", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "verify", Name: "Verify", Type: "script", ScriptLanguage: "shell", Script: "exit 1", MaxAttempts: 1, VerificationStep: true},
		},
	}
	run := domain.Run{
		ID: "http-pass-run", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed",
		Nodes: map[string]*domain.NodeRun{
			"verify": {NodeID: "verify", Status: "failed", Attempt: 1, Error: "exit status 1"},
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}

	code, body := postControl(t, g, "/api/runs/http-pass-run/pass", map[string]any{"nodeId": "verify"})
	if code != http.StatusConflict {
		t.Fatalf("pass without override = %d %s, want 409", code, body)
	}
	stored, _ := s.RunState(run.ID)
	if stored.Nodes["verify"].Status != "failed" {
		t.Fatalf("verify status = %q, want failed", stored.Nodes["verify"].Status)
	}

	code, body = postControl(t, g, "/api/runs/http-pass-run/pass", map[string]any{"nodeId": "verify", "override": true})
	if code != http.StatusOK {
		t.Fatalf("pass with override = %d %s, want 200", code, body)
	}
	stored, _ = s.RunState(run.ID)
	if stored.Nodes["verify"].Status != "succeeded" {
		t.Fatalf("verify status = %q, want succeeded", stored.Nodes["verify"].Status)
	}
}
