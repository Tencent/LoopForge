package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func cvBGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func cvBWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMoreFilesystemGitPaths(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one", "two"} {
		dir := filepath.Join(root, name)
		cvBWrite(t, filepath.Join(dir, "tracked.txt"), []byte("first\n"))
		cvBWrite(t, filepath.Join(dir, ".env.example"), []byte("example\n"))
		cvBWrite(t, filepath.Join(dir, ".env"), []byte("secret\n"))
		cvBWrite(t, filepath.Join(dir, "binary.dat"), []byte{'a', 0, 'b'})
		cvBWrite(t, filepath.Join(dir, "large.dat"), bytes.Repeat([]byte("z"), workspaceFileLimit+1))
		cvBWrite(t, filepath.Join(dir, "diffbig.txt"), bytes.Repeat([]byte("old line\n"), 150000))
		cvBGit(t, dir, "init")
		cvBGit(t, dir, "config", "user.email", "cvb@example.test")
		cvBGit(t, dir, "config", "user.name", "cvB")
		cvBGit(t, dir, "add", ".")
		cvBGit(t, dir, "commit", "-m", "initial "+name)
	}
	run := domain.Run{Repositories: []domain.RepositoryContext{
		{RepositoryID: "one", WorkspaceDir: "one"},
		{RepositoryID: "bad", WorkspaceDir: "../outside"},
		{RepositoryID: "two", WorkspaceDir: "two"},
	}}
	roots := workspaceRepositoryRoots(root, run)
	if len(roots) != 2 || roots[0].label != "one" || roots[1].label != "two" {
		t.Fatalf("repository roots: %#v", roots)
	}
	if got := collectWorkspaceGitRepositories(root, run); len(got) != 2 {
		t.Fatalf("git repositories: %#v", got)
	}
	if _, _, ok := findWorkspaceRepository(root, run, "workspace"); ok {
		t.Fatal("ambiguous workspace repository resolved")
	}
	if path, label, ok := findWorkspaceRepository(root, run, "one"); !ok || path != filepath.Join(root, "one") || label != "one" {
		t.Fatalf("find one: %q %q %v", path, label, ok)
	}

	cvBWrite(t, filepath.Join(root, "one", "tracked.txt"), []byte("changed\n"))
	cvBGit(t, filepath.Join(root, "one"), "mv", "tracked.txt", "renamed.txt")
	cvBWrite(t, filepath.Join(root, "one", "diffbig.txt"), bytes.Repeat([]byte("new line\n"), 150000))
	cvBWrite(t, filepath.Join(root, "two", ".env.local"), []byte("hidden\n"))
	cvBWrite(t, filepath.Join(root, "two", "new.txt"), []byte("new\n"))
	changes, warning := collectWorkspaceChanges(context.Background(), root, run)
	if warning != "" || len(changes) < 2 {
		t.Fatalf("changes: %#v warning=%q", changes, warning)
	}
	for _, change := range changes {
		if strings.Contains(change.Path, ".env.local") {
			t.Fatalf("sensitive change leaked: %#v", change)
		}
	}
	diff, clipped, err := readWorkspaceDiff(context.Background(), root, run, "one/renamed.txt")
	if err != nil || clipped || diff == "" {
		t.Fatalf("renamed diff: %q %v %v", diff, clipped, err)
	}
	cvBWrite(t, filepath.Join(root, "unrelated.txt"), []byte("outside repositories\n"))
	_, _, _ = readWorkspaceDiff(context.Background(), root, run, "unrelated.txt")
	if diff, clipped, err = readWorkspaceDiff(context.Background(), root, run, ""); err != nil || !clipped || len(diff) != workspaceFileLimit {
		t.Fatalf("large diff: %d %v %v", len(diff), clipped, err)
	}
	base, clipped, err := readWorkspaceBaseFile(context.Background(), root, run, "two/new.txt")
	if err != nil || clipped || base != "" {
		t.Fatalf("untracked base: %q %v %v", base, clipped, err)
	}
	if _, _, err = readWorkspaceBaseFile(context.Background(), root, run, "missing.txt"); !os.IsNotExist(err) {
		t.Fatalf("missing base error = %v", err)
	}

	head := cvBGit(t, filepath.Join(root, "one"), "rev-parse", "HEAD")
	if files, err := collectWorkspaceCommitFiles(context.Background(), root, run, "one", head); err != nil || len(files) != 5 {
		t.Fatalf("commit files: %#v %v", files, err)
	}
	if files, err := collectWorkspaceCommitFiles(context.Background(), root, run, "missing", head); err == nil || files != nil {
		t.Fatalf("missing repository commit: %#v %v", files, err)
	}
	if files, err := collectWorkspaceCommitFiles(context.Background(), root, run, "two", "abcdef0"); err == nil || files != nil {
		t.Fatalf("missing commit object: %#v %v", files, err)
	}
	if blob, size, err := readWorkspaceGitBlob(context.Background(), root, run, "two/new.txt", head); err != nil || blob != "" || size != 0 {
		t.Fatalf("cross-repository blob: %q %d %v", blob, size, err)
	}
	if blob, size, err := readWorkspaceGitBlob(context.Background(), root, run, "one", head); err != nil || blob != "" || size != 0 {
		t.Fatalf("directory blob: %q %d %v", blob, size, err)
	}
	if _, _, err := readWorkspaceGitBlob(context.Background(), root, run, ".env", head); err == nil {
		t.Fatal("sensitive blob path accepted")
	}
	if _, _, err := readWorkspaceGitBlob(context.Background(), root, run, "one/binary.dat", head); err == nil {
		t.Fatal("binary git blob accepted")
	}
	if blob, size, err := readWorkspaceGitBlob(context.Background(), root, run, "one/large.dat", head); err != nil || len(blob) != workspaceFileLimit || size != workspaceFileLimit {
		t.Fatalf("large git blob: %d %d %v", len(blob), size, err)
	}
	if _, _, err := readWorkspaceBaseFile(context.Background(), root, run, "one/binary.dat"); err == nil {
		t.Fatal("binary base file accepted")
	}
	if base, clipped, err := readWorkspaceBaseFile(context.Background(), root, run, "one/large.dat"); err != nil || !clipped || len(base) != workspaceFileLimit {
		t.Fatalf("large base file: %d %v %v", len(base), clipped, err)
	}

	plainRoot := t.TempDir()
	plainRun := domain.Run{}
	if log, err := collectWorkspaceLog(context.Background(), plainRoot, plainRun, ""); err != nil || log["head"] != "" {
		t.Fatalf("plain log: %#v %v", log, err)
	}
	if files, err := collectWorkspaceCommitFiles(context.Background(), plainRoot, plainRun, "", head); err != nil || len(files) != 0 {
		t.Fatalf("plain commit files: %#v %v", files, err)
	}
	if _, err := collectWorkspaceLog(context.Background(), root, run, "missing"); err == nil {
		t.Fatal("missing repository log accepted")
	}
	brokenRoot := t.TempDir()
	cvBWrite(t, filepath.Join(brokenRoot, ".git"), []byte("not a repository"))
	if changes, warning := collectWorkspaceChanges(context.Background(), brokenRoot, domain.Run{}); warning == "" || len(changes) != 0 {
		t.Fatalf("broken repository changes: %#v %q", changes, warning)
	}

	large := bytes.Repeat([]byte("x"), workspaceFileLimit+1)
	cvBWrite(t, filepath.Join(root, "large.txt"), large)
	if _, size, err := readWorkspaceFile(root, "large.txt"); err == nil || size != int64(len(large)) {
		t.Fatalf("large preview: size=%d err=%v", size, err)
	}
	for i := 0; i < 101; i++ {
		cvBWrite(t, filepath.Join(root, "search", string(rune('a'+i%26)), strings.Repeat("x", i/26)+".txt"), []byte("needle\n"))
	}
	results, truncated, err := searchWorkspace(context.Background(), root, "needle")
	if err != nil || !truncated || len(results) != 100 {
		t.Fatalf("search limit: %d %v %v", len(results), truncated, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := searchWorkspace(cancelled, root, "needle"); err == nil {
		t.Fatal("cancelled search succeeded")
	}
}

func TestWorkspaceMoreRemoteProxyMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := cvBNewHTTPFixture(t)
	run := domain.Run{
		ID: "cvb-remote-more", Status: "running", PauseRequested: true, Nodes: map[string]*domain.NodeRun{},
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "cvb-runner"},
	}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRunner(domain.Runner{ID: "cvb-runner", Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	hub := newRunnerControlHub()
	conn := &runnerControlConn{runnerID: "cvb-runner", send: make(chan runnerControlMessage, 20), done: make(chan struct{})}
	hub.conns[conn.runnerID] = conn
	h := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}, runnerControls: hub}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for message := range conn.send {
			request := message.WorkspaceRequest
			switch request.Query {
			case "error404":
				hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{Status: http.StatusNotFound, Error: "missing"})
				continue
			case "badstatus":
				hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{Status: http.StatusOK, Error: "bad"})
				continue
			case "empty":
				hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{})
				continue
			case "invalidjson":
				hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{Payload: []byte("{")})
				continue
			}
			payload, _ := json.Marshal(gin.H{"available": true, "operation": request.Operation})
			hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{Payload: payload})
		}
	}()
	t.Cleanup(func() {
		close(conn.send)
		<-done
	})

	paths := []string{
		"/api/runs/:id/workspace",
		"/api/runs/:id/workspace/tree",
		"/api/runs/:id/workspace/file",
		"/api/runs/:id/workspace/log",
		"/api/runs/:id/workspace/commit",
		"/api/runs/:id/workspace/diff",
		"/api/runs/:id/workspace/search",
	}
	for _, fullPath := range paths {
		rec := httptest.NewRecorder()
		router := gin.New()
		router.GET(fullPath, h.runWorkspace)
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, strings.Replace(fullPath, ":id", run.ID, 1)+"?path=a&repository=one&id=abcdef0&q=needle", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d: %s", fullPath, rec.Code, rec.Body.String())
		}
	}
	for _, tc := range []struct {
		path  string
		query string
		want  int
	}{
		{paths[0], "error404", http.StatusOK},
		{paths[6], "badstatus", http.StatusBadRequest},
		{paths[6], "empty", http.StatusBadGateway},
		{paths[0], "invalidjson", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		testRouter := gin.New()
		testRouter.GET(tc.path, h.runWorkspace)
		requestPath := strings.Replace(tc.path, ":id", run.ID, 1)
		testRouter.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, requestPath+"?q="+tc.query, nil))
		if rec.Code != tc.want {
			t.Errorf("proxy %s %s = %d want %d: %s", tc.path, tc.query, rec.Code, tc.want, rec.Body.String())
		}
	}

	unavailable := &runsHandlers{deps: h.deps, runnerControls: newRunnerControlHub()}
	for _, fullPath := range []string{paths[0], paths[2]} {
		rec := httptest.NewRecorder()
		router := gin.New()
		router.GET(fullPath, unavailable.runWorkspace)
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, strings.Replace(fullPath, ":id", run.ID, 1), nil))
		want := http.StatusConflict
		if fullPath == paths[0] {
			want = http.StatusOK
		}
		if rec.Code != want {
			t.Errorf("unavailable %s = %d want %d", fullPath, rec.Code, want)
		}
	}

	rec := httptest.NewRecorder()
	router := gin.New()
	router.GET("/unexpected", func(c *gin.Context) { h.proxyRunnerWorkspace(c, run, "cvb-runner") })
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unexpected", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unexpected operation = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	router = gin.New()
	router.GET(paths[0], func(c *gin.Context) { (&runsHandlers{deps: h.deps}).proxyRunnerWorkspace(c, run, "cvb-runner") })
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, strings.Replace(paths[0], ":id", run.ID, 1), nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil remote channel = %d", rec.Code)
	}

	local := domain.Run{ID: "cvb-local-missing", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(local); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want int
	}{{"/api/runs/cvb-local-missing/workspace", http.StatusOK}, {"/api/runs/cvb-local-missing/workspace/tree", http.StatusConflict}} {
		rec := cvBRequest(t, f, http.MethodGet, tc.path, nil, true)
		if rec.Code != tc.want {
			t.Errorf("missing local workspace %s = %d want %d", tc.path, rec.Code, tc.want)
		}
	}
}

func TestWorkspaceMoreEditAuthorizationAndErrors(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	run := domain.Run{ID: "cvb-edit-more", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "cvb-view-only"}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutProject(domain.Project{ID: "cvb-view-only", Name: "view", OwnerID: 0, Members: []domain.ProjectMember{{UserID: f.user.ID, Role: "viewer"}, {UserID: 999999, Role: "maintainer"}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	root := f.e.RunWorkspace(run.ID)
	cvBWrite(t, filepath.Join(root, "note.txt"), []byte("old"))
	if rec := cvBRequest(t, f, http.MethodGet, "/api/runs/"+run.ID+"/workspace/diff?path=missing.txt", nil, true); rec.Code != http.StatusNotFound {
		t.Fatalf("missing diff = %d: %s", rec.Code, rec.Body.String())
	}
	direct := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}}
	custom := gin.New()
	custom.GET("/custom/:id", direct.runWorkspace)
	customRec := httptest.NewRecorder()
	custom.ServeHTTP(customRec, httptest.NewRequest(http.MethodGet, "/custom/"+run.ID, nil))
	if customRec.Code != http.StatusNotFound {
		t.Fatalf("unknown workspace operation = %d", customRec.Code)
	}

	if rec := cvBRequest(t, f, http.MethodPost, "/api/runs/"+run.ID+"/workspace/takeover", nil, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous takeover = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodPost, "/api/runs/"+run.ID+"/workspace/takeover", nil, true); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer takeover = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodPut, "/api/runs/"+run.ID+"/workspace/file", nil, true); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer write = %d: %s", rec.Code, rec.Body.String())
	}

	running := domain.Run{ID: "cvb-edit-running", Status: "running", ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(running); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodPost, "/api/runs/"+running.ID+"/workspace/takeover", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("running takeover = %d: %s", rec.Code, rec.Body.String())
	}

	owned := run
	owned.ID = "cvb-edit-owned"
	owned.ExecutionContext.ProjectID = "platform"
	owned.Nodes = map[string]*domain.NodeRun{"child": {ChildRunID: "nested"}}
	if err := f.s.PutRun(owned); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodPost, "/api/runs/"+owned.ID+"/workspace/takeover", nil, true); rec.Code != http.StatusConflict {
		t.Fatalf("parent with child takeover = %d: %s", rec.Code, rec.Body.String())
	}
	cleanOwned := owned
	cleanOwned.ID = "cvb-edit-clean"
	cleanOwned.Nodes = map[string]*domain.NodeRun{}
	cleanOwned.WorkspaceRoot = t.TempDir()
	cleanOwned.WorkspaceEditorID = f.user.ID
	cleanOwned.WorkspaceEditUntil = time.Now().UTC().Add(time.Hour)
	if err := f.s.PutRun(cleanOwned); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodPut, "/api/runs/"+cleanOwned.ID+"/workspace/file", "not-an-object", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid write body = %d: %s", rec.Code, rec.Body.String())
	}

	h := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}}
	for _, status := range []string{"running", "waiting", "succeeded", "failed", "cancelled"} {
		probe := cleanOwned
		probe.Status = status
		probe.PauseRequested = status == "waiting"
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = authedRequest(t, httptest.NewRequest(http.MethodGet, "/", nil), f.user)
		got := h.workspaceWritable(c, probe)
		want := status != "running"
		if got != want {
			t.Errorf("workspaceWritable(%s) = %v want %v", status, got, want)
		}
	}

	remote := owned
	remote.ID = "cvb-close-remote"
	remote.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
	remote.ExecutionContext.ExecutionRunnerID = "cvb-runner"
	if err := f.s.PutRunner(domain.Runner{ID: "cvb-runner", Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRun(remote); err != nil {
		t.Fatal(err)
	}
	if err := h.closeWorkspaceTerminals(context.Background(), remote, f.user.ID); err == nil {
		t.Fatal("remote close without channel succeeded")
	}
}

func TestWorkspaceDeletedFileCancelledSearchAndTimeout(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	run := domain.Run{ID: "cvb-r2-workspace-edge", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	root := f.e.RunWorkspace(run.ID)
	cvBWrite(t, filepath.Join(root, "deleted.txt"), []byte("tracked\n"))
	cvBGit(t, root, "init")
	cvBGit(t, root, "config", "user.email", "cvb@example.test")
	cvBGit(t, root, "config", "user.name", "cvB")
	cvBGit(t, root, "add", "deleted.txt")
	cvBGit(t, root, "commit", "-m", "tracked")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodGet,
		"/api/runs/"+run.ID+"/workspace/diff?path=deleted.txt", nil, true); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted diff = %d: %s", rec.Code, rec.Body.String())
	}
	cvBWrite(t, filepath.Join(root, "live.txt"), []byte("tracked\n"))

	h := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}}
	router := gin.New()
	router.GET("/api/runs/:id/workspace/search", h.runWorkspace)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID+"/workspace/search?q=tracked", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cancelled search = %d: %s", rec.Code, rec.Body.String())
	}

	remote := domain.Run{ID: "cvb-r2-workspace-timeout", Status: "running", Nodes: map[string]*domain.NodeRun{},
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "cvb-timeout"}}
	if err := f.s.PutRun(remote); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRunner(domain.Runner{ID: "cvb-timeout", Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	hub := newRunnerControlHub()
	hub.conns["cvb-timeout"] = &runnerControlConn{runnerID: "cvb-timeout", send: make(chan runnerControlMessage), done: make(chan struct{})}
	h.runnerControls = hub
	router = gin.New()
	router.GET("/api/runs/:id/workspace", h.runWorkspace)
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	req = httptest.NewRequest(http.MethodGet, "/api/runs/"+remote.ID+"/workspace", nil).WithContext(expired)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "超时") {
		t.Fatalf("remote timeout = %d: %s", rec.Code, rec.Body.String())
	}

	searchRoot := t.TempDir()
	longLine := "needle " + strings.Repeat("x", 300)
	cvBWrite(t, filepath.Join(searchRoot, "000.txt"), []byte(longLine))
	for i := 1; i < 83; i++ {
		cvBWrite(t, filepath.Join(searchRoot, fmt.Sprintf("%03d.txt", i)), bytes.Repeat([]byte("z"), 256*1024))
	}
	results, truncated, err := searchWorkspace(context.Background(), searchRoot, "needle")
	if err != nil || !truncated || len(results) != 1 || len([]rune(results[0].Preview)) > 241 {
		t.Fatalf("large search = %#v truncated=%v err=%v", results, truncated, err)
	}
}
