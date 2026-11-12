package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"
)

// A real platform admin must not control the browser of someone else's
// personal workspace, while admin control over project workspaces stays.
// This pins the personal-workspace check before the platform-admin bypass.
func TestBrowserControlOfPersonalWorkspaceIgnoresPlatformAdmin(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "personal-admin"}); err != nil {
		t.Fatal(err)
	}
	admin, _ := h.repo.UserByLoginName("personal-admin")
	if _, err := h.repo.GrantPlatformAdmin(admin.LoginName); err != nil {
		t.Fatal(err)
	}
	browser := &browserHandlers{deps: h.deps}
	req := httptest.NewRequest("POST", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(admin)))
	if !browser.isPlatformAdmin(req) {
		t.Fatal("fixture: admin request not recognised as platform admin")
	}
	personal := domain.Run{ID: "personal-admin-browser", Kind: domain.RunKindWorkspace, ExecutionContext: domain.TelemetryContext{ResponsibleUserID: owner.ID}}
	if browser.canControl(req, personal, admin.ID) {
		t.Fatal("platform admin controls another user's personal workspace browser")
	}
	project := domain.Run{ID: "project-admin-browser", Kind: domain.RunKindWorkspace, ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ResponsibleUserID: owner.ID}}
	if !browser.canControl(req, project, admin.ID) {
		t.Fatal("platform admin lost control of project workspace browser")
	}
}
