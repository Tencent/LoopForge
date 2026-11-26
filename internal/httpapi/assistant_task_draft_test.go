package httpapi

import (
	"encoding/json"
	"testing"
)

func TestValidateAssistantTaskDraft(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		projectID string
		wantErr   string
	}{
		{name: "valid cron", body: `{"name":"日报","prompt":"汇总","cron":"0 9 * * *","projectId":"p1"}`, projectID: "p1"},
		{name: "valid interval", body: `{"name":"巡检","prompt":"检查","intervalSec":60,"projectId":" p1 "}`, projectID: "p1"},
		{name: "empty scope accepts any project", body: `{"name":"巡检","prompt":"检查","intervalSec":120,"projectId":"p2"}`},
		{name: "invalid json", body: `{`, projectID: "p1", wantErr: "invalid assistant task"},
		{name: "missing name", body: `{"name":" ","prompt":"检查","intervalSec":60,"projectId":"p1"}`, projectID: "p1", wantErr: "invalid assistant task"},
		{name: "missing prompt", body: `{"name":"巡检","intervalSec":60,"projectId":"p1"}`, projectID: "p1", wantErr: "invalid assistant task"},
		{name: "invalid cron", body: `{"name":"巡检","prompt":"检查","cron":"not a cron","intervalSec":600,"projectId":"p1"}`, projectID: "p1", wantErr: "invalid assistant task"},
		{name: "interval too short", body: `{"name":"巡检","prompt":"检查","intervalSec":59,"projectId":"p1"}`, projectID: "p1", wantErr: "invalid assistant task"},
		{name: "missing project", body: `{"name":"巡检","prompt":"检查","intervalSec":60,"projectId":"  "}`, projectID: "p1", wantErr: "invalid assistant task"},
		{name: "other project", body: `{"name":"巡检","prompt":"检查","intervalSec":60,"projectId":"p2"}`, projectID: "p1", wantErr: "invalid draft scope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAssistantTaskDraft(json.RawMessage(tc.body), tc.projectID)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateAssistantTaskDraft() error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("validateAssistantTaskDraft() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateAssistantDraftDelegatesTaskDraft(t *testing.T) {
	ok := assistantDraft{Tool: " assistant_tasks_save ", Body: json.RawMessage(`{"name":"巡检","prompt":"检查","intervalSec":60,"projectId":"p1"}`)}
	if err := validateAssistantDraft(&ok, "p1"); err != nil {
		t.Fatalf("validateAssistantDraft(task) error = %v", err)
	}
	if ok.Tool != "assistant_tasks_save" {
		t.Fatalf("tool not trimmed: %q", ok.Tool)
	}
	bad := assistantDraft{Tool: "assistant_tasks_save", Body: json.RawMessage(`{"name":"巡检","prompt":"检查","intervalSec":60,"projectId":"p2"}`)}
	if err := validateAssistantDraft(&bad, "p1"); err == nil || err.Error() != "invalid draft scope" {
		t.Fatalf("validateAssistantDraft(other project) error = %v, want invalid draft scope", err)
	}
}
