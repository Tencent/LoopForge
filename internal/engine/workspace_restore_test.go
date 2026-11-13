package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func awaitWorkspaceRestore(t *testing.T, e *Engine, id string) domain.WorkspaceRestoreStatus {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		state, err := e.WorkspaceRestoreStatus(id)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status != "restoring" {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background workspace restore did not settle")
	return domain.WorkspaceRestoreStatus{}
}

func TestBackgroundWorkspaceRestoreSharesLeaseAcrossAPIInstances(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			worker, controller, id := remoteWorkspaceRestoreFixture(t, backend)
			before, _ := worker.repo.WorkspaceCheckpoint(id)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var restores atomic.Int32
			serveWorkspaceRestoreWorker(t, worker, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				var request domain.RunnerWorkspaceRequest
				_ = json.Unmarshal(body, &request)
				if request.Operation == domain.RunnerWorkspaceOperationRestore {
					if restores.Add(1) == 1 {
						close(entered)
					}
					<-release
				}
				worker.HandleWorkspaceCheckpoint(w, r)
			}))
			t.Cleanup(func() { once.Do(func() { close(release) }); awaitWorkspaceRestore(t, controller, id) })
			state, err := controller.StartWorkspaceRestore(id)
			if err != nil || state.Status != "restoring" {
				t.Fatalf("restore not acknowledged promptly: %+v %v", state, err)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("restore RPC did not start")
			}
			other := New(worker.repo, runtime.NewManager(t.TempDir()))
			for i := 0; i < 3; i++ {
				state, err = other.StartWorkspaceRestore(id)
				if err != nil || state.Status != "restoring" || state.StartedAt == nil {
					t.Fatalf("other API did not reuse the operation: %+v %v", state, err)
				}
			}
			if _, err = other.BeginConversationWorkspaceUse(id); !errors.Is(err, repo.ErrWorkspaceInUse) {
				t.Fatalf("background restore lost its workspace lease: %v", err)
			}
			once.Do(func() { close(release) })
			if state = awaitWorkspaceRestore(t, other, id); state.Status != "ready" {
				t.Fatalf("restore failed: %+v", state)
			}
			after, _ := worker.repo.WorkspaceCheckpoint(id)
			run, _ := worker.repo.RunState(id)
			if restores.Load() != 1 || !run.WorkspaceCleanedAt.IsZero() || after.LeaseToken != "" || after.LeaseUntil != nil {
				t.Fatalf("restore was duplicated or not acknowledged: count=%d run=%+v meta=%+v", restores.Load(), run, after)
			}
			if !after.SavedAt.Equal(*before.SavedAt) || run.Status != "failed" || run.Nodes["prepare"].Attempt != 1 {
				t.Fatal("restoration resaved the archive or changed SOP execution")
			}
			if data, err := os.ReadFile(filepath.Join(worker.RunWorkspace(id), "README.md")); err != nil || string(data) != "saved changes" {
				t.Fatalf("restored files lost: %q %v", data, err)
			}
		})
	}
}

func TestRemoteWorkspaceProbeDoesNotRestoreOnBusyOrTransportFailure(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"busy", "legacy-busy", "offline", "invalid-directory"} {
		t.Run(scenario, func(t *testing.T) {
			worker, controller, id := remoteWorkspaceRestoreFixture(t, "sqlite")
			run, _ := worker.repo.RunState(id)
			run.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
			run.ExecutionContext.ExecutionRunnerID = "probe-runner"
			calls := 0
			controller.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
				calls++
				if req.Operation != domain.RunnerWorkspaceOperationCheckpointStatus && req.Operation != domain.RunnerWorkspaceOperationCheckpointExport {
					t.Fatalf("unexpected upload/restore on %s: %s", scenario, req.Operation)
				}
				switch scenario {
				case "offline":
					return domain.RunnerWorkspaceResponse{}, errors.New("runner disconnected")
				case "legacy-busy":
					if req.Operation == domain.RunnerWorkspaceOperationCheckpointStatus {
						return domain.RunnerWorkspaceResponse{Status: 400, Error: "unsupported operation"}, nil
					}
					return domain.RunnerWorkspaceResponse{Status: 409, Error: "workspace is still in use"}, nil
				case "invalid-directory":
					return domain.RunnerWorkspaceResponse{Status: 409, Error: "workspace is not a directory"}, nil
				default:
					return domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorBusy, Error: "workspace is still in use"}, nil
				}
			}
			err := controller.restoreOrReuseRemoteConversationCheckpoint(context.Background(), run)
			if err == nil || strings.Contains(scenario, "busy") && !errors.Is(err, repo.ErrWorkspaceInUse) {
				t.Fatalf("probe failure lost: %v", err)
			}
			if calls != 1 && (scenario != "legacy-busy" || calls != 2) {
				t.Fatalf("unexpected probe retries: %d", calls)
			}
		})
	}
}

func TestBackgroundWorkspaceRestoreFailureIsVisibleAndExplicitlyRetryable(t *testing.T) {
	t.Parallel()
	worker, controller, id := remoteWorkspaceRestoreFixture(t, "sqlite")
	run, _ := worker.repo.RunState(id)
	run.ExecutionContext.ExecutionMode, run.ExecutionContext.ExecutionRunnerID = domain.ExecutionModeRunner, "restore-runner"
	if err := worker.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	controller.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{}, errors.New("runner disconnected")
	}
	if _, err := controller.StartWorkspaceRestore(id); err != nil {
		t.Fatal(err)
	}
	state := awaitWorkspaceRestore(t, controller, id)
	if state.Status != "failed" || !strings.Contains(state.Error, "runner disconnected") {
		t.Fatalf("background error hidden: %+v", state)
	}
	meta, _ := worker.repo.WorkspaceCheckpoint(id)
	if meta.LeaseToken != "" || meta.Error != "" {
		t.Fatal("restore failure retained the lease or invalidated the saved snapshot")
	}
	controller.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		if req.Operation == domain.RunnerWorkspaceOperationCheckpointStatus {
			return domain.RunnerWorkspaceResponse{Status: 404, Code: domain.WorkspaceErrorMissing, Error: "missing"}, nil
		}
		if req.Operation != domain.RunnerWorkspaceOperationRestore {
			return domain.RunnerWorkspaceResponse{}, errors.New("unexpected restore request: " + req.Operation)
		}
		return domain.RunnerWorkspaceResponse{Status: 200}, nil
	}
	if _, err := controller.StartWorkspaceRestore(id); err != nil {
		t.Fatal(err)
	}
	if state = awaitWorkspaceRestore(t, controller, id); state.Status != "ready" {
		t.Fatalf("explicit retry failed: %+v", state)
	}
}

func TestCheckpointClosePreservesSharedWorkspaceScope(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	run, _ := e.repo.RunState(id)
	run.ExecutionContext.ExecutionMode, run.ExecutionContext.ExecutionRunnerID = domain.ExecutionModeRunner, "scope-runner"
	run.ExecutionContext.RunnerWorkspaceLayout = domain.RunnerWorkspaceLayoutShared
	called := false
	e.WorkspaceCheckpointTransport = func(ctx context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		called = true
		if req.WorkspaceLayout != domain.RunnerWorkspaceLayoutShared || req.Operation != domain.RunnerWorkspaceOperationCheckpointClose || ctx.Err() != nil {
			t.Fatalf("transfer close lost scope or inherited cancellation: %+v %v", req, ctx.Err())
		}
		return domain.RunnerWorkspaceResponse{Status: 200}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.closeCheckpointTransfer(ctx, run, "transfer")
	if !called {
		t.Fatal("transfer cleanup skipped")
	}
}

func TestBackgroundWorkspaceRestoreWaitsForOlderRunnerRPC(t *testing.T) {
	t.Parallel()
	worker, controller, id := remoteWorkspaceRestoreFixture(t, "sqlite")
	run, _ := worker.repo.RunState(id)
	run.ExecutionContext.ExecutionMode, run.ExecutionContext.ExecutionRunnerID = domain.ExecutionModeRunner, "older-rpc-runner"
	if err := worker.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	var busy atomic.Bool
	busy.Store(true)
	probed := make(chan struct{}, 1)
	controller.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		if req.Operation != domain.RunnerWorkspaceOperationCheckpointStatus {
			return domain.RunnerWorkspaceResponse{}, errors.New("must not re-upload while an older restore finishes")
		}
		if busy.Load() {
			select {
			case probed <- struct{}{}:
			default:
			}
			return domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorBusy, Error: "workspace is still in use"}, nil
		}
		return runtime.WorkspaceCheckpointStatus(worker.RunWorkspace(id)), nil
	}
	if _, err := controller.StartWorkspaceRestore(id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { busy.Store(false); awaitWorkspaceRestore(t, controller, id) })
	select {
	case <-probed:
	case <-time.After(5 * time.Second):
		t.Fatal("older RPC was not probed")
	}
	if err := os.MkdirAll(worker.RunWorkspace(id), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(worker.RunWorkspace(id), "newer.md")
	if err := os.WriteFile(path, []byte("surviving changes"), 0600); err != nil {
		t.Fatal(err)
	}
	busy.Store(false)
	if state := awaitWorkspaceRestore(t, controller, id); state.Status != "ready" {
		t.Fatalf("normal restore contention failed: %+v", state)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "surviving changes" {
		t.Fatalf("older RPC result overwritten: %q %v", data, err)
	}
}

func TestWorkspaceRestoreExpiredLeaseFencesOldCompletion(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			worker, controller, id := remoteWorkspaceRestoreFixture(t, backend)
			old, err := worker.repo.BeginWorkspaceRestore(id, time.Now().Add(-20*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if state, err := controller.WorkspaceRestoreStatus(id); err != nil || state.Status != "failed" {
				t.Fatalf("interrupted restore stayed busy forever: %+v %v", state, err)
			}
			current, err := worker.repo.BeginWorkspaceRestore(id, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.repo.FinishWorkspaceRestore(id, old, time.Now().UTC(), "late error"); !errors.Is(err, repo.ErrWorkspaceInUse) {
				t.Fatalf("old completion changed successor state: %v", err)
			}
			meta, _ := worker.repo.WorkspaceCheckpoint(id)
			if meta.LeaseToken != current || meta.RestoreStatus != "restoring" || meta.RestoreError != "" {
				t.Fatal("stale owner released a new restore lease")
			}
			if err := worker.repo.FinishWorkspaceRestore(id, current, time.Now().UTC(), "interrupted"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkspaceRestoreOutcomeOutranksClearedBinding(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"sqlite", "mysql"} {
		for _, scenario := range []string{"expired-after-binding", "failed-after-binding"} {
			t.Run(backend+"/"+scenario, func(t *testing.T) {
				worker, controller, id := remoteWorkspaceRestoreFixture(t, backend)
				now := time.Now().UTC()
				token, err := worker.repo.BeginWorkspaceRestore(id, now)
				if err != nil || token == "" {
					t.Fatalf("restore lease not acquired: %q %v", token, err)
				}
				// The restore binds the workspace, which clears the cleanup mark,
				// before the rest of its work has finished.
				if err = worker.repo.RestoreCheckpointWorkspaceBinding(id, token, worker.workspaceHost, worker.sandboxes.Root(), now); err != nil {
					t.Fatal(err)
				}
				wantError := repo.WorkspaceRestoreInterrupted
				if scenario == "expired-after-binding" {
					// The process exits before recording an outcome.
					if err = worker.repo.DB.Model(&repo.WorkspaceCheckpoint{}).Where("run_id = ?", id).
						Update("lease_until", now.Add(-time.Minute)).Error; err != nil {
						t.Fatal(err)
					}
				} else {
					wantError = "post-binding failure"
					if err = worker.repo.FinishWorkspaceRestore(id, token, now, wantError); err != nil {
						t.Fatal(err)
					}
				}
				if run, _ := worker.repo.RunState(id); !run.WorkspaceCleanedAt.IsZero() {
					t.Fatal("fixture binding did not clear the cleanup mark")
				}
				state, err := controller.WorkspaceRestoreStatus(id)
				if err != nil || state.Status != "failed" || state.Error != wantError {
					t.Fatalf("unfinished restore reported as %+v %v", state, err)
				}
				// The failure stays retryable although the workspace is bound.
				if state, err = controller.StartWorkspaceRestore(id); err != nil || state.Status == "failed" {
					t.Fatalf("retry was not started: %+v %v", state, err)
				}
				if state = awaitWorkspaceRestore(t, controller, id); state.Status != "ready" || state.Error != "" {
					t.Fatalf("retry did not finish the restore: %+v", state)
				}
				if meta, _ := worker.repo.WorkspaceCheckpoint(id); meta.RestoreStatus != "ready" || meta.LeaseToken != "" {
					t.Fatalf("retry left restore state %q lease=%q", meta.RestoreStatus, meta.LeaseToken)
				}
			})
		}
	}
}

func TestWorkspaceUseSupersedesEarlierRestoreOutcome(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"sqlite", "mysql"} {
		for _, scenario := range []string{"failed", "interrupted"} {
			t.Run(backend+"/"+scenario, func(t *testing.T) {
				worker, controller, id := remoteWorkspaceRestoreFixture(t, backend)
				now := time.Now().UTC()
				var old string
				var err error
				if scenario == "failed" {
					old, err = worker.repo.BeginWorkspaceRestore(id, now)
					if err == nil {
						err = worker.repo.FinishWorkspaceRestore(id, old, now, "runner disconnected")
					}
				} else {
					old, err = worker.repo.BeginWorkspaceRestore(id, now.Add(-20*time.Minute))
				}
				if err != nil {
					t.Fatal(err)
				}
				// An ordinary workspace use takes the lease and binds the workspace.
				token, err := worker.repo.BeginWorkspaceUse(id, now)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "interrupted" {
					meta, _ := worker.repo.WorkspaceCheckpoint(id)
					if meta.RestoreStatus != "failed" || meta.RestoreError != repo.WorkspaceRestoreInterrupted {
						t.Fatalf("taken-over restore not recorded as interrupted: %q %q", meta.RestoreStatus, meta.RestoreError)
					}
					if err = worker.repo.FinishWorkspaceRestore(id, old, now, ""); !errors.Is(err, repo.ErrWorkspaceInUse) {
						t.Fatalf("fenced restore recorded an outcome: %v", err)
					}
				}
				if err = worker.repo.RestoreCheckpointWorkspaceBinding(id, token, worker.workspaceHost, worker.sandboxes.Root(), now); err != nil {
					t.Fatal(err)
				}
				if state, err := controller.WorkspaceRestoreStatus(id); err != nil || state.Status != "ready" || state.Error != "" {
					t.Fatalf("bound workspace still reports the earlier restore: %+v %v", state, err)
				}
				if token, err = worker.repo.BeginWorkspaceRestore(id, now.Add(time.Hour)); err != nil || token != "" {
					t.Fatalf("ready workspace started another restore: %q %v", token, err)
				}
			})
		}
	}
}

func TestLegacyWorkspaceProbeConfirmsMissingRoot(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"runner-missing", "runner-file-missing", "worker-missing", "worker-file-missing"} {
		t.Run(scenario, func(t *testing.T) {
			worker, controller, id := remoteWorkspaceRestoreFixture(t, "sqlite")
			run, _ := worker.repo.RunState(id)
			if strings.HasPrefix(scenario, "runner-") {
				run.ExecutionContext.ExecutionMode, run.ExecutionContext.ExecutionRunnerID = domain.ExecutionModeRunner, "legacy-runner"
			}
			missingFile := strings.Contains(scenario, "file-missing")
			root := worker.RunWorkspace(id)
			missing := root
			if missingFile {
				missing = filepath.Join(root, "README.md")
			}
			restores := 0
			controller.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
				switch req.Operation {
				case domain.RunnerWorkspaceOperationCheckpointStatus:
					return domain.RunnerWorkspaceResponse{Status: 400, Error: "unsupported operation"}, nil
				case domain.RunnerWorkspaceOperationCheckpointExport:
					return domain.RunnerWorkspaceResponse{Status: 409, Error: "lstat " + missing + ": no such file or directory"}, nil
				case domain.RunnerWorkspaceOperationInfo:
					if missingFile {
						return domain.RunnerWorkspaceResponse{Status: 200}, nil
					}
					return domain.RunnerWorkspaceResponse{Status: 404, Error: "stat " + root + ": no such file or directory"}, nil
				case domain.RunnerWorkspaceOperationRestore:
					restores++
					return domain.RunnerWorkspaceResponse{Status: 200}, nil
				default:
					return domain.RunnerWorkspaceResponse{}, errors.New("unexpected legacy operation")
				}
			}
			if strings.HasPrefix(scenario, "worker-") {
				// Exercise a legacy Worker's HTTP response without Runner routing.
				serveWorkspaceRestoreWorker(t, worker, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req domain.RunnerWorkspaceRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					response, err := controller.WorkspaceCheckpointTransport(r.Context(), run, req)
					if err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					_ = json.NewEncoder(w).Encode(response)
				}))
			}
			err := controller.restoreOrReuseRemoteConversationCheckpoint(context.Background(), run)
			if missingFile && (err == nil || restores != 0) || !missingFile && (err != nil || restores != 1) {
				t.Fatalf("legacy probe confused root and file errors: restores=%d err=%v", restores, err)
			}
		})
	}
}
