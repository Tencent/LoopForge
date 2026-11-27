package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestWorkflowManagedKnowledgeValidation(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	project, _ := f.s.Project("default")
	projectID := project.ID

	for _, role := range []domain.AgentDefinition{
		{ID: "mk-cli", Name: "CLI role", ProjectID: projectID},
		{ID: "mk-a2a", Name: "A2A role", ProjectID: projectID, ExecutorID: domain.A2AExecutorID},
		{ID: "mk-wma", Name: "WMA role", ProjectID: projectID, ExecutorID: domain.WMAExecutorID},
	} {
		if err := f.s.PutAgent(role); err != nil {
			t.Fatal(err)
		}
	}
	refs := []domain.KnowledgeRef{{ConnectorID: "kc-mk", Mode: "all"}}

	cases := []struct {
		name     string
		workflow domain.WorkflowDefinition
		wantErr  []string
	}{
		{
			name: "managed role with a node knowledge binding is rejected at save",
			workflow: domain.WorkflowDefinition{ID: "mk-a2a-wf", ProjectID: projectID, Nodes: []domain.WorkflowNode{
				{ID: "n1", Name: "A2A step", Type: "agent", AgentID: "mk-a2a", KnowledgeRefs: refs},
			}},
			wantErr: []string{"A2A step", "托管 Agent", "kc-mk"},
		},
		{
			name: "WMA role with a node knowledge binding is rejected at save",
			workflow: domain.WorkflowDefinition{ID: "mk-wma-wf", ProjectID: projectID, Nodes: []domain.WorkflowNode{
				{ID: "n1", Name: "WMA step", Type: "agent", AgentID: "mk-wma", KnowledgeRefs: refs},
			}},
			wantErr: []string{"WMA step", "托管 Agent"},
		},
		{
			name: "CLI role keeps node knowledge bindings",
			workflow: domain.WorkflowDefinition{ID: "mk-cli-wf", ProjectID: projectID, Nodes: []domain.WorkflowNode{
				{ID: "n1", Name: "CLI step", Type: "agent", AgentID: "mk-cli", KnowledgeRefs: refs},
			}},
		},
		{
			name: "managed role without knowledge bindings is unaffected",
			workflow: domain.WorkflowDefinition{ID: "mk-plain-wf", ProjectID: projectID, Nodes: []domain.WorkflowNode{
				{ID: "n1", Name: "Plain A2A", Type: "agent", AgentID: "mk-a2a"},
			}},
		},
		{
			name: "non-agent nodes never carry knowledge refs",
			workflow: domain.WorkflowDefinition{ID: "mk-script-wf", ProjectID: projectID, Nodes: []domain.WorkflowNode{
				{ID: "n1", Name: "Script", Type: "script"},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkflowManagedKnowledge(f.s, tc.workflow)
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("validateWorkflowManagedKnowledge() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateWorkflowManagedKnowledge() = nil, want a rejection")
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q missing %q", err.Error(), want)
				}
			}
		})
	}

	// Run-level pins reach every node, so one managed node makes the whole pin
	// unusable; workflows made only of CLI nodes keep accepting the pin.
	pin := []domain.KnowledgeRef{{ConnectorID: "kc-pin", Mode: "all"}}
	mixed := domain.WorkflowDefinition{ID: "mk-mixed-wf", ProjectID: projectID, Nodes: []domain.WorkflowNode{
		{ID: "n1", Name: "CLI step", Type: "agent", AgentID: "mk-cli"},
		{ID: "n2", Name: "A2A step", Type: "agent", AgentID: "mk-a2a"},
	}}
	if err := validateRunManagedKnowledge(f.s, mixed, pin); err == nil || !strings.Contains(err.Error(), "kc-pin") {
		t.Fatalf("run-level pin on a workflow with a managed node = %v, want rejection naming kc-pin", err)
	}
	cliOnly := domain.WorkflowDefinition{ID: "mk-cli-only-wf", ProjectID: projectID, Nodes: []domain.WorkflowNode{
		{ID: "n1", Name: "CLI step", Type: "agent", AgentID: "mk-cli"},
	}}
	if err := validateRunManagedKnowledge(f.s, cliOnly, pin); err != nil {
		t.Fatalf("run-level pin on CLI-only workflow = %v, want nil", err)
	}
	if err := validateRunManagedKnowledge(f.s, mixed, nil); err != nil {
		t.Fatalf("run without a pin must not be rejected: %v", err)
	}
	if err := validateRunManagedKnowledge(f.s, mixed, []domain.KnowledgeRef{}); err != nil {
		t.Fatalf("explicit empty pin must not be rejected: %v", err)
	}
}

func TestSaveWorkflowRejectsManagedNodeKnowledgeBinding(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	project, _ := f.s.Project("default")
	projectID := project.ID
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "sk-a2a", Name: "A2A", ProjectID: projectID, ExecutorID: domain.A2AExecutorID}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "sk-cli", Name: "CLI", ProjectID: projectID}); err != nil {
		t.Fatal(err)
	}
	workflow := func(id, agentID string) map[string]any {
		return map[string]any{
			"id": id, "name": id, "projectId": projectID, "status": "draft", "sandboxProfileId": "test-native",
			"nodes": []map[string]any{{
				"id": "n1", "name": "绑定知识库的步骤", "type": "agent", "agentId": agentID,
				"knowledgeRefs": []map[string]any{{"connectorId": "kc-save", "mode": "all"}},
			}},
			"edges": []map[string]any{},
		}
	}

	rec := cvBRequest(t, f, http.MethodPut, "/api/workflows", workflow("sk-a2a-wf", "sk-a2a"), true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "托管 Agent") {
		t.Fatalf("managed-agent workflow save = %d %s, want 400 naming the managed Agent", rec.Code, rec.Body.String())
	}
	if _, stored := f.s.Workflow("sk-a2a-wf"); stored {
		t.Fatal("rejected workflow was persisted")
	}

	rec = cvBRequest(t, f, http.MethodPut, "/api/workflows", workflow("sk-cli-wf", "sk-cli"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("CLI workflow save = %d %s, want 200", rec.Code, rec.Body.String())
	}
	stored, ok := f.s.Workflow("sk-cli-wf")
	if !ok || len(stored.Nodes) != 1 || len(stored.Nodes[0].KnowledgeRefs) != 1 {
		t.Fatalf("CLI workflow knowledge binding not persisted: %+v", stored.Nodes)
	}
}
