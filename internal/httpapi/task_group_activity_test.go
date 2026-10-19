package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGroupActivityRequiresProjectAccessAndReturnsDurableEvidence(t *testing.T) {
	base, user := workbenchFixture(t)
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	run := domain.Run{ID: "activity", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", GroupMembers: []string{"dev"}, Nodes: []domain.WorkflowNode{{ID: "n", AgentID: "dev", RequestID: "one"}}}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "succeeded", Attempt: 1, StartedAt: at, FinishedAt: at.Add(time.Minute)}}, Telemetry: []domain.TelemetryEvent{{ID: "proof", NodeID: "n", Timestamp: at.Add(50 * time.Second), Sequence: 1, Context: domain.TelemetryContext{Attempt: 1}, Category: "verification", Name: "process.finished", Level: "info", Attributes: map[string]any{"gateId": "tests", "gateName": "Unit tests", "command": "go test ./..."}}}, Artifacts: []domain.Artifact{{ID: "report", RunID: "activity", NodeID: "n", Attempt: 1, Name: "report", Path: "report.md", Content: "private report body", CreatedAt: at.Add(time.Minute)}}}
	if err := base.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	h := &runsHandlers{deps: base.deps}
	stranger := user
	stranger.ID += 100
	c, rec := workbenchContext(stranger, "GET", "/api/runs/activity/group/activity", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: "activity"}}
	h.groupActivity(c)
	if rec.Code != 403 {
		t.Fatalf("unauthorized %d %s", rec.Code, rec.Body)
	}
	c, rec = workbenchContext(user, "GET", "/api/runs/activity/group/activity", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: "activity"}}
	h.groupActivity(c)
	if rec.Code != 200 {
		t.Fatalf("allowed %d %s", rec.Code, rec.Body)
	}
	var got domain.GroupActivity
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Receipt.Verification.Status != "passed" || len(got.Receipt.Artifacts) != 1 {
		t.Fatalf("durable evidence missing: %+v", got)
	}
}

func TestWorkspaceWriteMakesPreviousVerificationHistorical(t *testing.T) {
	a, owner, _, _, _ := sharedWorkbenchFixture(t)
	root := t.TempDir()
	en := engine.New(a.repo, runtime.NewManager(root))
	run, _ := a.repo.RunState("shared-run")
	at := time.Now().UTC().Add(-2 * time.Minute)
	run.Status = "waiting"
	run.WorkflowSnapshot.CollaborationMode = "group"
	run.WorkflowSnapshot.GroupMembers = []string{"dev"}
	run.WorkflowSnapshot.Nodes = []domain.WorkflowNode{{ID: "completed", AgentID: "dev", RequestID: "done"}}
	run.Nodes = map[string]*domain.NodeRun{"completed": {NodeID: "completed", Status: "succeeded", Attempt: 1, StartedAt: at, FinishedAt: at.Add(time.Minute)}}
	run.Telemetry = []domain.TelemetryEvent{{ID: "last-check", NodeID: "completed", Sequence: 1, Timestamp: at.Add(50 * time.Second), Context: domain.TelemetryContext{Attempt: 1}, Category: "verification", Name: "process.finished", Level: "info", Attributes: map[string]any{"gateId": "check", "command": "test -f app.go"}}}
	if err := a.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, run.ID), 0700); err != nil {
		t.Fatal(err)
	}
	a.deps.engine = en
	gateway := &agentToolGateway{deps: a.deps}
	gateway.api = New(a.repo, en, nil, "", "")
	session := &AgentToolSession{Mode: AgentToolAssistant, RunID: run.ID, ProjectID: run.ExecutionContext.ProjectID, UserID: owner.ID, User: owner}
	_, err := gateway.callMainBotWorkspace(context.Background(), session, "workbench_workspace_write", map[string]any{"path": "app.go", "content": "new content", "expectedSha256": "missing"})
	if err != nil {
		t.Fatal(err)
	}
	current, ok := a.repo.Run(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	got := domain.BuildGroupActivity(current, nil)
	if got.Receipt.Verification.Status != "historical" {
		t.Fatalf("edited files still claim previous pass: %+v", got.Receipt.Verification)
	}
	if len(current.Events) == 0 || current.Events[len(current.Events)-1].Type != "workspace.changed" {
		t.Fatalf("missing workspace change fact: %+v", current.Events)
	}
	count := len(current.Events)
	_, err = gateway.callMainBotWorkspace(context.Background(), session, "workbench_workspace_write", map[string]any{"path": "app.go", "content": "overwrite", "expectedSha256": "missing"})
	if err == nil {
		t.Fatal("conflicting write accepted")
	}
	current, _ = a.repo.Run(run.ID)
	if len(current.Events) != count {
		t.Fatal("failed write created a mutation fact")
	}
}
