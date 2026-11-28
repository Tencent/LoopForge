package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// A request without a usable identity must get a 403 when it binds project
// credentials to a LOOP; reading user.ID before checking ok used to panic.
func TestWorkflowProjectCredentialsWithoutIdentityIsForbidden(t *testing.T) {
	h, _ := workbenchFixture(t)
	catalog := &catalogHandlers{deps: h.deps}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/workflows", nil)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("validateWorkflowProjectCredentials panicked: %v", r)
		}
	}()
	ok := catalog.validateWorkflowProjectCredentials(c, domain.WorkflowDefinition{ID: "wf-cred", ProjectID: "workbench", ProjectCredentialIDs: []string{"registry"}})
	if ok || rec.Code != http.StatusForbidden {
		t.Fatalf("ok=%v status=%d, want false/403", ok, rec.Code)
	}
}

// Saving a platform-wide role keeps it platform-wide; sending a project id
// is rejected instead of silently moving the role into that project.
func TestPlatformRoleSaveStaysPlatformWide(t *testing.T) {
	s, h := authorizationTestServer(t)
	if err := s.PutAgent(domain.AgentDefinition{ID: "platform-reviewer", Name: "Reviewer", Version: 1}); err != nil {
		t.Fatal(err)
	}
	admin, owner := syncUser(t, s, "auth-admin"), syncUser(t, s, "auth-owner")
	role := map[string]any{"id": "platform-reviewer", "name": "Reviewer v2", "version": 2}

	if status := authedStatus(t, h, owner, http.MethodPut, "/api/agents", role); status != http.StatusForbidden {
		t.Fatalf("non-admin save of platform role status=%d, want 403", status)
	}
	if status := authedStatus(t, h, admin, http.MethodPut, "/api/agents", role); status != http.StatusOK {
		t.Fatalf("admin save of platform role status=%d, want 200", status)
	}
	saved, _ := s.Agent("platform-reviewer")
	if saved.ProjectID != "" || saved.Name != "Reviewer v2" {
		t.Fatalf("saved=%+v, want platform-wide role renamed", saved)
	}
	moved := map[string]any{"id": "platform-reviewer", "name": "Reviewer v3", "version": 3, "projectId": "auth-p"}
	if status := authedStatus(t, h, admin, http.MethodPut, "/api/agents", moved); status != http.StatusConflict {
		t.Fatalf("moving platform role into a project status=%d, want 409", status)
	}
	if after, _ := s.Agent("platform-reviewer"); after.ProjectID != "" {
		t.Fatalf("platform role was moved into project %q", after.ProjectID)
	}
}
