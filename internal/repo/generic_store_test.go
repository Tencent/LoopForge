package repo

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func testStoreWithGenericTemplates(t *testing.T) *Store {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := SeedTestGenericTemplates(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGenericTemplatesLiveInDedicatedTable(t *testing.T) {
	s := testStoreWithGenericTemplates(t)
	want := []string{GenericBackendDeliveryWorkflowID, GenericProductDeliveryWorkflowID, GenericFrontendDeliveryWorkflowID}
	for _, id := range want {
		if _, stored := payloadOne[domain.WorkflowDefinition](s.DB, "workflows", id); stored {
			t.Fatalf("generic template %q must not occupy the workflows table", id)
		}
		template, ok := s.GenericTemplateByWorkflow(id)
		if !ok || !template.Workflow.Generic || template.Workflow.ID != id {
			t.Fatalf("generic template %q missing from generic_templates: %+v", id, template)
		}
		if wf, ok := s.Workflow(id); !ok || !wf.Generic {
			t.Fatalf("store Workflow(%q) missed the generic_templates row", id)
		}
	}
	listed := s.Workflows()
	seen := 0
	for _, workflow := range listed {
		if workflow.Generic {
			seen++
		}
	}
	if seen < 3 {
		t.Fatalf("store listed %d generic workflows, want at least 3", seen)
	}
}

func TestUpdateGenericWorkflowGuardsStaleWritesAndKeepsHistory(t *testing.T) {
	s := testStoreWithGenericTemplates(t)
	before, ok := s.GenericWorkflowByID(GenericBackendDeliveryWorkflowID)
	if !ok {
		t.Fatal("backend template missing")
	}
	first := before
	first.Description = "admin A"
	saved, err := s.UpdateGenericWorkflow(first)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != before.Revision+1 {
		t.Fatalf("revision = %d, want %d", saved.Revision, before.Revision+1)
	}
	stale := before
	stale.Description = "admin B"
	if _, err := s.UpdateGenericWorkflow(stale); err == nil || !strings.Contains(err.Error(), "revision conflict") {
		t.Fatalf("stale write error = %v, want revision conflict", err)
	}
	current, _ := s.GenericWorkflowByID(GenericBackendDeliveryWorkflowID)
	if current.Description != "admin A" {
		t.Fatalf("stale write overwrote the saved edit: %q", current.Description)
	}
	revisions := s.Revisions("workflow", GenericBackendDeliveryWorkflowID)
	if len(revisions) == 0 {
		t.Fatal("admin edit left no revision history to roll back to")
	}
}

func TestUpdateGenericAgentStaysInTemplateAndIgnoresProjectCatalog(t *testing.T) {
	s := testStoreWithGenericTemplates(t)
	if err := s.PutProject(domain.Project{ID: "skillhub", Key: "SH", Name: "SkillHub"}); err != nil {
		t.Fatal(err)
	}
	hijack := domain.AgentDefinition{ID: GenericAgentReview, Name: "SkillHub 评审", ProjectID: "skillhub", Version: 9, Prompt: "project copy"}
	if err := s.PutAgent(hijack); err != nil {
		t.Fatal(err)
	}
	template, ok := s.GenericTemplateByWorkflow(GenericBackendDeliveryWorkflowID)
	if !ok {
		t.Fatal("backend template missing")
	}
	var review domain.AgentDefinition
	for _, agent := range template.Agents {
		if agent.ID == GenericAgentReview {
			review = agent
		}
	}
	if review.ID == "" {
		t.Fatal("review role missing from template")
	}
	review.Prompt = "platform review instructions"
	saved, err := s.UpdateGenericAgent(GenericBackendDeliveryWorkflowID, review)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ProjectID != "" || saved.Prompt != "platform review instructions" {
		t.Fatalf("generic agent save leaked a project identity: %+v", saved)
	}
	got, _ := s.GenericAgentByID(GenericAgentReview)
	if got.Prompt != "platform review instructions" {
		t.Fatalf("template role was not updated: %+v", got)
	}
	projectCopy, _ := s.Agent(GenericAgentReview)
	if projectCopy.Prompt != "project copy" || projectCopy.ProjectID != "skillhub" {
		t.Fatalf("project catalog role was overwritten: %+v", projectCopy)
	}
	if _, err := s.UpdateGenericAgent(GenericBackendDeliveryWorkflowID, domain.AgentDefinition{ID: "skillhub-reviewer"}); err == nil {
		t.Fatal("updating a role that is not on the template must fail")
	}
}

func TestUpdateGenericAgentRejectsStaleWrite(t *testing.T) {
	s := testStoreWithGenericTemplates(t)
	base, ok := s.GenericAgentByID(GenericAgentReview)
	if !ok {
		t.Fatal("review role missing")
	}
	first, second := base, base
	first.Description = "先保存的人写的"
	first.Version = base.Version
	saved, err := s.UpdateGenericAgent(GenericBackendDeliveryWorkflowID, first)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != base.Version+1 {
		t.Fatalf("version = %d, want %d", saved.Version, base.Version+1)
	}
	second.Description = "后保存的人写的"
	second.Version = base.Version
	if _, err := s.UpdateGenericAgent(GenericBackendDeliveryWorkflowID, second); err == nil {
		t.Fatal("a stale write silently overwrote the other admin's edit")
	}
	got, _ := s.GenericAgentByID(GenericAgentReview)
	if got.Description != "先保存的人写的" {
		t.Fatalf("stored description = %q", got.Description)
	}
}

func TestUpdateGenericAgentAcceptsUnversionedWrite(t *testing.T) {
	s := testStoreWithGenericTemplates(t)
	base, _ := s.GenericAgentByID(GenericAgentReview)
	patch := base
	patch.Version = 0
	patch.Description = "脚本改的"
	saved, err := s.UpdateGenericAgent(GenericBackendDeliveryWorkflowID, patch)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != base.Version+1 {
		t.Fatalf("version = %d, want %d", saved.Version, base.Version+1)
	}
}

func TestPublishGenericWorkflow(t *testing.T) {
	s := testStoreWithGenericTemplates(t)
	before, _ := s.GenericWorkflowByID(GenericBackendDeliveryWorkflowID)
	before.Description += " updated"
	if _, err := s.UpdateGenericWorkflow(before); err != nil {
		t.Fatal(err)
	}
	published, err := s.PublishGenericWorkflow(GenericBackendDeliveryWorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != "published" || published.Version != before.Version+1 {
		t.Fatalf("publish = %+v", published)
	}
}

func TestWorkflowNamesUsingAgentSkipsGenericTemplateShadows(t *testing.T) {
	s := testStoreWithGenericTemplates(t)
	template, ok := s.GenericWorkflowByID(GenericBackendDeliveryWorkflowID)
	if !ok || len(template.Nodes) == 0 {
		t.Fatal("backend template has no node")
	}
	roleID := template.Nodes[0].AgentID
	if _, ok := s.GenericAgentByID(roleID); !ok {
		t.Fatalf("template role %q missing", roleID)
	}
	// Adopting a template clones its roles into a project and often keeps the
	// original id. Generic templates and other projects' LOOPs run the built-in
	// role, so they are not references to the project clone.
	projectRole := domain.AgentDefinition{ID: roleID, Name: "本项目角色", ProjectID: "project-1", ExecutorID: "codebuddy"}
	if err := s.PutAgent(projectRole); err != nil {
		t.Fatal(err)
	}
	if names := s.WorkflowNamesUsingAgent(projectRole); len(names) != 0 {
		t.Fatalf("shadowed project role reported as referenced: %v", names)
	}
	own := domain.WorkflowDefinition{ID: "project-flow", Name: "本项目交付", ProjectID: "project-1", Status: "published",
		Nodes: []domain.WorkflowNode{{ID: "work", AgentID: roleID}}}
	if err := s.PutWorkflow(own); err != nil {
		t.Fatal(err)
	}
	if names := s.WorkflowNamesUsingAgent(projectRole); len(names) != 1 || names[0] != "本项目交付" {
		t.Fatalf("own-project reference missing: %v", names)
	}
	// A platform role with the same id resolves in every LOOP.
	if names := s.WorkflowNamesUsingAgent(domain.AgentDefinition{ID: roleID, Name: "平台角色"}); len(names) < 2 {
		t.Fatalf("platform role lost template references: %v", names)
	}
}
