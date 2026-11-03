package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func remoteWorkspaceRestoreFixture(t *testing.T, backend string) (*Engine, *Engine, string) {
	t.Helper()
	var store *repo.Store
	var err error
	if backend == "mysql" {
		dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("isolated local Docker MySQL required")
		}
		store, err = repo.Open(dsn)
	} else {
		store, err = repo.OpenTest()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	id := fmt.Sprintf("restore-retry-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		store.DB.Table("runs").Where("id = ?", id).Update("status", "cancelled")
		if err := store.DeleteRun(id); err != nil {
			t.Error(err)
		}
	})
	worker := New(store, runtime.NewManager(t.TempDir()))
	worker.ConfigureExecution("restore-worker-"+id, "restore-worker-"+id, time.Minute, false)
	worker.controlToken = "restore-retry-control"
	t.Cleanup(worker.checkpointTransfers.Close)
	workflow := domain.WorkflowDefinition{ID: id, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "prepare", Type: "command"}, {ID: "close", Type: "agent", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{Source: "prepare", Target: "close", When: "success"}}}
	run := domain.Run{ID: id, WorkflowID: id, WorkflowSnapshot: workflow, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeCloud},
		Nodes: map[string]*domain.NodeRun{
			"prepare": {NodeID: "prepare", Status: "succeeded", Attempt: 1, Output: "upstream result"},
			"close":   {NodeID: "close", Status: "succeeded", Attempt: 1},
		},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Table("runs").Where("id = ?", id).Updates(map[string]any{
		"workspace_host": worker.workspaceHost, "workspace_root": worker.sandboxes.Root(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	root := worker.RunWorkspace(id)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("saved changes"), 0600); err != nil {
		t.Fatal(err)
	}
	worker.reconcileTerminalWorkspaceCleanup()
	run, _ = store.RunState(id)
	if run.WorkspaceCleanedAt.IsZero() {
		t.Fatal("fixture workspace was not reclaimed")
	}
	run.Status = "failed"
	run.Nodes["close"].Status = "failed"
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	controller := New(store, runtime.NewManager(t.TempDir()))
	controller.ConfigureExecution("restore-api-"+id, "restore-api-"+id, time.Minute, false)
	controller.controlToken = worker.controlToken
	return worker, controller, id
}

func serveWorkspaceRestoreWorker(t *testing.T, worker *Engine, handler http.Handler) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	if err := worker.repo.HeartbeatEngineInstance(repo.EngineInstance{
		InstanceID: worker.workspaceHost, Role: "worker", WorkspaceHost: worker.workspaceHost, ControlURL: server.URL,
	}, time.Minute); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		worker.repo.DB.Table("engine_instances").Where("instance_id = ?", worker.workspaceHost).Delete(nil)
	})
}

func TestConversationWorkspaceRetryAfterLostRestoreReply(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			worker, controller, id := remoteWorkspaceRestoreFixture(t, backend)
			var lostReply atomic.Bool
			serveWorkspaceRestoreWorker(t, worker, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					http.Error(w, err.Error(), 500)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				var request domain.RunnerWorkspaceRequest
				if err := json.Unmarshal(body, &request); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), 400)
					return
				}
				if request.Operation == domain.RunnerWorkspaceOperationRestore && lostReply.CompareAndSwap(false, true) {
					recorder := httptest.NewRecorder()
					worker.HandleWorkspaceCheckpoint(recorder, r)
					var response domain.RunnerWorkspaceResponse
					if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || recorder.Code != 200 || response.Error != "" {
						t.Errorf("worker restore failed: %d %s", recorder.Code, recorder.Body.String())
					}
					http.Error(w, "lost restore reply", http.StatusBadGateway)
					return
				}
				worker.HandleWorkspaceCheckpoint(w, r)
			}))
			if err := controller.RestoreConversationWorkspaceIfNeeded(context.Background(), id); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
				t.Fatalf("first restore should lose its reply: %v", err)
			}
			run, _ := worker.repo.RunState(id)
			if run.WorkspaceCleanedAt.IsZero() {
				t.Fatal("cleanup marker cleared without acknowledgement")
			}
			root := worker.RunWorkspace(id)
			if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "newer.md"), []byte("newer surviving changes"), 0600); err != nil {
				t.Fatal(err)
			}
			// Repeat the HTTP preflight and then the actual retry, including context.
			if err := controller.RestoreConversationWorkspaceIfNeeded(context.Background(), id); err != nil {
				t.Fatalf("retry could not reuse the restored workspace: %v", err)
			}
			if err := controller.RetryContext(context.Background(), id, "", "继续", "保留代码，重新核验收尾"); err != nil {
				t.Fatal(err)
			}
			run, _ = worker.repo.RunState(id)
			if run.Status != "running" || !run.WorkspaceCleanedAt.IsZero() || run.WorkspaceHost != worker.workspaceHost || run.WorkspaceRoot != worker.sandboxes.Root() {
				t.Fatalf("retry lost workspace binding: %+v", run)
			}
			if run.Nodes["prepare"].Status != "succeeded" || run.Nodes["prepare"].Attempt != 1 || run.Nodes["prepare"].Output != "upstream result" || run.Nodes["close"].Status != "pending" || !strings.Contains(run.Nodes["close"].Feedback, "保留代码，重新核验收尾") {
				t.Fatalf("retry lost upstream results or human context: %+v", run.Nodes)
			}
			if _, err := os.Stat(filepath.Join(root, "README.md")); !os.IsNotExist(err) {
				t.Fatal("retry resurrected an intentionally deleted file")
			}
			if data, err := os.ReadFile(filepath.Join(root, "newer.md")); err != nil || string(data) != "newer surviving changes" {
				t.Fatalf("retry overwrote surviving changes: %q %v", data, err)
			}
		})
	}
}

func TestConversationWorkspaceRunnerReusesSurvivingDirectory(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"no-cleanup", "queued-cleanup", "completed-cleanup", "leased-cleanup", "running-cleanup", "corrupt-snapshot", "legacy-runner", "legacy-chunked-runner"} {
		t.Run(scenario, func(t *testing.T) {
			worker, controller, id := remoteWorkspaceRestoreFixture(t, "sqlite")
			run, _ := worker.repo.RunState(id)
			run.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
			run.ExecutionContext.ExecutionRunnerID = "restore-runner"
			if err := worker.repo.PutRun(run); err != nil {
				t.Fatal(err)
			}
			cleanupStatus := map[string]string{"queued-cleanup": domain.RunnerTaskQueued, "completed-cleanup": domain.RunnerTaskSuccess, "leased-cleanup": domain.RunnerTaskLeased, "running-cleanup": domain.RunnerTaskRunning}[scenario]
			if cleanupStatus != "" {
				if err := worker.repo.PutRunnerTask(domain.RunnerTask{ID: "rt-cleanup-" + id, RunnerID: "restore-runner", RunID: id, Provider: "terminal", Action: domain.RunnerTaskActionCleanupRun, Status: cleanupStatus}); err != nil {
					t.Fatal(err)
				}
			}
			root := worker.RunWorkspace(id)
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "newer.md"), []byte("surviving changes"), 0600); err != nil {
				t.Fatal(err)
			}
			if scenario == "corrupt-snapshot" {
				if err := worker.repo.DB.Model(&repo.WorkspaceCheckpoint{}).Where("run_id = ?", id).Update("sha256", "invalid").Error; err != nil {
					t.Fatal(err)
				}
			}
			calls, restores, exports, closes := 0, 0, 0, 0
			controller.WorkspaceCheckpointTransport = func(ctx context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
				calls++
				if request.Operation == domain.RunnerWorkspaceOperationCheckpointStatus {
					if strings.HasPrefix(scenario, "legacy-") {
						return domain.RunnerWorkspaceResponse{Status: 400, Error: "unsupported operation"}, nil
					}
					return runtime.WorkspaceCheckpointStatus(root), nil
				}
				if scenario == "legacy-runner" && request.Operation == domain.RunnerWorkspaceOperationCheckpointExport {
					return domain.RunnerWorkspaceResponse{Status: 400, Error: "unsupported operation"}, nil
				}
				if request.Operation == domain.RunnerWorkspaceOperationCheckpointClose {
					closes++
				}
				if domain.IsWorkspaceCheckpointTransferOperation(request.Operation) {
					response := worker.checkpointTransfers.Handle(ctx, root, request)
					if request.Operation == domain.RunnerWorkspaceOperationCheckpointExport && response.Error == "" {
						exports++
					}
					return response, nil
				}
				if request.Operation == domain.RunnerWorkspaceOperationCheckpoint {
					archive, err := runtime.CreateWorkspaceCheckpoint(root)
					if err != nil {
						return domain.RunnerWorkspaceResponse{Error: err.Error()}, nil
					}
					payload, _ := json.Marshal(map[string][]byte{"checkpoint": archive})
					return domain.RunnerWorkspaceResponse{Payload: payload}, nil
				}
				restores++
				return domain.RunnerWorkspaceResponse{Error: "unexpected restore"}, nil
			}
			err := controller.RestoreConversationWorkspaceIfNeeded(context.Background(), id)
			blocked := scenario == "leased-cleanup" || scenario == "running-cleanup"
			if blocked || scenario == "corrupt-snapshot" {
				if err == nil || blocked && !strings.Contains(err.Error(), "尚在回收") || !blocked && !strings.Contains(err.Error(), "校验失败") {
					t.Fatalf("unsafe workspace was reused: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			got, _ := worker.repo.RunState(id)
			if got.WorkspaceCleanedAt.IsZero() != (err == nil) || restores != 0 || blocked && calls != 0 {
				t.Fatalf("cleanup marker or restore calls invalid: marker=%v calls=%d restores=%d", got.WorkspaceCleanedAt, calls, restores)
			}
			if !blocked && (closes != exports || scenario == "legacy-chunked-runner" && exports == 0 || !strings.HasPrefix(scenario, "legacy-") && exports != 0) {
				t.Fatalf("checkpoint probe transfer leaked: exports=%d closes=%d", exports, closes)
			}
			if scenario == "queued-cleanup" {
				cleanup, _ := worker.repo.RunnerTask("rt-cleanup-" + id)
				if cleanup.Status != domain.RunnerTaskCanceled {
					t.Fatal("queued cleanup was not canceled")
				}
			}
			if data, err := os.ReadFile(filepath.Join(root, "newer.md")); err != nil || string(data) != "surviving changes" {
				t.Fatalf("surviving changes lost: %q %v", data, err)
			}
		})
	}
}
