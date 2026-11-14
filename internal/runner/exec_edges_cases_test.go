package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	flowruntime "agentflow/internal/runtime"
)

type cvRRunnerSandbox struct {
	workspace string
	output    string
	err       error
}

func (s *cvRRunnerSandbox) ID() string                  { return "cvR" }
func (s *cvRRunnerSandbox) Workspace() string           { return s.workspace }
func (s *cvRRunnerSandbox) Close(context.Context) error { return nil }
func (s *cvRRunnerSandbox) Exec(_ context.Context, _ flowruntime.CommandSpec, sink flowruntime.EventSink) (string, error) {
	sink(domain.TelemetryEvent{Name: "process.stdout", Message: "sandbox line"})
	sink(domain.TelemetryEvent{Name: "custom", Message: "observation"})
	return s.output, s.err
}

type cvRBlockingRunnerSandbox struct {
	workspace string
	started   chan struct{}
}

func (s *cvRBlockingRunnerSandbox) ID() string                  { return "blocking" }
func (s *cvRBlockingRunnerSandbox) Workspace() string           { return s.workspace }
func (s *cvRBlockingRunnerSandbox) Close(context.Context) error { return nil }
func (s *cvRBlockingRunnerSandbox) Exec(ctx context.Context, _ flowruntime.CommandSpec, _ flowruntime.EventSink) (string, error) {
	close(s.started)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestExecuteProcessAndSandboxOutcomes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	d := &Daemon{
		cfg:             Config{DefaultWorkspaceRoot: t.TempDir()},
		client:          NewClient(server.URL),
		controls:        map[string]chan string{},
		pendingControls: map[string]string{},
	}
	success := d.executeProcess(context.Background(), domain.RunnerTask{ID: "success"}, []string{"sh", "-c", "printf 'hello\\n'"})
	if success.status != domain.RunnerTaskSuccess || success.result != "hello" {
		t.Fatalf("success=%+v", success)
	}
	failed := d.executeProcess(context.Background(), domain.RunnerTask{ID: "failed"}, []string{"sh", "-c", "echo bad >&2; exit 7"})
	if failed.status != domain.RunnerTaskFailed || failed.exitCode != 7 {
		t.Fatalf("failed=%+v", failed)
	}
	spawn := d.executeProcess(context.Background(), domain.RunnerTask{ID: "spawn"}, []string{filepath.Join(t.TempDir(), "missing")})
	if spawn.status != domain.RunnerTaskFailed {
		t.Fatalf("spawn=%+v", spawn)
	}
	sb := &cvRRunnerSandbox{workspace: t.TempDir(), output: "sandbox result"}
	result := d.executeSandboxProcess(context.Background(), domain.RunnerTask{ID: "sandbox"}, sb, flowruntime.CommandSpec{Argv: []string{"x"}})
	if result.status != domain.RunnerTaskSuccess || result.result != "sandbox result" {
		t.Fatalf("sandbox=%+v", result)
	}
	sb.err = errors.New("sandbox failed")
	result = d.executeSandboxProcess(context.Background(), domain.RunnerTask{ID: "sandbox-fail"}, sb, flowruntime.CommandSpec{Argv: []string{"x"}})
	if result.status != domain.RunnerTaskFailed {
		t.Fatalf("sandbox fail=%+v", result)
	}
}

func TestExecuteProcessAndSandboxCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	d := &Daemon{
		cfg:             Config{DefaultWorkspaceRoot: t.TempDir()},
		client:          NewClient(server.URL),
		controls:        map[string]chan string{},
		pendingControls: map[string]string{},
	}
	// Cancel only after the child is running: an earlier cancel races
	// StartTask and yields failed instead of canceled.
	marker := filepath.Join(t.TempDir(), "spawned")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	result := d.executeProcess(ctx, domain.RunnerTask{ID: "cancel-process"}, []string{"sh", "-c", `touch "$1"; exec sleep 30`, "sh", marker})
	if result.status != domain.RunnerTaskCanceled {
		t.Fatalf("process=%+v", result)
	}
	sandbox := &cvRBlockingRunnerSandbox{workspace: t.TempDir(), started: make(chan struct{})}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-sandbox.started
		cancel()
	}()
	result = d.executeSandboxProcess(ctx, domain.RunnerTask{ID: "cancel-sandbox"}, sandbox, flowruntime.CommandSpec{Argv: []string{"x"}})
	if result.status != domain.RunnerTaskCanceled {
		t.Fatalf("sandbox=%+v", result)
	}
}

func TestEventBatcherRequeuesFailedFlush(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fail", http.StatusInternalServerError) }))
	defer server.Close()
	b := newEventBatcher(domain.RunnerTask{ID: "task"}, NewClient(server.URL))
	b.addLine("stdout", "one")
	b.addLine("stdout", "two")
	b.flush(context.Background())
	if len(b.buf) != 2 || b.size != 6 {
		t.Fatalf("buf=%+v size=%d", b.buf, b.size)
	}
}

func TestReplaceCheckoutContentsAndRunSandboxTouch(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	replacement := filepath.Join(parent, "replacement")
	_ = os.MkdirAll(workspace, 0o755)
	_ = os.MkdirAll(replacement, 0o755)
	_ = os.WriteFile(filepath.Join(workspace, "old"), []byte("old"), 0o600)
	_ = os.WriteFile(filepath.Join(replacement, "new"), []byte("new"), 0o600)
	if err := replaceCheckoutContents(workspace, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "new")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "old")); !os.IsNotExist(err) {
		t.Fatalf("old err=%v", err)
	}
	old := time.Now().Add(-time.Hour)
	d := &Daemon{runSandboxes: map[string]*runSandbox{
		"a": {runID: "run", lastUsed: old},
		"b": {runID: "other", lastUsed: old},
	}}
	d.touchRunSandboxes("run")
	if !d.runSandboxes["a"].lastUsed.After(old) || !d.runSandboxes["b"].lastUsed.Equal(old) {
		t.Fatal("sandbox touch")
	}
}

func TestEnsureRunnerBrowserDisabled(t *testing.T) {
	t.Setenv("AGENTFLOW_BROWSER", "off")
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}}
	reason := d.ensureRunnerBrowser(context.Background(), domain.RunnerWorkspaceRequest{RunID: "run"})
	if reason != "平台已关闭 Agent 浏览器" {
		t.Fatalf("reason=%q", reason)
	}
	t.Setenv("AGENTFLOW_BROWSER", "on")
	if reason := d.ensureRunnerBrowser(context.Background(), domain.RunnerWorkspaceRequest{RunID: "../bad"}); !strings.Contains(reason, "无法定位") {
		t.Fatalf("invalid reason=%q", reason)
	}
	if reason := d.ensureRunnerBrowser(context.Background(), domain.RunnerWorkspaceRequest{RunID: "missing"}); !strings.Contains(reason, "不在这台执行机") {
		t.Fatalf("missing reason=%q", reason)
	}
}

func TestRunSandboxKeysAndRequirements(t *testing.T) {
	dockerfile := domain.DockerfileDefinition{ID: "d", Content: "FROM scratch"}
	environment := domain.EnvironmentDefinition{ID: "e"}
	task := domain.RunnerTask{
		ID: "task", RunID: "run/id", Provider: "codex", Workspace: "runs/run-id/workspace",
		Sandbox: &domain.RunnerSandboxSpec{
			Profile:    domain.SandboxProfile{Driver: "docker"},
			Dockerfile: &dockerfile, Environment: &environment,
			Requirements: &domain.RunnerSandboxRequirements{PrivateHome: true, BunFS: true, CLIStates: []domain.RunnerCLIStateRequirement{{Directory: ".x", Files: []string{"a"}}}},
		},
	}
	requirements, resources := taskSandboxSpec(task)
	if !requirements.PrivateHome || !requirements.BunFS || len(requirements.CLIStates) != 1 || resources.Dockerfile.ID != "d" || resources.Environment.ID != "e" {
		t.Fatalf("requirements=%+v resources=%+v", requirements, resources)
	}
	key, id, shared := runSandboxKey(task, requirements, resources)
	if !shared || key == "" || !strings.HasPrefix(id, "rt-run-run-id-") {
		t.Fatalf("key=%q id=%q shared=%v", key, id, shared)
	}
	task.Workspace = "other"
	if _, _, shared := runSandboxKey(task, requirements, resources); shared {
		t.Fatal("non-shared workspace shared")
	}
	task.Sandbox.Requirements = nil
	task.Workspace = "runs/run/workspace"
	task.Invocation = &domain.RunnerInvocation{CredentialSource: domain.RuntimeAuthLocal}
	for _, provider := range []string{"codex", "claude-code", "codebuddy", "mock"} {
		task.Provider = provider
		req, _ := taskSandboxSpec(task)
		if provider != "mock" && len(req.CLIStates) != 1 {
			t.Fatalf("%s req=%+v", provider, req)
		}
	}
	if !sharedRunWorkspace("runs/r/workspace") || sharedRunWorkspace("runs/../workspace") || containerSafe("a/b") != "a-b" {
		t.Fatal("workspace/container helpers")
	}
}

func TestPrepareInvocationWorkspaceAndResolveCommandEdges(t *testing.T) {
	workspace := t.TempDir()
	if err := prepareInvocationWorkspace(workspace, nil); err != nil {
		t.Fatal(err)
	}
	inv := &domain.RunnerInvocation{GateResultPath: "../bad"}
	if err := prepareInvocationWorkspace(workspace, inv); err == nil {
		t.Fatal("unsafe gate accepted")
	}
	inv = &domain.RunnerInvocation{Inputs: []domain.RunnerInputFile{{Path: "bad", Content: "x"}}}
	if err := prepareInvocationWorkspace(workspace, inv); err == nil {
		t.Fatal("unsafe input accepted")
	}
	target := filepath.Join(workspace, ".agentflow", "inputs", "x")
	_ = os.MkdirAll(filepath.Dir(target), 0o755)
	_ = os.MkdirAll(target, 0o755)
	if err := writeInputFile(workspace, domain.RunnerInputFile{Path: ".agentflow/inputs/x"}); err == nil {
		t.Fatal("directory input accepted")
	}
	if got, err := resolveCommandPath(nil); err != nil || got != nil {
		t.Fatalf("nil argv=%v err=%v", got, err)
	}
	if goruntime.GOOS == "windows" {
		return // Windows command resolution has a native executable fixture.
	}
	absolute := []string{"/bin/sh", "-c", "true"}
	if got, err := resolveCommandPath(absolute); err != nil || got[0] != "/bin/sh" {
		t.Fatalf("absolute=%v err=%v", got, err)
	}
	got, err := resolveCommandPath([]string{"sh", "-c", "true"})
	if err != nil || !filepath.IsAbs(got[0]) {
		t.Fatalf("unresolved=%v err=%v", got, err)
	}
	missing, err := resolveCommandPath([]string{"cvR-no-such-command"})
	if err != nil || missing[0] != "cvR-no-such-command" {
		t.Fatalf("missing=%v err=%v", missing, err)
	}
}

func TestManagedRuntimeDetectionAndPendingControls(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "state.json"), []byte(`{"current":{},"previous":{},"candidates":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:             Config{ManagedCLIRoot: root},
		controls:        map[string]chan string{},
		pendingControls: map[string]string{},
	}
	runtimes, err := d.detectManagedRuntimes(context.Background())
	if err != nil || len(runtimes) < 2 {
		t.Fatalf("runtimes=%+v err=%v", runtimes, err)
	}
	d.signalTaskControl("pending", domain.RunnerTaskDesiredCancel)
	ch := d.registerTaskControl("pending")
	select {
	case got := <-ch:
		if got != domain.RunnerTaskDesiredCancel {
			t.Fatalf("control=%q", got)
		}
	default:
		t.Fatal("pending control not delivered")
	}
	ch <- "full"
	d.signalTaskControl("pending", "ignored")
	if len(d.activeTaskIDs()) != 1 {
		t.Fatal("active task missing")
	}
	d.unregisterTaskControl("pending")
}

func TestClaimLoopImmediateBranches(t *testing.T) {
	d := &Daemon{}
	d.updateReady.Store(true)
	if err := d.claimLoop(context.Background()); !errors.Is(err, ErrUpdateReady) {
		t.Fatalf("update err=%v", err)
	}
	d.updateReady.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.claimLoop(ctx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := d.claimLoop(ctx); err != nil {
		t.Fatal(err)
	}
	d.runtimes = []domain.RunnerRuntime{{Provider: "mock", Status: domain.RuntimeStatusOnline}}
	d.cfg.MaxConcurrency = 1
	atomic.StoreInt32(&d.running, 1)
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := d.claimLoop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteRunCleanupSafeAndUnsafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	root := t.TempDir()
	target := filepath.Join(root, "runs", "run")
	_ = os.MkdirAll(target, 0o755)
	_ = os.WriteFile(filepath.Join(target, "file"), []byte("x"), 0o600)
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}, client: NewClient(server.URL), runSandboxes: map[string]*runSandbox{}}
	result := d.executeRunCleanup(context.Background(), domain.RunnerTask{ID: "cleanup", RunID: "run", Workspace: "runs/run"})
	if result.status != domain.RunnerTaskSuccess {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target err=%v", err)
	}
	result = d.executeRunCleanup(context.Background(), domain.RunnerTask{ID: "unsafe", RunID: "", Workspace: "/tmp"})
	if result.status != domain.RunnerTaskFailed || !strings.Contains(result.errMsg, "unsafe") {
		t.Fatalf("unsafe=%+v", result)
	}
}

func TestRunnerUpdateMetadataAndDownloadErrors(t *testing.T) {
	body := []byte("runner-binary")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("case") {
		case "status":
			http.Error(w, "bad", http.StatusBadGateway)
		case "json":
			_, _ = w.Write([]byte("{"))
		case "short":
			_, _ = w.Write([]byte(`{"sha256":"abc"}`))
		case "nonhex":
			_, _ = w.Write([]byte(`{"sha256":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}`))
		case "empty":
		case "wrong":
			_, _ = w.Write(body)
		default:
			if r.URL.Query().Get("metadata") == "1" {
				_, _ = w.Write([]byte(`{"sha256":"` + hash + `"}`))
			} else {
				_, _ = w.Write(body)
			}
		}
	}))
	defer server.Close()
	for _, name := range []string{"status", "json", "short", "nonhex"} {
		if _, err := runnerBinaryMetadata(context.Background(), server.URL+"?case="+name); err == nil {
			t.Fatalf("metadata case %s accepted", name)
		}
	}
	meta, err := runnerBinaryMetadata(context.Background(), server.URL+"?case=ok")
	if err != nil || meta.SHA256 != hash {
		t.Fatalf("meta=%+v err=%v", meta, err)
	}
	target := filepath.Join(t.TempDir(), "runner")
	if _, err := downloadRunnerBinary(context.Background(), server.URL+"?case=status", target, hash); err == nil {
		t.Fatal("download status accepted")
	}
	if _, err := downloadRunnerBinary(context.Background(), server.URL+"?case=empty", target, hash); err == nil {
		t.Fatal("empty download accepted")
	}
	if _, err := downloadRunnerBinary(context.Background(), server.URL+"?case=wrong", target, strings.Repeat("0", 64)); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	tmp, err := downloadRunnerBinary(context.Background(), server.URL+"?case=ok", target, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp)
	if got, err := hashFile(tmp); err != nil || got != hash {
		t.Fatalf("hash=%q err=%v", got, err)
	}
	d := &Daemon{}
	atomic.StoreInt32(&d.running, 1)
	if err := d.checkForUpdate(context.Background(), target); err != nil {
		t.Fatal(err)
	}
}

func TestControlLoopConnectionErrorAndPreparedInvocationErrors(t *testing.T) {
	d := &Daemon{client: NewClient("file:///tmp"), controls: map[string]chan string{}, pendingControls: map[string]string{}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	d.controlLoop(ctx)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "workspace-file")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	d = &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}, client: NewClient(server.URL), executor: flowruntime.NewExecutorRunner()}
	task := domain.RunnerTask{ID: "bad", Workspace: file, Invocation: &domain.RunnerInvocation{}}
	result, _ := d.executeInvocation(context.Background(), task)
	if result.status != domain.RunnerTaskFailed {
		t.Fatalf("workspace file result=%+v", result)
	}
	workspace := filepath.Join(d.cfg.DefaultWorkspaceRoot, "runs", "run", "workspace")
	task.Workspace = workspace
	task.Invocation = &domain.RunnerInvocation{GateResultPath: "../bad"}
	result, _ = d.executeInvocation(context.Background(), task)
	if result.status != domain.RunnerTaskFailed {
		t.Fatalf("unsafe gate result=%+v", result)
	}
}

func TestExecuteTaskUnsupportedAndUnavailableProvider(t *testing.T) {
	completed := make(chan CompleteRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/complete") {
			var request CompleteRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			completed <- request
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	d := &Daemon{
		cfg:             Config{DefaultWorkspaceRoot: t.TempDir()},
		client:          NewClient(server.URL),
		controls:        map[string]chan string{},
		pendingControls: map[string]string{},
	}
	atomic.StoreInt32(&d.running, 2)
	d.executeTask(context.Background(), domain.RunnerTask{ID: "unknown", Action: "future-action"})
	d.executeTask(context.Background(), domain.RunnerTask{ID: "provider", Provider: "not-installed"})
	for i := 0; i < 2; i++ {
		select {
		case request := <-completed:
			if request.Status != domain.RunnerTaskFailed {
				t.Fatalf("request=%+v", request)
			}
		case <-time.After(time.Second):
			t.Fatal("completion timeout")
		}
	}
}

func TestClaimLoopUnauthorizedReregisters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runners/tasks/claim":
			w.WriteHeader(http.StatusUnauthorized)
		case "/runners/register":
			_, _ = w.Write([]byte(`{"runnerId":"r","sessionToken":"new","expiresAt":"2035-01-01T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	d, err := New(Config{
		ServerURL: server.URL, BootstrapToken: "boot", StatePath: filepath.Join(t.TempDir(), "state.json"),
		DefaultWorkspaceRoot: t.TempDir(), MaxConcurrency: 1, DisableAutoUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.state.RunnerID = "r"
	d.runtimes = []domain.RunnerRuntime{{Provider: "mock", Status: domain.RuntimeStatusOnline}}
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) && d.client.sessionToken() != "new" {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	if err := d.claimLoop(ctx); err != nil {
		t.Fatal(err)
	}
	if d.client.sessionToken() != "new" {
		t.Fatalf("session=%q", d.client.sessionToken())
	}
}

func TestCheckForUpdateInstallsVerifiedBinary(t *testing.T) {
	body := []byte("#!/bin/sh\necho new\n")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("metadata") == "1" {
			_, _ = w.Write([]byte(`{"sha256":"` + hash + `"}`))
		} else {
			_, _ = w.Write(body)
		}
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "runner")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{client: NewClient(server.URL)}
	if err := d.checkForUpdate(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(body) || !d.updateReady.Load() {
		t.Fatalf("body=%q ready=%v", got, d.updateReady.Load())
	}
}
