package httpapi

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// A project writer can run an AI job with their own key instead of the
// project owner's; the binding is server-managed and only its user keeps it.
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
	put := func(who domain.User, runAs string, extra map[string]any) domain.ScheduleJob {
		t.Helper()
		body := map[string]any{"runAs": runAs}
		for k, v := range job {
			body[k] = v
		}
		for k, v := range extra {
			body[k] = v
		}
		return requestJSONAuthed[domain.ScheduleJob](t, server, who, http.MethodPut, "/api/schedule-jobs", body, http.StatusOK)
	}

	// The owner has no key, so by default the job cannot run.
	saved := put(member, "", map[string]any{"runAsUserId": other.ID, "runAsName": "forged"})
	if saved.RunAsUserID != 0 || saved.RunAsName != "" {
		t.Fatalf("forged binding stored: %d %q", saved.RunAsUserID, saved.RunAsName)
	}
	a := &assistantHandlers{deps: &deps{repo: st}}
	if _, err := a.scheduleJobAssistantUser(saved, saved.AssistantModel); err == nil || !strings.Contains(err.Error(), "你自己的 Key") {
		t.Fatalf("owner without key = %v", err)
	}

	if code := scheduleAuthzStatus(t, server, nokey, http.MethodPut, "/api/schedule-jobs", map[string]any{"id": "runas-job", "projectId": "runas-proj", "name": "巡检", "skipDispatch": true, "assistantPrompt": "p", "intervalSec": 3600, "runAs": "self"}); code != http.StatusBadRequest {
		t.Fatalf("binding to a user without a key = %d", code)
	}

	saved = put(member, "self", nil)
	if saved.RunAsUserID != member.ID || saved.RunAsName != "runas-member" || saved.RunAs != "" {
		t.Fatalf("self binding = %d %q %q", saved.RunAsUserID, saved.RunAsName, saved.RunAs)
	}
	user, err := a.scheduleJobAssistantUser(saved, saved.AssistantModel)
	if err != nil || user.ID != member.ID {
		t.Fatalf("run as = %d %v", user.ID, err)
	}
	if saved = put(member, "", nil); saved.RunAsUserID != member.ID {
		t.Fatalf("bound user's own save dropped the binding: %d", saved.RunAsUserID)
	}
	// Another writer's save never keeps (or takes) someone else's key.
	if saved = put(other, "", nil); saved.RunAsUserID != 0 {
		t.Fatalf("another writer kept the member's binding: %d", saved.RunAsUserID)
	}
	saved = put(member, "self", nil)
	if saved = put(member, "owner", nil); saved.RunAsUserID != 0 {
		t.Fatalf("owner mode kept the binding: %d", saved.RunAsUserID)
	}
	if code := scheduleAuthzStatus(t, server, member, http.MethodPut, "/api/schedule-jobs", map[string]any{"id": "runas-job", "projectId": "runas-proj", "name": "巡检", "skipDispatch": true, "assistantPrompt": "p", "intervalSec": 3600, "runAs": "someone"}); code != http.StatusBadRequest {
		t.Fatalf("unknown run-as mode = %d", code)
	}

	// A bound member who can no longer write stops the job instead of
	// falling back to anyone else's key.
	saved = put(member, "self", nil)
	if err := st.PutUser(domain.User{LoginName: "runas-owner", CodeBuddyAPIKey: "owner-key"}); err != nil {
		t.Fatal(err)
	}
	project.Members[0].Role = "viewer"
	if err := st.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if _, err := a.scheduleJobAssistantUser(saved, saved.AssistantModel); err == nil || !strings.Contains(err.Error(), "runas-member 已不是该项目的可写成员") {
		t.Fatalf("demoted member = %v", err)
	}
}

// Starting or stopping a job from its row flips only enabled: another
// writer's toggle keeps the member's key binding and the pinned session.
func TestScheduleJobEnabledTogglesOnlyTheFlag(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "schedule-enabled-test-secret")
	h, _ := workbenchFixture(t)
	st := h.repo
	for _, login := range []string{"toggle-owner", "toggle-member", "toggle-viewer"} {
		if err := st.PutUser(domain.User{LoginName: login, CodeBuddyAPIKey: "k"}); err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := st.UserByLoginName("toggle-owner")
	member, _ := st.UserByLoginName("toggle-member")
	viewer, _ := st.UserByLoginName("toggle-viewer")
	if err := st.PutProject(domain.Project{ID: "toggle-proj", Name: "T", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: member.ID, Role: "member"}, {UserID: viewer.ID, Role: "viewer"}}}); err != nil {
		t.Fatal(err)
	}
	job := domain.ScheduleJob{ID: "toggle-job", ProjectID: "toggle-proj", Name: "巡检", SkipDispatch: true, AssistantPrompt: "p", IntervalSec: 3600, Enabled: true,
		RunAsUserID: member.ID, RunAsName: "toggle-member", SessionMode: domain.ScheduleSessionFixed, FixedSessionID: "chat-pinned"}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	server := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	path := "/api/schedule-jobs/toggle-job/enabled"
	if code := scheduleAuthzStatus(t, server, viewer, http.MethodPost, path, map[string]any{"enabled": false}); code != http.StatusForbidden {
		t.Fatalf("viewer toggled a job: %d", code)
	}
	if code := scheduleAuthzStatus(t, server, owner, http.MethodPost, path, map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("missing enabled = %d", code)
	}
	if code := scheduleAuthzStatus(t, server, owner, http.MethodPost, "/api/schedule-jobs/missing/enabled", map[string]any{"enabled": false}); code != http.StatusNotFound {
		t.Fatalf("missing job = %d", code)
	}
	saved := requestJSONAuthed[domain.ScheduleJob](t, server, owner, http.MethodPost, path, map[string]any{"enabled": false}, http.StatusOK)
	if saved.Enabled || saved.RunAsUserID != member.ID || saved.FixedSessionID != "chat-pinned" || saved.AssistantPrompt != "p" {
		t.Fatalf("stop = %+v", saved)
	}
	stored, _ := st.ScheduleJob("toggle-job")
	if stored.Enabled {
		t.Fatal("stop was not stored")
	}
	if saved = requestJSONAuthed[domain.ScheduleJob](t, server, owner, http.MethodPost, path, map[string]any{"enabled": true}, http.StatusOK); !saved.Enabled || saved.RunAsUserID != member.ID {
		t.Fatalf("start = %+v", saved)
	}
}
