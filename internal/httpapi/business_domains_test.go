package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBusinessDomainsPermissionsAndCurrentAttribution(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "business-domain-test-secret")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	var s *repo.Store
	var err error
	if dsn := os.Getenv("AGENTFLOW_BUSINESS_TEST_MYSQL_DSN"); dsn != "" {
		s, err = repo.Open(dsn)
	} else {
		s, err = repo.OpenTest()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	users := map[string]domain.User{}
	for _, login := range []string{"business-admin", "business-owner", "business-manager", "business-viewer", "business-outsider"} {
		u := domain.User{LoginName: login}
		if login == "business-admin" {
			u.PlatformRole = domain.PlatformRoleAdmin
		}
		if err := s.PutUser(u); err != nil {
			t.Fatal(err)
		}
		users[login], _ = s.UserByLoginName(login)
	}
	admin, owner, manager, viewer, outsider := users["business-admin"], users["business-owner"], users["business-manager"], users["business-viewer"], users["business-outsider"]
	for _, p := range []domain.Project{{ID: "business-p1", Name: "云解析", OwnerID: owner.ID}, {ID: "business-p2", Name: "VPC 解析", OwnerID: owner.ID}} {
		if err := s.PutProject(p); err != nil {
			t.Fatal(err)
		}
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	a := domain.BusinessDomain{ID: "business-a", Name: "测试 DNS 控制台", Active: true, ManagerIDs: []int64{manager.ID}, ViewerIDs: []int64{viewer.ID}}
	b := domain.BusinessDomain{ID: "business-b", Name: "测试其他业务", Active: true, ManagerIDs: []int64{manager.ID}, ViewerIDs: []int64{}}
	empty := domain.BusinessDomain{ID: "business-empty", Name: "测试空业务", Active: true, ManagerIDs: []int64{}, ViewerIDs: []int64{}}
	requestJSONAuthed[map[string]any](t, h, outsider, http.MethodPut, "/api/business-domains", a, 403)
	for _, d := range []domain.BusinessDomain{a, b, empty} {
		requestJSONAuthed[domain.BusinessDomain](t, h, admin, http.MethodPut, "/api/business-domains", d, 200)
	}
	requestJSONAuthed[map[string]any](t, h, admin, http.MethodPut, "/api/business-domains?createOnly=true", a, 409)
	requestJSONAuthed[map[string]any](t, h, viewer, http.MethodPut, "/api/business-domains", a, 403)
	invalid := a
	invalid.Active = false
	requestJSONAuthed[map[string]any](t, h, manager, http.MethodPut, "/api/business-domains", invalid, 403)
	invalid = a
	invalid.ManagerIDs = []int64{manager.ID, outsider.ID}
	requestJSONAuthed[map[string]any](t, h, manager, http.MethodPut, "/api/business-domains", invalid, 403)
	a.Description = "管理员维护说明"
	requestJSONAuthed[domain.BusinessDomain](t, h, manager, http.MethodPut, "/api/business-domains", a, 200)
	invalid = a
	invalid.ID = "business-duplicate"
	requestJSONAuthed[map[string]any](t, h, admin, http.MethodPut, "/api/business-domains", invalid, 409)
	invalid = a
	invalid.ViewerIDs = []int64{99999999}
	requestJSONAuthed[map[string]any](t, h, admin, http.MethodPut, "/api/business-domains", invalid, 400)
	assign := func(user domain.User, p, expected, target string, status int) {
		t.Helper()
		requestJSONAuthed[map[string]any](t, h, user, http.MethodPut, "/api/projects/"+p+"/business-domain", map[string]string{"expectedDomainId": expected, "businessDomainId": target, "reason": "测试归属调整"}, status)
	}
	assign(outsider, "business-p1", "", a.ID, 403)
	assign(owner, "business-p1", "", a.ID, 200)
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPut, "/api/projects/business-p2/business-domain", map[string]string{"expectedDomainId": "", "businessDomainId": a.ID}, 200)
	assign(owner, "business-p1", a.ID, b.ID, 403)
	requestJSONAuthed[map[string]any](t, h, outsider, http.MethodPut, "/api/projects", domain.Project{ID: "business-p1", Name: "冒充 Owner", OwnerID: outsider.ID}, 403)
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPut, "/api/projects", domain.Project{ID: "business-p1", Name: "绕过归属接口", BusinessDomainID: b.ID}, 400)
	// Stale ordinary project saves must not erase a concurrently changed assignment.
	stale, _ := s.Project("business-p1")
	for i, status := range []string{"succeeded", "succeeded", "failed"} {
		projectID := "business-p1"
		if i == 2 {
			projectID = "business-p2"
		}
		issueID := "business-issue-" + status + string(rune('a'+i))
		if err := s.PutIssue(domain.Issue{ID: issueID, ProjectID: projectID, Title: "历史需求", Status: "in_progress", Kind: "requirement", Source: "local"}); err != nil {
			t.Fatal(err)
		}
		context := domain.TelemetryContext{ProjectID: projectID, IssueID: issueID}
		if i == 1 {
			context.ProjectID = ""
		} // legacy fallback
		at := time.Date(2025, 3, 1+i, 0, 0, 0, 0, time.UTC)
		if err := s.PutRun(domain.Run{ID: "business-run-" + issueID, WorkflowID: "business-workflow", Status: status, ExecutionContext: context, CreatedAt: at, UpdatedAt: at, Usage: domain.TokenUsage{TotalTokens: int64(10 * (i + 1))}}); err != nil {
			t.Fatal(err)
		}
	}
	report := func(user domain.User, id string) domain.BusinessDomainReport {
		t.Helper()
		return requestJSONAuthed[domain.BusinessDomainReport](t, h, user, http.MethodGet, "/api/business-domain-report?businessDomainId="+id+"&from=2025-01-01T00:00:00Z&to=2025-12-31T23:59:59Z", nil, 200)
	}
	got := report(viewer, a.ID)
	if got.Summary.TotalRuns != 3 || got.Summary.TotalTokens != 60 || got.Summary.UniqueIssues != 3 || got.Summary.SuccessRate < 66 || got.Summary.SuccessRate > 67 {
		t.Fatalf("bad unified aggregates: %+v", got)
	}
	requestJSONAuthed[map[string]any](t, h, outsider, http.MethodGet, "/api/business-domain-report?businessDomainId="+a.ID, nil, 403)
	requestJSONAuthed[map[string]any](t, h, viewer, http.MethodGet, "/api/business-domain-report?businessDomainId="+b.ID, nil, 403)
	if got := report(admin, empty.ID); got.Summary.TotalRuns != 0 || len(got.Projects) != 0 {
		t.Fatalf("empty scope leaked all runs: %+v", got)
	}
	if got := report(outsider, "all"); got.Summary.TotalRuns != 0 || len(got.Projects) != 0 {
		t.Fatalf("unauthorized all leaked data: %+v", got)
	}
	assign(admin, "business-p1", "", b.ID, 409)
	assign(admin, "business-p1", "  "+a.ID+"  ", b.ID, 200)
	stale.Name = "已更新项目名"
	if err := s.PutProject(stale); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Project(stale.ID)
	if p.BusinessDomainID != b.ID {
		t.Fatalf("stale save reset domain: %+v", p)
	}
	if got := report(viewer, a.ID); got.Summary.TotalRuns != 1 || got.Summary.TotalTokens != 30 {
		t.Fatalf("old domain kept history: %+v", got)
	}
	if got := report(manager, b.ID); got.Summary.TotalRuns != 2 || got.Summary.TotalTokens != 30 {
		t.Fatalf("new domain missed history/legacy fallback: %+v", got)
	}
	if got := report(admin, "all"); got.Summary.TotalRuns != 3 || got.Summary.TotalTokens != 60 {
		t.Fatalf("global totals changed after transfer: %+v", got)
	}
	// A viewer receives aggregates, never run or individual detail fields.
	req := httptest.NewRequest("GET", "/api/business-domain-report", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("guest status: %d", rec.Code)
	}
	history := requestJSONAuthed[[]domain.BusinessDomainAssignment](t, h, owner, http.MethodGet, "/api/projects/business-p1/business-domain/history", nil, 200)
	if len(history) != 2 || history[0].ActorID != admin.ID || history[0].FromDomainID != a.ID || history[0].ToDomainID != b.ID {
		t.Fatalf("bad audit history: %+v", history)
	}
	b.Active = false
	requestJSONAuthed[domain.BusinessDomain](t, h, admin, http.MethodPut, "/api/business-domains", b, 200)
	assign(admin, "business-p2", a.ID, b.ID, 400)
	if got := report(manager, b.ID); got.Summary.TotalRuns != 2 {
		t.Fatal("disabled business lost its existing data")
	}
	requestJSONAuthed[map[string]any](t, h, admin, http.MethodGet, "/api/business-domain-report?from=2025-12-01T00:00:00Z&to=2025-01-01T00:00:00Z", nil, 400)
	requestJSONAuthed[map[string]any](t, h, admin, http.MethodGet, "/api/business-domain-report?grain=invalid", nil, 400)
	assign(admin, "business-p2", a.ID, "", 200)
	if got := report(owner, "unclassified"); got.Summary.TotalRuns != 1 {
		t.Fatal("unclassified owner scope missing")
	}
	// Ensure ordinary HTTP project payloads preserve assignment when omitted.
	saved := requestJSONAuthed[domain.Project](t, h, owner, http.MethodPut, "/api/projects", domain.Project{ID: p.ID, Name: "正常项目保存", OwnerID: owner.ID}, 200)
	if saved.BusinessDomainID != b.ID || strings.TrimSpace(saved.Name) == "" {
		t.Fatalf("ordinary save: %+v", saved)
	}
}
