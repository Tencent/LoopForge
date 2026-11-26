package httpapi

import (
	"bytes"
	"net/http"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// A shared workflow can run without a project. The empty project ID must not
// authorize every signed-in account to approve, cancel or delete the run.
func TestRunWriteGuardRequiresResponsibleUserWithoutProject(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "outsider"}); err != nil {
		t.Fatal(err)
	}
	outsider, _ := h.repo.UserByLoginName("outsider")

	run := domain.Run{
		ID:               "projectless-run",
		Status:           "waiting",
		ExecutionContext: domain.TelemetryContext{ResponsibleUserID: owner.ID},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.Use(h.runWriteGuard())
	router.POST("/api/runs/:id/approve", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	for _, tc := range []struct {
		name   string
		user   domain.User
		status int
	}{
		{"responsible user", owner, http.StatusOK},
		{"unrelated user", outsider, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := workbenchContext(tc.user, http.MethodPost, "/api/runs/projectless-run/approve", bytes.NewBufferString(`{}`))
			router.ServeHTTP(rec, c.Request)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

// Unknown ownership fails closed rather than falling back to the empty-project
// "any authenticated user" rule.
func TestRunWriteGuardRejectsProjectlessRunWithoutOwner(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	h, owner := workbenchFixture(t)
	if err := h.repo.PutRun(domain.Run{ID: "orphan-run", Status: "waiting", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(h.runWriteGuard())
	router.POST("/api/runs/:id/approve", func(c *gin.Context) { c.Status(http.StatusOK) })

	c, rec := workbenchContext(owner, http.MethodPost, "/api/runs/orphan-run/approve", bytes.NewBufferString(`{}`))
	router.ServeHTTP(rec, c.Request)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}
