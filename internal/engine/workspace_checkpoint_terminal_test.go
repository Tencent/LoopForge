package engine

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// checkpointTerminalResponses are what current and older runners send while a
// workspace terminal is open during a checkpoint operation.
var checkpointTerminalResponses = map[string]domain.RunnerWorkspaceResponse{
	"coded":  {Status: 409, Code: domain.WorkspaceErrorTerminalActive, Error: "workspace terminal is still active"},
	"legacy": {Status: 409, Error: "workspace terminal is still active"},
}

func requireTerminalActive(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errWorkspaceTerminalActive) || errors.Is(err, repo.ErrWorkspaceInUse) {
		t.Fatalf("open terminal must fail fast instead of waiting as lease contention: %v", err)
	}
}

func TestWorkspaceCheckpointResponseErrorSeparatesTerminalFromContention(t *testing.T) {
	for name, response := range checkpointTerminalResponses {
		t.Run(name, func(t *testing.T) {
			requireTerminalActive(t, workspaceCheckpointResponseError(response))
		})
	}
	for _, busy := range []domain.RunnerWorkspaceResponse{
		{Status: 409, Code: domain.WorkspaceErrorBusy, Error: "workspace is still in use"},
		{Status: 409, Error: "workspace is still in use"},
	} {
		if err := workspaceCheckpointResponseError(busy); !errors.Is(err, repo.ErrWorkspaceInUse) {
			t.Fatalf("lease contention must stay retryable: %+v -> %v", busy, err)
		}
	}
	if err := workspaceCheckpointResponseError(domain.RunnerWorkspaceResponse{Status: 404, Code: domain.WorkspaceErrorMissing, Error: "missing"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing workspace classification changed: %v", err)
	}
}

// terminalRestoreFixture is a reclaimed runner workspace whose runner reports
// an open terminal for the operations named in terminalOps.
func terminalRestoreFixture(t *testing.T, response domain.RunnerWorkspaceResponse, terminalOps ...string) (*Engine, domain.Run, *atomic.Int32) {
	t.Helper()
	worker, controller, id := remoteWorkspaceRestoreFixture(t, "sqlite")
	run, _ := worker.repo.RunState(id)
	run.ExecutionContext.ExecutionMode, run.ExecutionContext.ExecutionRunnerID = domain.ExecutionModeRunner, "terminal-runner"
	if err := worker.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	run, _ = worker.repo.RunState(id)
	var terminalCalls atomic.Int32
	controller.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		for _, op := range terminalOps {
			if req.Operation == op {
				terminalCalls.Add(1)
				return response, nil
			}
		}
		switch req.Operation {
		case domain.RunnerWorkspaceOperationCheckpointStatus:
			return domain.RunnerWorkspaceResponse{Status: 404, Code: domain.WorkspaceErrorMissing, Error: "missing"}, nil
		case domain.RunnerWorkspaceOperationCheckpointClose:
			return domain.RunnerWorkspaceResponse{Status: 200}, nil
		}
		return domain.RunnerWorkspaceResponse{}, errors.New("unexpected operation " + req.Operation)
	}
	return controller, run, &terminalCalls
}

func TestCheckpointExportFailsFastWhileTerminalIsOpen(t *testing.T) {
	t.Parallel()
	for name, response := range checkpointTerminalResponses {
		t.Run(name, func(t *testing.T) {
			e, run, calls := terminalRestoreFixture(t, response, domain.RunnerWorkspaceOperationCheckpointExport)
			requireTerminalActive(t, e.saveConversationWorkspace(context.Background(), run, ""))
			if calls.Load() != 1 {
				t.Fatalf("export retried an open terminal %d times", calls.Load())
			}
		})
	}
}

func TestCheckpointProbeFailsFastWhileTerminalIsOpen(t *testing.T) {
	t.Parallel()
	for name, response := range checkpointTerminalResponses {
		t.Run(name, func(t *testing.T) {
			// Runners without the cheap status probe answer with an export.
			e, run, calls := terminalRestoreFixture(t, response, domain.RunnerWorkspaceOperationCheckpointExport)
			base := e.WorkspaceCheckpointTransport
			restores := 0
			e.WorkspaceCheckpointTransport = func(ctx context.Context, r domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
				switch req.Operation {
				case domain.RunnerWorkspaceOperationCheckpointStatus:
					return domain.RunnerWorkspaceResponse{Status: 400, Error: "unsupported operation"}, nil
				case domain.RunnerWorkspaceOperationRestore:
					restores++
				}
				return base(ctx, r, req)
			}
			requireTerminalActive(t, e.restoreOrReuseRemoteConversationCheckpoint(context.Background(), run))
			if calls.Load() != 1 || restores != 0 {
				t.Fatalf("probe retried or restored over an open terminal: probes=%d restores=%d", calls.Load(), restores)
			}
		})
	}
}

func TestCheckpointRestoreFailsFastWhileTerminalIsOpen(t *testing.T) {
	t.Parallel()
	for name, response := range checkpointTerminalResponses {
		t.Run(name+"/execution", func(t *testing.T) {
			e, run, calls := terminalRestoreFixture(t, response, domain.RunnerWorkspaceOperationRestore)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			busy := false
			err := e.restoreCheckpointForExecution(ctx, run, func() { busy = true })
			requireTerminalActive(t, err)
			if busy || calls.Load() != 1 {
				t.Fatalf("node start waited on an open terminal: busy=%v restores=%d", busy, calls.Load())
			}
		})
		t.Run(name+"/background", func(t *testing.T) {
			e, run, calls := terminalRestoreFixture(t, response, domain.RunnerWorkspaceOperationRestore)
			if _, err := e.StartWorkspaceRestore(run.ID); err != nil {
				t.Fatal(err)
			}
			state := awaitWorkspaceRestore(t, e, run.ID)
			if state.Status != "failed" || !strings.Contains(state.Error, errWorkspaceTerminalActive.Error()) || calls.Load() != 1 {
				t.Fatalf("background restore hid or retried an open terminal: %+v restores=%d", state, calls.Load())
			}
			if meta, _ := e.repo.WorkspaceCheckpoint(run.ID); meta.LeaseToken != "" {
				t.Fatal("failed restore kept its workspace lease")
			}
		})
	}
}
