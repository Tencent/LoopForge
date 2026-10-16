package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestMainBotWorkspaceWritesAndCommandsUseTaskFiles(t *testing.T) {
	a, owner, _, outsider, _ := sharedWorkbenchFixture(t)
	root := t.TempDir()
	en := engine.New(a.repo, runtime.NewManager(root))
	r, _ := a.repo.RunState("shared-run")
	r.Status = "running"
	r.PauseRequested = false
	r.WorkflowSnapshot.CollaborationMode = "group"
	r.Nodes = map[string]*domain.NodeRun{}
	if err := a.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, r.ID)
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	a.deps.engine = en
	h := &agentToolGateway{deps: a.deps}
	h.api = New(a.repo, en, tracker.NewService(), "", "")
	s := &AgentToolSession{Mode: AgentToolAssistant, RunID: r.ID, ProjectID: r.ExecutionContext.ProjectID, UserID: owner.ID, User: owner}
	call := func(name string, args map[string]any) (string, error) {
		return h.callMainBotWorkspace(context.Background(), s, "workbench_workspace_"+name, args)
	}
	raw, err := call("read", map[string]any{"path": "note.txt"})
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Content string `json:"content"`
		SHA     string `json:"sha256"`
	}
	if err = json.Unmarshal([]byte(raw), &file); err != nil || file.Content != "before" {
		t.Fatal(raw, err)
	}
	if _, err = call("write", map[string]any{"path": "note.txt", "content": "after", "expectedSha256": file.SHA}); err != nil {
		t.Fatal(err)
	}
	if _, err = call("write", map[string]any{"path": "note.txt", "content": "stale", "expectedSha256": file.SHA}); err == nil {
		t.Fatal("overwrote another edit")
	}
	if _, err = call("write", map[string]any{"path": "created.txt", "content": "new", "expectedSha256": "missing"}); err != nil {
		t.Fatal(err)
	}
	raw, err = call("command", map[string]any{"command": "test \"$(cat note.txt)\" = after && printf 'verified'"})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exitCode"`
	}
	if err = json.Unmarshal([]byte(raw), &result); err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, "verified") {
		t.Fatal(raw, err)
	}
	raw, err = call("command", map[string]any{"command": "exit 7"})
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(raw), &result) != nil || result.ExitCode != 7 {
		t.Fatal(raw)
	}
	if _, err = call("write", map[string]any{"path": "../escaped", "content": "bad", "expectedSha256": "missing"}); err == nil {
		t.Fatal("escaped workspace")
	}
	if _, err = call("write", map[string]any{"path": ".env", "content": "bad", "expectedSha256": "missing"}); err == nil {
		t.Fatal("wrote hidden metadata")
	}
	s.User = outsider
	s.UserID = outsider.ID
	if _, err = call("write", map[string]any{"path": "note.txt", "content": "foreign", "expectedSha256": runtime.WorkspaceContentSHA("after")}); err == nil {
		t.Fatal("outsider wrote files")
	}
	s.User = owner
	s.UserID = owner.ID
	s.ExecMode.Consultation = true
	if len(mainBotWorkspaceTools(s)) != 0 {
		t.Fatal("consultation received write tools")
	}
	if _, err = call("read", map[string]any{"path": "note.txt"}); err == nil {
		t.Fatal("consultation accessed main tools")
	}
	s.ExecMode.Consultation = false
	s.ProjectID = "foreign"
	if _, err = call("read", map[string]any{"path": "note.txt"}); err == nil {
		t.Fatal("foreign run accepted")
	}
	s.ProjectID = r.ExecutionContext.ProjectID
	r.WorkflowSnapshot.CollaborationMode = ""
	if err = a.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if _, err = call("write", map[string]any{"path": "note.txt", "content": "active SOP", "expectedSha256": runtime.WorkspaceContentSHA("after")}); err == nil {
		t.Fatal("active SOP was edited")
	}
	r.Status = "succeeded"
	if err = a.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if _, err = call("write", map[string]any{"path": "note.txt", "content": "finished SOP edit", "expectedSha256": runtime.WorkspaceContentSHA("after")}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.repo.WorkspaceCheckpointArchive(r.ID); err != nil {
		t.Fatal("Main Bot did not persist edits", err)
	}
	r, _ = a.repo.RunState(r.ID)
	r.WorkspaceCleanedAt = time.Now().UTC()
	if err = a.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}
	raw, err = call("read", map[string]any{"path": "note.txt"})
	if err != nil || (!strings.Contains(raw, "finished SOP edit") || !strings.Contains(raw, `"workspaceRestored":true`)) {
		t.Fatal("Main Bot did not restore reclaimed edits", raw, err)
	}
	restored, _ := a.repo.RunState(r.ID)
	if restored.Status != "succeeded" || !restored.WorkspaceCleanedAt.IsZero() {
		t.Fatal("restore altered completed SOP", restored.Status)
	}

}

func TestMainBotLegacyArtifactRepairExplainsPartialRebuild(t *testing.T) {
	a, owner, _, _, _ := sharedWorkbenchFixture(t)
	en := engine.New(a.repo, runtime.NewManager(t.TempDir()))
	r, _ := a.repo.RunState("shared-run")
	r.Status = "waiting"
	r.PauseRequested = true
	r.Nodes = map[string]*domain.NodeRun{}
	r.Artifacts = []domain.Artifact{{ID: "design-archived", RunID: r.ID, NodeID: "design", Path: "design.md", Content: "historical design", SHA256: runtime.WorkspaceContentSHA("historical design"), CreatedAt: time.Now()}, {ID: "change-archived", RunID: r.ID, NodeID: "implement", Path: "change-report.md", Content: "old change", SHA256: runtime.WorkspaceContentSHA("old change"), CreatedAt: time.Now()}}
	if err := a.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := a.repo.DB.Table("runs").Where("id = ?", r.ID).Update("pause_requested", true).Error; err != nil {
		t.Fatal(err)
	}
	workspace := en.RunWorkspace(r.ID)
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "change-report.md"), []byte("new group edits"), 0600); err != nil {
		t.Fatal(err)
	}
	a.deps.engine = en
	h := &agentToolGateway{deps: a.deps}
	h.api = New(a.repo, en, tracker.NewService(), "", "")
	s := &AgentToolSession{Mode: AgentToolAssistant, RunID: r.ID, ProjectID: r.ExecutionContext.ProjectID, UserID: owner.ID, User: owner}
	raw, err := h.callMainBotWorkspace(context.Background(), s, "workbench_workspace_read", map[string]any{"path": "design.md"})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Content  string   `json:"content"`
		Notice   string   `json:"workspaceNotice"`
		Restored []string `json:"restoredArtifacts"`
		Snapshot bool     `json:"workspaceRestored"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil || payload.Content != "historical design" || payload.Snapshot || len(payload.Restored) != 1 || !strings.Contains(payload.Notice, "不是完整历史工作区快照恢复") {
		t.Fatal(raw)
	}
	got, _ := os.ReadFile(filepath.Join(workspace, "change-report.md"))
	if string(got) != "new group edits" {
		t.Fatal("lost group edits")
	}
	state, _ := a.repo.RunState(r.ID)
	if state.Status != "waiting" || !state.PauseRequested {
		t.Fatal("repair restarted SOP")
	}
}
