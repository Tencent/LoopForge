package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// The workflows handler now routes both save paths through
// respondWorkflowSaveError and requireWorkflowSandbox. These guard the parts
// the PR tests leave open: only revision conflicts map to 409, the generic
// template path keeps its 409, and a blank-but-not-empty sandbox id is still
// reported as missing rather than "not found".

func TestRespondWorkflowSaveErrorOnlyMapsRevisionConflictTo409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"revision conflict", errors.New("workflow revision conflict: current revision is 3"), http.StatusConflict, "workflow revision conflict: current revision is 3"},
		{"plain error falls back to respond", errors.New("disk full"), http.StatusBadRequest, "disk full"},
		{"typed http error keeps its status", HTTPError{Status: http.StatusForbidden, Message: "forbidden"}, http.StatusForbidden, "forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			respondWorkflowSaveError(c, tc.err)
			if rec.Code != tc.code {
				t.Fatalf("status = %d want %d: %s", rec.Code, tc.code, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != tc.msg {
				t.Fatalf("error = %q want %q", body["error"], tc.msg)
			}
		})
	}
}

func TestGenericWorkflowUpdateRevisionConflictReturns409(t *testing.T) {
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "example-admin")
	f := cvBNewHTTPFixture(t)
	if err := repo.SeedTestGenericTemplates(f.s); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutUser(domain.User{LoginName: "example-admin", ChineseName: "Admin"}); err != nil {
		t.Fatal(err)
	}
	admin, _ := f.s.UserByLoginName("example-admin")
	f.user = admin

	template, ok := f.s.GenericTemplateByWorkflow(repo.GenericBackendDeliveryWorkflowID)
	if !ok {
		t.Fatal("generic template not seeded")
	}
	stale := template.Workflow
	if strings.TrimSpace(stale.SandboxProfileID) == "" {
		t.Fatal("seeded generic workflow has no sandbox profile")
	}
	current := stale
	current.Name += " current"
	if rec := cvBRequest(t, f, http.MethodPut, "/api/workflows", current, true); rec.Code != http.StatusOK {
		t.Fatalf("generic update = %d: %s", rec.Code, rec.Body.String())
	}
	stale.Name += " stale"
	rec := cvBRequest(t, f, http.MethodPut, "/api/workflows", stale, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "revision conflict") {
		t.Fatalf("stale generic update = %d want 409 revision conflict: %s", rec.Code, rec.Body.String())
	}
}

func TestWorkflowSaveTreatsBlankSandboxProfileIDAsMissing(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "blank-box", Key: "BLANK", Name: "Blank"}); err != nil {
		t.Fatal(err)
	}
	rec := cvBRequest(t, f, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "blank-box-flow", Name: "Blank flow", ProjectID: "blank-box", SandboxProfileID: "   ",
	}, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "workflow sandboxProfileId is required") {
		t.Fatalf("blank sandboxProfileId = %d: %s", rec.Code, rec.Body.String())
	}
	if _, exists := f.s.Workflow("blank-box-flow"); exists {
		t.Fatal("rejected workflow must not be stored")
	}
}
