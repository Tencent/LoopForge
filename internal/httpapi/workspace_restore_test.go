package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"github.com/gin-gonic/gin"
)

func TestRetryAsyncRestoreSurvivesDisconnectAndReusesBackgroundOperation(t *testing.T) {
	a, owner, _, outsider, _ := sharedWorkbenchFixture(t)
	en := engine.New(a.repo, runtime.NewManager(t.TempDir()))
	en.ConfigureExecution("restore-api", "restore-api", time.Minute, false)
	a.deps.engine = en
	h := &runsHandlers{deps: a.deps}
	run, _ := a.repo.RunState("shared-run")
	run.Status = "failed"
	run.WorkflowSnapshot.Nodes = []domain.WorkflowNode{{ID: "work", Type: "script", Script: "true", MaxAttempts: 1}}
	run.WorkflowSnapshot.SandboxProfileID = "test-native"
	run.Nodes = map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "failed", Attempt: 1}}
	run.ExecutionContext.ExecutionMode, run.ExecutionContext.ExecutionRunnerID = domain.ExecutionModeRunner, "restore-runner"
	if err := a.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := a.repo.PutRunner(domain.Runner{ID: "restore-runner", Status: domain.RunnerStatusOnline, Runtimes: []domain.RunnerRuntime{{Provider: "terminal", Status: domain.RuntimeStatusOnline}}}); err != nil {
		t.Fatal(err)
	}
	token, err := en.BeginConversationWorkspaceUse(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.repo.SaveWorkspaceCheckpoint(run.ID, token, time.Now().UTC(), runtime.EmptyWorkspaceCheckpoint()); err != nil {
		t.Fatal(err)
	}
	if err = a.repo.FinishWorkspaceUse(run.ID, token, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
	if err = a.repo.DB.Table("runs").Where("id = ?", run.ID).Update("workspace_cleaned_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var restores atomic.Int32
	en.WorkspaceCheckpointTransport = func(ctx context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		if req.Operation == domain.RunnerWorkspaceOperationCheckpointStatus {
			return domain.RunnerWorkspaceResponse{Status: 404, Code: domain.WorkspaceErrorMissing, Error: "missing"}, nil
		}
		if req.Operation != domain.RunnerWorkspaceOperationRestore {
			return domain.RunnerWorkspaceResponse{}, errors.New("unexpected workspace operation")
		}
		if restores.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			return domain.RunnerWorkspaceResponse{Status: 200}, nil
		case <-ctx.Done():
			return domain.RunnerWorkspaceResponse{}, ctx.Err()
		}
	}
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			state, _ := en.WorkspaceRestoreStatus(run.ID)
			if state.Status != "restoring" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("background restore did not finish")
	})
	router := gin.New()
	router.POST("/api/runs/:id/retry", h.run)
	router.GET("/api/runs/:id/workspace/restore", h.workspaceRestoreStatus)
	decision := `{"nodeId":"work","reason":"重跑","expected":{"status":"failed","attempt":1,"reworkCount":0}}`
	request, response := workbenchContext(owner, "POST", "/api/runs/shared-run/retry", bytes.NewBufferString(decision))
	request.Request.Header.Set("Prefer", "respond-async")
	ctx, cancel := context.WithCancel(request.Request.Context())
	request.Request = request.Request.WithContext(ctx)
	router.ServeHTTP(response, request.Request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("restore was not accepted separately: %d %s", response.Code, response.Body)
	}
	cancel() // Closing the page cannot cancel the accepted restore.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not start")
	}
	if _, err := en.BeginConversationWorkspaceUse(run.ID); !errors.Is(err, repo.ErrWorkspaceInUse) {
		t.Fatalf("disconnect released the background lease: %v", err)
	}
	request, response = workbenchContext(owner, "POST", "/api/runs/shared-run/retry", bytes.NewBufferString(decision))
	request.Request.Header.Set("Prefer", "respond-async")
	router.ServeHTTP(response, request.Request)
	if response.Code != 202 || restores.Load() != 1 {
		t.Fatalf("repeat retry duplicated restoration: %d %s count=%d", response.Code, response.Body, restores.Load())
	}
	request, response = workbenchContext(outsider, "GET", "/api/runs/shared-run/workspace/restore", bytes.NewBuffer(nil))
	router.ServeHTTP(response, request.Request)
	if response.Code != 403 {
		t.Fatal("restore status escaped project permissions", response.Code)
	}
	once.Do(func() { close(release) })
	for deadline := time.Now().Add(5 * time.Second); ; {
		request, response = workbenchContext(owner, "GET", "/api/runs/shared-run/workspace/restore", bytes.NewBuffer(nil))
		router.ServeHTTP(response, request.Request)
		var state domain.WorkspaceRestoreStatus
		if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if state.Status == "ready" {
			break
		}
		if state.Status != "restoring" || time.Now().After(deadline) {
			t.Fatalf("background result not visible: %s", response.Body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	request, response = workbenchContext(owner, "POST", "/api/runs/shared-run/retry", bytes.NewBufferString(decision))
	request.Request.Header.Set("Prefer", "respond-async")
	router.ServeHTTP(response, request.Request)
	if response.Code != 200 {
		t.Fatalf("ready workspace could not retry: %d %s", response.Code, response.Body)
	}
	current, _ := a.repo.RunState(run.ID)
	if current.Status != "running" || current.Nodes["work"].Status != "pending" || restores.Load() != 1 {
		t.Fatal("retry did not start exactly once after restoration")
	}
}

func TestAsyncWorkspaceRestoreDoesNotAcknowledgeMissingSnapshot(t *testing.T) {
	a, owner, _, _, _ := sharedWorkbenchFixture(t)
	a.deps.engine = engine.New(a.repo, runtime.NewManager(t.TempDir()))
	if err := a.repo.DB.Table("runs").Where("id = ?", "shared-run").Updates(map[string]any{"status": "succeeded", "workspace_cleaned_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	h := &runsHandlers{deps: a.deps}
	c, response := workbenchContext(owner, "POST", "/api/runs/shared-run/workspace/restore", bytes.NewBufferString(`{}`))
	c.Params = gin.Params{{Key: "id", Value: "shared-run"}}
	c.Request.Header.Set("Prefer", "respond-async")
	h.workspaceRestore(c)
	if response.Code != 409 {
		t.Fatalf("missing snapshot was acknowledged as restored: %d %s", response.Code, response.Body)
	}
}

func TestWorkspaceRestoreEligibilityUsesWorkspaceEditAccess(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	a, owner, member, viewer, _ := sharedWorkbenchFixture(t)
	project, _ := a.repo.Project("workbench")
	project.Members = append(project.Members, domain.ProjectMember{UserID: viewer.ID, Role: "viewer"})
	if err := a.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	a.deps.engine = engine.New(a.repo, runtime.NewManager(t.TempDir()))
	h := &runsHandlers{deps: a.deps}
	run, _ := a.repo.RunState("shared-run")
	now := time.Now().UTC()
	token, err := h.engine.BeginConversationWorkspaceUse(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.repo.SaveWorkspaceCheckpoint(run.ID, token, now, runtime.EmptyWorkspaceCheckpoint()); err != nil {
		t.Fatal(err)
	}
	if err = h.repo.FinishWorkspaceUse(run.ID, token, now, nil, ""); err != nil {
		t.Fatal(err)
	}
	run.Status = "succeeded"
	run.WorkspaceCleanedAt = now
	if err = h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.Use(h.runWriteGuard())
	router.GET("/api/runs/:id/workspace", h.runWorkspace)
	router.POST("/api/runs/:id/workspace/restore", h.workspaceRestore)
	const workspace = "/api/runs/shared-run/workspace"
	for _, tc := range []struct {
		name       string
		user       domain.User
		restorable bool
	}{
		{"viewer", viewer, false},
		{"member", member, true},
		{"owner", owner, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, response := workbenchContext(tc.user, http.MethodGet, workspace, bytes.NewBuffer(nil))
			router.ServeHTTP(response, request.Request)
			var result struct {
				Available         bool       `json:"available"`
				Restorable        *bool      `json:"restorable"`
				CheckpointSavedAt *time.Time `json:"checkpointSavedAt"`
			}
			err := json.Unmarshal(response.Body.Bytes(), &result)
			if response.Code != http.StatusOK || err != nil || result.Available || result.CheckpointSavedAt == nil || result.Restorable == nil || *result.Restorable != tc.restorable {
				t.Fatalf("restorable should be %v for %s: %d %s", tc.restorable, tc.name, response.Code, response.Body)
			}
		})
	}
	request, response := workbenchContext(viewer, http.MethodPost, workspace+"/restore", bytes.NewBuffer(nil))
	request.Request.Header.Set("Prefer", "respond-async")
	router.ServeHTTP(response, request.Request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("viewer restore: %d %s", response.Code, response.Body)
	}
	request, response = workbenchContext(owner, http.MethodPost, workspace+"/restore", bytes.NewBuffer(nil))
	router.ServeHTTP(response, request.Request)
	if response.Code != http.StatusOK {
		t.Fatalf("owner restore: %d %s", response.Code, response.Body)
	}
}
