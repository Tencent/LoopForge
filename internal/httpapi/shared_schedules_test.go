package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/schedule"
	"agentflow/internal/tracker"
)

func TestSharedScheduleSubscriptionUsesPinnedVersionAndTargetOwner(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "shared-schedules-test-secret")
	h, sourceOwner := workbenchFixture(t)
	st := h.repo
	for _, login := range []string{"subscriber", "outsider", "viewer"} {
		if err := st.PutUser(domain.User{LoginName: login, CodeBuddyAPIKey: "key-" + login}); err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := st.UserByLoginName("subscriber")
	outsider, _ := st.UserByLoginName("outsider")
	viewer, _ := st.UserByLoginName("viewer")
	if err := st.PutProject(domain.Project{ID: "subscriber", Name: "Target", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: viewer.ID, Role: "viewer"}}}); err != nil {
		t.Fatal(err)
	}
	source := domain.ScheduleJob{ID: "publish-source", ProjectID: "workbench", Name: "巡检", SkipDispatch: true, AssistantPrompt: "Source prompt", IntervalSec: 3600, AssistantModel: "glm-5.3-flash-ioa", OwnerUserID: sourceOwner.ID}
	if err := st.PutScheduleJob(source); err != nil {
		t.Fatal(err)
	}
	server := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	publish := map[string]any{"baseVersion": 0, "name": "共享巡检", "prompt": "old: {{params.days}} days", "parameters": []domain.ScheduleParameter{{Name: "days", Label: "天数", Type: "integer", Required: true, Default: "3"}}}
	requestJSONAuthed[map[string]any](t, server, owner, http.MethodPost, "/api/schedule-jobs/publish-source/publish", publish, 403)
	requestJSONAuthed[map[string]any](t, server, sourceOwner, http.MethodPost, "/api/schedule-jobs/publish-source/publish", map[string]any{"baseVersion": -1}, 400)
	requestJSONAuthed[map[string]any](t, server, sourceOwner, http.MethodPost, "/api/schedule-jobs/publish-source/publish", map[string]any{}, 400)
	v1 := requestJSONAuthed[domain.SharedScheduleVersion](t, server, sourceOwner, http.MethodPost, "/api/schedule-jobs/publish-source/publish", publish, 201)
	if v1.Version != 1 || v1.SourceProjectID != "workbench" {
		t.Fatalf("invalid first publication %+v", v1)
	}
	raw, _ := json.Marshal(v1)
	if strings.Contains(string(raw), "key-") || strings.Contains(string(raw), "test-only") || strings.Contains(string(raw), "ownerUserId") {
		t.Fatalf("publication leaked execution identity: %s", raw)
	}
	requestJSONAuthed[map[string]any](t, server, sourceOwner, http.MethodPost, "/api/schedule-jobs/publish-source/publish", publish, 409)
	binding := map[string]any{"projectId": "subscriber", "skipDispatch": true, "sharedTaskId": v1.ID, "sharedTaskVersion": 1, "parameterValues": map[string]string{"days": "5"}, "assistantPrompt": "untrusted copy", "enabled": true}
	requestJSONAuthed[map[string]any](t, server, sourceOwner, http.MethodPost, "/api/schedule-jobs", binding, 403)
	requestJSONAuthed[map[string]any](t, server, viewer, http.MethodPost, "/api/schedule-jobs", binding, 403)
	subscribed := requestJSONAuthed[domain.ScheduleJob](t, server, owner, http.MethodPost, "/api/schedule-jobs", binding, 200)
	if subscribed.AssistantPrompt != "" || subscribed.OwnerUserID != owner.ID || subscribed.IntervalSec != 3600 || subscribed.SharedTaskVersion != 1 || subscribed.ParameterValues["days"] != "5" {
		t.Fatalf("invalid binding %+v", subscribed)
	}
	requestJSONAuthed[map[string]any](t, server, owner, http.MethodPost, "/api/schedule-jobs/"+subscribed.ID+"/publish", publish, 400)
	for _, actor := range []domain.User{sourceOwner, outsider, viewer} {
		for _, route := range []struct{ method, path string }{{http.MethodDelete, "/api/schedule-jobs/" + subscribed.ID}, {http.MethodPost, "/api/schedule-jobs/" + subscribed.ID + "/run"}} {
			requestJSONAuthed[map[string]any](t, server, actor, route.method, route.path, nil, 403)
		}
	}
	for _, actor := range []domain.User{sourceOwner, outsider} {
		jobs := requestJSONAuthed[[]domain.ScheduleJob](t, server, actor, http.MethodGet, "/api/schedule-jobs", nil, 200)
		for _, job := range jobs {
			if job.ID == subscribed.ID {
				t.Fatal("private subscription leaked through list")
			}
		}
		bootstrap := requestJSONAuthed[map[string]json.RawMessage](t, server, actor, http.MethodGet, "/api/bootstrap", nil, 200)
		if strings.Contains(string(bootstrap["scheduleJobs"]), subscribed.ID) {
			t.Fatal("private subscription leaked through bootstrap")
		}
	}
	jobs := requestJSONAuthed[[]domain.ScheduleJob](t, server, viewer, http.MethodGet, "/api/schedule-jobs", nil, 200)
	visible := false
	for _, job := range jobs {
		visible = visible || job.ID == subscribed.ID
	}
	if !visible {
		t.Fatal("project viewer cannot read subscription")
	}
	// Publishing does not mutate any subscription. New required inputs are checked on explicit upgrade.
	publish["baseVersion"] = 1
	publish["prompt"] = "new: {{params.team}} / {{params.days}}"
	publish["parameters"] = []domain.ScheduleParameter{{Name: "days", Label: "天数", Type: "integer", Required: true, Default: "3"}, {Name: "team", Label: "业务", Type: "string", Required: true}}
	v2 := requestJSONAuthed[domain.SharedScheduleVersion](t, server, sourceOwner, http.MethodPost, "/api/schedule-jobs/publish-source/publish", publish, 201)
	versions := requestJSONAuthed[[]domain.SharedScheduleVersion](t, server, owner, http.MethodGet, "/api/shared-schedule-tasks/"+v1.ID+"/versions", nil, 200)
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].Prompt != v1.Prompt {
		t.Fatalf("invalid history %+v", versions)
	}
	catalog := requestJSONAuthed[[]domain.SharedScheduleVersion](t, server, outsider, http.MethodGet, "/api/shared-schedule-tasks", nil, 200)
	if len(catalog) != 1 || catalog[0].Version != v2.Version {
		t.Fatalf("invalid catalog %+v", catalog)
	}
	stored, _ := st.ScheduleJob(subscribed.ID)
	if stored.SharedTaskVersion != 1 {
		t.Fatal("publication silently upgraded subscription")
	}
	var generated string
	var actorID int64
	h.generate = func(_ context.Context, prompt string, actor domain.User) (string, error) {
		generated = prompt
		actorID = actor.ID
		return "OK", nil
	}
	svc := schedule.New(st, tracker.NewService(), func(domain.ScheduleJob, domain.Issue, string) error {
		t.Fatal("shared AI task unexpectedly dispatched SOP scan")
		return nil
	})
	svc.SetAssistantPrep(h.prepareScheduleJob)
	if result, err := svc.RunJob(stored.ID); err != nil || result.LastAssistantMessage != "OK" || result.LastStatus != "success" {
		t.Fatalf("subscription scheduler failed: %+v %v", result, err)
	}
	if actorID != owner.ID || !strings.Contains(generated, "old: 5 days") || strings.Contains(generated, "new:") {
		t.Fatalf("wrong version or identity: actor=%d prompt=%s", actorID, generated)
	}
	binding["id"] = subscribed.ID
	binding["sharedTaskVersion"] = 2
	requestJSONAuthed[map[string]any](t, server, owner, http.MethodPut, "/api/schedule-jobs", binding, 400)
	binding["parameterValues"] = map[string]string{"days": "8", "team": "pay"}
	binding["intervalSec"] = 900
	upgraded := requestJSONAuthed[domain.ScheduleJob](t, server, owner, http.MethodPut, "/api/schedule-jobs", binding, 200)
	if upgraded.IntervalSec != 900 || upgraded.SharedTaskVersion != 2 {
		t.Fatalf("upgrade didn't preserve local timing %+v", upgraded)
	}
	if _, err := svc.RunNow(upgraded.ID); err != nil {
		t.Fatal(err)
	}
	if actorID != owner.ID || !strings.Contains(generated, "new: pay / 8") {
		t.Fatalf("wrong upgraded execution %d %s", actorID, generated)
	}
	// A subscription must never fall back to a publisher's or a previous owner's key.
	owner.CodeBuddyAPIKey = ""
	if _, err := st.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{CodeBuddyAPIKeyClear: true}); err != nil {
		t.Fatal(err)
	}
	forged := upgraded
	forged.OwnerUserID = sourceOwner.ID
	if _, err := h.prepareScheduleJob(forged); err == nil {
		t.Fatal("subscription fell back to another execution identity")
	}
	// Clearing the binding or moving it cannot bypass target-project permissions.
	binding["sharedTaskId"] = ""
	binding["assistantPrompt"] = "standalone"
	requestJSONAuthed[map[string]any](t, server, outsider, http.MethodPut, "/api/schedule-jobs", binding, 403)
	requestJSONAuthed[map[string]any](t, server, owner, http.MethodPut, "/api/schedule-jobs", binding, 400)
	binding["sharedTaskId"] = v1.ID
	binding["projectId"] = "workbench"
	requestJSONAuthed[map[string]any](t, server, owner, http.MethodPut, "/api/schedule-jobs", binding, 403)
	requestJSONAuthed[map[string]bool](t, server, owner, http.MethodDelete, "/api/schedule-jobs/"+subscribed.ID, nil, 200)
	if _, ok := st.ScheduleJob(subscribed.ID); ok {
		t.Fatal("subscription wasn't deleted")
	}
	if _, err := st.SharedScheduleVersion(v1.ID, 1); err != nil {
		t.Fatal("deleting subscription deleted shared version")
	}
}
