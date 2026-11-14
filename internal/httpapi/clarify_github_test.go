package httpapi

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

func TestPersistClarifyReadyKeepsGitHubIssuePendingForWriteBack(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	for _, issue := range []domain.Issue{
		{
			ID: "iss-github-ready", ProjectID: "platform", Title: "github", Kind: "requirement", Source: "github",
			Description: "原 GitHub 描述",
			ExternalRef: &domain.ExternalRef{Source: "github", ExternalID: "9001", ExternalIID: 12},
		},
		{ID: "iss-github-unlinked", ProjectID: "platform", Title: "github without ref", Kind: "requirement", Source: "github"},
	} {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	h := &streamHandlers{deps: &deps{repo: s, sync: tracker.NewService()}}
	ready := map[string]any{"summary": "GitHub 需求结论", "questions": []any{}, "ready": true}

	stored, err := h.persistClarifyOutcome("iss-github-ready", ready, "")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Clarification.Status != domain.ClarificationReady || stored.Clarification.WriteBackStatus != "pending" {
		t.Fatalf("GitHub issue must wait for write-back confirmation, got %+v", stored.Clarification)
	}
	if !strings.Contains(stored.Description, "原 GitHub 描述") || !strings.Contains(stored.Description, "GitHub 需求结论") {
		t.Fatalf("platform description missing conclusion: %q", stored.Description)
	}

	unlinked, err := h.persistClarifyOutcome("iss-github-unlinked", ready, "")
	if err != nil {
		t.Fatal(err)
	}
	if unlinked.Clarification.WriteBackStatus != "skipped" {
		t.Fatalf("GitHub issue without an external ref has nothing to write back, got %q", unlinked.Clarification.WriteBackStatus)
	}

	noSync := &streamHandlers{deps: &deps{repo: s}}
	if err := s.PutIssue(domain.Issue{
		ID: "iss-github-nosync", ProjectID: "platform", Title: "github no sync", Kind: "requirement", Source: "github",
		ExternalRef: &domain.ExternalRef{Source: "github", ExternalID: "9002", ExternalIID: 13},
	}); err != nil {
		t.Fatal(err)
	}
	withoutSync, err := noSync.persistClarifyOutcome("iss-github-nosync", ready, "")
	if err != nil {
		t.Fatal(err)
	}
	if withoutSync.Clarification.WriteBackStatus != "skipped" {
		t.Fatalf("without a tracker service write-back must be skipped, got %q", withoutSync.Clarification.WriteBackStatus)
	}
}

func TestClarificationCommentNamesEachTrackerSource(t *testing.T) {
	for _, tc := range []struct {
		issue  domain.Issue
		source string
	}{
		{domain.Issue{ID: "local"}, "来源：工作项 `local`"},
		{domain.Issue{ID: "gf", Source: "gongfeng"}, "来源：工蜂 Issue `gf`"},
		{domain.Issue{ID: "tapd", Source: "tapd"}, "来源：TAPD 需求 `tapd`"},
		{domain.Issue{ID: "gh", Source: "github"}, "来源：GitHub Issue `gh`"},
	} {
		if got := clarificationComment(tc.issue, " result "); !strings.Contains(got, tc.source) || !strings.Contains(got, "\n\nresult\n\n") {
			t.Errorf("clarificationComment(source=%q) = %q, want footer %q", tc.issue.Source, got, tc.source)
		}
	}
}
