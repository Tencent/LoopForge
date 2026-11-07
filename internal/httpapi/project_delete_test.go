package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// A user created okr-system by mistake and asked the assistant to delete
// it; there was no way to. Deleting archives: the project leaves the list,
// its data stays, the id stays taken and the owner or an admin can restore it.
func TestProjectDeleteArchivesAndRestores(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "project-delete-test-secret")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	users := map[string]domain.User{}
	for _, login := range []string{"pd-admin", "pd-owner", "pd-member"} {
		u := domain.User{LoginName: login}
		if login == "pd-admin" {
			u.PlatformRole = domain.PlatformRoleAdmin
		}
		if err := s.PutUser(u); err != nil {
			t.Fatal(err)
		}
		users[login], _ = s.UserByLoginName(login)
	}
	admin, owner, member := users["pd-admin"], users["pd-owner"], users["pd-member"]
	if err := s.PutProject(domain.Project{ID: "okr-system", Name: "OKR系统", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: member.ID}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(domain.Project{ID: "keep", Name: "保留", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "okr-1", ProjectID: "okr-system", Title: "初始化", Status: "open"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	listed := func() []string {
		var ids []string
		for _, p := range requestJSONAuthed[[]domain.Project](t, h, member, http.MethodGet, "/api/projects", nil, 200) {
			ids = append(ids, p.ID)
		}
		return ids
	}

	requestJSONAuthed[map[string]any](t, h, member, http.MethodDelete, "/api/projects/okr-system", nil, 403)
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodDelete, "/api/projects/missing", nil, 404)

	// Unfinished runs and enabled schedules block deletion.
	run := domain.Run{ID: "okr-run", Status: "running", ExecutionContext: domain.TelemetryContext{ProjectID: "okr-system"}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScheduleJob(domain.ScheduleJob{ID: "okr-job", Name: "每日同步", Enabled: true, ProjectID: "okr-system", Cron: "0 9 * * *"}); err != nil {
		t.Fatal(err)
	}
	blocked := requestJSONAuthed[map[string]string](t, h, owner, http.MethodDelete, "/api/projects/okr-system", nil, 409)
	if !strings.Contains(blocked["error"], "1 个未结束的运行") || !strings.Contains(blocked["error"], "「每日同步」") {
		t.Fatalf("blockers not explained: %v", blocked)
	}
	run.Status = "succeeded"
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScheduleJob(domain.ScheduleJob{ID: "okr-job", Name: "每日同步", Enabled: false, ProjectID: "okr-system", Cron: "0 9 * * *"}); err != nil {
		t.Fatal(err)
	}

	deleted := requestJSONAuthed[domain.Project](t, h, owner, http.MethodDelete, "/api/projects/okr-system", nil, 200)
	if deleted.ArchivedAt == nil || deleted.ArchivedBy != owner.ID {
		t.Fatalf("not archived: %+v", deleted)
	}
	if ids := listed(); strings.Contains(strings.Join(ids, ","), "okr-system") || !strings.Contains(strings.Join(ids, ","), "keep") {
		t.Fatalf("project list after delete = %v", ids)
	}
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodDelete, "/api/projects/okr-system", nil, 404)
	if _, ok := s.Issue("okr-1"); !ok {
		t.Fatal("deleting a project removed its requirements")
	}
	// The id stays taken: re-creating or editing it must not silently restore.
	conflict := requestJSONAuthed[map[string]string](t, h, owner, http.MethodPut, "/api/projects", domain.Project{ID: "okr-system", Name: "OKR系统2"}, 409)
	if !strings.Contains(conflict["error"], "已删除") {
		t.Fatalf("conflict = %v", conflict)
	}
	// A client cannot archive or unarchive through the save body either.
	saved := requestJSONAuthed[domain.Project](t, h, owner, http.MethodPut, "/api/projects", map[string]any{"id": "keep", "name": "保留", "archivedAt": "2026-01-01T00:00:00Z"}, 200)
	if saved.ArchivedAt != nil {
		t.Fatal("save body archived a project")
	}

	requestJSONAuthed[map[string]any](t, h, member, http.MethodPost, "/api/projects/okr-system/restore", nil, 403)
	restored := requestJSONAuthed[domain.Project](t, h, admin, http.MethodPost, "/api/projects/okr-system/restore", nil, 200)
	if restored.ArchivedAt != nil || restored.Name != "OKR系统" || len(restored.Members) != 1 {
		t.Fatalf("restore lost data: %+v", restored)
	}
	if ids := listed(); !strings.Contains(strings.Join(ids, ","), "okr-system") {
		t.Fatalf("restored project not listed: %v", ids)
	}
	// Admins can delete projects they do not own.
	requestJSONAuthed[domain.Project](t, h, admin, http.MethodDelete, "/api/projects/okr-system", nil, 200)
}

func TestAssistantTurnContextNamesTheChatProject(t *testing.T) {
	inProject := assistantTurnContext(assistantRequest{ProjectID: "skillhub"})
	for _, want := range []string{"本对话属于项目 skillhub", "不要说没有选中项目", "不归属当前项目", "projects_delete", "git.example.com/projects/new"} {
		if !strings.Contains(inProject, want) {
			t.Errorf("project chat context missing %q", want)
		}
	}
	if global := assistantTurnContext(assistantRequest{}); !strings.Contains(global, "没有绑定项目") || strings.Contains(global, "本对话属于项目") {
		t.Errorf("global chat context = %q", global)
	}
}
