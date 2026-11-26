package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// assignWorkBranches gives every repository without a branch the first
// repository's work branch, else a generated one. A branch the caller
// explicitly continues or creates belongs to its own repository, and a
// read-only checkout has no work branch at all.
func assignWorkBranches(repositories []domain.RepositoryContext, generate func() string) {
	if len(repositories) == 0 {
		return
	}
	workBranch := ""
	if first := repositories[0]; !first.RequireExistingWorkBranch && !first.CreateNewWorkBranch && !first.ReadOnly {
		workBranch = strings.TrimSpace(first.WorkBranch)
	}
	for i := range repositories {
		if repositories[i].ReadOnly {
			repositories[i].WorkBranch = ""
			continue
		}
		if strings.TrimSpace(repositories[i].WorkBranch) == "" {
			if workBranch == "" {
				workBranch = generate()
			}
			repositories[i].WorkBranch = workBranch
		}
	}
}

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
// Each repository is checked out into a first-level
// sub-directory, gets its work branch and, on scheduled runs, its own
// integration merge and MR. LOOP deliverables stay at the run workspace root.
func (e *Engine) StartWithRepositoriesContext(workflowID, input string, repositories []domain.RepositoryContext, executionContext domain.TelemetryContext) (domain.Run, error) {
	return e.StartWithRepositories(context.Background(), workflowID, input, repositories, executionContext)
}

// StartWithRepositories allows cancellation while resolving an automatic
// sandbox. Once admitted, the run continues independently of the caller.
func (e *Engine) StartWithRepositories(ctx context.Context, workflowID, input string, repositories []domain.RepositoryContext, executionContext domain.TelemetryContext) (domain.Run, error) {
	wf, ok := e.repo.Workflow(workflowID)
	if !ok {
		return domain.Run{}, fmt.Errorf("workflow not found")
	}
	if executionContext.SharedScheduleID != "" {
		version, err := e.repo.SharedScheduleVersion(executionContext.SharedScheduleID, executionContext.SharedScheduleVersion)
		if err != nil {
			return domain.Run{}, err
		}
		if err := e.repo.ValidateSharedScheduleAssets(version); err != nil {
			return domain.Run{}, err
		}
		allowed := version.HasAsset("sop", workflowID)
		if !allowed && executionContext.ParentRunID != "" {
			if parent, ok := e.repo.RunState(executionContext.ParentRunID); ok && parent.ExecutionContext.SharedScheduleID == executionContext.SharedScheduleID && parent.ExecutionContext.SharedScheduleVersion == executionContext.SharedScheduleVersion &&
				parent.ExecutionContext.ProjectID == executionContext.ProjectID && parent.ExecutionContext.ResponsibleUserID == executionContext.ResponsibleUserID {
				for _, node := range parent.WorkflowSnapshot.Nodes {
					if node.Type == "sop" && node.SOPWorkflowID == workflowID && node.ID == executionContext.ParentNodeID {
						allowed = true
						break
					}
				}
			}
		}
		if !allowed {
			return domain.Run{}, fmt.Errorf("LOOP 不在共享任务的引用范围内")
		}
	}
	return e.startWorkflowDefinition(ctx, wf, input, repositories, executionContext, false)
}

// startWorkflowDefinition also accepts a run-only conversation workspace definition.
// It never writes a template to the catalog.
func (e *Engine) startWorkflowDefinition(ctx context.Context, wf domain.WorkflowDefinition, input string, repositories []domain.RepositoryContext, executionContext domain.TelemetryContext, workspace bool) (domain.Run, error) {
	return e.startWorkflowDefinitionSeeded(ctx, wf, input, repositories, executionContext, workspace, nil)
}

// startWorkflowDefinitionSeeded lets seed adjust the new run (node states,
// artifacts) after every node is initialized and before it is persisted.
func (e *Engine) startWorkflowDefinitionSeeded(ctx context.Context, wf domain.WorkflowDefinition, input string, repositories []domain.RepositoryContext, executionContext domain.TelemetryContext, workspace bool, seed func(*domain.Run)) (domain.Run, error) {
	if executionContext.IsEvolutionReplay() {
		if err := e.validateEvolutionReplayDefinition(wf, repositories); err != nil {
			return domain.Run{}, err
		}
	}
	workflowID := wf.ID
	repositories = normalizeRepositories(repositories)
	if executionContext.ParentRunID == "" || !executionContext.ExecutionPinned || executionContext.WorkspaceRunID == "" {
		domain.AssignRepositoryDirs(repositories)
	}
	e.mu.Lock()
	draining, executeRuns := e.draining, e.executeRuns
	instanceID, ttl := e.instanceID, e.leaseTTL
	e.mu.Unlock()
	if draining {
		return domain.Run{}, ErrExecutionDisabled
	}
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
	if wf.AutoSelectSandbox && !wf.Shared && !wf.Generic && wf.ProjectID != "" {
		if executionContext.ProjectID == "" {
			executionContext.ProjectID = wf.ProjectID
		}
		if executionContext.ProjectID != wf.ProjectID {
			return domain.Run{}, apperr.New(apperr.Forbidden, "只能在当前项目运行其私有 LOOP")
		}
	}
	wf = defaultWorkflowSandboxProfile(wf)
	wf, err := configureBaselineAlignCLI(wf)
	if err != nil {
		return domain.Run{}, err
	}
	wf, err = domain.AttachPreviewBuild(wf)
	if err != nil {
		return domain.Run{}, err
	}
	if err := validate(wf); err != nil {
		return domain.Run{}, err
	}
	var workspaceSandboxID string
	var parent domain.Run
	if executionContext.ParentRunID != "" {
		var ok bool
		parent, ok = e.repo.RunState(executionContext.ParentRunID)
		if !ok {
			return domain.Run{}, fmt.Errorf("父 LOOP 运行不存在")
		}
		if wf.AutoSelectSandbox {
			workspaceSandboxID = parent.WorkflowSnapshot.SandboxProfileID
		}
		if target := parent.ExecutionContext; target.ExecutionPool == domain.ExecutionPoolProjectAuto {
			executionContext.ExecutionPool = target.ExecutionPool
			executionContext.ExecutionMode = target.ExecutionMode
			executionContext.ExecutionRunnerID = target.ExecutionRunnerID
			executionContext.RunnerWorkspaceLayout = target.RunnerWorkspaceLayout
		}
	}
	if !workspace && executionContext.ParentRunID == "" && executionContext.WorkspaceRunID != "" {
		owner, err := e.sopConversationWorkspace(executionContext)
		if err != nil {
			return domain.Run{}, err
		}
		if domain.TerminalRunStatus(owner.Status) || owner.CancelRequested {
			return domain.Run{}, fmt.Errorf("对话工作区已结束")
		}
		workspaceSandboxID = owner.WorkflowSnapshot.SandboxProfileID
		executionContext.ExecutionRunnerID = e.defaultExecutionRunner(owner)
		if executionContext.ExecutionRunnerID != "" {
			runner, ok := e.repo.Runner(executionContext.ExecutionRunnerID)
			if !ok || !runner.SupportsWorkspaceOwnerPreparation() || owner.ExecutionContext.RunnerWorkspaceLayout != domain.RunnerWorkspaceLayoutShared {
				return domain.Run{}, fmt.Errorf("原执行机器或工作区不支持共享检出目录，请更新执行机器并重新打开工作区")
			}
			if runner.OwnerUserID > 0 && !e.repo.CanUseRunner(runner, executionContext.ProjectID, executionContext.ResponsibleUserID) {
				return domain.Run{}, apperr.New(apperr.Forbidden, repo.ErrRunnerProjectDenied.Error())
			}
			if len(repositories) > 0 && !runner.SupportsConversationSOPWorkspace() {
				return domain.Run{}, fmt.Errorf("复用对话中的仓库检出需要执行机器升级至 %s", domain.RunnerVersionConversationSOPWorkspace)
			}
		}
		executionContext.ExecutionPinned = true
		executionContext.ExecutionPool = ""
	}
	autoAssign := e.selectSOPExecutionTarget(&executionContext, !workspace)
	if len(wf.ProjectCredentialIDs) > 0 {
		if _, err := e.ResolveRunProjectCredentialEnv(context.Background(), domain.Run{WorkflowSnapshot: wf, ExecutionContext: executionContext}); err != nil {
			return domain.Run{}, err
		}
	}
	var profile domain.SandboxProfile
	var sandboxSelection string
	var candidates []autoAssignCandidate
	switch {
	case workspace:
		profile, err = e.repo.ConversationSandbox(executionContext.ProjectID, wf.SandboxProfileID)
	case autoAssign:
		candidates, profile, sandboxSelection, err = e.prepareAutoAssign(ctx, wf, executionContext, repositories, input)
	case wf.AutoSelectSandbox:
		profile, sandboxSelection, err = e.selectSOPSandbox(ctx, wf, &executionContext, repositories, input, workspaceSandboxID)
	default:
		var ok bool
		if profile, ok = e.repo.Sandbox(wf.SandboxProfileID); !ok {
			err = fmt.Errorf("sandbox profile %q not found", wf.SandboxProfileID)
		}
	}
	if err != nil {
		return domain.Run{}, err
	}
	sandbox, err := resolveSandbox(profile)
	if err != nil {
		return domain.Run{}, err
	}
	if err := e.requireRunnerSandboxVersion(executionContext.ExecutionRunnerID, profile); err != nil {
		return domain.Run{}, err
	}
	if executionContext.SharedScheduleID != "" {
		if err := e.validateSharedScheduleWorkflow(wf, executionContext.ProjectID, profile); err != nil {
			return domain.Run{}, err
		}
	}
	if wf.AutoSelectSandbox {
		wf.SandboxProfileID = profile.ID
	}
	wf.ExecutorDefaults = nil
	if !autoAssign {
		if err := e.requireProjectCredentialRunner(wf, executionContext.ExecutionRunnerID); err != nil {
			return domain.Run{}, err
		}
		if executionContext.ResponsibleUserID > 0 {
			wf.ExecutorDefaults, _, err = e.planSOPExecutors(wf, executionContext, false)
			if err != nil {
				return domain.Run{}, err
			}
		}
		if _, err := workflowSandboxRequirements(e.repo, wf); err != nil {
			return domain.Run{}, err
		}
		if err := e.assignSOPWorkBranches(ctx, executionContext, repositories, input, time.Now().UTC(), nil); err != nil {
			return domain.Run{}, err
		}
		if err := e.requireRunnerRepositoryCapabilities(executionContext.ExecutionRunnerID, repositories); err != nil {
			return domain.Run{}, err
		}
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("run-%d", now.UnixNano())
	var firstRepository domain.RepositoryContext
	if len(repositories) > 0 {
		firstRepository = repositories[0]
	}
	executionContext.RunID, executionContext.TraceID, executionContext.WorkflowID, executionContext.WorkflowVersion = id, id, workflowID, wf.Version
	wf.SandboxSnapshot = sandbox
	var canaryNames []string
	if !workspace {
		executionContext.EvolutionCanaryIDs, canaryNames = e.assignEvolutionCanaries(id, wf, executionContext)
	}
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
	if parent.ID != "" {
		if err := inheritSOPHandoff(parent, &r); err != nil {
			return domain.Run{}, err
		}
	}
	if workspace {
		r.Kind = domain.RunKindWorkspace
		r.Status = "waiting"
	}
	if workspace {
		e.event(&r, "", "workspace.created", "对话工作区已创建")
	} else {
		e.event(&r, "", "run.started", "流水线开始执行")
		if sandboxSelection != "" {
			e.event(&r, "", domain.RunEventSandboxSelected, sandboxSelection)
		}
		if borrowsConversationWorkspace(r) {
			e.event(&r, "", "workspace.shared", "复用对话工作区 "+workspaceRunID(r))
		}
	}
	if len(canaryNames) > 0 {
		e.event(&r, "", "evolution.canary", "本次运行参与进化试用，使用「"+strings.Join(canaryNames, "、")+"」的候选正文")
	}
	if seed != nil {
		seed(&r)
	}
	executeHere := executeRuns && !workspace && (!borrowsConversationWorkspace(r) || !e.workspaceBlocked(r))
	if executeHere {
		if strings.TrimSpace(instanceID) == "" || ttl <= 0 {
			return domain.Run{}, ErrExecutionDisabled
		}
		r.OwnerInstanceID, r.Generation, r.LeaseUntil = instanceID, 1, time.Now().UTC().Add(ttl)
	}
	// Control plane persists an unowned running row. The worker claims it
	// through ResumeInterruptedRuns. Combined instances still insert already
	// leased so another process cannot start a second execute() on the same run.
	if autoAssign {
		err = e.submitAutoAssignRun(r, candidates)
	} else {
		err = e.repo.PutRun(r)
	}
	if err != nil {
		return domain.Run{}, err
	}
	if current, ok := e.repo.RunState(r.ID); ok {
		r = current
	}
	if !workspace {
		e.recordRunAssetPins(r)
	}
	e.publish(r.ID)
	if r.Status == "queued" {
		e.PromoteQueuedRunsAsync()
	}
	if !executeHere || r.Status == "queued" {
		return r, nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	if !e.claimRunning(id, cancel) {
		cancel()
		return r, nil
	}
	go e.execute(runCtx, id)
	return r, nil
}

func (e *Engine) selectSOPExecutionTarget(executionContext *domain.TelemetryContext, allowAuto bool) bool {
	if executionContext.ParentRunID != "" && executionContext.ExecutionPool == domain.ExecutionPoolProjectAuto {
		return false
	}
	if executionContext.ParentRunID == "" && !executionContext.ExecutionPinned {
		defaults := e.repo.ResolveRunnerDefaults(executionContext.ResponsibleUserID, executionContext.ProjectID)
		if allowAuto && executionContext.WorkspaceRunID == "" && executionContext.ProjectID != "" &&
			defaults.ProjectRunnerMode == domain.RunnerSelectionModeAuto {
			executionContext.ExecutionPool = domain.ExecutionPoolProjectAuto
			executionContext.ExecutionMode = ""
			executionContext.ExecutionRunnerID = ""
			executionContext.RunnerWorkspaceLayout = ""
			return true
		}
		executionContext.ExecutionRunnerID = defaults.Runner.ID
		executionContext.WorkspaceRunID = ""
	}
	if executionContext.ExecutionRunnerID == "" && !executionContext.ExecutionPinned {
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
	return false
}

func (e *Engine) executes() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.executeRuns && !e.draining
}
