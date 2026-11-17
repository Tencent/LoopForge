package engine

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

func gateBindingRun(id string) domain.Run {
	wf := domain.WorkflowDefinition{
		ID: "gate-binding-wf", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "verify", Type: "script", ScriptLanguage: "shell", Script: "true", MaxAttempts: 1},
			{ID: "gate", Type: "approval"},
		},
		Edges: []domain.WorkflowEdge{{Source: "verify", Target: "gate"}},
	}
	return domain.Run{
		ID: id, WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "waiting",
		Nodes: map[string]*domain.NodeRun{
			"verify": {NodeID: "verify", Status: "succeeded", Attempt: 1},
			"gate":   {NodeID: "gate", Status: "waiting_approval", Attempt: 1},
		},
		Artifacts: []domain.Artifact{{
			ID: "artifact-verify", RunID: id, NodeID: "verify", Attempt: 1,
			Name: "verify", Path: "verify.json", MediaType: "application/json",
			Size: 2, SHA256: "aaaa", Content: "{}", CreatedAt: time.Now().UTC(),
		}},
	}
}

func TestGateBindingApprovesMatchingArtifactHash(t *testing.T) {
	e, _ := cvEEngine(t)
	e.executeRuns = false
	run := gateBindingRun("gate-binding-ok")
	cvEPutRun(t, e, run)

	if err := e.Approve(run.ID, "gate", "", NodeExpectation{
		NodeID: "gate", Status: "waiting_approval", Attempt: 1,
		ArtifactName: "verify", ArtifactSHA256: "aaaa",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.repo.RunState(run.ID)
	if got.Nodes["gate"].Status != "succeeded" {
		t.Fatalf("gate status = %q, want succeeded", got.Nodes["gate"].Status)
	}
}

func TestGateBindingRejectsMismatchedArtifactHash(t *testing.T) {
	e, _ := cvEEngine(t)
	e.executeRuns = false
	run := gateBindingRun("gate-binding-mismatch")
	cvEPutRun(t, e, run)

	err := e.Approve(run.ID, "gate", "", NodeExpectation{
		NodeID: "gate", Status: "waiting_approval", Attempt: 1,
		ArtifactName: "verify", ArtifactSHA256: "bbbb",
	})
	if err == nil {
		t.Fatal("approval with a mismatched artifact hash was accepted")
	}
	if _, ok := apperr.As(err); !ok {
		t.Fatalf("error is not an app error: %v", err)
	}
	if !strings.Contains(err.Error(), "bbbb") || !strings.Contains(err.Error(), "aaaa") {
		t.Fatalf("error %q must name expected and actual hash", err)
	}

	got, _ := e.repo.RunState(run.ID)
	if got.Nodes["gate"].Status != "waiting_approval" || got.Status != "waiting" {
		t.Fatalf("rejected approval changed run: status=%q node=%q", got.Status, got.Nodes["gate"].Status)
	}
	found := false
	for _, event := range got.Events {
		if event.Type == "gate.approval_rejected" {
			found = true
			if !strings.Contains(event.Message, "bbbb") || !strings.Contains(event.Message, "aaaa") {
				t.Fatalf("audit event %q must name expected and actual hash", event.Message)
			}
		}
	}
	if !found {
		t.Fatalf("gate.approval_rejected audit event missing: %+v", got.Events)
	}
}

func TestGateBindingRejectsMissingArtifact(t *testing.T) {
	e, _ := cvEEngine(t)
	e.executeRuns = false
	run := gateBindingRun("gate-binding-missing")
	cvEPutRun(t, e, run)

	err := e.Approve(run.ID, "gate", "", NodeExpectation{
		NodeID: "gate", Status: "waiting_approval", Attempt: 1,
		ArtifactName: "absent", ArtifactSHA256: "aaaa",
	})
	if err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("missing artifact must reject approval naming it, got %v", err)
	}
	got, _ := e.repo.RunState(run.ID)
	if got.Nodes["gate"].Status != "waiting_approval" {
		t.Fatalf("gate status = %q, want waiting_approval", got.Nodes["gate"].Status)
	}
}

func TestGateBindingWithoutExpectedKeepsLegacyBehavior(t *testing.T) {
	e, _ := cvEEngine(t)
	e.executeRuns = false
	run := gateBindingRun("gate-binding-legacy")
	cvEPutRun(t, e, run)

	if err := e.Approve(run.ID, "gate", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := e.repo.RunState(run.ID)
	if got.Nodes["gate"].Status != "succeeded" {
		t.Fatalf("gate status = %q, want succeeded", got.Nodes["gate"].Status)
	}
}
