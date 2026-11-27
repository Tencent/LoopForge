package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

func TestWorkspacePreparationAPISeparatesReadersAndWriters(t *testing.T) {
	a, owner, peer, out, _ := sharedWorkbenchFixture(t)
	if err := a.repo.PutUser(domain.User{LoginName: "read-only-workspace"}); err != nil {
		t.Fatal(err)
	}
	viewer, _ := a.repo.UserByLoginName("read-only-workspace")
	project, _ := a.repo.Project("workbench")
	project.Members = append(project.Members, domain.ProjectMember{UserID: viewer.ID, Role: "viewer"})
	if err := a.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	en := engine.New(a.repo, runtime.NewManager(root))
	a.deps.engine = en
	h := &runsHandlers{deps: a.deps}
	run, _ := a.repo.RunState("shared-run")
	run.Status = "succeeded"
	run.WorkflowSnapshot.SandboxProfileID = "test-native"
	run.WorkflowSnapshot.Nodes = []domain.WorkflowNode{{ID: "prep", Name: "运行环境准备", Type: "script", Script: "mkdir -p .agentflow; printf secret-internal-output; printf ready > .agentflow/prepared", RunOnWorkspaceRebuild: true}}
	run.Nodes = map[string]*domain.NodeRun{"prep": {NodeID: "prep", Status: "succeeded", Attempt: 7, Output: "business history"}}
	if err := a.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, run.ID), 0700); err != nil {
		t.Fatal(err)
	}
	call := func(user domain.User, method string) (int, string) {
		t.Helper()
		c, w := workbenchContext(user, method, "/api/runs/shared-run/workspace/preparation", bytes.NewBufferString(`{}`))
		c.Params = gin.Params{{Key: "id", Value: run.ID}}
		h.workspacePreparation(c)
		return w.Code, w.Body.String()
	}
	if code, _ := call(out, "GET"); code != 403 {
		t.Fatal("outsider could read status", code)
	}
	if code, body := call(viewer, "GET"); code != 200 || !strings.Contains(body, "unprepared") {
		t.Fatal(code, body)
	}
	if code, _ := call(viewer, "POST"); code != 403 {
		t.Fatal("viewer could execute preparation", code)
	}
	code, body := call(peer, "POST")
	if code != 200 || !strings.Contains(body, `"status":"ready"`) {
		t.Fatal(code, body)
	}
	if strings.Contains(body, "secret-internal-output") || strings.Contains(body, "Fingerprint") || strings.Contains(body, "environment\":") {
		t.Fatal("preparation internals leaked", body)
	}
	if code, body = call(owner, "GET"); code != 200 || !strings.Contains(body, `"status":"ready"`) {
		t.Fatal(code, body)
	}
	current, _ := a.repo.RunState(run.ID)
	if current.Status != "succeeded" || current.Nodes["prep"].Attempt != 7 || current.Nodes["prep"].Output != "business history" {
		t.Fatal("preflight changed business history", current)
	}
}

func TestWorkspacePreparationLeaseContentionIsRetryable(t *testing.T) {
	a, user := workbenchFixture(t)
	a.engine = engine.New(a.repo, runtime.NewManager(t.TempDir()))
	h := &runsHandlers{deps: a.deps}
	run := domain.Run{ID: "handoff-ide-busy", Status: "waiting", PauseRequested: true, ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, Nodes: map[string]*domain.NodeRun{}}
	if err := a.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	token, err := a.engine.BeginConversationWorkspaceUse(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer a.engine.EndConversationWorkspaceUse(context.Background(), run.ID, token)
	c, w := workbenchContext(user, "POST", "/api/runs/"+run.ID+"/workspace/preparation", bytes.NewBufferString(`{}`))
	c.Params = gin.Params{{Key: "id", Value: run.ID}}
	h.workspacePreparation(c)
	if w.Code != 409 || w.Header().Get("Retry-After") != "2" {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		Status, Code string
		RetryAfterMs int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "busy" || response.Code != "workspace_busy" || response.RetryAfterMs != 2000 {
		t.Fatal(response)
	}
}
