package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestScheduleCRUDValidationBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{ID: "cvb-schedule-wf", Name: "Schedule", ProjectID: "platform", SandboxProfileID: "local", Version: 1}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		body any
		want int
	}{
		{map[string]any{"name": "x", "source": "invalid", "workflowId": "cvb-schedule-wf"}, 400},
		{map[string]any{"name": "x", "skipDispatch": true}, 400},
		{map[string]any{"name": "x", "workflowId": "cvb-schedule-wf", "maxDispatch": 99}, 400},
		{map[string]any{"name": "x"}, 400},
		{map[string]any{"name": "x", "workflowId": "missing"}, 400},
		{map[string]any{"name": "x", "workflowId": "cvb-schedule-wf", "cron": "bad"}, 400},
		{map[string]any{"id": "cvb-job", "name": "Dispatch", "projectId": "platform", "workflowId": "cvb-schedule-wf", "intervalSec": 60}, 200},
		{map[string]any{"id": "cvb-ai-job", "name": "AI", "projectId": "platform", "skipDispatch": true, "assistantPrompt": "summarize", "cron": "0 * * * *"}, 200},
		{map[string]any{"id": "cvb-job", "name": "Updated", "projectId": "platform", "workflowId": "cvb-schedule-wf", "intervalSec": 60}, 200},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, http.MethodPost, "/api/schedule-jobs", tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%d schedule = %d want %d: %s", i, rec.Code, tc.want, rec.Body.String())
		}
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/schedule-jobs/cvb-job", nil, true); rec.Code != 404 {
		t.Errorf("individual GET = %d", rec.Code)
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/schedule-jobs/cvb-job", nil, true); rec.Code != 200 {
		t.Errorf("delete = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestProjectUpdateAndStreamCancelBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "cvb-proj-update", Key: "CPU", Name: "Project", OwnerID: f.user.ID, TAPDWorkspaceID: "old", TAPDIterationID: "iter"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		body any
		want int
	}{
		{map[string]any{"id": "cvb-proj-update", "name": "Project", "ownerId": f.user.ID, "members": []any{map[string]any{"userId": f.user.ID}, map[string]any{"userId": f.user.ID}}}, 400},
		{map[string]any{"id": "cvb-proj-update", "name": "Project", "ownerId": f.user.ID, "defaultRepositoryId": "missing"}, 400},
		{map[string]any{"id": "cvb-proj-update", "name": "Project", "ownerId": f.user.ID, "tapdClear": true, "runEval": map[string]any{"enabled": true}}, 200},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, http.MethodPut, "/api/projects", tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%d project = %d want %d: %s", i, rec.Code, tc.want, rec.Body.String())
		}
	}
	for _, path := range []string{"/api/chat-clarify/cancel?jobId=missing", "/api/chat-clarify/cancel"} {
		rec := cvBRequest(t, f, http.MethodPost, path, map[string]any{}, true)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "cancelled") {
			t.Errorf("cancel %s = %d: %s", path, rec.Code, rec.Body.String())
		}
	}
}
