package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
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
	a, owner, _, _, _ := sharedWorkbenchFixture(t)
	root := t.TempDir()
	en := engine.New(a.repo, runtime.NewManager(root))
	r, _ := a.repo.RunState("shared-run")
	r.Status = "running"
	r.PauseRequested = false
	r.Kind = domain.RunKindWorkspace
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
	s.ExecMode = assistantExecModeForApproval(AssistantApprovalAssisted)
	allowReview := false
	reviews := 0
	s.approvalReviewer = func(_ context.Context, tool, _ string, args map[string]any) assistantApprovalReview {
		reviews++
		if args["runId"] != r.ID || args["arguments"] == nil {
			t.Fatal("review lost workspace scope", args)
		}
		outcome := "deny"
		if allowReview {
			outcome = "allow"
		}
		return assistantApprovalReview{Outcome: outcome, RiskLevel: "low", UserAuthorization: "high", Rationale: "核对本次具体工作区操作"}
	}
	if _, err = call("write", map[string]any{"path": "blocked.txt", "content": "bad", "expectedSha256": "missing"}); err == nil {
		t.Fatal("unapproved workspace write executed")
	}
	if _, err = call("command", map[string]any{"command": "touch blocked.txt"}); err == nil {
		t.Fatal("unapproved command executed")
	}
	if _, err = os.Stat(filepath.Join(workspace, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatal("review happened after the side effect")
	}
	allowReview = true
	if _, err = call("write", map[string]any{"path": "approved.txt", "content": "approved", "expectedSha256": "missing"}); err != nil {
		t.Fatal(err)
	}
	if reviews != 3 {
		t.Fatal("not every concrete workspace write was reviewed", reviews)
	}
	s.ExecMode = assistantExecMode{}
	s.ProjectID = "foreign"
	if _, err = call("read", map[string]any{"path": "note.txt"}); err == nil {
		t.Fatal("foreign run accepted")
	}
	s.ProjectID = r.ExecutionContext.ProjectID
	r.Kind = ""
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

// The user cannot open a path on the execution machine, so Main Bot hands a
// workspace file over as an attachment of its reply instead.
func TestMainBotAttachesWorkspaceFilesToReply(t *testing.T) {
	a, owner, peer, _, _ := sharedWorkbenchFixture(t)
	en := engine.New(a.repo, runtime.NewManager(t.TempDir()))
	r, _ := a.repo.RunState("shared-run")
	r.ID, r.Status, r.Nodes = "attach-run", "succeeded", map[string]*domain.NodeRun{}
	if err := a.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	workspace := en.RunWorkspace(r.ID)
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("demo.html", "<h1>v1</h1>")
	write("docs/notes.md", "# 说明")
	write("logo.png", "not really an image")
	write("blob.html", "a\x00b")
	a.deps.engine = en
	h := &agentToolGateway{deps: a.deps}
	h.api = New(a.repo, en, tracker.NewService(), "", "")
	user := owner
	user.GitToken = "gt-secret-attach-value"
	s := &AgentToolSession{Mode: AgentToolAssistant, RunID: r.ID, ProjectID: r.ExecutionContext.ProjectID, UserID: owner.ID, User: user}
	attach := func(s *AgentToolSession, path string) (string, error) {
		return h.callMainBotWorkspace(context.Background(), s, "workbench_workspace_attach", map[string]any{"path": path})
	}
	if raw, err := attach(s, "/workspace/demo.html"); err != nil || !strings.Contains(raw, `"attached":"demo.html"`) {
		t.Fatalf("attach demo raw=%s err=%v", raw, err)
	}
	for _, bad := range []string{"", "../escape.html", "logo.png", "blob.html", "missing.html"} {
		if raw, err := attach(s, bad); err == nil {
			t.Fatalf("attached %q: %s", bad, raw)
		}
	}
	write("demo.html", "<h1>v2</h1><p>gt-secret-attach-value</p>")
	if _, err := attach(s, "demo.html"); err != nil {
		t.Fatal(err)
	}
	if _, err := attach(s, "docs/notes.md"); err != nil {
		t.Fatal(err)
	}
	participant := &AgentToolSession{Mode: AgentToolAssistant, RunID: r.ID, ProjectID: r.ExecutionContext.ProjectID, UserID: peer.ID, User: peer}
	if _, err := attach(participant, "docs/notes.md"); err != nil || len(participant.takeReplyFiles()) != 1 {
		t.Fatalf("project participant could not attach a shared workspace file: %v", err)
	}
	files := s.takeReplyFiles()
	if len(files) != 2 || files[0].Name != "demo.html" || files[0].MimeType != "text/html" || files[1].Name != "notes.md" || files[1].MimeType != "text/markdown" {
		t.Fatalf("reply files=%+v", files)
	}
	if body := string(files[0].Data); !strings.Contains(body, "v2") || strings.Contains(body, "gt-secret-attach-value") {
		t.Fatalf("attached content not current or not redacted: %q", body)
	}
	if len(s.takeReplyFiles()) != 0 {
		t.Fatal("reply files must be handed over once")
	}
	saved := saveAgentReplyFiles(owner.ID, files, s.ProjectID)
	if len(saved) != 2 {
		t.Fatalf("saved=%+v", saved)
	}
	record, _, err := loadWorkbenchAttachment(owner.ID, saved[0].ID)
	if err != nil || record.ProjectID != s.ProjectID || record.MimeType != "text/html" || record.Name != "demo.html" {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestReplyScreenshotsKeepLatestDistinctFrames(t *testing.T) {
	s := &AgentToolSession{}
	for i := 1; i <= agentReplyScreenshotLimit+2; i++ {
		frame := []byte(fmt.Sprintf("frame-%d", i))
		s.addReplyScreenshot("image/jpeg", frame)
		s.addReplyScreenshot("image/jpeg", frame)
	}
	if err := s.attachReplyFile("demo.html", "text/html", []byte("<p>")); err != nil {
		t.Fatal(err)
	}
	files := s.takeReplyFiles()
	var shots []string
	for _, f := range files {
		if f.Screenshot {
			shots = append(shots, f.Name+"="+string(f.Data))
		}
	}
	want := []string{"browser-screenshot-3.jpg=frame-3", "browser-screenshot-4.jpg=frame-4", "browser-screenshot-5.jpg=frame-5", "browser-screenshot-6.jpg=frame-6"}
	if strings.Join(shots, ",") != strings.Join(want, ",") || len(files) != agentReplyScreenshotLimit+1 {
		t.Fatalf("screenshots=%v files=%d", shots, len(files))
	}
	for i := 0; i < agentReplyFileLimit; i++ {
		if err := s.attachReplyFile(fmt.Sprintf("f%d.txt", i), "text/plain", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.attachReplyFile("one-more.txt", "text/plain", nil); err == nil {
		t.Fatal("file limit not enforced")
	}
	if err := s.attachReplyFile("f0.txt", "text/plain", []byte("new")); err != nil {
		t.Fatal("re-attaching an existing name must replace it", err)
	}
}
