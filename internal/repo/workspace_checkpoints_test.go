package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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
	if err := s.MarkNodeCheckpointRestorePending(run.ID, token, now); err != nil {
		t.Fatal(err)
	}
	if parked, err := s.ParkOrphanNodeCheckpointRestore(run.ID, now.Add(time.Minute)); err != nil || parked {
		t.Fatalf("live workspace lease: parked=%v err=%v", parked, err)
	}
	if parked, err := s.ParkOrphanNodeCheckpointRestore(run.ID, now.Add(16*time.Minute)); err != nil || !parked {
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
