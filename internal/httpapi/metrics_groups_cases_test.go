package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func cvBGinContext(t *testing.T, target string) *gin.Context {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return c
}

func cvBRawRequest(t *testing.T, f cvBHTTPFixture, method, path, body string, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authed {
		req = authedRequest(t, req, f.user)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func TestRunsRemainingPureAndHTTPBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, tc := range []struct {
		method string
		path   string
		body   any
		auth   bool
		want   int
	}{
		{http.MethodGet, "/api/analytics?from=2026-10-01T00:00:00Z&to=2026-10-30T00:00:00Z&origin=external&scope=x&userId=1&issueSource=tapd&grain=day&tz=UTC", nil, false, http.StatusOK},
		{http.MethodGet, "/api/runs?responsible=me", nil, false, http.StatusUnauthorized},
		{http.MethodPost, "/api/runs", map[string]any{}, false, http.StatusConflict},
		{http.MethodPost, "/api/runs", map[string]any{"projectId": "missing"}, true, http.StatusBadRequest},
		{http.MethodPatch, "/api/runs", nil, false, http.StatusMethodNotAllowed},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, tc.auth)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	if rec := cvBRawRequest(t, f, http.MethodPost, "/api/runs", "{", true); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed run = %d: %s", rec.Code, rec.Body.String())
	}
	runsHandler := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}}
	router := gin.New()
	router.GET("/cvb-default/:id", runsHandler.run)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cvb-default/anything", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("run default = %d", rec.Code)
	}
	wf := domain.WorkflowDefinition{
		Description: strings.Repeat("d", 300),
		Nodes:       []domain.WorkflowNode{{ID: "outer", Script: "secret", Command: "secret"}},
		SOPCheckpoint: &domain.SOPCheckpoint{
			Workflow: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "inner", Script: "secret", Command: "secret"}}},
			Nodes:    map[string]*domain.NodeRun{"inner": {NodeID: "inner"}},
		},
		GroupHistory: []domain.GroupTaskHistory{{
			Node:  domain.WorkflowNode{ID: "old", Script: "secret", Command: "secret"},
			State: domain.NodeRun{Output: strings.Repeat("o", 3000)},
		}},
	}
	slim := slimWorkflowSnapshot(wf)
	if slim.Nodes[0].Script != "" || slim.SOPCheckpoint.Nodes != nil || slim.SOPCheckpoint.Workflow.Nodes[0].Command != "" ||
		slim.GroupHistory[0].Node.Script != "" || len(slim.GroupHistory[0].State.Output) >= 3000 || len(slim.Description) >= 300 {
		t.Fatalf("workflow not slimmed: %+v", slim)
	}
}
