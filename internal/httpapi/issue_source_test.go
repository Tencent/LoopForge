package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// The AI assistant edits an issue by echoing it back through issues_save and
// drafted "source":"local" for a synced GitHub issue when only asked to change
// its priority. The edit relabelled the issue 平台新建 while its externalRef
// still pointed at GitHub, so a closed GitHub issue no longer counted as
// tracker-closed until the next pull. Where an item came from is not editable.
func TestIssueEditKeepsWhereTheIssueCameFrom(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "p", Key: "P", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	synced := domain.Issue{ID: "gh-r-1", ProjectID: "p", Number: 1, Kind: "issue", Title: "days_in_month", Status: "todo", Priority: "medium",
		Source: "github", ExternalRef: &domain.ExternalRef{Source: "github", ExternalID: "10", ExternalIID: 1, ExternalState: "closed", ExternalType: "issue"}}
	if err := st.PutIssue(synced); err != nil {
		t.Fatal(err)
	}
	h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	edited := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": synced.ID, "projectId": "p", "title": synced.Title, "priority": "high", "source": "local",
	}, http.StatusOK)
	stored, _ := st.Issue(synced.ID)
	for _, got := range []domain.Issue{edited, stored} {
		if got.Source != "github" || got.Priority != "high" || !got.TrackerClosed() {
			t.Fatalf("edit relabelled a GitHub issue: source=%q priority=%q trackerClosed=%v", got.Source, got.Priority, got.TrackerClosed())
		}
	}

	local := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{"projectId": "p", "title": "平台需求"}, http.StatusOK)
	forged := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": local.ID, "projectId": "p", "title": local.Title, "source": "github",
	}, http.StatusOK)
	if local.Source != "local" || forged.Source != "local" {
		t.Fatalf("a platform requirement became %q after an edit claiming GitHub", forged.Source)
	}
}
