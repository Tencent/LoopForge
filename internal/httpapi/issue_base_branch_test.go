package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestNormalizeIssueBaseBranch(t *testing.T) {
	for _, branch := range []string{"", "feature/continue-work", "release/2026.10", "修复/套餐", "origin/valid-branch", "@"} {
		got, err := normalizeIssueBaseBranch("  " + branch + "  ")
		if err != nil || got != branch {
			t.Errorf("branch %q: got %q, %v", branch, got, err)
		}
	}
	for _, branch := range []string{"HEAD", "--upload-pack=echo", "feature name", "feature\nname", "feature\x7fname", "foo..bar", "foo@{1}", "foo~1", "foo^", "foo:bar", "foo?", "foo*", "foo[bar", `foo\bar`, "/feature", "feature/", "feature//foo", ".feature", "feature/.hidden", "feature/foo.lock/bar", "feature/foo."} {
		if _, err := normalizeIssueBaseBranch(branch); err == nil {
			t.Errorf("invalid branch %q was accepted", branch)
		}
	}
}

func TestIssueBaseBranchCreateEditAndDefault(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	repository := domain.Repository{ID: "branch-repo", ProjectID: "platform", Name: "Branch repo", DefaultBranch: "main"}
	if err := st.PutRepository(repository); err != nil {
		t.Fatal(err)
	}
	h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	created := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "platform", "title": "继续已有开发", "repositoryId": repository.ID, "baseBranch": " feature/continue-work ",
	}, http.StatusOK)
	if created.BaseBranch != "feature/continue-work" {
		t.Fatalf("custom base branch not persisted: %+v", created)
	}
	if _, err := st.PatchIssue(created.ID, func(issue *domain.Issue) error {
		issue.Clarification.SessionID = "original-branch-session"
		issue.Clarification.SessionUserID = 42
		issue.Clarification.Answers = map[string]string{"scope": "保留业务范围"}
		issue.Clarification.History = []domain.ClarificationMessage{{Role: "user", Content: "保留业务回答"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	edited := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": created.ID, "title": "继续已有开发并补充测试",
	}, http.StatusOK)
	if edited.BaseBranch != created.BaseBranch || edited.Clarification.SessionID != "original-branch-session" {
		t.Fatalf("omitted baseBranch must preserve existing selection: %+v", edited)
	}
	reset := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": created.ID, "baseBranch": "",
	}, http.StatusOK)
	if reset.BaseBranch != "" {
		t.Fatalf("explicit blank must restore dynamic default: %+v", reset)
	}
	if reset.Clarification.SessionID != "" || reset.Clarification.SessionUserID != 0 || reset.Clarification.Answers["scope"] != "保留业务范围" || len(reset.Clarification.History) != 1 {
		t.Fatalf("branch changes must discard the code session and preserve business answers: %+v", reset.Clarification)
	}
	if len(reset.Operations) == 0 || !strings.Contains(reset.Operations[len(reset.Operations)-1].Detail, "工作分支改为自动新建") {
		t.Fatalf("base branch change missing from audit log: %+v", reset.Operations)
	}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "platform", "title": "非法基线", "repositoryId": repository.ID, "baseBranch": "feature/foo~1",
	}, http.StatusBadRequest)
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "platform", "title": "没有仓库", "baseBranch": "feature/continue-work",
	}, http.StatusBadRequest)
}

func TestIssueBaseBranchRejectsMultiRepoCustomAndKeepsLegacyDefault(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, repository := range []domain.Repository{
		{ID: "branch-primary", ProjectID: "platform", Name: "Primary", DefaultBranch: "main"},
		{ID: "branch-secondary", ProjectID: "platform", Name: "Secondary", DefaultBranch: "release"},
	} {
		if err := st.PutRepository(repository); err != nil {
			t.Fatal(err)
		}
	}
	h := &issuesHandlers{deps: &deps{repo: st}}
	issue := domain.Issue{ProjectID: "platform", RepositoryIDs: []string{"branch-primary", "branch-secondary"}, BaseBranch: "feature/continue-work"}
	if err := h.bindIssueRepositories(&issue); err == nil || !strings.Contains(err.Error(), "仅支持单个") {
		t.Fatalf("multi-repo custom branch must not be ignored: %v", err)
	}
	issue.BaseBranch = "main"
	h.normalizeLegacyIssueBaseBranch(&issue)
	if err := h.bindIssueRepositories(&issue); err != nil || issue.BaseBranch != "" {
		t.Fatalf("legacy first-repo default should remain editable: %v", err)
	}
	issue.BaseBranch = ""
	if err := h.bindIssueRepositories(&issue); err != nil || issue.BaseBranch != "" {
		t.Fatalf("empty base should use each repo default dynamically: %+v, %v", issue, err)
	}
}

func TestImportTAPDBaseBranchSelectionAndReimport(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{StaffID: 3173, LoginName: "branch-importer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := st.UserByLoginName("branch-importer")
	if _, err := st.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{TaihuPAT: "tai_pat_mock_import"}); err != nil {
		t.Fatal(err)
	}
	project, _ := st.Project("platform")
	project.OwnerID = owner.ID
	if err := st.PutProject(project); err != nil {
		t.Fatal(err)
	}
	repoID := putPlatformImportRepo(t, st)
	h := newTAPDImportHandler(t, st, t.TempDir(), `{"data":[{"id":"1010000000100001332","name":"继续已有分支开发","status":"open"}]}`)
	payload := map[string]any{"projectId": "platform", "repositoryId": repoID,
		"url": "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001332", "baseBranch": "--invalid"}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusBadRequest)
	payload["baseBranch"] = "master"
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusBadRequest)
	for _, issue := range st.Issues() {
		if issue.ExternalRef != nil && issue.ExternalRef.ExternalID == "1010000000100001332" {
			t.Fatal("invalid branch must be rejected before importing or persisting TAPD data")
		}
	}
	payload["baseBranch"] = "feature/continue-work"
	imported := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if imported.BaseBranch != "feature/continue-work" {
		t.Fatalf("import must bind the selected base: %+v", imported)
	}
	if _, err := st.PatchIssue(imported.ID, func(issue *domain.Issue) error {
		issue.Clarification.SessionID = "imported-branch-session"
		issue.Clarification.SessionUserID = owner.ID
		issue.Clarification.Answers = map[string]string{"scope": "保留导入回答"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	delete(payload, "baseBranch")
	kept := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if kept.BaseBranch != imported.BaseBranch || kept.Clarification.SessionID != "imported-branch-session" {
		t.Fatalf("reimport without baseBranch must preserve existing selection: %+v", kept)
	}
	payload["baseBranch"] = "release/2026"
	changed := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if changed.BaseBranch != "release/2026" {
		t.Fatalf("reimport must apply an explicit changed selection: %+v", changed)
	}
	if changed.Clarification.SessionID != "" || changed.Clarification.SessionUserID != 0 || changed.Clarification.Answers["scope"] != "保留导入回答" {
		t.Fatalf("reimported branch changes must invalidate the code session only: %+v", changed.Clarification)
	}
	payload["baseBranch"] = ""
	reset := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if reset.BaseBranch != "" {
		t.Fatalf("reimport explicit blank must reset the baseline: %+v", reset)
	}
	if _, err := st.PatchIssue(imported.ID, func(issue *domain.Issue) error {
		issue.BaseBranch = "master"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	delete(payload, "baseBranch")
	legacy := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", payload, http.StatusOK)
	if legacy.BaseBranch != "" {
		t.Fatalf("reimport without an explicit selection must normalize the legacy main branch: %+v", legacy)
	}
}

func TestDispatchCustomBaseBranchSkipsScheduledIntegration(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutRepository(domain.Repository{ID: "dispatch-branch-repo", ProjectID: "platform", Name: "Repo", DefaultBranch: "master", IntegrateBranch: "agentflow/integration"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutWorkflow(domain.WorkflowDefinition{ID: "branch-flow", Name: "Branch flow", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval"}}}); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	d := &deps{repo: st, engine: eng, sync: tracker.NewService()}
	for _, scheduled := range []bool{false, true} {
		id := "custom-manual"
		req := dispatchRequest{IssueID: id}
		if scheduled {
			id, req.IssueID, req.ScheduleJobID = "custom-scheduled", "custom-scheduled", "branch-job"
		}
		if err := st.PutIssue(domain.Issue{ID: id, ProjectID: "platform", Title: id, Kind: "issue", RepositoryID: "dispatch-branch-repo", BaseBranch: "feature/continue-work", WorkflowID: "branch-flow"}); err != nil {
			t.Fatal(err)
		}
		task, err := dispatchIssue(d, req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = eng.Cancel(task.RunID) })
		run, _ := st.Run(task.RunID)
		if task.Repository.BaseBranch != "master" || run.Repository.BaseBranch != "master" || run.Repository.WorkBranch != "feature/continue-work" || !run.Repository.RequireExistingWorkBranch || run.Repository.IntegrateBranch != "" {
			t.Fatalf("custom selection was overridden: task=%+v run=%+v", task.Repository, run.Repository)
		}
		if !strings.Contains(run.Input, "merge request target branch: master") || !strings.Contains(run.Input, "existing work branch: feature/continue-work") || !strings.Contains(run.Input, "do not create or rename") {
			t.Fatalf("dispatch must continue the existing branch and target configured main: %s", run.Input)
		}
	}
	if err := st.PutIssue(domain.Issue{ID: "invalid-branch-dispatch", ProjectID: "platform", Title: "invalid", Kind: "issue", RepositoryID: "dispatch-branch-repo", BaseBranch: "feature/foo~1", WorkflowID: "branch-flow"}); err != nil {
		t.Fatal(err)
	}
	_, err = dispatchIssue(d, dispatchRequest{IssueID: "invalid-branch-dispatch"})
	var he HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusBadRequest {
		t.Fatalf("dispatch must reject invalid persisted branch: %v", err)
	}
	if st.IssueHasTask("invalid-branch-dispatch") {
		t.Fatal("invalid branch must be rejected before task creation")
	}
}

func TestIssueRepositoryChangeInvalidatesClarifySession(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, id := range []string{"original-repo", "new-repo"} {
		if err := st.PutRepository(domain.Repository{ID: id, ProjectID: "platform", Name: id, DefaultBranch: "master"}); err != nil {
			t.Fatal(err)
		}
	}
	issue := domain.Issue{ID: "repo-switch", ProjectID: "platform", Title: "更换分析仓库", RepositoryID: "original-repo", BaseBranch: "feature/continue",
		Clarification: domain.ClarificationAssessment{SessionID: "original-repo-session", SessionUserID: 42, Answers: map[string]string{"scope": "保留回答"}}}
	if err := st.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	updated := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{"id": issue.ID, "repositoryId": "new-repo"}, http.StatusOK)
	if updated.BaseBranch != issue.BaseBranch || updated.Clarification.SessionID != "" || updated.Clarification.SessionUserID != 0 || updated.Clarification.Answers["scope"] != "保留回答" {
		t.Fatalf("repository changes must invalidate the old code session: %+v", updated)
	}
}

func TestImportTAPDRejectsInvalidLinksBeforeMCP(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	repoID := putPlatformImportRepo(t, st)
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(gateway.Close)
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "tapd-rejected-links", Name: "TAPD MCP", ProjectID: "platform", MarketID: "70", Scope: "project", Transport: "http", URL: gateway.URL}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSyncConfig(domain.SyncConfig{TAPDMCPURL: gateway.URL}); err != nil {
		t.Fatal(err)
	}
	h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	for _, tc := range []struct{ text, errorPart string }{
		{"只有标题，没有链接", "无法识别"},
		{"https://example.test/tapd_fe/10000000/story/detail/1010000000100001332", "tapd.example.com"},
		{"【两个工作项】\nhttps://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001332\nhttps://tapd.example.com/tapd_fe/10000000/task/detail/1010000000100001443", "多个不同"},
	} {
		response := requestJSON[map[string]string](t, h, http.MethodPost, "/api/issues/import-tapd", map[string]any{
			"projectId": "platform", "repositoryId": repoID, "url": tc.text,
		}, http.StatusBadRequest)
		if !strings.Contains(response["error"], tc.errorPart) {
			t.Fatalf("unhelpful validation error for %q: %+v", tc.text, response)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid input must not trigger MCP calls, got %d", calls.Load())
	}
}

func TestDispatchEmptyBaseBranchUsesCurrentConfiguredMainBranch(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	repository := domain.Repository{ID: "configured-main-repo", ProjectID: "platform", Name: "Service", DefaultBranch: "release/original-main"}
	if err := st.PutRepository(repository); err != nil {
		t.Fatal(err)
	}
	if err := st.PutWorkflow(domain.WorkflowDefinition{ID: "configured-main-flow", Name: "Main branch flow", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval"}}}); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h := New(st, eng, tracker.NewService(), "", "")
	issue := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "platform", "title": "沿用项目仓库主分支", "kind": "issue", "repositoryId": repository.ID, "baseBranch": "", "workflowId": "configured-main-flow",
	}, http.StatusOK)
	if issue.BaseBranch != "" {
		t.Fatalf("blank baseline must remain dynamic: %+v", issue)
	}
	repository.DefaultBranch = "develop/current-main"
	if err := st.PutRepository(repository); err != nil {
		t.Fatal(err)
	}
	task := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]any{}, http.StatusCreated)
	t.Cleanup(func() { _ = eng.Cancel(task.RunID) })
	run, _ := st.Run(task.RunID)
	if task.Repository.BaseBranch != repository.DefaultBranch || run.Repository.BaseBranch != repository.DefaultBranch || run.Repository.DefaultBranch != repository.DefaultBranch || run.Repository.RequireExistingWorkBranch || run.Repository.WorkBranch == "" {
		t.Fatalf("blank baseline ignored current configured main branch: task=%+v run=%+v", task.Repository, run.Repository)
	}
	if !strings.Contains(run.Input, "merge request target branch: "+repository.DefaultBranch) {
		t.Fatalf("MR target ignored configured main branch: %s", run.Input)
	}
}

func TestIssueRejectsExplicitSharedWorkBranchesAndNormalizesLegacyEdits(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	repository := domain.Repository{ID: "shared-branch-repo", ProjectID: "platform", Name: "Repo", DefaultBranch: "release/main", IntegrateBranch: "agentflow/integration"}
	if err := st.PutRepository(repository); err != nil {
		t.Fatal(err)
	}
	h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	for _, branch := range []string{repository.DefaultBranch, repository.IntegrateBranch, "master", "main"} {
		response := requestJSON[map[string]string](t, h, http.MethodPost, "/api/issues", map[string]any{
			"projectId": "platform", "title": "不可直接处理共享分支", "repositoryId": repository.ID, "baseBranch": branch,
		}, http.StatusBadRequest)
		if !strings.Contains(response["error"], "共享分支") {
			t.Fatalf("shared branch must produce an actionable validation error: %+v", response)
		}
	}
	legacy := domain.Issue{ID: "legacy-default-branch", ProjectID: "platform", Title: "旧默认分支记录", RepositoryID: repository.ID, BaseBranch: repository.DefaultBranch}
	if err := st.PutIssue(legacy); err != nil {
		t.Fatal(err)
	}
	updated := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{"id": legacy.ID, "title": "仅修改标题"}, http.StatusOK)
	if updated.BaseBranch != "" {
		t.Fatalf("partial edit must normalize the legacy default baseline: %+v", updated)
	}
	requestJSON[map[string]string](t, h, http.MethodPut, "/api/issues", map[string]any{"id": legacy.ID, "baseBranch": repository.DefaultBranch}, http.StatusBadRequest)
}

func TestDispatchLegacyDefaultBranchCreatesNewWorkBranchWithoutChangingRecord(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	repository := domain.Repository{ID: "legacy-dispatch-repo", ProjectID: "platform", Name: "Repo", DefaultBranch: "release/configured-main"}
	if err := st.PutRepository(repository); err != nil {
		t.Fatal(err)
	}
	if err := st.PutWorkflow(domain.WorkflowDefinition{ID: "legacy-main-flow", Name: "Legacy main", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval"}}}); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "legacy-main", ProjectID: "platform", Title: "历史主分支", Kind: "issue", RepositoryID: repository.ID, BaseBranch: repository.DefaultBranch, WorkflowID: "legacy-main-flow"}
	if err := st.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	task, err := dispatchIssue(&deps{repo: st, engine: eng, sync: tracker.NewService()}, dispatchRequest{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Cancel(task.RunID) })
	if task.Repository.BaseBranch != repository.DefaultBranch || task.Repository.WorkBranch == "" || task.Repository.WorkBranch == repository.DefaultBranch || task.Repository.RequireExistingWorkBranch {
		t.Fatalf("legacy main value must create a fresh work branch: %+v", task.Repository)
	}
	stored, _ := st.Issue(issue.ID)
	if stored.BaseBranch != issue.BaseBranch {
		t.Fatalf("dispatch must interpret legacy values without rewriting the record: %+v", stored)
	}
}
