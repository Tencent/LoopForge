package tracker

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestGongfengSyncedIssueNewIssue(t *testing.T) {
	repo := domain.Repository{ID: "repo-1", ProjectID: "proj-1", WebURL: "https://git.example.com/group/app/"}
	priority := 3
	gi := gongfengIssue{ID: 9001, IID: 42, Title: "title", Description: "desc", State: "closed", Priority: &priority, CreatedAt: "2026-01-02T03:04:05Z"}

	before := time.Now().UTC()
	issue := gongfengSyncedIssue(repo, gi, domain.Issue{}, false)

	if issue.ID != "gf-repo-1-42" || issue.Number != 42 {
		t.Fatalf("id/number = %q/%d, want gf-repo-1-42/42", issue.ID, issue.Number)
	}
	if issue.ProjectID != "proj-1" || issue.RepositoryID != "repo-1" || issue.Kind != "issue" || issue.Source != "gongfeng" {
		t.Fatalf("unexpected identity fields: %+v", issue)
	}
	if issue.Title != "title" || issue.Description != "desc" || issue.Priority != "high" {
		t.Fatalf("unexpected content fields: %+v", issue)
	}
	if issue.Status != "closed" || issue.CloseSource != "external" {
		t.Fatalf("status/closeSource = %q/%q, want closed/external", issue.Status, issue.CloseSource)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !issue.CreatedAt.Equal(want) {
		t.Fatalf("createdAt = %v, want %v", issue.CreatedAt, want)
	}
	if issue.UpdatedAt.Before(before) {
		t.Fatalf("updatedAt = %v, want >= %v", issue.UpdatedAt, before)
	}
	ref := issue.ExternalRef
	if ref == nil {
		t.Fatal("externalRef is nil")
	}
	if ref.Source != "gongfeng" || ref.ExternalID != "9001" || ref.ExternalIID != 42 || ref.ExternalState != "closed" || ref.ExternalType != "issue" {
		t.Fatalf("unexpected externalRef: %+v", ref)
	}
	if ref.ExternalURL != "https://git.example.com/group/app/issues/42" {
		t.Fatalf("externalURL = %q", ref.ExternalURL)
	}
	if ref.SyncedAt.Before(before) {
		t.Fatalf("syncedAt = %v, want >= %v", ref.SyncedAt, before)
	}
}

func TestGongfengSyncedIssueKeepsExistingLocalFields(t *testing.T) {
	repo := domain.Repository{ID: "repo-1", ProjectID: "proj-1", WebURL: "https://git.example.com/group/app"}
	created := time.Date(2025, 5, 6, 7, 8, 9, 0, time.UTC)
	existing := domain.Issue{
		ID:             "local-7",
		Number:         7,
		Status:         "in_progress",
		CreatedAt:      created,
		AssigneeUserID: 11,
	}
	gi := gongfengIssue{ID: 9002, IID: 43, Title: "remote title", State: "opened", CreatedAt: "2026-01-02T03:04:05Z"}

	issue := gongfengSyncedIssue(repo, gi, existing, true)

	if issue.ID != "local-7" || issue.Number != 7 {
		t.Fatalf("id/number = %q/%d, want local-7/7", issue.ID, issue.Number)
	}
	if issue.Status != "in_progress" || issue.CloseSource != "" {
		t.Fatalf("status/closeSource = %q/%q, want local status kept", issue.Status, issue.CloseSource)
	}
	if !issue.CreatedAt.Equal(created) || issue.AssigneeUserID != 11 {
		t.Fatalf("createdAt/assignee = %v/%d, want existing values", issue.CreatedAt, issue.AssigneeUserID)
	}
	if issue.Title != "remote title" || issue.Priority != "medium" {
		t.Fatalf("title/priority = %q/%q, want remote title and default priority", issue.Title, issue.Priority)
	}
	if issue.ExternalRef == nil || issue.ExternalRef.ExternalState != "opened" || issue.ExternalRef.ExternalURL != "https://git.example.com/group/app/issues/43" {
		t.Fatalf("unexpected externalRef: %+v", issue.ExternalRef)
	}
}
