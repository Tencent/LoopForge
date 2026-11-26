package repo

import (
	"encoding/json"
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

func TestWorkflowRevisionRestoreKeepsVersionNumbersUnique(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "rev-ver", Name: "Rev Version", Status: "draft", Description: "first"}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	v1, err := s.PublishWorkflow(wf.ID)
	if err != nil || v1.Status != "published" {
		t.Fatalf("publish v1: %+v %v", v1, err)
	}
	v1.Description = "second"
	if err := s.PutWorkflow(v1); err != nil {
		t.Fatal(err)
	}
	v2, err := s.PublishWorkflow(wf.ID)
	if err != nil || v2.Version != v1.Version+1 || v2.Description != "second" {
		t.Fatalf("publish v2: %+v %v", v2, err)
	}
	var first domain.DefinitionRevision
	for _, rev := range s.Revisions("workflow", wf.ID) {
		var snap domain.WorkflowDefinition
		if json.Unmarshal(rev.Payload, &snap) == nil && snap.Description == "first" && snap.Version == v1.Version {
			first = rev
			break
		}
	}
	if first.ID == "" {
		t.Fatal("no revision holds the first release")
	}
	restored, err := s.RestoreWorkflowRevision(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Description != "first" || restored.Version != v2.Version {
		t.Fatalf("restore must keep the latest version number: %+v", restored)
	}
	v3, err := s.PublishWorkflow(wf.ID)
	if err != nil || v3.Version != v2.Version+1 || v3.Description != "first" {
		t.Fatalf("publishing the restored draft must create a new version: %+v %v", v3, err)
	}
}

func TestAgentRevisionRestore(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	agent := domain.AgentDefinition{ID: "rev-agent", Name: "Rev Agent", Prompt: "v1", ExecutorID: "mock"}
	if err := s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	agent.Prompt = "v2"
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
	if restored.Prompt != "v1" {
		t.Fatalf("restore instructions = %q", restored.Prompt)
	}
}
