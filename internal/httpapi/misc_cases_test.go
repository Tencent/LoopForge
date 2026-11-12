package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"

	"github.com/gin-gonic/gin"
)

func cvBMiscRequestAs(t *testing.T, f cvBHTTPFixture, user domain.User, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = authedRequest(t, req, user)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func cvBMiscContext(method, path string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, path, nil)
	return c, rec
}

func TestMiscScheduleRemainingHelpersAndHTTP(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{
		ID: "cvb-misc-wf", Name: "misc workflow", ProjectID: "platform",
		Status: "published", Version: 1, SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "approve", Name: "approve", Type: "approval", MaxAttempts: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutProject(domain.Project{ID: "cvb-misc-project", Key: "CMP", Name: "misc", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRepository(domain.Repository{
		ID: "cvb-misc-repo", ProjectID: "cvb-misc-project", Name: "repo", Provider: "local",
		DefaultBranch: "main", IntegrateBranch: " feature ",
	}); err != nil {
		t.Fatal(err)
	}

	longPrompt := strings.Repeat("x", 8000)
	longRec := cvBRequest(t, f, http.MethodPost, "/api/schedule-jobs", map[string]any{
		"name": "long", "skipDispatch": true, "assistantPrompt": longPrompt,
	}, true)
	if longRec.Code != http.StatusOK {
		t.Fatalf("long prompt = %d: %s", longRec.Code, longRec.Body.String())
	}
	var longJob domain.ScheduleJob
	if err := json.Unmarshal(longRec.Body.Bytes(), &longJob); err != nil || longJob.AssistantPrompt != longPrompt {
		t.Fatalf("long prompt not stored: %v %s", err, longRec.Body.String())
	}

	for i, tc := range []struct {
		body any
		want int
	}{
		{map[string]any{"id": "cvb-misc-job", "name": "job", "projectId": "cvb-misc-project", "workflowId": "cvb-misc-wf", "intervalSec": 1}, http.StatusOK},
	} {
		rec := cvBRequest(t, f, http.MethodPost, "/api/schedule-jobs", tc.body, true)
		if rec.Code != tc.want {
			t.Fatalf("schedule case %d = %d want %d: %s", i, rec.Code, tc.want, rec.Body.String())
		}
	}
	job, ok := f.s.ScheduleJob("cvb-misc-job")
	if !ok || job.IntervalSec != 300 || job.OwnerUserID != f.user.ID {
		t.Fatalf("normalized job = %+v, %v", job, ok)
	}
	job.LastStatus = "success"
	job.LastError = "old"
	job.LastPulled = 7
	job.LastDispatched = 4
	job.LastAssistantMessage = "done"
	job.Blockers = []domain.ScheduleJobBlocker{{RepositoryID: "cvb-misc-repo"}}
	if err := f.s.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	rec := cvBRequest(t, f, http.MethodPut, "/api/schedule-jobs", map[string]any{
		"id": "cvb-misc-job", "name": "updated", "projectId": "cvb-misc-project",
		"workflowId": "cvb-misc-wf", "intervalSec": 60,
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("update job = %d: %s", rec.Code, rec.Body.String())
	}
	updated, _ := f.s.ScheduleJob("cvb-misc-job")
	if updated.LastStatus != "success" || updated.LastPulled != 7 || len(updated.Blockers) != 1 {
		t.Fatalf("runtime fields were not retained: %+v", updated)
	}

	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "true")
	if rec := cvBRequest(t, f, http.MethodPost, "/api/schedule-jobs/not-there/run", nil, true); rec.Code == http.StatusAccepted {
		t.Fatalf("missing schedule unexpectedly started: %s", rec.Body.String())
	}
	c, recRun := cvBMiscContext(http.MethodPost, "/api/schedule-jobs/missing/run")
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	(&scheduleHandlers{deps: &deps{repo: f.s}}).scheduleJobRun(c)
	_ = recRun

	h := &scheduleHandlers{deps: &deps{repo: f.s, engine: f.e}}
	if err := h.dispatchScheduled(domain.ScheduleJob{ID: "j", WorkflowID: "cvb-misc-wf"}, domain.Issue{ID: "missing"}, "schedule"); err == nil {
		t.Fatal("dispatchScheduled missing issue returned nil error")
	}
	if got, ok := h.issueRepository(domain.Issue{ProjectID: "missing"}); ok || got.ID != "" {
		t.Fatalf("unexpected repository: %+v", got)
	}
	if got, ok := h.issueRepository(domain.Issue{ProjectID: "cvb-misc-project"}); ok || got.ID != "" {
		t.Fatalf("project without default repository resolved: %+v", got)
	}
	project, _ := f.s.Project("cvb-misc-project")
	project.DefaultRepositoryID = "cvb-misc-repo"
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	gotRepo, ok := h.issueRepository(domain.Issue{ProjectID: project.ID})
	if !ok || gotRepo.ID != "cvb-misc-repo" {
		t.Fatalf("default repository = %+v, %v", gotRepo, ok)
	}
	if _, ok := h.issueRepository(domain.Issue{ProjectID: "platform", RepositoryID: gotRepo.ID}); ok {
		t.Fatal("cross-project repository accepted")
	}
	ctx := scheduledRepositoryContext(gotRepo)
	if ctx.RepositoryID != gotRepo.ID || ctx.BaseBranch != "feature" || ctx.IntegrateBranch != "feature" {
		t.Fatalf("repository context = %+v", ctx)
	}
	mainOnly := gotRepo
	mainOnly.IntegrateBranch = ""
	if got := scheduledRepositoryContext(mainOnly); got.BaseBranch != "main" {
		t.Fatalf("default base branch = %+v", got)
	}

	if alignWorkflowUsesPlatformAgent(domain.WorkflowDefinition{}) {
		t.Fatal("empty workflow uses align agent")
	}
	if !alignWorkflowUsesPlatformAgent(domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{
		{ID: "other", Type: "agent", AgentID: engine.BaselineAlignAgentID},
		{ID: "resolve", Type: "agent", AgentID: engine.BaselineAlignAgentID},
	}}) {
		t.Fatal("platform align agent was not found")
	}
	conflict := &engine.AlignConflictError{
		Integrate: "feature", Default: "main", Files: []string{"a.go"},
		Blocks: []domain.ConflictBlock{{File: "a.go"}},
	}
	noEngine := &scheduleHandlers{deps: &deps{repo: f.s}}
	blocker := noEngine.blockerFromConflict(domain.ScheduleJob{ID: "j"}, gotRepo, conflict, f.user.ID)
	if blocker.RepositoryID != gotRepo.ID || !strings.Contains(blocker.Reason, "engine is not configured") {
		t.Fatalf("blocker = %+v", blocker)
	}
	if _, err := noEngine.startBaselineAlignRun(domain.ScheduleJob{}, gotRepo, nil, f.user.ID); err == nil {
		t.Fatal("nil engine start unexpectedly succeeded")
	}

	if err := f.s.PutRun(domain.Run{
		ID: "cvb-align-running", WorkflowID: engine.BaselineAlignWorkflowID, Status: "running",
		Repository: domain.RepositoryContext{RepositoryID: gotRepo.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if id, active := h.activeAlignRunID(" " + gotRepo.ID + " "); !active || id != "cvb-align-running" {
		t.Fatalf("active run = %q, %v", id, active)
	}
	if _, active := h.activeAlignRunID(""); active || !h.alignRunActive(gotRepo.ID) {
		t.Fatal("active align predicates")
	}
	if id, err := h.startBaselineAlignRun(domain.ScheduleJob{}, gotRepo, conflict, f.user.ID); err != nil || id != "cvb-align-running" {
		t.Fatalf("existing align run = %q, %v", id, err)
	}
	h.cancelStaleAlignRuns("")
	noEngine.cancelStaleAlignRuns(gotRepo.ID)
	h.cancelStaleAlignRuns(gotRepo.ID)

	if !h.ensureBaselineAlignAgent() {
		t.Fatal("ensure align agent")
	}
	_ = f.s.DeleteWorkflow(engine.BaselineAlignWorkflowID)
	if !h.ensureBaselineAlignWorkflow(domain.ScheduleJob{}) {
		t.Fatal("ensure align workflow")
	}
	if wf, ok := f.s.Workflow(engine.BaselineAlignWorkflowID); !ok || wf.Version < baselineAlignWorkflowVersion {
		t.Fatalf("align workflow = %+v, %v", wf, ok)
	}
	noOwner := domain.Project{ID: "cvb-no-owner", Key: "CNO", Name: "no owner", DefaultRepositoryID: "cvb-no-owner-repo"}
	if err := f.s.PutProject(noOwner); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRepository(domain.Repository{
		ID: "cvb-no-owner-repo", ProjectID: noOwner.ID, Name: "no owner repo",
		DefaultBranch: "main", IntegrateBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	blocked, err := h.guardScheduledBaseline(domain.ScheduleJob{}, []domain.Issue{
		{ProjectID: "missing"}, {ProjectID: noOwner.ID}, {ProjectID: noOwner.ID},
	})
	if err != nil || blocked["cvb-no-owner-repo"].Reason == "" {
		t.Fatalf("baseline guard = %+v, %v", blocked, err)
	}
	gotRepo.IntegrateBranch = gotRepo.DefaultBranch
	if err := f.s.PutRepository(gotRepo); err != nil {
		t.Fatal(err)
	}
	blocked, err = h.guardScheduledBaseline(domain.ScheduleJob{}, []domain.Issue{{
		ProjectID: project.ID, RepositoryID: gotRepo.ID,
	}})
	if err != nil || len(blocked) != 0 {
		t.Fatalf("no-op baseline guard = %+v, %v", blocked, err)
	}

	preserved := domain.ScheduleJob{
		ID: "cvb-owner-preserve", Name: "preserve", ProjectID: "missing-project",
		WorkflowID: "cvb-misc-wf", IntervalSec: 60, OwnerUserID: f.user.ID,
	}
	if err := f.s.PutScheduleJob(preserved); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodPut, "/api/schedule-jobs", map[string]any{
		"id": preserved.ID, "name": preserved.Name, "projectId": preserved.ProjectID,
		"workflowId": preserved.WorkflowID, "intervalSec": 60,
	}, true); rec.Code != http.StatusOK {
		t.Fatalf("owner preservation = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMiscProjectPlaybooksPermissionsAndAssignments(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	f := cvBNewHTTPFixture(t)
	member := domain.User{ID: 7102, LoginName: "cvb-member", ChineseName: "成员"}
	outsider := domain.User{ID: 7103, LoginName: "cvb-outsider"}
	if err := f.s.PutUser(member); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutUser(outsider); err != nil {
		t.Fatal(err)
	}
	f.user.PlatformRole = domain.PlatformRoleAdmin
	if err := f.s.PutUser(f.user); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{
		ID: "cvb-playbook-wf", Name: "playbook", ProjectID: "platform",
		Status: "published", Version: 1, SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "approve", Name: "approve", Type: "approval", MaxAttempts: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	h := &projectsHandlers{deps: &deps{repo: f.s}}
	if err := h.applyProjectPlaybook(nil); err != nil {
		t.Fatal(err)
	}
	emptyBooks := domain.Project{ID: "platform", SOPPlaybooks: []domain.SOPPlaybook{}}
	if err := h.applyProjectPlaybook(&emptyBooks); err != nil {
		t.Fatal(err)
	}
	badBook := domain.Project{ID: "platform", SOPPlaybooks: []domain.SOPPlaybook{{
		ID: "bad", Bindings: []domain.IssueSOPBinding{{WorkflowID: "missing"}},
	}}}
	if err := h.applyProjectPlaybook(&badBook); err == nil {
		t.Fatal("missing playbook workflow accepted")
	}
	goodBook := domain.Project{ID: "platform", SOPPlaybooks: []domain.SOPPlaybook{{
		ID: "good", Name: "Good", Default: true,
		Bindings: []domain.IssueSOPBinding{{WorkflowID: "cvb-playbook-wf"}},
	}}}
	if err := h.applyProjectPlaybook(&goodBook); err != nil || goodBook.WorkflowID != "cvb-playbook-wf" {
		t.Fatalf("good playbook = %+v, %v", goodBook, err)
	}
	legacyEmpty := domain.Project{ID: "platform", SOPBindings: []domain.IssueSOPBinding{}}
	if err := h.applyProjectPlaybook(&legacyEmpty); err != nil || legacyEmpty.SOPBindings != nil {
		t.Fatalf("empty legacy bindings = %+v, %v", legacyEmpty, err)
	}
	legacyBad := domain.Project{ID: "platform", SOPBindings: []domain.IssueSOPBinding{{WorkflowID: "missing"}}}
	if err := h.applyProjectPlaybook(&legacyBad); err == nil {
		t.Fatal("missing legacy workflow accepted")
	}
	legacyGood := domain.Project{ID: "platform", SOPBindings: []domain.IssueSOPBinding{{WorkflowID: "cvb-playbook-wf"}}}
	if err := h.applyProjectPlaybook(&legacyGood); err != nil || legacyGood.WorkflowID != "cvb-playbook-wf" {
		t.Fatalf("legacy binding = %+v, %v", legacyGood, err)
	}

	if err := f.s.PutProject(domain.Project{ID: "cvb-owned", Key: "OWN", Name: "Owned", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		user domain.User
		body any
		want int
	}{
		{member, map[string]any{"id": "cvb-owned", "name": "changed"}, http.StatusForbidden},
		{member, map[string]any{"id": "cvb-new-wrong-owner", "name": "new", "ownerId": outsider.ID}, http.StatusForbidden},
		{member, map[string]any{"id": "cvb-new-member", "name": "new"}, http.StatusOK},
	} {
		rec := cvBMiscRequestAs(t, f, tc.user, http.MethodPost, "/api/projects", tc.body)
		if rec.Code != tc.want {
			t.Fatalf("project permission case %d = %d want %d: %s", i, rec.Code, tc.want, rec.Body.String())
		}
	}
	if rec := cvBMiscRequestAs(t, f, member, http.MethodPost, "/api/projects", "{"); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed project = %d: %s", rec.Code, rec.Body.String())
	}
	c, rec := cvBMiscContext(http.MethodPost, "/api/projects")
	c.Request.Body = io.NopCloser(strings.NewReader(`{"id":"cvb-direct","name":"direct"}`))
	c.Request.ContentLength = int64(len(`{"id":"cvb-direct","name":"direct"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	(&projectsHandlers{deps: &deps{repo: f.s}}).projects(c)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("direct unauthenticated project = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBMiscRequestAs(t, f, f.user, http.MethodPost, "/api/projects", map[string]any{
		"id": "cvb-playbook-bad", "name": "bad",
		"sopBindings": []any{map[string]any{"workflowId": "missing"}},
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad project playbook = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBMiscRequestAs(t, f, f.user, http.MethodPost, "/api/projects", map[string]any{
		"id": "cvb-domain-bypass", "name": "bad", "businessDomainId": "missing",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("business domain bypass = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBMiscRequestAs(t, f, f.user, http.MethodPost, "/api/projects", map[string]any{
		"id": "cvb-tapd-clear-new", "name": "clear", "tapdClear": true,
	}); rec.Code != http.StatusOK {
		t.Fatalf("new TAPD clear project = %d: %s", rec.Code, rec.Body.String())
	}

	if rec := cvBMiscRequestAs(t, f, outsider, http.MethodGet, "/api/projects/cvb-owned/business-domain/history", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden history = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBMiscRequestAs(t, f, f.user, http.MethodGet, "/api/projects/missing/business-domain/history", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing assignment project = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBMiscRequestAs(t, f, f.user, http.MethodPut, "/api/projects/cvb-owned/business-domain", map[string]any{
		"reason": strings.Repeat("界", 501),
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("long assignment reason = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBMiscRequestAs(t, f, f.user, http.MethodPut, "/api/projects/cvb-owned/business-domain", map[string]any{
		"businessDomainId": "missing-domain",
	}); rec.Code != http.StatusNotFound {
		t.Fatalf("missing assignment domain = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBMiscRequestAs(t, f, f.user, http.MethodPut, "/api/business-domains", map[string]any{
		"id": "cvb-misc-domain", "name": "misc domain", "active": true,
	}); rec.Code != http.StatusOK {
		t.Fatalf("create domain = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBMiscRequestAs(t, f, f.user, http.MethodPut, "/api/projects/cvb-owned/business-domain", map[string]any{
		"businessDomainId": "cvb-misc-domain", "reason": "assign",
	}); rec.Code != http.StatusOK {
		t.Fatalf("assign domain = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBMiscRequestAs(t, f, f.user, http.MethodGet, "/api/projects/cvb-owned/business-domain/history", nil); rec.Code != http.StatusOK {
		t.Fatalf("assignment history = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBMiscRequestAs(t, f, f.user, http.MethodPut, "/api/projects/cvb-owned/business-domain", map[string]any{
		"expectedDomainId": "wrong",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("assignment conflict = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMiscRoomAuthorizationRosterAndSuccessHTTP(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	f := cvBNewHTTPFixture(t)
	owner := domain.User{ID: 7201, LoginName: "owner", ChineseName: "所有者"}
	viewer := domain.User{ID: 7202, LoginName: "viewer", ChineseName: "只读"}
	member := domain.User{ID: 7203, LoginName: "member"}
	outsider := domain.User{ID: 7204, LoginName: "outsider"}
	chineseOnly := domain.User{ID: 7205, ChineseName: "中文名"}
	for _, user := range []domain.User{owner, viewer, member, outsider, chineseOnly} {
		if err := f.s.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	project := domain.Project{
		ID: "cvb-room-project", Key: "CRP", Name: "rooms", OwnerID: owner.ID,
		Members: []domain.ProjectMember{
			{UserID: owner.ID, Role: "member"},
			{UserID: viewer.ID, Role: "viewer"},
			{UserID: member.ID, Role: "member"},
			{UserID: chineseOnly.ID, Role: "member"},
			{UserID: 999999, Role: "member"},
		},
	}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{
		ID: "cvb-room-wf", Name: "room workflow", ProjectID: project.ID,
		Status: "published", Version: 1, SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "approve", Name: "approve", Type: "approval", MaxAttempts: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	created := cvBMiscRequestAs(t, f, owner, http.MethodPost, "/api/rooms", map[string]any{
		"projectId": project.ID, "name": "Room",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create room = %d: %s", created.Code, created.Body.String())
	}
	var room domain.ChatRoomView
	if err := json.Unmarshal(created.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	base := "/api/rooms/" + room.ID
	for i, tc := range []struct {
		user   domain.User
		method string
		path   string
		body   any
		want   int
	}{
		{viewer, http.MethodGet, base, nil, http.StatusOK},
		{viewer, http.MethodPost, base + "/messages", map[string]any{"body": "no"}, http.StatusForbidden},
		{member, http.MethodPost, base + "/messages", map[string]any{"body": "yes"}, http.StatusCreated},
		{outsider, http.MethodGet, base, nil, http.StatusForbidden},
		{owner, http.MethodPost, base + "/issues", map[string]any{"title": "new issue", "description": "work", "workflowId": "cvb-room-wf"}, http.StatusCreated},
	} {
		rec := cvBMiscRequestAs(t, f, tc.user, tc.method, tc.path, tc.body)
		if rec.Code != tc.want {
			t.Fatalf("room case %d = %d want %d: %s", i, rec.Code, tc.want, rec.Body.String())
		}
	}

	h := &roomsHandlers{deps: &deps{repo: f.s, engine: f.e}}
	if roster := h.projectRoster("missing"); len(roster) != 0 {
		t.Fatalf("missing roster = %+v", roster)
	}
	roster := h.projectRoster(project.ID)
	if len(roster) != 4 || roster[0].Name != "owner(所有者)" || roster[3].Name != "中文名" {
		t.Fatalf("roster = %+v", roster)
	}
	views := h.roomViews([]domain.ChatRoom{
		{ID: "one", ProjectID: project.ID}, {ID: "two", ProjectID: project.ID},
	})
	if len(views) != 2 || len(views[0].Members) != 4 || len(h.roomView(domain.ChatRoom{ProjectID: project.ID}).Members) != 4 {
		t.Fatalf("room views = %+v", views)
	}

	c, rec := cvBMiscContext(http.MethodGet, "/")
	if h.roomAccess(c, project.ID, false) || rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous access = %v, %d", h.roomAccess(c, project.ID, false), rec.Code)
	}
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "false")
	c, _ = cvBMiscContext(http.MethodGet, "/")
	if !h.roomAccess(c, project.ID, true) {
		t.Fatal("local anonymous access denied")
	}
	if id, name := h.roomActor(c); id != 0 || name != "群成员" {
		t.Fatalf("anonymous actor = %d, %q", id, name)
	}
	if got := newChatID("cvb"); !strings.HasPrefix(got, "cvb-") {
		t.Fatalf("chat id = %q", got)
	}

	c, rec = cvBMiscContext(http.MethodGet, "/")
	h.createIssue(c)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("create issue method = %d", rec.Code)
	}
	c, rec = cvBMiscContext(http.MethodPost, "/")
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	h.createIssue(c)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("create issue missing room = %d", rec.Code)
	}

	c, rec = cvBMiscContext(http.MethodDelete, "/api/rooms")
	h.rooms(c)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("rooms method = %d", rec.Code)
	}
	c, rec = cvBMiscContext(http.MethodDelete, "/api/rooms/"+room.ID)
	c.Params = gin.Params{{Key: "id", Value: room.ID}}
	h.room(c)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("room method = %d", rec.Code)
	}
	c, rec = cvBMiscContext(http.MethodDelete, "/api/rooms/"+room.ID+"/messages")
	c.Params = gin.Params{{Key: "id", Value: room.ID}}
	h.messages(c)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("messages method = %d", rec.Code)
	}

	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	for _, tc := range []struct {
		method string
		invoke func(*gin.Context)
	}{
		{http.MethodPost, h.rooms},
		{http.MethodPatch, h.room},
		{http.MethodPost, h.messages},
		{http.MethodPost, h.createIssue},
	} {
		c, rec = cvBMiscContext(tc.method, "/")
		c.Params = gin.Params{{Key: "id", Value: room.ID}}
		c.Request.Body = http.NoBody
		c.Request = c.Request.WithContext(
			context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(owner)),
		)
		tc.invoke(c)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("malformed room handler = %d: %s", rec.Code, rec.Body.String())
		}
	}
	c, rec = cvBMiscContext(http.MethodGet, "/")
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	c.Request = c.Request.WithContext(
		context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(owner)),
	)
	h.messages(c)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing room messages = %d", rec.Code)
	}
	c, rec = cvBMiscContext(http.MethodGet, "/")
	c.Request = c.Request.WithContext(
		context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(member)),
	)
	if h.roomAccess(c, "missing", false) || rec.Code != http.StatusNotFound {
		t.Fatalf("missing project access = %v, %d", h.roomAccess(c, "missing", false), rec.Code)
	}
	ownerless := domain.Project{ID: "cvb-open-room-project", Key: "COR", Name: "open"}
	if err := f.s.PutProject(ownerless); err != nil {
		t.Fatal(err)
	}
	c, _ = cvBMiscContext(http.MethodGet, "/")
	c.Request = c.Request.WithContext(
		context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(member)),
	)
	if !h.roomAccess(c, ownerless.ID, true) {
		t.Fatal("ownerless project denied")
	}
	if id, name := h.roomActor(c); id != member.ID || name != member.LoginName {
		t.Fatalf("member actor = %d, %q", id, name)
	}
}
