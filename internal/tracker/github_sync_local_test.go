package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestGitHubRefreshPreservesLocalLifecycleAndPlaybook(t *testing.T) {
	for _, remoteState := range []string{"open", "closed"} {
		t.Run(remoteState, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			r := domain.Repository{ID: "sync", ProjectID: "project", Provider: "github", GongfengProjectID: "github:example/repo"}
			issue := domain.Issue{ID: "gh-sync-2", ProjectID: r.ProjectID, RepositoryID: r.ID,
				Status: "in_progress", AutomationState: "eligible", WorkflowID: "workflow",
				PlaybookID: "custom-playbook", SOPBindings: []domain.IssueSOPBinding{{WorkflowID: "custom-step"}},
				ExternalRef: &domain.ExternalRef{Source: "github", ExternalIID: 2, ExternalState: "open"}}
			if err := st.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/repos/example/repo/issues" {
					t.Errorf("unexpected request: %s", req.URL.Path)
				}
				_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 2, "id": 10, "title": "Updated remote title", "state": remoteState}})
			}))
			t.Cleanup(srv.Close)
			svc := NewService()
			svc.githubAPIBase = srv.URL
			if n, err := svc.pullGitHubIssues(st, r); err != nil || n != 1 {
				t.Fatalf("sync: %d, %v", n, err)
			}
			got, ok := st.Issue(issue.ID)
			if !ok || got.Status != issue.Status || got.CloseSource != "" || got.AutomationState != issue.AutomationState {
				t.Errorf("local lifecycle changed: %+v", got)
			}
			if got.PlaybookID != issue.PlaybookID || !reflect.DeepEqual(got.SOPBindings, issue.SOPBindings) {
				t.Errorf("local playbook lost: playbook=%q bindings=%+v", got.PlaybookID, got.SOPBindings)
			}
			if got.Title != "Updated remote title" || got.ExternalRef == nil || got.ExternalRef.ExternalState != remoteState {
				t.Errorf("remote metadata not refreshed: %+v", got)
			}
		})
	}
}
