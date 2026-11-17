package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func verificationPassRun(id string) domain.Run {
	wf := domain.WorkflowDefinition{
		ID: "pass-override-wf", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "verify", Name: "Verify", Type: "script", ScriptLanguage: "shell", Script: "exit 1", MaxAttempts: 1, VerificationStep: true},
			{ID: "ship", Name: "Ship", Type: "script", ScriptLanguage: "shell", Script: "true", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{Source: "verify", Target: "ship"}},
	}
	return domain.Run{
		ID: id, WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed",
		Nodes: map[string]*domain.NodeRun{
			"verify": {NodeID: "verify", Status: "failed", Attempt: 1, Error: "exit status 1"},
			"ship":   {NodeID: "ship", Status: "pending"},
		},
	}
}

func TestPassRejectsVerificationNodeWithoutOverride(t *testing.T) {
	e, _ := cvEEngine(t)
	e.executeRuns = false
	run := verificationPassRun("pass-override-reject")
	cvEPutRun(t, e, run)

	err := e.Pass(run.ID, "verify", "environment broke the check")
	if err == nil {
		t.Fatal("verification node was passed without an override")
	}
	if !strings.Contains(err.Error(), "override") {
		t.Fatalf("error %q must name the override switch", err)
	}
	got, _ := e.repo.RunState(run.ID)
	if got.Nodes["verify"].Status != "failed" {
		t.Fatalf("verify status = %q, want failed", got.Nodes["verify"].Status)
	}
	if !hasRunEvent(got, "node.pass_rejected") {
		t.Fatalf("node.pass_rejected audit event missing: %+v", got.Events)
	}
}

func TestPassOverridePassesVerificationNodeAndRecordsActor(t *testing.T) {
	e, _ := cvEEngine(t)
	e.executeRuns = false
	run := verificationPassRun("pass-override-accept")
	cvEPutRun(t, e, run)

	err := e.PassWithOptions(run.ID, "verify", "flake, rerun manually", PassOptions{Override: true, Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := e.repo.RunState(run.ID)
	if got.Nodes["verify"].Status != "succeeded" {
		t.Fatalf("verify status = %q, want succeeded", got.Nodes["verify"].Status)
	}
	found := false
	for _, event := range got.Events {
		if event.Type == "node.passed_override" {
			found = true
			if !strings.Contains(event.Message, "alice") {
				t.Fatalf("override event %q must name the operator", event.Message)
			}
		}
	}
	if !found {
		t.Fatalf("node.passed_override audit event missing: %+v", got.Events)
	}
}

func TestPassKeepsWorkingForNonVerificationScriptNode(t *testing.T) {
	e, _ := cvEEngine(t)
	e.executeRuns = false
	run := verificationPassRun("pass-override-plain")
	run.WorkflowSnapshot.Nodes[0].VerificationStep = false
	cvEPutRun(t, e, run)

	if err := e.Pass(run.ID, "verify", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := e.repo.RunState(run.ID)
	if got.Nodes["verify"].Status != "succeeded" {
		t.Fatalf("verify status = %q, want succeeded", got.Nodes["verify"].Status)
	}
}
