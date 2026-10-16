package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// CLI Runner dispatch (cli-connect.md). A role (AgentDefinition) bound to a
// runner machine executes its Agent nodes there: the fully compiled task book
// and asset bundle are queued as a durable RunnerTask, the runner machine
// runs its local Agent CLI in the Run's shared workspace, and declared artifact
// files are uploaded back so verification and handoff keep working on the
// control plane. Verification gates execute on the same machine and
// workspace (terminal tasks), which is also the path for future sandbox-side
// operations (docker/seatbelt/bwrap on the runner).

// SetRunnerDispatchNotify wires the long-poll wakeup so a freshly queued task
// reaches an idle runner immediately instead of on the next claim retry.
func (e *Engine) SetRunnerDispatchNotify(fn func(provider string)) {
	e.mu.Lock()
	e.runnerDispatchNotify = fn
	e.mu.Unlock()
}

func (e *Engine) notifyRunnerDispatch(provider string) {
	e.mu.Lock()
	fn := e.runnerDispatchNotify
	e.mu.Unlock()
	if fn != nil {
		fn(provider)
	}
}

// SetRunnerControlNotify wires the low-latency runner WebSocket control path.
// A missing/disconnected listener is harmless because desired_state is durable
// and is also returned by lease renewal.
func (e *Engine) SetRunnerControlNotify(fn func(runnerID, taskID, desiredState string)) {
	e.mu.Lock()
	e.runnerControlNotify = fn
	e.mu.Unlock()
}

func (e *Engine) notifyRunnerControl(runnerID, taskID, desiredState string) {
	e.mu.Lock()
	fn := e.runnerControlNotify
	e.mu.Unlock()
	if fn != nil && strings.TrimSpace(runnerID) != "" {
		fn(runnerID, taskID, desiredState)
	}
}

func (e *Engine) cancelRunnerTasksForRun(runID string) error {
	ids, err := e.repo.CancelRunnerTasksForRun(runID, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, id := range ids {
		e.NotifyRunnerTaskChanged(id)
		if task, ok := e.repo.RunnerTask(id); ok {
			e.notifyRunnerControl(task.LeaseOwner, id, task.DesiredState)
		}
	}
	return nil
}

// runnerWorkspaceRel is the runner-side workspace frozen for a Run. New Runs
// share one checkout across scripts, Agents, and gates. Runs created before
// that layout was introduced keep their per-node path so an in-place retry
// continues from the directory where it originally failed.
func runnerWorkspaceRel(run domain.Run, nodeID string) string {
	base := filepath.Join("runs", safeRunnerPath(run.ID))
	if run.ExecutionContext.RunnerWorkspaceLayout == domain.RunnerWorkspaceLayoutShared {
		return filepath.ToSlash(filepath.Join(base, "workspace"))
	}
	return filepath.ToSlash(filepath.Join(base, safeRunnerPath(nodeID)))
}

// runnerPreviewWorkspaceRel is the directory a preview SourcePath is
// relative to: the shared checkout, or the run root for legacy layouts.
func runnerPreviewWorkspaceRel(run domain.Run) string {
	if run.ExecutionContext.RunnerWorkspaceLayout == domain.RunnerWorkspaceLayoutShared {
		return runnerWorkspaceRel(run, "")
	}
	return runnerRunWorkspaceRel(run.ID)
}

func runnerRunWorkspaceRel(runID string) string {
	return filepath.ToSlash(filepath.Join("runs", safeRunnerPath(runID)))
}

func safeRunnerPath(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "node"
	}
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

func (e *Engine) runnerSandboxSpec(run domain.Run) (*domain.RunnerSandboxSpec, error) {
	profile, resources, err := e.workspaceSandboxProfile(run)
	if err != nil {
		return nil, err
	}
	spec := resolvedSandboxSnapshot(profile, resources)
	requirements, err := workspaceConversationRequirements(e.repo, run.WorkflowSnapshot)
	if err != nil {
		return nil, err
	}
	spec.Requirements = &domain.RunnerSandboxRequirements{PrivateHome: requirements.PrivateHome, BunFS: requirements.BunFS}
	for _, state := range requirements.CLIStates {
		spec.Requirements.CLIStates = append(spec.Requirements.CLIStates, domain.RunnerCLIStateRequirement{Directory: state.Directory, Files: append([]string(nil), state.Files...)})
	}
	return spec, nil
}

// dispatchRunnerTask queues one Agent invocation as a RunnerTask targeted at
// the responsible user's default execution machine, and waits for its
// terminal state. Each runtime selects local CLI login or the responsible
// user's saved provider credential; only the latter is placed in the
// task-scoped invocation. The run's repository travels with the task so the
// sandbox/workspace is created on that machine.
func (e *Engine) dispatchRunnerTask(ctx context.Context, runnerID string, run domain.Run, node domain.WorkflowNode, sb *runEnvironment, workspace string, ex domain.ExecutorDefinition, agent domain.AgentDefinition, assets domain.AgentAssetBundle, prompt, invocationID string, options runtime.InvocationOptions, sink runtime.EventSink) (string, error) {
	if err := e.requireRunnerProvider(runnerID, ex.Kind); err != nil {
		return "", err
	}
	runner, _ := e.repo.Runner(runnerID)
	authMode := runner.RuntimeAuthMode(ex.Kind)
	resumeSessionID := resolveRunnerResumeSessionID(run, node.ID, options)
	invocation := &domain.RunnerInvocation{
		ArchivedFiles:    e.legacyArchivedWorkspaceFiles(run, node.ID),
		Concurrent:       options.Concurrent,
		RunID:            run.ID,
		NodeID:           node.ID,
		InvocationID:     invocationID,
		ResumeSessionID:  resumeSessionID,
		Executor:         ex,
		Agent:            agent,
		Assets:           assets,
		Artifacts:        node.ArtifactOutputs,
		HumanContext:     run.HumanContext,
		CredentialSource: authMode,
		GateResultPath:   options.GateResultPath,
	}
	if strings.TrimSpace(options.GateResultPath) != "" {
		invocation.Artifacts = append(invocation.Artifacts, gateResultContract(options.GateResultPath))
	}
	if runtime.RequiresTaihuPAT(assets) {
		if strings.TrimSpace(sb.credentialSnapshot().TaihuPAT) == "" {
			return "", errors.New("当前责任人未配置太湖个人令牌：请先在个人凭证中配置")
		}
		invocation.TaihuPAT = sb.credentialSnapshot().TaihuPAT
	}
	if authMode == domain.RuntimeAuthPersonal {
		switch ex.Kind {
		case "codex":
			if strings.TrimSpace(sb.credentialSnapshot().Codex.APIKey) == "" {
				return "", errors.New("当前责任人未配置 Codex API Key：请先在个人设置中配置，或将执行机器认证方式切回本机登录")
			}
			invocation.CodexAPIKey = sb.credentialSnapshot().Codex.APIKey
			invocation.CodexBaseURL = sb.credentialSnapshot().Codex.BaseURL
		case "claude-code":
			if strings.TrimSpace(sb.credentialSnapshot().ClaudeCode.APIKey) == "" {
				return "", errors.New("当前责任人未配置 Claude Code API Key：请先在个人设置中配置，或将执行机器认证方式切回本机登录")
			}
			invocation.ClaudeCodeAPIKey = sb.credentialSnapshot().ClaudeCode.APIKey
			invocation.ClaudeCodeBaseURL = sb.credentialSnapshot().ClaudeCode.BaseURL
		case "codebuddy":
			if strings.TrimSpace(sb.credentialSnapshot().CodeBuddy.APIKey) == "" {
				return "", errors.New("当前责任人未配置 CodeBuddy API Key：请先在个人设置中配置")
			}
			invocation.CodeBuddyAPIKey = sb.credentialSnapshot().CodeBuddy.APIKey
			invocation.CodeBuddyInternetEnvironment = sb.credentialSnapshot().CodeBuddy.InternetEnvironment
		}
	}
	bindRunnerRepositories(invocation, run, sb.credentialSnapshot().GitToken, sb.credentialSnapshot().GitUserName, sb.credentialSnapshot().GitUserEmail)
	reported := sb.reportedOutputs(node.ID)
	if reported != nil {
		invocation.Artifacts = nil
		invocation.ReportOutputs = true
		invocation.Inputs = reported.inputs
		invocation.OutputFiles = artifactOutputFiles(run, node)
		if path := strings.TrimSpace(options.GateResultPath); path != "" {
			invocation.OutputFiles = append(invocation.OutputFiles, path)
		}
		invocation.RecoverOutputs = normalizeExecutionScope(node.ExecutionScope) != domain.ExecutionScopeRepositories
	}
	invocation.MCPCallServers = e.resolveScriptMCPAssets(run.ExecutionContext.ProjectID, run.ExecutionContext.ResponsibleUserID).MCPServers
	if strings.TrimSpace(invocation.TaihuPAT) == "" {
		invocation.TaihuPAT = sb.credentialSnapshot().TaihuPAT
	}
	task := domain.RunnerTask{
		ID:          fmt.Sprintf("rt-%d-%s", time.Now().UnixNano(), safeRunnerPath(node.ID)),
		RunnerID:    runnerID,
		Provider:    ex.Kind,
		Prompt:      prompt,
		Workspace:   workspace,
		Status:      domain.RunnerTaskQueued,
		MaxAttempts: 1, // engine-level retry/loop owns re-invocation policy
		RunID:       run.ID,
		NodeID:      node.ID,
		Invocation:  invocation,
	}
	sandboxSpec, err := e.runnerSandboxSpec(run)
	if err != nil {
		return "", err
	}
	task.Sandbox = sandboxSpec
	if err := validateSOPHandoffRunnerPayload(run, task); err != nil {
		return "", err
	}
	if err := e.repo.PutRunnerTask(task); err != nil {
		return "", err
	}
	e.notifyRunnerDispatch(ex.Kind)
	if sink != nil {
		sink(domain.TelemetryEvent{
			Category: "sandbox", Name: "runner.dispatch", Phase: "start",
			Message:    fmt.Sprintf("任务下发到执行机器 %s（provider=%s task=%s）", runnerID, ex.Kind, task.ID),
			Attributes: map[string]any{"provider": ex.Kind, "runnerId": runnerID, "taskId": task.ID},
		})
	}
	final, err := e.awaitRunnerTask(ctx, task.ID, sink)
	if err != nil {
		return "", err
	}
	if reported != nil {
		reported.absorb(final.Outputs)
		e.recordWorkspaceFacts(run.ID, final.Outputs)
	}
	switch final.Status {
	case domain.RunnerTaskSuccess:
		if reported == nil {
			if err := restoreRunnerArtifacts(sb.Workspace(), task.Invocation.Artifacts, final.Artifacts); err != nil {
				return final.Result, fmt.Errorf("restore runner artifacts: %w", err)
			}
		}
		if sink != nil {
			sink(domain.TelemetryEvent{
				Category: "sandbox", Name: "runner.completed", Phase: "finish",
				Message:    fmt.Sprintf("执行机器任务完成（task=%s attempt=%d 产物 %d 个）", final.ID, final.Attempt, len(final.Artifacts)),
				Attributes: map[string]any{"taskId": final.ID, "attempt": final.Attempt, "artifacts": len(final.Artifacts)},
			})
		}
		return final.Result, nil
	case domain.RunnerTaskFailed:
		msg := strings.TrimSpace(final.Error)
		if msg == "" {
			msg = "runner task failed"
		}
		if final.ExitCode != nil {
			msg = fmt.Sprintf("%s (exit %d)", msg, *final.ExitCode)
		}
		return "", errors.New(msg)
	case domain.RunnerTaskCanceled:
		return "", errors.New("runner task canceled")
	default:
		return "", fmt.Errorf("runner task ended in unexpected state %q", final.Status)
	}
}

// resolveRunnerResumeSessionID picks the CLI session a runner-side invocation
// should resume. The engine-level option wins: on the missing-submit recovery
// path it carries the session the engine just observed (nr.SessionID), while
// nr.ResumeSessionID may already have been cleared by telemetry. The node field
// remains the fallback for operator-requested resumes that did not go through
// the engine loop.
func resolveRunnerResumeSessionID(run domain.Run, nodeID string, options runtime.InvocationOptions) string {
	if id := strings.TrimSpace(options.ResumeSessionID); id != "" {
		return id
	}
	if nr := run.Nodes[nodeID]; nr != nil {
		return strings.TrimSpace(nr.ResumeSessionID)
	}
	return ""
}

func bindRunnerRepositories(invocation *domain.RunnerInvocation, run domain.Run, gitToken, gitUserName, gitUserEmail string) {
	if invocation == nil {
		return
	}
	invocation.GitUserName = strings.TrimSpace(gitUserName)
	invocation.GitUserEmail = strings.TrimSpace(gitUserEmail)
	if strings.TrimSpace(run.Repository.CloneURL) != "" && len(run.Repositories) == 0 {
		invocation.Repository = &domain.RunnerRepository{
			RepositoryID: run.Repository.RepositoryID,
			Name:         run.Repository.Name,
			CloneURL:     run.Repository.CloneURL,
			Ref:          run.Repository.BaseBranch,
			WorkBranch:   run.Repository.WorkBranch,
			GitToken:     gitToken,
		}
	}
	for _, rc := range run.RepositoryContexts() {
		if strings.TrimSpace(rc.CloneURL) == "" {
			continue
		}
		invocation.Repositories = append(invocation.Repositories, domain.RunnerRepository{
			RepositoryID:   rc.RepositoryID,
			Name:           rc.Name,
			CloneURL:       rc.CloneURL,
			Ref:            rc.BaseBranch,
			WorkBranch:     rc.WorkBranch,
			GitToken:       gitToken,
			Dir:            rc.WorkspaceDir,
			SyncWorkBranch: workBranchSyncable(rc),
		})
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// dispatchRunnerScript executes a workflow script on the Run's frozen
// execution machine. Repository metadata is included so a script can also be
// the first node; subsequent shared-layout nodes reuse the same checkout and
// its workspace-local Git index flags.
func (e *Engine) dispatchRunnerScript(ctx context.Context, runnerID string, run domain.Run, nodeID, workspace, language, script, artifactDirectory string, artifacts []domain.ArtifactContract, sb *runEnvironment, gitToken, gitUserName, gitUserEmail, taihuPAT string, assets domain.AgentAssetBundle, sink runtime.EventSink) (string, error) {
	if err := e.requireRunnerProvider(runnerID, "terminal"); err != nil {
		return "", err
	}
	command := script
	if strings.EqualFold(strings.TrimSpace(language), "python") {
		command = "python3 -c " + shellSingleQuote(script)
	}
	invocation := &domain.RunnerInvocation{
		ArchivedFiles: e.legacyArchivedWorkspaceFiles(run, nodeID),
		Concurrent:    run.WorkflowSnapshot.CollaborationMode == "group", RunID: run.ID, NodeID: nodeID, Artifacts: artifacts, ArtifactDirectory: artifactDirectory, Assets: assets, TaihuPAT: taihuPAT, MCPCallServers: assets.MCPServers, HumanContext: run.HumanContext}
	bindRunnerRepositories(invocation, run, gitToken, gitUserName, gitUserEmail)
	reported := sb.reportedOutputs(nodeID)
	if reported != nil {
		invocation.Artifacts = nil
		invocation.ArtifactDirectory = ""
		invocation.ReportOutputs = true
		invocation.Inputs = reported.inputs
		for _, contract := range artifacts {
			if rel, ok := artifactRelPath(artifactDirectory, contract.Path); ok {
				invocation.OutputFiles = append(invocation.OutputFiles, rel)
			}
		}
		invocation.RecoverOutputs = strings.TrimSpace(artifactDirectory) == ""
	}
	task := domain.RunnerTask{
		ID:          fmt.Sprintf("rt-%d-%s-script", time.Now().UnixNano(), safeRunnerPath(nodeID)),
		RunnerID:    runnerID,
		Provider:    "terminal",
		Command:     command,
		Workspace:   workspace,
		Status:      domain.RunnerTaskQueued,
		MaxAttempts: 1,
		RunID:       run.ID,
		NodeID:      nodeID,
		Invocation:  invocation,
	}
	var err error
	task.Sandbox, err = e.runnerSandboxSpec(run)
	if err != nil {
		return "", err
	}
	if err := validateSOPHandoffRunnerPayload(run, task); err != nil {
		return "", err
	}
	if err := e.repo.PutRunnerTask(task); err != nil {
		return "", err
	}
	e.notifyRunnerDispatch("terminal")
	if sink != nil {
		sink(domain.TelemetryEvent{
			Category: "sandbox", Name: "runner.dispatch", Phase: "start",
			Message:    fmt.Sprintf("脚本下发到执行机器 %s（task=%s）", runnerID, task.ID),
			Attributes: map[string]any{"provider": "terminal", "runnerId": runnerID, "taskId": task.ID},
		})
	}
	final, err := e.awaitRunnerTask(ctx, task.ID, sink)
	if err != nil {
		return "", err
	}
	if reported != nil {
		reported.absorb(final.Outputs)
		e.recordWorkspaceFacts(run.ID, final.Outputs)
	}
	switch final.Status {
	case domain.RunnerTaskSuccess:
		if reported == nil {
			if err := restoreRunnerArtifacts(filepath.Join(sb.Workspace(), filepath.FromSlash(artifactDirectory)), artifacts, final.Artifacts); err != nil {
				return final.Result, fmt.Errorf("restore runner script artifacts: %w", err)
			}
		}
		return final.Result, nil
	case domain.RunnerTaskCanceled:
		return final.Result, errors.New("runner script canceled")
	default:
		message := strings.TrimSpace(final.Error)
		if message == "" {
			message = strings.TrimSpace(final.Result)
		}
		if message == "" {
			message = "script failed on runner"
		}
		return final.Result, errors.New(message)
	}
}

// defaultExecutionRunner resolves the responsible user's default execution
// machine (执行机器). Empty means the cloud mode.
func (e *Engine) defaultExecutionRunner(run domain.Run) string {
	switch run.ExecutionContext.ExecutionMode {
	case domain.ExecutionModeCloud:
		return ""
	case domain.ExecutionModeRunner:
		return strings.TrimSpace(run.ExecutionContext.ExecutionRunnerID)
	}
	// Legacy runs predate target snapshots; retain their historical behavior.
	return e.repo.DefaultRunner(run.ExecutionContext.ResponsibleUserID)
}

// validateExecutionReconnect fails a retry before mutating run state when the
// frozen execution target cannot be reached. The actual dispatch repeats the
// same validation to cover a disconnect between this check and execution.
func (e *Engine) validateExecutionReconnect(run domain.Run, nodeID string) error {
	runnerID := e.defaultExecutionRunner(run)
	if runnerID == "" || isPoolRun(run) {
		// A pool run is rebound to another pool runner when its node starts.
		return nil
	}
	runner, ok := e.repo.Runner(runnerID)
	if !ok {
		return fmt.Errorf("无法重连原执行环境：执行机器 %s 不存在", runnerID)
	}
	if runner.Status != domain.RunnerStatusOnline {
		return fmt.Errorf("无法重连原执行环境：执行机器 %s（%s）当前离线", runnerID, runner.Name)
	}
	for _, node := range run.WorkflowSnapshot.Nodes {
		if node.ID != nodeID {
			continue
		}
		provider := "terminal"
		if node.Type != "script" && node.Type != "command" && node.Type != "approval" {
			agent, found := e.repo.Agent(node.AgentID)
			if !found {
				return fmt.Errorf("无法重连原执行环境：agent %s 不存在", node.AgentID)
			}
			executor, err := resolveNodeExecutor(e.repo, node, agent)
			if err != nil {
				return fmt.Errorf("无法重连原执行环境：%w", err)
			}
			provider = executor.Kind
		}
		if !runner.SupportsProvider(provider) {
			return fmt.Errorf("无法重连原执行环境：执行机器 %s 不再提供 %s", runnerID, provider)
		}
		return nil
	}
	return fmt.Errorf("无法重连原执行环境：快照中不存在节点 %s", nodeID)
}

// queueRunnerRunCleanup durably schedules cleanup on the machine that owns
// the frozen runner workspace. Successful runs call it during normal finish;
// explicit deletion also calls it for failed/cancelled runs. The deterministic
// task ID makes those paths idempotent without resetting active/completed work.
func (e *Engine) queueRunnerRunCleanup(run domain.Run) error {
	runnerID := e.defaultExecutionRunner(run)
	if runnerID == "" {
		return nil
	}
	taskID := "rt-cleanup-" + safeRunnerPath(run.ID)
	if existing, ok := e.repo.RunnerTask(taskID); ok {
		switch existing.Status {
		case domain.RunnerTaskQueued, domain.RunnerTaskLeased, domain.RunnerTaskRunning:
			return nil
		}
	}
	if _, ok := e.repo.Runner(runnerID); !ok {
		// Explicit runner uninstall removes every runner workspace before it
		// deregisters the machine. There is no endpoint left to claim a cleanup
		// task afterwards, so consider that lifecycle already settled.
		return nil
	}
	task := domain.RunnerTask{
		ID:          taskID,
		RunnerID:    runnerID,
		Provider:    "terminal",
		Action:      domain.RunnerTaskActionCleanupRun,
		Workspace:   runnerRunWorkspaceRel(run.ID),
		Status:      domain.RunnerTaskQueued,
		MaxAttempts: 3,
		RunID:       run.ID,
		Sandbox:     run.WorkflowSnapshot.SandboxSnapshot,
	}
	if err := e.repo.PutRunnerTask(task); err != nil {
		return err
	}
	e.notifyRunnerDispatch("terminal")
	return nil
}

func (e *Engine) queueRunnerPreviewArchive(run domain.Run, spec domain.RunnerPreviewArchiveSpec) error {
	runnerID := e.defaultExecutionRunner(run)
	if runnerID == "" {
		return fmt.Errorf("preview archive requires the original runner")
	}
	taskID := "rt-preview-archive-" + safeRunnerPath(run.ID)
	if existing, ok := e.repo.RunnerTask(taskID); ok {
		switch existing.Status {
		case domain.RunnerTaskQueued, domain.RunnerTaskLeased, domain.RunnerTaskRunning:
			return nil
		}
		// A finished archive is replaced when the build node succeeds again.
		// The new task carries a fresh upload token for the new bundle.
	}
	if _, ok := e.repo.Runner(runnerID); !ok {
		return fmt.Errorf("preview archive runner %s is unavailable", runnerID)
	}
	task := domain.RunnerTask{
		ID:             taskID,
		RunnerID:       runnerID,
		Provider:       "terminal",
		Action:         domain.RunnerTaskActionArchivePreview,
		Workspace:      runnerPreviewWorkspaceRel(run),
		Status:         domain.RunnerTaskQueued,
		MaxAttempts:    3,
		RunID:          run.ID,
		PreviewArchive: &spec,
	}
	if err := e.repo.PutRunnerTask(task); err != nil {
		return err
	}
	e.notifyRunnerDispatch("terminal")
	return nil
}

// dispatchRunnerCommand runs a terminal command (verification gate) on the
// bound runner machine inside the node's task workspace. The invocation
// carries the same repository git token as a script task: a gate such as
// `git push` otherwise starts with prompts disabled and no credentials.
func (e *Engine) dispatchRunnerCommand(ctx context.Context, runnerID, runID, nodeID, workspace, command string, gateSink runtime.EventSink) (string, error) {
	task := domain.RunnerTask{
		ID:          fmt.Sprintf("rt-%d-gate", time.Now().UnixNano()),
		RunnerID:    runnerID,
		Provider:    "terminal",
		Command:     command,
		Workspace:   workspace,
		Status:      domain.RunnerTaskQueued,
		MaxAttempts: 1,
		RunID:       runID,
		NodeID:      nodeID,
	}
	if run, ok := e.repo.RunState(runID); ok {
		var err error
		task.Sandbox, err = e.runnerSandboxSpec(run)
		if err != nil {
			return "", err
		}
		task.Invocation = e.gateCommandInvocation(run, nodeID)
	}
	if err := e.repo.PutRunnerTask(task); err != nil {
		return "", err
	}
	e.notifyRunnerDispatch("terminal")
	if gateSink != nil {
		gateSink(domain.TelemetryEvent{Category: "process", Name: "process.started", Phase: "start", Message: "门禁命令下发到执行器：" + command})
	}
	final, err := e.awaitRunnerTask(ctx, task.ID, gateSink)
	if err != nil {
		return "", err
	}
	switch final.Status {
	case domain.RunnerTaskSuccess:
		if gateSink != nil {
			gateSink(domain.TelemetryEvent{Category: "process", Name: "process.finished", Phase: "finish", Message: "门禁在执行器上执行完成"})
		}
		return final.Result, nil
	case domain.RunnerTaskCanceled:
		return "", errors.New("runner task canceled")
	default:
		msg := strings.TrimSpace(final.Error)
		if msg == "" {
			msg = strings.TrimSpace(final.Result)
		}
		if msg == "" {
			msg = "command failed on runner"
		}
		return final.Result, errors.New(msg)
	}
}

// gateCommandInvocation is the payload a verification command needs on the
// runner. Repository entries supply the responsible user's git token; an
// existing checkout is left in place, and the runner only uses the token to
// authenticate that command.
func (e *Engine) gateCommandInvocation(run domain.Run, nodeID string) *domain.RunnerInvocation {
	assets := e.resolveScriptMCPAssets(run.ExecutionContext.ProjectID, run.ExecutionContext.ResponsibleUserID)
	invocation := &domain.RunnerInvocation{
		Concurrent: run.WorkflowSnapshot.CollaborationMode == "group", RunID: run.ID, NodeID: nodeID, Assets: assets, MCPCallServers: assets.MCPServers}
	if user, ok := e.repo.User(run.ExecutionContext.ResponsibleUserID); ok {
		invocation.TaihuPAT = user.TaihuPAT
	}
	identity := resolveGitIdentity(e.repo, run.ExecutionContext.ResponsibleUserID)
	bindRunnerRepositories(invocation, run, resolveGitToken(e.repo, run.ExecutionContext.ResponsibleUserID), identity.name, identity.email)
	return invocation
}

// NotifyRunnerTaskChanged wakes workflow goroutines waiting for a runner task
// transition. The DB remains authoritative; notifications only avoid a query
// per running task per second.
func (e *Engine) NotifyRunnerTaskChanged(taskID string) {
	e.runnerTaskMu.Lock()
	defer e.runnerTaskMu.Unlock()
	for ch := range e.runnerTaskSubs[taskID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (e *Engine) subscribeRunnerTask(taskID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	e.runnerTaskMu.Lock()
	if e.runnerTaskSubs[taskID] == nil {
		e.runnerTaskSubs[taskID] = map[chan struct{}]struct{}{}
	}
	e.runnerTaskSubs[taskID][ch] = struct{}{}
	e.runnerTaskMu.Unlock()
	return ch, func() {
		e.runnerTaskMu.Lock()
		delete(e.runnerTaskSubs[taskID], ch)
		if len(e.runnerTaskSubs[taskID]) == 0 {
			delete(e.runnerTaskSubs, taskID)
		}
		e.runnerTaskMu.Unlock()
	}
}

// awaitRunnerTask waits on in-process task notifications and polls the durable
// task/event stream when API and worker run in separate processes. Run
// cancellation also flags the remote task.
func (e *Engine) awaitRunnerTask(ctx context.Context, taskID string, sink runtime.EventSink) (domain.RunnerTask, error) {
	var lastEventSeq int64
	lastStatus := ""
	reportStatus := func(task domain.RunnerTask) {
		if sink == nil || task.Status == lastStatus {
			return
		}
		lastStatus = task.Status
		switch task.Status {
		case domain.RunnerTaskLeased:
			sink(domain.TelemetryEvent{
				Category: "sandbox", Name: "runner.claimed", Phase: "progress",
				Message:  "执行机器已领取任务，正在准备工作区和沙箱",
				Provider: task.Provider,
				Source:   domain.TelemetrySource{Layer: "executor", Adapter: "runner-v1", Provider: task.Provider},
			})
		case domain.RunnerTaskRunning:
			sink(domain.TelemetryEvent{
				Category: "process", Name: "runner.started", Phase: "start",
				Message:  "执行机器已启动任务",
				Provider: task.Provider,
				Source:   domain.TelemetrySource{Layer: "executor", Adapter: "runner-v1", Provider: task.Provider},
			})
		}
	}
	for {
		current, ok := e.repo.RunnerTask(taskID)
		if !ok {
			return domain.RunnerTask{}, fmt.Errorf("runner task %s disappeared", taskID)
		}
		lastEventSeq = bridgeRunnerEventsToSink(current, e.repo.RunnerTaskEventsAfter(taskID, lastEventSeq), lastEventSeq, sink)
		reportStatus(current)
		switch current.Status {
		case domain.RunnerTaskSuccess, domain.RunnerTaskFailed, domain.RunnerTaskCanceled:
			e.flushAllObservations()
			return current, nil
		}
		changed, unsubscribe := e.subscribeRunnerTask(taskID)
		// Re-read after subscribing to close the check/subscribe race.
		current, ok = e.repo.RunnerTask(taskID)
		if !ok {
			unsubscribe()
			return domain.RunnerTask{}, fmt.Errorf("runner task %s disappeared", taskID)
		}
		lastEventSeq = bridgeRunnerEventsToSink(current, e.repo.RunnerTaskEventsAfter(taskID, lastEventSeq), lastEventSeq, sink)
		reportStatus(current)
		switch current.Status {
		case domain.RunnerTaskSuccess, domain.RunnerTaskFailed, domain.RunnerTaskCanceled:
			unsubscribe()
			e.flushAllObservations()
			return current, nil
		}
		// Runner events and completion are written through the API process in
		// production, while this await loop belongs to the worker process. Poll
		// the shared durable queue frequently enough to preserve live telemetry;
		// the in-process subscription still provides an immediate fast path.
		fallback := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			fallback.Stop()
			unsubscribe()
			_ = e.repo.CancelRunnerTask(taskID, time.Now().UTC())
			return domain.RunnerTask{}, ctx.Err()
		case <-changed:
		case <-fallback.C:
		}
		fallback.Stop()
		unsubscribe()
	}
}

// bridgeRunnerEventsToSink normalizes durable runner events in the process
// that owns the Run lease. The API process must not mutate a worker-owned Run:
// doing so is rejected by updateRun and previously made remote observability
// silently disappear even though runner_task_events contained the full stream.
func bridgeRunnerEventsToSink(task domain.RunnerTask, events []domain.RunnerTaskEvent, after int64, sink runtime.EventSink) int64 {
	if sink == nil {
		for _, event := range events {
			if event.Seq > after {
				after = event.Seq
			}
		}
		return after
	}
	for _, event := range events {
		if event.Seq <= after {
			continue
		}
		after = event.Seq
		if event.Type == "observation" {
			var observation domain.TelemetryEvent
			if json.Unmarshal([]byte(event.Data), &observation) != nil {
				continue
			}
			observation.Timestamp = event.Ts
			if observation.Source.Layer == "" {
				observation.Source.Layer = "executor"
			}
			if observation.Source.Adapter == "" {
				observation.Source.Adapter = "runner-v1"
			}
			if observation.Source.Provider == "" {
				observation.Source.Provider = task.Provider
			}
			if observation.Provider == "" {
				observation.Provider = task.Provider
			}
			sink(observation)
			continue
		}
		if event.Type != "stdout" && event.Type != "stderr" {
			continue
		}
		// Invocation adapters already normalize stderr. Valid stream-json/jsonl
		// stdout frames are retained only in runner_task_events to avoid a second,
		// noisy copy beside their normalized observations.
		if task.Invocation != nil {
			if event.Type == "stderr" {
				continue
			}
			protocol := strings.ToLower(strings.TrimSpace(task.Invocation.Executor.Protocol))
			if (protocol == "jsonl" || protocol == "stream-json") && json.Valid([]byte(event.Data)) {
				continue
			}
		}
		level := "info"
		if event.Type == "stderr" {
			level = "error"
		}
		sink(domain.TelemetryEvent{
			Timestamp: event.Ts,
			Category:  "process",
			Name:      "process." + event.Type,
			Phase:     "progress",
			Level:     level,
			Message:   event.Data,
			Provider:  task.Provider,
			Source:    domain.TelemetrySource{Layer: "executor", Adapter: "runner-v1", Provider: task.Provider},
		})
	}
	return after
}

// requireRunnerProvider validates that the bound runner is online and has the
// executor's CLI enabled, so a misbound role fails fast with a clear message
// instead of queueing a task nobody can claim.
func (e *Engine) requireRunnerProvider(runnerID, provider string) error {
	runner, ok := e.repo.Runner(strings.TrimSpace(runnerID))
	if !ok {
		return fmt.Errorf("执行器 %s 不存在：请到「个人 → 执行器」页面重新绑定", runnerID)
	}
	if runner.Status != domain.RunnerStatusOnline {
		return fmt.Errorf("执行器 %s（%s）当前离线，无法路由 %s 任务", runnerID, runner.Name, provider)
	}
	if !runner.SupportsProvider(provider) {
		return fmt.Errorf("执行器 %s（%s）未启用 %s CLI：请在执行器页面开启或改用云端 API Key 模式", runnerID, runner.Name, provider)
	}
	return nil
}

// restoreRunnerArtifacts writes artifact content uploaded by the runner into
// the control-plane run sandbox workspace so collectArtifacts, verification
// gates, and downstream handoff keep working unchanged.
func restoreRunnerArtifacts(sandboxWorkspace string, contracts []domain.ArtifactContract, uploaded map[string]string) error {
	if len(contracts) == 0 || len(uploaded) == 0 {
		return nil
	}
	byName := make(map[string]domain.ArtifactContract, len(contracts))
	for _, contract := range contracts {
		byName[contract.Name] = contract
	}
	for name, content := range uploaded {
		contract, ok := byName[name]
		if !ok || strings.TrimSpace(contract.Path) == "" {
			continue
		}
		target := filepath.Join(sandboxWorkspace, filepath.FromSlash(contract.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
