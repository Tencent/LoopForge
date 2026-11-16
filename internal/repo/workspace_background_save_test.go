package repo

import (
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestWorkspaceBackgroundSavePreemptionFencesOldPublisher(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := "background-save"
	if err := s.PutRun(domain.Run{ID: id, Kind: domain.RunKindWorkspace, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	use, err := s.BeginWorkspaceUse(id, now)
	if err != nil {
		t.Fatal(err)
	}
	save, err := s.HandoffWorkspaceUseToSave(id, use, now)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := s.WorkspaceCheckpoint(id)
	if meta.SaveRetryAt == nil || meta.Error != "" {
		t.Fatalf("pending save is not durable: %+v", meta)
	}
	if _, err = s.BeginWorkspaceCheckpointSave(id, now); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatal("maintenance stole live background save", err)
	}
	next, previous, err := s.BeginWorkspaceUsePreemptingSave(id, now.Add(time.Millisecond))
	if err != nil || previous != save || next == save {
		t.Fatal("foreground did not preempt archive", err)
	}
	if err = s.SaveWorkspaceCheckpoint(id, save, now, []byte("stale archive")); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatal("old archive published", err)
	}
	if err = s.FinishWorkspaceUse(id, save, now, nil, "late old failure"); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatal("old save released next lease", err)
	}
	meta, _ = s.WorkspaceCheckpoint(id)
	if meta.LeaseToken != next || meta.Error != "" || meta.SavedAt != nil {
		t.Fatalf("old save changed new lease: %+v", meta)
	}
	if _, err = s.HandoffWorkspaceUseToSave(id, use, now); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatal("stale turn handed off a new lease", err)
	}
	if err = s.FinishWorkspaceUse(id, next, now, nil, ""); err != nil {
		t.Fatal(err)
	}
	maintenance, err := s.BeginWorkspaceCheckpointSave(id, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginWorkspaceUse(id, now); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatal("foreground stole cleanup-capable maintenance lease", err)
	}
	if err = s.FinishWorkspaceUse(id, maintenance, now, nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundSaveFinishDoesNotReviveReleasedWorkspace(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := "released-background-save"
	if err = s.PutRun(domain.Run{ID: id, Kind: domain.RunKindWorkspace, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	usedAt := time.Now().UTC()
	use, err := s.BeginWorkspaceUse(id, usedAt)
	if err != nil {
		t.Fatal(err)
	}
	save, err := s.HandoffWorkspaceUseToSave(id, use, usedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReleaseWorkspace(id, usedAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveWorkspaceCheckpoint(id, save, usedAt.Add(time.Second), []byte("saved after switching away")); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishWorkspaceUse(id, save, usedAt, nil, ""); err != nil {
		t.Fatal(err)
	}
	meta, _ := s.WorkspaceCheckpoint(id)
	if !meta.Released() {
		t.Fatalf("background completion changed real activity time: %+v", meta)
	}
}
