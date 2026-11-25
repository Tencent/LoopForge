package httpapi

import (
	"net/http"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Reimporting a TAPD story into a different single repository without an
// explicit branch must not carry the old repository's work branch over: the
// branch only exists in the repository it was chosen for.
func TestImportTAPDRepositorySwitchDropsPreviousBaseBranch(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, id := range []string{"switch-old-repo", "switch-new-repo"} {
		if err := st.PutRepository(domain.Repository{ID: id, ProjectID: "platform", Name: id, DefaultBranch: "master"}); err != nil {
			t.Fatal(err)
		}
	}
	const storyID = "1010000000100007160"
	h := newTAPDImportHandler(t, st, t.TempDir(), `{"data":[{"id":"`+storyID+`","name":"切换仓库重新导入","status":"open"}]}`)
	payload := map[string]any{"projectId": "platform", "repositoryId": "switch-old-repo", "baseBranch": "feature/old-repo-work",
		"url": "https://tapd.example.com/tapd_fe/10000000/story/detail/" + storyID}
	imported := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if imported.RepositoryID != "switch-old-repo" || imported.BaseBranch != "feature/old-repo-work" || imported.RepositoryBranches != nil {
		t.Fatalf("initial single-repository import must keep the selected branch: %+v", imported)
	}

	delete(payload, "baseBranch")
	payload["repositoryId"] = "switch-new-repo"
	switched := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if switched.ID != imported.ID {
		t.Fatalf("reimport must update the existing issue: got %q, want %q", switched.ID, imported.ID)
	}
	if switched.RepositoryID != "switch-new-repo" || len(switched.RepositoryIDs) > 1 {
		t.Fatalf("reimport must bind the newly selected repository: %+v", switched)
	}
	if switched.BaseBranch != "" || switched.WorkBranchForRepository("switch-new-repo") != "" {
		t.Fatalf("switching repositories without a branch must drop the old repository's branch: %+v", switched)
	}
	stored, ok := st.Issue(imported.ID)
	if !ok || stored.BaseBranch != "" || stored.RepositoryID != "switch-new-repo" {
		t.Fatalf("persisted issue kept the old repository's branch: %+v", stored)
	}
}
