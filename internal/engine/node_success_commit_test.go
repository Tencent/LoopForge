package engine

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func publishedArtifactObservations(e *Engine, runID string) int {
	count := 0
	for _, item := range e.takePendingObservations(runID) {
		if item.event.Name == "artifact.published" {
			count++
		}
	}
	return count
}

func TestCommitNodeSuccessPublishesArtifactsOnlyAfterCommit(t *testing.T) {
	t.Parallel()
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	r := leaseRun(t, s, "run-commit-ok", "running", "running")
	leased, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	node := domain.WorkflowNode{ID: "node", Name: "方案设计", Type: "agent"}
	artifacts := []domain.Artifact{{ID: "a1", Name: "design", Path: "design.md"}}

	if !e.commitNodeSuccess(r.ID, node, leased.Generation, 1, "done", artifacts, "") {
		t.Fatal("commit with an owned lease and a running node must land")
	}
	got, _ := s.RunState(r.ID)
	if got.Nodes["node"].Status != "succeeded" || got.Nodes["node"].Output != "done" {
		t.Fatalf("node not committed: status=%s output=%q", got.Nodes["node"].Status, got.Nodes["node"].Output)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].ID != "a1" {
		t.Fatalf("artifacts not stored: %+v", got.Artifacts)
	}
	if n := publishedArtifactObservations(e, r.ID); n != 1 {
		t.Fatalf("artifact.published observations = %d, want 1", n)
	}
}

func TestCommitNodeSuccessManualGateWaitsAndPublishes(t *testing.T) {
	t.Parallel()
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	r := leaseRun(t, s, "run-commit-gate", "running", "running")
	leased, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	node := domain.WorkflowNode{ID: "node", Name: "演示", Type: "agent"}
	if !e.commitNodeSuccess(r.ID, node, leased.Generation, 1, "ok", []domain.Artifact{{ID: "a1", Name: "demo"}}, "人工验收") {
		t.Fatal("commit must land")
	}
	got, _ := s.RunState(r.ID)
	if got.Nodes["node"].Status != "waiting_approval" {
		t.Fatalf("node status = %s, want waiting_approval", got.Nodes["node"].Status)
	}
	if n := publishedArtifactObservations(e, r.ID); n != 1 {
		t.Fatalf("artifact.published observations = %d, want 1", n)
	}
}

func TestCommitNodeSuccessDroppedByNodeFenceDoesNotPublish(t *testing.T) {
	t.Parallel()
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	// A parallel rework reset the node to pending while its agent was still
	// finishing: the late success must not land, and must not look like it did.
	r := leaseRun(t, s, "run-commit-reset", "running", "pending")
	leased, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	node := domain.WorkflowNode{ID: "node", Name: "方案设计", Type: "agent"}
	if e.commitNodeSuccess(r.ID, node, leased.Generation, 1, "late", []domain.Artifact{{ID: "a1", Name: "design"}}, "") {
		t.Fatal("commit for a node that is no longer in flight must be dropped")
	}
	got, _ := s.RunState(r.ID)
	if got.Nodes["node"].Status != "pending" || len(got.Artifacts) != 0 {
		t.Fatalf("dropped commit mutated run: status=%s artifacts=%d", got.Nodes["node"].Status, len(got.Artifacts))
	}
	if n := publishedArtifactObservations(e, r.ID); n != 0 {
		t.Fatalf("artifact.published observations = %d, want 0", n)
	}
}

func TestCommitNodeSuccessDroppedByStaleGenerationDoesNotPublish(t *testing.T) {
	t.Parallel()
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	r := leaseRun(t, s, "run-commit-generation", "running", "running")
	leased, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	node := domain.WorkflowNode{ID: "node", Name: "方案设计", Type: "agent"}
	if e.commitNodeSuccess(r.ID, node, leased.Generation-1, 1, "stale", []domain.Artifact{{ID: "a1", Name: "design"}}, "") {
		t.Fatal("commit from an older generation must be dropped")
	}
	got, _ := s.RunState(r.ID)
	if got.Nodes["node"].Status != "running" || len(got.Artifacts) != 0 {
		t.Fatalf("dropped commit mutated run: status=%s artifacts=%d", got.Nodes["node"].Status, len(got.Artifacts))
	}
	if n := publishedArtifactObservations(e, r.ID); n != 0 {
		t.Fatalf("artifact.published observations = %d, want 0", n)
	}
}

func TestCommitNodeSuccessWithoutLeaseDoesNotPublish(t *testing.T) {
	t.Parallel()
	s, _ := repo.OpenTest()
	defer s.Close()
	owner, other := leaseEngine(t, s, "owner"), leaseEngine(t, s, "other")
	r := leaseRun(t, s, "run-commit-foreign", "running", "running")
	leased, err := owner.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	node := domain.WorkflowNode{ID: "node", Name: "方案设计", Type: "agent"}
	if other.commitNodeSuccess(r.ID, node, leased.Generation, 1, "foreign", []domain.Artifact{{ID: "a1", Name: "design"}}, "") {
		t.Fatal("an instance without the run lease must not commit")
	}
	got, _ := s.RunState(r.ID)
	if got.Nodes["node"].Status != "running" || len(got.Artifacts) != 0 {
		t.Fatalf("non-owner mutated run: status=%s artifacts=%d", got.Nodes["node"].Status, len(got.Artifacts))
	}
	if n := publishedArtifactObservations(other, r.ID); n != 0 {
		t.Fatalf("artifact.published observations = %d, want 0", n)
	}
}

func TestCommitNodeSuccessStoreWriteFailureDoesNotPublish(t *testing.T) {
	t.Parallel()
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	r := leaseRun(t, s, "run-commit-put-fails", "running", "running")
	leased, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	// The fences all pass and the mutation runs, but PutRun fails: the node row
	// refuses to become succeeded. The outcome must be reported as dropped and
	// no artifact.published may be queued for the rolled-back write.
	if err := s.DB.Exec(`CREATE TRIGGER fail_node_success BEFORE UPDATE ON run_nodes
		WHEN NEW.run_id = 'run-commit-put-fails' AND NEW.status = 'succeeded'
		BEGIN SELECT RAISE(ABORT, 'injected store failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	node := domain.WorkflowNode{ID: "node", Name: "方案设计", Type: "agent"}
	if e.commitNodeSuccess(r.ID, node, leased.Generation, 1, "done", []domain.Artifact{{ID: "a1", Name: "design"}}, "") {
		t.Fatal("a commit whose store write failed must be reported as dropped")
	}
	got, _ := s.RunState(r.ID)
	if got.Nodes["node"].Status != "running" || len(got.Artifacts) != 0 {
		t.Fatalf("failed write left run changed: status=%s artifacts=%d", got.Nodes["node"].Status, len(got.Artifacts))
	}
	if n := publishedArtifactObservations(e, r.ID); n != 0 {
		t.Fatalf("artifact.published observations = %d, want 0", n)
	}
}

func TestCommitNodeSuccessClearsStaleErrorAndStampsFinish(t *testing.T) {
	t.Parallel()
	for _, gate := range []string{"", "人工验收"} {
		t.Run("gate="+gate, func(t *testing.T) {
			s, _ := repo.OpenTest()
			defer s.Close()
			e := leaseEngine(t, s, "owner")
			r := leaseRun(t, s, "run-commit-fields", "running", "running")
			leased, err := e.acquireLease(r)
			if err != nil {
				t.Fatal(err)
			}
			// A previous attempt left an error on the node; the landed success
			// must clear it and record when the node finished.
			if !e.updateRunAt(r.ID, false, func(r *domain.Run) { r.Nodes["node"].Error = "上一次尝试失败" }) {
				t.Fatal("seed error not stored")
			}
			before := time.Now().UTC()
			node := domain.WorkflowNode{ID: "node", Name: "方案设计", Type: "agent"}
			if !e.commitNodeSuccess(r.ID, node, leased.Generation, 1, "done", nil, gate) {
				t.Fatal("commit must land")
			}
			got, _ := s.RunState(r.ID)
			nr := got.Nodes["node"]
			if nr.Error != "" {
				t.Fatalf("stale error survived the successful commit: %q", nr.Error)
			}
			if nr.FinishedAt.IsZero() || nr.FinishedAt.Before(before.Add(-time.Second)) {
				t.Fatalf("FinishedAt = %v, want stamped at commit (>= %v)", nr.FinishedAt, before)
			}
		})
	}
}

func TestNodeCommitRejectionNamesTheFence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  domain.Run
		nr   *domain.NodeRun
		want string
	}{
		{"missing node", domain.Run{Generation: 3}, nil, "node is missing from the run"},
		{"generation moved", domain.Run{Generation: 4}, &domain.NodeRun{Status: "running"}, "run generation moved from 3 to 4"},
		{"node not in flight", domain.Run{Generation: 3}, &domain.NodeRun{Status: "pending"}, "node is pending, not in flight"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nodeCommitRejection(tc.run, tc.nr, 3); !strings.Contains(got, tc.want) {
				t.Fatalf("nodeCommitRejection = %q, want %q", got, tc.want)
			}
		})
	}
}
