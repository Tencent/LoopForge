package engine

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestGitLabConversationLocalPushUsesRequesterAndPreservesWorkspace(t *testing.T) {
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "team", "repo.git")
	if err := os.MkdirAll(filepath.Dir(remote), 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	testGit(t, remote, "config", "http.receivepack", "true")
	handler := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitLab test"`)
			w.WriteHeader(401)
			return
		}
		if password != "requester-gitlab-secret" {
			t.Error("wrong provider/requester credential")
			w.WriteHeader(403)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(st, runtime.NewManager(filepath.Join(root, "workspaces")))
	run := domain.Run{ID: "gitlab-local-push", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ExecutionMode: domain.ExecutionModeCloud}, Repositories: []domain.RepositoryContext{{RepositoryID: "gl", Provider: "gitlab", Name: "repo", CloneURL: srv.URL + "/team/repo.git", GongfengProjectID: "gitlab:" + srv.URL + "/team/repo", BaseBranch: "main", WorkBranch: "feature", WorkspaceDir: "repo"}}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(e.sandboxes.RunWorkspace(run.ID), "repo")
	if out, err := exec.Command("git", "clone", remote, checkout).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	testGit(t, checkout, "checkout", "-b", "main")
	testGit(t, checkout, "config", "user.name", "Original")
	testGit(t, checkout, "config", "user.email", "original@example.test")
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, checkout, "add", ".")
	testGit(t, checkout, "commit", "-m", "base")
	testGit(t, checkout, "push", "origin", "main")
	testGit(t, checkout, "checkout", "-b", "feature")
	testGit(t, checkout, "remote", "set-url", "origin", srv.URL+"/team/repo.git")
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	credentials := ConversationGitLabCredentials{BaseURL: srv.URL, Token: "requester-gitlab-secret"}
	heads, err := e.PushConversationWorkspace(context.Background(), run.ID, "", "alice", "alice@example.test", "change", "", credentials)
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 || heads[0].Head != testGit(t, remote, "rev-parse", "feature") {
		t.Fatalf("heads=%+v", heads)
	}
	if testGit(t, checkout, "branch", "--show-current") != "feature" {
		t.Error("workspace branch changed")
	}
	if testGit(t, remote, "log", "-1", "--format=%an", "feature") != "alice" {
		t.Error("commit not attributed to requester")
	}
	config, err := os.ReadFile(filepath.Join(checkout, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "secret") {
		t.Error("persisted token in workspace")
	}
	// A substituted repository on the same instance must fail before committing.
	testGit(t, checkout, "remote", "set-url", "origin", srv.URL+"/team/other.git")
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("not committed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := testGit(t, checkout, "rev-parse", "HEAD")
	if _, err := e.PushConversationWorkspace(context.Background(), run.ID, "", "alice", "alice@example.test", "unsafe", "", credentials); err == nil {
		t.Error("accepted substituted origin")
	}
	if testGit(t, checkout, "rev-parse", "HEAD") != before {
		t.Error("committed before destination validation")
	}
}
