package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func (e *Engine) workspaceSandboxProfile(run domain.Run) (domain.SandboxProfile, runtime.SandboxResources, error) {
	if spec := run.WorkflowSnapshot.SandboxSnapshot; spec != nil {
		return spec.Profile, sandboxResourcesFromSnapshot(spec), nil
	}
	id := strings.TrimSpace(run.WorkflowSnapshot.SandboxProfileID)
	if id == "" {
		id = "local"
	}
	profile, ok := e.repo.Sandbox(id)
	if !ok {
		return profile, runtime.SandboxResources{}, fmt.Errorf("sandbox profile %q not found", id)
	}
	resources, err := resolveSandboxResources(e.repo, profile)
	return profile, resources, err
}

// acquireConversationEnvironment does not acquire or steal the SOP scheduler
// lease. The workspace-use lease fences cleanup; the sandbox's workspace flock
// fences another process. An active local scheduler shares its existing handle.
func (e *Engine) acquireConversationEnvironment(ctx context.Context, run domain.Run) (*runEnvironment, error) {
	if e.foreignWorkspaceBind(run) || e.defaultExecutionRunner(run) != "" {
		return nil, fmt.Errorf("工作区属于其他执行实例")
	}
	e.runEnvMu.Lock()
	defer e.runEnvMu.Unlock()
	if env := e.runEnvs[run.ID]; env != nil {
		env.setCredentials(e.resolveRunCredentials(run))
		return env, nil
	}
	profile, resources, err := e.workspaceSandboxProfile(run)
	if err != nil {
		return nil, err
	}
	requirements, err := workspaceConversationRequirements(e.repo, run.WorkflowSnapshot)
	if err != nil {
		return nil, err
	}
	credentials := e.resolveRunCredentials(run)
	sb, err := e.sandboxes.AcquireRun(ctx, run.ID, profile, requirements, credentials, resources)
	if err != nil {
		return nil, err
	}
	env := &runEnvironment{sandbox: sb, credentials: credentials}
	e.runEnvs[run.ID] = env
	return env, nil
}

func (e *Engine) preparationSink(runID string) runtime.EventSink {
	return func(event domain.TelemetryEvent) {
		switch event.Name {
		case "workspace.preparation.started", "workspace.preparation.completed", "workspace.preparation.failed":
			e.AppendRunEvent(runID, event.Name, event.Message)
		}
	}
}

func (e *Engine) prepareLocalEnvironment(ctx context.Context, run domain.Run, env *runEnvironment) (runtime.WorkspacePreparationStatus, error) {
	env.preparing.Store(true)
	defer env.preparing.Store(false)
	// Caller owns env.nodeMu so reconstruction is serialized without turning
	// environment preparation into business-node attempts.
	assets := e.resolveScriptMCPAssets(run.ExecutionContext.ProjectID, run.ExecutionContext.ResponsibleUserID)
	if len(runtime.PreparationNodes(workspacePreparationPlan(run))) > 0 {
		if err := runtime.ProvisionMCPAssets(ctx, env, env.Workspace(), assets, nil); err != nil {
			return runtime.WorkspacePreparationStatus{Status: "failed", Error: err.Error(), Steps: []runtime.WorkspacePreparationStep{}}, err
		}
	}
	return runtime.EnsureWorkspacePreparation(ctx, env.sandbox, workspacePreparationPlan(run), run.RepositoryContexts(), e.preparationSink(run.ID))
}

func (e *Engine) preparedWorkspaceRequest(run domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceRequest, error) {
	if runnerID := e.defaultExecutionRunner(run); runnerID != "" {
		if run.ExecutionContext.RunnerWorkspaceLayout != domain.RunnerWorkspaceLayoutShared && len(runtime.PreparationNodes(runtime.WorkspacePreparationWorkflow(run.WorkflowSnapshot))) > 0 {
			return request, fmt.Errorf("此历史运行使用逐节点工作区，不能自动重建共享环境；请重新发起一次运行")
		}
		runner, ok := e.repo.Runner(runnerID)
		if !ok || !runner.SupportsWorkspacePreparation() {
			return request, fmt.Errorf("执行机器需升级至 %s 才能准备环境或打开沙箱终端", domain.RunnerVersionWorkspacePreparation)
		}
	}
	spec, err := e.runnerSandboxSpec(run)
	if err != nil {
		return request, err
	}
	request.Sandbox = spec
	// Requests are constructed from the persisted frozen run, never client JSON.
	wf := workspacePreparationPlan(run)
	if request.Operation == domain.RunnerWorkspaceOperationPreparationRecord {
		wf = runtime.WorkspacePreparationWorkflow(run.WorkflowSnapshot)
		found := false
		for _, node := range wf.Nodes {
			if node.ID == request.PreparationNodeID && node.RunOnWorkspaceRebuild {
				found = true
				break
			}
		}
		if !found {
			return request, fmt.Errorf("节点未启用工作区重建执行")
		}
	}
	request.Workflow = &wf
	request.Repositories = run.RepositoryContexts()
	credentials := e.resolveRunCredentials(run)
	invocation := &domain.RunnerInvocation{}
	bindRunnerRepositories(invocation, run, credentials.GitToken, credentials.GitUserName, credentials.GitUserEmail)
	assets := e.resolveScriptMCPAssets(run.ExecutionContext.ProjectID, run.ExecutionContext.ResponsibleUserID)
	invocation.Assets = assets
	invocation.MCPCallServers = assets.MCPServers
	invocation.TaihuPAT = credentials.TaihuPAT
	request.PreparationInvocation = invocation
	request.RunStatus = run.Status
	request.CollaborationMode = run.WorkflowSnapshot.CollaborationMode
	request.WorkspaceLayout = run.ExecutionContext.RunnerWorkspaceLayout
	return request, nil
}

func (e *Engine) remoteWorkspacePreparation(ctx context.Context, run domain.Run, operation string) (runtime.WorkspacePreparationStatus, error) {
	request, err := e.preparedWorkspaceRequest(run, domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: operation})
	if err != nil {
		return runtime.WorkspacePreparationStatus{}, err
	}
	response, err := e.requestWorkspaceCheckpoint(ctx, run, request)
	if err != nil {
		return runtime.WorkspacePreparationStatus{}, err
	}
	var state runtime.WorkspacePreparationStatus
	if len(response.Payload) > 0 {
		_ = json.Unmarshal(response.Payload, &state)
	}
	if response.Error != "" {
		if state.Status == "" {
			state.Status = "failed"
		}
		state.Error = response.Error
		return state, fmt.Errorf("%s", response.Error)
	}
	if state.Status == "" {
		return state, fmt.Errorf("执行端尚不支持工作区准备，请更新执行器")
	}
	return state, nil
}

// WorkspacePreparationStatus reads only the last recorded readiness state.
// Actual source and environment fingerprints are checked before every use.
func (e *Engine) WorkspacePreparationStatus(ctx context.Context, id string) (runtime.WorkspacePreparationStatus, error) {
	run, ok := e.repo.RunState(id)
	if !ok {
		return runtime.WorkspacePreparationStatus{}, fmt.Errorf("运行不存在")
	}
	if e.defaultExecutionRunner(run) != "" || e.foreignWorkspaceBind(run) {
		return e.remoteWorkspacePreparation(ctx, run, domain.RunnerWorkspaceOperationPreparationStatus)
	}
	root, reason := e.LocalRunWorkspace(run)
	if reason != "" {
		return runtime.WorkspacePreparationStatus{Status: "unprepared", Steps: []runtime.WorkspacePreparationStep{}}, nil
	}
	state := runtime.ReadWorkspacePreparationStatus(root)
	if state.Status == "preparing" {
		e.runEnvMu.Lock()
		env := e.runEnvs[id]
		active := env != nil && env.preparing.Load()
		e.runEnvMu.Unlock()
		if !active {
			state.Status = "unprepared"
			state.Error = "上次工作区准备已中断，请重新准备"
		}
	}

	if state.Status == "ready" {
		e.runEnvMu.Lock()
		env := e.runEnvs[id]
		e.runEnvMu.Unlock()
		reason := ""
		if env == nil {
			reason = "执行环境尚未检查；文件仍可浏览"
		} else {
			epoch, err := runtime.ReadSandboxEnvironmentIdentity(ctx, env.sandbox)
			if err != nil || !runtime.WorkspacePreparationReusable(state, workspacePreparationPlan(run), run.RepositoryContexts(), epoch) {
				reason = "执行环境或准备配置已变化，需要重新准备"
			}
		}
		if reason != "" {
			state.Status = "unprepared"
			state.Error = reason
			for i := range state.Steps {
				state.Steps[i].Status = "unprepared"
			}
			return state, nil
		}
	}
	return state, nil
}

// EnsureWorkspaceReady establishes the execution environment after file
// restoration. It deliberately leaves the run outcome and node history intact.
func (e *Engine) EnsureWorkspaceReady(ctx context.Context, id string) (runtime.WorkspacePreparationStatus, error) {
	run, ok := e.repo.RunState(id)
	if !ok {
		return runtime.WorkspacePreparationStatus{}, fmt.Errorf("运行不存在")
	}
	if e.defaultExecutionRunner(run) != "" || e.foreignWorkspaceBind(run) {
		return e.remoteWorkspacePreparation(ctx, run, domain.RunnerWorkspaceOperationPrepare)
	}
	env, err := e.acquireConversationEnvironment(ctx, run)
	if err != nil {
		return runtime.WorkspacePreparationStatus{Status: "failed", Error: err.Error(), Steps: []runtime.WorkspacePreparationStep{}}, err
	}
	env.nodeMu.Lock()
	defer env.nodeMu.Unlock()
	if current, ok := e.repo.RunState(id); ok {
		run = current
	}
	if hasRunningNode(run) || env.activeExecutions.Load() > 0 || e.workspaceTerminals.HasRunScope(id) || e.WorkspaceActive != nil && e.WorkspaceActive(run) {
		state := runtime.ReadWorkspacePreparationStatus(env.Workspace())
		epoch, err := runtime.ReadSandboxEnvironmentIdentity(ctx, env.sandbox)
		if run.WorkflowSnapshot.CollaborationMode == "group" && err == nil && runtime.WorkspacePreparationReusable(state, workspacePreparationPlan(run), run.RepositoryContexts(), epoch) {
			return state, nil
		}
		return runtime.WorkspacePreparationStatus{Status: "unprepared", Error: "成员或终端正在使用工作区，请结束当前操作后重新准备", Steps: []runtime.WorkspacePreparationStep{}}, fmt.Errorf("成员或终端正在使用工作区，请结束当前操作后重新准备")
	}
	return e.prepareLocalEnvironment(ctx, run, env)
}

func (e *Engine) PrepareNodeWorkspace(ctx context.Context, run domain.Run, env *runEnvironment) (runtime.WorkspacePreparationStatus, error) {
	if len(runtime.PreparationNodes(runtime.WorkspacePreparationWorkflow(run.WorkflowSnapshot))) == 0 {
		return runtime.WorkspacePreparationStatus{Status: "ready", Steps: []runtime.WorkspacePreparationStep{}}, nil
	}
	if e.defaultExecutionRunner(run) != "" {
		if run.WorkflowSnapshot.CollaborationMode == "group" {
			if current, ok := e.repo.RunState(run.ID); ok {
				run = current
			}
			state, statusErr := e.remoteWorkspacePreparation(ctx, run, domain.RunnerWorkspaceOperationPreparationStatus)
			if statusErr == nil && state.Status == "ready" {
				return state, nil
			}
		}
		return e.remoteWorkspacePreparation(ctx, run, domain.RunnerWorkspaceOperationPrepare)
	}
	if run.WorkflowSnapshot.CollaborationMode == "group" && env.activeExecutions.Load() > 0 {
		state := runtime.ReadWorkspacePreparationStatus(env.Workspace())
		epoch, err := runtime.ReadSandboxEnvironmentIdentity(ctx, env.sandbox)
		if err == nil && runtime.WorkspacePreparationReusable(state, workspacePreparationPlan(run), run.RepositoryContexts(), epoch) {
			return state, nil
		}
		return state, fmt.Errorf("正在协作的工作区环境发生变化，请等待当前分工结束后重新准备")
	}
	return e.prepareLocalEnvironment(ctx, run, env)
}

// HandleWorkspaceTerminal uses exactly the same environment and preparation
// contract as SOP execution. The pool owns the PTY, the Engine owns the sandbox.
func (e *Engine) HandleWorkspaceTerminal(ctx context.Context, id string, pool *runtime.TerminalPool, scope string, req domain.WorkspaceTerminalRequest) (runtime.TerminalResult, error) {
	if req.Operation == "open" {
		if _, err := e.EnsureWorkspaceReady(ctx, id); err != nil {
			return runtime.TerminalResult{}, err
		}
	}
	return e.HandlePreparedWorkspaceTerminal(ctx, id, pool, scope, req)
}

func (e *Engine) HandlePreparedWorkspaceTerminal(ctx context.Context, id string, pool *runtime.TerminalPool, scope string, req domain.WorkspaceTerminalRequest) (runtime.TerminalResult, error) {
	if req.Operation != "open" {
		return pool.HandleSandbox(ctx, nil, scope, req, nil)
	}
	e.runEnvMu.Lock()
	env := e.runEnvs[id]
	e.runEnvMu.Unlock()
	if env == nil {
		return runtime.TerminalResult{}, fmt.Errorf("工作区环境尚未准备，请重新打开终端")
	}

	state := runtime.ReadWorkspacePreparationStatus(env.Workspace())
	epoch, err := runtime.ReadSandboxEnvironmentIdentity(ctx, env.sandbox)
	if err != nil || state.Status != "ready" || state.Environment != epoch {
		return runtime.TerminalResult{}, fmt.Errorf("工作区执行环境已变化，请重新准备后打开终端")
	}
	return pool.HandleSandbox(ctx, env.sandbox, scope, req, nil)
}

// TrustedWorkspaceRequest attaches frozen execution configuration to an
// authenticated workspace transport. It is never exposed as user-controlled configuration.
func (e *Engine) TrustedWorkspaceRequest(id string, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceRequest, error) {
	run, ok := e.repo.RunState(id)
	if !ok {
		return request, fmt.Errorf("运行不存在")
	}
	return e.preparedWorkspaceRequest(run, request)
}

func (e *Engine) WorkspaceRemoteHost(run domain.Run) bool { return e.foreignWorkspaceBind(run) }
func (e *Engine) ForwardWorkspaceRequest(ctx context.Context, run domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
	trusted, err := e.preparedWorkspaceRequest(run, request)
	if err != nil {
		return domain.RunnerWorkspaceResponse{}, err
	}
	return e.requestWorkspaceCheckpoint(ctx, run, trusted)
}

// A settled historical workflow may refer to a role removed from the catalog.
// Opening its files/terminal must not require resurrecting that model role.
// Execution still uses the strict workflow validation and executor resolution.
func workspaceConversationRequirements(st *repo.Store, wf domain.WorkflowDefinition) (runtime.SandboxRequirements, error) {
	var items []runtime.SandboxRequirements
	for _, node := range wf.Nodes {
		if node.Type != "agent" {
			continue
		}
		agent, ok := workflowAgent(st, wf, node.AgentID)
		if !ok {
			continue
		}
		executor, err := resolveNodeExecutor(st, node, agent)
		if err != nil {
			continue
		}
		items = append(items, runtime.ProviderRequirements(executor.Kind))
	}
	return runtime.MergeSandboxRequirements(items...), nil
}

// ForwardPreparedWorkspaceRequest is used under an editor transaction after
// the caller has bound all configuration outside the run-row lock.
func (e *Engine) ForwardPreparedWorkspaceRequest(ctx context.Context, run domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
	run.ExecutionContext.ExecutionMode = domain.ExecutionModeCloud
	return e.requestWorkspaceCheckpoint(ctx, run, request)
}

// Only scripts that already succeeded in normal SOP execution can be replayed
// to reconstruct its environment. Pending/failed/current/downstream nodes keep
// their ordinary scheduling semantics and are never executed early.
func workspacePreparationPlan(run domain.Run) domain.WorkflowDefinition {
	wf := runtime.WorkspacePreparationWorkflow(run.WorkflowSnapshot)
	wf.Nodes = append([]domain.WorkflowNode(nil), wf.Nodes...)
	states := run.Nodes
	if run.WorkflowSnapshot.CollaborationMode == "group" && run.WorkflowSnapshot.SOPCheckpoint != nil {
		states = run.WorkflowSnapshot.SOPCheckpoint.Nodes
	}
	for i := range wf.Nodes {
		if !wf.Nodes[i].RunOnWorkspaceRebuild {
			continue
		}
		state := states[wf.Nodes[i].ID]
		if state == nil || state.Status != "succeeded" {
			wf.Nodes[i].RunOnWorkspaceRebuild = false
		}
	}
	wf.CollaborationMode = ""
	wf.SOPCheckpoint = nil
	return wf
}

func (e *Engine) recordNodeWorkspacePreparation(ctx context.Context, run domain.Run, env *runEnvironment, node domain.WorkflowNode, output string) error {
	if len(output) > 32<<10 {
		output = output[:32<<10]
	}
	if !node.RunOnWorkspaceRebuild {
		return nil
	}
	if e.defaultExecutionRunner(run) != "" {
		request, err := e.preparedWorkspaceRequest(run, domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationPreparationRecord, PreparationNodeID: node.ID, PreparationOutput: output})
		if err != nil {
			return err
		}
		response, err := e.requestWorkspaceCheckpoint(ctx, run, request)
		if err != nil {
			return err
		}
		if response.Error != "" {
			return fmt.Errorf("记录工作区重建步骤失败：%s", response.Error)
		}
		return nil
	}
	if run.WorkflowSnapshot.CollaborationMode == "group" {
		env.nodeMu.Lock()
		defer env.nodeMu.Unlock()
	}
	_, err := runtime.RecordWorkspacePreparation(ctx, env.sandbox, runtime.WorkspacePreparationWorkflow(run.WorkflowSnapshot), run.RepositoryContexts(), node.ID, output)
	return err
}
