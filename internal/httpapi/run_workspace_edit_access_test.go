package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"net/http"
	"testing"
)

func TestWorkspaceEditAllowedCoversProjectRoles(t *testing.T) {
	h, owner := workbenchFixture(t)
	users := map[string]domain.User{"owner": owner}
	for _, name := range []string{"editor", "viewer", "outsider", "admin"} {
		if err := h.repo.PutUser(domain.User{LoginName: "ws-" + name}); err != nil {
			t.Fatal(err)
		}
		users[name], _ = h.repo.UserByLoginName("ws-" + name)
	}
	if _, err := h.repo.GrantPlatformAdmin(users["admin"].LoginName); err != nil {
		t.Fatal(err)
	}
	project, _ := h.repo.Project("workbench")
	project.Members = []domain.ProjectMember{{UserID: users["editor"].ID, Role: "member"}, {UserID: users["viewer"].ID, Role: "Viewer"}}
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutProject(domain.Project{ID: "unowned", Name: "Unowned"}); err != nil {
		t.Fatal(err)
	}
	runs := &runsHandlers{deps: h.deps}
	runIn := func(projectID string) domain.Run {
		return domain.Run{ID: "run-" + projectID, ExecutionContext: domain.TelemetryContext{ProjectID: projectID}}
	}
	cases := []struct {
		name, user, project string
		want                bool
	}{
		{"owner", "owner", "workbench", true},
		{"editor member", "editor", "workbench", true},
		{"viewer member is case-insensitive", "viewer", "workbench", false},
		{"outsider", "outsider", "workbench", false},
		{"platform admin", "admin", "workbench", true},
		{"unowned project without members", "outsider", "unowned", true},
		{"missing project", "outsider", "missing", false},
		{"platform admin on missing project", "admin", "missing", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := workbenchContext(users[tc.user], http.MethodGet, "/", bytes.NewBuffer(nil))
			if got := runs.workspaceEditAllowed(c.Request, runIn(tc.project), users[tc.user].ID); got != tc.want {
				t.Fatalf("workspaceEditAllowed = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWorkspaceEditAllowedGuestUserDeniedOnUnownedProject is a regression test
// for the operator-precedence IDOR vulnerability. Guest/anonymous users with
// ID 0 must NEVER gain workspace edit access, even on projects where
// OwnerID == 0, because the matching IDs would let unauthenticated sessions
// write files and execute commands in any unowned project's workspace.
func TestWorkspaceEditAllowedGuestUserDeniedOnUnownedProject(t *testing.T) {
	h, _ := workbenchFixture(t)
	if err := h.repo.PutProject(domain.Project{ID: "unowned-guest", Name: "UnownedGuest"}); err != nil {
		t.Fatal(err)
	}
	runs := &runsHandlers{deps: h.deps}
	run := domain.Run{
		ID:               "run-unowned-guest",
		ExecutionContext:  domain.TelemetryContext{ProjectID: "unowned-guest"},
	}
	// Guest user has ID 0 — same as the unowned project's OwnerID.
	// Before the fix, this returned true (the IDOR vulnerability).
	var guestUserID int64 = 0
	c, _ := workbenchContext(domain.User{ID: guestUserID}, http.MethodGet, "/", bytes.NewBuffer(nil))
	if got := runs.workspaceEditAllowed(c.Request, run, guestUserID); got {
		t.Fatalf("workspaceEditAllowed(guest, unowned) = true, want false: "+
			"guest users (ID=0) must not gain access via OwnerID==0 match")
	}
}
