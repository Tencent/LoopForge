package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func handoffRepoFixture(t *testing.T) (*Store, domain.SOPHandoffJob, time.Time) {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	now := time.Now().UTC()
	r := domain.Run{ID: "durable", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group"}, CreatedAt: now}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	j, created, err := s.BeginSOPHandoffJob(domain.SOPHandoffJob{ID: "op-1", RunID: r.ID, NodeID: "build", NodeName: "Build", ActorID: 1, CreatedAt: now})
	if err != nil || !created {
		t.Fatal(err)
	}
	return s, j, now
}
func TestSOPHandoffJobLeaseRecoveryFencesOldWorkerAndWorkspace(t *testing.T) {
	s, j, now := handoffRepoFixture(t)
	duplicate, created, err := s.BeginSOPHandoffJob(domain.SOPHandoffJob{ID: "duplicate", RunID: j.RunID, NodeID: "other", ActorID: 2})
	if err != nil || created || duplicate.ID != j.ID {
		t.Fatal("duplicate replaced active job", err)
	}
	if _, err := s.BeginWorkspaceUse(j.RunID, now); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatal("pending job did not reserve writers", err)
	}
	j, claimed, err := s.ClaimSOPHandoffJob(j.RunID, j.ID, "worker-a", now, 10*time.Second)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	token, err := s.BeginWorkspaceUse(j.RunID, now, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := s.WorkspaceCheckpoint(j.RunID)
	if checkpoint.LeaseUntil == nil || !checkpoint.LeaseUntil.Equal(now.Add(10*time.Second)) {
		t.Fatal("job workspace retained 15 minute lease")
	}
	renewed, err := s.RenewSOPHandoffJob(j.RunID, j.ID, j.Owner, now.Add(5*time.Second), now.Add(15*time.Second))
	if err != nil || !renewed {
		t.Fatal(err)
	}
	checkpoint, _ = s.WorkspaceCheckpoint(j.RunID)
	if !checkpoint.LeaseUntil.Equal(now.Add(15 * time.Second)) {
		t.Fatal("workspace renewal diverged")
	}
	if _, ok, err := s.ClaimSOPHandoffJob(j.RunID, j.ID, "too-early", now.Add(6*time.Second), 10*time.Second); err != nil || ok {
		t.Fatal("stole live lease", err)
	}
	next, claimed, err := s.ClaimSOPHandoffJob(j.RunID, j.ID, "worker-b", now.Add(16*time.Second), 10*time.Second)
	if err != nil || !claimed || next.Attempt != j.Attempt+1 {
		t.Fatal("cannot reclaim after crash", err)
	}
	if ok, err := s.UpdateSOPHandoffJob(j, j.Owner, now.Add(17*time.Second)); err != nil || ok {
		t.Fatal("stale worker updated progress", err)
	}
	if err := s.FinishWorkspaceUse(j.RunID, token, now.Add(17*time.Second), nil, ""); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatal("stale workspace token survived claim", err)
	}
	token, err = s.BeginWorkspaceUse(j.RunID, now.Add(17*time.Second), next.ID)
	if err != nil {
		t.Fatal("new worker blocked by abandoned workspace lease", err)
	}
	if err := s.FinishWorkspaceUse(j.RunID, token, now.Add(18*time.Second), nil, ""); err != nil {
		t.Fatal(err)
	}
	next.Status = domain.SOPHandoffJobFailed
	next.Phase = domain.SOPHandoffPhaseStarting
	next.Summary = "private cached summary"
	next.ModelSourceSHA = "source-digest"
	next.SummaryArtifactID = "saved-summary"
	if ok, err := s.FinishSOPHandoffJob(next, next.Owner, now.Add(19*time.Second)); err != nil || !ok {
		t.Fatal(err)
	}
	retried, ok, err := s.RetrySOPHandoffJob(next.RunID, next.ID, now.Add(20*time.Second))
	if err != nil || !ok || retried.Phase != domain.SOPHandoffPhaseStarting || retried.Summary != next.Summary {
		t.Fatal("retry lost saved stage", err)
	}
	data, _ := json.Marshal(retried)
	for _, secret := range []string{"private cached summary", "source-digest", "worker-b", "leaseUntil"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("internal job state leaked", secret)
		}
	}
}
func TestSOPHandoffJobsProjectScopeAndManualResumeReconciliation(t *testing.T) {
	s, j, now := handoffRepoFixture(t)
	j, _, _ = s.ClaimSOPHandoffJob(j.RunID, j.ID, "owner", now, time.Minute)
	j.Status = domain.SOPHandoffJobFailed
	j.Phase = domain.SOPHandoffPhaseStarting
	j.Summary = "private-body"
	if ok, err := s.FinishSOPHandoffJob(j, j.Owner, now.Add(time.Second)); err != nil || !ok {
		t.Fatal(err)
	}
	jobs, err := s.SOPHandoffJobsForProject("p")
	if err != nil || len(jobs) != 1 || jobs[0].Summary != "" {
		t.Fatal("project list failed/leaked summary", jobs, err)
	}
	other, err := s.SOPHandoffJobsForProject("other")
	if err != nil || len(other) != 0 {
		t.Fatal("cross-project jobs leaked", err)
	}
	if changed, err := s.CompleteObservedSOPHandoffJob(j.RunID, j.ID, now); err != nil || changed {
		t.Fatal("unresumed operation marked complete", err)
	}
	run, _ := s.RunState(j.RunID)
	run.WorkflowSnapshot.CollaborationMode = ""
	run.WorkflowSnapshot.GroupSOPHandoff = &domain.GroupSOPHandoff{ID: j.ID}
	run.Status = "cancelled"
	run.PauseRequested = false
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.CompleteObservedSOPHandoffJob(j.RunID, j.ID, now); err != nil || changed {
		t.Fatal("cancellation cannot prove the SOP started", err)
	}
	run.Status = "running"
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.CompleteObservedSOPHandoffJob(j.RunID, j.ID, now); err != nil || !changed {
		t.Fatal("manual continue did not clear old failure", err)
	}
	latest, _, _ := s.SOPHandoffJob(j.RunID)
	if latest.Status != domain.SOPHandoffJobSucceeded || latest.Error != "" {
		t.Fatal(latest)
	}
}
