package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func localSandboxStore(t *testing.T) (*repo.Store, domain.SandboxProfile) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutRunner(domain.Runner{ID: "runner-1", Name: "mac", RunnerVersion: domain.RunnerVersionLocalWorktree, OwnerUserID: 1}); err != nil {
		t.Fatal(err)
	}
	profile := domain.SandboxProfile{ID: "mine", Name: "Mine", ProjectID: "platform", Driver: domain.SandboxDriverLocal, RunnerID: "runner-1"}
	if err := s.PutSandbox(profile); err != nil {
		t.Fatal(err)
	}
	return s, profile
}

// An SOP never runs on a machine without isolation, even when its stored
// definition still names a local sandbox.
func TestSOPRefusesLocalSandbox(t *testing.T) {
	t.Parallel()
	s, profile := localSandboxStore(t)
	wf := domain.WorkflowDefinition{ID: "local-sop", Name: "Local SOP", SandboxProfileID: profile.ID,
		Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build", Type: "command", Command: "true", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err == nil || !strings.Contains(err.Error(), "SOP 不能使用 local") {
		t.Fatalf("local SOP saved: %v", err)
	}
	raw, _ := json.Marshal(wf)
	if err := s.DB.Table("workflows").Create(map[string]any{"id": wf.ID, "name": wf.Name, "version": 0, "revision": 0, "status": "", "payload": raw}).Error; err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(t.TempDir()))
	if _, err := e.Start(wf.ID, ""); err == nil || !strings.Contains(err.Error(), "SOP 不能使用 local") {
		t.Fatalf("local SOP started: %v", err)
	}
}

func TestLocalSandboxOpensOnlyOnItsOwnersBoundMachine(t *testing.T) {
	t.Parallel()
	s, profile := localSandboxStore(t)
	if err := s.PutRunner(domain.Runner{ID: "runner-old", Name: "old", RunnerVersion: domain.RunnerVersionGitSSHKnownHosts, OwnerUserID: 1}); err != nil {
		t.Fatal(err)
	}
	e := New(s, nil)
	owner := domain.TelemetryContext{ExecutionRunnerID: "runner-1", ResponsibleUserID: 1}
	if err := e.requireLocalSandboxRunner(profile, owner); err != nil {
		t.Fatal(err)
	}
	stale := profile
	stale.RunnerID = "runner-old"
	for name, tc := range map[string]struct {
		profile domain.SandboxProfile
		context domain.TelemetryContext
	}{
		"no machine":     {profile, domain.TelemetryContext{ResponsibleUserID: 1}},
		"other machine":  {profile, domain.TelemetryContext{ExecutionRunnerID: "runner-2", ResponsibleUserID: 1}},
		"cloud pool":     {profile, domain.TelemetryContext{ExecutionRunnerID: "runner-1", ExecutionPool: domain.ExecutionModeCloud, ResponsibleUserID: 1}},
		"other user":     {profile, domain.TelemetryContext{ExecutionRunnerID: "runner-1", ResponsibleUserID: 2}},
		"old runner":     {stale, domain.TelemetryContext{ExecutionRunnerID: "runner-old", ResponsibleUserID: 1}},
		"missing runner": {domain.SandboxProfile{ID: "gone", Driver: domain.SandboxDriverLocal, RunnerID: "gone"}, domain.TelemetryContext{ExecutionRunnerID: "gone", ResponsibleUserID: 1}},
	} {
		if err := e.requireLocalSandboxRunner(tc.profile, tc.context); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := e.requireLocalSandboxRunner(domain.SandboxProfile{Driver: domain.SandboxDriverNative}, domain.TelemetryContext{}); err != nil {
		t.Fatalf("non-local sandbox checked: %v", err)
	}
}

// A local workspace's worktrees are the only copy of uncommitted work: idle
// reclaim never removes them and turns never snapshot them.
func TestLocalWorkspaceIsNeitherReclaimedNorSnapshotted(t *testing.T) {
	t.Parallel()
	s, profile := localSandboxStore(t)
	e := New(s, runtime.NewManager(t.TempDir()))
	run := domain.Run{ID: "local-ws", Kind: domain.RunKindWorkspace, WorkflowID: domain.ConversationWorkspaceWorkflowID, Status: "waiting", Nodes: map[string]*domain.NodeRun{},
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxSnapshot: &domain.RunnerSandboxSpec{Profile: profile}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(e.RunWorkspace(run.ID), "service", "work.txt")
	if err := os.MkdirAll(filepath.Dir(work), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(work, []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.saveConversationWorkspace(context.Background(), run, "turn"); err != nil {
		t.Fatalf("local workspace snapshot: %v", err)
	}
	e.cleanupWorkspace(run, time.Now().Add(48*time.Hour))
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("idle reclaim removed local work: %v", err)
	}
	if stored, _ := s.Run(run.ID); !stored.WorkspaceCleanedAt.IsZero() {
		t.Fatalf("local workspace marked reclaimed: %+v", stored)
	}
}
