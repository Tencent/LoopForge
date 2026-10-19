package engine

import (
	"agentflow/internal/domain"
	"context"
	"fmt"
	"strings"
	"time"
)

func (e *Engine) Start(workflowID, input string) (domain.Run, error) {
	return e.StartWithContext(workflowID, input, domain.RepositoryContext{})
}

func (e *Engine) StartWithContext(workflowID, input string, repository domain.RepositoryContext) (domain.Run, error) {
	return e.StartWithRepositoriesContext(workflowID, input, []domain.RepositoryContext{repository}, domain.TelemetryContext{})
}

func (e *Engine) StartWithExecutionContext(workflowID, input string, repository domain.RepositoryContext, executionContext domain.TelemetryContext) (domain.Run, error) {
	return e.StartWithRepositoriesContext(workflowID, input, []domain.RepositoryContext{repository}, executionContext)
}

// StartWithRepositoriesContext starts a run with one or more repositories.
// All repositories are equals: each is checked out into a first-level
// sub-directory, gets the same work branch and, on scheduled runs, its own
// integration merge and MR. SOP deliverables stay at the run workspace root.
func (e *Engine) StartWithRepositoriesContext(workflowID, input string, repositories []domain.RepositoryContext, executionContext domain.TelemetryContext) (domain.Run, error) {
	return e.startWorkflow(workflowID, input, repositories, executionContext, false)
}

func (e *Engine) startWorkflow(workflowID, input string, repositories []domain.RepositoryContext, executionContext domain.TelemetryContext, group bool) (domain.Run, error) {
	repositories = normalizeRepositories(repositories)
	domain.AssignRepositoryDirs(repositories)
	e.mu.Lock()
	draining, executeRuns := e.draining, e.executeRuns
	instanceID, ttl := e.instanceID, e.leaseTTL
	e.mu.Unlock()
	if draining {
		return domain.Run{}, ErrExecutionDisabled
	}
	stored, ok := e.repo.Workflow(workflowID)
	if !ok {
		return domain.Run{}, fmt.Errorf("workflow not found")
	}
	wf := stored
	wf.CollaborationMode = ""
	wf.TaskTitle = ""
	wf.GroupMembers = nil
	wf.SOPCheckpoint = nil
	wf.GroupSOPHandoff = nil
	wf.GroupHistory = nil
	if wf.Generic {
		if strings.TrimSpace(executionContext.ProjectID) == "" {
			return domain.Run{}, fmt.Errorf("直接运行通用模板需要先选择项目")
		}
		if _, found := e.repo.Project(executionContext.ProjectID); !found {
			return domain.Run{}, fmt.Errorf("项目 %q 不存在，请刷新后重试", executionContext.ProjectID)
		}
	}
	wf = defaultWorkflowSandboxProfile(wf)
	wf, err := domain.AttachPreviewBuild(wf)
	if err != nil {
		return domain.Run{}, err
	}
	if err := validate(wf); err != nil {
		return domain.Run{}, err
	}
	profile, ok := e.repo.Sandbox(wf.SandboxProfileID)
	if !ok {
		return domain.Run{}, fmt.Errorf("sandbox profile %q not found", wf.SandboxProfileID)
	}
	resources, err := resolveSandboxResources(e.repo, profile)
	if err != nil {
		return domain.Run{}, err
	}
	if _, err := workflowSandboxRequirements(e.repo, wf); err != nil {
		return domain.Run{}, err
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("run-%d", now.UnixNano())
	workBranch := ""
	if len(repositories) > 0 {
		if workBranch = strings.TrimSpace(repositories[0].WorkBranch); workBranch == "" {
			issueID := strings.TrimSpace(executionContext.IssueID)
			if issueID != "" {
				if issue, ok := e.repo.Issue(issueID); ok {
					workBranch = domain.DefaultWorkBranch(issue, now)
				}
			}
			if workBranch == "" {
				workBranch = defaultWorkBranch(executionContext.IssueNumber, issueID, now)
			}
		}
	}
	for i := range repositories {
		if strings.TrimSpace(repositories[i].WorkBranch) == "" {
			repositories[i].WorkBranch = workBranch
		}
	}
	var firstRepository domain.RepositoryContext
	if len(repositories) > 0 {
		firstRepository = repositories[0]
	}
	executionContext.RunID, executionContext.TraceID, executionContext.WorkflowID, executionContext.WorkflowVersion = id, id, workflowID, wf.Version
	if executionContext.ParentRunID == "" {
		executionContext.ExecutionRunnerID = e.repo.DefaultRunner(executionContext.ResponsibleUserID)
	}
	if executionContext.ExecutionRunnerID == "" {
		if runner, ok := e.cloudPoolRunner(executionContext.ProjectID); ok {
			executionContext.ExecutionRunnerID = runner.ID
			executionContext.ExecutionPool = domain.ExecutionModeCloud
		} else if e.cloudPoolRequired(executionContext.ProjectID) {
			// The first node binds a pool runner once one is online.
			executionContext.ExecutionPool = domain.ExecutionModeCloud
		}
	}
	if executionContext.ExecutionRunnerID != "" || executionContext.ExecutionPool == domain.ExecutionModeCloud {
		executionContext.ExecutionMode = domain.ExecutionModeRunner
		executionContext.RunnerWorkspaceLayout = domain.RunnerWorkspaceLayoutShared
	} else {
		executionContext.ExecutionMode = domain.ExecutionModeCloud
	}
	if err := e.requireExistingWorkBranchRunner(executionContext.ExecutionRunnerID, repositories); err != nil {
		return domain.Run{}, err
	}
	wf.SandboxSnapshot = resolvedSandboxSnapshot(profile, resources)
	r := domain.Run{
		ID: id, WorkflowID: workflowID, WorkflowSnapshot: wf, Status: "running", Input: input,
		Repository: firstRepository, ExecutionContext: executionContext, Nodes: map[string]*domain.NodeRun{},
		CreatedAt: now, UpdatedAt: now,
	}
	if len(repositories) > 0 {
		r.Repositories = append([]domain.RepositoryContext(nil), repositories...)
	}
	for _, n := range wf.Nodes {
		r.Nodes[n.ID] = &domain.NodeRun{NodeID: n.ID, Status: "pending"}
	}
	if executionContext.ParentRunID != "" {
		parent, exists := e.repo.RunState(executionContext.ParentRunID)
		if !exists {
			return domain.Run{}, fmt.Errorf("父 SOP 运行不存在")
		}
		if err := inheritSOPHandoff(parent, &r); err != nil {
			return domain.Run{}, err
		}
	}
	if group {
		freezeSOPCheckpoint(&r)
		r.WorkflowSnapshot.CollaborationMode = "group"
		r.WorkflowSnapshot.TaskTitle = truncateGroupText(input, 100)
		r.WorkflowSnapshot.Edges = nil
		r.WorkflowSnapshot.Preview = nil
		r.WorkflowSnapshot.Notifications = nil
		for _, n := range r.Nodes {
			n.Status = "skipped"
		}
		r.Status = "waiting"
	}
	e.event(&r, "", "run.started", "流水线开始执行")
	if executeRuns && !group {
		if strings.TrimSpace(instanceID) == "" || ttl <= 0 {
			return domain.Run{}, ErrExecutionDisabled
		}
		r.OwnerInstanceID, r.Generation, r.LeaseUntil = instanceID, 1, now.Add(ttl)
	}
	// Control plane persists an unowned running row. The worker claims it
	// through ResumeInterruptedRuns. Combined instances still insert already
	// leased so another process cannot start a second execute() on the same run.
	if err := e.repo.PutRun(r); err != nil {
		return domain.Run{}, err
	}
	if current, ok := e.repo.RunState(r.ID); ok {
		r = current
	}
	e.publish(r.ID)
	if !executeRuns || r.Status == "queued" || group {
		return r, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	if !e.claimRunning(id, cancel) {
		cancel()
		return r, nil
	}
	go e.execute(ctx, id)
	return r, nil
}

func (e *Engine) executes() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.executeRuns && !e.draining
}
