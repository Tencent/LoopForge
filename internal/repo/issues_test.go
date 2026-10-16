package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestDefaultWorkflowID(t *testing.T) {
	if got := DefaultWorkflowID("gongfeng", ""); got != issueFixWorkflowID {
		t.Fatalf("gongfeng default: %s", got)
	}
	if got := DefaultWorkflowID("tapd", "requirement"); got != requirementWorkflowID {
		t.Fatalf("tapd default: %s", got)
	}
	if got := DefaultWorkflowID("local", "issue"); got != issueFixWorkflowID {
		t.Fatalf("local issue default: %s", got)
	}
}

func TestPutIssueDefaultsWorkflowWithoutOverwritingExplicitSelection(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutIssue(domain.Issue{ID: "gf-1", ProjectID: "platform", Source: "gongfeng", Title: "bug", WorkflowID: requirementWorkflowID}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "tapd-1", ProjectID: "platform", Source: "tapd", Title: "story", WorkflowID: issueFixWorkflowID}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "local-1", ProjectID: "platform", Title: "local"}); err != nil {
		t.Fatal(err)
	}
	gf, _ := s.Issue("gf-1")
	if gf.WorkflowID != requirementWorkflowID || gf.Kind != "issue" {
		t.Fatalf("gongfeng: %+v", gf)
	}
	tapd, _ := s.Issue("tapd-1")
	if tapd.WorkflowID != issueFixWorkflowID {
		t.Fatalf("tapd: %+v", tapd)
	}
	local, _ := s.Issue("local-1")
	if local.WorkflowID != requirementWorkflowID {
		t.Fatalf("local: %+v", local)
	}
}

func TestPutIssueClassifiesTrackerClosureWithoutSourceHint(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	issue := domain.Issue{
		ID: "gf-closed-task", ProjectID: "platform", Source: "gongfeng", Status: "todo",
		ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalState: "opened"},
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	issue.Status = "closed" // A task refresh can set status before a tracker pull.
	issue.ExternalRef.ExternalState = "closed"
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if got.Status != "closed" || got.CloseSource != "external" {
		t.Fatalf("tracker closure classified as manual: %+v", got)
	}
}

func TestPutIssueUsesProjectDefaultRequirementWorkflow(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	const projectSOP = "igtm-requirement-dev"
	if err := s.PutProject(domain.Project{ID: "igtm", Key: "IGTM", Name: "igtm", WorkflowID: projectSOP}); err != nil {
		t.Fatal(err)
	}

	// New requirement (tapd) uses the project's configured default SOP.
	if err := s.PutIssue(domain.Issue{ID: "req-1", ProjectID: "igtm", Source: "tapd", Title: "story"}); err != nil {
		t.Fatal(err)
	}
	if req, _ := s.Issue("req-1"); req.WorkflowID != projectSOP {
		t.Fatalf("requirement should use project default %q, got %+v", projectSOP, req)
	}

	// Local requirement (no source) also uses the project default SOP.
	if err := s.PutIssue(domain.Issue{ID: "req-2", ProjectID: "igtm", Title: "local requirement"}); err != nil {
		t.Fatal(err)
	}
	if req, _ := s.Issue("req-2"); req.WorkflowID != projectSOP {
		t.Fatalf("local requirement should use project default %q, got %+v", projectSOP, req)
	}

	// Defects (gongfeng) stay on the production repair loop unless the project
	// has its own {projectId}-issue-fix SOP.
	if err := s.PutIssue(domain.Issue{ID: "bug-1", ProjectID: "igtm", Source: "gongfeng", Title: "bug"}); err != nil {
		t.Fatal(err)
	}
	if bug, _ := s.Issue("bug-1"); bug.WorkflowID != issueFixWorkflowID || bug.Kind != "issue" {
		t.Fatalf("gongfeng defect should use repair loop, got %+v", bug)
	}

	if err := s.PutProject(domain.Project{ID: "gslb", Key: "GSLB", Name: "GSLB"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "gslb-issue-fix", Name: "GSLB Issue", SandboxProfileID: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "bug-gslb", ProjectID: "gslb", Source: "gongfeng", Title: "bug"}); err != nil {
		t.Fatal(err)
	}
	if bug, _ := s.Issue("bug-gslb"); bug.WorkflowID != "gslb-issue-fix" {
		t.Fatalf("gslb defect should use gslb-issue-fix, got %+v", bug)
	}

	// Explicit selection is never overwritten by the project default.
	if err := s.PutIssue(domain.Issue{ID: "req-3", ProjectID: "igtm", Source: "tapd", Title: "explicit", WorkflowID: "custom-flow"}); err != nil {
		t.Fatal(err)
	}
	if req, _ := s.Issue("req-3"); req.WorkflowID != "custom-flow" {
		t.Fatalf("explicit workflow was overwritten: %+v", req)
	}

	// Project without a configured SOP falls back to the production default.
	if err := s.PutProject(domain.Project{ID: "bare", Key: "BARE", Name: "bare"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "req-4", ProjectID: "bare", Source: "tapd", Title: "story"}); err != nil {
		t.Fatal(err)
	}
	if req, _ := s.Issue("req-4"); req.WorkflowID != requirementWorkflowID {
		t.Fatalf("bare project requirement should fall back to %q, got %+v", requirementWorkflowID, req)
	}
}

// 新项目默认 SOP 是内置通用模板（LoopForge 后端交付）时，同步/新建的需求与
// 工蜂缺陷都默认使用它，保证开箱即用。
func TestPutIssueBindsGenericProjectDefaultForAllIssues(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "newproj", Key: "NEW", Name: "new", WorkflowID: GenericBackendDeliveryWorkflowID}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "req-np", ProjectID: "newproj", Source: "tapd", Title: "story"}); err != nil {
		t.Fatal(err)
	}
	if req, _ := s.Issue("req-np"); req.WorkflowID != GenericBackendDeliveryWorkflowID {
		t.Fatalf("requirement should use generic project default, got %+v", req)
	}
	if err := s.PutIssue(domain.Issue{ID: "bug-np", ProjectID: "newproj", Source: "gongfeng", Title: "bug"}); err != nil {
		t.Fatal(err)
	}
	if bug, _ := s.Issue("bug-np"); bug.WorkflowID != GenericBackendDeliveryWorkflowID || bug.Kind != "issue" {
		t.Fatalf("gongfeng issue should use generic project default, got %+v", bug)
	}
	// 项目专属缺陷 SOP 仍然优先于项目通用默认。
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "newproj-issue-fix", Name: "Newproj fix", SandboxProfileID: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "bug-np-2", ProjectID: "newproj", Source: "gongfeng", Title: "bug 2"}); err != nil {
		t.Fatal(err)
	}
	if bug, _ := s.Issue("bug-np-2"); bug.WorkflowID != "newproj-issue-fix" {
		t.Fatalf("project issue-fix SOP should win, got %+v", bug)
	}
}

func TestPutIssuePreservesDeliveryFieldsOnSyncOverwrite(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	deliveredAt := time.Now().UTC()
	if err := s.PutIssue(domain.Issue{
		ID:                "gf-5",
		ProjectID:         "platform",
		Source:            "gongfeng",
		Title:             "delivered",
		Status:            "done",
		DeliveryState:     "merged",
		DeliveryMRURL:     "https://git.example.com/org/repo/merge_requests/5",
		DeliveryMRState:   "merged",
		DeliveryUpdatedAt: deliveredAt,
		ExternalRef:       &domain.ExternalRef{Source: "gongfeng", ExternalIID: 5, ExternalState: "closed"},
	}); err != nil {
		t.Fatal(err)
	}
	// Gongfeng pull snapshot without delivery fields (sync race).
	if err := s.PutIssue(domain.Issue{
		ID:          "gf-5",
		ProjectID:   "platform",
		Source:      "gongfeng",
		Title:       "delivered",
		Status:      "done",
		ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalIID: 5, ExternalState: "closed"},
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Issue("gf-5")
	if !ok || got.DeliveryState != "merged" || got.DeliveryMRState != "merged" {
		t.Fatalf("delivery fields lost: %+v", got)
	}
}

func TestRestoreTrackerBindingFromStableID(t *testing.T) {
	var issue domain.Issue
	RestoreTrackerBinding(&issue, domain.Issue{ID: "tapd-1010000000118483777"})
	if !IssueBoundToTracker(issue) || issue.Source != "tapd" || issue.ExternalRef.ExternalID != "1010000000118483777" {
		t.Fatalf("tapd restore: %+v", issue)
	}
	bound := domain.Issue{Source: "tapd", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "keep-me", ExternalURL: "https://tapd.example.com/x"}}
	wiped := domain.Issue{ID: bound.ID}
	RestoreTrackerBinding(&wiped, bound)
	if wiped.ExternalRef == nil || wiped.ExternalRef.ExternalID != "keep-me" {
		t.Fatalf("existing snapshot should win: %+v", wiped)
	}
}
