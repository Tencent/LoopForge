package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func scheduleAuthzStatus(t *testing.T, h http.Handler, user domain.User, method, path string, body any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	token, err := auth.NewClientFromEnv().SignUser(&auth.User{ID: user.ID, LoginName: user.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// AI jobs run as the project Owner with full access, so a non-member or a
// viewer must not be able to plant, rewrite, trigger, read or delete one.
func TestScheduleJobsRequireProjectAccess(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "schedule-authz-test-secret")
	h, _ := workbenchFixture(t)
	st := h.repo
	for _, login := range []string{"sched-owner", "sched-member", "sched-viewer", "sched-outsider"} {
		if err := st.PutUser(domain.User{LoginName: login, CodeBuddyAPIKey: "key-" + login}); err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := st.UserByLoginName("sched-owner")
	member, _ := st.UserByLoginName("sched-member")
	viewer, _ := st.UserByLoginName("sched-viewer")
	outsider, _ := st.UserByLoginName("sched-outsider")
	if err := st.PutProject(domain.Project{ID: "sched-victim", Name: "Victim", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: member.ID, Role: "member"}, {UserID: viewer.ID, Role: "viewer"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(domain.Project{ID: "sched-own", Name: "Outsider own", OwnerID: outsider.ID}); err != nil {
		t.Fatal(err)
	}
	server := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	aiJob := func(id, project, prompt string) map[string]any {
		return map[string]any{"id": id, "projectId": project, "name": "巡检", "skipDispatch": true, "assistantPrompt": prompt, "intervalSec": 3600}
	}
	if err := st.PutScheduleJob(domain.ScheduleJob{ID: "victim-job", ProjectID: "sched-victim", Name: "巡检", SkipDispatch: true, AssistantPrompt: "owner prompt", IntervalSec: 3600, OwnerUserID: owner.ID}); err != nil {
		t.Fatal(err)
	}

	for _, who := range []domain.User{outsider, viewer} {
		if code := scheduleAuthzStatus(t, server, who, http.MethodPost, "/api/schedule-jobs", aiJob("", "sched-victim", "planted")); code != http.StatusForbidden {
			t.Fatalf("%s created an AI job in another project: %d", who.LoginName, code)
		}
		if code := scheduleAuthzStatus(t, server, who, http.MethodPut, "/api/schedule-jobs", aiJob("victim-job", "sched-victim", "rewritten")); code != http.StatusForbidden {
			t.Fatalf("%s rewrote the owner's job: %d", who.LoginName, code)
		}
		if code := scheduleAuthzStatus(t, server, who, http.MethodPost, "/api/schedule-jobs/victim-job/run", nil); code != http.StatusForbidden {
			t.Fatalf("%s triggered the owner's job: %d", who.LoginName, code)
		}
		if code := scheduleAuthzStatus(t, server, who, http.MethodDelete, "/api/schedule-jobs/victim-job", nil); code != http.StatusForbidden {
			t.Fatalf("%s deleted the owner's job: %d", who.LoginName, code)
		}
	}
	// Writing to an own project by the victim's id must not move the job over.
	if code := scheduleAuthzStatus(t, server, outsider, http.MethodPut, "/api/schedule-jobs", aiJob("victim-job", "sched-own", "moved")); code != http.StatusForbidden {
		t.Fatalf("outsider moved the owner's job into its project: %d", code)
	}
	if got, ok := st.ScheduleJob("victim-job"); !ok || got.AssistantPrompt != "owner prompt" || got.ProjectID != "sched-victim" {
		t.Fatalf("victim job changed: %+v %v", got, ok)
	}

	listed := requestJSONAuthed[[]domain.ScheduleJob](t, server, outsider, http.MethodGet, "/api/schedule-jobs", nil, 200)
	for _, job := range listed {
		if job.ProjectID == "sched-victim" {
			t.Fatalf("outsider can read another project's job: %+v", job)
		}
	}
	seen := false
	for _, job := range requestJSONAuthed[[]domain.ScheduleJob](t, server, viewer, http.MethodGet, "/api/schedule-jobs", nil, 200) {
		seen = seen || job.ID == "victim-job"
	}
	if !seen {
		t.Fatal("viewer should still read the project's jobs")
	}

	saved := requestJSONAuthed[domain.ScheduleJob](t, server, member, http.MethodPut, "/api/schedule-jobs", aiJob("victim-job", "sched-victim", "member edit"), 200)
	if saved.AssistantPrompt != "member edit" || saved.OwnerUserID != owner.ID {
		t.Fatalf("member edit should keep the project owner as executor: %+v", saved)
	}
	requestJSONAuthed[map[string]bool](t, server, member, http.MethodDelete, "/api/schedule-jobs/victim-job", nil, 200)
	created := requestJSONAuthed[domain.ScheduleJob](t, server, outsider, http.MethodPost, "/api/schedule-jobs", aiJob("", "sched-own", "own prompt"), 200)
	if created.ProjectID != "sched-own" || created.OwnerUserID != outsider.ID {
		t.Fatalf("owner should manage its own project's jobs: %+v", created)
	}

	// A job whose project record is gone stays with its stored owner, and the
	// request can never pick whose key a job runs with.
	if err := st.PutScheduleJob(domain.ScheduleJob{ID: "orphan-job", ProjectID: "sched-gone", Name: "孤儿", SkipDispatch: true, AssistantPrompt: "orphan prompt", IntervalSec: 3600, OwnerUserID: member.ID}); err != nil {
		t.Fatal(err)
	}
	orphan := aiJob("orphan-job", "sched-gone", "orphan edit")
	orphan["ownerUserId"] = owner.ID
	if code := scheduleAuthzStatus(t, server, outsider, http.MethodPut, "/api/schedule-jobs", orphan); code != http.StatusForbidden {
		t.Fatalf("outsider edited an orphan job: %d", code)
	}
	if code := scheduleAuthzStatus(t, server, outsider, http.MethodPost, "/api/schedule-jobs", aiJob("", "sched-gone", "planted")); code != http.StatusForbidden {
		t.Fatalf("outsider created a job in a missing project: %d", code)
	}
	edited := requestJSONAuthed[domain.ScheduleJob](t, server, member, http.MethodPut, "/api/schedule-jobs", orphan, 200)
	if edited.AssistantPrompt != "orphan edit" || edited.OwnerUserID != member.ID {
		t.Fatalf("orphan edit must keep the stored owner, not the requested one: %+v", edited)
	}
	if err := st.PutProject(domain.Project{ID: "sched-ownerless", Name: "No owner", Members: []domain.ProjectMember{{UserID: outsider.ID, Role: "member"}}}); err != nil {
		t.Fatal(err)
	}
	spoofed := aiJob("", "sched-ownerless", "spoof")
	spoofed["ownerUserId"] = owner.ID
	if got := requestJSONAuthed[domain.ScheduleJob](t, server, outsider, http.MethodPost, "/api/schedule-jobs", spoofed, 200); got.OwnerUserID != 0 {
		t.Fatalf("request chose the executing owner: %+v", got)
	}
}
