package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestRunWorkspaceBrowseIsReadOnlyAndHidesSensitiveFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	workspace := filepath.Join(root, "run-workspace")
	if err := os.MkdirAll(filepath.Join(workspace, ".agentflow", "private-home"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteWorkspaceFile(t, filepath.Join(workspace, "main.go"), "package main\n")
	mustWriteWorkspaceFile(t, filepath.Join(workspace, ".env"), "TOKEN=secret\n")
	mustWriteWorkspaceFile(t, filepath.Join(workspace, ".agentflow", "private-home", "credentials.json"), "secret\n")
	mustWriteWorkspaceFile(t, filepath.Join(workspace, ".agentflow", "runtime", "mcp-servers.json"), "secret\n")
	mustWriteWorkspaceFile(t, filepath.Join(workspace, ".agentflow", "session.json"), "generated metadata\n")
	mustRunWorkspaceGit(t, workspace, "init")
	mustRunWorkspaceGit(t, workspace, "config", "user.email", "test@example.com")
	mustRunWorkspaceGit(t, workspace, "config", "user.name", "Test")
	mustRunWorkspaceGit(t, workspace, "add", "main.go")
	mustRunWorkspaceGit(t, workspace, "commit", "-m", "initial")
	mustWriteWorkspaceFile(t, filepath.Join(workspace, "main.go"), "package main\n\nfunc main() {}\n")
	mustWriteWorkspaceFile(t, filepath.Join(workspace, "notes.txt"), "worker note\n")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWriteWorkspaceFile(t, outside, "host secret\n")
	if err := os.Symlink(outside, filepath.Join(workspace, "outside-link")); err != nil {
		t.Fatal(err)
	}

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.PutRun(domain.Run{ID: "run-workspace", Status: "waiting", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	handler := New(store, engine.New(store, runtime.NewManager(root)), tracker.NewService(), "", "")

	info := requestEmptyJSON[struct {
		Available    bool              `json:"available"`
		ReadOnly     bool              `json:"readOnly"`
		Changes      []workspaceChange `json:"changes"`
		Repositories []string          `json:"repositories"`
	}](t, handler, http.MethodGet, "/api/runs/run-workspace/workspace", http.StatusOK)
	if !info.Available || !info.ReadOnly {
		t.Fatalf("unexpected workspace info: %+v", info)
	}
	if len(info.Repositories) != 1 || info.Repositories[0] != "workspace" {
		t.Fatalf("root git workspace should report a single repository, got %+v", info.Repositories)
	}
	if !workspaceChangeExists(info.Changes, "main.go") || !workspaceChangeExists(info.Changes, "notes.txt") {
		t.Fatalf("missing git changes: %+v", info.Changes)
	}
	for _, change := range info.Changes {
		if change.Repository != "workspace" {
			t.Fatalf("root git change %q should belong to workspace, got %q", change.Path, change.Repository)
		}
	}
	if workspaceChangeExists(info.Changes, ".env") {
		t.Fatalf("sensitive file leaked through git status: %+v", info.Changes)
	}

	tree := requestEmptyJSON[struct {
		Entries []workspaceEntry `json:"entries"`
	}](t, handler, http.MethodGet, "/api/runs/run-workspace/workspace/tree", http.StatusOK)
	paths := map[string]bool{}
	for _, entry := range tree.Entries {
		paths[entry.Path] = true
	}
	if !paths["main.go"] || !paths["notes.txt"] {
		t.Fatalf("workspace tree = %+v", tree.Entries)
	}
	for _, hidden := range []string{".git", ".env", ".agentflow", ".agentflow/session.json", ".agentflow/private-home", ".agentflow/private-home/credentials.json", ".agentflow/runtime", ".agentflow/runtime/mcp-servers.json"} {
		if paths[hidden] {
			t.Fatalf("sensitive path %q leaked through tree", hidden)
		}
	}

	file := requestEmptyJSON[struct {
		Content string `json:"content"`
	}](t, handler, http.MethodGet, "/api/runs/run-workspace/workspace/file?path=main.go", http.StatusOK)
	if !strings.Contains(file.Content, "func main") {
		t.Fatalf("file content = %q", file.Content)
	}

	diff := requestEmptyJSON[struct {
		Diff     string `json:"diff"`
		Original string `json:"original"`
		Modified string `json:"modified"`
	}](t, handler, http.MethodGet, "/api/runs/run-workspace/workspace/diff?path=main.go", http.StatusOK)
	if !strings.Contains(diff.Diff, "+func main() {}") {
		t.Fatalf("diff = %q", diff.Diff)
	}
	if diff.Original != "package main\n" || !strings.Contains(diff.Modified, "func main") {
		t.Fatalf("diff editor content = original %q, modified %q", diff.Original, diff.Modified)
	}
	untrackedDiff := requestEmptyJSON[struct {
		Original string `json:"original"`
		Modified string `json:"modified"`
	}](t, handler, http.MethodGet, "/api/runs/run-workspace/workspace/diff?path=notes.txt", http.StatusOK)
	if untrackedDiff.Original != "" || untrackedDiff.Modified != "worker note\n" {
		t.Fatalf("untracked diff editor content = original %q, modified %q", untrackedDiff.Original, untrackedDiff.Modified)
	}

	search := requestEmptyJSON[struct {
		Results []workspaceSearchResult `json:"results"`
	}](t, handler, http.MethodGet, "/api/runs/run-workspace/workspace/search?q=worker", http.StatusOK)
	if len(search.Results) != 1 || search.Results[0].Path != "notes.txt" || search.Results[0].Line != 1 {
		t.Fatalf("search results = %+v", search.Results)
	}
	hiddenSearch := requestEmptyJSON[struct {
		Results []workspaceSearchResult `json:"results"`
	}](t, handler, http.MethodGet, "/api/runs/run-workspace/workspace/search?q=secret", http.StatusOK)
	if len(hiddenSearch.Results) != 0 {
		t.Fatalf("sensitive search results leaked: %+v", hiddenSearch.Results)
	}

	log := requestEmptyJSON[struct {
		Branch  string `json:"branch"`
		Commits []struct {
			ID      string `json:"id"`
			Message string `json:"message"`
			Author  string `json:"author"`
		} `json:"commits"`
	}](t, handler, http.MethodGet, "/api/runs/run-workspace/workspace/log", http.StatusOK)
	if len(log.Commits) == 0 || log.Commits[0].Message != "initial" || log.Commits[0].ID == "" {
		t.Fatalf("workspace log = %+v", log)
	}
	commit := requestEmptyJSON[struct {
		Files []workspaceCommitFile `json:"files"`
	}](t, handler, http.MethodGet, "/api/runs/run-workspace/workspace/commit?id="+log.Commits[0].ID, http.StatusOK)
	if !workspaceCommitFileExists(commit.Files, "main.go") {
		t.Fatalf("commit files = %+v", commit.Files)
	}
	blob := requestEmptyJSON[struct {
		Content string `json:"content"`
	}](t, handler, http.MethodGet, "/api/runs/run-workspace/workspace/file?path=main.go&ref="+log.Commits[0].ID, http.StatusOK)
	if blob.Content != "package main\n" {
		t.Fatalf("commit blob = %q", blob.Content)
	}

	for _, path := range []string{"../outside", ".env", ".agentflow/private-home/credentials.json", "outside-link"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs/run-workspace/workspace/file?path="+path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("path %q status = %d, body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestRunWorkspaceReportsCleanedWorkspace(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.PutRun(domain.Run{ID: "run-cleaned", Status: "succeeded", WorkspaceCleanedAt: time.Now().UTC(), Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	handler := New(store, engine.New(store, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	info := requestEmptyJSON[struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	}](t, handler, http.MethodGet, "/api/runs/run-cleaned/workspace", http.StatusOK)
	if info.Available || !strings.Contains(info.Reason, "已回收") {
		t.Fatalf("unexpected workspace info: %+v", info)
	}
}

func TestRunWorkspaceReportsRemoteRunnerBoundary(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	run := domain.Run{
		ID:     "run-remote-workspace",
		Status: "running",
		Nodes:  map[string]*domain.NodeRun{},
		ExecutionContext: domain.TelemetryContext{
			ExecutionMode:     domain.ExecutionModeRunner,
			ExecutionRunnerID: "runner-1",
		},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	handler := New(store, engine.New(store, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	info := requestEmptyJSON[struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	}](t, handler, http.MethodGet, "/api/runs/run-remote-workspace/workspace", http.StatusOK)
	if info.Available || !strings.Contains(info.Reason, "Daemon 已卸载") || !strings.Contains(info.Reason, "无法恢复") {
		t.Fatalf("unexpected remote workspace info: %+v", info)
	}

	file := requestEmptyJSON[struct {
		Error string `json:"error"`
	}](t, handler, http.MethodGet, "/api/runs/run-remote-workspace/workspace/file?path=README.md", http.StatusGone)
	if !strings.Contains(file.Error, "Daemon 已卸载") {
		t.Fatalf("unexpected remote workspace file error: %+v", file)
	}
}

func mustWriteWorkspaceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRunWorkspaceGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func workspaceChangeExists(changes []workspaceChange, path string) bool {
	for _, change := range changes {
		if change.Path == path {
			return true
		}
	}
	return false
}

func workspaceCommitFileExists(files []workspaceCommitFile, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}
