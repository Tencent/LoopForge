package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// An explicit operation log on the update wins over the stored one; the stored
// log only fills in when the update carries none.
func TestInheritStoredIssueFieldsKeepsExplicitOperationsOverStored(t *testing.T) {
	old := domain.Issue{Operations: []domain.IssueOperation{{Action: "create"}}}
	v := domain.Issue{Operations: []domain.IssueOperation{{Action: "create"}, {Action: "edit"}}}
	inheritStoredIssueFields(&v, old, false)
	if len(v.Operations) != 2 || v.Operations[1].Action != "edit" {
		t.Fatalf("explicit operations replaced by stored ones: %+v", v.Operations)
	}
}

// A first clarification start must survive an update when the stored issue
// has never started clarifying.
func TestInheritStoredIssueFieldsKeepsNewClarificationStartWhenStoredIsZero(t *testing.T) {
	start := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	v := domain.Issue{}
	v.Clarification.StartedAt = start
	inheritStoredIssueFields(&v, domain.Issue{}, false)
	if !v.Clarification.StartedAt.Equal(start) {
		t.Fatalf("clarification start = %v, want %v", v.Clarification.StartedAt, start)
	}
}

func TestNormalizeIssueClosureEdgeCases(t *testing.T) {
	trackerClosed := &domain.ExternalRef{ExternalState: "closed"}
	cases := []struct {
		name            string
		v               domain.Issue
		old             domain.Issue
		hadOld          bool
		wantStatus      string
		wantCloseSource string
	}{
		{
			// A tracker close overrides any stale local close source.
			name:       "tracker close replaces stale close source",
			v:          domain.Issue{Status: "open", CloseSource: "manual", Source: "github", ExternalRef: trackerClosed},
			wantStatus: "closed", wantCloseSource: "external",
		},
		{
			// Already closed locally and closed in the tracker, no stored issue.
			name:       "closed with tracker closed and no stored issue is external",
			v:          domain.Issue{Status: "closed", Source: "tapd", ExternalRef: trackerClosed},
			wantStatus: "closed", wantCloseSource: "external",
		},
		{
			// Without a stored issue the zero-value old must not be consulted.
			name:       "stored close source ignored without stored issue",
			v:          domain.Issue{Status: "closed"},
			old:        domain.Issue{Status: "closed", CloseSource: "external"},
			hadOld:     false,
			wantStatus: "closed", wantCloseSource: "manual",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.v
			normalizeIssueClosure(&v, tc.old, tc.hadOld)
			if v.Status != tc.wantStatus || v.CloseSource != tc.wantCloseSource || v.AutomationState != "ignored" {
				t.Fatalf("got %q/%q/%q, want %q/%q/ignored", v.Status, v.CloseSource, v.AutomationState, tc.wantStatus, tc.wantCloseSource)
			}
		})
	}
}

// Re-saving an already closed issue without a close source (e.g. editing its
// title) must keep the stored close source instead of reclassifying it.
func TestPutIssueKeepsStoredCloseSourceOnResave(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	issue := domain.Issue{ID: "closed-resave", ProjectID: "platform", Title: "bug", Status: "closed", CloseSource: "external"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Issue(issue.ID)
	if stored.CloseSource != "external" {
		t.Fatalf("first save close source = %q, want external", stored.CloseSource)
	}
	edit := stored
	edit.Title = "bug (renamed)"
	edit.CloseSource = ""
	if err := s.PutIssue(edit); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if got.Title != "bug (renamed)" || got.Status != "closed" || got.CloseSource != "external" || got.AutomationState != "ignored" {
		t.Fatalf("resave = %q/%q/%q/%q, want renamed/closed/external/ignored", got.Title, got.Status, got.CloseSource, got.AutomationState)
	}
}
