package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
)

func cvAStore(t *testing.T) *repo.Store {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func cvAContext(t *testing.T, method, target, body string, user *domain.User) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user != nil {
		req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(*user)))
	}
	c.Request = req
	return c, rec
}

func TestAssistantTaskValidationPersistenceAndDue(t *testing.T) {
	valid := assistantTask{Name: "巡检", Prompt: "检查", ProjectID: "platform", IntervalSec: 60, Enabled: true}
	got, err := normalizeAssistantTask(valid, assistantTask{})
	if err != nil || got.ID == "" || got.LastStatus != "idle" || got.CreatedAt.IsZero() {
		t.Fatalf("normalize new = %+v, %v", got, err)
	}
	for _, tc := range []assistantTask{
		{Prompt: "p", ProjectID: "p", IntervalSec: 60},
		{Name: strings.Repeat("名", 81), Prompt: "p", ProjectID: "p", IntervalSec: 60},
		{Name: "n", Prompt: strings.Repeat("p", 4001), ProjectID: "p", IntervalSec: 60},
		{Name: "n", Prompt: "p", ProjectID: "p", IntervalSec: 59},
		{Name: "n", Prompt: "p", ProjectID: "p", IntervalSec: assistantTaskMaxInterval + 1},
		{Name: "n", Prompt: "p", ProjectID: "p", Cron: "bad cron"},
	} {
		if _, err := normalizeAssistantTask(tc, assistantTask{}); err == nil {
			t.Fatalf("invalid task accepted: %+v", tc)
		}
	}
	old := got
	old.SessionID, old.LastError, old.LastMessage = "session", "old error", "old message"
	updated, err := normalizeAssistantTask(assistantTask{Name: "new", Prompt: "new", ProjectID: "platform", Cron: "0 9 * * 1-5"}, old)
	if err != nil || updated.CreatedAt != old.CreatedAt || updated.SessionID != "session" || updated.IntervalSec != 0 {
		t.Fatalf("normalize update = %+v, %v", updated, err)
	}

	st := cvAStore(t)
	if saveAssistantTask(nil, 1, got) == nil {
		t.Fatal("nil store save succeeded")
	}
	if err := saveAssistantTask(st, 7, got); err != nil {
		t.Fatal(err)
	}
	if item, err := loadAssistantTask(st, 7, got.ID); err != nil || item.Name != "巡检" {
		t.Fatalf("load = %+v, %v", item, err)
	}
	if items, err := listAssistantTasks(st, 7); err != nil || len(items) != 1 {
		t.Fatalf("list = %d, %v", len(items), err)
	}
	if err := deleteAssistantTask(st, 7, got.ID); err != nil {
		t.Fatal(err)
	}
	if err := deleteAssistantTask(st, 7, got.ID); err == nil {
		t.Fatal("missing delete succeeded")
	}

	now := time.Now().UTC()
	for name, tc := range map[string]struct {
		task assistantTask
		want bool
	}{
		"disabled":       {assistantTask{Enabled: false, IntervalSec: 60}, false},
		"short":          {assistantTask{Enabled: true, IntervalSec: 30}, false},
		"new":            {assistantTask{Enabled: true, IntervalSec: 60}, true},
		"recent running": {assistantTask{Enabled: true, IntervalSec: 60, LastStatus: "running", LastRunAt: now.Add(-time.Minute)}, false},
		"interval due":   {assistantTask{Enabled: true, IntervalSec: 60, LastRunAt: now.Add(-61 * time.Second)}, true},
		"interval early": {assistantTask{Enabled: true, IntervalSec: 60, LastRunAt: now.Add(-30 * time.Second)}, false},
		"cron due":       {assistantTask{Enabled: true, Cron: "* * * * *", LastRunAt: now.Add(-2 * time.Minute)}, true},
		"bad cron":       {assistantTask{Enabled: true, Cron: "bad", LastRunAt: now.Add(-2 * time.Minute)}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if actual := assistantTaskDue(tc.task, now); actual != tc.want {
				t.Fatalf("due=%v want=%v", actual, tc.want)
			}
		})
	}
	if firstNonEmpty("", " x ", "y") != " x " || truncateRunes(" abc ", 2) != "ab…" {
		t.Fatal("small task helpers failed")
	}
}

func TestAssistantTaskHandlersAndExecution(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutProject(domain.Project{ID: "task-project", Name: "Task Project"})
	_ = st.PutUser(domain.User{LoginName: "task-user", CodeBuddyAPIKey: "secret"})
	user, _ := st.UserByLoginName("task-user")
	h := &assistantHandlers{deps: &deps{repo: st}}

	c, rec := cvAContext(t, http.MethodGet, "/api/assistant/tasks", "", nil)
	h.listTasks(c)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodPut, "/api/assistant/tasks", `{"name":"","prompt":"","projectId":"task-project","intervalSec":60}`, &user)
	h.putTask(c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid put = %d %s", rec.Code, rec.Body.String())
	}
	c, rec = cvAContext(t, http.MethodPut, "/api/assistant/tasks", `{"name":"job","prompt":"do it","projectId":"missing","intervalSec":60}`, &user)
	h.putTask(c)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing project put = %d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodPut, "/api/assistant/tasks", `{"id":"job1","name":"job","prompt":"do it","projectId":"task-project","intervalSec":60,"enabled":true}`, &user)
	h.putTask(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d %s", rec.Code, rec.Body.String())
	}
	c, rec = cvAContext(t, http.MethodGet, "/api/assistant/tasks?projectId=other", "", &user)
	h.listTasks(c)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("filtered list = %d %s", rec.Code, rec.Body.String())
	}
	c, rec = cvAContext(t, http.MethodDelete, "/api/assistant/tasks/missing", "", &user)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	h.deleteTask(c)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing delete = %d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodPost, "/api/assistant/tasks/missing/run", "", &user)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	h.runTaskNow(c)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing run now = %d %s", rec.Code, rec.Body.String())
	}
	_ = st.PutUser(domain.User{LoginName: "task-no-key"})
	noKeyUser, _ := st.UserByLoginName("task-no-key")
	c, rec = cvAContext(t, http.MethodPost, "/api/assistant/tasks/job1/run", "", &noKeyUser)
	c.Params = gin.Params{{Key: "id", Value: "job1"}}
	h.runTaskNow(c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key run now = %d", rec.Code)
	}
	h.startTaskTicker()
	var nilHandler *assistantHandlers
	nilHandler.tickAssistantTasks()

	h.generate = func(context.Context, string, domain.User) (string, error) { return "完成 secret", nil }
	task, _ := loadAssistantTask(st, user.ID, "job1")
	if err := h.executeAssistantTask(context.Background(), user, &task); err != nil {
		t.Fatal(err)
	}
	if task.LastStatus != "ok" || strings.Contains(task.LastMessage, "secret") || task.SessionID == "" {
		t.Fatalf("executed task = %+v", task)
	}
	h.active.Store(user.ID, true)
	if err := h.executeAssistantTask(context.Background(), user, &task); !errors.Is(err, errAssistantBusy) {
		t.Fatalf("busy error = %v", err)
	}
	h.active.Delete(user.ID)
	h.generate = func(context.Context, string, domain.User) (string, error) { return "", errors.New("upstream") }
	if err := h.executeAssistantTask(context.Background(), user, &task); err == nil || task.LastStatus != "error" {
		t.Fatalf("generation failure = %v %+v", err, task)
	}
	noKey := user
	noKey.CodeBuddyAPIKey = ""
	if err := h.executeAssistantTask(context.Background(), noKey, &task); err == nil || !strings.Contains(task.LastError, "API Key") {
		t.Fatalf("missing key = %v %+v", err, task)
	}
}

func TestAssistantAcceptanceAndDraftWarnings(t *testing.T) {
	for input, want := range map[string]string{
		"u:p@tcp(host:3306)/db":                  "parseTime=true",
		"u:p@tcp(host:3306)/db?charset=utf8":     "parseTime=true",
		" u:p@tcp(host:3306)/db?parseTime=true ": "charset=utf8mb4",
	} {
		if got := mysqlReadDSN(input); !strings.Contains(got, want) {
			t.Fatalf("mysqlReadDSN(%q)=%q", input, got)
		}
	}
	if !loopbackMySQLDSN("u@tcp(localhost:3306)/x") || loopbackMySQLDSN("u@tcp(db:3306)/x") {
		t.Fatal("loopback classification")
	}
	if _, err := LookupCodeBuddyCredentialReadOnly(context.Background(), "", ""); err == nil {
		t.Fatal("empty production lookup accepted")
	}
	if _, err := LookupCodeBuddyCredentialReadOnly(context.Background(), ":", "x"); err == nil {
		t.Fatal("bad DSN accepted")
	}
	st := cvAStore(t)
	if _, err := SeedAssistantAcceptance(st, AssistantAcceptanceIdentity{}); err == nil {
		t.Fatal("incomplete identity accepted")
	}
	seeded, err := SeedAssistantAcceptance(st, AssistantAcceptanceIdentity{
		LoginName: "accept-user", CodeBuddyAPIKey: "cb", GitToken: "git",
	})
	if err != nil || seeded.ChineseName != "accept-user" || seeded.GitToken != "git" {
		t.Fatalf("seeded = %+v, %v", seeded, err)
	}
	if _, ok := st.Project(assistantAcceptanceProjectID); !ok {
		t.Fatal("acceptance project missing")
	}

	h := &assistantHandlers{deps: &deps{repo: st}}
	for tool, wantPart := range map[string]string{
		"agents_delete":       "不存在",
		"rules_delete":        "不存在",
		"skills_delete":       "不存在",
		"dockerfiles_delete":  "不存在",
		"environments_delete": "不存在",
		"sandboxes_delete":    "不存在",
		"workflows_delete":    "不存在",
	} {
		d := assistantDraft{Tool: tool, Body: json.RawMessage(`{"id":"missing"}`)}
		warnings := h.assistantDraftWarnings(d)
		if len(warnings) == 0 || !strings.Contains(warnings[0], wantPart) {
			t.Fatalf("%s warnings=%v", tool, warnings)
		}
	}
	if warnings := h.assistantDraftWarnings(assistantDraft{Tool: "agents_delete"}); len(warnings) != 1 {
		t.Fatalf("missing id warnings=%v", warnings)
	}
	wf := assistantDraft{Tool: "workflows_save", Body: json.RawMessage(`{"nodes":[{"id":"a","type":"agent"},{"id":"b","type":"agent","agentId":"missing"},{"id":"s","type":"script"},{"id":"sub","type":"sop"}]}`)}
	if warnings := h.assistantDraftWarnings(wf); len(warnings) != 4 {
		t.Fatalf("workflow warnings=%v", warnings)
	}
	if got := sharedAssetWarnings("规则", true, []string{"A"}, "角色"); len(got) != 2 {
		t.Fatalf("shared warnings=%v", got)
	}
	const projectID = assistantAcceptanceProjectID
	for _, err := range []error{
		st.PutRule(domain.RuleDefinition{ID: "rule-ref", Name: "Rule", ProjectID: projectID}),
		st.PutSkill(domain.SkillDefinition{ID: "skill-ref", Name: "Skill", ProjectID: projectID}),
		st.PutAgent(domain.AgentDefinition{ID: "agent-ref", Name: "Agent Ref", ProjectID: projectID, Rules: []string{"rule-ref"}, Skills: []string{"skill-ref"}}),
		st.PutDockerfile(domain.DockerfileDefinition{ID: "docker-ref", Name: "Docker", ProjectID: projectID, Content: "FROM scratch"}),
		st.PutEnvironment(domain.EnvironmentDefinition{ID: "env-ref", Name: "Env", ProjectID: projectID}),
		st.PutSandbox(domain.SandboxProfile{ID: "sandbox-ref", Name: "Sandbox Ref", ProjectID: projectID, Driver: "docker", DockerfileID: "docker-ref"}),
		st.PutSandbox(domain.SandboxProfile{ID: "sandbox-env-ref", Name: "Env Sandbox Ref", ProjectID: projectID, Driver: "bwrap", EnvironmentID: "env-ref"}),
		st.PutWorkflow(domain.WorkflowDefinition{ID: "workflow-ref", Name: "Workflow Ref", ProjectID: projectID, SandboxProfileID: "sandbox-ref"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for name, got := range map[string][]string{
		"rule":        agentNamesReferencing(h.repo, "rule", "rule-ref"),
		"skill":       agentNamesReferencing(h.repo, "skill", "skill-ref"),
		"dockerfile":  sandboxNamesReferencing(h.repo, "dockerfile", "docker-ref"),
		"environment": sandboxNamesReferencing(h.repo, "environment", "env-ref"),
		"sandbox":     workflowNamesReferencingSandbox(h.repo, "sandbox-ref"),
	} {
		want := map[string]string{"rule": "Agent Ref", "skill": "Agent Ref", "dockerfile": "Sandbox Ref", "environment": "Env Sandbox Ref", "sandbox": "Workflow Ref"}[name]
		if len(got) != 1 || got[0] != want {
			t.Fatalf("%s references = %#v, want [%q]", name, got, want)
		}
	}
	// 删除被引用的资产时，草稿告警要列出引用方（经共用的引用方查询 helper）。
	for tool, tc := range map[string]struct{ id, want string }{
		"rules_delete":        {"rule-ref", "规则仍被 1 个角色引用（Agent Ref）"},
		"skills_delete":       {"skill-ref", "技能仍被 1 个角色引用（Agent Ref）"},
		"dockerfiles_delete":  {"docker-ref", "Dockerfile仍被 1 个沙箱引用（Sandbox Ref）"},
		"environments_delete": {"env-ref", "环境安装脚本仍被 1 个沙箱引用（Env Sandbox Ref）"},
		"sandboxes_delete":    {"sandbox-ref", "沙箱仍被 1 个流程引用（Workflow Ref）"},
	} {
		d := assistantDraft{Tool: tool, Body: json.RawMessage(`{"id":"` + tc.id + `"}`)}
		if got := h.assistantDraftWarnings(d); len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Fatalf("%s warnings=%v, want %q", tool, got, tc.want)
		}
	}
}
