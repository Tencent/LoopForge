package schedule

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

func TestRunJobDispatchLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		want  int
	}{
		{"unconfigured", 0, 10},
		{"minimum", 1, 1},
		{"custom", 3, 3},
		{"maximum", 10, 10},
		{"invalidStoredAboveMaximum", 11, 10},
		{"invalidStoredNegative", -1, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			for i := range 15 {
				if err := st.PutIssue(domain.Issue{ID: fmt.Sprintf("issue-%02d", i), ProjectID: "platform", Status: "todo"}); err != nil {
					t.Fatal(err)
				}
			}
			job := domain.ScheduleJob{ID: "job", ProjectID: "platform", WorkflowID: "wf", MaxDispatch: tc.limit, Enabled: true}
			if err := st.PutScheduleJob(job); err != nil {
				t.Fatal(err)
			}
			dispatched := map[string]bool{}
			svc := New(st, tracker.NewService(), func(_ domain.ScheduleJob, issue domain.Issue, origin string) error {
				if dispatched[issue.ID] {
					t.Fatalf("issue %s dispatched twice", issue.ID)
				}
				dispatched[issue.ID] = true
				return st.PutTask(domain.Task{ID: "task-" + issue.ID, IssueID: issue.ID, WorkflowID: "wf", Status: "queued"})
			})
			result, err := svc.RunJob(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(dispatched) != tc.want || result.LastDispatched != tc.want || result.LastPulled != 15 || result.LastStatus != "success" {
				t.Fatalf("timer dispatches=%d want=%d result=%+v", len(dispatched), tc.want, result)
			}
			result, err = svc.RunNow(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			remaining := min(tc.want, 15-tc.want)
			stored, _ := st.ScheduleJob(job.ID)
			if len(dispatched) != tc.want+remaining || result.LastDispatched != remaining || stored.LastDispatched != remaining {
				t.Fatalf("manual dispatches=%d result=%+v stored=%+v", len(dispatched), result, stored)
			}
		})
	}
}

func TestRunJobDispatchLimitCountsOnlySuccessfulDispatches(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for i, issue := range []domain.Issue{
		{ID: "already-ran"},
		{ID: "ignored", AutomationState: "ignored"},
		{ID: "blocked", RepositoryID: "blocked-repo"},
		{ID: "duplicate"},
		{ID: "unclarified"},
		{ID: "first"},
		{ID: "second"},
		{ID: "later"},
	} {
		issue.ProjectID = "platform"
		issue.Status = "todo"
		issue.Number = 100 - i
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutTask(domain.Task{ID: "existing", IssueID: "already-ran", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", WorkflowID: "wf", MaxDispatch: 2}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	var dispatched []string
	svc := New(st, tracker.NewService(), func(_ domain.ScheduleJob, issue domain.Issue, _ string) error {
		switch issue.ID {
		case "duplicate":
			return errors.New("已经跑过")
		case "unclarified":
			return errors.New("澄清未达标")
		}
		dispatched = append(dispatched, issue.ID)
		return nil
	})
	svc.SetBaselineGuard(func(domain.ScheduleJob, []domain.Issue) (map[string]domain.ScheduleJobBlocker, error) {
		return map[string]domain.ScheduleJobBlocker{"blocked-repo": {RepositoryID: "blocked-repo", Reason: "基线冲突"}}, nil
	})
	result, err := svc.RunJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(dispatched, ",") != "first,second" || result.LastDispatched != 2 || result.LastStatus != "waiting" || result.LastPulled != 8 {
		t.Fatalf("dispatched=%v result=%+v", dispatched, result)
	}
}

func TestRunJobOnlyDispatchesEligibleUnprocessedIssues(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, issue := range []domain.Issue{
		{ID: "todo", ProjectID: "platform", Source: "gongfeng", Status: "todo", Title: "todo"},
		{ID: "done", ProjectID: "platform", Source: "gongfeng", Status: "done", Title: "done"},
		{ID: "ignored", ProjectID: "platform", Source: "gongfeng", Status: "todo", Title: "ignored", AutomationState: "ignored"},
		{ID: "manual", ProjectID: "platform", Source: "gongfeng", Status: "todo", Title: "manual"},
		{ID: "other-sop", ProjectID: "platform", Source: "gongfeng", Status: "in_progress", Title: "ran another SOP"},
	} {
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutTask(domain.Task{ID: "manual-task", IssueID: "manual", RunID: "manual-run", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	// 需求已经人工跑过另一条 SOP，定时任务不能再给它追加这条 SOP。
	if err := st.PutTask(domain.Task{ID: "other-task", IssueID: "other-sop", WorkflowID: "another-sop", RunID: "other-run", Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	job := domain.ScheduleJob{ID: "job", Name: "Gongfeng", ProjectID: "platform", Source: "gongfeng", WorkflowID: "wf", IntervalSec: 60, Enabled: true}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	var dispatched []string
	var origin string
	svc := New(st, tracker.NewService(), func(_ domain.ScheduleJob, issue domain.Issue, triggerOrigin string) error {
		dispatched = append(dispatched, issue.ID)
		origin = triggerOrigin
		return nil
	})
	result, err := svc.RunJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatched) != 1 || dispatched[0] != "todo" || result.LastDispatched != 1 {
		t.Fatalf("dispatched=%v result=%+v", dispatched, result)
	}
	if origin != "timer" {
		t.Fatalf("trigger origin=%q", origin)
	}
}

func TestRunNowMarksManualScheduleTrigger(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutIssue(domain.Issue{ID: "todo", ProjectID: "platform", Source: "gongfeng", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", Source: "gongfeng", WorkflowID: "wf", Enabled: true}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	var origin string
	svc := New(st, tracker.NewService(), func(_ domain.ScheduleJob, _ domain.Issue, value string) error {
		origin = value
		return nil
	})
	if _, err := svc.RunNow(job.ID); err != nil {
		t.Fatal(err)
	}
	if origin != "manual_run" {
		t.Fatalf("trigger origin=%q", origin)
	}
}

func TestRunJobWaitsWhenBaselineGuardBlocksRepo(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "platform", DefaultRepositoryID: "repo"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "todo", ProjectID: "platform", Source: "gongfeng", Status: "todo", RepositoryID: "repo"}); err != nil {
		t.Fatal(err)
	}
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", Source: "gongfeng", WorkflowID: "wf", Enabled: true}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	var dispatched []string
	svc := New(st, tracker.NewService(), func(_ domain.ScheduleJob, issue domain.Issue, _ string) error {
		dispatched = append(dispatched, issue.ID)
		return nil
	})
	svc.SetBaselineGuard(func(domain.ScheduleJob, []domain.Issue) (map[string]domain.ScheduleJobBlocker, error) {
		return map[string]domain.ScheduleJobBlocker{"repo": {Reason: "集成分支 agentflow/integration 与默认分支 master 存在冲突（same.txt）"}}, nil
	})
	result, err := svc.RunJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatched) != 0 || result.LastStatus != "waiting" || !strings.Contains(result.LastError, "存在冲突") {
		t.Fatalf("blocked repo must wait: dispatched=%v result=%+v", dispatched, result)
	}
	if len(result.Blockers) != 1 || result.Blockers[0].RepositoryID != "repo" || result.Blockers[0].Reason == "" {
		t.Fatalf("waiting job must persist structured blockers: %+v", result.Blockers)
	}
	if result.Blockers[0].Since.IsZero() {
		t.Fatalf("blocker since must be set: %+v", result.Blockers[0])
	}
}

func TestRunJobAssistantPrepBeforeDispatch(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutIssue(domain.Issue{ID: "todo", ProjectID: "platform", Source: "gongfeng", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", Source: "gongfeng", WorkflowID: "wf", Enabled: true, AssistantPrompt: "标 ignored"}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	var dispatched []string
	svc := New(st, tracker.NewService(), func(_ domain.ScheduleJob, issue domain.Issue, _ string) error {
		dispatched = append(dispatched, issue.ID)
		return nil
	})
	svc.SetAssistantPrep(func(job domain.ScheduleJob) (string, error) {
		issue, _ := st.Issue("todo")
		issue.AutomationState = "ignored"
		if err := st.PutIssue(issue); err != nil {
			return "", err
		}
		return "ignored one", nil
	})
	result, err := svc.RunJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatched) != 0 {
		t.Fatalf("ignored after prompt must not dispatch: %v", dispatched)
	}
	if result.LastAssistantMessage != "ignored one" || result.LastDispatched != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestRunJobSkipDispatchDoesNotStartSOP(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutIssue(domain.Issue{ID: "todo", ProjectID: "platform", Source: "gongfeng", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", SkipDispatch: true, Enabled: true, AssistantPrompt: "巡检"}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	var dispatched []string
	svc := New(st, tracker.NewService(), func(_ domain.ScheduleJob, issue domain.Issue, _ string) error {
		dispatched = append(dispatched, issue.ID)
		return nil
	})
	svc.SetAssistantPrep(func(domain.ScheduleJob) (string, error) { return "inspected", nil })
	result, err := svc.RunJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatched) != 0 {
		t.Fatalf("prompt-only job must not dispatch: %v", dispatched)
	}
	if result.LastAssistantMessage != "inspected" || result.LastDispatched != 0 || result.LastStatus != "success" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestJobDueCron(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	job := domain.ScheduleJob{Enabled: true, Cron: "0 * * * *", LastRunAt: time.Date(2026, 9, 14, 10, 0, 0, 0, loc).UTC()}
	if jobDue(job, time.Date(2026, 9, 14, 10, 30, 0, 0, loc).UTC()) {
		t.Fatal("should wait until the next hour")
	}
	if !jobDue(job, time.Date(2026, 9, 14, 11, 0, 0, 0, loc).UTC()) {
		t.Fatal("hourly cron should fire at minute 0")
	}
}

func TestMatchingIssuesUsesCurrentProject(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, issue := range []domain.Issue{
		{ID: "tapd-current", ProjectID: "platform", Source: "tapd", Status: "todo", Title: "current"},
		{ID: "gongfeng-same", ProjectID: "platform", Source: "gongfeng", Status: "todo", Title: "same project"},
		{ID: "tapd-other-project", ProjectID: "other", Source: "tapd", Status: "todo", Title: "other project"},
	} {
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(st, tracker.NewService(), nil)
	issues := svc.matchingIssues(domain.ScheduleJob{ProjectID: "platform"})
	ids := map[string]bool{}
	for _, issue := range issues {
		ids[issue.ID] = true
	}
	if len(issues) != 2 || !ids["tapd-current"] || !ids["gongfeng-same"] || ids["tapd-other-project"] {
		t.Fatalf("matching issues=%+v", issues)
	}
	gongfeng := map[string]bool{}
	for _, issue := range svc.matchingIssues(domain.ScheduleJob{ProjectID: "platform", Source: "gongfeng"}) {
		gongfeng[issue.ID] = true
	}
	if len(gongfeng) != 1 || !gongfeng["gongfeng-same"] {
		t.Fatalf("gongfeng-only matching=%v", gongfeng)
	}
	tapd := map[string]bool{}
	for _, issue := range svc.matchingIssues(domain.ScheduleJob{ProjectID: "platform", Source: "tapd"}) {
		tapd[issue.ID] = true
	}
	if len(tapd) != 1 || !tapd["tapd-current"] {
		t.Fatalf("tapd-only matching=%v", tapd)
	}
}

func TestMatchingIssuesFiltersStatuses(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, issue := range []domain.Issue{
		{ID: "backlog", ProjectID: "platform", Status: "backlog"},
		{ID: "todo", ProjectID: "platform", Status: "todo"},
		{ID: "running", ProjectID: "platform", Status: "in_progress"},
		{ID: "blocked", ProjectID: "platform", Status: "blocked"},
	} {
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(st, tracker.NewService(), nil)
	idsOf := func(job domain.ScheduleJob) map[string]bool {
		ids := map[string]bool{}
		for _, issue := range svc.matchingIssues(job) {
			ids[issue.ID] = true
		}
		return ids
	}
	def := idsOf(domain.ScheduleJob{ProjectID: "platform"})
	if len(def) != 4 || !def["backlog"] || !def["todo"] || !def["running"] || !def["blocked"] {
		t.Fatalf("legacy empty statuses=%v", def)
	}
	narrow := idsOf(domain.ScheduleJob{ProjectID: "platform", IssueStatuses: []string{"backlog", "todo"}})
	if len(narrow) != 2 || !narrow["backlog"] || !narrow["todo"] || narrow["running"] {
		t.Fatalf("new-job statuses=%v", narrow)
	}
	custom := idsOf(domain.ScheduleJob{ProjectID: "platform", IssueStatuses: []string{"in_progress", "blocked"}})
	if len(custom) != 2 || !custom["running"] || !custom["blocked"] || custom["todo"] {
		t.Fatalf("custom statuses=%v", custom)
	}
}

func TestRunJobHonorsSelectedIssueStatuses(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, issue := range []domain.Issue{
		{ID: "todo", ProjectID: "platform", Source: "gongfeng", Status: "todo"},
		{ID: "running", ProjectID: "platform", Source: "gongfeng", Status: "in_progress"},
	} {
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", WorkflowID: "wf", IssueStatuses: []string{"in_progress"}, Enabled: true}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	var dispatched []string
	svc := New(st, tracker.NewService(), func(_ domain.ScheduleJob, issue domain.Issue, _ string) error {
		dispatched = append(dispatched, issue.ID)
		return nil
	})
	result, err := svc.RunJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatched) != 1 || dispatched[0] != "running" || result.LastPulled != 1 || result.LastDispatched != 1 {
		t.Fatalf("dispatched=%v result=%+v", dispatched, result)
	}
}

func TestRunJobHonorsSourceFilter(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, issue := range []domain.Issue{
		{ID: "gf", ProjectID: "platform", Source: "gongfeng", Status: "todo"},
		{ID: "tapd", ProjectID: "platform", Source: "tapd", Status: "todo"},
		{ID: "local", ProjectID: "platform", Source: "local", Status: "todo"},
	} {
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", Source: "gongfeng", WorkflowID: "wf", IssueStatuses: []string{"todo"}, Enabled: true}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	var dispatched []string
	svc := New(st, tracker.NewService(), func(_ domain.ScheduleJob, issue domain.Issue, _ string) error {
		dispatched = append(dispatched, issue.ID)
		return nil
	})
	result, err := svc.RunJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatched) != 1 || dispatched[0] != "gf" || result.LastPulled != 1 || result.LastDispatched != 1 {
		t.Fatalf("dispatched=%v result=%+v", dispatched, result)
	}
}

func TestRunJobDispatchesRequirementsWithoutClarificationGate(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, issue := range []domain.Issue{
		{ID: "thin-req", ProjectID: "platform", Kind: "requirement", Source: "tapd", Status: "todo", WorkflowID: "wf", Clarification: domain.ClarificationAssessment{Status: domain.ClarificationNeedsUser, Gated: true}},
		{ID: "sync-req", ProjectID: "platform", Kind: "requirement", Source: "tapd", Status: "todo", WorkflowID: "wf", Clarification: domain.ClarificationAssessment{Status: domain.ClarificationNeedsUser}},
		{ID: "ready-req", ProjectID: "platform", Kind: "requirement", Source: "tapd", Status: "todo", WorkflowID: "wf", Clarification: domain.ClarificationAssessment{Status: domain.ClarificationReady, Gated: true}},
		{ID: "bug", ProjectID: "platform", Kind: "issue", Source: "gongfeng", Status: "todo", WorkflowID: "wf"},
	} {
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", WorkflowID: "wf", Enabled: true}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	var dispatched []string
	svc := New(st, tracker.NewService(), func(_ domain.ScheduleJob, issue domain.Issue, _ string) error {
		dispatched = append(dispatched, issue.ID)
		return nil
	})
	result, err := svc.RunJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.LastStatus != "success" || result.LastDispatched != 4 {
		t.Fatalf("result=%+v dispatched=%v", result, dispatched)
	}
	seen := map[string]bool{}
	for _, id := range dispatched {
		seen[id] = true
	}
	if !seen["thin-req"] || !seen["sync-req"] || !seen["ready-req"] || !seen["bug"] {
		t.Fatalf("dispatched=%v", dispatched)
	}
}

func TestRunJobPersistsRunningStatus(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", SkipDispatch: true, Enabled: true, AssistantPrompt: "巡检"}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	svc := New(st, tracker.NewService(), nil)
	svc.SetAssistantPrep(func(domain.ScheduleJob) (string, error) {
		close(started)
		<-release
		return "done", nil
	})
	done := make(chan domain.ScheduleJob, 1)
	go func() {
		result, runErr := svc.RunJob(job.ID)
		if runErr != nil {
			t.Errorf("run: %v", runErr)
		}
		done <- result
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("assistant did not start")
	}
	current, ok := st.ScheduleJob(job.ID)
	if !ok || current.LastStatus != "running" || current.LastRunAt.IsZero() {
		t.Fatalf("expected persisted running job, got %+v", current)
	}
	close(release)
	result := <-done
	if result.LastStatus != "success" || result.LastAssistantMessage != "done" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestStartNowReturnsBeforeCompletion(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", SkipDispatch: true, Enabled: true, AssistantPrompt: "巡检"}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	svc := New(st, tracker.NewService(), nil)
	svc.SetAssistantPrep(func(domain.ScheduleJob) (string, error) {
		close(started)
		<-release
		return "inspected", nil
	})
	got, err := svc.StartNow(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != "running" {
		t.Fatalf("StartNow must return running: %+v", got)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background run did not start")
	}
	if current, _ := st.ScheduleJob(job.ID); current.LastStatus != "running" {
		t.Fatalf("store must stay running: %+v", current)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, _ := st.ScheduleJob(job.ID)
		if current.LastStatus == "success" && current.LastAssistantMessage == "inspected" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("background run did not finish: %+v", current)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Loops are long-running AI jobs: one must not block another job, while the
// same job never overlaps itself.
func TestStartNowRunsDifferentJobsConcurrently(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	first := domain.ScheduleJob{ID: "job-a", ProjectID: "platform", SkipDispatch: true, Enabled: true, AssistantPrompt: "a"}
	second := domain.ScheduleJob{ID: "job-b", ProjectID: "platform", SkipDispatch: true, Enabled: true, AssistantPrompt: "b"}
	if err := st.PutScheduleJob(first); err != nil {
		t.Fatal(err)
	}
	if err := st.PutScheduleJob(second); err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 2)
	release := make(chan struct{})
	svc := New(st, tracker.NewService(), nil)
	svc.SetAssistantPrep(func(job domain.ScheduleJob) (string, error) {
		started <- job.ID
		<-release
		return "ok", nil
	})
	if _, err := svc.StartNow(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartNow(second.ID); err != nil {
		t.Fatalf("a running job blocked another job: %v", err)
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("jobs did not run concurrently: %v", seen)
		}
	}
	got, err := svc.StartNow(first.ID)
	if !errors.Is(err, ErrAlreadyRunning) || got.ID != first.ID || got.LastStatus != "running" {
		t.Fatalf("same job overlap err=%v job=%+v", err, got)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		a, _ := st.ScheduleJob(first.ID)
		b, _ := st.ScheduleJob(second.ID)
		if a.LastStatus == "success" && b.LastStatus == "success" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("jobs did not finish: %+v %+v", a, b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDispatchIsSerializedAcrossJobs(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	wf := domain.WorkflowDefinition{ID: "wf", ProjectID: "platform", Name: "wf", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "n", Name: "n", Type: "approval", MaxAttempts: 1}}}
	if err := st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "shared-issue", ProjectID: "platform", Title: "t", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"trigger-a", "trigger-b"} {
		if err := st.PutScheduleJob(domain.ScheduleJob{ID: id, ProjectID: "platform", Enabled: true, WorkflowID: wf.ID, IssueStatuses: []string{"todo"}}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	active, maxActive := 0, 0
	svc := New(st, tracker.NewService(), func(domain.ScheduleJob, domain.Issue, string) error {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	})
	var wg sync.WaitGroup
	for _, id := range []string{"trigger-a", "trigger-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := svc.RunNow(id); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	if maxActive != 1 {
		t.Fatalf("dispatch ran concurrently across jobs: max=%d", maxActive)
	}
}

func TestRecoverInterruptedJobs(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	job := domain.ScheduleJob{ID: "job", Name: "stale", LastStatus: "running", LastError: "", Enabled: true, IntervalSec: 3600}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	svc := New(st, tracker.NewService(), nil)
	svc.recoverInterruptedJobs()
	got, ok := st.ScheduleJob(job.ID)
	if !ok || got.LastStatus != "failed" || !strings.Contains(got.LastError, "服务重启") {
		t.Fatalf("stale running job: %+v", got)
	}
}

func TestJobDueSkipsRunning(t *testing.T) {
	job := domain.ScheduleJob{Enabled: true, IntervalSec: 30, LastStatus: "running", LastRunAt: time.Now().UTC().Add(-time.Hour)}
	if jobDue(job, time.Now().UTC()) {
		t.Fatal("running job must not be due")
	}
}

func TestClaimRejectsPersistedRunningRegardlessOfAge(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	svc := New(st, tracker.NewService(), nil)
	for _, age := range []time.Duration{0, 20 * time.Minute, 180 * time.Minute, 4 * time.Hour} {
		job := domain.ScheduleJob{ID: "job", LastStatus: "running", LastRunAt: time.Now().UTC().Add(-age)}
		if err := st.PutScheduleJob(job); err != nil {
			t.Fatal(err)
		}
		if got, err := svc.StartNow(job.ID); !errors.Is(err, ErrAlreadyRunning) || got.LastStatus != "running" {
			t.Fatalf("age=%s: err=%v job=%+v", age, err, got)
		}
	}
}

func TestCronDropsOccurrencesDuringRun(t *testing.T) {
	for _, status := range []string{"success", "failed"} {
		t.Run(status, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			start := time.Date(2026, 10, 12, 2, 0, 0, 0, time.UTC)
			finish := start.Add(150 * time.Minute)
			job := domain.ScheduleJob{ID: "job", Enabled: true, Cron: "0 * * * *", LastStatus: status, LastRunAt: start}
			neverRun := domain.ScheduleJob{ID: "other", Enabled: true, Cron: "0 * * * *"}
			for _, v := range []domain.ScheduleJob{job, neverRun} {
				if err := st.PutScheduleJob(v); err != nil {
					t.Fatal(err)
				}
			}
			svc := New(st, tracker.NewService(), nil)
			svc.lastFinishedAt[job.ID] = finish
			for _, now := range []time.Time{finish, start.Add(179 * time.Minute)} {
				if _, err := svc.claimJob(job.ID, now); !errors.Is(err, errNotDue) {
					t.Fatalf("missed cron must not run: now=%s err=%v", now, err)
				}
			}
			if _, err := svc.claimJob(job.ID, start.Add(180*time.Minute)); err != nil {
				t.Fatalf("next cron must run: err=%v", err)
			}
			// Another job's run does not delay a job that never ran.
			if _, err := svc.claimJob(neverRun.ID, finish); err != nil {
				t.Fatalf("other job must not wait for this one: err=%v", err)
			}
			got, _ := st.ScheduleJob(job.ID)
			if got.LastRunAt.Equal(start) {
				t.Fatal("next execution must update LastRunAt")
			}
		})
	}
}

func TestTimerRunsAsyncAndSkipsOverlap(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	job := domain.ScheduleJob{ID: "job", Enabled: true, Cron: "* * * * *", SkipDispatch: true, AssistantPrompt: "inspect"}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	svc := New(st, tracker.NewService(), nil)
	started := make(chan struct{}, 2)
	unblock := make(chan struct{})
	defer func() {
		close(unblock)
		deadline := time.Now().Add(2 * time.Second)
		for {
			svc.mu.Lock()
			running := len(svc.running) > 0
			svc.mu.Unlock()
			if !running {
				break
			}
			if time.Now().After(deadline) {
				t.Error("timer did not finish")
				break
			}
			time.Sleep(time.Millisecond)
		}
	}()
	svc.SetAssistantPrep(func(domain.ScheduleJob) (string, error) {
		started <- struct{}{}
		<-unblock
		return "ok", nil
	})
	returned := make(chan struct{})
	go func() { svc.runDueJobs(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler blocked on execution")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timer did not start")
	}
	before, _ := st.ScheduleJob(job.ID)
	svc.runDueJobsAt(before.LastRunAt.Add(2 * time.Hour))
	after, _ := st.ScheduleJob(job.ID)
	if after.LastStatus != "running" || !after.LastRunAt.Equal(before.LastRunAt) {
		t.Fatalf("overlap changed running job: before=%+v after=%+v", before, after)
	}
	select {
	case <-started:
		t.Fatal("overlapping cron started another execution")
	default:
	}
}

func TestStartAfterDefersInterruptedJobRecoveryUntilGateCloses(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	// A predecessor process is still executing this job during a handover.
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", Source: "gongfeng", WorkflowID: "wf", Enabled: true, LastStatus: "running"}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	svc := New(st, tracker.NewService(), func(domain.ScheduleJob, domain.Issue, string) error { return nil })
	gate := make(chan struct{})
	svc.StartAfter(gate)
	t.Cleanup(svc.Stop)
	time.Sleep(50 * time.Millisecond)
	if got, _ := st.ScheduleJob(job.ID); got.LastStatus != "running" {
		t.Fatalf("successor failed a job its predecessor is running: %+v", got)
	}
	close(gate)
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _ := st.ScheduleJob(job.ID)
		if got.LastStatus == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("interrupted job not recovered after the predecessor exited: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	svc.Stop()
	svc.Stop()
}
