package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestClaimCheckpointWorkspaceCleanupRequiresIdleHourForSOP(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		status            string
		updated, lastUsed time.Time
	}{
		{"failed", now.Add(-time.Hour), now.Add(-2 * time.Hour)},
		{"waiting", now.Add(-2 * time.Hour), now.Add(-time.Hour)},
	} {
		t.Run(tc.status, func(t *testing.T) {
			row := runRow{ID: tc.status, Status: tc.status, Revision: 3, Updated: &tc.updated}
			if err := s.DB.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.ReleaseWorkspace(row.ID, tc.lastUsed); err != nil {
				t.Fatal(err)
			}
			token, err := s.BeginWorkspaceCheckpointSave(row.ID, now.Add(-time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SaveWorkspaceCheckpoint(row.ID, token, now, []byte("snapshot")); err != nil {
				t.Fatal(err)
			}
			if claimed, err := s.ClaimCheckpointWorkspaceCleanup(row.ID, token, row.Revision, now.Add(-time.Second)); err != nil || claimed {
				t.Fatalf("claim before hour = %v, %v", claimed, err)
			}
			if claimed, err := s.ClaimCheckpointWorkspaceCleanup(row.ID, token, row.Revision, now); err != nil || !claimed {
				t.Fatalf("claim at hour = %v, %v", claimed, err)
			}
		})
	}
}

func TestIdleReclaimableSOPRunsIncludesLegacyTopLevelRuns(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	now := time.Now().UTC()
	cutoff := now.Add(-time.Hour)
	later := now.Add(time.Hour)
	child := jsonBytes(domain.TelemetryContext{ParentRunID: "legacy"})
	for _, row := range []runRow{
		{ID: "legacy", Status: "failed", Updated: &cutoff},
		{ID: "paused", Status: "waiting", PauseRequested: true, Updated: &cutoff},
		{ID: "fresh", Status: "failed", Updated: &now},
		{ID: "delayed", Status: "waiting", Updated: &cutoff, CleanupNotBefore: &later},
		{ID: "child", Status: "failed", Updated: &cutoff, ExecutionContext: child},
		{ID: "conversation", Kind: domain.RunKindWorkspace, Status: "waiting", Updated: &cutoff},
		{ID: "legacy-group", WorkflowID: domain.FreeCollaborationWorkflowID, Status: "waiting", Updated: &cutoff},
		{ID: "running", Status: "running", Updated: &cutoff},
		{ID: "cleaned", Status: "failed", Updated: &cutoff, WorkspaceCleaned: &now},
	} {
		if err := s.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]bool{}
	for _, run := range s.IdleReclaimableSOPRuns(cutoff) {
		got[run.ID] = true
	}
	if len(got) != 2 || !got["legacy"] || !got["paused"] {
		t.Fatalf("idle SOPs = %v", got)
	}
	cleaned := s.CleanedSOPRunsHoldingAdmission()
	if len(cleaned) != 1 || cleaned[0].ID != "cleaned" {
		t.Fatalf("cleaned SOPs holding slots = %+v", cleaned)
	}
}

// A reaped turn releases only the lease it recorded, and only once nothing
// renews it any more. A live holder on any replica, a newer turn's lease and a
// background save all keep theirs.
func TestReleaseAbandonedWorkspaceUseFreesOnlyTheReapedTurnsLease(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	taken := now.Add(-2 * time.Minute)
	for _, id := range []string{"abandoned-use", "renewed-use", "replaced-use", "background-save"} {
		if err = s.PutRun(domain.Run{ID: id, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}); err != nil {
			t.Fatal(err)
		}
	}
	release := func(runID, token string) bool {
		t.Helper()
		released, err := s.ReleaseAbandonedWorkspaceUse(runID, token, now)
		if err != nil {
			t.Fatal(err)
		}
		return released
	}
	leaseOf := func(runID string) string {
		t.Helper()
		item, _ := s.WorkspaceCheckpoint(runID)
		return item.LeaseToken
	}

	abandoned, err := s.BeginWorkspaceUse("abandoned-use", taken)
	if err != nil {
		t.Fatal(err)
	}
	if !release("abandoned-use", abandoned) {
		t.Fatal("an unrenewed lease of a reaped turn was kept")
	}
	if release("abandoned-use", abandoned) {
		t.Fatal("a lease was released twice")
	}
	if _, err = s.BeginWorkspaceUse("abandoned-use", now); err != nil {
		t.Fatal("abandoned foreground lease still blocked the next turn", err)
	}

	// Another replica still executing the turn renews its lease.
	renewed, err := s.BeginWorkspaceUse("renewed-use", taken)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RenewWorkspaceUse("renewed-use", renewed, now.Add(-10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if release("renewed-use", renewed) || leaseOf("renewed-use") != renewed {
		t.Fatal("a lease its holder still renews was released")
	}

	// The reaped turn's lease ended and a newer turn took the workspace
	// before the reader released: the stale token must not free it.
	old, err := s.BeginWorkspaceUse("replaced-use", taken)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishWorkspaceUse("replaced-use", old, taken, nil, ""); err != nil {
		t.Fatal(err)
	}
	newer, err := s.BeginWorkspaceUse("replaced-use", taken)
	if err != nil {
		t.Fatal(err)
	}
	if release("replaced-use", old) || leaseOf("replaced-use") != newer {
		t.Fatal("a newer turn's lease was released with the reaped turn's token")
	}

	token, err := s.BeginWorkspaceUse("background-save", taken)
	if err != nil {
		t.Fatal(err)
	}
	save, err := s.HandoffWorkspaceUseToSave("background-save", token, taken)
	if err != nil {
		t.Fatal(err)
	}
	if release("background-save", token) || release("background-save", save) || leaseOf("background-save") != save {
		t.Fatal("background save lease dropped")
	}
	if release("", abandoned) || release("missing-run", abandoned) {
		t.Fatal("released a lease without a run")
	}
}

func TestWorkspaceCheckpointLeaseCASAndDurableIdleDeadline(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	r := domain.Run{ID: "checkpoint", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err = s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	r, _ = s.RunState(r.ID)
	token, err := s.BeginWorkspaceUse(r.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginWorkspaceUse(r.ID, now); err != ErrWorkspaceInUse {
		t.Fatal("concurrent lease allowed", err)
	}
	if err = s.RenewWorkspaceUse(r.ID, "wrong", now); err != ErrWorkspaceInUse {
		t.Fatal("foreign token renewed")
	}
	if claimed, _ := s.ClaimCheckpointWorkspaceCleanup(r.ID, token, r.Revision, now); claimed {
		t.Fatal("unsaved files reclaimed")
	}
	if err = s.MarkWorkspaceFollowUp(r.ID, token, now); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishWorkspaceUse(r.ID, token, now, []byte("test snapshot"), ""); err != nil {
		t.Fatal(err)
	}
	before, _ := s.WorkspaceCheckpoint(r.ID)
	token, err = s.BeginWorkspaceCheckpointSave(r.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.WorkspaceCheckpoint(r.ID)
	if !after.LastUsedAt.Equal(before.LastUsedAt) {
		t.Fatal("maintenance changed idle deadline")
	}
	if claimed, err := s.ClaimCheckpointWorkspaceCleanup(r.ID, token, r.Revision+1, now.Add(time.Minute)); err != nil || claimed {
		t.Fatal("stale revision reclaimed", err)
	}
	if err = s.SaveWorkspaceCheckpoint(r.ID, token, now, []byte{}); err == nil {
		t.Fatal("empty snapshot accepted")
	}
	if claimed, err := s.ClaimCheckpointWorkspaceCleanup(r.ID, token, r.Revision, now.Add(time.Minute)); err != nil || claimed {
		t.Fatal("recent activity reclaimed", err)
	}
	if err = s.RenewWorkspaceUse(r.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimCheckpointWorkspaceCleanup(r.ID, token, r.Revision, now.Add(time.Hour)); err != nil || !claimed {
		t.Fatal("saved idle files not reclaimed", err)
	}
	if err = s.RestoreCheckpointWorkspaceBinding(r.ID, token, "host", "root", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	restored, _ := s.RunState(r.ID)
	if restored.Status != "succeeded" || !restored.WorkspaceCleanedAt.IsZero() {
		t.Fatal("restore altered SOP outcome")
	}
	if err = s.FinishWorkspaceUse(r.ID, token, now, nil, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(before)
	if strings.Contains(string(raw), "test snapshot") || strings.Contains(string(raw), before.SHA256) || strings.Contains(string(raw), "leaseToken") {
		t.Fatal("private archive/lease leaked", string(raw))
	}
	if err = s.DeleteRun(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.WorkspaceCheckpoint(r.ID); ok {
		t.Fatal("deleted run left snapshot")
	}
}

func TestNodeRewindFencesWorkspaceAndRunResumption(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := domain.Run{ID: "node-rewind", Status: "running", Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "running"}}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginNodeRewind(r.ID, time.Now().UTC()); err == nil {
		t.Fatal("running run was accepted")
	}
	r, _ = s.RunState(r.ID)
	r.Status, r.Nodes["n"].Status = "failed", "failed"
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	token, err := s.BeginNodeRewind(r.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginWorkspaceUse(r.ID, time.Now().UTC()); err != ErrWorkspaceInUse {
		t.Fatalf("workspace lease: %v", err)
	}
	if err := s.TryAcquireRunLease(r.ID, "worker", r.Generation, time.Minute); err != ErrWorkspaceInUse {
		t.Fatalf("run lease: %v", err)
	}
	r, _ = s.RunState(r.ID)
	r.Status = "running"
	if err := s.PutRun(r); err != ErrWorkspaceInUse {
		t.Fatalf("resumption: %v", err)
	}
	if err := s.FinishWorkspaceUse(r.ID, token, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(r); err != nil {
		t.Fatalf("resume after release: %v", err)
	}
}

func TestInterruptedNodeRewindKeepsDurableFence(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := domain.Run{ID: "interrupted-rewind", Status: "failed", Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "failed"}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token, err := s.BeginNodeRewind(run.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeRewindPending(run.ID, token, now); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishWorkspaceUse(run.ID, token, now, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.TryAcquireRunLease(run.ID, "worker", run.Generation, time.Minute); err != ErrWorkspaceInUse {
		t.Fatalf("interrupted rewind allowed run lease: %v", err)
	}
	if _, err := s.BeginWorkspaceUse(run.ID, now); err == nil {
		t.Fatal("interrupted rewind allowed workspace use")
	}
	token, err = s.BeginNodeRewind(run.ID, now.Add(time.Second))
	if err != nil {
		t.Fatalf("retry rewind: %v", err)
	}
	if err := s.CompleteNodeRewind(run.ID, token, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishWorkspaceUse(run.ID, token, now.Add(time.Second), nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginWorkspaceUse(run.ID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("workspace remains fenced: %v", err)
	}
}

func TestOrphanAutomaticRestoreIsParkedForRecovery(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := domain.Run{ID: "orphan-restore", Status: "running", Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "running", Attempt: 2}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token, err := s.BeginWorkspaceUse(run.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeCheckpointRestorePending(run.ID, run.ID, token, now); err != nil {
		t.Fatal(err)
	}
	if parked, err := s.ParkOrphanNodeCheckpointRestore(run.ID, run.ID, now.Add(time.Minute)); err != nil || parked {
		t.Fatalf("live workspace lease: parked=%v err=%v", parked, err)
	}
	if parked, err := s.ParkOrphanNodeCheckpointRestore(run.ID, run.ID, now.Add(16*time.Minute)); err != nil || !parked {
		t.Fatalf("expired workspace lease: parked=%v err=%v", parked, err)
	}
	current, _ := s.RunState(run.ID)
	if current.Status != "failed" || current.Nodes["n"].Status != "failed" {
		t.Fatalf("orphan state: %+v", current)
	}
	if _, err := s.BeginNodeRewind(run.ID, now.Add(16*time.Minute)); err != nil {
		t.Fatalf("recovery rewind: %v", err)
	}
}

func TestOrphanChildRestoreParksOnlyOwningRun(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	parent := domain.Run{ID: "restore-parent", Status: "running", Nodes: map[string]*domain.NodeRun{"p": {NodeID: "p", Status: "running"}}}
	child := domain.Run{ID: "restore-child", Status: "running", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: parent.ID}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "running", Attempt: 2}}}
	for _, run := range []domain.Run{parent, child} {
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	token, err := s.BeginWorkspaceUse(parent.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeCheckpointRestorePending(parent.ID, child.ID, token, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateNodeRewind(parent.ID, token, now); err != nil {
		t.Fatalf("automatic restore lease rejected: %v", err)
	}
	if err := s.ValidateNodeRewind(parent.ID, "stale", now); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("automatic restore accepted stale lease: %v", err)
	}
	if err := s.TryAcquireRunLease(child.ID, "worker", 0, time.Minute); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("child bypassed workspace restore fence: %v", err)
	}
	when := now.Add(16 * time.Minute)
	if parked, err := s.ParkOrphanNodeCheckpointRestore(parent.ID, parent.ID, when); err != nil || parked {
		t.Fatalf("parent parked: %v, %v", parked, err)
	}
	if parked, err := s.ParkOrphanNodeCheckpointRestore(child.ID, parent.ID, when); err != nil || !parked {
		t.Fatalf("child not parked: %v, %v", parked, err)
	}
	got, _ := s.RunState(child.ID)
	if got.Status != "failed" || got.Nodes["n"].Status != "failed" {
		t.Fatalf("child state = %+v", got)
	}
	if parked, err := s.ParkAfterSharedNodeRestore(parent.ID, parent.ID, "", parent.Generation, when); err != nil || !parked {
		t.Fatalf("parent not parked after child: %v, %v", parked, err)
	}
	parent, _ = s.RunState(parent.ID)
	if parent.Status != "failed" || parent.Nodes["p"].Status != "failed" {
		t.Fatalf("parent state = %+v", parent)
	}
	if _, err := s.BeginSharedNodeRewind(child.ID, parent.ID, when); err != nil {
		t.Fatalf("child recovery remains blocked: %v", err)
	}
}

func TestNestedRestoreFailureWaitsForOtherRunLease(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	owner := domain.Run{ID: "nested-owner", Status: "running", Generation: 1, Nodes: map[string]*domain.NodeRun{"sop": {NodeID: "sop", Status: "running"}}}
	middle := domain.Run{ID: "nested-middle", Status: "running", Generation: 1, OwnerInstanceID: "worker", LeaseUntil: now.Add(time.Minute), ExecutionContext: domain.TelemetryContext{WorkspaceRunID: owner.ID}, Nodes: map[string]*domain.NodeRun{"sop": {NodeID: "sop", Status: "running"}}}
	child := domain.Run{ID: "nested-child", Status: "running", Generation: 1, ExecutionContext: domain.TelemetryContext{WorkspaceRunID: owner.ID}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "running", Attempt: 1}}}
	for _, run := range []domain.Run{owner, middle, child} {
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	token, err := s.BeginWorkspaceUse(owner.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeCheckpointRestorePending(owner.ID, child.ID, token, now); err != nil {
		t.Fatal(err)
	}
	if err := s.FailInterruptedNodeCheckpointRestore(child.ID, owner.ID, token, "n", 1, 1, "interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishWorkspaceUse(owner.ID, token, now, nil, ""); err != nil {
		t.Fatal(err)
	}
	if parked, err := s.ParkAfterSharedNodeRestore(middle.ID, owner.ID, "", 1, now); err != nil || parked {
		t.Fatalf("live middle lease parked: %v, %v", parked, err)
	}
	if parked, err := s.ParkAfterSharedNodeRestore(owner.ID, owner.ID, "", 1, now); err != nil || !parked {
		t.Fatalf("owner not parked: %v, %v", parked, err)
	}
	if _, err := s.BeginSharedNodeRewind(child.ID, owner.ID, now); err == nil {
		t.Fatal("rewind accepted an active nested sibling")
	}
	if parked, err := s.ParkAfterSharedNodeRestore(middle.ID, owner.ID, "", 1, now.Add(2*time.Minute)); err != nil || !parked {
		t.Fatalf("expired middle lease not parked: %v, %v", parked, err)
	}
	if _, err := s.BeginSharedNodeRewind(child.ID, owner.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("rewind after all runs stopped: %v", err)
	}
}

func TestChildNodeRewindFencesSharedWorkspace(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	parent := domain.Run{ID: "rewind-owner", Status: "failed", Nodes: map[string]*domain.NodeRun{"sop": {NodeID: "sop", Status: "failed"}}}
	child := domain.Run{ID: "rewind-child", Status: "waiting", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: parent.ID}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "failed"}}}
	sibling := domain.Run{ID: "rewind-sibling", Status: "running", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: parent.ID}, Nodes: map[string]*domain.NodeRun{}}
	for _, run := range []domain.Run{parent, child, sibling} {
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	changedOwner := child
	changedOwner.ExecutionContext.WorkspaceRunID = ""
	if err := s.PutRun(changedOwner); err == nil {
		t.Fatal("existing child changed workspace owner")
	}
	now := time.Now().UTC()
	if _, err := s.BeginSharedNodeRewind(child.ID, parent.ID, now); err == nil {
		t.Fatal("rewind accepted a running sibling")
	}
	sibling, _ = s.RunState(sibling.ID)
	sibling.Status = "failed"
	if err := s.PutRun(sibling); err != nil {
		t.Fatal(err)
	}
	token, err := s.BeginSharedNodeRewind(child.ID, parent.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.ID, child.ID, sibling.ID} {
		run, _ := s.RunState(id)
		if err := s.TryAcquireRunLease(id, "worker", run.Generation, time.Minute); !errors.Is(err, ErrWorkspaceInUse) {
			t.Fatalf("run %s bypassed fence: %v", id, err)
		}
	}
	if _, err := s.RequestRunPause(child.ID); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("child pause bypassed shared fence: %v", err)
	}
	newChild := domain.Run{ID: "new-child", Status: "running", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: parent.ID}, Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(newChild); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("new child bypassed fence: %v", err)
	}
	if err := s.MarkNodeRewindPending(parent.ID, token, now); err != nil {
		t.Fatal(err)
	}
	child, _ = s.RunState(child.ID)
	child.Nodes["n"].Status = "pending"
	child.Events = append(child.Events, domain.RunEvent{ID: "rewind-event", RunID: child.ID, NodeID: "n", Type: "workspace.rewound", Timestamp: now})
	if err := s.FinishNodeRewind(child, token, child.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishWorkspaceUse(parent.ID, token, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
	child, _ = s.RunState(child.ID)
	if child.Nodes["n"].Status != "pending" || len(child.Events) != 1 || child.Events[0].Type != "workspace.rewound" {
		t.Fatalf("child state: %+v", child)
	}
	meta, _ := s.WorkspaceCheckpoint(parent.ID)
	if meta.NodeRestoreRunID != "" {
		t.Fatal("restore owner was not cleared")
	}
}

func TestWorkspaceRunsRejectsCorruptExecutionContext(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := domain.Run{ID: "corrupt-workspace-owner", Status: "failed", Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&runRow{}).Where("id = ?", run.ID).Update("execution_context", []byte("{")).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.WorkspaceRuns("some-owner"); err == nil {
		t.Fatal("corrupt workspace owner was silently ignored")
	}
}
func TestWorkspaceCheckpointDetectsCorruptArchiveAndPreservesLastGoodSave(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := domain.Run{ID: "bad-checkpoint", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err = s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token, err := s.BeginWorkspaceUse(r.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishWorkspaceUse(r.ID, token, now, []byte("good"), ""); err != nil {
		t.Fatal(err)
	}
	token, err = s.BeginWorkspaceUse(r.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishWorkspaceUse(r.ID, token, now, nil, "database unavailable"); err != nil {
		t.Fatal(err)
	}
	body, err := s.WorkspaceCheckpointArchive(r.ID)
	if err != nil || string(body) != "good" {
		t.Fatal("last good save lost", err)
	}
	meta, _ := s.WorkspaceCheckpoint(r.ID)
	if meta.Error == "" {
		t.Fatal("save failure lost")
	}
	if err = s.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ?", r.ID).Update("archive", []byte("tampered")).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.WorkspaceCheckpointArchive(r.ID); err == nil {
		t.Fatal("corrupt archive accepted")
	}
}
