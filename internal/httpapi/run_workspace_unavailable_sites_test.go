package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

// workspaceUnavailableView is what the panel decodes from the 「工作区不可用」
// response; every call site of workspaceUnavailablePayload must fill it with
// its own reason.
type workspaceUnavailableView struct {
	Available         *bool                       `json:"available"`
	ReadOnly          bool                        `json:"readOnly"`
	Reason            string                      `json:"reason"`
	RunStatus         string                      `json:"runStatus"`
	RepositoryDetails []workspaceRepositoryDetail `json:"repositoryDetails"`
}

func assertWorkspaceUnavailableView(t *testing.T, got workspaceUnavailableView, reason, status string) {
	t.Helper()
	if got.Available == nil || *got.Available || !got.ReadOnly {
		t.Fatalf("available/readOnly = %v/%v, want false/true", got.Available, got.ReadOnly)
	}
	if got.Reason != reason {
		t.Fatalf("reason = %q, want %q", got.Reason, reason)
	}
	if got.RunStatus != status {
		t.Fatalf("runStatus = %q, want %q", got.RunStatus, status)
	}
	if len(got.RepositoryDetails) != 1 || got.RepositoryDetails[0].Name != "app" || got.RepositoryDetails[0].CloneURL != "https://example.com/app.git" {
		t.Fatalf("repositoryDetails = %+v", got.RepositoryDetails)
	}
}

// A local run whose workspace directory was never created reports the local
// reason through the shared payload, not an empty or generic one.
func TestRunWorkspaceMissingLocalWorkspaceReportsReason(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	run := domain.Run{
		ID: "run-local-missing", Status: "failed", Nodes: map[string]*domain.NodeRun{},
		Repositories: []domain.RepositoryContext{{Name: "app", CloneURL: "https://user:secret@example.com/app.git", WorkBranch: "feat/x"}},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	handler := New(store, engine.New(store, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	info := requestEmptyJSON[workspaceUnavailableView](t, handler, http.MethodGet, "/api/runs/run-local-missing/workspace", http.StatusOK)
	assertWorkspaceUnavailableView(t, info, "工作区尚未创建或已被清理", "failed")
}

// When the runner answers the info request with 404, the panel must show the
// runner's own error as the reason (it explains why the directory is gone).
func TestRunWorkspaceRemoteNotFoundReportsRunnerError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := cvBNewHTTPFixture(t)
	run := domain.Run{
		ID: "run-remote-404", Status: "succeeded", Nodes: map[string]*domain.NodeRun{},
		Repositories:     []domain.RepositoryContext{{Name: "app", CloneURL: "https://user:secret@example.com/app.git"}},
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner-404"},
	}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRunner(domain.Runner{ID: "runner-404", Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	hub := newRunnerControlHub()
	conn := &runnerControlConn{runnerID: "runner-404", send: make(chan runnerControlMessage, 4), done: make(chan struct{})}
	hub.conns[conn.runnerID] = conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for message := range conn.send {
			hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{Status: http.StatusNotFound, Error: "执行机器上的运行目录不存在"})
		}
	}()
	t.Cleanup(func() {
		close(conn.send)
		<-done
	})
	h := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}, runnerControls: hub}

	router := gin.New()
	router.GET("/api/runs/:id/workspace", h.runWorkspace)
	router.GET("/api/runs/:id/workspace/tree", h.runWorkspace)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs/run-remote-404/workspace", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("info status = %d: %s", rec.Code, rec.Body.String())
	}
	var info workspaceUnavailableView
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceUnavailableView(t, info, "执行机器上的运行目录不存在", "succeeded")

	// Only the info view degrades to the unavailable payload; other
	// operations keep the runner's 404 so the panel shows an error.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs/run-remote-404/workspace/tree", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("tree status = %d: %s", rec.Code, rec.Body.String())
	}
	var tree map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if tree["error"] != "执行机器上的运行目录不存在" || tree["available"] != nil {
		t.Fatalf("tree body = %v", tree)
	}
}
