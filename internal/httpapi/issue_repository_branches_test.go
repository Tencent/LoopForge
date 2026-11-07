package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func putIssueBranchRepositories(t *testing.T, st *repo.Store) []string {
	t.Helper()
	ids := []string{"branch-api", "branch-web", "branch-extra"}
	for i, id := range ids {
		if err := st.PutRepository(domain.Repository{ID: id, ProjectID: "platform", Name: id,
			DefaultBranch: fmt.Sprintf("release/main-%d", i), IntegrateBranch: fmt.Sprintf("agentflow/integration-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func TestIssueRepositoryBranchesCreateEditAndReset(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ids := putIssueBranchRepositories(t, st)
	h := New(st, engine.New(st, runtime.NewManager(t.TempDir())), tracker.NewService(), "", "")
	branches := map[string]string{ids[0]: "feature/api", ids[1]: "feature/web"}
	created := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "platform", "title": "前后端联合开发", "repositoryIds": ids[:2],
		"repositoryBranches": map[string]string{ids[0]: " feature/api ", ids[1]: "feature/web"},
	}, http.StatusOK)
	if !maps.Equal(created.RepositoryBranches, branches) || created.BaseBranch != "" {
		t.Fatalf("per-repository branches were not normalized and persisted: %+v", created)
	}
	if _, err := st.PatchIssue(created.ID, func(issue *domain.Issue) error {
		issue.Clarification.SessionID = "multi-repo-session"
		issue.Clarification.SessionUserID = 1
		issue.Clarification.Answers = map[string]string{"scope": "保留业务回答"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	edited := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": created.ID, "title": "补充需求标题",
	}, http.StatusOK)
	if !maps.Equal(edited.RepositoryBranches, branches) || edited.Clarification.SessionID != "multi-repo-session" {
		t.Fatalf("partial edits must preserve branches and their code session: %+v", edited)
	}
	branches[ids[1]] = "feature/web-v2"
	changed := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": created.ID, "repositoryBranches": branches,
	}, http.StatusOK)
	if !maps.Equal(changed.RepositoryBranches, branches) || changed.Clarification.SessionID != "" || changed.Clarification.SessionUserID != 0 || changed.Clarification.Answers["scope"] != "保留业务回答" {
		t.Fatalf("secondary branch changes must reset code context and retain business answers: %+v", changed)
	}
	if len(changed.Operations) == 0 || !strings.Contains(changed.Operations[len(changed.Operations)-1].Detail, "各仓库继续开发分支") {
		t.Fatalf("branch changes were not audited: %+v", changed.Operations)
	}
	reduced := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": created.ID, "repositoryIds": ids[:1], "repositoryId": ids[0],
	}, http.StatusOK)
	if len(reduced.RepositoryBranches) != 1 || reduced.BaseBranch != branches[ids[0]] {
		t.Fatalf("removing a repository must drop its branch and preserve single-repo compatibility: %+v", reduced)
	}
	reset := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": created.ID, "repositoryBranches": map[string]string{},
	}, http.StatusOK)
	if len(reset.RepositoryBranches) != 0 || reset.BaseBranch != "" {
		t.Fatalf("explicit empty mapping must restore automatic branches: %+v", reset)
	}
}

func TestImportTAPDRepositoryBranchesAndReimport(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	testImportTAPDRepositoryBranches(t, st)
}

func TestMySQLImportTAPDRepositoryBranches(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{ID: 1, StaffID: 1, LoginName: "branch-importer"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(domain.Project{ID: "platform", Key: "AFP", OwnerID: 1, TAPDWorkspaceID: "10000000"}); err != nil {
		t.Fatal(err)
	}
	testImportTAPDRepositoryBranches(t, st)
}

func testImportTAPDRepositoryBranches(t *testing.T, st *repo.Store) {
	t.Helper()
	ids := putIssueBranchRepositories(t, st)
	storyID := fmt.Sprint(time.Now().UnixNano())
	t.Cleanup(func() { _ = st.DeleteIssue("tapd-" + storyID) })
	h := newTAPDImportHandler(t, st, t.TempDir(), fmt.Sprintf(`{"data":[{"id":%q,"name":"多仓库需求","status":"open"}]}`, storyID))
	payload := map[string]any{"projectId": "platform", "repositoryIds": ids,
		"url": "https://tapd.example.com/tapd_fe/10000000/story/detail/" + storyID}
	for _, invalid := range []map[string]string{
		{ids[1]: "feature/web~1"}, {ids[1]: "release/main-1"}, {ids[1]: "agentflow/integration-1"},
		{"unbound-repository": "feature/other"},
	} {
		payload["repositoryBranches"] = invalid
		requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusBadRequest)
	}
	for _, issue := range st.Issues() {
		if issue.ExternalRef != nil && issue.ExternalRef.ExternalID == storyID {
			t.Fatal("invalid branch selections must be rejected before importing TAPD data")
		}
	}
	branches := map[string]string{ids[0]: "feature/api", ids[1]: "feature/web"}
	payload["repositoryBranches"] = map[string]string{ids[0]: " feature/api ", ids[1]: "feature/web", ids[2]: " "}
	imported := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if !maps.Equal(imported.RepositoryBranches, branches) || imported.WorkBranchForRepository(ids[2]) != "" {
		t.Fatalf("TAPD import lost mixed branch selections: %+v", imported)
	}
	if _, err := st.PatchIssue(imported.ID, func(issue *domain.Issue) error {
		issue.Clarification.SessionID = "imported-multi-repo-session"
		issue.Clarification.SessionUserID = 1
		issue.Clarification.Answers = map[string]string{"scope": "保留导入回答"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	delete(payload, "repositoryBranches")
	kept := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if !maps.Equal(kept.RepositoryBranches, branches) || kept.Clarification.SessionID != "imported-multi-repo-session" {
		t.Fatalf("reimport without branch selections must preserve context: %+v", kept)
	}
	branches[ids[1]] = "feature/web-v2"
	payload["repositoryBranches"] = branches
	changed := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if !maps.Equal(changed.RepositoryBranches, branches) || changed.Clarification.SessionID != "" || changed.Clarification.Answers["scope"] != "保留导入回答" {
		t.Fatalf("secondary repository branch change must invalidate reimported code context: %+v", changed)
	}
	payload["repositoryIds"] = ids[1:2]
	delete(payload, "repositoryBranches")
	reduced := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if reduced.BaseBranch != branches[ids[1]] || len(reduced.RepositoryBranches) != 1 {
		t.Fatalf("reimport must keep branches only for remaining repositories: %+v", reduced)
	}
	// Older single-repository clients can replace the selection via baseBranch.
	payload["baseBranch"] = "feature/legacy"
	legacy := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if legacy.BaseBranch != "feature/legacy" || len(legacy.RepositoryBranches) != 0 {
		t.Fatalf("legacy branch updates stopped working: %+v", legacy)
	}
	delete(payload, "baseBranch")
	payload["repositoryIds"] = ids
	expanded := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if expanded.WorkBranchForRepository(ids[1]) != "feature/legacy" || expanded.WorkBranchForRepository(ids[0]) != "" {
		t.Fatalf("adding repositories must retain the original repository's legacy branch: %+v", expanded)
	}
	payload["repositoryBranches"] = map[string]string{}
	reset := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if len(reset.RepositoryBranches) != 0 || reset.BaseBranch != "" {
		t.Fatalf("explicit empty mapping must clear all imported selections: %+v", reset)
	}
}

func TestDispatchRepositoryBranches(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ids := putIssueBranchRepositories(t, st)
	if err := st.PutWorkflow(domain.WorkflowDefinition{ID: "multi-branch-flow", Name: "Approval", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval"}}}); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	d := &deps{repo: st, engine: eng, sync: tracker.NewService()}
	for _, scheduled := range []bool{false, true} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("scheduled=%t/mixed=%t", scheduled, mixed), func(t *testing.T) {
				branches := map[string]string{ids[0]: "feature/api", ids[1]: "feature/web"}
				if mixed {
					delete(branches, ids[1])
				}
				id := fmt.Sprintf("multi-branch-%t-%t", scheduled, mixed)
				if err := st.PutIssue(domain.Issue{ID: id, ProjectID: "platform", Title: id, Kind: "issue", RepositoryID: ids[0],
					RepositoryIDs: ids[:2], RepositoryBranches: branches, WorkflowID: "multi-branch-flow"}); err != nil {
					t.Fatal(err)
				}
				req := dispatchRequest{IssueID: id}
				if scheduled {
					req.ScheduleJobID = "multi-branch-job"
				}
				task, err := dispatchIssue(d, req)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = eng.Cancel(task.RunID) })
				run, _ := st.Run(task.RunID)
				if len(run.Repositories) != 2 {
					t.Fatalf("run lost multi-repo context: %+v", run)
				}
				for _, rc := range run.Repositories {
					selected := branches[rc.RepositoryID]
					repository, _ := st.Repository(rc.RepositoryID)
					if selected != "" {
						if rc.WorkBranch != selected || !rc.RequireExistingWorkBranch || rc.BaseBranch != repository.DefaultBranch || rc.IntegrateBranch != "" {
							t.Fatalf("selected work branch was overridden: %+v", rc)
						}
						if !strings.Contains(run.Input, "existing work branch: "+selected) {
							t.Fatalf("prompt lost per-repository branch %s", selected)
						}
					} else {
						expectedBase := repository.DefaultBranch
						if scheduled {
							expectedBase = repository.IntegrateBranch
						}
						if rc.RequireExistingWorkBranch || rc.BaseBranch != expectedBase || rc.WorkBranch == "" {
							t.Fatalf("unselected repository did not create its own branch: %+v", rc)
						}
					}
				}
			})
		}
	}
	if err := st.PutIssue(domain.Issue{ID: "invalid-multi-branch", ProjectID: "platform", Title: "invalid", Kind: "issue",
		RepositoryIDs: ids[:2], RepositoryBranches: map[string]string{ids[1]: "release/main-1"}, WorkflowID: "multi-branch-flow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: "invalid-multi-branch"}); err == nil || st.IssueHasTask("invalid-multi-branch") {
		t.Fatalf("invalid stored branch selection must fail before task creation: %v", err)
	}
}

func TestComposerClarifyRepositoryBranches(t *testing.T) {
	seen := make(chan domain.Issue, 1)
	setClarifyExecForTest(func(_ context.Context, issue domain.Issue, _ chatClarifyRequest, _ func(string, any)) (any, string, map[string]any, error) {
		seen <- issue
		return map[string]any{"ready": false, "summary": "请补充验收标准"}, "", nil, nil
	})
	t.Cleanup(func() { setClarifyExecForTest(nil) })
	st, h := clarifyTestServer(t)
	ids := putIssueBranchRepositories(t, st)
	payload := map[string]any{"projectId": "platform", "repositoryIds": ids[:2], "message": "继续前后端开发",
		"repositoryBranches": map[string]string{ids[0]: " feature/api ", ids[1]: "feature/web"}}
	body, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPost, "/api/chat-clarify", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("clarification status=%d: %s", response.Code, response.Body.String())
	}
	select {
	case issue := <-seen:
		if issue.WorkBranchForRepository(ids[0]) != "feature/api" || issue.WorkBranchForRepository(ids[1]) != "feature/web" {
			t.Fatalf("clarification lost per-repository branches: %+v", issue)
		}
	default:
		t.Fatal("clarification did not execute")
	}
	payload["repositoryBranches"] = map[string]string{ids[1]: "feature/web~1"}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/chat-clarify", payload, http.StatusBadRequest)
}
