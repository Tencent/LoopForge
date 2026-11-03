package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func cvERunner(t *testing.T, e *Engine, id string, status string, providers ...string) domain.Runner {
	t.Helper()
	r := domain.Runner{ID: id, Name: id, Status: status, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation}
	for _, provider := range providers {
		r.Runtimes = append(r.Runtimes, domain.RunnerRuntime{Provider: provider, Status: "ready", AuthMode: domain.RuntimeAuthLocal})
	}
	if err := e.repo.PutRunner(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func cvERunnerRun(id, runnerID string) domain.Run {
	return domain.Run{
		ID: id, Status: "failed",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", SandboxProfileID: "local"},
		ExecutionContext: domain.TelemetryContext{
			ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runnerID,
			RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared, ResponsibleUserID: 1,
		},
		Nodes: map[string]*domain.NodeRun{},
	}
}

func TestRunnerCallbacksPathsBindingsAndReconnect(t *testing.T) {
	e, _ := cvEEngine(t)
	var dispatched, controlled string
	e.SetRunnerDispatchNotify(func(provider string) { dispatched = provider })
	e.notifyRunnerDispatch("codex")
	if dispatched != "codex" {
		t.Fatal(dispatched)
	}
	e.SetRunnerControlNotify(func(runnerID, taskID, state string) { controlled = runnerID + taskID + state })
	e.notifyRunnerControl("", "task", "cancel")
	if controlled != "" {
		t.Fatal("empty runner notified")
	}
	e.notifyRunnerControl("runner", "task", "cancel")
	if controlled != "runnertaskcancel" {
		t.Fatal(controlled)
	}

	legacy := cvERunnerRun("run/path", "runner")
	legacy.ExecutionContext.RunnerWorkspaceLayout = ""
	if got := runnerWorkspaceRel(legacy, "node/x"); got != "runs/run-path/node-x" {
		t.Fatal(got)
	}
	shared := legacy
	shared.ExecutionContext.RunnerWorkspaceLayout = domain.RunnerWorkspaceLayoutShared
	if got := runnerWorkspaceRel(shared, "ignored"); got != "runs/run-path/workspace" {
		t.Fatal(got)
	}
	if got := runnerPreviewWorkspaceRel(legacy); got != "runs/run-path" {
		t.Fatal(got)
	}
	if shellSingleQuote("a'b") != `'a'"'"'b'` {
		t.Fatal(shellSingleQuote("a'b"))
	}
	if resolveRunnerResumeSessionID(domain.Run{Nodes: map[string]*domain.NodeRun{"n": {ResumeSessionID: "old"}}}, "n", runtime.InvocationOptions{ResumeSessionID: " new "}) != "new" {
		t.Fatal("option resume ID did not win")
	}
	if resolveRunnerResumeSessionID(domain.Run{Nodes: map[string]*domain.NodeRun{"n": {ResumeSessionID: " old "}}}, "n", runtime.InvocationOptions{}) != "old" {
		t.Fatal("node resume ID missing")
	}

	invocation := &domain.RunnerInvocation{}
	run := domain.Run{
		Repository: domain.RepositoryContext{RepositoryID: "legacy", CloneURL: "https://legacy", BaseBranch: "main", WorkBranch: "work"},
	}
	bindRunnerRepositories(invocation, run, "token", " Name ", " mail ")
	if invocation.Repository == nil || invocation.Repository.GitToken != "token" || invocation.GitUserName != "Name" {
		t.Fatalf("legacy binding = %+v", invocation)
	}
	invocation = &domain.RunnerInvocation{}
	run.Repositories = []domain.RepositoryContext{{RepositoryID: "one", CloneURL: "https://one", WorkspaceDir: "one"}, {RepositoryID: "skip"}}
	bindRunnerRepositories(invocation, run, "token", "", "")
	if len(invocation.Repositories) != 1 || invocation.Repositories[0].Dir != "one" {
		t.Fatalf("bindings = %+v", invocation.Repositories)
	}
	bindRunnerRepositories(nil, run, "", "", "")

	if err := e.requireRunnerProvider("missing", "codex"); err == nil {
		t.Fatal("missing runner accepted")
	}
	cvERunner(t, e, "offline", domain.RunnerStatusOffline, "codex")
	if err := e.requireRunnerProvider("offline", "codex"); err == nil {
		t.Fatal("offline runner accepted")
	}
	cvERunner(t, e, "online", domain.RunnerStatusOnline, "terminal", "codex")
	if err := e.requireRunnerProvider("online", "claude-code"); err == nil {
		t.Fatal("unsupported provider accepted")
	}
	if err := e.requireRunnerProvider("online", "codex"); err != nil {
		t.Fatal(err)
	}
	reconnect := cvERunnerRun("reconnect", "missing")
	reconnect.WorkflowSnapshot.Nodes = []domain.WorkflowNode{{ID: "script", Type: "script"}, {ID: "agent", Type: "agent", AgentID: "missing"}}
	if err := e.validateExecutionReconnect(reconnect, "script"); err == nil {
		t.Fatal("missing reconnect runner accepted")
	}
	reconnect.ExecutionContext.ExecutionRunnerID = "offline"
	if err := e.validateExecutionReconnect(reconnect, "script"); err == nil {
		t.Fatal("offline reconnect accepted")
	}
	reconnect.ExecutionContext.ExecutionRunnerID = "online"
	if err := e.validateExecutionReconnect(reconnect, "unknown"); err == nil {
		t.Fatal("unknown node accepted")
	}
	if err := e.validateExecutionReconnect(reconnect, "agent"); err == nil {
		t.Fatal("missing agent accepted")
	}
	if err := e.validateExecutionReconnect(reconnect, "script"); err != nil {
		t.Fatal(err)
	}
	reconnect.ExecutionContext.ExecutionMode = domain.ExecutionModeCloud
	if err := e.validateExecutionReconnect(reconnect, "anything"); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerQueueAwaitEventsAndArtifacts(t *testing.T) {
	e, _ := cvEEngine(t)
	cvERunner(t, e, "runner", domain.RunnerStatusOnline, "terminal", "codex")
	run := cvERunnerRun("queue-run", "runner")

	if err := e.queueRunnerRunCleanup(domain.Run{ID: "cloud", ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeCloud}}); err != nil {
		t.Fatal(err)
	}
	missing := cvERunnerRun("missing-runner", "gone")
	if err := e.queueRunnerRunCleanup(missing); err != nil {
		t.Fatal(err)
	}
	if err := e.queueRunnerRunCleanup(run); err != nil {
		t.Fatal(err)
	}
	if err := e.queueRunnerRunCleanup(run); err != nil {
		t.Fatal(err)
	}
	if task, ok := e.repo.RunnerTask("rt-cleanup-queue-run"); !ok || task.Action != domain.RunnerTaskActionCleanupRun {
		t.Fatalf("cleanup task = %+v %v", task, ok)
	}
	if err := e.queueRunnerPreviewArchive(domain.Run{ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeCloud}}, domain.RunnerPreviewArchiveSpec{}); err == nil {
		t.Fatal("cloud preview archive accepted")
	}
	if err := e.queueRunnerPreviewArchive(missing, domain.RunnerPreviewArchiveSpec{}); err == nil {
		t.Fatal("missing preview runner accepted")
	}
	spec := domain.RunnerPreviewArchiveSpec{PreviewID: "p", RunID: run.ID, SourcePath: "dist"}
	if err := e.queueRunnerPreviewArchive(run, spec); err != nil {
		t.Fatal(err)
	}
	if err := e.queueRunnerPreviewArchive(run, spec); err != nil {
		t.Fatal(err)
	}

	success := domain.RunnerTask{ID: "done", RunnerID: "runner", Provider: "codex", Status: domain.RunnerTaskSuccess, Result: "ok"}
	if err := e.repo.PutRunnerTask(success); err != nil {
		t.Fatal(err)
	}
	got, err := e.awaitRunnerTask(context.Background(), success.ID, nil)
	if err != nil || got.Result != "ok" {
		t.Fatalf("await = %+v %v", got, err)
	}
	if _, err := e.awaitRunnerTask(context.Background(), "missing", nil); err == nil {
		t.Fatal("missing task awaited")
	}
	ctx, cancel := context.WithCancel(context.Background())
	pending := domain.RunnerTask{ID: "pending", RunnerID: "runner", Provider: "terminal", Status: domain.RunnerTaskQueued}
	if err := e.repo.PutRunnerTask(pending); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := e.awaitRunnerTask(ctx, pending.ID, func(domain.TelemetryEvent) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel await = %v", err)
	}

	now := time.Now().UTC()
	task := domain.RunnerTask{Provider: "codex"}
	events := []domain.RunnerTaskEvent{
		{Seq: 1, Type: "observation", Data: `{"name":"model.done","message":"done"}`, Ts: now},
		{Seq: 2, Type: "observation", Data: `{bad`, Ts: now},
		{Seq: 3, Type: "stdout", Data: "hello", Ts: now},
		{Seq: 4, Type: "stderr", Data: "bad", Ts: now},
		{Seq: 5, Type: "other", Data: "skip", Ts: now},
	}
	var observed []domain.TelemetryEvent
	if after := bridgeRunnerEventsToSink(task, events, 0, func(event domain.TelemetryEvent) { observed = append(observed, event) }); after != 5 || len(observed) != 3 {
		t.Fatalf("bridge after/observed = %d/%d", after, len(observed))
	}
	if after := bridgeRunnerEventsToSink(task, events, 2, nil); after != 5 {
		t.Fatalf("nil bridge after = %d", after)
	}
	task.Invocation = &domain.RunnerInvocation{Executor: domain.ExecutorDefinition{Protocol: "jsonl"}}
	observed = nil
	bridgeRunnerEventsToSink(task, []domain.RunnerTaskEvent{{Seq: 1, Type: "stdout", Data: `{}`}, {Seq: 2, Type: "stderr", Data: "x"}}, 0, func(event domain.TelemetryEvent) { observed = append(observed, event) })
	if len(observed) != 0 {
		t.Fatalf("protocol noise retained: %+v", observed)
	}

	dir := t.TempDir()
	contracts := []domain.ArtifactContract{{Name: "report", Path: "nested/report.md"}, {Name: "empty", Path: ""}}
	if err := restoreRunnerArtifacts(dir, contracts, map[string]string{"report": "body", "unknown": "skip", "empty": "skip"}); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "nested", "report.md")); string(body) != "body" {
		t.Fatalf("restored = %q", body)
	}
	if err := restoreRunnerArtifacts(dir, nil, map[string]string{"x": "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerDispatchEarlyAndCanceledPaths(t *testing.T) {
	e, _ := cvEEngine(t)
	r := cvERunner(t, e, "dispatch", domain.RunnerStatusOnline, "terminal", "codex", "claude-code", "codebuddy")
	for i := range r.Runtimes {
		r.Runtimes[i].AuthMode = domain.RuntimeAuthPersonal
	}
	if err := e.repo.PutRunner(r); err != nil {
		t.Fatal(err)
	}
	run := cvERunnerRun("dispatch-run", r.ID)
	node := domain.WorkflowNode{ID: "agent", Type: "agent"}
	sb := &runEnvironment{}
	for _, kind := range []string{"codex", "claude-code", "codebuddy"} {
		_, err := e.dispatchRunnerTask(context.Background(), r.ID, run, node, sb, "workspace", domain.ExecutorDefinition{ID: kind, Kind: kind}, domain.AgentDefinition{ID: "a"}, domain.AgentAssetBundle{}, "prompt", "inv", runtime.InvocationOptions{}, nil)
		if err == nil {
			t.Fatalf("%s missing personal credential accepted", kind)
		}
	}
	sb.credentials.Codex.APIKey = "key"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.dispatchRunnerTask(ctx, r.ID, run, node, sb, "workspace", domain.ExecutorDefinition{ID: "codex", Kind: "codex"}, domain.AgentDefinition{ID: "a"}, domain.AgentAssetBundle{}, "prompt", "inv", runtime.InvocationOptions{GateResultPath: "gate.json"}, func(domain.TelemetryEvent) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatch cancel = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := e.dispatchRunnerScript(ctx, r.ID, run, "script", "workspace", "python", "print('x')", "", nil, sb, "", "", "", domain.AgentAssetBundle{}, func(domain.TelemetryEvent) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("script cancel = %v", err)
	}
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := e.dispatchRunnerCommand(ctx, r.ID, run.ID, "gate", "workspace", "echo ok", func(domain.TelemetryEvent) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("command cancel = %v", err)
	}
}

func TestWorkspaceCheckpointHandlerRunnerValidation(t *testing.T) {
	e, _ := cvEEngine(t)
	cvERunner(t, e, "handler", domain.RunnerStatusOnline, "terminal")
	run := cvERunnerRun("handler-run", "handler")
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.controlToken = "secret"
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Status: 200, Payload: json.RawMessage(`{"operation":"` + request.Operation + `"}`)}, nil
	}
	call := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/internal/workspace-checkpoint", strings.NewReader(body))
		req.Header.Set(controlTokenHeader, token)
		rec := httptest.NewRecorder()
		e.HandleWorkspaceCheckpoint(rec, req)
		return rec
	}
	if rec := call("", `{}`); rec.Code != 401 {
		t.Fatalf("unauthorized = %d", rec.Code)
	}
	if rec := call("secret", `{bad`); rec.Code != 400 {
		t.Fatalf("malformed = %d", rec.Code)
	}
	if rec := call("secret", `{"runId":"missing","operation":"x"}`); rec.Code != 404 {
		t.Fatalf("missing = %d", rec.Code)
	}
	if rec := call("secret", `{"runId":"handler-run","operation":"x"}`); rec.Code != 400 {
		t.Fatalf("unsupported = %d", rec.Code)
	}
	if rec := call("secret", `{"runId":"handler-run","operation":"terminal"}`); rec.Code != 400 {
		t.Fatalf("invalid terminal = %d", rec.Code)
	}
	body := `{"runId":"handler-run","operation":"preparation_status"}`
	if rec := call("secret", body); rec.Code != 200 || !strings.Contains(rec.Body.String(), "operation") {
		t.Fatalf("forward = %d %s", rec.Code, rec.Body.String())
	}
	e.WorkspaceCheckpointTransport = nil
	if rec := call("secret", body); rec.Code != 503 {
		t.Fatalf("missing transport = %d", rec.Code)
	}
}
