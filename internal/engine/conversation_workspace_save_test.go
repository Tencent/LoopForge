package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestBackgroundWorkspaceSaveIsPreemptedByAnotherInstance(t *testing.T) {
	owner, id := checkpointFixture(t)
	run, _ := owner.repo.RunState(id)
	run.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
	run.ExecutionContext.ExecutionRunnerID = "test-remote"
	if err := owner.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	var exporting atomic.Bool
	transport := func(ctx context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		switch request.Operation {
		case domain.RunnerWorkspaceOperationCheckpointExport:
			exporting.Store(true)
			close(entered)
			<-ctx.Done()
			exporting.Store(false)
			return domain.RunnerWorkspaceResponse{}, ctx.Err()
		case domain.RunnerWorkspaceOperationCheckpointStatus:
			if exporting.Load() {
				return domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorBusy, Error: "workspace is still in use"}, nil
			}
			return domain.RunnerWorkspaceResponse{Status: 200}, nil
		default:
			return domain.RunnerWorkspaceResponse{}, errors.New("unexpected operation")
		}
	}
	owner.WorkspaceCheckpointTransport = transport
	other := New(owner.repo, owner.sandboxes)
	other.WorkspaceCheckpointTransport = transport
	use, err := owner.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	began := time.Now()
	done, err := owner.EndConversationWorkspaceUseAsync(ctx, id, use)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(began) > time.Second {
		t.Fatal("turn waited for snapshot")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background save did not start independently of cancellation")
	}
	began = time.Now()
	next, err := other.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal("next turn blocked behind snapshot", err)
	}
	if time.Since(began) > time.Second || exporting.Load() {
		t.Fatal("foreground did not promptly release remote gate")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("superseded save reported a failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("old save ignored preemption")
	}
	meta, _ := owner.repo.WorkspaceCheckpoint(id)
	if meta.LeaseToken != next || meta.Error != "" {
		t.Fatalf("superseded save damaged next lease: %+v", meta)
	}
	if err = owner.repo.FinishWorkspaceUse(id, next, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundWorkspaceSaveFailurePreservesFiles(t *testing.T) {
	e, id := checkpointFixture(t)
	if err := os.WriteFile(filepath.Join(e.RunWorkspace(id), ".git"), []byte("gitdir: /outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	use, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	done, err := e.EndConversationWorkspaceUseAsync(context.Background(), id, use)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil {
		t.Fatal("save failure lost")
	}
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	if meta.Error == "" || meta.SaveRetryAt == nil || meta.LeaseToken != "" {
		t.Fatalf("failure was not retriable: %+v", meta)
	}
	if _, err = os.Stat(filepath.Join(e.RunWorkspace(id), "README.md")); err != nil {
		t.Fatal("failure deleted original files", err)
	}
}

func TestInterruptedBackgroundSaveIsRetriedBeforeIdleCleanup(t *testing.T) {
	e, id := checkpointFixture(t)
	initial, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.EndConversationWorkspaceUse(context.Background(), id, initial); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(e.RunWorkspace(id), "README.md"), []byte("new edit after previous snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	use, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.repo.MarkWorkspaceFollowUp(id, use, now); err != nil {
		t.Fatal(err)
	}
	if _, err = e.repo.HandoffWorkspaceUseToSave(id, use, now); err != nil {
		t.Fatal(err)
	}
	// Simulate the owner exiting before archive work starts.
	if err = e.repo.DB.Model(&repo.WorkspaceCheckpoint{}).Where("run_id = ?", id).Update("lease_until", now.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	run, _ := e.repo.RunState(id)
	managed, claimed, _, err := e.claimIdleConversationWorkspace(run, now.Add(time.Second))
	if err != nil || !managed || claimed {
		t.Fatal("pending save was not retried safely", managed, claimed, err)
	}
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	if meta.SavedAt == nil || meta.SaveRetryAt != nil || meta.LeaseToken != "" {
		t.Fatalf("pending save not recovered: %+v", meta)
	}
	archive, err := e.repo.WorkspaceCheckpointArchive(id)
	if err != nil || runtime.ValidateWorkspaceCheckpoint(archive) != nil {
		t.Fatal("recovered archive invalid", err)
	}
	root := filepath.Join(t.TempDir(), "restored")
	if err = runtime.RestoreWorkspaceCheckpoint(root, archive); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(content) != "new edit after previous snapshot" {
		t.Fatalf("pending save reused stale archive: %q %v", content, err)
	}
}
