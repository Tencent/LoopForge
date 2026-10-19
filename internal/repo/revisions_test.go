package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestWorkflowRevisionDiffAndRestore(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "rev-sop", Name: "Rev SOP", Status: "draft", Description: "first"}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Workflow(wf.ID)
	stored.Description = "second"
	if err := s.PutWorkflow(stored); err != nil {
		t.Fatal(err)
	}
	revs := s.Revisions("workflow", wf.ID)
	if len(revs) == 0 {
		t.Fatal("expected a snapshot of the previous workflow")
	}
	restored, err := s.RestoreWorkflowRevision(revs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Description != "first" {
		t.Fatalf("restore description = %q", restored.Description)
	}
	got, _ := s.Workflow(wf.ID)
	if got.Description != "first" {
		t.Fatalf("stored description = %q", got.Description)
	}
}

func TestAgentRevisionRestore(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	agent := domain.AgentDefinition{ID: "rev-agent", Name: "Rev Agent", Instructions: "v1", ExecutorID: "mock"}
	if err := s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	agent.Instructions = "v2"
	agent.Version = 2
	if err := s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	revs := s.Revisions("agent", agent.ID)
	if len(revs) == 0 {
		t.Fatal("expected a snapshot of the previous agent")
	}
	restored, err := s.RestoreAgentRevision(revs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Instructions != "v1" {
		t.Fatalf("restore instructions = %q", restored.Instructions)
	}
}
