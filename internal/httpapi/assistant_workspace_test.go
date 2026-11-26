package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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
	// Preparing the workspace (as workbench_workspace_open does before handing
	// over to the CLI) rebuilds the missing archived artifacts.
	raw, err := h.callMainBotWorkspace(context.Background(), s, "workbench_workspace_tree", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Notice   string   `json:"workspaceNotice"`
		Restored []string `json:"restoredArtifacts"`
		Snapshot bool     `json:"workspaceRestored"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil || payload.Snapshot || len(payload.Restored) != 1 || !strings.Contains(payload.Notice, "不是完整历史工作区快照恢复") {
		t.Fatal(raw)
	}
	if design, _ := os.ReadFile(filepath.Join(workspace, "design.md")); string(design) != "historical design" {
		t.Fatalf("design.md = %q", design)
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
	write("report.py", "print('工单日报')\n")
	write("logo.png", "not really an image")
	write("blob.html", "a\x00b")
	a.deps.engine = en
	h := &agentToolGateway{deps: a.deps}
	h.api = New(a.repo, en, tracker.NewService(), "", "")
	user := owner
	user.GitToken = "gt-secret-attach-value"
	s := &AgentToolSession{Mode: AgentToolAssistant, RuntimeMode: domain.AssistantModeWorkspace, RunID: r.ID, ProjectID: r.ExecutionContext.ProjectID, UserID: owner.ID, User: user}
	// Files reach the user through workbench_publish_artifact's path.
	attach := func(s *AgentToolSession, path string) (string, error) {
		raw, _ := json.Marshal(map[string]any{"path": path})
		result, err := h.publishArtifact(context.Background(), s, raw)
		if err != nil {
			return "", err
		}
		text := result["content"].([]map[string]any)[0]["text"].(string)
		if result["isError"] == true {
			return "", errors.New(text)
		}
		return text, nil
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
	if raw, err := attach(s, "report.py"); err != nil || !strings.Contains(raw, `"attached":"report.py"`) {
		t.Fatalf("attach Python source raw=%s err=%v", raw, err)
	}
	participant := &AgentToolSession{Mode: AgentToolAssistant, RuntimeMode: domain.AssistantModeWorkspace, RunID: r.ID, ProjectID: r.ExecutionContext.ProjectID, UserID: peer.ID, User: peer}
	if _, err := attach(participant, "docs/notes.md"); err != nil || len(participant.takeReplyFiles()) != 1 {
		t.Fatalf("project participant could not attach a shared workspace file: %v", err)
	}
	if raw, err := attach(participant, filepath.Join(workspace, "docs", "notes.md")); err != nil || !strings.Contains(raw, `"attached":"notes.md"`) || len(participant.takeReplyFiles()) != 1 {
		t.Fatalf("attach host workspace path raw=%s err=%v", raw, err)
	}
	files := s.takeReplyFiles()
	if len(files) != 3 || files[0].Name != "demo.html" || files[0].MimeType != "text/html" || files[1].Name != "notes.md" || files[1].MimeType != "text/markdown" {
		t.Fatalf("reply files=%+v", files)
	}
	if file := files[2]; file.Name != "report.py" || file.MimeType != "text/plain" || string(file.Data) != "print('工单日报')\n" {
		t.Fatalf("Python attachment=%+v", file)
	}
	if body := string(files[0].Data); !strings.Contains(body, "v2") || strings.Contains(body, "gt-secret-attach-value") {
		t.Fatalf("attached content not current or not redacted: %q", body)
	}
	if len(s.takeReplyFiles()) != 0 {
		t.Fatal("reply files must be handed over once")
	}
	saved := saveAgentReplyFiles(owner.ID, files, s.ProjectID)
	if len(saved) != 3 {
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
