package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
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

// The review of 3819f1c9 found three writers that bypassed the save check:
// clone, revision restore and the generic-template branch of PUT. Each one
// must refuse to persist a managed node that still carries knowledge refs.
func TestManagedKnowledgeRejectedOnCloneAndRestore(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	project, _ := f.s.Project("default")
	projectID := project.ID
	role := domain.AgentDefinition{ID: "cr-role", Name: "Role", ProjectID: projectID}
	if err := f.s.PutAgent(role); err != nil {
		t.Fatal(err)
	}
	refs := []map[string]any{{"connectorId": "kc-cr", "mode": "all"}}
	body := func(revision int, knowledge []map[string]any) map[string]any {
		return map[string]any{
			"id": "cr-wf", "name": "cr-wf", "projectId": projectID, "status": "draft", "sandboxProfileId": "test-native",
			"revision": revision,
			"nodes":    []map[string]any{{"id": "n1", "name": "知识库步骤", "type": "agent", "agentId": role.ID, "knowledgeRefs": knowledge}},
			"edges":    []map[string]any{},
		}
	}
	// Saved while the role is still a CLI role: the binding is legitimate.
	rec := cvBRequest(t, f, http.MethodPut, "/api/workflows", body(0, refs), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("CLI save = %d %s", rec.Code, rec.Body.String())
	}
	withKB, _ := f.s.Workflow("cr-wf")
	rec = cvBRequest(t, f, http.MethodPut, "/api/workflows", body(withKB.Revision, nil), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("second save = %d %s", rec.Code, rec.Body.String())
	}
	var kbRevision string
	for _, rev := range f.s.Revisions("workflow", "cr-wf") {
		var payload domain.WorkflowDefinition
		if json.Unmarshal(rev.Payload, &payload) == nil && len(payload.Nodes) == 1 && len(payload.Nodes[0].KnowledgeRefs) == 1 {
			kbRevision = rev.ID
		}
	}
	if kbRevision == "" {
		t.Fatal("no revision with the knowledge binding was recorded")
	}

	// The role then becomes a managed Agent.
	role.ExecutorID = domain.A2AExecutorID
	if err := f.s.PutAgent(role); err != nil {
		t.Fatal(err)
	}

	rec = cvBRequest(t, f, http.MethodPost, "/api/workflows/cr-wf/revisions/"+kbRevision+"/restore", map[string]any{}, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "托管 Agent") {
		t.Fatalf("restore of a knowledge revision onto a managed role = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if current, _ := f.s.Workflow("cr-wf"); len(current.Nodes[0].KnowledgeRefs) != 0 {
		t.Fatalf("rejected restore still wrote the binding back: %+v", current.Nodes[0])
	}

	// A legacy row (saved before the save check existed) must not be cloned
	// into a fresh LOOP either.
	legacy := withKB
	legacy.ID, legacy.Name, legacy.Revision = "cr-legacy", "cr-legacy", 0
	if err := f.s.PutWorkflow(legacy); err != nil {
		t.Fatal(err)
	}
	rec = cvBRequest(t, f, http.MethodPost, "/api/workflows/cr-legacy/clone", map[string]any{"id": "cr-copy", "name": "copy", "projectId": projectID}, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "kc-cr") {
		t.Fatalf("clone of a managed node with knowledge = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if _, exists := f.s.Workflow("cr-copy"); exists {
		t.Fatal("rejected clone was persisted")
	}
	// Without the binding the same LOOP still clones.
	rec = cvBRequest(t, f, http.MethodPost, "/api/workflows/cr-wf/clone", map[string]any{"id": "cr-copy-ok", "name": "copy", "projectId": projectID}, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("clone without knowledge = %d %s, want 201", rec.Code, rec.Body.String())
	}
}

func TestGenericTemplateSaveAndCloneRejectManagedKnowledge(t *testing.T) {
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "example-admin")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "generic-admin-session-secret")
	h, s := newGenericTemplateServer(t)
	if err := s.PutUser(domain.User{LoginName: "example-admin", ChineseName: "Admin"}); err != nil {
		t.Fatal(err)
	}
	admin, _ := s.UserByLoginName("example-admin")

	// Adopting once leaves a CLI copy of the MR role in the catalog under the
	// template's role id — the lookup the old validator preferred.
	requestJSON[domain.WorkflowDefinition](t, h, http.MethodPost, "/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
		map[string]any{"id": "alpha-backend", "name": "后端", "projectId": "alpha"}, http.StatusCreated)
	if catalog, ok := s.Agent(repo.GenericAgentMR); !ok || domain.IsManagedAgentExecutor(catalog.ExecutorID) {
		t.Fatalf("adopted MR role should be a CLI catalog role: %+v", catalog)
	}

	template, _ := s.GenericTemplateByWorkflow(repo.GenericBackendDeliveryWorkflowID)
	for i := range template.Agents {
		if template.Agents[i].ID == repo.GenericAgentMR {
			template.Agents[i].ExecutorID = domain.WMAExecutorID
		}
	}
	if err := s.PutGenericTemplate(template); err != nil {
		t.Fatal(err)
	}
	detail := requestJSONAuthed[domain.WorkflowDefinition](t, h, admin, http.MethodGet,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID, nil, http.StatusOK)
	bound := false
	for i := range detail.Nodes {
		if detail.Nodes[i].AgentID == repo.GenericAgentMR {
			detail.Nodes[i].KnowledgeRefs = []domain.KnowledgeRef{{ConnectorID: "kc-generic", Mode: "all"}}
			bound = true
		}
	}
	if !bound {
		t.Fatal("template has no MR node")
	}
	for _, generic := range []bool{true, false} {
		payload := detail
		payload.Generic = generic
		rec := requestJSONAuthed[map[string]any](t, h, admin, http.MethodPut, "/api/workflows", payload, http.StatusBadRequest)
		if msg, _ := rec["error"].(string); !strings.Contains(msg, "kc-generic") {
			t.Fatalf("generic=%v template save error = %v, want the managed knowledge rejection", generic, rec)
		}
	}
	stored, _ := s.GenericTemplateByWorkflow(repo.GenericBackendDeliveryWorkflowID)
	for _, node := range stored.Workflow.Nodes {
		if len(node.KnowledgeRefs) != 0 {
			t.Fatalf("rejected template save persisted a binding: %+v", node)
		}
	}

	// A template whose packaged managed role already carries a binding must
	// not be adopted into a project either, and must not pre-create roles.
	stored.Workflow.Nodes = detail.Nodes
	if err := s.PutGenericTemplate(stored); err != nil {
		t.Fatal(err)
	}
	rec := requestJSON[map[string]any](t, h, http.MethodPost, "/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
		map[string]any{"id": "beta-backend", "name": "后端", "projectId": "beta"}, http.StatusBadRequest)
	if msg, _ := rec["error"].(string); !strings.Contains(msg, "kc-generic") {
		t.Fatalf("generic clone error = %v", rec)
	}
	if _, exists := s.Workflow("beta-backend"); exists {
		t.Fatal("rejected adoption was persisted")
	}
}
