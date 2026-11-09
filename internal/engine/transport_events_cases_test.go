package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

func TestBrowserAndWorkspaceTransportErrorPaths(t *testing.T) {
	e, st := cvEEngine(t)
	run := cvERunnerRun("browser-run", "browser-runner")
	run.Status = "waiting"
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if response := e.HandleBrowserRequest(context.Background(), run, nil); response.Status != 400 {
		t.Fatalf("nil browser response=%+v", response)
	}
	if response := e.HandleBrowserRequest(context.Background(), run, &domain.BrowserRequest{}); response.Status != 503 {
		t.Fatalf("missing transport response=%+v", response)
	}
	request, err := e.BrowserWorkspaceRequest(run, &domain.BrowserRequest{})
	if err != nil || request.Operation != domain.RunnerWorkspaceOperationBrowser || request.RunID != run.ID {
		t.Fatalf("browser request=%+v err=%v", request, err)
	}
	if _, err := e.BrowserWorkspaceRequest(run, &domain.BrowserRequest{Ensure: true}); err == nil {
		t.Fatal("unregistered runner accepted ensure request")
	}
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{}, errors.New("transport broke")
	}
	if response := e.HandleBrowserRequest(context.Background(), run, &domain.BrowserRequest{}); response.Status != 503 || !strings.Contains(response.Error, "transport broke") {
		t.Fatalf("transport error response=%+v", response)
	}
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Status: 202, Payload: json.RawMessage(`{"operation":"` + req.Operation + `"}`)}, nil
	}
	if response := e.HandleBrowserRequest(context.Background(), run, &domain.BrowserRequest{}); response.Status != 202 {
		t.Fatalf("transport success=%+v", response)
	}
	if got := browserWorkspaceRequest(run, nil); got.Browser != nil || got.WorkspaceLayout != domain.RunnerWorkspaceLayoutShared {
		t.Fatalf("raw browser request=%+v", got)
	}

	e.WorkspaceCheckpointTransport = nil
	if _, err := e.requestWorkspaceCheckpoint(context.Background(), run, domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationRestore}); !errors.Is(err, ErrWorkspaceControlConfiguration) {
		t.Fatalf("missing token err=%v", err)
	}
	e.controlToken = "token"
	if _, err := e.requestWorkspaceCheckpoint(context.Background(), run, domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationRestore}); !errors.Is(err, ErrWorkspaceControlConfiguration) {
		t.Fatalf("missing base err=%v", err)
	}
}

func TestWorkspaceCheckpointHTTPStatusesAndValidation(t *testing.T) {
	if _, err := postWorkspaceRequest(context.Background(), "://bad", "token", domain.RunnerWorkspaceRequest{}); err == nil {
		t.Fatal("invalid workspace URL accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := postWorkspaceRequest(canceled, "http://127.0.0.1:1", "token", domain.RunnerWorkspaceRequest{}); err == nil {
		t.Fatal("canceled workspace request succeeded")
	}
	for _, tc := range []struct {
		name       string
		status     int
		operation  string
		body       string
		wantErr    string
		wantStatus int
	}{
		{name: "checkpoint-status-legacy", status: http.StatusBadRequest, operation: domain.RunnerWorkspaceOperationCheckpointStatus, wantStatus: 400},
		{name: "unauthorized", status: http.StatusUnauthorized, operation: domain.RunnerWorkspaceOperationRestore, wantErr: "认证失败"},
		{name: "forbidden", status: http.StatusForbidden, operation: domain.RunnerWorkspaceOperationRestore, wantErr: "认证失败"},
		{name: "server", status: http.StatusInternalServerError, operation: domain.RunnerWorkspaceOperationRestore, wantErr: "HTTP 500"},
		{name: "invalid-json", status: http.StatusOK, operation: domain.RunnerWorkspaceOperationRestore, body: "{", wantErr: "EOF"},
		{name: "success", status: http.StatusOK, operation: domain.RunnerWorkspaceOperationRestore, body: `{"status":201}`, wantStatus: 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(controlTokenHeader) != "token" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("missing request headers")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			response, err := postWorkspaceRequest(context.Background(), server.URL+"/", "token", domain.RunnerWorkspaceRequest{Operation: tc.operation})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v want=%q", err, tc.wantErr)
				}
			} else if err != nil || response.Status != tc.wantStatus {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}

	e, st := cvEEngine(t)
	e.controlToken = "secret"
	call := func(body string, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/internal/workspace-checkpoint", bytes.NewBufferString(body))
		req.Header.Set(controlTokenHeader, token)
		rec := httptest.NewRecorder()
		e.HandleWorkspaceCheckpoint(rec, req)
		return rec
	}
	if rec := call(`{}`, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d", rec.Code)
	}
	if rec := call(`{`, "secret"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid body=%d", rec.Code)
	}
	missing, _ := json.Marshal(domain.RunnerWorkspaceRequest{RunID: "missing", Operation: domain.RunnerWorkspaceOperationRestore})
	if rec := call(string(missing), "secret"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing run=%d", rec.Code)
	}
	run := cvERunnerRun("transport-run", "transport-runner")
	run.Status = "waiting"
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	for _, request := range []domain.RunnerWorkspaceRequest{
		{RunID: run.ID, Operation: "unknown"},
		{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationTerminal},
	} {
		raw, _ := json.Marshal(request)
		if rec := call(string(raw), "secret"); rec.Code != http.StatusBadRequest {
			t.Fatalf("operation %q code=%d body=%s", request.Operation, rec.Code, rec.Body.String())
		}
	}
	restore, _ := json.Marshal(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationRestore})
	if rec := call(string(restore), "secret"); rec.Code != http.StatusConflict {
		t.Fatalf("runner in-use restore code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkerEnvelopeAndObservationValidationPaths(t *testing.T) {
	e, st := cvEEngine(t)
	run := domain.Run{
		ID: "events-more", Status: "running", WorkflowID: "wf",
		WorkflowSnapshot: domain.WorkflowDefinition{Version: 2, Nodes: []domain.WorkflowNode{{ID: "node"}}},
		Nodes:            map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "running", Attempt: 2}},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if got := e.IngestOTLPTraces([]domain.TelemetryEvent{{}, {RunID: "missing"}, {Context: domain.TelemetryContext{RunID: run.ID, NodeID: "node"}, Name: "otel"}}); got != 1 {
		t.Fatalf("accepted traces=%d", got)
	}
	validSpec := domain.ObservabilitySpecVersion
	cases := []domain.WorkerEnvelope{
		{},
		{SpecVersion: validSpec, Type: "observation"},
		{SpecVersion: validSpec, Type: "observation", Observation: &domain.TelemetryEvent{}},
		{SpecVersion: validSpec, Type: "observation", Observation: &domain.TelemetryEvent{Source: domain.TelemetrySource{Layer: "executor", Adapter: "test"}, NodeID: "missing"}},
		{SpecVersion: validSpec, Type: "log"},
		{SpecVersion: validSpec, Type: "log", Log: &domain.LogRecord{}},
		{SpecVersion: validSpec, Type: "log", Log: &domain.LogRecord{Source: domain.TelemetrySource{Layer: "executor", Adapter: "test"}}},
		{SpecVersion: validSpec, Type: "log", Log: &domain.LogRecord{Source: domain.TelemetrySource{Layer: "executor", Adapter: "test"}, Body: strings.Repeat("x", 64*1024+1)}},
		{SpecVersion: validSpec, Type: "log", Log: &domain.LogRecord{Source: domain.TelemetrySource{Layer: "executor", Adapter: "test"}, Body: "body", Context: domain.TelemetryContext{NodeID: "missing"}}},
		{SpecVersion: validSpec, Type: "other"},
	}
	for i, envelope := range cases {
		if err := e.IngestWorkerEnvelope(run.ID, envelope); err == nil {
			t.Fatalf("invalid envelope %d accepted: %+v", i, envelope)
		}
	}
	if err := e.IngestWorkerEnvelope("missing", domain.WorkerEnvelope{SpecVersion: validSpec, Type: "log", Log: &domain.LogRecord{}}); err == nil {
		t.Fatal("missing run accepted")
	}
	if err := e.IngestWorkerEnvelope(run.ID, domain.WorkerEnvelope{
		SpecVersion: validSpec, Type: "observation",
		Observation: &domain.TelemetryEvent{ID: "observation-id", Name: "process.stdout", Category: "process", Message: "hello", NodeID: "node", Source: domain.TelemetrySource{Layer: "executor", Adapter: "test"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.IngestWorkerEnvelope(run.ID, domain.WorkerEnvelope{
		SpecVersion: validSpec, Type: "log",
		Log: &domain.LogRecord{Body: "worker log", Context: domain.TelemetryContext{NodeID: "node"}, Source: domain.TelemetrySource{Layer: "executor", Adapter: "test"}},
	}); err != nil {
		t.Fatal(err)
	}
	updated, _ := st.Run(run.ID)
	if len(updated.Telemetry) == 0 || len(updated.Logs) < 2 {
		t.Fatalf("telemetry=%d logs=%d", len(updated.Telemetry), len(updated.Logs))
	}
}

func TestRepositoryWorkspaceLocalAndGitBranches(t *testing.T) {
	ctx := context.Background()
	if err := prepareRepositories(ctx, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	repository := domain.RepositoryContext{RepositoryID: "local", Name: "Local", LocalPath: source, DefaultBranch: "main", BaseBranch: "main"}
	if err := prepareRepository(ctx, workspace, repository); err != nil {
		t.Fatal(err)
	}
	if !workspaceHasRepoWork(workspace) {
		if err := os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("change"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !workspaceHasRepoWork(workspace) {
		t.Fatal("workspace change not detected")
	}
	if err := prepareRepository(ctx, t.TempDir(), domain.RepositoryContext{RepositoryID: "bad", RequireExistingWorkBranch: true, WorkBranch: "main", BaseBranch: "main"}); err == nil {
		t.Fatal("invalid continuation branch accepted")
	}
	if err := prepareRepository(ctx, t.TempDir(), domain.RepositoryContext{RepositoryID: "missing"}); err == nil {
		t.Fatal("repository without source accepted")
	}
	if err := prepareRepository(ctx, t.TempDir(), domain.RepositoryContext{RepositoryID: "bad-local", LocalPath: filepath.Join(source, "missing")}); err == nil {
		t.Fatal("missing local source accepted")
	}
	if err := prepareRepository(ctx, t.TempDir(), domain.RepositoryContext{RepositoryID: "continue-local", LocalPath: source, WorkBranch: "feature/x", RequireExistingWorkBranch: true}); err == nil {
		t.Fatal("non-git continuation accepted")
	}
	if err := prepareRepository(ctx, t.TempDir(), domain.RepositoryContext{RepositoryID: "base-local", LocalPath: source, BaseBranch: "release", DefaultBranch: "main"}); err == nil {
		t.Fatal("non-git custom base accepted")
	}

	missing := filepath.Join(t.TempDir(), "new-dir")
	if err := resetCloneDestination(missing); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := resetCloneDestination(filePath); err == nil {
		t.Fatal("file clone destination accepted")
	}
	clearDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(clearDir, "old"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := resetCloneDestination(clearDir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(clearDir); len(entries) != 0 {
		t.Fatalf("clone destination not cleared: %v", entries)
	}

	gitDir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if out, err := exec.Command("git", append([]string{"-C", gitDir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(gitDir, "file.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "base"}, {"branch", "agentflow-base"}} {
		if out, err := exec.Command("git", append([]string{"-C", gitDir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := checkoutWorkBranch(ctx, gitDir, "", "", false); err != nil {
		t.Fatal(err)
	}
	if err := checkoutWorkBranch(ctx, gitDir, "feature/new", "master", false); err != nil {
		t.Fatal(err)
	}
	if branch, _ := gitC(ctx, gitDir, "branch", "--show-current"); strings.TrimSpace(branch) != "feature/new" {
		t.Fatalf("branch=%q", branch)
	}
	if err := checkoutContinuedWorkBranch(ctx, gitDir, "missing"); err == nil {
		t.Fatal("missing continued branch accepted")
	}
	if err := fetchRemoteWorkBranch(ctx, gitDir, ""); err != nil {
		t.Fatal(err)
	}
}

func TestLocalWorkspaceCheckpointMoreBranches(t *testing.T) {
	e, st := cvEEngine(t)
	e.sandboxes = runtime.NewManager(t.TempDir())
	e.controlToken = "local-token"
	run := domain.Run{
		ID: "local-checkpoint-more", Status: "failed",
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "prepare", Type: "script", ScriptLanguage: "shell", Script: "true", RunOnWorkspaceRebuild: true}}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := e.RunWorkspace(run.ID)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "original.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	call := func(request domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
		raw, _ := json.Marshal(request)
		req := httptest.NewRequest(http.MethodPost, "/internal/workspace-checkpoint", bytes.NewReader(raw))
		req.Header.Set(controlTokenHeader, "local-token")
		rec := httptest.NewRecorder()
		e.HandleWorkspaceCheckpoint(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("operation %s code=%d body=%s", request.Operation, rec.Code, rec.Body.String())
		}
		var response domain.RunnerWorkspaceResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	if response := call(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationCheckpointStatus}); response.Status == 0 && len(response.Payload) == 0 {
		t.Fatalf("checkpoint status=%+v", response)
	}
	checkpoint := call(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationCheckpoint})
	if checkpoint.Error != "" || len(checkpoint.Payload) == 0 {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}
	restore := call(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationRestore, Checkpoint: []byte("invalid")})
	if restore.Error == "" {
		t.Fatalf("invalid restore=%+v", restore)
	}
	content := "restored artifact"
	artifacts := call(domain.RunnerWorkspaceRequest{
		RunID: run.ID, Operation: domain.RunnerWorkspaceOperationArtifacts,
		ArtifactFiles: []domain.WorkspaceArtifactFile{{Path: "out/result.txt", Content: content, SHA256: runtime.WorkspaceContentSHA(content)}},
	})
	if artifacts.Error != "" {
		t.Fatalf("artifacts=%+v", artifacts)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "out", "result.txt")); err != nil || string(data) != content {
		t.Fatalf("artifact=%q err=%v", data, err)
	}
	exported := call(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationCheckpointExport})
	if exported.Error != "" {
		t.Fatalf("export=%+v", exported)
	}
	preparationStatus := call(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationPreparationStatus})
	if len(preparationStatus.Payload) == 0 {
		t.Fatalf("preparation status=%+v", preparationStatus)
	}
	prepared := call(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationPrepare})
	if prepared.Error != "" || len(prepared.Payload) == 0 {
		t.Fatalf("prepare=%+v", prepared)
	}
	recorded := call(domain.RunnerWorkspaceRequest{
		RunID: run.ID, Operation: domain.RunnerWorkspaceOperationPreparationRecord,
		Workflow: &run.WorkflowSnapshot, PreparationNodeID: "prepare", PreparationOutput: "done",
	})
	if recorded.Error != "" && len(recorded.Payload) == 0 {
		t.Fatalf("preparation record=%+v", recorded)
	}
	terminal := call(domain.RunnerWorkspaceRequest{
		RunID: run.ID, Operation: domain.RunnerWorkspaceOperationTerminal, TerminalOwner: 7,
		Terminal: &domain.WorkspaceTerminalRequest{Operation: "close-all"},
	})
	if terminal.Error != "" || len(terminal.Payload) == 0 {
		t.Fatalf("terminal close=%+v", terminal)
	}
	invalidTerminal := call(domain.RunnerWorkspaceRequest{
		RunID: run.ID, Operation: domain.RunnerWorkspaceOperationTerminal, TerminalOwner: 7,
		Terminal: &domain.WorkspaceTerminalRequest{Operation: "invalid"},
	})
	if invalidTerminal.Error == "" {
		t.Fatalf("invalid terminal=%+v", invalidTerminal)
	}
	localBrowser := e.HandleBrowserRequest(context.Background(), run, &domain.BrowserRequest{Operation: "state"})
	if localBrowser.Status == 0 && len(localBrowser.Payload) == 0 {
		t.Fatalf("local browser=%+v", localBrowser)
	}
	browserResponse := call(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationBrowser})
	if browserResponse.Status != 400 {
		t.Fatalf("browser handler=%+v", browserResponse)
	}
	rawReclaim, _ := json.Marshal(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationReclaim, CheckpointLease: "wrong"})
	reclaimReq := httptest.NewRequest(http.MethodPost, "/internal/workspace-checkpoint", bytes.NewReader(rawReclaim))
	reclaimReq.Header.Set(controlTokenHeader, "local-token")
	reclaimRec := httptest.NewRecorder()
	e.HandleWorkspaceCheckpoint(reclaimRec, reclaimReq)
	if reclaimRec.Code != http.StatusConflict {
		t.Fatalf("unreserved reclaim=%d body=%s", reclaimRec.Code, reclaimRec.Body.String())
	}

	runner := cvERunner(t, e, "handler-runner-more", domain.RunnerStatusOnline, "terminal")
	runner.RunnerVersion = domain.RunnerVersionWorkspaceOwnerPreparation
	if err := st.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	remote := cvERunnerRun("handler-remote-more", runner.ID)
	remote.Status = "waiting"
	if err := st.PutRun(remote); err != nil {
		t.Fatal(err)
	}
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{}, errors.New("runner unavailable")
	}
	remoteResponse := call(domain.RunnerWorkspaceRequest{RunID: remote.ID, Operation: domain.RunnerWorkspaceOperationPreparationStatus})
	if remoteResponse.Status != 503 || !strings.Contains(remoteResponse.Error, "runner unavailable") {
		t.Fatalf("remote handler=%+v", remoteResponse)
	}
	e.WorkspaceCheckpointTransport = nil
	rawRemote, _ := json.Marshal(domain.RunnerWorkspaceRequest{RunID: remote.ID, Operation: domain.RunnerWorkspaceOperationPreparationStatus})
	remoteReq := httptest.NewRequest(http.MethodPost, "/internal/workspace-checkpoint", bytes.NewReader(rawRemote))
	remoteReq.Header.Set(controlTokenHeader, "local-token")
	remoteRec := httptest.NewRecorder()
	e.HandleWorkspaceCheckpoint(remoteRec, remoteReq)
	if remoteRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing runner transport=%d body=%s", remoteRec.Code, remoteRec.Body.String())
	}
	unregistered := cvERunnerRun("handler-unregistered-more", "absent")
	unregistered.Status = "waiting"
	if err := st.PutRun(unregistered); err != nil {
		t.Fatal(err)
	}
	rawUnregistered, _ := json.Marshal(domain.RunnerWorkspaceRequest{RunID: unregistered.ID, Operation: domain.RunnerWorkspaceOperationPreparationStatus})
	unregisteredReq := httptest.NewRequest(http.MethodPost, "/internal/workspace-checkpoint", bytes.NewReader(rawUnregistered))
	unregisteredReq.Header.Set(controlTokenHeader, "local-token")
	unregisteredRec := httptest.NewRecorder()
	e.HandleWorkspaceCheckpoint(unregisteredRec, unregisteredReq)
	if unregisteredRec.Code != http.StatusConflict {
		t.Fatalf("unregistered runner preparation=%d body=%s", unregisteredRec.Code, unregisteredRec.Body.String())
	}

	busy := domain.Run{ID: "local-checkpoint-busy-more", Status: "running", WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(busy); err != nil {
		t.Fatal(err)
	}
	busyResponse := call(domain.RunnerWorkspaceRequest{RunID: busy.ID, Operation: domain.RunnerWorkspaceOperationCheckpointStatus})
	if busyResponse.Status != http.StatusConflict || busyResponse.Code != domain.WorkspaceErrorBusy {
		t.Fatalf("busy checkpoint status=%+v", busyResponse)
	}

	noWorkspace, noWorkspaceStore := cvEEngine(t)
	noWorkspace.controlToken = "local-token"
	noWorkspaceRun := domain.Run{ID: "checkpoint-no-workspace-more", Status: "failed", Nodes: map[string]*domain.NodeRun{}}
	if err := noWorkspaceStore.PutRun(noWorkspaceRun); err != nil {
		t.Fatal(err)
	}
	rawNoWorkspace, _ := json.Marshal(domain.RunnerWorkspaceRequest{RunID: noWorkspaceRun.ID, Operation: domain.RunnerWorkspaceOperationCheckpoint})
	noWorkspaceReq := httptest.NewRequest(http.MethodPost, "/internal/workspace-checkpoint", bytes.NewReader(rawNoWorkspace))
	noWorkspaceReq.Header.Set(controlTokenHeader, "local-token")
	noWorkspaceRec := httptest.NewRecorder()
	noWorkspace.HandleWorkspaceCheckpoint(noWorkspaceRec, noWorkspaceReq)
	if noWorkspaceRec.Code != http.StatusConflict {
		t.Fatalf("missing local workspace=%d body=%s", noWorkspaceRec.Code, noWorkspaceRec.Body.String())
	}
}

func TestCheckpointStreamLegacyErrorsAndResponseMapping(t *testing.T) {
	if err := workspaceCheckpointResponseError(domain.RunnerWorkspaceResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := workspaceCheckpointResponseError(domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorBusy, Error: "busy"}); !errors.Is(err, repo.ErrWorkspaceInUse) {
		t.Fatalf("busy err=%v", err)
	}
	if err := workspaceCheckpointResponseError(domain.RunnerWorkspaceResponse{Status: 404, Error: "missing"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing err=%v", err)
	}
	if err := workspaceCheckpointResponseError(domain.RunnerWorkspaceResponse{Status: 500, Error: "bad"}); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("generic err=%v", err)
	}
	for _, message := range []string{
		"stat /workspace: no such file or directory",
		"stat C: The system cannot find the path specified.",
		"stat C: The system cannot find the file specified.",
	} {
		if !legacyCheckpointMissingPath(message) {
			t.Fatalf("missing path not recognized: %q", message)
		}
	}
	if legacyCheckpointMissingPath("permission denied") {
		t.Fatal("permission error recognized as missing")
	}

	e, _ := cvEEngine(t)
	run := cvERunnerRun("stream-legacy-more", "runner")
	archive := runtime.EmptyWorkspaceCheckpoint()
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		switch request.Operation {
		case domain.RunnerWorkspaceOperationCheckpointExport:
			return domain.RunnerWorkspaceResponse{Status: 400, Error: "unsupported"}, nil
		case domain.RunnerWorkspaceOperationCheckpoint:
			payload, _ := json.Marshal(map[string]any{"checkpoint": archive})
			return domain.RunnerWorkspaceResponse{Payload: payload}, nil
		default:
			return domain.RunnerWorkspaceResponse{}, nil
		}
	}
	var destination bytes.Buffer
	if err := e.downloadConversationCheckpoint(context.Background(), run, &destination); err != nil || destination.Len() != len(archive) {
		t.Fatalf("legacy download bytes=%d err=%v", destination.Len(), err)
	}
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Payload: json.RawMessage(`{}`)}, nil
	}
	if err := e.downloadConversationCheckpoint(context.Background(), run, &bytes.Buffer{}); err == nil {
		t.Fatal("empty legacy checkpoint accepted")
	}
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Payload: json.RawMessage(`{"id":"transfer","size":0,"sha256":"bad"}`)}, nil
	}
	if err := e.downloadConversationCheckpoint(context.Background(), run, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid transfer manifest accepted")
	}
}
