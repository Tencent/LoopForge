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
	wf := domain.WorkflowDefinition{ID: "rev-sop", Name: "Rev LOOP", Status: "draft", Description: "first"}
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
	// The designer adopts the returned definition and saves the next edit with
	// its revision, so it must be the stored one, not the snapshot's.
	if restored.Revision != got.Revision || !restored.UpdatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("restore returned revision %d (%s), stored %d (%s)", restored.Revision, restored.UpdatedAt, got.Revision, got.UpdatedAt)
	}
	restored.Description = "third"
	if err := s.PutWorkflow(restored); err != nil {
		t.Fatalf("saving an edit right after restore: %v", err)
	}
}

func TestWorkflowRevisionRestoreReturnsPublishedMarker(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "rev-pub", Name: "Rev Published", Status: "draft", Description: "draft"}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	published, err := s.PublishWorkflow(wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	var draft domain.DefinitionRevision
	for _, rev := range s.Revisions("workflow", wf.ID) {
		var snap domain.WorkflowDefinition
		if json.Unmarshal(rev.Payload, &snap) == nil && snap.PublishedRevision == 0 {
			draft = rev
		}
	}
	if draft.ID == "" {
		t.Fatal("no revision holds the unpublished draft")
	}
	// Restoring a snapshot taken before the first release keeps the release
	// marker of the stored LOOP rather than the snapshot's empty one.
	restored, err := s.RestoreWorkflowRevision(draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Workflow(wf.ID)
	if restored.Revision != got.Revision || restored.PublishedRevision != published.PublishedRevision || got.PublishedRevision != published.PublishedRevision {
		t.Fatalf("restored %d/%d, stored %d/%d, published %d", restored.Revision, restored.PublishedRevision, got.Revision, got.PublishedRevision, published.PublishedRevision)
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
	got, _ := s.Agent(agent.ID)
	if restored.Version != got.Version || !restored.UpdatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("restore returned v%d (%s), stored v%d (%s)", restored.Version, restored.UpdatedAt, got.Version, got.UpdatedAt)
	}
}

func TestGenericWorkflowRevisionRestoreRewritesTemplate(t *testing.T) {
	s := testStoreWithGenericTemplates(t)
	before, ok := s.GenericWorkflowByID(GenericBackendDeliveryWorkflowID)
	if !ok {
		t.Fatal("backend template missing")
	}
	edited := before
	edited.Description = "admin edit"
	if _, err := s.UpdateGenericWorkflow(edited); err != nil {
		t.Fatal(err)
	}
	revs := s.Revisions("workflow", GenericBackendDeliveryWorkflowID)
	if len(revs) == 0 {
		t.Fatal("expected a snapshot of the template before the edit")
	}
	restored, err := s.RestoreWorkflowRevision(revs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GenericWorkflowByID(GenericBackendDeliveryWorkflowID)
	if restored.Description != before.Description || got.Description != before.Description {
		t.Fatalf("restore returned %q, template is %q, want %q", restored.Description, got.Description, before.Description)
	}
	if !got.Generic || restored.Revision != got.Revision || !restored.UpdatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("restore returned revision %d (%s), template %d (%s)", restored.Revision, restored.UpdatedAt, got.Revision, got.UpdatedAt)
	}
	// The template id stays out of the workflows table.
	if _, shadow := s.storedWorkflow(GenericBackendDeliveryWorkflowID); shadow {
		t.Fatal("restoring a generic template wrote a workflows row")
	}
	// The designer saves its next edit with the returned revision.
	restored.Description = "after restore"
	if _, err := s.UpdateGenericWorkflow(restored); err != nil {
		t.Fatalf("saving an edit right after restore: %v", err)
	}
}
