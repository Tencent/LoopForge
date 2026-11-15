package httpapi

import (
	"encoding/json"
	"testing"
)

// Whitespace-only fields must be treated as empty: a blank prompt is rejected,
// and a blank cron falls back to the interval rule instead of being parsed.
func TestValidateAssistantTaskDraftBlankFields(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "blank prompt", body: `{"name":"巡检","prompt":"  \n\t","intervalSec":60,"projectId":"p1"}`, wantErr: "invalid assistant task"},
		{name: "blank cron uses interval", body: `{"name":"巡检","prompt":"检查","cron":"   ","intervalSec":60,"projectId":"p1"}`},
		{name: "blank cron with short interval", body: `{"name":"巡检","prompt":"检查","cron":"   ","intervalSec":30,"projectId":"p1"}`, wantErr: "invalid assistant task"},
		{name: "valid cron ignores interval", body: `{"name":"日报","prompt":"汇总","cron":" 0 9 * * * ","intervalSec":0,"projectId":"p1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAssistantTaskDraft(json.RawMessage(tc.body), "p1")
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
