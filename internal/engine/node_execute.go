package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

func (e *Engine) execute(ctx context.Context, runID string) {
	r, ok := e.repo.RunState(runID)
	if !ok || r.Status == "queued" || r.AdmissionState == "unstarted" {
		e.clearRunning(runID)
		return
	}
	if !e.ownsRun(r) {
		status, msg, done := settleStatusForCompletedGraph(workflowForRun(e.repo, r), r)
		if done && !(status == "succeeded" && scheduledIntegratePending(r)) {
			e.finishAt(runID, r.Generation, status, msg)
		}
		e.clearRunning(runID)
		return
	}
	generation := r.Generation
	ctx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		interval := e.leaseTTL / 3
		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := e.renewRunLease(runID, generation); err != nil {
					cancel()
					return
				}
				if e.runControlRequested(runID) {
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-heartbeatDone
		_ = e.repo.ReleaseRunLease(runID, e.instanceID, generation)
		e.mu.Lock()
		delete(e.running, runID)
		e.mu.Unlock()
	}()
	var groupWorkers sync.WaitGroup
	defer func() { cancel(); groupWorkers.Wait() }()
	launched := map[string]bool{}
	groupDone := make(chan string, 8)
	for {
		select {
		case <-ctx.Done():
			e.finishInterruptedAt(runID, generation)
			return
		default:
		}
		r, ok := e.repo.RunState(runID)
		if !ok || isTerminalRunStatus(r.Status) {
			return
		}
		if r.CancelRequested {
			e.finishAt(runID, generation, "cancelled", "流水线已取消")
			return
		}
		if r.PauseRequested {
			e.pauseAt(runID, generation, "运行已由用户暂停")
			return
		}
		if r.Generation != generation || !e.ownsRun(r) {
			return
		}
		wf := workflowForRun(e.repo, r)
		ready := readyNodes(wf, r)
		if wf.CollaborationMode == "group" {
		drainDone:
			for {
				select {
				case id := <-groupDone:
					delete(launched, id)
				default:
					break drainDone
				}
			}
			if len(ready) == 0 && !hasRunningNode(r) {
				idle := false
				e.updateRun(runID, func(current *domain.Run) {
					if len(readyNodes(current.WorkflowSnapshot, *current)) == 0 && !hasRunningNode(*current) {
						current.Status = "waiting"
						idle = true
					}
				})
				if idle {
					return
				}
			}
			for _, n := range ready {
				if launched[n.ID] {
					continue
				}
				launched[n.ID] = true
				groupWorkers.Add(1)
				go func(node domain.WorkflowNode) {
					defer groupWorkers.Done()
					e.executeNode(ctx, runID, wf, node)
					select {
					case groupDone <- node.ID:
					case <-ctx.Done():
					}
				}(n)
			}
			time.Sleep(80 * time.Millisecond)
			continue
		}
		if len(ready) == 0 {
			if allSucceeded(wf, r) {
				rework, err := e.integrateScheduledRun(runID)
				if err != nil {
					e.finishAt(runID, generation, "failed", "合入 integration 失败："+err.Error())
					return
				}
				if rework {
					continue
				}
				e.finishAt(runID, generation, "succeeded", "流水线执行完成")
				return
			}
			if hasWaiting(wf, r) {
				if alignOnlyRun(r) {
					e.updateRun(runID, func(current *domain.Run) {
						if autoSucceedAlignApprovals(current, time.Now().UTC()) {
							e.event(current, "", "gate.auto_approved", "基线对齐无需人工确认，已自动通过")
						}
					})
					continue
				}
				e.updateRun(runID, func(current *domain.Run) { current.Status = "waiting" })
				return
			}
			if hasFailed(wf, r) {
				e.finishAt(runID, generation, "failed", pipelineFailedMessage)
				return
			}
			time.Sleep(80 * time.Millisecond)
			continue
		}
		var wg sync.WaitGroup
		for _, node := range ready {
			node := node
			wg.Add(1)
			go func() { defer wg.Done(); e.executeNode(ctx, runID, wf, node) }()
		}
		wg.Wait()
	}
}

func resolveNodeExecutor(st *repo.Store, node domain.WorkflowNode, agent domain.AgentDefinition) (domain.ExecutorDefinition, error) {
	executorID := agent.ExecutorID
	if executorID == "" {
		executorID = node.ExecutorID
	}
	if executorID == "" {
		executorID = "codebuddy"
	}
	ex, found := repo.BuiltInExecutor(executorID)
	if !found && executorID == "mock" {
		// The deterministic mock is reserved for internal tests and old run snapshots.
		ex, found = st.Executor(executorID)
	}
	if !found {
		// Before executors became a fixed catalog, Agents and workflow snapshots
		// persisted arbitrary executor IDs. Resolve their provider kind to the
		// corresponding built-in CLI while the one-time database migration is
		// being rolled out; never reuse the legacy binary or arguments.
		if legacy, ok := st.Executor(executorID); ok {
			ex, found = repo.BuiltInExecutorForKind(legacy.Kind)
			if found && strings.TrimSpace(agent.Model) == "" && strings.TrimSpace(legacy.Model) != "" {
				ex.Model = legacy.Model
			}
		}
	}
	if !found {
		return domain.ExecutorDefinition{}, fmt.Errorf("executor %q not found (agent %s)", executorID, agent.ID)
	}
	if strings.TrimSpace(agent.Model) != "" {
		ex.Model = agent.Model
	}
	if strings.TrimSpace(agent.Effort) != "" {
		ex.Effort = agent.Effort
	}
	return ex, nil
}

func workflowSandboxRequirements(st *repo.Store, wf domain.WorkflowDefinition) (runtime.SandboxRequirements, error) {
	var requirements []runtime.SandboxRequirements
	for _, node := range wf.Nodes {
		if node.Type == "script" || node.Type == "command" || node.Type == "approval" || node.Type == "sop" {
			continue
		}
		agent, ok := workflowAgent(st, wf, node.AgentID)
		if !ok {
			return runtime.SandboxRequirements{}, fmt.Errorf("agent %q not found", node.AgentID)
		}
		executor, err := resolveNodeExecutor(st, node, agent)
		if err != nil {
			return runtime.SandboxRequirements{}, err
		}
		requirements = append(requirements, runtime.ProviderRequirements(executor.Kind))
	}
	return runtime.MergeSandboxRequirements(requirements...), nil
}

func dockerImageBuildLabel(snap *domain.RunnerSandboxSpec) string {
	if snap != nil && snap.Dockerfile != nil {
		if name := strings.TrimSpace(snap.Dockerfile.Name); name != "" {
			return name
		}
		if id := strings.TrimSpace(snap.Dockerfile.ID); id != "" {
			return id
		}
	}
	return "沙箱镜像"
}

func dockerImageNeedsBuild(ctx context.Context, snap *domain.RunnerSandboxSpec) bool {
	if snap == nil || snap.Dockerfile == nil || strings.TrimSpace(snap.Dockerfile.Content) == "" {
		return true
	}
	return !runtime.DockerImageCached(ctx, *snap.Dockerfile)
}

func formatElapsedCN(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d.Round(time.Second) / time.Second)
	if sec < 60 {
		return fmt.Sprintf("%d 秒", sec)
	}
	min := sec / 60
	rem := sec % 60
	if rem == 0 {
		return fmt.Sprintf("%d 分钟", min)
	}
	return fmt.Sprintf("%d 分 %d 秒", min, rem)
}

func (e *Engine) bindDockerWorkspace(ctx context.Context, runID, nodeID string, snap *domain.RunnerSandboxSpec, sb *runEnvironment) error {
	building := dockerImageNeedsBuild(ctx, snap)
	if building {
		e.observe(runID, nodeID, domain.TelemetryEvent{
			Category:   "sandbox",
			Name:       "sandbox.image.building",
			Phase:      "start",
			Message:    fmt.Sprintf("首次运行：正在构建沙箱镜像（%s），通常需要数分钟，完成后才会开始执行", dockerImageBuildLabel(snap)),
			Attributes: map[string]any{"dockerfileId": dockerfileID(snap)},
		})
		e.flushAllObservations()
	}
	started := time.Now()
	if err := sb.BindWorkspace(ctx); err != nil {
		return err
	}
	if building {
		e.observe(runID, nodeID, domain.TelemetryEvent{
			Category:   "sandbox",
			Name:       "sandbox.image.ready",
			Phase:      "finish",
			Message:    fmt.Sprintf("沙箱镜像已就绪，耗时 %s", formatElapsedCN(time.Since(started))),
			Attributes: map[string]any{"durationMs": time.Since(started).Milliseconds(), "dockerfileId": dockerfileID(snap)},
		})
		e.flushAllObservations()
	}
	return nil
}

func dockerfileID(snap *domain.RunnerSandboxSpec) string {
	if snap == nil || snap.Dockerfile == nil {
		return ""
	}
	return snap.Dockerfile.ID
}

func (e *Engine) executeNode(ctx context.Context, runID string, wf domain.WorkflowDefinition, node domain.WorkflowNode) {
	currentLease, ok := e.repo.RunState(runID)
	if !ok || !e.ownsRun(currentLease) {
		return
	}
	if wf.CollaborationMode != "group" {
		unlock := e.lockRunExec(runID)
		defer unlock()
	}
	executionGeneration := currentLease.Generation
	executionAttempt := 0
	started := false
	e.updateRun(runID, func(r *domain.Run) {
		nr := r.Nodes[node.ID]
		if nr.Status != "pending" {
			return
		}
		if nr.Error != "" && runtime.IsCwdMountFailure(errors.New(nr.Error)) {
			nr.Status = "failed"
			e.event(r, node.ID, "node.failed", nr.Error)
			return
		}
		started = true
		r.Status = "running"
		nr.Status = "running"
		if !continuingInterruptedAttempt(nr) {
			nr.Attempt++
		}
		executionAttempt = nr.Attempt
		nr.StartedAt = time.Now().UTC()
		nr.GateResult = nil
		e.event(r, node.ID, "node.started", node.Name+" 开始执行")
	})
	if !started {
		return
	}
	failNode := func(err error, allowReject bool) {
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if runtime.IsSandboxInterrupt(err) {
			e.requeueInterruptedNode(runID, node.ID, executionAttempt, executionGeneration)
			return
		}
		e.nodeFailedAt(runID, node, executionAttempt, executionGeneration, err, allowReject)
	}
	if node.Type == "sop" {
		e.executeSOPNode(ctx, runID, wf, node, executionAttempt, executionGeneration)
		return
	}
	if node.Type == "approval" {
		auto := false
		e.updateRun(runID, func(r *domain.Run) {
			if r.SandboxID != "" {
				r.Nodes[node.ID].SandboxID = r.SandboxID
			}
			if !alignOnlyRun(*r) {
				return
			}
			nr := r.Nodes[node.ID]
			nr.Status = "succeeded"
			nr.FinishedAt = time.Now().UTC()
			e.event(r, node.ID, "gate.auto_approved", "基线对齐无需人工确认，已自动通过")
			auto = true
		})
		if auto {
			return
		}
		e.waitApproval(runID, node, "等待人工批准")
		return
	}
	current, _ := e.repo.RunState(runID)
	current, err := e.bindPoolRunner(ctx, current, node.ID)
	if err != nil {
		failNode(err, false)
		return
	}
	profile := domain.SandboxProfile{}
	if current.WorkflowSnapshot.SandboxSnapshot != nil {
		profile = current.WorkflowSnapshot.SandboxSnapshot.Profile
	} else {
		var found bool
		profile, found = e.repo.Sandbox(wf.SandboxProfileID)
		if !found {
			failNode(fmt.Errorf("sandbox profile %q not found", wf.SandboxProfileID), nodeRejects(node))
			return
		}
	}
	requirements, err := workflowSandboxRequirements(e.repo, wf)
	if err != nil {
		failNode(err, nodeRejects(node))
		return
	}
	acquireProfile := profile
	// A selected execution machine owns the real process sandbox. The control
	// plane keeps only a local staging workspace for prompt/materialization and
	// artifact handoff; creating Docker here would put isolation on the wrong
	// host and leave the runner CLI outside it.
	if e.defaultExecutionRunner(current) != "" {
		acquireProfile = domain.SandboxProfile{ID: profile.ID + "-runner-staging", Name: profile.Name + " staging", Driver: "local"}
	}
	sb, created, err := e.acquireRunEnvironment(ctx, current, acquireProfile, requirements)
	if err != nil {
		failNode(err, nodeRejects(node))
		return
	}
	if created {
		e.observe(runID, node.ID, domain.TelemetryEvent{Category: "sandbox", Name: "sandbox.created", Phase: "start", Message: sb.ID(), Attributes: map[string]any{
			"profileId":     profile.ID,
			"driver":        profile.Driver,
			"instanceId":    e.instanceID,
			"workspaceHost": e.workspaceHost,
			"workspaceRoot": e.sandboxes.Root(),
		}})
	}
	e.updateRun(runID, func(r *domain.Run) {
		r.SandboxID = sb.ID()
		// Retain the node field while old clients migrate; every node references
		// the same run-owned sandbox.
		r.Nodes[node.ID].SandboxID = sb.ID()
	})
	sb.nodeMu.Lock()
	preparing := true
	defer func() {
		if preparing {
			sb.nodeMu.Unlock()
		}
	}()
	sink := func(event domain.TelemetryEvent) { e.observe(runID, node.ID, event) }
	// gateExec runs a verification-gate command. In cloud mode it executes in
	// the control-plane sandbox; when the Agent is bound to a CLI Runner
	// (执行器) it is dispatched to that machine so gates run next to the CLI
	// output in the runner-side task workspace.
	gateExec := func(command string, target executionTarget, gateSink runtime.EventSink) (string, error) {
		return sb.Exec(ctx, runtime.CommandSpec{Argv: []string{"sh", "-lc", bindExecutionTarget(command, target)}, Provider: "verification", CredentialBindings: []runtime.CredentialBinding{runtime.BindGit, runtime.BindMCP}}, gateSink)
	}
	current, _ = e.repo.RunState(runID)
	gitTok := sb.credentialSnapshot().GitToken
	ctx = withGitToken(ctx, gitTok)
	ctx = withGitIdentity(ctx, sb.credentialSnapshot().GitUserName, sb.credentialSnapshot().GitUserEmail)
	// Repository checkout and Agent-configuration index isolation belong to the
	// run environment, not to each node. The same checkout and .git/index are
	// reused by all nodes, so repeatedly toggling skip-worktree only creates
	// needless index.lock contention. A reconstructed runEnvironment (restart or
	// lease takeover) starts unprepared and revalidates the persisted checkout.
	// A runner that integrates on its own checkout needs nothing from a
	// control-plane clone, so none is made.
	stagingCheckout := true
	if runner, ok := e.repo.Runner(e.defaultExecutionRunner(current)); ok && runner.SupportsIntegrate() {
		stagingCheckout = false
	}
	if err := sb.prepareRepositoriesOnce(func() error {
		if !stagingCheckout {
			return nil
		}
		repoWorkspaces := repositoryWorkspaceDirs(sb.Workspace(), current.RepositoryContexts())
		for _, dir := range repoWorkspaces {
			if err := runtime.RestoreProjectAgentGitIndex(ctx, dir, sb.Driver()); err != nil {
				return fmt.Errorf("restore repository Agent configuration index: %w", err)
			}
		}
		if err := prepareRepositories(ctx, sb.Workspace(), current.RepositoryContexts()); err != nil {
			return fmt.Errorf("repository checkout: %w", err)
		}
		for _, dir := range repoWorkspaces {
			if err := runtime.ProtectProjectAgentGitIndex(ctx, dir, sb.Driver()); err != nil {
				return fmt.Errorf("protect repository Agent configuration index: %w", err)
			}
		}
		for _, rc := range current.RepositoryContexts() {
			e.observe(runID, node.ID, domain.TelemetryEvent{Category: "sandbox", Name: "repository.checked_out", Phase: "finish", Message: rc.Name + " @ " + rc.WorkBranch, Attributes: map[string]any{"repositoryId": rc.RepositoryID, "repositoryDir": rc.WorkspaceDir, "baseBranch": rc.BaseBranch, "workBranch": rc.WorkBranch}})
		}
		return nil
	}); err != nil {
		failNode(err, nodeRejects(node))
		return
	}
	if err := e.pullGroupOrSOPWorkBranches(ctx, runID, node, sb.Workspace(), current); err != nil {
		failNode(fmt.Errorf("同步工作分支失败: %w", err), false)
		return
	}
	// An execution machine that reports its outputs receives the inputs and
	// human context with each task and returns the files the engine needs;
	// the control-plane workspace is then not read for this node.
	if err := validateSOPHandoffArtifacts(current); err != nil {
		failNode(err, false)
		return
	}
	var reported *reportedOutputs
	artifactContext := ""
	if runner, ok := e.repo.Runner(e.defaultExecutionRunner(current)); ok && runner.SupportsReportedOutputs() {
		var inputs []domain.RunnerInputFile
		artifactContext, inputs = runnerArtifactInputs(wf, node, current)
		reported = sb.beginReportedOutputs(node.ID, inputs)
		defer sb.endReportedOutputs(node.ID)
	} else {
		if err := runtime.EnsureControlRoot(sb.Workspace()); err != nil {
			failNode(fmt.Errorf("prepare AgentFlow control root: %w", err), nodeRejects(node))
			return
		}
		artifactContext, err = materializeInputs(sb.Workspace(), wf, node, current)
		if err != nil {
			failNode(fmt.Errorf("artifact handoff: %w", err), nodeRejects(node))
			return
		}
		if err := writeHumanContextFile(sb.Workspace(), current.HumanContext); err != nil {
			failNode(fmt.Errorf("write human context: %w", err), nodeRejects(node))
			return
		}
	}
	if artifactContext != "" {
		e.observe(runID, node.ID, domain.TelemetryEvent{Category: "artifact", Name: "artifact.inputs.materialized", Phase: "finish", Message: artifactContext})
	}
	if reported == nil {
		if _, err := runtime.RestoreMissingWorkspaceArtifacts(sb.Workspace(), e.legacyArchivedWorkspaceFiles(current, node.ID)); err != nil {
			failNode(fmt.Errorf("restore archived outputs: %w", err), nodeRejects(node))
			return
		}
	}
	var outputs nodeOutputs = workspaceOutputs(sb.Workspace())
	retroWorkspace := sb.Workspace()
	if reported != nil {
		outputs = reported
		retroWorkspace = ""
	}
	if strings.EqualFold(sb.Driver(), "docker") {
		if err := e.bindDockerWorkspace(ctx, runID, node.ID, current.WorkflowSnapshot.SandboxSnapshot, sb); err != nil {
			failNode(fmt.Errorf("bind docker workspace after checkout: %w", err), nodeRejects(node))
			return
		}
	}
	if domain.IsPreviewBuildNode(node.ID) {
		found, evidence := false, ""
		if facts, ok := e.RunWorkspaceFacts(runID); reported != nil && ok {
			found, evidence = facts.Frontend, facts.FrontendEvidence
		} else if reported != nil {
			// No node has reported the runner checkout yet; let the build decide.
			found = true
		} else {
			found, evidence = domain.DetectFrontendProject(sb.Workspace())
		}
		if !found {
			e.completeSkippedPreviewBuild(runID, node, executionGeneration, executionAttempt, current, evidence)
			return
		}
	}
	_, preparationErr := e.PrepareNodeWorkspace(ctx, current, sb)
	if preparationErr != nil {
		failNode(preparationErr, nodeRejects(node))
		return
	}
	if wf.CollaborationMode == "group" {
		sb.activeExecutions.Add(1)
		defer sb.activeExecutions.Add(-1)
		sb.nodeMu.Unlock()
		preparing = false
	}
	var output string
	defaultFailure := "fail"
	if nodeRejects(node) {
		defaultFailure = "reject"
	}
	gates := make([]verificationRun, 0)
	effectiveNode := node
	maxIterations := 1
	loopOnExhausted := "fail"
	loopFeedback := ""
	invocationSeq := 0
	requiresSubmit := false
	var runOnce func(string) (string, error)
	switch node.Type {
	case "script", "command":
		runOnce = func(string) (string, error) {

			if reported != nil {
				reported.reset()
			}
			script := node.Script
			language := node.ScriptLanguage
			if node.Type == "command" {
				script = node.Command
				language = "shell"
			}
			started := time.Now()
			command := script
			if language == "python" {
				command = "python3 -c " + shellSingleQuote(script)
			}
			rawOutput, runErr := executeForTargets(current, node.ExecutionScope, func(target executionTarget) (string, error) {
				bound := bindExecutionTarget(command, target)
				targetSink := executionTargetSink(sink, target)
				scriptAssets := e.resolveScriptMCPAssets(current.ExecutionContext.ProjectID, current.ExecutionContext.ResponsibleUserID)
				if execMachine := e.defaultExecutionRunner(current); execMachine != "" {
					workspace := runnerWorkspaceRel(current, node.ID)
					return e.dispatchRunnerScript(ctx, execMachine, current, node.ID, workspace, "shell", bound, target.directory(), effectiveNode.ArtifactOutputs, sb, sb.credentialSnapshot().GitToken, sb.credentialSnapshot().GitUserName, sb.credentialSnapshot().GitUserEmail, sb.credentialSnapshot().TaihuPAT, scriptAssets, targetSink)
				}
				if err := runtime.ProvisionMCPAssets(ctx, sb, sb.Workspace(), scriptAssets, targetSink); err != nil {
					return "", err
				}
				return sb.Exec(ctx, runtime.CommandSpec{Concurrent: wf.CollaborationMode == "group", Argv: []string{"sh", "-lc", bound}, Provider: "script", CredentialBindings: []runtime.CredentialBinding{runtime.BindGit, runtime.BindMCP}}, targetSink)
			})
			if execMachine := e.defaultExecutionRunner(current); execMachine != "" {
				workspace := runnerWorkspaceRel(current, node.ID)
				gateExec = func(command string, target executionTarget, gateSink runtime.EventSink) (string, error) {
					return e.dispatchRunnerCommand(ctx, execMachine, runID, node.ID, workspace, bindExecutionTarget(command, target), gateSink)
				}
			}
			if runErr == nil && node.RunOnWorkspaceRebuild {
				if recordErr := e.recordNodeWorkspacePreparation(ctx, current, sb, node, rawOutput); recordErr != nil {
					e.AppendRunEvent(runID, "workspace.preparation.record_failed", "脚本 "+node.Name+" 已正常完成，但工作区重建记录保存失败："+recordErr.Error()+"；不会自动重试已完成脚本")
				}
			}
			duration := time.Since(started)
			status, level := "成功", ""
			if runErr != nil {
				status, level = "失败", "error"
			}
			sink(domain.TelemetryEvent{
				Category: "script", Name: "script.completed", Phase: "finish", Level: level,
				Message:    fmt.Sprintf("%s 脚本执行%s · %d ms · 输出 %d bytes", scriptLanguageLabel(language), status, duration.Milliseconds(), len(rawOutput)),
				Attributes: map[string]any{"language": language, "status": status, "durationMs": duration.Milliseconds(), "outputBytes": len(rawOutput)},
			})
			if strings.TrimSpace(rawOutput) == "" {
				rawOutput = fmt.Sprintf("# 脚本执行结果\n\n- 语言：%s\n- 状态：%s\n- 耗时：%d ms\n- 标准输出：无\n", scriptLanguageLabel(language), status, duration.Milliseconds())
			}
			return rawOutput, runErr
		}
	default:
		requiresSubmit = true
		agent, found := workflowAgent(e.repo, wf, node.AgentID)
		if !found {
			err = fmt.Errorf("agent %q not found", node.AgentID)
			break
		}
		ex, errResolve := resolveNodeExecutor(e.repo, node, agent)
		if errResolve != nil {
			err = errResolve
			break
		}
		effectiveNode.ArtifactOutputs = mergeArtifactContracts(agent.ArtifactOutputs, node.ArtifactOutputs)
		gates = gates[:0]
		for _, gate := range agent.Verification {
			gates = append(gates, verificationRun{gate: gate, onFailure: resolveGateOnFailure(gate.OnFailure, defaultFailure)})
		}
		for _, point := range agent.Interventions {
			if point.Required && (point.Stage == "after_execution" || point.Stage == "before_publish") {
				gates = append(gates, verificationRun{gate: domain.VerificationGate{ID: point.ID, Name: point.Name, Mode: "manual", Stage: point.Stage, Required: true}, onFailure: defaultFailure})
			}
		}
		if agent.Loop.Enabled {
			maxIterations = max(1, agent.Loop.MaxIterations)
			loopOnExhausted = agent.Loop.OnExhausted
		}
		runOnce = func(feedback string) (string, error) {
			if reported != nil {
				reported.reset()
			}
			current, _ := e.repo.RunState(runID)
			invocationSeq++
			attempt := 1
			resumeSessionID := ""
			resumeFeedback := ""
			if nr := current.Nodes[node.ID]; nr != nil {
				if nr.Attempt > 0 {
					attempt = nr.Attempt
				}
				resumeSessionID = strings.TrimSpace(nr.ResumeSessionID)
				resumeFeedback = strings.TrimSpace(nr.Feedback)
				if feedback == gateMissingRetryMarker {
					resumeSessionID = strings.TrimSpace(nr.SessionID)
				}
			}
			invocationID := fmt.Sprintf("%s-%d-%d", node.ID, attempt, invocationSeq)
			rendered := renderPromptWithStore(e.repo, wf, effectiveNode, current)
			hadCatalog := strings.Contains(rendered, "{{artifacts}}")
			prompt := strings.ReplaceAll(rendered, "{{artifacts}}", artifactContext)
			if !hadCatalog && artifactContext != "" {
				prompt += "\n\n# Upstream artifacts\n\n" + artifactContext
			}
			prompt = appendArtifactContract(prompt, effectiveNode, current.RepositoryContexts())
			if feedback != "" && feedback != gateMissingRetryMarker {
				template := agent.Loop.FeedbackPrompt
				if template == "" {
					template = "上一轮未通过 Agent 门禁。请根据以下反馈在同一工作区内修正，只重做失败部分：\n{{feedback}}"
				}
				prompt += "\n\n" + strings.ReplaceAll(template, "{{feedback}}", feedback)
			}
			if resumeSessionID != "" {
				prompt = "请从刚才中断的位置继续执行。"
				if resumeFeedback != "" {
					prompt += "\n\n操作员补充了以下信息，请结合它继续：\n\n" + resumeFeedback
				}
			}
			if requiresSubmit {
				prompt = appendSubmitGateInstruction(prompt, node.RejectTo, feedback == gateMissingRetryMarker)
			}
			assets := e.resolveAssets(agent, current.ExecutionContext.ProjectID, wf.ID, current.ExecutionContext.ResponsibleUserID)
			if wf.CollaborationMode == "group" {
				assets.MCPServers = append(assets.MCPServers, e.groupMCPServer(current, node, attempt))
				prompt += e.groupTaskContext(current, node)
			}
			warnMCPSkipped := func(message string) {
				sink(domain.TelemetryEvent{Category: "mcp", Name: "mcp.skipped", Phase: "start", Level: "warn", Message: message})
			}
			if warning := knowledgeSkippedWarning(agent, assets); warning != "" {
				warnMCPSkipped(warning)
			}
			options := runtime.InvocationOptions{Concurrent: wf.CollaborationMode == "group", RunID: runID, NodeID: node.ID, ResumeSessionID: resumeSessionID, Repositories: current.RepositoryContexts()}
			if requiresSubmit {
				options.GateResultPath = gateResultPath(node.ID, attempt)
			}
			if execMachine := e.defaultExecutionRunner(current); execMachine != "" {
				// 执行机器 mode (bind-cli): the responsible user selected a default
				// execution machine, so the compiled task book, asset bundle, and
				// repository checkout are dispatched there; Codex / Claude Code use
				// that machine's own CLI login, CodeBuddy uses the responsible
				// user's platform API key. Gates follow on the same machine and
				// workspace. An empty selection keeps the cloud mode below.
				workspace := runnerWorkspaceRel(current, node.ID)
				result, dispatchErr := e.dispatchRunnerTask(ctx, execMachine, current, effectiveNode, sb, workspace, ex, agent, assets, prompt, invocationID, options, sink)
				if dispatchErr == nil {
					gateExec = func(command string, target executionTarget, gateSink runtime.EventSink) (string, error) {
						return e.dispatchRunnerCommand(ctx, execMachine, runID, node.ID, workspace, bindExecutionTarget(command, target), gateSink)
					}
				}
				return result, dispatchErr
			}
			boundAssets, skipped, bindErr := runtime.PrepareMCPServers(assets, runtime.MCPTarget{TaihuPAT: sb.credentialSnapshot().TaihuPAT, GitToken: sb.credentialSnapshot().GitToken, RequireTaihuPAT: true})
			for _, warning := range skipped {
				warnMCPSkipped(warning)
			}
			if bindErr != nil {
				return "", bindErr
			}
			return e.runner.Run(ctx, sb, invocationID, ex, agent, boundAssets, prompt, options, sink)
		}
	}
	if err != nil {
		failNode(err, nodeRejects(node))
		return
	}
	gateRejected := false
	failedAction := ""
	lastIteration := 0
	submitPath := ""
	if requiresSubmit {
		submitPath = gateResultPath(node.ID, executionAttempt)
	}
	for iteration := 1; iteration <= maxIterations; iteration++ {
		lastIteration = iteration
		if requiresSubmit {
			e.updateRun(runID, func(r *domain.Run) {
				if nr := r.Nodes[node.ID]; nr != nil {
					nr.GateResult = nil
				}
			})
			if reported == nil {
				if clearErr := clearGateResult(sb.Workspace(), submitPath); clearErr != nil {
					err = fmt.Errorf("clear submit_gate_result: %w", clearErr)
					failedAction = "human"
					break
				}
			}
		}
		output, err = runOnce(loopFeedback)
		if err != nil && strings.Contains(err.Error(), "without a terminal result") {
			e.observe(runID, node.ID, domain.TelemetryEvent{Category: "orchestration", Name: "agent.loop", Phase: "finish", Message: "执行器未返回 result，按工作区产物继续门禁，不再重跑 Agent", Attributes: map[string]any{"iteration": iteration}})
			err = nil
		}
		var submitted *domain.GateResult
		if err == nil && requiresSubmit {
			submitted, err = readGateResultFrom(outputs, submitPath)
			if err != nil || submitted == nil {
				firstProblem := "未调用 submit_gate_result"
				if err != nil {
					firstProblem = err.Error()
					if reported == nil {
						_ = clearGateResult(sb.Workspace(), submitPath)
					}
				}
				e.flushAllObservations()
				resumable, _ := e.repo.RunState(runID)
				sessionID := ""
				if nr := resumable.Nodes[node.ID]; nr != nil {
					sessionID = strings.TrimSpace(nr.SessionID)
				}
				if sessionID == "" {
					err = fmt.Errorf("%s，且执行器没有返回可恢复的 CLI session ID", firstProblem)
				} else {
					e.observe(runID, node.ID, domain.TelemetryEvent{Category: "verification", Name: "gate.submit.missing", Phase: "progress", Level: "warn", Message: firstProblem + "，恢复同一 CLI session 要求重新提交", Attributes: map[string]any{"iteration": iteration, "sessionId": sessionID}})
					output, err = runOnce(gateMissingRetryMarker)
					if err == nil {
						submitted, err = readGateResultFrom(outputs, submitPath)
					}
				}
			}
			if err == nil && submitted == nil {
				err = fmt.Errorf("未收到 submit_gate_result，恢复 CLI session 后仍未提交")
			}
			if err != nil {
				failedAction = "human"
			} else {
				e.updateRun(runID, func(r *domain.Run) {
					if nr := r.Nodes[node.ID]; nr != nil {
						copy := *submitted
						nr.GateResult = &copy
					}
				})
				e.observe(runID, node.ID, domain.TelemetryEvent{Category: "verification", Name: "gate.submit.received", Phase: "finish", Message: "收到 Agent 节点终态：" + submitted.Decision, Attributes: map[string]any{"decision": submitted.Decision, "reason": submitted.Reason}})
				err, failedAction, gateRejected = applyGateResult(submitted, node.RejectTo)
				if err != nil {
					// A terminal decision short-circuits artifact validation and
					// command gates below. Surface that explicitly so operators
					// can tell "objective evidence never ran" from "evidence ran
					// and failed" when auditing a failed or rejected node.
					skippedGates := 0
					for _, candidate := range gates {
						if strings.EqualFold(candidate.gate.Mode, "manual") || strings.EqualFold(candidate.gate.Mode, "submit") {
							continue
						}
						if joinGateCommands(candidate.gate.Commands, effectiveNode.ArtifactOutputs) == "" {
							continue
						}
						skippedGates++
					}
					if skippedGates > 0 || len(effectiveNode.ArtifactOutputs) > 0 {
						e.observe(runID, node.ID, domain.TelemetryEvent{Category: "verification", Name: "gate.terminal", Phase: "finish", Level: "warn", Message: fmt.Sprintf("decision=%s; skipped by terminal decision (command gates %d, artifact contracts %d)", submitted.Decision, skippedGates, len(effectiveNode.ArtifactOutputs)), Attributes: map[string]any{"decision": submitted.Decision, "skippedGates": skippedGates, "artifactContracts": len(effectiveNode.ArtifactOutputs)}})
					}
				}
			}
		}
		if err == nil {
			live, _ := e.repo.RunState(runID)
			var recovered []string
			if reported != nil {
				recovered = reported.takeRecovered()
			} else {
				recovered = recoverMisplacedArtifacts(sb.Workspace(), live.Repository.LocalPath, effectiveNode)
			}
			if len(recovered) > 0 {
				e.observe(runID, node.ID, domain.TelemetryEvent{Category: "artifact", Name: "artifact.recovered", Phase: "progress", Message: "从错误写入位置收回交付物：" + strings.Join(recovered, "；")})
			}
			if validateErr := validateArtifactOutputs(outputs, live, effectiveNode); validateErr != nil {
				err = fmt.Errorf("artifact contract: %w", validateErr)
			} else if len(effectiveNode.ArtifactOutputs) > 0 {
				e.observe(runID, node.ID, domain.TelemetryEvent{Category: "artifact", Name: "artifact.contract.validated", Phase: "finish", Message: "产物契约校验通过", Attributes: map[string]any{"contracts": len(effectiveNode.ArtifactOutputs), "iteration": iteration}})
			}
		}
		if err == nil {
			for _, candidate := range gates {
				gate := candidate.gate
				if strings.EqualFold(gate.Mode, "manual") || strings.EqualFold(gate.Mode, "submit") {
					continue
				}
				gateCmd := joinGateCommands(gate.Commands, effectiveNode.ArtifactOutputs)
				if gateCmd == "" {
					continue
				}
				gateSink := func(event domain.TelemetryEvent) {
					event.Category = "verification"
					if event.Attributes == nil {
						event.Attributes = map[string]any{}
					}
					event.Attributes["gateId"] = gate.ID
					event.Attributes["gateName"] = gate.Name
					event.Attributes["iteration"] = iteration
					event.Attributes["command"] = gateCmd
					switch event.Name {
					case "process.started":
						event.Message = "门禁开始：" + gate.Name
					case "process.finished":
						if event.Level == "error" {
							event.Message = "门禁未通过：" + gate.Name
						} else {
							event.Message = "门禁通过：" + gate.Name
						}
					}
					sink(event)
				}
				verifyOutput, verifyErr := executeForTargets(current, gate.ExecutionScope, func(target executionTarget) (string, error) {
					return gateExec(gateCmd, target, executionTargetSink(gateSink, target))
				})
				if verifyErr != nil {
					detail := strings.TrimSpace(verifyOutput)
					if len(detail) > 2_000 {
						detail = detail[len(detail)-2_000:]
					}
					if detail == "" {
						detail = "no command output"
					}
					err = fmt.Errorf("verification %s command %q: %w (%s)", gate.Name, gateCmd, verifyErr, detail)
					failedAction = candidate.onFailure
					gateRejected = failedAction == "reject"
				}
				if err != nil {
					break
				}
			}
		}
		if err == nil || iteration == maxIterations || ctx.Err() != nil {
			break
		}
		if failedAction != "loop" {
			e.observe(runID, node.ID, domain.TelemetryEvent{Category: "orchestration", Name: "agent.loop", Phase: "finish", Message: "门禁失败策略为 " + failedAction + "，跳过自修正", Attributes: map[string]any{"iteration": iteration, "onFailure": failedAction, "feedback": err.Error()}})
			break
		}
		loopFeedback = err.Error()
		e.observe(runID, node.ID, domain.TelemetryEvent{Category: "orchestration", Name: "agent.loop", Phase: "progress", Message: fmt.Sprintf("第 %d 轮未通过，进入自修正", iteration), Attributes: map[string]any{"iteration": iteration, "maxIterations": maxIterations, "feedback": loopFeedback}})
	}
	e.attachLoopRetro(runID, node.ID, retroWorkspace, lastIteration, err, failedAction)
	if err != nil {
		e.freezeAvailableArtifacts(runID, effectiveNode, sb.Workspace(), reported, output)
		if ctx.Err() != nil || runtime.IsSandboxInterrupt(err) {
			if ctx.Err() == nil {
				e.requeueInterruptedNode(runID, node.ID, executionAttempt, executionGeneration)
			}
			return
		}
		if isMaxTurnsErr(err) {
			e.updateRun(runID, func(r *domain.Run) {
				nr := r.Nodes[node.ID]
				nr.Output, nr.Error, nr.Status, nr.Activity, nr.FinishedAt = output, err.Error(), "waiting_approval", "对话轮次用尽，等待确认是否重试", time.Now().UTC()
				e.event(r, node.ID, "agent.turns.exhausted", "对话轮次已用尽，请确认是否重试本节点")
			})
			return
		}
		if failedAction == "human" {
			e.updateRun(runID, func(r *domain.Run) {
				nr := r.Nodes[node.ID]
				nr.Output, nr.Error, nr.Status, nr.Activity, nr.FinishedAt = output, err.Error(), "waiting_approval", "等待人工确认", time.Now().UTC()
				message := "Agent 请求人工确认"
				if nr.GateResult != nil && strings.TrimSpace(nr.GateResult.ActionRequired) != "" {
					message += "：" + nr.GateResult.ActionRequired
				}
				e.event(r, node.ID, "gate.waiting", message)
			})
			return
		}
		if maxIterations > 1 && loopOnExhausted == "human" {
			e.updateRun(runID, func(r *domain.Run) {
				nr := r.Nodes[node.ID]
				nr.Output, nr.Error, nr.Status, nr.FinishedAt = output, err.Error(), "waiting_approval", time.Now().UTC()
				e.event(r, node.ID, "agent.loop.exhausted", "Agent 自修正次数已耗尽，转人工判断："+err.Error())
			})
			return
		}
		failNode(err, failedAction == "reject" || (failedAction == "" && allowRejectOnFailure(node, err, gateRejected)))
		return
	}
	manualGateName := ""
	for _, candidate := range gates {
		gate := candidate.gate
		if gate.Mode == "manual" && gate.Required {
			if manualGateName == "" {
				manualGateName = gate.Name
			}
			continue
		}
	}
	current, _ = e.repo.RunState(runID)
	artifacts, err := collectArtifactsMode(outputs, current, effectiveNode, output, false)
	if err != nil {
		failNode(fmt.Errorf("artifact contract: %w", err), nodeRejects(node))
		return
	}
	if !shouldCommitNodeOutcome(current, current.Nodes[node.ID], executionGeneration, executionAttempt, true) {
		return
	}
	if err := e.syncWorkBranches(ctx, runID, effectiveNode, sb.Workspace(), current); err != nil {
		failNode(fmt.Errorf("推送工作分支失败: %w", err), false)
		return
	}
	e.updateRun(runID, func(r *domain.Run) {
		nr := r.Nodes[node.ID]
		if !shouldCommitNodeOutcome(*r, nr, executionGeneration, executionAttempt, true) {
			return
		}
		r.Artifacts = append(r.Artifacts, artifacts...)
		nr.Output = output
		nr.Error = ""
		if manualGateName != "" {
			nr.Status = "waiting_approval"
			nr.FinishedAt = time.Now().UTC()
			e.event(r, node.ID, "gate.waiting", "等待人工验证："+manualGateName)
		} else {
			nr.Status = "succeeded"
			nr.FinishedAt = time.Now().UTC()
			e.event(r, node.ID, "node.succeeded", node.Name+" 已完成")
		}
	})
	for _, artifact := range artifacts {
		e.observe(runID, node.ID, domain.TelemetryEvent{Category: "artifact", Name: "artifact.published", Phase: "finish", Message: artifact.Name, Attributes: map[string]any{"artifactId": artifact.ID, "path": artifact.Path, "mediaType": artifact.MediaType, "size": artifact.Size, "sha256": artifact.SHA256}})
	}
	if manualGateName != "" {
		return
	}
	if domain.IsPreviewBuildNode(node.ID) {
		e.archivePreviewAfterBuild(runID)
	}
}

// archivePreviewAfterBuild publishes the static site as soon as the build
// node succeeds. Later gates, including demo confirmation, can open it while
// the run is still waiting.
func (e *Engine) archivePreviewAfterBuild(runID string) {
	if e.preview == nil {
		return
	}
	current, ok := e.repo.RunState(runID)
	if !ok || !current.WorkflowSnapshot.Preview.Static() {
		return
	}
	nr := current.Nodes[domain.PreviewBuildNodeID]
	if nr == nil || nr.Status != "succeeded" {
		return
	}
	e.preview.ArchiveBuiltPreview(current)
}

func (e *Engine) completeSkippedPreviewBuild(runID string, node domain.WorkflowNode, executionGeneration, executionAttempt int, current domain.Run, evidence string) {
	reason := "工作区没有前端工程，已跳过预览"
	if extra := strings.TrimSpace(evidence); extra != "" {
		reason = reason + "（" + extra + "）"
	}
	if e.preview != nil {
		e.preview.Skip(current, reason)
	}
	output := "# 前端预览\n\n已跳过：工作区没有可构建的前端工程。业务交付不受影响。\n"
	if extra := strings.TrimSpace(evidence); extra != "" {
		output += "\n" + extra + "\n"
	}
	e.observe(runID, node.ID, domain.TelemetryEvent{
		Category: "preview", Name: "preview.skipped", Phase: "finish", Message: reason,
	})
	e.updateRun(runID, func(r *domain.Run) {
		nr := r.Nodes[node.ID]
		if !shouldCommitNodeOutcome(*r, nr, executionGeneration, executionAttempt, true) {
			return
		}
		nr.Output = output
		nr.Error = ""
		nr.Status = "succeeded"
		nr.FinishedAt = time.Now().UTC()
		e.event(r, node.ID, "node.succeeded", node.Name+" 已跳过（非前端仓库）")
	})
}

func scriptLanguageLabel(language string) string {
	if language == "python" {
		return "Python"
	}
	return "Shell"
}

// joinGateCommands binds each configured gate command against the node's
// artifact contracts and joins them into a single script so the gate runs as
// one shell/process invocation instead of one invocation per line. Running
// each line as an independent `sh -lc` call previously broke any multi-line
// construct a user wrote into a gate (if/for/function blocks, line
// continuations, heredocs, ...) with confusing "unexpected end of file" /
// "unexpected token" syntax errors, because a block that only makes sense as
// a whole was split across unrelated shell invocations. Joining preserves
// per-line bindings (artifact-aware grep, git diff excludes) while letting
// multi-line control flow work as a normal shell script. `set -e` is
// prepended for multi-command gates so the first failing statement still
// aborts the remaining commands, matching the previous fail-fast behavior.
func joinGateCommands(commands []string, artifacts []domain.ArtifactContract) string {
	bound := make([]string, 0, len(commands))
	for _, cmd := range commands {
		trimmed := strings.TrimSpace(cmd)
		if trimmed == "" {
			continue
		}
		bound = append(bound, bindVerificationCommand(trimmed, artifacts))
	}
	if len(bound) == 0 {
		return ""
	}
	if len(bound) == 1 {
		return bound[0]
	}
	return "set -e\n" + strings.Join(bound, "\n")
}
