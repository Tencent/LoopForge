package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"github.com/gin-gonic/gin"
)

func TestRunnerCheckpointLeaseAuthentication(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	runnerID, session := registerTestRunner(t, f.h)
	_, otherSession := registerTestRunner(t, f.h)
	run := domain.Run{ID: "runner-checkpoint-lease", Status: "running", ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runnerID}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	token, err := f.s.BeginWorkspaceUse(run.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	call := func(session, lease string) int {
		t.Helper()
		body := fmt.Sprintf(`{"runId":%q,"token":%q}`, run.ID, lease)
		req := httptest.NewRequest(http.MethodPost, "/runners/workspace/checkpoint-lease", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		if session != "" {
			req.Header.Set("Authorization", "Bearer "+session)
		}
		response := httptest.NewRecorder()
		f.h.ServeHTTP(response, req)
		return response.Code
	}
	if got := call("", token); got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d", got)
	}
	if got := call(otherSession, token); got != http.StatusConflict {
		t.Fatalf("other runner = %d", got)
	}
	if got := call(session, "incorrect"); got != http.StatusConflict {
		t.Fatalf("invalid lease = %d", got)
	}
	if got := call(session, token); got != http.StatusNoContent {
		t.Fatalf("owner = %d", got)
	}
}

func TestNodeCheckpointHTTPPermissionsAndRewind(t *testing.T) {
	a, owner, peer, outsider, _ := sharedWorkbenchFixture(t)
	manager := runtime.NewManager(t.TempDir())
	a.deps.engine = engine.New(a.repo, manager)
	h := &runsHandlers{deps: a.deps}
	run, _ := a.repo.RunState("shared-run")
	run.Status = "running"
	run.ExecutionContext.NodeCheckpointsEnabled = true
	run.Repository = domain.RepositoryContext{RepositoryID: "repo"}
	run.Nodes = map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "succeeded", Attempt: 1}}
	root := manager.RunWorkspace(run.ID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cvBGit(t, root, "init", "-q")
	file := filepath.Join(root, "file")
	cvBWrite(t, file, []byte("before\n"))
	cvBGit(t, root, "add", ".")
	cvBGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "baseline")
	start, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, run.RepositoryContexts(), 1)
	if err != nil {
		t.Fatal(err)
	}
	cvBWrite(t, file, []byte("after\n"))
	end, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, run.RepositoryContexts(), 1)
	if err != nil {
		t.Fatal(err)
	}
	start.End = end.Repositories
	run.Nodes["a"].Checkpoints = []domain.NodeCheckpoint{start}
	if err := a.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	call := func(user domain.User, method, query string) (int, string) {
		t.Helper()
		c, w := workbenchContext(user, method, "/api/runs/"+run.ID+"/workspace/rewind"+query, bytes.NewBufferString(`{"nodeId":"a","attempt":1}`))
		c.Params = gin.Params{{Key: "id", Value: run.ID}}
		if method == "GET" {
			h.nodeCheckpointDiff(c, run)
		} else {
			h.workspaceRewind(c)
		}
		return w.Code, w.Body.String()
	}
	if code, body := call(outsider, "GET", "?node=a&attempt=1"); code != 403 {
		t.Fatalf("outsider diff: %d %s", code, body)
	}
	if code, body := call(peer, "GET", "?node=a&attempt=1"); code != 200 || !bytes.Contains([]byte(body), []byte("+after")) {
		t.Fatalf("diff: %d %s", code, body)
	}
	run.Nodes["a"].Status = "running"
	run.Nodes["a"].Checkpoints[0].End = nil
	if code, body := call(peer, "GET", "?node=a&attempt=1"); code != 200 || !bytes.Contains([]byte(body), []byte("+after")) || !bytes.Contains([]byte(body), []byte(`"live":true`)) {
		t.Fatalf("live diff: %d %s", code, body)
	}
	run.Nodes["a"].Status = "succeeded"
	run.Nodes["a"].Checkpoints[0].End = end.Repositories
	if code, body := call(owner, "POST", ""); code != 409 {
		t.Fatalf("running rewind: %d %s", code, body)
	}
	run, _ = a.repo.RunState(run.ID)
	run.Status = "failed"
	if err := a.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if code, body := call(owner, "POST", ""); code != 200 {
		t.Fatalf("rewind: %d %s", code, body)
	}
	if contents, err := os.ReadFile(file); err != nil || string(contents) != "before\n" {
		t.Fatal(fmt.Sprintf("file %q: %v", contents, err))
	}
}

func TestChildCheckpointRewindPermissions(t *testing.T) {
	a, owner, _, outsider, _ := sharedWorkbenchFixture(t)
	manager := runtime.NewManager(t.TempDir())
	a.deps.engine = engine.New(a.repo, manager)
	h := &runsHandlers{deps: a.deps}
	parent, _ := a.repo.RunState("shared-run")
	parent.Status = "failed"
	parent.ExecutionContext.NodeCheckpointsEnabled = true
	parent.Nodes = map[string]*domain.NodeRun{"sop": {NodeID: "sop", Status: "failed", ChildRunID: "http-child"}}
	if err := a.repo.PutRun(parent); err != nil {
		t.Fatal(err)
	}
	root := manager.RunWorkspace(parent.ID)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	cvBGit(t, root, "init", "-q")
	file := filepath.Join(root, "file")
	cvBWrite(t, file, []byte("before\n"))
	cvBGit(t, root, "add", ".")
	cvBGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	repository := domain.RepositoryContext{RepositoryID: "repo"}
	cp, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, []domain.RepositoryContext{repository}, 1)
	if err != nil {
		t.Fatal(err)
	}
	cvBWrite(t, file, []byte("after\n"))
	child := domain.Run{ID: "http-child", Status: "failed", Repository: repository, ExecutionContext: domain.TelemetryContext{ProjectID: parent.ExecutionContext.ProjectID, ParentRunID: parent.ID, WorkspaceRunID: parent.ID, NodeCheckpointsEnabled: true}, Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "failed", Attempt: 1, Checkpoints: []domain.NodeCheckpoint{cp}}}}
	if err := a.repo.PutRun(child); err != nil {
		t.Fatal(err)
	}
	call := func(user domain.User) (int, string) {
		c, w := workbenchContext(user, http.MethodPost, "/api/runs/http-child/workspace/rewind", bytes.NewBufferString(`{"nodeId":"a","attempt":1}`))
		c.Params = gin.Params{{Key: "id", Value: child.ID}}
		h.workspaceRewind(c)
		return w.Code, w.Body.String()
	}
	if code, body := call(outsider); code != http.StatusForbidden {
		t.Fatalf("outsider: %d %s", code, body)
	}
	if err := a.repo.PutProject(domain.Project{ID: "child-project", OwnerID: outsider.ID}); err != nil {
		t.Fatal(err)
	}
	child.ExecutionContext.ProjectID = "child-project"
	if err := a.repo.PutRun(child); err != nil {
		t.Fatal(err)
	}
	if code, body := call(outsider); code != http.StatusForbidden {
		t.Fatalf("child project owner without workspace access: %d %s", code, body)
	}
	{
		c, w := workbenchContext(outsider, http.MethodGet, "/api/runs/http-child/workspace/diff?node=a&attempt=1", bytes.NewBuffer(nil))
		h.nodeCheckpointDiff(c, child)
		if w.Code != http.StatusForbidden {
			t.Fatalf("child project owner read parent workspace diff: %d %s", w.Code, w.Body.String())
		}
	}
	child.ExecutionContext.ProjectID = parent.ExecutionContext.ProjectID
	if err := a.repo.PutRun(child); err != nil {
		t.Fatal(err)
	}
	if code, body := call(owner); code != http.StatusOK {
		t.Fatalf("owner: %d %s", code, body)
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != "before\n" {
		t.Fatalf("restored file: %q, %v", content, err)
	}
}
