package httpapi

import (
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/schedule"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

func TestScheduleJobCRUD(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutWorkflow(domain.WorkflowDefinition{ID: "scheduled-wf", Name: "Scheduled", SandboxProfileID: "local"}); err != nil {
		t.Fatal(err)
	}
	h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	created := requestJSON[domain.ScheduleJob](t, h, http.MethodPost, "/api/schedule-jobs", map[string]any{
		"name": "扫描需求", "projectId": "platform", "workflowId": "scheduled-wf",
		"useIssueWorkflow": true, "intervalSec": 3600, "enabled": true,
	}, http.StatusOK)
	if created.ID == "" || created.IntervalSec != 3600 || created.Cron != "" || !created.Enabled || created.SkipDispatch || !created.UseIssueWorkflow || created.AssistantPrompt != "" {
		t.Fatalf("unexpected dispatch job: %+v", created)
	}
	if got := strings.Join(created.IssueStatuses, ","); got != "backlog,todo" {
		t.Fatalf("default issue statuses = %q", got)
	}
	if created.Source != "" {
		t.Fatalf("default source = %q", created.Source)
	}
	gongfengOnly := requestJSON[domain.ScheduleJob](t, h, http.MethodPost, "/api/schedule-jobs", map[string]any{
		"name": "只跑工蜂", "projectId": "platform", "workflowId": "scheduled-wf",
		"source": "gongfeng", "intervalSec": 3600, "enabled": true,
	}, http.StatusOK)
	if gongfengOnly.Source != "gongfeng" {
		t.Fatalf("gongfeng source = %q", gongfengOnly.Source)
	}
	requestJSON[map[string]string](t, h, http.MethodPost, "/api/schedule-jobs", map[string]any{
		"name": "坏来源", "projectId": "platform", "workflowId": "scheduled-wf", "source": "local",
	}, http.StatusBadRequest)
	custom := requestJSON[domain.ScheduleJob](t, h, http.MethodPost, "/api/schedule-jobs", map[string]any{
		"name": "只扫进行中", "projectId": "platform", "workflowId": "scheduled-wf",
		"issueStatuses": []string{"in_progress", "done", "todo"}, "intervalSec": 300, "enabled": true,
	}, http.StatusOK)
	if got := strings.Join(custom.IssueStatuses, ","); got != "todo,in_progress" {
		t.Fatalf("normalized issue statuses = %q", got)
	}
	promptOnly := requestJSON[domain.ScheduleJob](t, h, http.MethodPost, "/api/schedule-jobs", map[string]any{
		"name": "工作日巡检", "projectId": "platform", "skipDispatch": true,
		"cron": "0 9 * * 1-5", "assistantPrompt": "列出未完成事项", "enabled": true,
		"issueStatuses": []string{"todo"},
	}, http.StatusOK)
	if promptOnly.ID == "" || promptOnly.Cron != "0 9 * * 1-5" || promptOnly.IntervalSec != 0 || !promptOnly.SkipDispatch {
		t.Fatalf("unexpected prompt-only job: %+v", promptOnly)
	}
	if len(promptOnly.IssueStatuses) != 0 {
		t.Fatalf("AI job must not keep issue statuses: %+v", promptOnly.IssueStatuses)
	}
	jobs := requestJSON[[]domain.ScheduleJob](t, h, http.MethodGet, "/api/schedule-jobs", nil, http.StatusOK)
	if len(jobs) != 4 {
		t.Fatalf("unexpected schedule jobs: %+v", jobs)
	}
	requestJSON[map[string]bool](t, h, http.MethodDelete, "/api/schedule-jobs/"+created.ID, nil, http.StatusOK)
	if _, ok := st.ScheduleJob(created.ID); ok {
		t.Fatal("schedule job was not deleted")
	}
}

func TestScheduledDispatchUsesIntegrationBranchAndManualDispatchDoesNot(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	project := domain.Project{ID: "platform", Name: "Platform", DefaultRepositoryID: "repo"}
	if err := st.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "repo", ProjectID: project.ID, Name: "Repo", Provider: "gongfeng", DefaultBranch: "master", IntegrateBranch: "agentflow/integration"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "scheduled-wf", Name: "Scheduled", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval"}}}
	if err := st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	s := &Server{deps: &deps{repo: st, engine: e, sync: tracker.NewService()}}

	putIssue := func(id string) {
		if err := st.PutIssue(domain.Issue{ID: id, ProjectID: project.ID, Title: id, Source: "gongfeng", Kind: "issue", WorkflowID: wf.ID}); err != nil {
			t.Fatal(err)
		}
	}
	putIssue("scheduled")
	scheduled, err := dispatchIssue(s.deps, dispatchRequest{IssueID: "scheduled", WorkflowID: wf.ID, ScheduleJobID: "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cancel(scheduled.RunID) })
	run, _ := st.Run(scheduled.RunID)
	if scheduled.DispatchMode != "scheduled" || scheduled.TriggerOrigin != "timer" {
		t.Fatalf("scheduled trigger: %+v", scheduled)
	}
	if run.ExecutionContext.ScheduleJobID != "job-1" || run.Repository.BaseBranch != "agentflow/integration" || run.Repository.IntegrateBranch != "agentflow/integration" {
		t.Fatalf("scheduled repository context: %+v", run.Repository)
	}
	if !strings.Contains(run.Input, "configured main branch: master") || !strings.Contains(run.Input, "merges the configured main branch into the integration baseline") || strings.Contains(run.Input, "promotes the integration branch") {
		t.Fatalf("scheduled input does not describe baseline-only integration: %q", run.Input)
	}

	putIssue("manual")
	manual, err := dispatchIssue(s.deps, dispatchRequest{IssueID: "manual", WorkflowID: wf.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cancel(manual.RunID) })
	run, _ = st.Run(manual.RunID)
	if manual.DispatchMode != "primary" || manual.TriggerOrigin != "manual" {
		t.Fatalf("manual trigger: %+v", manual)
	}
	if run.Repository.BaseBranch != "master" || run.Repository.IntegrateBranch != "" {
		t.Fatalf("manual dispatch was changed: %+v", run.Repository)
	}

	putIssue("schedule-run-now")
	runNow, err := dispatchIssue(s.deps, dispatchRequest{IssueID: "schedule-run-now", WorkflowID: wf.ID, ScheduleJobID: "job-1", TriggerOrigin: "manual_run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cancel(runNow.RunID) })
	if runNow.DispatchMode != "scheduled" || runNow.TriggerOrigin != "manual_run" {
		t.Fatalf("run-now trigger: %+v", runNow)
	}
}

func TestSingleSOPDispatchReplacesStaleBinding(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	project := domain.Project{ID: "platform", Name: "Platform"}
	if err := st.PutProject(project); err != nil {
		t.Fatal(err)
	}
	bound := domain.WorkflowDefinition{ID: "issue-fix-production", Name: "生产闭环", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Type: "approval"}}}
	started := domain.WorkflowDefinition{ID: "sop-sandbox", Name: "沙箱", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Type: "approval"}}}
	for _, wf := range []domain.WorkflowDefinition{bound, started} {
		if err := st.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	e := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	s := &Server{deps: &deps{repo: st, engine: e, sync: tracker.NewService()}}
	if err := st.PutIssue(domain.Issue{ID: "one", ProjectID: project.ID, Title: "one", Source: "gongfeng", Kind: "issue", WorkflowID: bound.ID}); err != nil {
		t.Fatal(err)
	}
	task, err := dispatchIssue(s.deps, dispatchRequest{IssueID: "one", WorkflowID: started.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
	got, ok := st.Issue("one")
	if !ok || got.WorkflowID != started.ID {
		t.Fatalf("single SOP binding = %q, want %s", got.WorkflowID, started.ID)
	}
	multi := domain.Issue{ID: "multi", ProjectID: project.ID, Title: "multi", Source: "local", Kind: "requirement", WorkflowID: bound.ID, SOPBindings: []domain.IssueSOPBinding{
		{WorkflowID: bound.ID, Order: 1},
		{WorkflowID: started.ID, Order: 2},
	}}
	if err := st.PutIssue(multi); err != nil {
		t.Fatal(err)
	}
	second, err := dispatchIssue(s.deps, dispatchRequest{IssueID: "multi", WorkflowID: started.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cancel(second.RunID) })
	got, ok = st.Issue("multi")
	if !ok || got.WorkflowID != bound.ID || got.SOPBindings[0].WorkflowID != bound.ID {
		t.Fatalf("multi SOP bindings were rewritten: %+v", got)
	}
	if err := st.PutIssue(domain.Issue{ID: "kept", ProjectID: project.ID, Title: "kept", Source: "gongfeng", Kind: "issue", WorkflowID: bound.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(s.deps, dispatchRequest{IssueID: "kept", WorkflowID: "deleted-sop"}); err == nil {
		t.Fatal("starting a missing SOP should fail")
	}
	got, ok = st.Issue("kept")
	if !ok || got.WorkflowID != bound.ID {
		t.Fatalf("failed start rewrote the binding: %+v", got)
	}
}

func TestScheduledJobWaitsWhenIntegrateConflictsWithDefault(t *testing.T) {
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", source}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-b", "master")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "same.txt"), []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "shared")
	git("branch", "agentflow/integration")
	if err := os.WriteFile(filepath.Join(source, "same.txt"), []byte("master\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "master side")
	git("checkout", "agentflow/integration")
	if err := os.WriteFile(filepath.Join(source, "same.txt"), []byte("integrate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "integrate side")
	if out, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create bare remote: %v: %s", err, out)
	}

	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	project := domain.Project{ID: "platform", Name: "Platform", DefaultRepositoryID: "repo"}
	if err := st.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "repo", ProjectID: project.ID, Name: "Repo", Provider: "gongfeng", DefaultBranch: "master", IntegrateBranch: "agentflow/integration", CloneURL: remote}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "scheduled-wf", Name: "Scheduled", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval"}}}
	if err := st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "conflicted", ProjectID: project.ID, Title: "conflicted", Source: "gongfeng", Kind: "issue", WorkflowID: wf.ID, Status: "todo"}
	if err := st.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	e := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	e.ConfigureExecution("test", "test", time.Second, false)
	s := &Server{deps: &deps{repo: st, engine: e, sync: tracker.NewService()}}
	s.schedule = &scheduleHandlers{deps: s.deps}
	s.deps.schedule = schedule.New(st, s.deps.sync, s.schedule.dispatchScheduled)
	s.deps.schedule.SetBaselineGuard(s.schedule.guardScheduledBaseline)
	if err := st.PutScheduleJob(domain.ScheduleJob{ID: "job-1", Name: "Gongfeng", ProjectID: project.ID, Source: "gongfeng", WorkflowID: wf.ID, Enabled: true, IntervalSec: 60}); err != nil {
		t.Fatal(err)
	}
	job, err := s.deps.schedule.RunNow("job-1")
	if err != nil {
		t.Fatalf("conflict must wait, not fail the job: %v", err)
	}
	if job.LastStatus != "waiting" || !strings.Contains(job.LastError, "存在冲突") || job.LastDispatched != 0 {
		t.Fatalf("expected waiting baseline gate, got %+v", job)
	}
	if st.IssueHasTask(issue.ID) {
		t.Fatal("conflict must not dispatch the issue SOP")
	}
	var alignRun domain.Run
	for _, run := range st.Runs() {
		if run.WorkflowID == engine.BaselineAlignWorkflowID {
			alignRun = run
			break
		}
	}
	if alignRun.ID == "" {
		t.Fatal("conflict did not start a baseline alignment run")
	}
	t.Cleanup(func() { _ = e.Cancel(alignRun.ID) })
	if alignRun.ExecutionContext.ResponsibleUserID != 1 {
		t.Fatalf("baseline alignment responsible user = %d, want issue owner 1", alignRun.ExecutionContext.ResponsibleUserID)
	}
	stored, ok := st.ScheduleJob("job-1")
	if !ok {
		t.Fatal("schedule job missing after run")
	}
	if len(stored.Blockers) != 1 {
		t.Fatalf("expected 1 structured blocker, got %+v", stored.Blockers)
	}
	b := stored.Blockers[0]
	if b.RepositoryID != "repo" || b.RepositoryName != "Repo" || b.AlignRunID != alignRun.ID {
		t.Fatalf("unexpected blocker: %+v (alignRun=%s)", b, alignRun.ID)
	}
	if b.IntegrateBranch != "agentflow/integration" || b.DefaultBranch != "master" {
		t.Fatalf("unexpected blocker branches: %+v", b)
	}
	if len(b.ConflictFiles) == 0 {
		t.Fatalf("blocker missing conflict files: %+v", b)
	}
	if len(b.ConflictBlocks) == 0 {
		t.Fatalf("blocker missing conflict blocks: %+v", b)
	}
	if b.ConflictBlocks[0].File == "" || b.ConflictBlocks[0].Ours == "" || b.ConflictBlocks[0].Theirs == "" {
		t.Fatalf("conflict block missing sides: %+v", b.ConflictBlocks[0])
	}
	if b.Since.IsZero() {
		t.Fatalf("blocker since not set: %+v", b)
	}
}

func TestEnsureBaselineAlignWorkflowUsesPlatformAgentNotProjectFixer(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutAgent(domain.AgentDefinition{ID: "issue-fix-engineer", Name: "修复并提交", ProjectID: "skillhub", ExecutorID: "codebuddy"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutWorkflow(domain.WorkflowDefinition{
		ID: "issue-fix-production", Name: "Issue 修复", ProjectID: "skillhub", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "fix", Type: "agent", AgentID: "issue-fix-engineer", Name: "修复并提交"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutWorkflow(domain.WorkflowDefinition{
		ID: engine.BaselineAlignWorkflowID, Name: "对齐默认分支到基线", ProjectID: "skillhub",
		Version: 3, Status: "published", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "resolve", Type: "agent", AgentID: "issue-fix-engineer"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := &scheduleHandlers{deps: &deps{repo: st}}
	if !h.ensureBaselineAlignWorkflow(domain.ScheduleJob{ProjectID: "skillhub", WorkflowID: "issue-fix-production"}) {
		t.Fatal("ensure should rewrite project fixer onto the platform align role")
	}
	wf, ok := st.Workflow(engine.BaselineAlignWorkflowID)
	if !ok || wf.Version < baselineAlignWorkflowVersion || wf.SandboxProfileID != "local" {
		t.Fatalf("upgraded workflow = %+v", wf)
	}
	if !alignWorkflowUsesPlatformAgent(wf) {
		t.Fatalf("resolve agent = %+v, want %s", wf.Nodes, engine.BaselineAlignAgentID)
	}
	agent, ok := st.Agent(engine.BaselineAlignAgentID)
	if !ok || !agent.Shared || len(agent.Verification) != 0 || len(agent.Hooks) != 0 {
		t.Fatalf("platform align agent = %+v", agent)
	}
}

func TestEnsureBaselineAlignWorkflowUpgradesExistingRevision(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutWorkflow(domain.WorkflowDefinition{
		ID: engine.BaselineAlignWorkflowID, Name: "对齐默认分支到基线", ProjectID: "platform",
		Version: 1, Status: "published", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "resolve", Name: "解决冲突", Type: "agent", AgentID: "developer"},
			{ID: "confirm", Name: "确认对齐", Type: "approval"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	old, ok := st.Workflow(engine.BaselineAlignWorkflowID)
	if !ok || old.Revision < 1 {
		t.Fatalf("seed revision = %+v", old)
	}
	h := &scheduleHandlers{deps: &deps{repo: st}}
	if !h.ensureBaselineAlignWorkflow(domain.ScheduleJob{ProjectID: "platform"}) {
		t.Fatal("ensure should upgrade an existing v1 align SOP")
	}
	wf, ok := st.Workflow(engine.BaselineAlignWorkflowID)
	if !ok || wf.Version < baselineAlignWorkflowVersion {
		t.Fatalf("upgraded version = %d", wf.Version)
	}
	if len(wf.Nodes) != 2 || wf.Nodes[0].ID != "resolve" || wf.Nodes[1].ID != "verify" {
		t.Fatalf("upgraded nodes = %+v", wf.Nodes)
	}
	if !alignWorkflowUsesPlatformAgent(wf) {
		t.Fatalf("resolve agent = %+v, want %s", wf.Nodes, engine.BaselineAlignAgentID)
	}
	for _, n := range wf.Nodes {
		if n.ID == "confirm" {
			t.Fatal("confirm node must be removed")
		}
	}
}

func TestScheduleJobRunAcceptedBeforeFinish(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutScheduleJob(domain.ScheduleJob{
		ID: "job-slow", Name: "slow", ProjectID: "platform", SkipDispatch: true,
		AssistantPrompt: "巡检", Enabled: true, IntervalSec: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	svc := schedule.New(st, tracker.NewService(), nil)
	svc.SetAssistantPrep(func(domain.ScheduleJob) (string, error) {
		close(started)
		<-release
		return "ok", nil
	})
	h := &scheduleHandlers{deps: &deps{repo: st, schedule: svc}}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/schedule-jobs", h.scheduleJobs)
	r.POST("/api/schedule-jobs/:id/run", h.scheduleJobRun)

	got := requestJSON[domain.ScheduleJob](t, r, http.MethodPost, "/api/schedule-jobs/job-slow/run", map[string]any{}, http.StatusAccepted)
	if got.LastStatus != "running" {
		t.Fatalf("accepted job: %+v", got)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("assistant did not start")
	}
	listed := requestJSON[[]domain.ScheduleJob](t, r, http.MethodGet, "/api/schedule-jobs", nil, http.StatusOK)
	if len(listed) != 1 || listed[0].LastStatus != "running" {
		t.Fatalf("poll while running: %+v", listed)
	}
	requestJSON[map[string]string](t, r, http.MethodPost, "/api/schedule-jobs/job-slow/run", map[string]any{}, http.StatusConflict)
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		listed = requestJSON[[]domain.ScheduleJob](t, r, http.MethodGet, "/api/schedule-jobs", nil, http.StatusOK)
		if len(listed) == 1 && listed[0].LastStatus == "success" && listed[0].LastAssistantMessage == "ok" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("background run did not finish: %+v", listed)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestScheduleUIDemoServer(t *testing.T) {
	if os.Getenv("AGENTFLOW_SCHEDULE_UI_DEMO") != "1" {
		t.Skip("set AGENTFLOW_SCHEDULE_UI_DEMO=1 to serve local Studio with fake DP backlog issues")
	}
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "false")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "true")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	now := time.Now().UTC()
	if err := st.PutProject(domain.Project{
		ID: "dp", Key: "DP", Name: "DNSPod", Description: "本地预分诊验收", Color: "#3867f4",
		WorkflowID: "dnspod-feature-dev", OwnerID: 1,
		Members:   []domain.ProjectMember{{UserID: 1, Role: "maintainer"}},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, wf := range []domain.WorkflowDefinition{
		{ID: "dnspod-issue-fix", Name: "Issue 修复", ProjectID: "dp", Status: "published", SandboxProfileID: "local", Version: 1, Nodes: []domain.WorkflowNode{{ID: "work", Name: "处理", Type: "approval"}}},
		{ID: "dnspod-feature-dev", Name: "功能开发", ProjectID: "dp", Status: "published", SandboxProfileID: "local", Version: 1, Nodes: []domain.WorkflowNode{{ID: "work", Name: "处理", Type: "approval"}}},
	} {
		if err := st.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutRepository(domain.Repository{
		ID: "repo-dnspod", ProjectID: "dp", Name: "dnspod", Provider: "gongfeng",
		GongfengProjectID: "dnspod/dnspod", DefaultBranch: "master",
	}); err != nil {
		t.Fatal(err)
	}
	issues := []domain.Issue{
		{ID: "iss-fix-unbound", ProjectID: "dp", Title: "dnspod 解析失败：NXDOMAIN 误报", Status: "backlog", Kind: "issue", Source: "gongfeng", WorkflowID: "dnspod-issue-fix", ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalID: "1001", ExternalIID: 1}},
		{ID: "iss-fix-bound", ProjectID: "dp", Title: "dnspod 缓存刷新超时", Status: "backlog", Kind: "issue", Source: "gongfeng", WorkflowID: "dnspod-issue-fix", RepositoryID: "repo-dnspod", RepositoryIDs: []string{"repo-dnspod"}, ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalID: "1002", ExternalIID: 2}},
		{ID: "tapd-feature", ProjectID: "dp", Title: "支持批量添加解析记录", Status: "backlog", Kind: "requirement", Source: "tapd", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1024", ExternalType: "story"}},
		{ID: "tapd-low", ProjectID: "dp", Title: "下周同步一下文档", Status: "backlog", Kind: "requirement", Source: "tapd", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "2048", ExternalType: "story"}},
		{ID: "iss-todo", ProjectID: "dp", Title: "已在 todo 不应被 backlog 扫描", Status: "todo", Kind: "issue", Source: "gongfeng", WorkflowID: "dnspod-issue-fix", ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalID: "1003", ExternalIID: 3}},
	}
	for _, issue := range issues {
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	prompt := `你是项目 DP（projectId: dp）的需求预分诊助手。调用 issues_list，query={"status":"backlog","limit":50}，按 offset 翻页直到 hasMore=false。没有 backlog 则输出“无待处理需求”。对 source=gongfeng / kind=issue：确认 workflowId=dnspod-issue-fix，仓库缺失时结合标题与 repositories_list 判断，仅高把握用 issues_save。不要 issues_dispatch。对 source=tapd：无 TAPD MCP 时跳过处理人同步，仓库与 SOP 置信度不足则跳过。写操作只允许 issues_save，每轮最多 12 个。输出 Markdown 表格：需求编号 | 标题 | 来源 | 处理动作 | 置信度 | 说明。`
	if err := st.PutScheduleJob(domain.ScheduleJob{
		ID: "job-triage", Name: "DP backlog 需求预分诊", ProjectID: "dp", SkipDispatch: true,
		AssistantPrompt: prompt, AssistantModel: "default", Enabled: true, IntervalSec: 3600,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	if err := st.PutScheduleJob(domain.ScheduleJob{
		ID: "job-running-demo", Name: "模拟运行中（关页也不会停）", ProjectID: "dp", SkipDispatch: true,
		AssistantPrompt: "占位", AssistantModel: "default", Enabled: true, IntervalSec: 3600,
		LastRunAt: now, LastStatus: "running", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:18092")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	log.Printf("local Studio http://127.0.0.1:18092  project=dp  定时任务里可点「立即运行」")
	if err := http.Serve(ln, h); err != nil && !strings.Contains(err.Error(), "closed") {
		t.Fatal(err)
	}
}
