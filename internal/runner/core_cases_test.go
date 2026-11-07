package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	flowruntime "agentflow/internal/runtime"
)

func TestClientEndpointsAndErrors(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/runners/register":
			if r.Header.Get("X-Bootstrap-Token") != "boot" {
				t.Error("missing bootstrap token")
			}
			_, _ = io.WriteString(w, `{"runnerId":"r1","sessionToken":"s1","heartbeatInterval":2,"leaseTTL":3}`)
		case "/runners/heartbeat":
			_, _ = io.WriteString(w, `{"expiresAt":"2030-01-01T00:00:00Z"}`)
		case "/runners/tasks/claim":
			var body ClaimRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Preflight {
				_, _ = io.WriteString(w, `{"taskAvailable":true,"task":{"id":"t1"}}`)
			} else {
				_, _ = io.WriteString(w, `{"task":{"id":"t2"}}`)
			}
		case "/runners/tasks/lost/start":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"task lease lost"}`)
		case "/unauthorized":
			w.WriteHeader(http.StatusUnauthorized)
		case "/bad":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "bad request")
		case "/invalid-json":
			_, _ = io.WriteString(w, "{")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	c := NewClient(server.URL + "/")
	c.SetSession("session")
	ctx := context.Background()
	reg, err := c.Register(ctx, "boot", RegisterRequest{Name: "cvR"})
	if err != nil || reg.RunnerID != "r1" {
		t.Fatalf("register=%+v err=%v", reg, err)
	}
	hb, err := c.Heartbeat(ctx, HeartbeatRequest{Status: "online"})
	if err != nil || hb.ExpiresAt.IsZero() {
		t.Fatalf("heartbeat=%+v err=%v", hb, err)
	}
	available, task, err := c.WaitForTask(ctx, ClaimRequest{})
	if err != nil || !available || task.ID != "t1" {
		t.Fatalf("wait=%v %+v %v", available, task, err)
	}
	task, ok, err := c.Claim(ctx, ClaimRequest{})
	if err != nil || !ok || task.ID != "t2" {
		t.Fatalf("claim=%+v %v %v", task, ok, err)
	}
	if err := c.Deregister(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.StartTask(ctx, "lost"); !errors.Is(err, ErrTaskLeaseLost) {
		t.Fatalf("lease error=%v", err)
	}
	if err := c.do(ctx, http.MethodGet, "/unauthorized", nil, nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized=%v", err)
	}
	if err := c.do(ctx, http.MethodGet, "/bad", nil, nil); err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("bad=%v", err)
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/invalid-json", nil, &out); err == nil {
		t.Fatal("invalid response accepted")
	}
	for _, call := range []func() error{
		func() error { _, err := c.RenewLease(ctx, "x"); return err },
		func() error { _, err := c.TaskControl(ctx, "x"); return err },
		func() error { return c.SendEvents(ctx, "x", nil) },
		func() error { return c.Complete(ctx, "x", CompleteRequest{}) },
		func() error { return c.CompletePreview(ctx, "p") },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	if len(paths) < 10 {
		t.Fatalf("too few requests: %v", paths)
	}
}

func TestClientPreviewUpload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Preview-Upload-Token") != "upload" || r.Header.Get("Authorization") != "Bearer session" {
			t.Errorf("headers=%v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) == "fail" {
			http.Error(w, "rejected", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	c := NewClient(server.URL)
	c.SetSession("session")
	if err := c.UploadPreview(context.Background(), "p", "upload", strings.NewReader("ok")); err != nil {
		t.Fatal(err)
	}
	if err := c.UploadPreview(context.Background(), "p", "upload", strings.NewReader("fail")); err == nil {
		t.Fatal("failed upload accepted")
	}
}

func TestWorkspaceHelpersAndSearch(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "src"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "empty"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "node_modules"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "src", "main.txt"), []byte("alpha\nNeedle value\n"+strings.Repeat("x", 250)+" NEEDLE\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "binary"), []byte{0, 1}, 0o644)
	_ = os.WriteFile(filepath.Join(root, "large"), make([]byte, runnerWorkspaceFileLimit+1), 0o644)
	if _, _, err := runnerResolveWorkspacePath(root, "/etc/passwd", false); err == nil {
		t.Fatal("absolute path accepted")
	}
	if _, _, err := runnerResolveWorkspacePath(root, ".env", false); err == nil {
		t.Fatal("sensitive path accepted")
	}
	entries, truncated, err := runnerReadWorkspaceTree(root, "")
	if err != nil || truncated || len(entries) != 4 {
		t.Fatalf("tree=%+v truncated=%v err=%v", entries, truncated, err)
	}
	content, size, err := runnerReadWorkspaceFile(root, "src/main.txt")
	if err != nil || size == 0 || !strings.Contains(content, "Needle") {
		t.Fatalf("file=%q size=%d err=%v", content, size, err)
	}
	if _, _, err := runnerReadWorkspaceFile(root, "binary"); err == nil {
		t.Fatal("binary accepted")
	}
	if _, _, err := runnerReadWorkspaceFile(root, "large"); err == nil {
		t.Fatal("large accepted")
	}
	results, truncated, err := runnerSearchWorkspace(context.Background(), root, "needle")
	if err != nil || truncated || len(results) != 2 || !strings.HasSuffix(results[1].Preview, "…") {
		t.Fatalf("search=%+v truncated=%v err=%v", results, truncated, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := runnerSearchWorkspace(ctx, root, "needle"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	for _, sensitive := range []string{".git/config", ".env.local", ".ssh/key", ".agentflow/runtime/x", "id_rsa"} {
		if !runnerWorkspacePathSensitive(sensitive) {
			t.Fatalf("not sensitive: %s", sensitive)
		}
	}
	if runnerWorkspacePathSensitive(".env.example") || runnerWorkspacePathSensitive("src/main.go") {
		t.Fatal("safe path marked sensitive")
	}
	if path, err := runnerWorkspaceLogicalPath("src/main.go"); err != nil || path != "src/main.go" {
		t.Fatalf("logical=%q err=%v", path, err)
	}
	if _, err := runnerWorkspaceLogicalPath("../x"); err == nil {
		t.Fatal("logical traversal accepted")
	}
}

func TestWorkspaceGitViews(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	_ = os.MkdirAll(repoDir, 0o755)
	cvRGit(t, repoDir, "init", "-q")
	cvRGit(t, repoDir, "config", "user.name", "cvR")
	cvRGit(t, repoDir, "config", "user.email", "cvr@example.test")
	_ = os.WriteFile(filepath.Join(repoDir, "a.txt"), []byte("one\n"), 0o644)
	cvRGit(t, repoDir, "add", "a.txt")
	cvRGit(t, repoDir, "commit", "-qm", "first")
	head := strings.TrimSpace(cvRGit(t, repoDir, "rev-parse", "HEAD"))
	_ = os.WriteFile(filepath.Join(repoDir, "a.txt"), []byte("two\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repoDir, "new.txt"), []byte("new\n"), 0o644)
	repositories := []domain.RepositoryContext{{WorkspaceDir: "repo"}}
	if got := runnerCollectWorkspaceGitRepositories(root, repositories); len(got) != 1 || got[0] != "repo" {
		t.Fatalf("repositories=%v", got)
	}
	changes, warning := runnerCollectWorkspaceChanges(context.Background(), root, repositories)
	if warning != "" || len(changes) != 2 {
		t.Fatalf("changes=%+v warning=%q", changes, warning)
	}
	diff, truncated, err := runnerReadWorkspaceDiff(context.Background(), root, repositories, "repo/a.txt")
	if err != nil || truncated || !strings.Contains(diff, "+two") {
		t.Fatalf("diff=%q truncated=%v err=%v", diff, truncated, err)
	}
	base, truncated, err := runnerReadWorkspaceBaseFile(context.Background(), root, repositories, "repo/a.txt")
	if err != nil || truncated || base != "one\n" {
		t.Fatalf("base=%q truncated=%v err=%v", base, truncated, err)
	}
	log, err := runnerCollectWorkspaceLog(context.Background(), root, repositories, "repo")
	if err != nil || len(log["commits"].([]runnerWorkspaceCommit)) != 1 {
		t.Fatalf("log=%+v err=%v", log, err)
	}
	files, err := runnerCollectWorkspaceCommitFiles(context.Background(), root, repositories, "repo", head)
	if err != nil || len(files) != 1 || files[0].Path != "repo/a.txt" {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	blob, size, err := runnerReadWorkspaceGitBlob(context.Background(), root, repositories, "repo/a.txt", head)
	if err != nil || blob != "one\n" || size != 4 {
		t.Fatalf("blob=%q size=%d err=%v", blob, size, err)
	}
	if _, ok := runnerSafeGitObjectID("abcdef"); ok {
		t.Fatal("short object accepted")
	}
	if object, ok := runnerSafeGitObjectID(head); !ok || object != head {
		t.Fatal("valid object rejected")
	}
	if _, _, ok := runnerFindWorkspaceRepository(root, repositories, "missing"); ok {
		t.Fatal("missing repository found")
	}
}

func TestReadRunnerWorkspaceDispatch(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "runs", "run1", "workspace")
	_ = os.MkdirAll(workspace, 0o755)
	_ = os.WriteFile(filepath.Join(workspace, "readme.txt"), []byte("hello searchable\n"), 0o644)
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: base}}
	req := domain.RunnerWorkspaceRequest{RunID: "run1", WorkspaceLayout: domain.RunnerWorkspaceLayoutShared, RunStatus: "failed"}
	for _, operation := range []string{
		domain.RunnerWorkspaceOperationTree,
		domain.RunnerWorkspaceOperationInfo,
		domain.RunnerWorkspaceOperationSearch,
		domain.RunnerWorkspaceOperationFile,
	} {
		req.Operation = operation
		req.Path = ""
		req.Query = "searchable"
		if operation == domain.RunnerWorkspaceOperationFile {
			req.Path = "readme.txt"
		}
		response := d.readRunnerWorkspace(context.Background(), req)
		if response.Status != http.StatusOK || response.Error != "" {
			t.Fatalf("%s: %+v", operation, response)
		}
	}
	req.Operation = domain.RunnerWorkspaceOperationWrite
	req.Path, req.Content = "readme.txt", "new"
	req.ExpectedSHA256 = flowruntime.WorkspaceContentSHA("hello searchable\n")
	if response := d.readRunnerWorkspace(context.Background(), req); response.Status != http.StatusOK {
		t.Fatalf("write=%+v", response)
	}
	req.RunStatus = "running"
	if response := d.readRunnerWorkspace(context.Background(), req); response.Status != http.StatusConflict {
		t.Fatalf("unsafe write=%+v", response)
	}
	req.Operation = "unknown"
	if response := d.readRunnerWorkspace(context.Background(), req); response.Status != http.StatusBadRequest {
		t.Fatalf("unknown=%+v", response)
	}
	req.Operation = domain.RunnerWorkspaceOperationBrowser
	if response := d.readRunnerWorkspace(context.Background(), req); response.Status != http.StatusBadRequest {
		t.Fatalf("browser=%+v", response)
	}
}

func TestDetectionProviderAndDaemonBasics(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, ".local", "bin")
	_ = os.MkdirAll(bin, 0o755)
	tool := filepath.Join(bin, "cvR-tool")
	_ = os.WriteFile(tool, []byte("#!/bin/sh\nprintf 'cvR 1.2\\nignored\\n'\n"), 0o755)
	path, ok := lookupCLI("cvR-tool")
	if !ok || path != tool {
		t.Fatalf("lookup=%q %v paths=%v", path, ok, extraSearchPaths())
	}
	if version := probeVersion(context.Background(), tool); version != "cvR 1.2" {
		t.Fatalf("version=%q", version)
	}
	runtimes := DetectRuntimes(context.Background(), map[string]string{"custom": "cvR-tool"})
	if path, ok := providerBinaryPath(runtimes, "CUSTOM"); !ok || path != tool {
		t.Fatalf("runtimes=%+v", runtimes)
	}
	if _, ok := providerBinaryPath([]domain.RunnerRuntime{{Provider: "x", Status: domain.RuntimeStatusDisabled}}, "x"); ok {
		t.Fatal("disabled provider selected")
	}
	for _, provider := range []string{"claude-code", "codex", "codebuddy"} {
		argv, err := providerArgv(provider, "bin", "prompt", []string{"--x"})
		if err != nil || argv[0] != "bin" || argv[len(argv)-1] != "prompt" {
			t.Fatalf("%s argv=%v err=%v", provider, argv, err)
		}
	}
	if _, err := providerArgv("other", "bin", "prompt", nil); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if trimTrailingSlash("http://x///") != "http://x" {
		t.Fatal("trim")
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty config accepted")
	}
	if _, err := New(Config{ServerURL: "http://x"}); err == nil {
		t.Fatal("missing token accepted")
	}
	d, err := New(Config{ServerURL: "http://x///", BootstrapToken: "b", StatePath: filepath.Join(home, "state.json"), MaxConcurrency: 99})
	if err != nil || d.cfg.MaxConcurrency != 32 || Version() == "" {
		t.Fatalf("daemon=%+v err=%v", d, err)
	}
	st := daemonState{RunnerID: "r", SessionToken: "s", ExpiresAt: time.Now()}
	if err := d.saveState(st); err != nil {
		t.Fatal(err)
	}
	d.state = daemonState{}
	loaded, err := d.loadState()
	if err != nil || loaded.RunnerID != "r" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestEventBatcherAndExecHelpers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	b := newEventBatcher(domain.RunnerTask{ID: "task"}, NewClient(server.URL))
	b.addLine("stdout", "line")
	b.addLine("stderr", "error")
	b.addObservation(domain.TelemetryEvent{Name: "custom", Message: "obs"})
	if b.result() != "line" || b.taskResult("fallback") != "fallback" {
		t.Fatalf("result=%q task=%q", b.result(), b.taskResult("fallback"))
	}
	b.flush(context.Background())
	if len(b.buf) != 0 {
		t.Fatal("buffer not flushed")
	}
	if tailString("abcdef", 3) != "def" || tailString("ab", 3) != "ab" {
		t.Fatal("tail")
	}
	env := upsertProcessEnv([]string{"A=1", "B=2", "A=3"}, "A", "x")
	if strings.Join(env, ",") != "A=x,B=2" {
		t.Fatalf("env=%v", env)
	}
	if !isBenignStreamClose(nil) || !isBenignStreamClose(io.ErrClosedPipe) || isBenignStreamClose(errors.New("other")) {
		t.Fatal("benign close")
	}
	if exitCode(nil) != 0 || exitCode(errors.New("x")) != -1 {
		t.Fatal("exit code")
	}
}

func cvRGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
