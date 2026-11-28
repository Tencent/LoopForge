package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestStartProjectSquad(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	f := cvBNewHTTPFixture(t)
	f.e.ConfigureExecution("api", "api", time.Minute, false)
	viewer := domain.User{ID: 91, LoginName: "squad-viewer"}
	if err := f.s.PutUser(viewer); err != nil {
		t.Fatal(err)
	}
	project := domain.Project{ID: "squad-project", Name: "Squad", OwnerID: f.user.ID,
		Members: []domain.ProjectMember{{UserID: viewer.ID, Role: domain.ProjectRoleViewer}},
	}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "another-private", Name: "Private", ProjectID: "another"}); err != nil {
		t.Fatal(err)
	}
	start := func(roles []string, budget int, workflow string) (int, []byte) {
		req := map[string]any{"projectId": project.ID, "workflowId": workflow, "input": "Review a receipt", "squad": domain.StartSquadRequest{MemberIDs: roles, MaxTurns: budget, SandboxProfileID: repo.TestNativeSandboxID}}
		rec := cvBRequest(t, f, http.MethodPost, "/api/runs", req, true)
		return rec.Code, rec.Body.Bytes()
	}
	for _, tc := range []struct {
		members  []string
		budget   int
		workflow string
	}{
		{[]string{"developer"}, 3, ""},
		{[]string{"developer", "developer"}, 3, ""},
		{[]string{"developer", "another-private"}, 3, ""},
		{[]string{"developer", "missing"}, 3, ""},
		{[]string{"developer", "reviewer"}, 17, ""},
		{[]string{"developer", "reviewer"}, 3, "some-loop"},
	} {
		if code, body := start(tc.members, tc.budget, tc.workflow); code/100 == 2 {
			t.Fatalf("accepted invalid request: %s", body)
		}
	}
	code, body := start([]string{"reviewer", "developer"}, 0, "")
	if code != 201 {
		t.Fatalf("start: %d %s", code, body)
	}
	var run domain.Run
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatal(err)
	}
	snapshot := run.WorkflowSnapshot
	if snapshot.Squad == nil || snapshot.Squad.MaxTurns != 6 || len(snapshot.Nodes) != 1 || snapshot.Nodes[0].AgentID != "reviewer" || len(snapshot.ExecutionRoles()) != 2 || run.ExecutionContext.ResponsibleUserID != f.user.ID {
		t.Fatalf("unexpected admitted squad: %s", body)
	}
	if _, exists := f.s.Workflow(snapshot.ID); exists {
		t.Fatal("ad hoc run became a catalog template")
	}

	f.user = viewer
	if code, body := start([]string{"reviewer", "developer"}, 2, ""); code != http.StatusForbidden {
		t.Fatalf("viewer start: %d %s", code, body)
	}
}
