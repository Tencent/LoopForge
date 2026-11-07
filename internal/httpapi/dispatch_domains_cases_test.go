package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestDispatchEarlyErrorsAndHandoff(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	d := &deps{repo: f.s, engine: f.e}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: "missing"}); cvBHTTPStatusRound2(err) != http.StatusNotFound {
		t.Fatalf("missing issue = %v", err)
	}
	blocked := domain.Issue{ID: "cvb-r2-blocked", ProjectID: "platform", Title: "Blocked", Status: "closed", AssigneeUserID: f.user.ID}
	if err := f.s.PutIssue(blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: blocked.ID}); cvBHTTPStatusRound2(err) != http.StatusConflict {
		t.Fatalf("blocked issue = %v", err)
	}
	noOwner := domain.Issue{ID: "cvb-r2-no-owner", ProjectID: "platform", Title: "No owner", Status: "todo"}
	if err := f.s.PutIssue(noOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: noOwner.ID}); err == nil {
		t.Fatal("ownerless issue dispatched")
	}
	invalidRepo := domain.Issue{ID: "cvb-r2-invalid-repo", ProjectID: "platform", Title: "Bad repo", Status: "todo",
		Kind: "bug", AssigneeUserID: f.user.ID, WorkflowID: "default", RepositoryIDs: []string{"missing"}}
	if err := f.s.PutIssue(invalidRepo); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: invalidRepo.ID}); cvBHTTPStatusRound2(err) != http.StatusBadRequest {
		t.Fatalf("invalid repository = %v", err)
	}
	rerun := invalidRepo
	rerun.ID, rerun.RepositoryIDs = "cvb-r2-rerun", nil
	if err := f.s.PutIssue(rerun); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: rerun.ID, Rerun: true}); cvBHTTPStatusRound2(err) != http.StatusBadRequest {
		t.Fatalf("premature rerun = %v", err)
	}

	issue := domain.Issue{SOPBindings: []domain.IssueSOPBinding{
		{ID: "first", WorkflowID: "wf-one"},
		{ID: "second", WorkflowID: "wf-two", DependsOn: []string{"first"}, Handoff: []string{"plan.md"}},
	}}
	text := appendSOPHandoff(issue, domain.Project{}, "wf-two", "input")
	if !strings.Contains(text, "wf-one") || !strings.Contains(text, "plan.md") {
		t.Fatalf("handoff = %q", text)
	}
	if appendSOPHandoff(issue, domain.Project{}, "missing", "input") != "input" {
		t.Fatal("unbound workflow changed input")
	}
	if primaryTaskID("issue", "workflow") != primaryTaskID("issue", "workflow") {
		t.Fatal("primary task id is not deterministic")
	}
}

func cvBHTTPStatusRound2(err error) int {
	var httpErr HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status
	}
	return 0
}

func TestBusinessDomainHTTPValidation(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/api/business-domains", nil, 200},
		{http.MethodPut, "/api/business-domains", map[string]any{"id": "all", "name": "Bad"}, 400},
		{http.MethodPut, "/api/business-domains", map[string]any{"id": "bad space", "name": "Bad"}, 400},
		{http.MethodPut, "/api/business-domains", map[string]any{"id": "cvb-r2-domain", "name": "Domain", "managerIds": []int64{f.user.ID, f.user.ID}}, 400},
		{http.MethodPut, "/api/business-domains", map[string]any{"id": "cvb-r2-domain", "name": "Domain", "managerIds": []int64{999999}}, 400},
		{http.MethodGet, "/api/business-domain-report?businessDomainId=missing", nil, 404},
		{http.MethodGet, "/api/business-domain-report?timeZone=Invalid/Zone", nil, 400},
		{http.MethodGet, "/api/business-domain-report?grain=hour", nil, 400},
		{http.MethodGet, "/api/business-domain-report?from=bad", nil, 400},
		{http.MethodGet, "/api/business-domain-report?from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z", nil, 400},
		{http.MethodGet, "/api/business-domain-report?grain=week&timeZone=UTC", nil, 200},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	if !hasBusinessUser([]int64{f.user.ID}, f.user.ID) || hasBusinessUser([]int64{f.user.ID}, 0) {
		t.Fatal("hasBusinessUser")
	}
}
