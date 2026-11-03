package httpapi

import (
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestIssueRouteLifecycleBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	issue := domain.Issue{ID: "cvb-issue", ProjectID: "platform", Title: "Coverage issue", Status: "todo", Kind: "requirement", Source: "local"}
	if err := f.s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/api/issues/cvb-issue", nil, 200},
		{http.MethodPost, "/api/issues/cvb-issue/comments", map[string]any{"body": ""}, 200},
		{http.MethodPost, "/api/issues/cvb-issue/comments", map[string]any{"body": "hello"}, 200},
		{http.MethodGet, "/api/issues/cvb-issue/runs", nil, 200},
		{http.MethodPost, "/api/issues/cvb-issue/clarification", map[string]any{"ready": true}, 400},
		{http.MethodPost, "/api/issues/cvb-issue/clarification", map[string]any{"summary": "clear", "ready": true, "dimensions": map[string]int{}}, 200},
		{http.MethodPost, "/api/issues/cvb-issue/chat-clarify/cancel", nil, 200},
		{http.MethodPost, "/api/issues/cvb-issue/resync-tapd", nil, 400},
		{http.MethodGet, "/api/issues/cvb-issue/requirement", nil, 200},
		{http.MethodGet, "/api/issues/cvb-issue/sops", nil, 200},
		{http.MethodPut, "/api/issues/cvb-issue/sops", map[string]any{"playbookId": "missing"}, 400},
		{http.MethodPost, "/api/issues/cvb-issue/close", nil, 200},
		{http.MethodPost, "/api/issues/cvb-issue/close", nil, 200},
		{http.MethodPost, "/api/issues/cvb-issue/reopen", nil, 200},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%d %s %s = %d want %d: %s", i, tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/issues/cvb-issue", nil, true); rec.Code != 204 {
		t.Errorf("delete = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestClarificationSubmissionBranches(t *testing.T) {
	now := time.Now().UTC()
	base := domain.Issue{ID: "i", Title: "Issue", Status: "analyzing", Kind: "requirement"}
	if _, err := applyClarificationSubmission(base, domain.Project{}, false, clarificationSubmission{Ready: true}, now); err == nil {
		t.Fatal("empty final summary accepted")
	}
	draft, err := applyClarificationSubmission(base, domain.Project{}, false, clarificationSubmission{Summary: "draft", Ready: false}, now)
	if err != nil || draft.Clarification.Status != "in_progress" {
		t.Fatalf("draft: %+v %v", draft, err)
	}
	final, err := applyClarificationSubmission(base, domain.Project{}, false, clarificationSubmission{
		Summary: "summary", Ready: true, Dimensions: map[string]int{"goal": 100}, Missing: []string{" x ", "", "x"},
	}, now)
	if err != nil || final.Clarification.Status != domain.ClarificationReady || !strings.Contains(final.Description, "summary") {
		t.Fatalf("final: %+v %v", final, err)
	}
	retryBase := final
	retryBase.Clarification.Summary = ""
	if _, err := applyClarificationSubmission(retryBase, domain.Project{}, false, clarificationSubmission{Retry: true}, now); err == nil {
		t.Fatal("retry without summary accepted")
	}
	bug := base
	bug.Kind = "issue"
	bugFinal, err := applyClarificationSubmission(bug, domain.Project{}, true, clarificationSubmission{Summary: "bug", Ready: true}, now)
	if err != nil || !bugFinal.Clarification.Passed {
		t.Fatalf("bug clarification: %+v %v", bugFinal, err)
	}
	for _, issue := range []domain.Issue{
		{},
		{Source: "gongfeng", ExternalRef: &domain.ExternalRef{ExternalID: "1"}},
		{Source: "tapd", ExternalRef: &domain.ExternalRef{ExternalID: "2"}},
	} {
		_ = issueSyncedFromTracker(issue)
	}
}

func TestIssuesMoreDirectHelpers(t *testing.T) {
	external := &domain.ExternalRef{Source: "tapd", ExternalID: "123"}
	existing := domain.Issue{
		ID: "existing", ProjectID: "platform", Title: "title", LatestTaskID: "task",
		Source: "tapd", ExternalRef: external, RepositoryIDs: []string{"repo-a"},
		RepositoryID: "repo-a", BaseBranch: "feature/x", Kind: "requirement",
		AssigneeID: "agent", AssigneeType: "agent", AssigneeUserID: 7,
		WorkflowID: "wf", SOPBindings: []domain.IssueSOPBinding{{WorkflowID: "wf"}},
		PlaybookID: "book", Clarification: domain.ClarificationAssessment{Score: 80, Passed: true},
		Operations: []domain.IssueOperation{{Action: "create"}},
	}
	merged := mergeIssueEdit(domain.Issue{ID: "existing"}, existing)
	if merged.ProjectID != "platform" || merged.LatestTaskID != "task" || merged.ExternalRef != external ||
		merged.RepositoryID != "repo-a" || merged.AssigneeUserID != 7 || merged.PlaybookID != "book" ||
		len(merged.SOPBindings) != 1 || len(merged.Operations) != 1 {
		t.Fatalf("merge did not preserve stored fields: %+v", merged)
	}

	for _, tc := range []struct {
		user *auth.User
		want string
	}{
		{nil, ""},
		{&auth.User{ID: 1, LoginName: "alice", ChineseName: "艾丽丝"}, "alice(艾丽丝)"},
		{&auth.User{ID: 2, ChineseName: "中文"}, "中文"},
		{&auth.User{ID: 3, LoginName: "login"}, "login"},
	} {
		if got, _ := actorLabel(tc.user); got != tc.want {
			t.Errorf("actorLabel(%+v) = %q want %q", tc.user, got, tc.want)
		}
	}

	statuses := []string{"backlog", "todo", "in_progress", "in_review", "done", "closed", "blocked", "cancelled", "", "custom"}
	for _, status := range statuses {
		if got := issueStatusName(status); got == "" {
			t.Errorf("empty status name for %q", status)
		}
	}
	before := domain.Issue{
		Status: "backlog", Title: "old", Description: "old", AssigneeUserID: 1,
		WorkflowID: "old-wf", TapdID: "1", BaseBranch: "old",
		RepositoryBranches: map[string]string{"r": "old"}, Priority: "low",
	}
	after := domain.Issue{
		Status: "todo", Title: "new", Description: "new", AssigneeUserID: 2,
		WorkflowID: "new-wf", TapdID: "2", BaseBranch: "new",
		RepositoryBranches: map[string]string{"r": "new"}, Priority: "high",
	}
	if detail := issueChangeDetail(before, after); !strings.Contains(detail, "状态") || !strings.Contains(detail, "优先级") {
		t.Fatalf("incomplete change detail: %q", detail)
	}
	after.TapdID, after.BaseBranch = "", ""
	if detail := issueChangeDetail(before, after); !strings.Contains(detail, "清除") || !strings.Contains(detail, "自动新建") {
		t.Fatalf("clear detail: %q", detail)
	}

	if got := appendClarificationDescription("description", " "); got != "description" {
		t.Fatalf("empty summary changed description: %q", got)
	}
	first := appendClarificationDescription("", "summary")
	replaced := appendClarificationDescription("prefix\n\n"+first, "replacement")
	if strings.Count(replaced, "## AgentFlow 澄清结论") != 1 || !strings.Contains(replaced, "replacement") {
		t.Fatalf("clarification replacement: %q", replaced)
	}
	for _, issue := range []domain.Issue{
		{ID: "local"},
		{ID: "gf", Source: "gongfeng"},
		{ID: "tapd", Source: "tapd"},
	} {
		if got := clarificationComment(issue, " result "); !strings.Contains(got, issue.ID) {
			t.Errorf("comment missing issue id: %q", got)
		}
	}

	now := time.Now().UTC()
	items := []domain.Issue{
		{ID: "b", ProjectID: "p", Number: 2, Title: "Alpha ticket", Status: "todo", Source: "local", AssigneeUserID: 7, UpdatedAt: now},
		{ID: "a", ProjectID: "p", Number: 1, Title: "Beta-ticket", Status: "backlog", Source: "tapd", AssigneeID: "bot", UpdatedAt: now, ExternalRef: &domain.ExternalRef{ExternalID: "EXT-9"}},
		{ID: "z", ProjectID: "other", Number: 3, Title: "Gamma", Status: "done", Source: "gongfeng", UpdatedAt: now.Add(time.Minute)},
		{ID: "c", ProjectID: "p", Number: 2, Title: "Unassigned", Status: "todo", Source: "local", UpdatedAt: now},
	}
	queries := []issueListQuery{
		{ProjectID: "p", Status: "todo,done", Source: "local"},
		{Assignee: "unassigned"},
		{Assignee: "7"},
		{Assignee: "BOT"},
		{Q: "ext-9"},
		{Q: "beta ticket"},
		{Q: "no-match"},
	}
	for _, query := range queries {
		_ = filterIssues(items, query)
	}
	_ = filterIssues([]domain.Issue{
		{ID: "low", Number: 1, UpdatedAt: now},
		{ID: "high", Number: 2, UpdatedAt: now},
	}, issueListQuery{})
	if fuzzyIssueTitleMatch("A-B C", "!!!") {
		t.Fatal("punctuation-only query matched")
	}
	if fuzzyIssueTitleMatch("Alpha Beta", "alpha missing") || !fuzzyIssueTitleMatch("Alpha-Beta", "alpha beta") {
		t.Fatal("fuzzy matching branches are wrong")
	}
	if normalizeIssueTitleSearch(" A-中_1 ") != "a中1" {
		t.Fatal("title normalization failed")
	}
	if !csvContainsFold("", "anything") || !csvContainsFold("TODO, done", "todo") || csvContainsFold("todo", "done") {
		t.Fatal("csv matching failed")
	}
	if got, err := parseIssueListInt(" ", 17); err != nil || got != 17 {
		t.Fatalf("default integer: %d %v", got, err)
	}
	if _, err := parseIssueListInt("nope", 0); err == nil {
		t.Fatal("invalid integer accepted")
	}
}

func TestIssuesMoreRepositoryWorkflowHelpers(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := issuesHandlers{deps: &deps{repo: f.s}}
	const (
		projectID = "cvb-more-project"
		otherID   = "cvb-more-other"
		workflow  = "cvb-more-workflow"
	)
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{ID: workflow, Name: "More workflow", ProjectID: projectID, Version: 1}); err != nil {
		t.Fatal(err)
	}
	project := domain.Project{
		ID: projectID, Key: "CVBM", Name: "More", DefaultRepositoryID: "cvb-more-repo",
		SOPPlaybooks: []domain.SOPPlaybook{{
			ID: "default-book", Name: "Default", Default: true,
			Bindings: []domain.IssueSOPBinding{{WorkflowID: workflow}},
		}},
	}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutProject(domain.Project{ID: otherID, Key: "CVBO", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRepository(domain.Repository{ID: "cvb-more-repo", ProjectID: projectID, Name: "Repo", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}

	issue := domain.Issue{ProjectID: projectID, Kind: "requirement", PlaybookID: "missing"}
	h.bindRequirementPlaybook(&issue)
	if issue.PlaybookID != "default-book" || issue.WorkflowID != workflow {
		t.Fatalf("default playbook was not bound: %+v", issue)
	}
	h.bindRequirementPlaybook(nil)
	nonRequirement := domain.Issue{ProjectID: projectID, Kind: "issue"}
	h.bindRequirementPlaybook(&nonRequirement)
	missingProject := domain.Issue{ProjectID: "missing", Kind: "requirement"}
	h.bindRequirementPlaybook(&missingProject)

	if err := h.bindIssueRepositories(&issue); err != nil || issue.RepositoryID != "cvb-more-repo" {
		t.Fatalf("default repository binding: %+v %v", issue, err)
	}
	wrong := domain.Issue{ProjectID: otherID, RepositoryID: "cvb-more-repo"}
	if err := h.bindIssueRepositories(&wrong); err == nil {
		t.Fatal("cross-project repository accepted")
	}
	if err := h.assignIssueWorkflow(nil, workflow); err != nil {
		t.Fatal(err)
	}
	if err := h.assignIssueWorkflow(&issue, " "); err != nil {
		t.Fatal(err)
	}
	if err := h.assignIssueWorkflow(&issue, "missing"); err == nil {
		t.Fatal("missing workflow accepted")
	}
	if err := h.assignIssueWorkflow(&issue, workflow); err != nil || issue.WorkflowID != workflow {
		t.Fatalf("workflow assignment: %+v %v", issue, err)
	}
	if h.knownWorkflow(" ") || !knownWorkflow(f.s, workflow) ||
		!knownWorkflow(f.s, repo.DefaultWorkflowID("tapd", "requirement")) {
		t.Fatal("known workflow checks failed")
	}

	if _, err := h.normalizeIssueSOPBindings(projectID, []domain.IssueSOPBinding{{WorkflowID: "missing"}}); err == nil {
		t.Fatal("missing SOP accepted")
	}
	if _, err := h.normalizeIssueSOPBindings(projectID, []domain.IssueSOPBinding{{WorkflowID: workflow, AssigneeUserID: 999999}}); err == nil {
		t.Fatal("missing assignee accepted")
	}
	if _, err := h.normalizeIssueSOPBindings(otherID, []domain.IssueSOPBinding{{WorkflowID: workflow}}); err == nil {
		t.Fatal("foreign SOP accepted")
	}
	if bindings, err := h.normalizeIssueSOPBindings(projectID, []domain.IssueSOPBinding{{WorkflowID: workflow}}); err != nil || len(bindings) != 1 {
		t.Fatalf("valid SOP binding: %+v %v", bindings, err)
	}
	if _, err := h.createIssueOnTracker(domain.Issue{}, "gongfeng"); err == nil {
		t.Fatal("gongfeng issue without repository accepted")
	}
	if _, err := h.createIssueOnTracker(domain.Issue{}, "unknown"); err == nil {
		t.Fatal("unknown tracker target accepted")
	}

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	h.recordIssueOperation(ctx, nil, "noop", "")
	if _, err := h.appendStoredOperation(ctx, "missing", "noop", ""); err == nil {
		t.Fatal("append operation accepted missing issue")
	}
	if err := f.s.PutIssue(domain.Issue{ID: "cvb-more-operation", ProjectID: projectID, Title: "operation"}); err != nil {
		t.Fatal(err)
	}
	if saved, err := h.appendStoredOperation(ctx, "cvb-more-operation", "update", "detail"); err != nil || len(saved.Operations) != 1 {
		t.Fatalf("append stored operation: %+v %v", saved, err)
	}
	missingRec := httptest.NewRecorder()
	missingCtx, _ := gin.CreateTestContext(missingRec)
	h.issueRequirement(missingCtx, domain.Issue{ID: "missing"})
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing requirement = %d", missingRec.Code)
	}
}

func TestIssuesMoreHTTPValidationAndPaging(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	future := time.Now().UTC().Add(10 * time.Minute)
	cases := []struct {
		method string
		path   string
		body   any
		want   int
	}{
		{http.MethodPost, "/api/issues", "not-an-object", http.StatusBadRequest},
		{http.MethodPost, "/api/issues", map[string]any{"projectId": "platform", "title": "future", "clarification": map[string]any{"startedAt": future}}, http.StatusBadRequest},
		{http.MethodPost, "/api/issues", map[string]any{"projectId": "missing", "title": "bad project"}, http.StatusBadRequest},
		{http.MethodPost, "/api/issues", map[string]any{"projectId": "platform", "title": "bad user", "assigneeUserId": 999999}, http.StatusBadRequest},
		{http.MethodPost, "/api/issues", map[string]any{"projectId": "platform", "title": "bad tapd", "tapdId": "12x"}, http.StatusBadRequest},
		{http.MethodPost, "/api/issues", map[string]any{"projectId": "platform", "title": "bad target", "syncTarget": "jira"}, http.StatusBadRequest},
		{http.MethodPost, "/api/issues", map[string]any{"id": "cvb-more-local", "projectId": "platform", "title": "local", "syncTarget": "local"}, http.StatusOK},
		{http.MethodPost, "/api/issues/import-tapd", "not-an-object", http.StatusBadRequest},
		{http.MethodPost, "/api/issues/import-tapd", map[string]any{
			"projectId": "missing",
			"url":       "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000118483431",
		}, http.StatusBadRequest},
		{http.MethodPost, "/api/issues/import-tapd", map[string]any{
			"projectId":     "platform",
			"url":           "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000118483431",
			"repositoryIds": []string{" ", "missing", "missing"},
		}, http.StatusBadRequest},
		{http.MethodGet, "/api/issues?limit=0&offset=99999", nil, http.StatusOK},
		{http.MethodGet, "/api/issues?limit=99999&offset=-1", nil, http.StatusBadRequest},
		{http.MethodGet, "/api/issues?limit=2&offset=bad", nil, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	if err := f.s.PutIssue(domain.Issue{
		ID: "cvb-more-edit", ProjectID: "platform", Title: "before", Status: "backlog",
		Kind: "requirement", Source: "local", TapdID: "123",
	}); err != nil {
		t.Fatal(err)
	}
	rec := cvBRequest(t, f, http.MethodPut, "/api/issues", map[string]any{
		"id": "cvb-more-edit", "title": "after", "status": "todo", "tapdId": "",
		"priority": "high", "baseBranch": "",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIssuesMoreRouteBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	valid := domain.Issue{
		ID: "cvb-more-route", ProjectID: "platform", Title: "route", Status: "backlog",
		Kind: "requirement", Source: "local",
		Clarification: domain.ClarificationAssessment{StartedAt: time.Now().UTC().Add(-time.Minute)},
	}
	orphan := domain.Issue{ID: "cvb-more-orphan", ProjectID: "missing", Title: "orphan", Kind: "requirement"}
	retry := domain.Issue{
		ID: "cvb-more-retry", ProjectID: "platform", Title: "retry", Kind: "requirement", Source: "local",
		Clarification: domain.ClarificationAssessment{Summary: "saved summary", WriteBackStatus: "failed"},
	}
	if err := f.s.PutIssue(valid); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutIssue(orphan); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutIssue(retry); err != nil {
		t.Fatal(err)
	}
	for _, run := range []domain.Run{
		{ID: "cvb-more-matching-run", Status: "failed", ExecutionContext: domain.TelemetryContext{IssueID: valid.ID}, Nodes: map[string]*domain.NodeRun{}},
		{ID: "cvb-more-other-run", Status: "failed", ExecutionContext: domain.TelemetryContext{IssueID: "other"}, Nodes: map[string]*domain.NodeRun{}},
	} {
		if err := f.s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		method string
		path   string
		body   any
		want   int
	}{
		{http.MethodPut, "/api/issues/cvb-more-route", nil, http.StatusNotFound},
		{http.MethodPost, "/api/issues/cvb-more-route/comments", "bad", http.StatusBadRequest},
		{http.MethodGet, "/api/issues/cvb-more-route/runs", nil, http.StatusOK},
		{http.MethodPost, "/api/issues/cvb-more-route/clarify", "bad", http.StatusBadRequest},
		{http.MethodPost, "/api/issues/cvb-more-orphan/clarify", map[string]any{"answers": map[string]string{}}, http.StatusBadRequest},
		{http.MethodPost, "/api/issues/cvb-more-route/clarify", map[string]any{"answers": map[string]string{"goal": "enough"}}, http.StatusOK},
		{http.MethodPost, "/api/issues/cvb-more-route/clarification", "bad", http.StatusBadRequest},
		{http.MethodPost, "/api/issues/cvb-more-retry/clarification", map[string]any{"retry": true}, http.StatusOK},
		{http.MethodPut, "/api/issues/cvb-more-route/sops", "bad", http.StatusBadRequest},
		{http.MethodPut, "/api/issues/cvb-more-route/sops", map[string]any{"bindings": []any{map[string]any{"workflowId": "missing"}}}, http.StatusBadRequest},
		{http.MethodPut, "/api/issues/cvb-more-route/sops", map[string]any{"bindings": []any{}}, http.StatusOK},
		{http.MethodPost, "/api/issues/cvb-more-route/reopen", nil, http.StatusConflict},
		{http.MethodPost, "/api/issues/cvb-more-route/dispatch", "bad", http.StatusBadRequest},
		{http.MethodPost, "/api/issues/cvb-more-route/chat-clarify", "bad", http.StatusBadRequest},
		{http.MethodGet, "/api/issues/cvb-more-route/chat-clarify", nil, http.StatusNotFound},
	}
	for _, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	busy := domain.Issue{
		ID: "cvb-more-busy-delete", ProjectID: "platform", Title: "busy",
		Clarification: domain.ClarificationAssessment{Status: domain.ClarificationAnalyzing},
	}
	if err := f.s.PutIssue(busy); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/issues/"+busy.ID, nil, true); rec.Code != http.StatusConflict {
		t.Fatalf("busy delete = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIssuesMoreTrackerCreateAndResync(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	f.h = newTAPDImportHandler(t, f.s, t.TempDir(), `{"id":"1010000000777777777","name":"coverage tracker issue","status":"open","data":[{"id":"1010000000777777777","name":"coverage tracker issue","status":"open"}]}`)
	rec := cvBRequest(t, f, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "platform", "title": "tracker create", "description": "description",
		"syncTarget": "tapd",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("tracker create = %d: %s", rec.Code, rec.Body.String())
	}
	var issue domain.Issue
	if err := json.Unmarshal(rec.Body.Bytes(), &issue); err != nil || issue.ID == "" {
		t.Fatalf("decode tracker issue: %+v %v", issue, err)
	}
	rec = cvBRequest(t, f, http.MethodPost, "/api/issues/"+issue.ID+"/clarification", map[string]any{
		"summary": "tracker clarification", "ready": true,
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("tracker clarification = %d: %s", rec.Code, rec.Body.String())
	}
	rec = cvBRequest(t, f, http.MethodPost, "/api/issues/"+issue.ID+"/clarification", map[string]any{"retry": true}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("tracker write-back retry = %d: %s", rec.Code, rec.Body.String())
	}
	rec = cvBRequest(t, f, http.MethodPost, "/api/issues/"+issue.ID+"/resync-tapd", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("tracker resync = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIssuesMoreClosedStoreErrors(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	h := issuesHandlers{deps: &deps{repo: store}}
	h.tasks = &tasksHandlers{deps: h.deps}
	issue := domain.Issue{ID: "closed-store", ProjectID: "platform"}
	for _, invoke := range []func(*gin.Context){
		func(c *gin.Context) { h.issueDetail(c, issue) },
		func(c *gin.Context) { h.issueRuns(c, issue) },
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		invoke(c)
		if rec.Code < 400 {
			t.Errorf("closed store response = %d: %s", rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	for _, invoke := range []func(*gin.Context){
		func(c *gin.Context) { h.submitIssueClarification(c, issue) },
		func(c *gin.Context) { h.issueSOPs(c, issue) },
	} {
		rec = httptest.NewRecorder()
		c, _ = gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(`{"summary":"summary","ready":true}`))
		c.Request.Header.Set("Content-Type", "application/json")
		invoke(c)
		if rec.Code < 400 {
			t.Errorf("closed store mutation = %d: %s", rec.Code, rec.Body.String())
		}
	}
}

func TestIssuesMoreForcedPersistenceError(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	issue := domain.Issue{ID: "cvb-more-write-error", ProjectID: "platform", Title: "write error", Kind: "requirement"}
	if err := f.s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DB.Exec(`CREATE TRIGGER cvb_more_reject_issue_update BEFORE UPDATE ON issues BEGIN SELECT RAISE(FAIL, 'forced issue update failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	rec := cvBRequest(t, f, http.MethodPost, "/api/issues/"+issue.ID+"/clarify", map[string]any{
		"answers": map[string]string{"goal": "answer"},
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("forced persistence error = %d: %s", rec.Code, rec.Body.String())
	}
}
