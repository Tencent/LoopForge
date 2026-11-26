package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// TestAnalyticsPersonalScopeResolvesCurrentUser verifies the minimal backend
// slice for #1076: personal=1 scopes analytics to the signed-in user when no
// explicit userId is supplied, while an explicit userId still wins.
func TestAnalyticsPersonalScopeResolvesCurrentUser(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "personal-scope-test-secret")
	h, owner := workbenchFixture(t)
	st := h.repo

	// A second user so the scope is observable.
	if err := st.PutUser(domain.User{LoginName: "analytics-other", CodeBuddyAPIKey: "test-only"}); err != nil {
		t.Fatal(err)
	}
	other, _ := st.UserByLoginName("analytics-other")
	now := time.Now().UTC().Add(-time.Minute)
	wf := domain.WorkflowDefinition{ID: "personal-wf", Name: "Personal", Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build"}}}
	for _, r := range []domain.Run{
		{ID: "mine", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ResponsibleUserID: owner.ID, IssueID: "iss-mine"},
			Usage:            domain.TokenUsage{TotalTokens: 10, CostUSD: 0.01, CostStatus: "reported"}, CreatedAt: now, UpdatedAt: now.Add(time.Second)},
		{ID: "theirs", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ResponsibleUserID: other.ID, IssueID: "iss-theirs"},
			Usage:            domain.TokenUsage{TotalTokens: 20, CostUSD: 0.02, CostStatus: "reported"}, CreatedAt: now, UpdatedAt: now.Add(time.Second)},
	} {
		if err := st.PutRun(r); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(st, engine.New(st, runtime.NewManager(t.TempDir())), tracker.NewService(), "", "")

	// personal=1 resolves the signed-in user; only that user's run is counted.
	got := requestJSONAuthed[domain.AnalyticsSnapshot](t, srv, owner, http.MethodGet, "/api/analytics?projectId=workbench&personal=1", nil, 200)
	if got.Filter.UserID != fmt.Sprint(owner.ID) {
		t.Fatalf("personal=1 filter.UserID = %q, want owner %d", got.Filter.UserID, owner.ID)
	}
	if got.Summary.TotalRuns != 1 || got.Summary.TotalTokens != 10 {
		t.Fatalf("personal scope did not isolate runs: runs=%d tokens=%d (filter=%+v)", got.Summary.TotalRuns, got.Summary.TotalTokens, got.Filter)
	}

	// Explicit userId wins over personal=1.
	explicit := requestJSONAuthed[domain.AnalyticsSnapshot](t, srv, owner, http.MethodGet, "/api/analytics?projectId=workbench&personal=1&userId="+fmt.Sprint(other.ID), nil, 200)
	if explicit.Filter.UserID != fmt.Sprint(other.ID) {
		t.Fatalf("explicit userId not honored: filter.UserID = %q", explicit.Filter.UserID)
	}
	if explicit.Summary.TotalRuns != 1 || explicit.Summary.TotalTokens != 20 {
		t.Fatalf("explicit userId scope wrong: runs=%d tokens=%d", explicit.Summary.TotalRuns, explicit.Summary.TotalTokens)
	}

	// Without personal, no implicit user scope (both runs visible).
	all := requestJSONAuthed[domain.AnalyticsSnapshot](t, srv, owner, http.MethodGet, "/api/analytics?projectId=workbench", nil, 200)
	if all.Filter.UserID != "" {
		t.Fatalf("non-personal request got implicit UserID %q", all.Filter.UserID)
	}
	if all.Summary.TotalRuns < 2 {
		t.Fatalf("non-personal request should include both runs, got %d", all.Summary.TotalRuns)
	}

	// An unauthenticated personal request is rejected, not silently broadened.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/analytics?projectId=workbench&personal=1", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated personal request = %d, want 401 (%s)", rec.Code, rec.Body.String())
	}
}
