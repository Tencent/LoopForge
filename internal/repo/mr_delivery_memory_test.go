package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestRunMRTextPrefersMRBodyOverPatch(t *testing.T) {
	text := runMRText(domain.Run{Artifacts: []domain.Artifact{
		{Name: "repository-patch", NodeID: "mr", Content: "-https://git.example.com/org/repo/merge_requests/282\n+https://git.example.com/org/repo/merge_requests/286"},
		{Name: "mr-body", NodeID: "mr", Content: "https://git.example.com/org/repo/-/merge_requests/286"},
		{Name: "closeout", NodeID: "close", Content: "https://git.example.com/org/repo/merge_requests/286"},
	}})
	refs := domain.ParseGongfengMRs(text)
	if len(refs) != 1 || refs[0].IID != 286 {
		t.Fatalf("expected only !286 from mr-body, got %+v text=%q", refs, text)
	}
}

func TestBetterAnalyticsMRPrefersIssueDelivery(t *testing.T) {
	older := mrRef{project: "org/repo", iid: 282, createdAt: time.Now().UTC(), sourceRank: 10}
	own := mrRef{project: "org/repo", iid: 286, createdAt: time.Now().UTC().Add(-time.Hour), sourceRank: 10}
	if !betterAnalyticsMR("org/repo", 286, older, "integrated", own, "integrated") {
		t.Fatal("issue delivery MR should win when both are on the baseline")
	}
	if betterAnalyticsMR("org/repo", 286, own, "integrated", older, "integrated") {
		t.Fatal("should not replace the preferred delivery MR")
	}
}

func TestApplyMRDeliveryStatesMemoryStore(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	items := []domain.AnalyticsDelivery{{
		IssueID: "gf-1", MRProject: "org/repo", MRIID: 9,
		MRURL:   "https://git.example.com/org/repo/merge_requests/9",
		MRState: "unknown", SOPSucceeded: true,
	}}
	issue, _ := s.Issue("gf-1")
	s.applyMRDeliveryStates(items, map[string][]mrRef{
		"gf-1": {{project: "org/repo", iid: 9, url: "https://git.example.com/org/repo/merge_requests/9"}},
	}, map[string]domain.Issue{"gf-1": issue})
	if items[0].MRState != "unknown" || items[0].Delivered {
		t.Fatalf("memory store keeps unknown without db: %#v", items[0])
	}
}

func TestMarkAndReopenDeliveredIssue(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	issue := domain.Issue{ID: "tapd-1", ProjectID: "platform", Source: "tapd", Status: "in_review", AutomationState: "eligible"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	mergedAt := time.Now().UTC().Add(-time.Minute)
	if err := s.MarkIssueDelivered(issue.ID, "https://git.example.com/org/repo/merge_requests/1", mergedAt); err != nil {
		t.Fatal(err)
	}
	locked, _ := s.Issue(issue.ID)
	if !locked.DeliveryLocked() || locked.Status != "done" || locked.AutomationState != "ignored" {
		t.Fatalf("merged issue not locked: %+v", locked)
	}
	reopened, err := s.ReopenDeliveredIssue(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.DeliveryState != "reopened" || reopened.Status != "todo" || reopened.AutomationState != "eligible" {
		t.Fatalf("issue not reopened: %+v", reopened)
	}
	if err := s.MarkIssueDelivered(issue.ID, locked.DeliveryMRURL, mergedAt); err != nil {
		t.Fatal(err)
	}
	stillOpen, _ := s.Issue(issue.ID)
	if stillOpen.DeliveryState != "reopened" {
		t.Fatalf("old merged MR relocked reopened issue: %+v", stillOpen)
	}
	if err := s.MarkIssueDelivered(issue.ID, locked.DeliveryMRURL, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	stillOpen, _ = s.Issue(issue.ID)
	if stillOpen.DeliveryState != "reopened" {
		t.Fatalf("newer MR check must not relock a reopened issue: %+v", stillOpen)
	}
	if err := s.MarkIssueDeliveredFromSOP(issue.ID, "https://git.example.com/org/repo/merge_requests/2", time.Now().UTC().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	lockedAgain, _ := s.Issue(issue.ID)
	if !lockedAgain.DeliveryLocked() {
		t.Fatalf("new SOP success should lock a reopened issue: %+v", lockedAgain)
	}
}

func TestExternalClosedIssueKeepsStatusDuringDelivery(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	issue := domain.Issue{
		ID: "gf-closed", ProjectID: "platform", Source: "gongfeng",
		Status: "closed", CloseSource: "external",
		ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalState: "closed"},
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkIssueDelivered(issue.ID, "https://git.example.com/org/repo/merge_requests/1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if got.Status != "closed" || got.CloseSource != "external" || !got.DeliveryLocked() {
		t.Fatalf("MR delivery must not turn external closure into done: %+v", got)
	}
	if _, err := s.ReopenDeliveredIssue(issue.ID); err == nil {
		t.Fatal("externally closed issue must be reopened in its tracker first")
	}
}

func TestReconcileCompletedDeliveries(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	issue := domain.Issue{ID: "tapd-done", ProjectID: "platform", Source: "tapd", LatestTaskID: "task-1"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "task-1", IssueID: issue.ID, RunID: "run-1", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{ID: "run-1", Status: "succeeded", UpdatedAt: time.Now().UTC(), ExecutionContext: domain.TelemetryContext{IssueID: issue.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileCompletedDeliveries(); err != nil {
		t.Fatal(err)
	}
	locked, _ := s.Issue(issue.ID)
	if !locked.DeliveryLocked() {
		t.Fatalf("expected succeeded SOP to lock issue: %+v", locked)
	}
}

func TestClassifyMRDeliveryUsesRepoBranches(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutRepository(domain.Repository{
		ID: "repo-skillhub", ProjectID: "platform", Name: "skillhub",
		GongfengProjectID: "example/team-project",
		DefaultBranch:     "master",
		IntegrateBranch:   "agentflow/integration",
	}); err != nil {
		t.Fatal(err)
	}
	baseline := s.classifyMRDelivery(domain.MRDeliveryState{
		MRProject: "example/team-project", State: "merged", TargetBranch: "agentflow/integration",
	})
	if baseline != "integrated" {
		t.Fatalf("baseline merge classified as %q", baseline)
	}
	mainline := s.classifyMRDelivery(domain.MRDeliveryState{
		MRProject: "example/team-project", State: "merged", TargetBranch: "master",
	})
	if mainline != "merged" {
		t.Fatalf("mainline merge classified as %q", mainline)
	}
	pending := s.classifyMRDelivery(domain.MRDeliveryState{
		MRProject: "example/team-project", State: "merged", TargetBranch: "",
	})
	if pending != "unknown" {
		t.Fatalf("merged without target classified as %q", pending)
	}
}

func TestPromoteIssueToMainlineKeepsWorkMR(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	issue := domain.Issue{
		ID: "gf-skillhub-32", ProjectID: "platform", Source: "gongfeng",
		Status: "done", DeliveryState: "merged", DeliveryMRState: "integrated",
		DeliveryMRURL:     "https://git.example.com/example/team-project/merge_requests/293",
		DeliveryUpdatedAt: time.Now().UTC().Add(-time.Hour),
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := s.PromoteIssueToMainline(issue.ID, issue.DeliveryMRURL, at); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if got.DeliveryMRState != "merged" {
		t.Fatalf("expected mainline label, got %+v", got)
	}
	if got.DeliveryMRURL != issue.DeliveryMRURL {
		t.Fatalf("work MR URL changed: %s", got.DeliveryMRURL)
	}
	if err := s.ReconcileDeliveryMRStates(); err != nil {
		t.Fatal(err)
	}
	still, _ := s.Issue(issue.ID)
	if still.DeliveryMRState != "merged" {
		t.Fatalf("reconcile downgraded mainline label: %+v", still)
	}
}

func TestPromoteSkipsWorkAfterPromote(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	promoteAt := time.Now().UTC().Add(-time.Hour)
	old := domain.Issue{
		ID: "gf-old", ProjectID: "platform",
		DeliveryState: "merged", DeliveryMRState: "integrated",
		DeliveryMRURL:     "https://git.example.com/org/repo/merge_requests/10",
		DeliveryUpdatedAt: promoteAt.Add(-time.Hour),
	}
	newer := domain.Issue{
		ID: "gf-new", ProjectID: "platform",
		DeliveryState: "merged", DeliveryMRState: "integrated",
		DeliveryMRURL:     "https://git.example.com/org/repo/merge_requests/20",
		DeliveryUpdatedAt: promoteAt.Add(time.Hour),
	}
	if err := s.PutIssue(old); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(newer); err != nil {
		t.Fatal(err)
	}
	if err := s.promoteIntegratedIssues("org/repo", promoteAt); err != nil {
		t.Fatal(err)
	}
	gotOld, _ := s.Issue(old.ID)
	gotNew, _ := s.Issue(newer.ID)
	if gotOld.DeliveryMRState != "merged" {
		t.Fatalf("older baseline MR should promote: %+v", gotOld)
	}
	if gotNew.DeliveryMRState != "integrated" {
		t.Fatalf("newer baseline MR must stay integrated: %+v", gotNew)
	}
}

func TestApplyDefaultBranchLandingUsesCommitNotTime(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	missing := "https://git.example.com/example/team-project/merge_requests/463"
	missingIssue := domain.Issue{
		ID: "gf-skillhub-77", ProjectID: "platform", Status: "done",
		DeliveryState: "merged", DeliveryMRState: "merged", DeliveryMRURL: missing,
	}
	present := "https://git.example.com/example/team-project/merge_requests/457"
	presentIssue := domain.Issue{
		ID: "gf-skillhub-76", ProjectID: "platform", Status: "done",
		DeliveryState: "merged", DeliveryMRState: "integrated", DeliveryMRURL: present,
	}
	for _, issue := range []domain.Issue{missingIssue, presentIssue} {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ApplyDefaultBranchLanding(missingIssue.ID, missing, false); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyDefaultBranchLanding(presentIssue.ID, present, true); err != nil {
		t.Fatal(err)
	}
	gotMissing, _ := s.Issue(missingIssue.ID)
	if gotMissing.DeliveryMRState != "integrated" || gotMissing.DeliveryMRURL != missing {
		t.Fatalf("commit absent from master should be baseline: %+v", gotMissing)
	}
	gotPresent, _ := s.Issue(presentIssue.ID)
	if gotPresent.DeliveryMRState != "merged" || gotPresent.DeliveryMRURL != present {
		t.Fatalf("commit on master should be mainline: %+v", gotPresent)
	}
}

func TestApplyMRDeliveryStatesHonorsPromotedIssue(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutIssue(domain.Issue{
		ID: "gf-1", DeliveryState: "merged", DeliveryMRState: "merged",
		DeliveryMRURL: "https://git.example.com/org/repo/merge_requests/9",
	}); err != nil {
		t.Fatal(err)
	}
	items := []domain.AnalyticsDelivery{{
		IssueID: "gf-1", MRProject: "org/repo", MRIID: 9,
		MRURL:   "https://git.example.com/org/repo/merge_requests/9",
		MRState: "integrated", SOPSucceeded: true,
	}}
	issue, _ := s.Issue("gf-1")
	s.applyMRDeliveryStates(items, map[string][]mrRef{
		"gf-1": {{project: "org/repo", iid: 9, url: items[0].MRURL}},
	}, map[string]domain.Issue{"gf-1": issue})
	if items[0].MRState != "merged" || !items[0].Delivered {
		t.Fatalf("promoted issue should count as delivered: %#v", items[0])
	}
}

func TestFinalizeAnalyticsDeliveryMerged(t *testing.T) {
	item := domain.AnalyticsDelivery{SOPSucceeded: true, MRURL: "http://x", MRState: "merged"}
	domain.FinalizeAnalyticsDelivery(&item)
	if !item.Delivered {
		t.Fatal("expected delivered")
	}
}

func TestStaleOpenedMRDoesNotOverwriteCanonicalDelivery(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	canonical := "https://git.example.com/example/team-project/merge_requests/248"
	stale := "https://git.example.com/example/team-project/merge_requests/183"
	issue := domain.Issue{
		ID: "gf-19", ProjectID: "skillhub", Source: "gongfeng",
		Status: "done", DeliveryState: "merged", DeliveryMRState: "merged",
		DeliveryMRURL:     canonical,
		DeliveryUpdatedAt: time.Now().UTC().Add(-time.Hour),
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureMRDeliveryState("example/team-project", 248, canonical, issue.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMRDeliveryState("example/team-project", 248, "merged", "master", time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureMRDeliveryState("example/team-project", 183, stale, issue.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMRDeliveryState("example/team-project", 183, "opened", "master", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if got.DeliveryMRURL != canonical {
		t.Fatalf("canonical MR URL changed: %s", got.DeliveryMRURL)
	}
	if got.DeliveryMRState != "merged" {
		t.Fatalf("stale opened MR overwrote delivery label: %+v", got)
	}
}

func TestCanonicalMRPollStillUpdatesDeliveryLabel(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	canonical := "https://git.example.com/org/repo/-/merge_requests/9"
	issue := domain.Issue{
		ID: "gf-1", Status: "done", DeliveryState: "merged", DeliveryMRState: "opened",
		DeliveryMRURL:     "https://git.example.com/org/repo/merge_requests/9",
		DeliveryUpdatedAt: time.Now().UTC().Add(-time.Hour),
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureMRDeliveryState("org/repo", 9, canonical, issue.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMRDeliveryState("org/repo", 9, "merged", "master", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if got.DeliveryMRState != "merged" {
		t.Fatalf("canonical merged poll should set MR 已合入, got %+v", got)
	}
}

func TestMultiRepositoryDeliveryMRs(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	issue := domain.Issue{ID: "tapd-multi", ProjectID: "platform", Source: "tapd", Status: "in_review"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	urlA := "https://git.example.com/org/a/-/merge_requests/1"
	urlB := "https://git.example.com/org/b/-/merge_requests/2"
	at := time.Now().UTC().Add(-time.Hour)
	if err := s.MarkIssueDeliveredFromSOPMRs(issue.ID, []domain.DeliveryMR{
		{RepositoryID: "a", Repository: "a", Project: "org/a", IID: 1, URL: urlA, GongfengState: "merged", TargetBranch: "master"},
		{RepositoryID: "b", Repository: "b", Project: "org/b", IID: 2, URL: urlB, GongfengState: "opened", TargetBranch: "master"},
	}, at); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if !got.DeliveryLocked() || len(got.DeliveryMRs) != 2 || got.DeliveryMRURL != urlA || got.DeliveryMRState != "opened" {
		t.Fatalf("multi-repo delivery not recorded: %+v", got)
	}
	if got.DeliveryMRs[0].State != "merged" || got.DeliveryMRs[1].State != "opened" || got.DeliveryLockedLabel() != "交付闭环" {
		t.Fatalf("per-MR states / label wrong: %+v label=%s", got.DeliveryMRs, got.DeliveryLockedLabel())
	}
	// Both MRs are registered for polling; the second merge lands the item.
	if err := s.UpdateMRDeliveryState("org/b", 2, "merged", "master", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Issue(issue.ID)
	if got.DeliveryMRState != "merged" || got.DeliveryLockedLabel() != "MR 已合入" || got.DeliveryMRURL != urlA {
		t.Fatalf("all MRs merged must read MR 已合入: %+v", got)
	}
	// A leftover MR from an earlier run must not join or relabel the item.
	if err := s.EnsureMRDeliveryState("org/a", 9, "https://git.example.com/org/a/-/merge_requests/9", issue.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMRDeliveryState("org/a", 9, "closed", "master", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Issue(issue.ID)
	if len(got.DeliveryMRs) != 2 || got.DeliveryMRState != "merged" {
		t.Fatalf("leftover MR changed delivery MRs: %+v", got)
	}
}

func TestReconcileDeliveryMRStatesRecoversHTTPArtifactLink(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	issue := domain.Issue{
		ID: "tapd-http-mr", ProjectID: "privatedns", Status: "done",
		DeliveryState: "merged", LatestTaskID: "task-http",
		DeliveryUpdatedAt: time.Now().UTC().Add(-time.Hour),
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "task-http", IssueID: issue.ID, RunID: "run-http", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "run-http", Status: "succeeded", UpdatedAt: time.Now().UTC(),
		ExecutionContext: domain.TelemetryContext{IssueID: issue.ID},
		Repositories: []domain.RepositoryContext{{
			RepositoryID: "vpcdns", Name: "vpcdns_oss", GongfengProjectID: "dnspod/vpcdns/vpcdns_oss",
			WorkBranch: "feature/story-1",
		}},
		Artifacts: []domain.Artifact{{
			NodeID: "mr", Name: "mr-body",
			Content: "http://git.example.com/dnspod/vpcdns/vpcdns_oss/merge_requests/558",
		}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileUnlinkedDeliveryMRs(); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	want := "https://git.example.com/dnspod/vpcdns/vpcdns_oss/-/merge_requests/558"
	if len(got.DeliveryMRs) != 1 || got.DeliveryMRURL != want || got.DeliveryMRs[0].IID != 558 {
		t.Fatalf("locked issue did not recover http MR: %+v", got)
	}
	if !got.DeliveryLocked() || got.Status != "done" {
		t.Fatalf("recovery must keep the item delivered: %+v", got)
	}
}

func TestReconcileDeliveryMRStatesMigratesLegacySingleMR(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	url := "https://git.example.com/org/a/-/merge_requests/3"
	issue := domain.Issue{ID: "tapd-legacy", ProjectID: "platform", Status: "done", DeliveryState: "merged", DeliveryMRURL: url, DeliveryMRState: "opened", DeliveryUpdatedAt: time.Now().UTC()}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureMRDeliveryState("org/a", 3, url, issue.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.recordMRState("org/a", 3, "merged", "master", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileDeliveryMRStates(); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if len(got.DeliveryMRs) != 1 || got.DeliveryMRs[0].URL != url || got.DeliveryMRState != "merged" {
		t.Fatalf("legacy MR not migrated into deliveryMrs: %+v", got)
	}
}
