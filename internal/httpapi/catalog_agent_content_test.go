package httpapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

func TestAgentContentEditPreservesConfiguration(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	role := domain.AgentDefinition{
		ID: "designer-content-edit-reviewer", ProjectID: "platform", Name: "Reviewer", Version: 3, Shared: true,
		Description: "Original description", Prompt: "Original prompt",
		ExecutorID: "codebuddy", Model: "example-model", Effort: "high",
		Rules: []string{"style"}, Skills: []string{"review"}, MCPServers: []string{"tools"},
		KnowledgeBase: true, PlatformMCP: true,
		Loop: domain.AgentLoopPolicy{Enabled: true, MaxIterations: 2},
		Interventions: []domain.HumanInterventionPoint{
			{ID: "review", Name: "Human review", Stage: "after_execution", Instructions: "Review the report"},
		},
	}
	if err := f.s.PutAgent(role); err != nil {
		t.Fatal(err)
	}
	before, _ := f.s.Agent(role.ID)
	update := func(id, project string, updatedAt any, changes map[string]any, status int) domain.AgentDefinition {
		t.Helper()
		body := map[string]any{"id": id, "projectId": project, "updatedAt": updatedAt, "changes": changes}
		res := cvBRequest(t, f, http.MethodPut, "/api/agents", body, true)
		if res.Code != status {
			t.Fatalf("save role = %d, want %d: %s", res.Code, status, res.Body.String())
		}
		var saved domain.AgentDefinition
		if status == http.StatusOK {
			if err := json.Unmarshal(res.Body.Bytes(), &saved); err != nil {
				t.Fatal(err)
			}
		}
		return saved
	}
	changes := map[string]any{"prompt": "Updated prompt", "description": ""}
	update("missing", role.ProjectID, before.UpdatedAt, changes, http.StatusNotFound)
	update(role.ID, "other", before.UpdatedAt, changes, http.StatusConflict)
	update(role.ID, role.ProjectID, nil, changes, http.StatusBadRequest)
	update(role.ID, role.ProjectID, before.UpdatedAt, map[string]any{"projectId": "other"}, http.StatusBadRequest)

	saved := update(role.ID, role.ProjectID, before.UpdatedAt, changes, http.StatusOK)
	want := before
	want.Version++
	want.UpdatedAt, want.Prompt, want.Description = saved.UpdatedAt, "Updated prompt", ""
	if stored, _ := f.s.Agent(role.ID); !reflect.DeepEqual(saved, want) || !reflect.DeepEqual(saved, stored) {
		t.Fatalf("role = %+v, want %+v", saved, want)
	}
	update(role.ID, role.ProjectID, before.UpdatedAt, map[string]any{"name": "Stale rename"}, http.StatusConflict)
	unchanged := update(role.ID, role.ProjectID, saved.UpdatedAt, changes, http.StatusOK)
	if !reflect.DeepEqual(unchanged, saved) {
		t.Fatalf("no-op edit changed role: %+v", unchanged)
	}
	revisions := f.s.Revisions("agent", role.ID)
	if len(revisions) != 1 {
		t.Fatalf("role revisions = %d, want one", len(revisions))
	}
	var snapshot domain.AgentDefinition
	if err := json.Unmarshal(revisions[0].Payload, &snapshot); err != nil || !reflect.DeepEqual(snapshot, before) {
		t.Fatalf("prior role revision = %+v, err = %v", snapshot, err)
	}
}

func TestAgentSaveWithNullChangesIsAFullSave(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	role := domain.AgentDefinition{ID: "designer-null-changes", ProjectID: "platform", Name: "Reviewer", ExecutorID: "codebuddy", Prompt: "Original prompt"}
	if err := f.s.PutAgent(role); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"id": role.ID, "projectId": role.ProjectID, "name": "Renamed", "executorId": "codebuddy", "prompt": "Full save", "changes": nil}
	res := cvBRequest(t, f, http.MethodPut, "/api/agents", body, true)
	if res.Code != http.StatusOK {
		t.Fatalf("full save with changes:null = %d: %s", res.Code, res.Body.String())
	}
	if stored, _ := f.s.Agent(role.ID); stored.Name != "Renamed" || stored.Prompt != "Full save" {
		t.Fatalf("stored role = %+v", stored)
	}
}
