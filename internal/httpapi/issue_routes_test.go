package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestApplyClarificationSubmissionDraftAndFinal(t *testing.T) {
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	issue := domain.Issue{ID: "i", ProjectID: "p", Kind: "requirement", Description: "原始描述"}

	draft, err := applyClarificationSubmission(issue, domain.Project{}, false, clarificationSubmission{Summary: " 草稿 "}, now)
	if err != nil || draft.Clarification.Status != "in_progress" || draft.Clarification.Summary != "草稿" || draft.Description != "原始描述" {
		t.Fatalf("draft should only record progress: %+v %v", draft.Clarification, err)
	}

	_, err = applyClarificationSubmission(issue, domain.Project{}, false, clarificationSubmission{Ready: true}, now)
	var he HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusBadRequest {
		t.Fatalf("final submission without summary should be rejected, got %v", err)
	}

	final, err := applyClarificationSubmission(issue, domain.Project{}, false, clarificationSubmission{Summary: "结论", Ready: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	a := final.Clarification
	if a.Status != domain.ClarificationReady || !a.Gated || !a.AssessedAt.Equal(now) || a.WriteBackStatus != "pending" || a.WriteBackFingerprint == "" {
		t.Fatalf("final submission state: %+v", a)
	}
	if !strings.Contains(final.Description, "结论") || !strings.HasPrefix(final.Description, "原始描述") {
		t.Fatalf("summary should be appended to the description: %q", final.Description)
	}

	written := final
	written.Clarification.WriteBackStatus = "succeeded"
	retried, err := applyClarificationSubmission(written, domain.Project{}, false, clarificationSubmission{Retry: true}, now)
	if err != nil || retried.Clarification.WriteBackStatus != "succeeded" {
		t.Fatalf("retrying an already written summary should not reset write-back: %+v %v", retried.Clarification, err)
	}
	_, err = applyClarificationSubmission(issue, domain.Project{}, false, clarificationSubmission{Retry: true}, now)
	if !errors.As(err, &he) {
		t.Fatalf("retry without any saved summary should be rejected, got %v", err)
	}
}

func TestApplyClarificationSubmissionBugsAlwaysPass(t *testing.T) {
	bug := domain.Issue{ID: "b", Kind: "issue"}
	out, err := applyClarificationSubmission(bug, domain.Project{}, false, clarificationSubmission{Summary: "复现步骤", Ready: true}, time.Now())
	if err != nil || !out.Clarification.Passed || out.Clarification.Gated {
		t.Fatalf("bug-style items pass without gating: %+v %v", out.Clarification, err)
	}
}

func TestMergeIssueEditKeepsStoredFieldsAndAuditLog(t *testing.T) {
	existing := domain.Issue{ID: "i", ProjectID: "p", Title: "旧标题", WorkflowID: "wf", BaseBranch: "main",
		Operations: []domain.IssueOperation{{Action: "create"}}}
	merged := mergeIssueEdit(domain.Issue{ID: "i", Title: "新标题", Operations: []domain.IssueOperation{{Action: "forged"}}}, existing)
	if merged.Title != "新标题" || merged.ProjectID != "p" || merged.WorkflowID != "wf" || merged.BaseBranch != "main" {
		t.Fatalf("merge: %+v", merged)
	}
	if len(merged.Operations) != 1 || merged.Operations[0].Action != "create" {
		t.Fatalf("operations must come from the stored issue: %+v", merged.Operations)
	}
	merged.Operations[0].Action = "mutated"
	if existing.Operations[0].Action != "create" {
		t.Fatal("merged operations must not alias the stored slice")
	}
}

func TestValidateManualTapdID(t *testing.T) {
	cases := []struct {
		issue  domain.Issue
		target string
		ok     bool
	}{
		{domain.Issue{}, "", true},
		{domain.Issue{TapdID: "123"}, "local", true},
		{domain.Issue{TapdID: "12a"}, "", false},
		{domain.Issue{TapdID: "123", Source: "tapd"}, "", false},
		{domain.Issue{TapdID: "123"}, "gongfeng", false},
	}
	for _, tc := range cases {
		if err := validateManualTapdID(tc.issue, tc.target); (err == nil) != tc.ok {
			t.Fatalf("%+v target=%q: err=%v", tc.issue, tc.target, err)
		}
	}
}

func TestIssueRoutesCoverRegisteredPaths(t *testing.T) {
	for path, route := range issueRoutes {
		if route.handle == nil || len(route.methods) == 0 {
			t.Fatalf("%s: incomplete route", path)
		}
	}
	if len(issueRoutes) != 13 {
		t.Fatalf("expected 13 issue sub-routes, got %d", len(issueRoutes))
	}
}
