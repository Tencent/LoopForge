package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestQueuedActivationLeavesWorkspaceArchivedUntilAdmission(t *testing.T) {
	for _, action := range []string{"retry", "resume"} {
		t.Run(action, func(t *testing.T) {
			a, owner, _, _, _ := sharedWorkbenchFixture(t)
			en := engine.New(a.repo, runtime.NewManager(t.TempDir()))
			en.ConfigureExecution("activation-api", "activation-host", time.Minute, false)
			a.deps.engine = en
			h := &runsHandlers{deps: a.deps}
			run, _ := a.repo.RunState("shared-run")
			run.ExecutionContext.ExecutionMode = domain.ExecutionModeCloud
			run.Status = "failed"
			run.WorkflowSnapshot.Nodes = []domain.WorkflowNode{{ID: "work", Type: "script", Script: "true", MaxAttempts: 1}}
			run.Nodes = map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "failed", Attempt: 1}}
			if action == "resume" {
				run.Status = "waiting"
				run.Nodes["work"].Status = "waiting_approval"
				run.Nodes["work"].Activity = "已由用户暂停，继续后脚本将从头执行"
				if err := a.repo.DB.Table("runs").Where("id = ?", run.ID).Update("pause_requested", true).Error; err != nil {
					t.Fatal(err)
				}
			}
			run.WorkspaceCleanedAt = time.Now().UTC()
			if err := a.repo.PutRun(run); err != nil {
				t.Fatal(err)
			}
			token, err := en.BeginConversationWorkspaceUse(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.repo.FinishWorkspaceUse(run.ID, token, time.Now().UTC(), runtime.EmptyWorkspaceCheckpoint(), ""); err != nil {
				t.Fatal(err)
			}
			if released, err := a.repo.ReleaseCleanedRunAdmission(run.ID, ""); err != nil || !released {
				t.Fatalf("release archived run: %v, %v", released, err)
			}
			for i := range repo.ProjectRunLimit {
				if err := a.repo.PutRun(domain.Run{ID: fmt.Sprintf("slot-%d", i), Status: "running", ExecutionContext: domain.TelemetryContext{ProjectID: run.ExecutionContext.ProjectID}}); err != nil {
					t.Fatal(err)
				}
			}
			router := gin.New()
			router.POST("/api/runs/:id/"+action, h.run)
			router.POST("/api/runs/:id/workspace/restore", h.workspaceRestore)
			post := func(path string) *httptest.ResponseRecorder {
				c, response := workbenchContext(owner, "POST", "/api/runs/shared-run/"+path, bytes.NewBufferString(`{}`))
				c.Request.Header.Set("Prefer", "respond-async")
				router.ServeHTTP(response, c.Request)
				return response
			}
			if response := post(action); response.Code != http.StatusOK {
				t.Fatalf("%s response = %d %s", action, response.Code, response.Body)
			}
			if got, _ := a.repo.RunState(run.ID); got.Status != "queued" || got.WorkspaceCleanedAt.IsZero() {
				t.Fatalf("activation did not queue with the archive intact: %s, %v", got.Status, got.WorkspaceCleanedAt)
			}
			if _, err := os.Stat(en.RunWorkspace(run.ID)); !os.IsNotExist(err) {
				t.Fatalf("activation opened a queued workspace: %v", err)
			}
			if response := post("workspace/restore"); response.Code != http.StatusOK && response.Code != http.StatusAccepted {
				t.Fatalf("explicit restore response = %d %s", response.Code, response.Body)
			}
			for deadline := time.Now().Add(5 * time.Second); ; {
				state, err := en.WorkspaceRestoreStatus(run.ID)
				if err != nil || state.Status == "failed" || time.Now().After(deadline) {
					t.Fatalf("explicit restore did not finish: %+v, %v", state, err)
				}
				if state.Status == "ready" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if got, _ := a.repo.RunState(run.ID); got.Status != "queued" || !got.WorkspaceCleanedAt.IsZero() {
				t.Fatalf("explicit restore should open the queued workspace: %s, %v", got.Status, got.WorkspaceCleanedAt)
			}
		})
	}
}

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
	run.Nodes = map[string]*domain.NodeRun{"sub": {NodeID: "sub", Status: "succeeded", ChildRunID: "shared-child"}}
	if err = h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	child := domain.Run{ID: "shared-child", Status: "succeeded", ExecutionContext: run.ExecutionContext}
	child.ExecutionContext.ParentRunID = run.ID
	child.ExecutionContext.WorkspaceRunID = run.ID
	if err = h.repo.PutRun(child); err != nil {
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
			for _, path := range []string{workspace, "/api/runs/shared-child/workspace"} {
				request, response := workbenchContext(tc.user, http.MethodGet, path, bytes.NewBuffer(nil))
				router.ServeHTTP(response, request.Request)
				var result struct {
					Available         bool       `json:"available"`
					Restorable        *bool      `json:"restorable"`
					CheckpointSavedAt *time.Time `json:"checkpointSavedAt"`
				}
				err := json.Unmarshal(response.Body.Bytes(), &result)
				if response.Code != http.StatusOK || err != nil || result.Available || result.CheckpointSavedAt == nil || result.Restorable == nil || *result.Restorable != tc.restorable {
					t.Fatalf("restorable should be %v for %s at %s: %d %s", tc.restorable, tc.name, path, response.Code, response.Body)
				}
			}
		})
	}
	request, response := workbenchContext(viewer, http.MethodPost, "/api/runs/shared-child/workspace/restore", bytes.NewBuffer(nil))
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

func TestSOPWorkspaceRestoresConversationCheckpoint(t *testing.T) {
	for _, mode := range []string{"sync", "async"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
			a, owner, _, viewer, _ := sharedWorkbenchFixture(t)
			project, _ := a.repo.Project("workbench")
			project.Members = append(project.Members, domain.ProjectMember{UserID: viewer.ID, Role: "viewer"})
			if err := a.repo.PutProject(project); err != nil {
				t.Fatal(err)
			}
			manager := runtime.NewManager(t.TempDir())
			en := engine.New(a.repo, manager)
			en.ConfigureExecution("restore-api", "restore-api", time.Minute, false)
			a.engine = en
			ctx := domain.TelemetryContext{ProjectID: project.ID, ResponsibleUserID: owner.ID, ExecutionPinned: true}
			workspace, err := en.StartConversationWorkspace("Conversation", ctx, repo.TestNativeSandboxID)
			if err != nil {
				t.Fatal(err)
			}
			const name, content = "handoff.txt", "unsent changes from chat"
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := runtime.CreateWorkspaceCheckpoint(source)
			if err != nil {
				t.Fatal(err)
			}
			token, err := en.BeginConversationWorkspaceUse(workspace.ID)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if err := a.repo.SaveWorkspaceCheckpoint(workspace.ID, token, now, snapshot); err != nil {
				t.Fatal(err)
			}
			if err := a.repo.FinishWorkspaceUse(workspace.ID, token, now, nil, ""); err != nil {
				t.Fatal(err)
			}
			workspace.WorkspaceCleanedAt = now
			if err := a.repo.PutRun(workspace); err != nil {
				t.Fatal(err)
			}
			ctx.WorkspaceRunID = workspace.ID
			sop := domain.Run{ID: "borrowing-sop", Status: "succeeded", ExecutionContext: ctx}
			if err := a.repo.PutRun(sop); err != nil {
				t.Fatal(err)
			}

			h := &runsHandlers{deps: a.deps}
			router := gin.New()
			router.Use(h.runWriteGuard())
			router.GET("/api/runs/:id/workspace", h.runWorkspace)
			router.GET("/api/runs/:id/workspace/file", h.runWorkspace)
			router.GET("/api/runs/:id/workspace/restore", h.workspaceRestoreStatus)
			router.POST("/api/runs/:id/workspace/restore", h.workspaceRestore)
			router.POST("/api/runs/:id/workspace/use", h.workspaceActivity)
			path := "/api/runs/" + sop.ID + "/workspace"
			request := func(actor domain.User, method, suffix string) *httptest.ResponseRecorder {
				c, rec := workbenchContext(actor, method, path+suffix, bytes.NewBuffer(nil))
				if mode == "async" {
					c.Request.Header.Set("Prefer", "respond-async")
				}
				router.ServeHTTP(rec, c.Request)
				return rec
			}
			view := request(owner, http.MethodGet, "")
			var info struct {
				Available         bool       `json:"available"`
				Restorable        bool       `json:"restorable"`
				CheckpointSavedAt *time.Time `json:"checkpointSavedAt"`
			}
			if err := json.Unmarshal(view.Body.Bytes(), &info); err != nil || view.Code != 200 ||
				info.Available || !info.Restorable || info.CheckpointSavedAt == nil {
				t.Fatalf("borrowed workspace should be restorable: %d %s", view.Code, view.Body)
			}
			if rec := request(viewer, http.MethodGet, ""); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"restorable":false`)) {
				t.Fatalf("viewer workspace info = %d: %s", rec.Code, rec.Body)
			}
			if rec := request(viewer, http.MethodPost, "/restore"); rec.Code != http.StatusForbidden {
				t.Fatalf("viewer restore = %d: %s", rec.Code, rec.Body)
			}
			foreign := sop
			foreign.ID, foreign.ExecutionContext.ProjectID = "foreign-sop", "foreign-project"
			if err := a.repo.PutProject(domain.Project{ID: foreign.ExecutionContext.ProjectID, Name: "Foreign", OwnerID: owner.ID}); err != nil {
				t.Fatal(err)
			}
			if err := a.repo.PutRun(foreign); err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"restore", "use"} {
				c, response := workbenchContext(owner, http.MethodPost, "/api/runs/"+foreign.ID+"/workspace/"+action, bytes.NewBuffer(nil))
				router.ServeHTTP(response, c.Request)
				if response.Code != http.StatusGone {
					t.Fatalf("cross-project workspace %s = %d: %s", action, response.Code, response.Body)
				}
			}
			sop.Status = "running"
			if err := a.repo.PutRun(sop); err != nil {
				t.Fatal(err)
			}
			if rec := request(owner, http.MethodPost, "/restore"); rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(repo.ErrWorkspaceInUse.Error())) {
				t.Fatalf("running SOP restore = %d: %s", rec.Code, rec.Body)
			}
			sop.Status = "succeeded"
			if err := a.repo.PutRun(sop); err != nil {
				t.Fatal(err)
			}
			rec := request(owner, http.MethodPost, "/restore")
			if rec.Code != http.StatusOK && (mode != "async" || rec.Code != http.StatusAccepted) {
				t.Fatalf("SOP restore = %d: %s", rec.Code, rec.Body)
			}
			for deadline := time.Now().Add(5 * time.Second); ; {
				status := request(owner, http.MethodGet, "/restore")
				var state domain.WorkspaceRestoreStatus
				if err := json.Unmarshal(status.Body.Bytes(), &state); err != nil || status.Code != http.StatusOK {
					t.Fatalf("SOP restore status = %d: %s", status.Code, status.Body)
				}
				if state.Status == "ready" {
					break
				}
				if state.Status != "restoring" || time.Now().After(deadline) {
					t.Fatalf("SOP restore did not finish: %s", status.Body)
				}
				time.Sleep(10 * time.Millisecond)
			}
			file := request(owner, http.MethodGet, "/file?path="+name)
			var result struct{ Content string }
			if err := json.Unmarshal(file.Body.Bytes(), &result); err != nil || file.Code != http.StatusOK || result.Content != content {
				t.Fatalf("restored file = %d: %s", file.Code, file.Body)
			}
			if restored, ok := a.repo.RunState(workspace.ID); !ok || !restored.WorkspaceCleanedAt.IsZero() {
				t.Fatal("the owning workspace still has a cleanup mark")
			}
			if _, err := os.Stat(manager.RunWorkspace(sop.ID)); !os.IsNotExist(err) {
				t.Fatalf("restore created a separate SOP workspace: %v", err)
			}
		})
	}
}

func TestSOPWorkspaceActivityTouchesConversationOwner(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	a, owner, member, outsider, _ := sharedWorkbenchFixture(t)
	en := engine.New(a.repo, runtime.NewManager(t.TempDir()))
	en.ConfigureExecution("activity-api", "activity-api", time.Minute, false)
	a.engine = en
	ctx := domain.TelemetryContext{ProjectID: "workbench", ResponsibleUserID: owner.ID, ExecutionPinned: true}
	workspace, err := en.StartConversationWorkspace("Conversation", ctx, repo.TestNativeSandboxID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(en.RunWorkspace(workspace.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := time.Now().UTC().Add(-30 * time.Minute)
	if err := a.repo.TouchWorkspaceUse(workspace.ID, previous); err != nil {
		t.Fatal(err)
	}
	ctx.WorkspaceRunID = workspace.ID
	sop := domain.Run{ID: "settled-sop", Status: "succeeded", WorkspaceCleanedAt: time.Now().UTC(), ExecutionContext: ctx}
	if err := a.repo.PutRun(sop); err != nil {
		t.Fatal(err)
	}

	h := &runsHandlers{deps: a.deps}
	router := gin.New()
	router.Use(h.runWriteGuard())
	router.GET("/api/runs/:id/workspace", h.runWorkspace)
	router.POST("/api/runs/:id/workspace/use", h.workspaceActivity)
	request := func(actor domain.User, method, suffix string) *httptest.ResponseRecorder {
		c, response := workbenchContext(actor, method, "/api/runs/"+sop.ID+"/workspace"+suffix, bytes.NewBuffer(nil))
		router.ServeHTTP(response, c.Request)
		return response
	}
	if rec := request(member, http.MethodGet, ""); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"available":true`)) {
		t.Fatalf("shared workspace should be available: %d %s", rec.Code, rec.Body)
	}
	if rec := request(outsider, http.MethodPost, "/use"); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider workspace activity = %d: %s", rec.Code, rec.Body)
	}
	if rec := request(member, http.MethodPost, "/use"); rec.Code != http.StatusOK {
		t.Fatalf("shared workspace activity = %d: %s", rec.Code, rec.Body)
	}
	if checkpoint, ok := a.repo.WorkspaceCheckpoint(workspace.ID); !ok || !checkpoint.LastUsedAt.After(previous) {
		t.Fatal("activity did not update the conversation workspace")
	}
	if _, ok := a.repo.WorkspaceCheckpoint(sop.ID); ok {
		t.Fatal("activity created a checkpoint for the borrowing SOP")
	}
	workspace.WorkspaceCleanedAt = time.Now().UTC()
	if err := a.repo.PutRun(workspace); err != nil {
		t.Fatal(err)
	}
	if rec := request(member, http.MethodPost, "/use"); rec.Code != http.StatusConflict {
		t.Fatalf("reclaimed workspace activity = %d: %s", rec.Code, rec.Body)
	}
}

func TestSharedRunnerWorkspaceRestoreRequiresConnectedRunner(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	a, user := workbenchFixture(t)
	en := engine.New(a.repo, runtime.NewManager(t.TempDir()))
	a.deps.engine = en
	runner := domain.Runner{ID: "shared-runner", Status: domain.RunnerStatusOnline}
	if err := a.repo.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	parent := domain.Run{
		ID: "runner-parent", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{
			ProjectID: "workbench", ExecutionMode: domain.ExecutionModeRunner,
			ExecutionRunnerID: runner.ID, RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
		},
		Nodes: map[string]*domain.NodeRun{"sub": {NodeID: "sub", Status: "succeeded", ChildRunID: "runner-child"}},
	}
	child := domain.Run{ID: "runner-child", Status: "succeeded", ExecutionContext: parent.ExecutionContext}
	child.ExecutionContext.ParentRunID = parent.ID
	child.ExecutionContext.WorkspaceRunID = parent.ID
	for _, run := range []domain.Run{parent, child} {
		if err := a.repo.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	token, err := en.BeginConversationWorkspaceUse(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.repo.SaveWorkspaceCheckpoint(parent.ID, token, now, runtime.EmptyWorkspaceCheckpoint()); err != nil {
		t.Fatal(err)
	}
	if err := a.repo.FinishWorkspaceUse(parent.ID, token, now, nil, ""); err != nil {
		t.Fatal(err)
	}
	hub := newRunnerControlHub()
	hub.startupGrace, hub.reconnectWait = 0, 0
	h := &runsHandlers{deps: a.deps, runnerControls: hub}
	router := gin.New()
	router.Use(h.runWriteGuard())
	router.GET("/api/runs/:id/workspace", h.runWorkspace)
	router.POST("/api/runs/:id/workspace/restore", h.workspaceRestore)
	request := func(id, method, suffix string) *httptest.ResponseRecorder {
		c, response := workbenchContext(user, method, "/api/runs/"+id+"/workspace"+suffix, bytes.NewBuffer(nil))
		c.Request.Header.Set("Prefer", "respond-async")
		router.ServeHTTP(response, c.Request)
		return response
	}
	connect := func(result domain.RunnerWorkspaceResponse) *runnerControlConn {
		conn := hub.register(runner, nil, false, false)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case message := <-conn.send:
					if message.WorkspaceRequest.RunID != parent.ID || message.WorkspaceRequest.WorkspaceLayout != domain.RunnerWorkspaceLayoutShared {
						t.Errorf("workspace request did not reach the shared checkout: %+v", message.WorkspaceRequest)
					}
					hub.resolveWorkspace(conn, message.RequestID, result)
				case <-conn.done:
					return
				}
			}
		}()
		t.Cleanup(func() {
			hub.unregister(conn)
			<-done
		})
		return conn
	}
	checkInfo := func(id string, available, restorable bool, reason string) {
		t.Helper()
		response := request(id, http.MethodGet, "")
		var info struct {
			Available         bool       `json:"available"`
			Restorable        *bool      `json:"restorable"`
			CheckpointSavedAt *time.Time `json:"checkpointSavedAt"`
			Reason            string     `json:"reason"`
		}
		err := json.Unmarshal(response.Body.Bytes(), &info)
		if response.Code != http.StatusOK || err != nil || info.Available != available ||
			info.Restorable == nil || *info.Restorable != restorable || info.CheckpointSavedAt == nil || !strings.Contains(info.Reason, reason) {
			t.Errorf("%s workspace want available=%t restorable=%t reason=%q, got %d %s", id, available, restorable, reason, response.Code, response.Body)
		}
	}
	conn := connect(domain.RunnerWorkspaceResponse{Status: http.StatusOK, Payload: json.RawMessage(`{"available":true}`)})
	for _, id := range []string{parent.ID, child.ID} {
		checkInfo(id, true, true, "")
	}
	hub.unregister(conn)

	checkOffline := func() {
		for _, id := range []string{parent.ID, child.ID} {
			checkInfo(id, false, false, "当前离线")
			response := request(id, http.MethodPost, "/restore")
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "当前离线") {
				t.Errorf("%s restore must report offline: %d %s", id, response.Code, response.Body)
			}
		}
	}
	checkOffline()
	if err := a.repo.DB.Table("runs").Where("id = ?", parent.ID).Update("workspace_cleaned_at", now).Error; err != nil {
		t.Fatal(err)
	}
	checkOffline()
	connect(domain.RunnerWorkspaceResponse{Status: http.StatusNotFound, Error: "workspace directory missing"})
	for _, id := range []string{parent.ID, child.ID} {
		checkInfo(id, false, true, "工作区已回收")
	}
}
