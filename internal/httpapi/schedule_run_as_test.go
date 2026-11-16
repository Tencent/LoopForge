package httpapi

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// An AI job runs with the keys of whoever saved it; the binding is
// server-managed and a writer who edits the job takes it over, so nobody runs
// a prompt with another member's keys.
func TestScheduleJobRunsWithTheSaversKey(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "schedule-run-as-test-secret")
	h, _ := workbenchFixture(t)
	st := h.repo
	for _, u := range []domain.User{
		{LoginName: "runas-owner"},
		{LoginName: "runas-member", CodeBuddyAPIKey: "member-key"},
		{LoginName: "runas-other", CodeBuddyAPIKey: "other-key"},
		{LoginName: "runas-nokey"},
	} {
		if err := st.PutUser(u); err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := st.UserByLoginName("runas-owner")
	member, _ := st.UserByLoginName("runas-member")
	other, _ := st.UserByLoginName("runas-other")
	nokey, _ := st.UserByLoginName("runas-nokey")
	project := domain.Project{ID: "runas-proj", Name: "RunAs", OwnerID: owner.ID, Members: []domain.ProjectMember{
		{UserID: member.ID, Role: "member"}, {UserID: other.ID, Role: "member"}, {UserID: nokey.ID, Role: "member"},
	}}
	if err := st.PutProject(project); err != nil {
		t.Fatal(err)
	}
	server := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	job := map[string]any{"id": "runas-job", "projectId": "runas-proj", "name": "巡检", "skipDispatch": true, "assistantPrompt": "p", "intervalSec": 3600}
	put := func(who domain.User, extra map[string]any) domain.ScheduleJob {
		t.Helper()
		body := map[string]any{}
		for k, v := range job {
			body[k] = v
		}
		for k, v := range extra {
			body[k] = v
		}
		return requestJSONAuthed[domain.ScheduleJob](t, server, who, http.MethodPut, "/api/schedule-jobs", body, http.StatusOK)
	}

	// With no key of their own and no shared key, the saver is told why the
	// job cannot be saved instead of creating one that only fails.
	if code := scheduleAuthzStatus(t, server, nokey, http.MethodPut, "/api/schedule-jobs", job); code != http.StatusBadRequest {
		t.Fatalf("saving without a key = %d", code)
	}
	if msg := scheduleMissingKeyError(assistantEngineCodeBuddy); !strings.Contains(msg, "共享 Key") {
		t.Fatalf("missing key reminder = %q", msg)
	}

	// The creator's keys run the job; a forged binding is ignored.
	saved := put(member, map[string]any{"runAsUserId": other.ID, "runAsName": "forged"})
	if saved.RunAsUserID != member.ID || saved.RunAsName != "runas-member" {
		t.Fatalf("creator binding = %d %q", saved.RunAsUserID, saved.RunAsName)
	}
	a := &assistantHandlers{deps: &deps{repo: st}}
	user, shared, err := a.scheduleJobAssistantUser(saved, saved.AssistantModel)
	if err != nil || user.ID != member.ID || user.CodeBuddyAPIKey != "member-key" || shared != nil {
		t.Fatalf("run as = %d %q %v %v", user.ID, user.CodeBuddyAPIKey, shared, err)
	}
	if saved = put(member, nil); saved.RunAsUserID != member.ID {
		t.Fatalf("creator's own save dropped the binding: %d", saved.RunAsUserID)
	}
	// Another writer's edit runs with their own key, never the creator's.
	if saved = put(other, nil); saved.RunAsUserID != other.ID {
		t.Fatalf("another writer's edit kept the creator's key: %d", saved.RunAsUserID)
	}

	// A job saved before the binding runs with the project owner's keys.
	legacy := saved
	legacy.RunAsUserID, legacy.RunAsName = 0, ""
	if _, _, err := a.scheduleJobAssistantUser(legacy, legacy.AssistantModel); err == nil || !strings.Contains(err.Error(), "编辑并保存一次任务") {
		t.Fatalf("owner without key = %v", err)
	}

	// Once the platform has a shared key, a saver without their own key falls
	// back to it, limited to the models it allows.
	if err := st.SetPlatformCodeBuddyKey(repo.PlatformCodeBuddyKeyUpdate{APIKey: "platform-secret", InternetEnvironment: "ioa", Models: []string{"glm-5.1", "kimi-k2.6"}}, 0); err != nil {
		t.Fatal(err)
	}
	if code := scheduleAuthzStatus(t, server, nokey, http.MethodPut, "/api/schedule-jobs", map[string]any{"id": "runas-job", "projectId": "runas-proj", "name": "巡检", "skipDispatch": true, "assistantPrompt": "p", "intervalSec": 3600, "assistantModel": "deepseek-v4.1-flash"}); code != http.StatusBadRequest {
		t.Fatalf("shared key with a model it does not allow = %d", code)
	}
	if code := scheduleAuthzStatus(t, server, nokey, http.MethodPut, "/api/schedule-jobs", map[string]any{"id": "runas-job", "projectId": "runas-proj", "name": "巡检", "skipDispatch": true, "assistantPrompt": "p", "intervalSec": 3600, "assistantModel": "codex:gpt-6-sol"}); code != http.StatusBadRequest {
		t.Fatalf("shared key for a Codex model = %d", code)
	}
	saved = put(nokey, nil)
	if saved.RunAsUserID != nokey.ID || saved.AssistantModel != "glm-5.1" {
		t.Fatalf("shared key binding = %d model %q", saved.RunAsUserID, saved.AssistantModel)
	}
	if saved = put(nokey, map[string]any{"assistantModel": "default"}); saved.AssistantModel != "glm-5.1" {
		t.Fatalf("CLI default on the shared key = %q", saved.AssistantModel)
	}
	if saved = put(nokey, map[string]any{"assistantModel": "kimi-k2.6"}); saved.AssistantModel != "kimi-k2.6" {
		t.Fatalf("allowed shared model = %q", saved.AssistantModel)
	}
	user, shared, err = a.scheduleJobAssistantUser(saved, saved.AssistantModel)
	if err != nil || user.ID != nokey.ID || user.CodeBuddyAPIKey != "platform-secret" || user.CodeBuddyInternetEnvironment != "ioa" || len(shared) != 2 {
		t.Fatalf("shared run = %d %q %q %v %v", user.ID, user.CodeBuddyAPIKey, user.CodeBuddyInternetEnvironment, shared, err)
	}
	// A run on the shared key never switches to a model it does not allow.
	stale := saved
	stale.AssistantModel = "deepseek-v4.1-flash"
	if _, err := a.prepareScheduleJob(stale); err == nil || !strings.Contains(err.Error(), "平台共享 Key") {
		t.Fatalf("shared run with a disallowed model = %v", err)
	}
	if _, _, err := a.scheduleJobAssistantUser(saved, "codex:gpt-6-sol"); err == nil || !strings.Contains(err.Error(), "Codex") {
		t.Fatalf("shared key is not lent to Codex: %v", err)
	}

	// A bound member who can no longer write stops the job instead of
	// falling back to anyone else's key.
	saved = put(member, nil)
	if err := st.PutUser(domain.User{LoginName: "runas-owner", CodeBuddyAPIKey: "owner-key"}); err != nil {
		t.Fatal(err)
	}
	project.Members[0].Role = "viewer"
	if err := st.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.scheduleJobAssistantUser(saved, saved.AssistantModel); err == nil || !strings.Contains(err.Error(), "runas-member 已不是该项目的可写成员") {
		t.Fatalf("demoted member = %v", err)
	}
}
