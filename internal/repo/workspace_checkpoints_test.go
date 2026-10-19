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
	if claimed, err := s.ClaimCheckpointWorkspaceCleanup(r.ID, token, r.Revision, now.Add(time.Minute)); err != nil || !claimed {
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
