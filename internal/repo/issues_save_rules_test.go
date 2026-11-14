package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestInheritStoredIssueFieldsPreservesStatusOnlyForSync(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	old := domain.Issue{
		Status: "closed", CloseSource: "manual", AutomationState: "ignored",
		CreatedAt: created, Number: 7, ConversationSessionID: "sess-1",
		Operations: []domain.IssueOperation{{Action: "create"}},
	}

	synced := domain.Issue{Status: "open", CloseSource: "", AutomationState: "pending"}
	inheritStoredIssueFields(&synced, old, true)
	if synced.Status != "closed" || synced.CloseSource != "manual" || synced.AutomationState != "ignored" {
		t.Fatalf("sync must keep stored lifecycle, got %q/%q/%q", synced.Status, synced.CloseSource, synced.AutomationState)
	}

	edited := domain.Issue{Status: "open", AutomationState: "pending"}
	inheritStoredIssueFields(&edited, old, false)
	if edited.Status != "open" || edited.AutomationState != "pending" {
		t.Fatalf("edit must keep its own lifecycle, got %q/%q", edited.Status, edited.AutomationState)
	}
	if !edited.CreatedAt.Equal(created) || edited.Number != 7 || edited.ConversationSessionID != "sess-1" || len(edited.Operations) != 1 {
		t.Fatalf("stored identity fields not inherited: %+v", edited)
	}
}

func TestInheritStoredIssueFieldsKeepsExplicitValues(t *testing.T) {
	old := domain.Issue{Number: 7, AutomationState: "ignored", ConversationSessionID: "sess-old"}
	v := domain.Issue{Number: 9, ConversationSessionID: "sess-new", Operations: []domain.IssueOperation{{Action: "edit"}}}
	inheritStoredIssueFields(&v, old, false)
	if v.Number != 9 || v.ConversationSessionID != "sess-new" || len(v.Operations) != 1 || v.Operations[0].Action != "edit" {
		t.Fatalf("explicit values overwritten: %+v", v)
	}
	if v.AutomationState != "ignored" {
		t.Fatalf("blank automation state should fall back to stored, got %q", v.AutomationState)
	}
}

func TestInheritStoredIssueFieldsKeepsEarliestClarificationStart(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	old := domain.Issue{}
	old.Clarification.StartedAt = early

	for name, start := range map[string]time.Time{"zero": {}, "later": late} {
		v := domain.Issue{}
		v.Clarification.StartedAt = start
		inheritStoredIssueFields(&v, old, false)
		if !v.Clarification.StartedAt.Equal(early) {
			t.Fatalf("%s: clarification start = %v, want %v", name, v.Clarification.StartedAt, early)
		}
	}

	earlier := early.Add(-time.Hour)
	v := domain.Issue{}
	v.Clarification.StartedAt = earlier
	inheritStoredIssueFields(&v, old, false)
	if !v.Clarification.StartedAt.Equal(earlier) {
		t.Fatalf("earlier start must be kept, got %v", v.Clarification.StartedAt)
	}
}

func TestNormalizeIssueClosure(t *testing.T) {
	trackerClosed := &domain.ExternalRef{ExternalState: "closed"}
	cases := []struct {
		name            string
		v               domain.Issue
		old             domain.Issue
		hadOld          bool
		wantStatus      string
		wantCloseSource string
		wantAutomation  string
	}{
		{
			name:       "tracker closed forces external close",
			v:          domain.Issue{Status: "open", Source: "github", ExternalRef: trackerClosed, AutomationState: "pending"},
			wantStatus: "closed", wantCloseSource: "external", wantAutomation: "ignored",
		},
		{
			name:       "closed keeps stored close source",
			v:          domain.Issue{Status: "closed"},
			old:        domain.Issue{Status: "closed", CloseSource: "external"},
			hadOld:     true,
			wantStatus: "closed", wantCloseSource: "external", wantAutomation: "ignored",
		},
		{
			name:       "legacy closed without source falls back to manual",
			v:          domain.Issue{Status: "closed"},
			old:        domain.Issue{Status: "closed"},
			hadOld:     true,
			wantStatus: "closed", wantCloseSource: "manual", wantAutomation: "ignored",
		},
		{
			name:       "new close without tracker is manual",
			v:          domain.Issue{Status: "closed"},
			old:        domain.Issue{Status: "open"},
			hadOld:     true,
			wantStatus: "closed", wantCloseSource: "manual", wantAutomation: "ignored",
		},
		{
			name:       "explicit close source is kept",
			v:          domain.Issue{Status: "closed", CloseSource: "external"},
			wantStatus: "closed", wantCloseSource: "external", wantAutomation: "ignored",
		},
		{
			name:       "open issue clears close source",
			v:          domain.Issue{Status: "open", CloseSource: "manual", AutomationState: "pending"},
			wantStatus: "open", wantCloseSource: "", wantAutomation: "pending",
		},
		{
			name:       "done issue is ignored by automation",
			v:          domain.Issue{Status: "done", AutomationState: "pending"},
			wantStatus: "done", wantCloseSource: "", wantAutomation: "ignored",
		},
		{
			name:       "cancelled issue is ignored by automation",
			v:          domain.Issue{Status: "cancelled", AutomationState: "pending"},
			wantStatus: "cancelled", wantCloseSource: "", wantAutomation: "ignored",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.v
			normalizeIssueClosure(&v, tc.old, tc.hadOld)
			if v.Status != tc.wantStatus || v.CloseSource != tc.wantCloseSource || v.AutomationState != tc.wantAutomation {
				t.Fatalf("got %q/%q/%q, want %q/%q/%q", v.Status, v.CloseSource, v.AutomationState, tc.wantStatus, tc.wantCloseSource, tc.wantAutomation)
			}
		})
	}
}
