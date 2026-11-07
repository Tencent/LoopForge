package httpapi

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	afruntime "agentflow/internal/runtime"
)

func TestWorkspaceBrowseErrorAndSuccessBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	run := domain.Run{ID: "cvb-ws", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	root := f.e.RunWorkspace(run.ID)
	if err := os.MkdirAll(filepath.Join(root, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.txt"), []byte("hello workspace\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"config", "user.email", "cvb@example.test"}, {"config", "user.name", "cvB"}, {"add", "src/main.txt"}, {"commit", "-m", "initial"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	headRaw, _ := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	head := strings.TrimSpace(string(headRaw))
	if err := os.WriteFile(filepath.Join(root, "src", "main.txt"), []byte("hello changed workspace\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want int
	}{
		{"/api/runs/cvb-ws/workspace", 200},
		{"/api/runs/cvb-ws/workspace/tree", 200},
		{"/api/runs/cvb-ws/workspace/tree?path=missing", 404},
		{"/api/runs/cvb-ws/workspace/tree?path=src/main.txt", 400},
		{"/api/runs/cvb-ws/workspace/file", 400},
		{"/api/runs/cvb-ws/workspace/file?path=missing", 404},
		{"/api/runs/cvb-ws/workspace/file?path=.env", 400},
		{"/api/runs/cvb-ws/workspace/file?path=src/main.txt", 200},
		{"/api/runs/cvb-ws/workspace/file?path=src/main.txt&ref=bad", 400},
		{"/api/runs/cvb-ws/workspace/file?path=src/main.txt&ref=" + head, 200},
		{"/api/runs/cvb-ws/workspace/log", 200},
		{"/api/runs/cvb-ws/workspace/log?repository=missing", 400},
		{"/api/runs/cvb-ws/workspace/commit?id=bad", 400},
		{"/api/runs/cvb-ws/workspace/commit?id=" + head, 200},
		{"/api/runs/cvb-ws/workspace/diff", 200},
		{"/api/runs/cvb-ws/workspace/diff?path=src/main.txt", 200},
		{"/api/runs/cvb-ws/workspace/search?q=x", 400},
		{"/api/runs/cvb-ws/workspace/search?q=workspace", 200},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, http.MethodGet, tc.path, nil, true)
		if rec.Code != tc.want {
			t.Errorf("%d %s = %d want %d: %s", i, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	cleaned := run
	cleaned.ID = "cvb-ws-cleaned"
	cleaned.WorkspaceCleanedAt = run.CreatedAt.Add(time.Hour)
	if cleaned.WorkspaceCleanedAt.IsZero() {
		cleaned.WorkspaceCleanedAt = time.Now()
	}
	if err := f.s.PutRun(cleaned); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/runs/cvb-ws-cleaned/workspace", nil, true); rec.Code != 200 {
		t.Errorf("cleaned info = %d", rec.Code)
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/runs/cvb-ws-cleaned/workspace/tree", nil, true); rec.Code != 410 {
		t.Errorf("cleaned tree = %d", rec.Code)
	}
}

func TestWorkspaceEditLifecycleBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	run := domain.Run{ID: "cvb-edit", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	root := f.e.RunWorkspace(run.ID)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPost, "/api/runs/missing/workspace/takeover", nil, 404},
		{http.MethodPost, "/api/runs/cvb-edit/workspace/takeover", nil, 200},
		{http.MethodPut, "/api/runs/cvb-edit/workspace/file", map[string]any{"path": "../bad", "content": "x"}, 409},
		{http.MethodPut, "/api/runs/cvb-edit/workspace/file", map[string]any{"path": "note.txt", "content": "hello", "expectedSha256": afruntime.WorkspaceContentSHA("initial")}, 200},
		{http.MethodPut, "/api/runs/cvb-edit/workspace/file", map[string]any{"path": "note.txt", "content": "changed", "expectedSha256": "wrong"}, 409},
		{http.MethodPost, "/api/runs/cvb-edit/workspace/terminal", map[string]any{"operation": "bad"}, 409},
		{http.MethodPost, "/api/runs/cvb-edit/workspace/release", nil, 200},
		{http.MethodPost, "/api/runs/cvb-edit/workspace/release", nil, 200},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%d %s = %d want %d: %s", i, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	child := run
	child.ID = "cvb-child"
	child.ExecutionContext.WorkspaceRunID = run.ID
	if err := f.s.PutRun(child); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodPost, "/api/runs/cvb-child/workspace/takeover", nil, true); rec.Code != 409 {
		t.Errorf("child takeover = %d: %s", rec.Code, rec.Body.String())
	}
}
