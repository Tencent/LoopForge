package repo

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestSharedWorkspaceBlocksCleanupAndNewUseAfterClaim(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	owner := domain.Run{ID: "owner", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	child := domain.Run{ID: "child", Status: "running", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: owner.ID}, Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(owner); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(child); err != nil {
		t.Fatal(err)
	}
	// Even legacy data with an inconsistent owner marker must not let the
	// orphan sweep remove a workspace still referenced by a live child.
	if err := s.DB.Model(&runRow{}).Where("id = ?", owner.ID).Update("workspace_cleaned_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	live, err := s.LiveSandboxRunIDsChecked()
	if err != nil || !slices.Contains(live, owner.ID) {
		t.Fatalf("shared owner missing from live set: %v, %v", live, err)
	}
	if err := s.ResetRunWorkspaceCleanup(owner.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, status := range []string{"running", "waiting", "failed"} {
		child.Status = status
		if err := s.PutRun(child); err != nil {
			t.Fatal(err)
		}
		if claimed, err := s.ClaimRunWorkspaceCleanup(owner.ID, now); err != nil || claimed {
			t.Fatalf("cleanup with %s child: claimed=%v err=%v", status, claimed, err)
		}
	}
	child.Status = "succeeded"
	if err := s.PutRun(child); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimRunWorkspaceCleanup(owner.ID, now); err != nil || !claimed {
		t.Fatalf("cleanup after child completion: claimed=%v err=%v", claimed, err)
	}
	child.Status = "running"
	if err := s.PutRun(child); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("retry after cleanup claim: %v", err)
	}
	child.ID = "late-child"
	if err := s.PutRun(child); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("new child after cleanup claim: %v", err)
	}
}

func TestSharedWorkspaceBlocksCheckpointAndExplicitDeletion(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	owner := domain.Run{ID: "owner", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(owner); err != nil {
		t.Fatal(err)
	}
	owner, _ = s.RunState(owner.ID)
	token, err := s.BeginWorkspaceUse(owner.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishWorkspaceUse(owner.ID, token, now, []byte("snapshot"), ""); err != nil {
		t.Fatal(err)
	}
	token, err = s.BeginWorkspaceCheckpointSave(owner.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	child := domain.Run{ID: "child", Status: "failed", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: owner.ID}, Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(child); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimCheckpointWorkspaceCleanup(owner.ID, token, owner.Revision, now.Add(time.Minute)); err != nil || claimed {
		t.Fatalf("checkpoint cleanup with retryable child: claimed=%v err=%v", claimed, err)
	}
	if err := s.ClaimWorkspaceDeletion(owner.ID, token, owner.Revision, now.Add(time.Minute)); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("explicit deletion with retryable child: %v", err)
	}
}

// This exercises the production MySQL row locks, including the insert/cleanup
// race between different service instances. It requires a disposable database.
func TestMySQLSharedWorkspaceAdmissionRacesCleanup(t *testing.T) {
	stores, prefix := admissionMySQLStores(t)
	var ids []string
	for i := 0; i < 12; i++ {
		ids = append(ids, fmt.Sprintf("%s-owner-%d", prefix, i), fmt.Sprintf("%s-child-%d", prefix, i))
	}
	admissionMySQLCleanup(t, stores[0], ids)
	for i := 0; i < 12; i++ {
		ownerID := fmt.Sprintf("%s-owner-%d", prefix, i)
		childID := fmt.Sprintf("%s-child-%d", prefix, i)
		owner := domain.Run{ID: ownerID, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
		child := domain.Run{ID: childID, Status: "running", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: ownerID}, Nodes: map[string]*domain.NodeRun{}}
		if err := stores[0].PutRun(owner); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		cleanup := make(chan struct {
			claimed bool
			err     error
		}, 1)
		write := make(chan error, 1)
		go func() {
			<-start
			claimed, err := stores[0].ClaimRunWorkspaceCleanup(ownerID, time.Now().UTC())
			cleanup <- struct {
				claimed bool
				err     error
			}{claimed, err}
		}()
		go func() { <-start; write <- stores[1].PutRun(child) }()
		close(start)
		result, writeErr := <-cleanup, <-write
		if result.err != nil || writeErr != nil && !errors.Is(writeErr, ErrWorkspaceInUse) {
			t.Fatalf("cleanup=%v, child=%v", result.err, writeErr)
		}
		if result.claimed == (writeErr == nil) {
			t.Fatalf("cleanup=%v, child=%v; exactly one operation must win", result.claimed, writeErr)
		}
	}
}

func TestChildAdmissionRejectsStaleParentLeaseAndAttempt(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	parent := domain.Run{ID: "guard-parent", Status: "running", Nodes: map[string]*domain.NodeRun{"sop": {NodeID: "sop", Status: "running", Attempt: 2}}}
	if err := s.PutRun(parent); err != nil {
		t.Fatal(err)
	}
	if err := s.TryAcquireRunLease(parent.ID, "worker", 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	parent, _ = s.RunState(parent.ID)
	child := domain.Run{ID: "guard-child", Status: "running", ExecutionContext: domain.TelemetryContext{ParentRunID: parent.ID, ParentNodeID: "sop", WorkspaceRunID: parent.ID}, Nodes: map[string]*domain.NodeRun{}}
	for _, tc := range []struct {
		worker              string
		generation, attempt int
	}{{"old-worker", parent.Generation, 2}, {"worker", parent.Generation + 1, 2}, {"worker", parent.Generation, 1}} {
		if err := s.PutChildRun(child, parent.ID, "sop", tc.worker, tc.generation, tc.attempt); !errors.Is(err, ErrRunLeaseLost) {
			t.Fatalf("stale guard %+v: %v", tc, err)
		}
		if _, exists := s.RunState(child.ID); exists {
			t.Fatal("stale guard created child")
		}
	}
	if err := s.PutChildRun(child, parent.ID, "sop", "worker", parent.Generation, 2); err != nil {
		t.Fatalf("current guard rejected: %v", err)
	}
	updated, _ := s.RunState(parent.ID)
	if updated.Nodes["sop"].ChildRunID != child.ID {
		t.Fatal("child reference was not stored with child admission")
	}
	child.ID = "duplicate-child"
	if err := s.PutChildRun(child, parent.ID, "sop", "worker", parent.Generation, 2); !errors.Is(err, ErrRunDecisionStale) {
		t.Fatalf("duplicate child accepted: %v", err)
	}
}
