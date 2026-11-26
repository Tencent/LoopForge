package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

func (e *Engine) persistControlDecision(runID string, r domain.Run, expected ...NodeExpectation) error {
	if r.Status == "running" {
		r.CancelRequested = false
		r.PauseRequested = false
	}
	// pause_requested is not part of the run snapshot write. Clear it whenever
	// the decision itself released the pause, including a group that stays
	// waiting because nothing was in flight.
	if len(expected) == 0 && !r.PauseRequested && !r.CancelRequested {
		if err := e.repo.ClearRunControl(runID); err != nil {
			return err
		}
	}
	var err error
	if len(expected) > 0 {
		err = e.repo.PutRunAtRevision(r, r.Revision)
	} else {
		err = e.repo.PutRun(r)
	}
	if err != nil {
		return err
	}
	e.publish(runID)
	if current, ok := e.repo.RunState(runID); ok && current.Status == "queued" {
		e.PromoteQueuedRunsAsync()
		return nil
	}
	if e.executes() {
		e.resume(runID)
		return nil
	}
	if r.OwnerInstanceID != "" {
		_ = e.repo.ReleaseRunLease(runID, r.OwnerInstanceID, r.Generation)
	}
	return nil
}

func (e *Engine) Cancel(runID string) error {
	cancelled, err := e.repo.CancelQueuedRun(runID)
	if err != nil {
		return err
	}
	if cancelled {
		e.publish(runID)
		e.PromoteQueuedRunsAsync()
		return nil
	}
	r, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("run not found")
	}
	if !isLiveStatus(r.Status) {
		if domain.TerminalRunStatus(r.Status) && r.HasActiveNode() {
			e.forceTerminate(runID, "cancelled", "流水线已取消")
			return nil
		}
		return fmt.Errorf("run is not active")
	}
	r, err = e.repo.RequestRunCancel(runID)
	if err != nil {
		return err
	}
	full, _ := e.repo.RunState(runID)
	for _, childID := range childRunIDs(full) {
		if child, ok := e.repo.RunState(childID); ok && isLiveStatus(child.Status) {
			_ = e.Cancel(childID)
		}
	}
	if err := e.cancelRunnerTasksForRun(runID); err != nil {
		return fmt.Errorf("cancel runner tasks: %w", err)
	}
	e.publish(runID)
	// A waiting Run has no active process to acknowledge the request. Settle it
	// synchronously so approval/pause records can always be cancelled from the
	// control-plane-only API process.
	if r.Status == "waiting" {
		e.forceTerminate(runID, "cancelled", "流水线已取消")
		return nil
	}
	if e.Interrupt(runID) {
		return nil
	}
	e.notifyRunControl(runID)
	// No scheduler goroutine owns this process. A live foreign lease means the
	// owning worker was notified (and will also see the flag on lease renew).
	if r.OwnerInstanceID != "" && r.LeaseUntil.After(time.Now().UTC()) && !e.ownsRun(r) {
		return nil
	}
	r, err = e.acquireLease(r)
	if err != nil {
		return nil
	}
	e.settleOrphan(runID, "运行已被手动终止（调度协程不存在，可能由服务重启导致）")
	_ = e.repo.ReleaseRunLease(runID, e.instanceID, r.Generation)
	return nil
}

// Pause interrupts the active node without finalising the Run or destroying
// its workspace. The worker settles at a human decision point where an
// operator can add context and continue the interrupted node.
func (e *Engine) Pause(runID string) error {
	r, err := e.repo.RequestRunPause(runID)
	if err != nil {
		return err
	}
	full, _ := e.repo.RunState(runID)
	for _, childID := range childRunIDs(full) {
		if child, ok := e.repo.RunState(childID); ok && child.Status == "running" {
			_ = e.Pause(childID)
		}
	}
	if err := e.cancelRunnerTasksForRun(runID); err != nil {
		return fmt.Errorf("pause runner tasks: %w", err)
	}
	e.publish(runID)
	if e.Interrupt(runID) {
		return nil
	}
	e.notifyRunControl(runID)
	if r.OwnerInstanceID != "" && r.LeaseUntil.After(time.Now().UTC()) && !e.ownsRun(r) {
		return nil
	}
	r, err = e.acquireLease(r)
	if err != nil {
		return nil
	}
	e.pauseAt(runID, r.Generation, "运行已由用户暂停")
	_ = e.repo.ReleaseRunLease(runID, e.instanceID, r.Generation)
	return nil
}

func (e *Engine) Approve(runID, nodeID, humanContext string, expected ...NodeExpectation) error {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	r, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("run not found")
	}
	if e.executes() && e.workspaceBlocked(r) {
		return fmt.Errorf("run %s 的工作区绑定在 %s:%s，请在绑定实例上审批（或清理该 run 的 workspace 绑定后重试）", runID, r.WorkspaceHost, r.WorkspaceRoot)
	}
	if e.executes() {
		var err error
		r, err = e.acquireLease(r)
		if err != nil {
			return err
		}
	}
	if err := checkNodeExpectation(r, expected); err != nil {
		return err
	}
	if err := forbidClosedGongfengIssue(e.repo, r); err != nil {
		return err
	}
	n := r.Nodes[nodeID]
	if n == nil || n.Status != "waiting_approval" {
		return fmt.Errorf("node is not waiting for approval")
	}
	wf := workflowForRun(e.repo, r)
	var routeDecision *domain.RouteDecision
	if n.GateResult != nil {
		if n.RouteDecision != nil {
			routeDecision = n.RouteDecision
		} else {
			var definition *domain.WorkflowNode
			for i := range wf.Nodes {
				if wf.Nodes[i].ID == nodeID {
					definition = &wf.Nodes[i]
					break
				}
			}
			if definition == nil {
				return fmt.Errorf("approval node definition not found")
			}
			var err error
			routeDecision, err = decideRoute(wf, *definition, n.GateResult.Route, routingArtifactsForNode(r, nodeID))
			if err != nil {
				return err
			}
		}
	}
	n.Status = "succeeded"
	n.RouteDecision = routeDecision
	n.FinishedAt = time.Now().UTC()
	note := recordHumanContext(&r, nodeID, "人工批准继续", humanContext)
	decision := "# Approval\n\nApproved by a human gate.\n"
	if note != "" {
		decision += "\n" + note + "\n"
	}
	appendArtifact(&r, nodeID, n.Attempt, "decision", "decision.md", "text/markdown", []byte(decision))
	e.event(&r, nodeID, "gate.approved", "人工门禁已通过")
	appendControlTelemetry(&r, nodeID, "gate.approved", "人工门禁已通过")
	if routeDecision != nil {
		e.event(&r, nodeID, "route.decided", fmt.Sprintf("人工批准路由：%v", routeDecision.Values))
	}
	r.Status = "running"
	return e.persistControlDecision(runID, r, expected...)
}

// Pass marks a failed (or paused) node as succeeded so downstream can
// continue without re-executing it. Operators use this after onFailure=fail,
// when reject reworks are exhausted, or when a hard script is paused
// mid-execution and retrying from scratch won't help (e.g. an environment
// problem) — hard scripts can otherwise stall a run forever.
func (e *Engine) Pass(runID, nodeID, humanContext string, expected ...NodeExpectation) error {
	if err := e.pass(runID, nodeID, humanContext, expected...); err != nil {
		return err
	}
	e.resumeParentAfterChildRecovery(runID)
	return nil
}

func (e *Engine) pass(runID, nodeID, humanContext string, expected ...NodeExpectation) error {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	r, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("run not found")
	}
	if e.executes() && e.workspaceBlocked(r) {
		return fmt.Errorf("run %s 的工作区绑定在 %s:%s，请在绑定实例上通过该节点（或清理该 run 的 workspace 绑定后重试）", runID, r.WorkspaceHost, r.WorkspaceRoot)
	}
	if e.executes() {
		var err error
		r, err = e.acquireLease(r)
		if err != nil {
			return err
		}
	}
	if err := checkNodeExpectation(r, expected); err != nil {
		return err
	}
	if err := forbidClosedGongfengIssue(e.repo, r); err != nil {
		return err
	}
	if nodeID == "" {
		nodeID = resumeNodeID(r)
	}
	n := r.Nodes[nodeID]
	if n == nil {
		return fmt.Errorf("pass node not found")
	}
	// A hard script parked at waiting_approval because a human paused the
	// run mid-execution (see nodeCancelled/pauseAt) can also be passed
	// manually: resuming would just re-run the script from scratch, which
	// doesn't help when it's stuck on something a retry can't fix.
	pausedScript := r.Status == "waiting" && r.PauseRequested && n.Status == "waiting_approval"
	if !pausedScript {
		if isLiveStatus(r.Status) {
			return fmt.Errorf("run is still active")
		}
		if r.Status != "failed" || n.Status != "failed" {
			return fmt.Errorf("only a failed node on a failed run can be passed")
		}
	}
	wf := workflowForRun(e.repo, r)
	var def domain.WorkflowNode
	found := false
	for _, node := range wf.Nodes {
		if node.ID == nodeID {
			def, found = node, true
			break
		}
	}
	if !found || !isScriptNode(def) {
		return fmt.Errorf("only a failed script node can be passed")
	}
	for _, edge := range wf.Edges {
		if edge.Source == nodeID && edge.Condition != nil && n.RouteDecision == nil {
			return fmt.Errorf("节点 %s 尚无有效路由决策，请重试节点而不是手动通过", nodeID)
		}
	}
	n.Status = "succeeded"
	n.FinishedAt = time.Now().UTC()
	n.Activity = "人工通过，未重新执行"
	note := recordHumanContext(&r, nodeID, "人工通过该节点，继续下游", humanContext)
	decisionReason := "after the node failed"
	if pausedScript {
		decisionReason = "while the run was paused mid-execution"
	}
	decision := fmt.Sprintf("# Pass\n\nManually passed by a human %s.\n", decisionReason)
	if note != "" {
		decision += "\n" + note + "\n"
	}
	appendArtifact(&r, nodeID, n.Attempt, "decision", "decision.md", "text/markdown", []byte(decision))
	e.event(&r, nodeID, "node.passed", "人工通过该节点，继续下游")
	appendControlTelemetry(&r, nodeID, "node.passed", "人工通过该节点，继续下游")
	r.CancelRequested = false
	r.PauseRequested = false
	r.Status = "running"
	markRunRelationsRunning(e.repo, r)
	return e.persistControlDecision(runID, r, expected...)
}

func (e *Engine) Reject(runID string, req domain.RejectRequest, expected ...NodeExpectation) error {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	r, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("run not found")
	}
	if e.executes() && e.workspaceBlocked(r) {
		return fmt.Errorf("run %s 的工作区绑定在 %s:%s，请在绑定实例上打回（或清理该 run 的 workspace 绑定后重试）", runID, r.WorkspaceHost, r.WorkspaceRoot)
	}
	if e.executes() {
		var err error
		r, err = e.acquireLease(r)
		if err != nil {
			return err
		}
	}
	if err := checkNodeExpectation(r, expected); err != nil {
		return err
	}
	if err := forbidClosedGongfengIssue(e.repo, r); err != nil {
		return err
	}
	wf := workflowForRun(e.repo, r)
	target := req.TargetID
	if target == "" {
		for _, n := range wf.Nodes {
			if n.ID == req.NodeID {
				target = n.RejectTo
			}
		}
	}
	if target == "" {
		return fmt.Errorf("reject target is required")
	}
	if r.Nodes[target] == nil {
		return fmt.Errorf("reject target not found")
	}
	for _, n := range wf.Nodes {
		if n.ID == req.NodeID && n.MaxReworks > 0 && r.Nodes[target].ReworkCount >= n.MaxReworks {
			return fmt.Errorf("rework limit reached")
		}
	}
	reset := descendants(wf, target)
	reset[target] = true
	if err := e.guardRoutingRoleDrift(&r, reset); err != nil {
		return err
	}
	overlayLatestNodeBindings(e.repo, &r, reset)
	var stale []string
	interrupted := map[string]bool{}
	for id := range reset {
		if nr := r.Nodes[id]; nr != nil {
			stale = append(stale, nr.ChildRunID)
			if nr.Status == "running" || nr.Status == "pending_input" {
				// A parallel sibling still running inside the reset range: its
				// attempt is abandoned, and the rerun must count as a new attempt
				// rather than continue the interrupted one.
				interrupted[id] = true
				nr.Activity = ""
			}
			nr.Status = "pending"
			nr.RouteDecision, nr.SkipReason = nil, ""
			nr.Error = ""
			nr.Output = ""
			nr.ChildRunID = ""
			nr.FinishedAt = time.Time{}
			if id == target {
				nr.ReworkCount++
				nr.Feedback = recordHumanContext(&r, req.NodeID, req.Reason, req.Context)
			} else {
				nr.Feedback = ""
			}
		}
	}
	e.event(&r, req.NodeID, "gate.rejected", fmt.Sprintf("打回到 %s：%s", target, req.Reason))
	appendControlTelemetry(&r, req.NodeID, "gate.rejected", fmt.Sprintf("打回到 %s：%s", target, req.Reason))
	r.Status = "running"
	e.cancelLiveChildRuns(stale)
	if err := e.persistControlDecision(runID, r, expected...); err != nil {
		return err
	}
	e.cancelNodeExecutions(runID, interrupted)
	return nil
}

// Retry reopens a stopped workflow from a selected
// node while preserving the original run, succeeded upstream nodes, and their
// frozen artifacts. Succeeded runs may already have a cleaned workspace; those
// are revived so the operator can adjust context and replay from a node.
// A manual retry grants exactly one extra attempt when the node has exhausted
// its configured automatic attempts. Empty nodeID resumes from the interrupted node.
func (e *Engine) Retry(runID, nodeID, reason, humanContext string, expected ...NodeExpectation) error {
	return e.RetryContext(context.Background(), runID, nodeID, reason, humanContext, expected...)
}

// RetryContext reuses the caller's workspace lease while restoring a reclaimed run.
func (e *Engine) RetryContext(ctx context.Context, runID, nodeID, reason, humanContext string, expected ...NodeExpectation) error {
	if err := e.retryContext(ctx, runID, nodeID, reason, humanContext, "", expected...); err != nil {
		return err
	}
	e.resumeParentAfterChildRecoveryContext(ctx, runID)
	return nil
}

// ShouldDeferRestoreUntilAdmitted avoids opening a reclaimed checkout for a
// run likely to queue. Execution rechecks the cleanup marker after admission.
func (e *Engine) ShouldDeferRestoreUntilAdmitted(run domain.Run) bool {
	if run.WorkspaceCleanedAt.IsZero() {
		return false
	}
	queued, err := e.repo.RunWouldQueueOnAdmission(run)
	return err == nil && queued
}

// cancelLiveChildRuns stops child LOOP runs whose parent node was reset and no
// longer references them, so they neither keep working nor hold a workspace.
// Callers hold stateMu and Cancel settles through updateRun, which takes it
// too, so the cancellation runs once the caller has released the lock.
func (e *Engine) cancelLiveChildRuns(ids []string) {
	live := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			live = append(live, id)
		}
	}
	if len(live) == 0 {
		return
	}
	go func() {
		for _, id := range live {
			if child, ok := e.repo.RunState(id); ok && isLiveStatus(child.Status) {
				_ = e.Cancel(id)
			}
		}
	}()
}

// resumeParentAfterChildRecovery lets a parent that failed because of this
// child LOOP run continue once the child is retried or passed: the parent
// replays its LOOP node and follows the recovered child instead of starting a
// new one.
func (e *Engine) resumeParentAfterChildRecovery(childID string) {
	e.resumeParentAfterChildRecoveryContext(context.Background(), childID)
}

func (e *Engine) resumeParentAfterChildRecoveryContext(ctx context.Context, childID string) {
	child, ok := e.repo.RunState(childID)
	if !ok {
		return
	}
	parentID := strings.TrimSpace(child.ExecutionContext.ParentRunID)
	nodeID := strings.TrimSpace(child.ExecutionContext.ParentNodeID)
	if parentID == "" || nodeID == "" {
		return
	}
	parent, ok := e.repo.RunState(parentID)
	if !ok || parent.Status != "failed" {
		return
	}
	nr := parent.Nodes[nodeID]
	if nr == nil || nr.ChildRunID != childID || nr.Status != "failed" {
		return
	}
	pointsAtChild := false
	for _, node := range parent.WorkflowSnapshot.Nodes {
		if node.ID == nodeID {
			pointsAtChild = strings.TrimSpace(node.SOPWorkflowID) == child.WorkflowID
		}
	}
	if !pointsAtChild {
		return
	}
	if err := e.retryContext(ctx, parentID, nodeID, "子 LOOP "+childID+" 已恢复，父流程重新接上", "", childID); err != nil {
		log.Printf("resume parent %s after child %s recovery: %v", parentID, childID, err)
		return
	}
	e.resumeParentAfterChildRecoveryContext(ctx, parentID)
}

func (e *Engine) retryContext(ctx context.Context, runID, nodeID, reason, humanContext, keepChildRunID string, expected ...NodeExpectation) error {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	r, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("run not found")
	}
	if e.executes() && e.workspaceBlocked(r) {
		return fmt.Errorf("run %s 的工作区绑定在 %s:%s，请在绑定实例上重试（或清理该 run 的 workspace 绑定后重试）", runID, r.WorkspaceHost, r.WorkspaceRoot)
	}
	if e.executes() {
		var err error
		r, err = e.acquireLease(r)
		if err != nil {
			return err
		}
	}
	if err := checkNodeExpectation(r, expected); err != nil {
		return err
	}
	if err := forbidClosedGongfengIssue(e.repo, r); err != nil {
		return err
	}
	explicitNode := nodeID != ""
	if nodeID == "" {
		nodeID = resumeNodeID(r)
	}
	if nodeID == "" {
		if allSinksSkippedJoin(r) {
			return fmt.Errorf("没有可重试的节点：最后一步因条件汇合规则被跳过，请检查汇合规则，或从产生路由决策的节点重新运行")
		}
		return fmt.Errorf("没有可重试的节点，请查看运行失败原因，或指定节点重跑")
	}
	if r.Nodes[nodeID] == nil {
		return fmt.Errorf("retry node not found")
	}
	if len(r.WorkflowSnapshot.Nodes) > 0 {
		found := false
		for _, node := range r.WorkflowSnapshot.Nodes {
			if node.ID == nodeID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("retry node is not in the current LOOP")
		}
	}
	nodeStatus := r.Nodes[nodeID].Status
	waitingRetry := r.Status == "waiting" && nodeStatus == "waiting_approval"
	replaySucceeded := r.Status == "succeeded"
	if r.Status == "running" || r.HasActiveNode() {
		return fmt.Errorf("run is still active")
	}
	if r.Status != "waiting" && r.Status != "failed" && r.Status != "cancelled" && !replaySucceeded {
		return fmt.Errorf("only failed, cancelled, waiting, or succeeded replay decisions can be resumed")
	}
	if explicitNode && !replayUpstreamSucceeded(r, nodeID) {
		return fmt.Errorf("cannot replay node %s: an upstream node has not succeeded", nodeID)
	}
	cleaned := !r.WorkspaceCleanedAt.IsZero()
	if !explicitNode && !replaySucceeded && (r.Status == "cancelled" || cleaned && !e.hasSavedWorkspaceCheckpoint(runID)) {
		return fmt.Errorf("run workspace has been cleaned or scheduled for cleanup; this run can no longer be retried")
	}
	reset := descendants(r.WorkflowSnapshot, nodeID)
	reset[nodeID] = true
	wholeWorkflow := len(reset) >= len(r.WorkflowSnapshot.Nodes)
	// Revive parallel branches interrupted by cancellation, without treating
	// them as part of the whole-workflow checkout reset.
	var revived []string
	for _, node := range r.WorkflowSnapshot.Nodes {
		if !reset[node.ID] && r.Nodes[node.ID] != nil && r.Nodes[node.ID].Status == "cancelled" {
			revived = append(revived, node.Name)
			reset[node.ID] = true
			for id := range descendants(r.WorkflowSnapshot, node.ID) {
				reset[id] = true
			}
		}
	}
	if err := e.guardRoutingRoleDrift(&r, reset); err != nil {
		return err
	}
	if err := e.validateExecutionReconnect(r, nodeID); err != nil {
		return err
	}
	deferRestore := e.ShouldDeferRestoreUntilAdmitted(r)
	if cleaned && !deferRestore {
		if err := e.restoreCheckpointBeforeExecution(ctx, r); err != nil {
			return err
		}
		r, _ = e.repo.RunState(runID)
		if err := e.repo.ResetRunWorkspaceCleanup(runID); err != nil {
			return err
		}
		r.WorkspaceCleanedAt = time.Time{}
		if e.executes() {
			if err := e.releaseRunEnvironment(runID, false); err != nil {
				log.Printf("discard run sandbox %s: %v", runID, err)
			}
			e.sandboxes.DiscardRunContainer(context.Background(), runID)
		}
	}
	for i := range r.WorkflowSnapshot.Nodes {
		n := &r.WorkflowSnapshot.Nodes[i]
		if n.ID == nodeID && n.MaxAttempts <= r.Nodes[nodeID].Attempt {
			n.MaxAttempts = r.Nodes[nodeID].Attempt + 1
		}
	}
	if strings.TrimSpace(reason) == "" {
		if waitingRetry {
			reason = "对话轮次用尽后人工确认重试"
		} else if replaySucceeded {
			reason = "从指定节点重跑"
		} else {
			reason = "从中断处继续"
		}
	}
	if e.executes() && !deferRestore && wholeWorkflow {
		// Replaying the whole LOOP against the previous attempt's container
		// inherits whatever that attempt broke or filled up, and the container
		// would otherwise survive until the run finally succeeds. Dropping the
		// handle first releases the workspace flock so the removal is safe;
		// ensureStarted rebuilds a clean container on the next exec.
		if err := e.releaseRunEnvironment(runID, false); err != nil {
			log.Printf("discard run sandbox %s: %v", runID, err)
		}
		e.sandboxes.DiscardRunContainer(context.Background(), runID)
	}
	overlayLatestNodeBindings(e.repo, &r, reset)
	for _, node := range r.WorkflowSnapshot.Nodes {
		if node.ID == nodeID && isScriptNode(node) {
			humanContext = ""
			break
		}
	}
	note := recordHumanContext(&r, nodeID, reason, humanContext)
	var stale []string
	for id := range reset {
		if nr := r.Nodes[id]; nr != nil {
			if nr.ChildRunID != keepChildRunID {
				stale = append(stale, nr.ChildRunID)
			}
			nr.Status = "pending"
			nr.RouteDecision, nr.SkipReason = nil, ""
			nr.Error = ""
			nr.Output = ""
			nr.ChildRunID = ""
			if id == nodeID {
				nr.ChildRunID = keepChildRunID
			}
			nr.FinishedAt = time.Time{}
			if id == nodeID {
				nr.Feedback = note
			} else {
				nr.Feedback = ""
			}
		}
	}
	retryLabel := "从中断处继续"
	if explicitNode {
		retryLabel = "从指定节点重跑"
	}
	e.event(&r, nodeID, "node.retry_requested", retryLabel+"："+note)
	if len(revived) > 0 {
		e.event(&r, "", "node.retry_requested", "一并恢复被取消的并行分支："+strings.Join(revived, "、"))
	}
	appendControlTelemetry(&r, nodeID, "node.retry_requested", retryLabel+"："+strings.TrimSpace(reason))
	r.CancelRequested = false
	r.PauseRequested = false
	r.Status = "running"
	if issueID := r.ExecutionContext.IssueID; issueID != "" {
		if issue, found := e.repo.Issue(issueID); found && issue.Status != "done" && issue.Status != "closed" {
			issue.Status = "in_progress"
			issue.UpdatedAt = time.Now().UTC()
			_ = e.repo.PutIssue(issue)
		}
	}
	if taskID := r.ExecutionContext.TaskID; taskID != "" {
		if task, found := e.repo.Task(taskID); found {
			task.Status = "running"
			task.UpdatedAt = time.Now().UTC()
			_ = e.repo.PutTask(task)
		}
	}
	e.cancelLiveChildRuns(stale)
	return e.persistControlDecision(runID, r, expected...)
}

func allSinksSkippedJoin(r domain.Run) bool {
	if r.Status != "failed" || len(r.WorkflowSnapshot.Nodes) == 0 {
		return false
	}
	wf := workflowForRun(nil, r)
	return allRoutingFinished(wf, r) && !allSucceeded(wf, r)
}

// ResumePaused continues every node interrupted by an operator pause. It is a
// separate operation from Retry so ordinary approval gates remain untouched.
// Child LOOP runs paused together with the parent are resumed with it.
// Children resume immediately when the parent still holds its slot. After
// idle reclaim, the parent resumes them once it is admitted again.
func (e *Engine) ResumePaused(runID, humanContext string, expected ...NodeExpectation) error {
	return e.ResumePausedContext(context.Background(), runID, humanContext, expected...)
}

// ResumePausedContext retains workspace ownership while restoring a paused LOOP.
func (e *Engine) ResumePausedContext(ctx context.Context, runID, humanContext string, expected ...NodeExpectation) error {
	r, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("run not found")
	}
	if err := checkNodeExpectation(r, expected); err != nil {
		return err
	}
	if r.Status == "waiting" && r.PauseRequested {
		for _, nr := range r.Nodes {
			if !isUserPausedNode(nr) || nr.ChildRunID == "" {
				continue
			}
			child, ok := e.repo.RunState(nr.ChildRunID)
			if !ok || !child.PauseRequested {
				continue
			}
			switch child.Status {
			case "running":
				return fmt.Errorf("子 LOOP %s 仍在停止当前任务，请稍后再继续", child.ID)
			case "waiting":
				if !resumeChildrenAfterAdmission(r) {
					if err := e.ResumePausedContext(ctx, child.ID, humanContext); err != nil {
						return fmt.Errorf("继续子 LOOP %s：%w", child.ID, err)
					}
				}
			}
		}
	}
	if !e.ShouldDeferRestoreUntilAdmitted(r) {
		if err := e.RestoreConversationWorkspaceIfNeeded(ctx, runID); err != nil {
			return err
		}
	}
	return e.resumePaused(runID, humanContext, expected...)
}

func resumeChildrenAfterAdmission(run domain.Run) bool {
	return run.AdmissionState == "released" || run.AdmissionState == "unstarted" || !run.WorkspaceCleanedAt.IsZero()
}

const resumePausedChildActivity = "等待继续子 LOOP"

// legacyResumePausedChildActivity is the same marker stored before the LOOP
// rename. A parent paused on that build still resumes its child.
const legacyResumePausedChildActivity = "等待继续子 SOP"

func isResumePausedChildActivity(activity string) bool {
	return activity == resumePausedChildActivity || activity == legacyResumePausedChildActivity
}

// childRunIDs lists the child LOOP runs referenced by the run's LOOP nodes.
func childRunIDs(r domain.Run) []string {
	ids := make([]string, 0)
	for _, nr := range r.Nodes {
		if nr != nil && nr.ChildRunID != "" {
			ids = append(ids, nr.ChildRunID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (e *Engine) resumePaused(runID, humanContext string, expected ...NodeExpectation) error {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	r, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("run not found")
	}
	if err := checkNodeExpectation(r, expected); err != nil {
		return err
	}
	if r.Status != "waiting" || !r.PauseRequested {
		return fmt.Errorf("run is not paused")
	}
	if e.executes() && e.workspaceBlocked(r) {
		return fmt.Errorf("run %s 的工作区绑定在 %s:%s，请在绑定实例上继续", runID, r.WorkspaceHost, r.WorkspaceRoot)
	}
	activeRunnerTasks, err := e.repo.ActiveRunnerTasksForRun(runID)
	if err != nil {
		return fmt.Errorf("check runner tasks: %w", err)
	}
	if len(activeRunnerTasks) > 0 {
		return fmt.Errorf("执行机器仍在停止当前任务，请等待远端进程退出后再继续")
	}
	paused := make([]string, 0)
	for id, nr := range r.Nodes {
		if isUserPausedNode(nr) {
			paused = append(paused, id)
		}
	}
	// Returning from group mode deliberately leaves a pending LOOP node, rather
	// than an interrupted worker. Recognize that explicit checkpoint, including
	// records created before this fix, without approving ordinary human gates.
	if len(paused) == 0 && !r.IsWorkspace() {
		for i := len(r.Events) - 1; i >= 0; i-- {
			ev := r.Events[i]
			if ev.Type == "group.sop_restored" {
				if nr := r.Nodes[ev.NodeID]; nr != nil && nr.Status == "pending" && nr.Activity == "切回 SOP，等待继续" && replayUpstreamSucceeded(r, ev.NodeID) {
					paused = append(paused, ev.NodeID)
				}
				break
			}
		}
	}
	if len(paused) == 0 {
		// Pause can land after the active step has already finished: the last
		// node succeeded and the scheduler exited, or group collaboration was
		// already idle. The pause flag is set, but no node is parked at
		// waiting_approval. Continue that graph instead of failing. Ordinary
		// human gates stay blocked — they are not a pause checkpoint.
		resumed, err := resumeUnpausedGraph(&r)
		if err != nil {
			return err
		}
		if !resumed {
			return fmt.Errorf("paused node not found")
		}
		if e.executes() && r.Status == "running" {
			r, err = e.acquireLease(r)
			if err != nil {
				return err
			}
		}
		message := "人工确认直接继续运行"
		if r.IsWorkspace() && r.Status == "waiting" {
			message = "继续自由协作"
		}
		e.event(&r, "", "run.resumed", message)
		appendControlTelemetry(&r, "", "run.resumed", message)
		if r.Status == "running" {
			markRunRelationsRunning(e.repo, r)
		}
		return e.persistControlDecision(runID, r, expected...)
	}
	if e.executes() {
		var err error
		r, err = e.acquireLease(r)
		if err != nil {
			return err
		}
	}
	sort.Strings(paused)
	nodes := make(map[string]domain.WorkflowNode, len(r.WorkflowSnapshot.Nodes))
	for _, node := range r.WorkflowSnapshot.Nodes {
		nodes[node.ID] = node
	}
	firstAgent := ""
	for _, id := range paused {
		if !isScriptNode(nodes[id]) {
			firstAgent = id
			break
		}
	}
	note := ""
	if firstAgent != "" && strings.TrimSpace(humanContext) != "" {
		note = recordHumanContext(&r, firstAgent, "", humanContext)
	}
	for _, id := range paused {
		nr := r.Nodes[id]
		restoredFeedback := ""
		if nr.Status == "pending" && nr.Activity == "切回 SOP，等待继续" {
			restoredFeedback = nr.Feedback
		}
		nr.Status = "pending"
		nr.Error = ""
		nr.Activity = ""
		if nr.ChildRunID != "" && resumeChildrenAfterAdmission(r) {
			nr.Activity = resumePausedChildActivity
		}
		nr.FinishedAt = time.Time{}
		nr.Feedback = ""
		nr.ResumeSessionID = ""
		if !isScriptNode(nodes[id]) {
			nr.Feedback = strings.TrimSpace(restoredFeedback + "\n" + note)
			nr.ResumeSessionID = strings.TrimSpace(nr.SessionID)
		}
	}
	r.CancelRequested = false
	r.PauseRequested = false
	r.Status = "running"
	message := "人工确认直接继续运行"
	if note != "" {
		message = "人工补充信息后继续运行"
	}
	e.event(&r, paused[0], "run.resumed", message)
	appendControlTelemetry(&r, paused[0], "run.resumed", message)
	markRunRelationsRunning(e.repo, r)
	return e.persistControlDecision(runID, r, expected...)
}

// RefreshRunSOP replaces a stopped Run's frozen graph with the latest
// published LOOP. Completed nodes with the same ID keep their outcomes, new
// nodes start pending, and the already materialised sandbox stays frozen.
func (e *Engine) RefreshRunSOP(runID string) error {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	r, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("run not found")
	}
	if r.Status == "running" || r.HasActiveNode() || (r.Status != "waiting" && !domain.TerminalRunStatus(r.Status)) {
		return fmt.Errorf("only a stopped run can update its LOOP")
	}
	if e.executes() {
		var err error
		r, err = e.acquireLease(r)
		if err != nil {
			return err
		}
	}
	latest, ok := latestWorkflowDefinition(e.repo, r.WorkflowID)
	if !ok {
		return fmt.Errorf("workflow not found")
	}
	previousVersion, previousRevision := r.WorkflowSnapshot.Version, r.WorkflowSnapshot.Revision
	if previousVersion == latest.Version && previousRevision == latest.Revision {
		return nil
	}
	// A route decision refers to edges in the frozen snapshot. Replacing that
	// graph while preserving completed nodes could activate the wrong branch.
	for _, nr := range r.Nodes {
		if nr != nil && nr.RouteDecision != nil {
			return fmt.Errorf("cannot refresh SOP after a routing decision; start a new run")
		}
	}
	// An Agent can be waiting for approval after it has submitted a route value
	// but before the decision is materialised. Treat that pending value exactly
	// like a committed decision: refreshing the graph would otherwise let the
	// old value select a different target in the new SOP. An approval without a
	// route value only has to stay refreshable while the new SOP asks it for no
	// decision — that is the escape hatch for a wrong conditional edge found
	// while the gate waits. If the new SOP does expect a decision there, the
	// attempt never produced one and approving it could only fail, so the
	// refresh must not hand the gate an unanswerable question. A waiting node
	// reaches this state either through an Agent human decision or through a
	// required manual role gate after pass, and both rebuild the decision the
	// same way, so neither outcome is filtered out here.
	//
	// `waiting_approval` also records an operator pause, which is a different
	// thing: resuming that node re-executes it from scratch, so it produces a
	// fresh route against whatever graph is frozen by then and has no stale
	// intent to protect. Skipping those nodes keeps a paused routing script
	// refreshable after a SOP update instead of forcing a new run.
	conditionalSources := map[string]bool{}
	for _, edge := range r.WorkflowSnapshot.Edges {
		if edge.Condition != nil {
			conditionalSources[edge.Source] = true
		}
	}
	latestConditionalSources := map[string]bool{}
	for _, edge := range latest.Edges {
		if edge.Condition != nil {
			latestConditionalSources[edge.Source] = true
		}
	}
	for _, node := range r.WorkflowSnapshot.Nodes {
		nr := r.Nodes[node.ID]
		if nr == nil || nr.Status != "waiting_approval" || nr.RouteDecision != nil || isUserPausedNode(nr) {
			continue
		}
		var pending domain.RouteValues
		if nr.GateResult != nil {
			pending = nr.GateResult.Route
		}
		if len(pending) > 0 {
			// A submitted value refers to the graph that was frozen when it was
			// produced. Either graph carrying conditions means the swap could keep
			// a stale intent or silently select a branch in the new one, so both
			// sides are checked here.
			if conditionalSources[node.ID] || latestConditionalSources[node.ID] {
				return fmt.Errorf("节点 %s 存在待处理的路由决策，不能刷新 SOP；请先审批原 SOP 或重新开始运行", node.ID)
			}
			continue
		}
		if latestConditionalSources[node.ID] {
			return fmt.Errorf("节点 %s 正在等待人工确认，但新版 SOP 为它新增了条件分支；请先按原版 SOP 审批，或重新开始运行", node.ID)
		}
	}
	// A graph/role update must not silently replace the environment underneath
	// an existing workspace. A new Run will pick up the latest sandbox asset.
	latest.SandboxProfileID = r.WorkflowSnapshot.SandboxProfileID
	latest.AutoSelectSandbox = r.WorkflowSnapshot.AutoSelectSandbox
	latest.SandboxSnapshot = r.WorkflowSnapshot.SandboxSnapshot
	latest, err := configureBaselineAlignCLI(latest)
	if err != nil {
		return err
	}
	latest, err = domain.AttachPreviewBuild(latest)
	if err != nil {
		return err
	}
	if nodeID := missingRouteDecision(latest, r); nodeID != "" {
		return fmt.Errorf("无法刷新 SOP：已完成的节点「%s」在新版 SOP 中需要路由决策，请创建新 Run 或调整 SOP", nodeID)
	}
	for _, node := range latest.Nodes {
		if r.Nodes[node.ID] == nil {
			r.Nodes[node.ID] = &domain.NodeRun{NodeID: node.ID, Status: "pending"}
		}
	}
	latest.RoutingRoleFingerprints = captureRoutingRoleFingerprints(e.repo, latest)
	r.WorkflowSnapshot = latest
	message := fmt.Sprintf("已更新为最新 LOOP：v%d #%d → v%d #%d（保留当前运行环境）", previousVersion, previousRevision, latest.Version, latest.Revision)
	e.event(&r, "", "run.workflow_refreshed", message)
	appendControlTelemetry(&r, "", "run.workflow_refreshed", message)
	if err := e.repo.PutRun(r); err != nil {
		return err
	}
	e.publish(runID)
	return nil
}

func isUserPausedNode(nr *domain.NodeRun) bool {
	return nr != nil && nr.Status == "waiting_approval" && strings.Contains(nr.Activity, "用户暂停")
}

// resumeUnpausedGraph clears a pause that did not interrupt a node.
// A human gate still blocks: resume must not approve it. Otherwise a finished
// LOOP goes back to the scheduler so it can settle, pending work continues,
// and an idle group returns to waiting for the next task.
func resumeUnpausedGraph(r *domain.Run) (bool, error) {
	if r == nil {
		return false, fmt.Errorf("run not found")
	}
	wf := r.WorkflowSnapshot
	if hasWaiting(wf, *r) {
		return false, nil
	}
	for _, n := range wf.Nodes {
		if r.Nodes[n.ID] == nil {
			return false, nil
		}
	}
	ready := readyNodes(wf, *r)
	r.CancelRequested = false
	r.PauseRequested = false
	if r.IsWorkspace() && len(ready) == 0 {
		r.Status = "waiting"
		return true, nil
	}
	if len(ready) > 0 || allSucceeded(wf, *r) || hasFailed(wf, *r) {
		r.Status = "running"
		return true, nil
	}
	return false, nil
}

func isScriptNode(node domain.WorkflowNode) bool {
	return node.Type == "script" || node.Type == "command"
}

func markRunRelationsRunning(st *repo.Store, r domain.Run) {
	if issueID := r.ExecutionContext.IssueID; issueID != "" {
		if issue, found := st.Issue(issueID); found && issue.Status != "done" && issue.Status != "closed" {
			issue.Status = "in_progress"
			issue.UpdatedAt = time.Now().UTC()
			_ = st.PutIssue(issue)
		}
	}
	if taskID := r.ExecutionContext.TaskID; taskID != "" {
		if task, found := st.Task(taskID); found {
			task.Status = "running"
			task.UpdatedAt = time.Now().UTC()
			_ = st.PutTask(task)
		}
	}
}

func (e *Engine) DeclineRetry(runID, nodeID string, expected ...NodeExpectation) error {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	r, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("run not found")
	}
	if err := checkNodeExpectation(r, expected); err != nil {
		return err
	}
	n := r.Nodes[nodeID]
	if n == nil || n.Status != "waiting_approval" {
		return fmt.Errorf("node is not waiting for approval")
	}
	n.Status = "failed"
	if strings.TrimSpace(n.Error) == "" {
		n.Error = "对话轮次用尽，人工选择不重试"
	}
	n.Activity = "人工选择不重试"
	n.FinishedAt = time.Now().UTC()
	e.event(&r, nodeID, "agent.turns.declined", "人工选择不重试本节点")
	appendControlTelemetry(&r, nodeID, "agent.turns.declined", "人工选择不重试本节点")
	r.Status = "failed"
	var err error
	if len(expected) > 0 {
		err = e.repo.PutRunAtRevision(r, r.Revision)
	} else {
		err = e.repo.PutRun(r)
	}
	if err != nil {
		return err
	}
	e.publish(runID)
	return nil
}

func overlayLatestNodeBindings(st *repo.Store, r *domain.Run, nodeIDs map[string]bool) {
	if st == nil || r == nil || len(nodeIDs) == 0 {
		return
	}
	// A frozen artifact condition resolves a contract by name, so pair this node
	// with a newer role and the agent may write the renamed artifact while the
	// edge still looks for the old one and quietly takes the default branch. The
	// whole binding stays frozen for such a node: role, executor, prompt and
	// contract have to move together to stay coherent. Other nodes in the same
	// conditional workflow keep refreshing, and a workflow without artifact
	// conditions keeps the historical overlay.
	frozenNodes := frozenArtifactContractSources(r.WorkflowSnapshot)
	latest, ok := latestWorkflowDefinition(st, r.WorkflowID)
	if !ok {
		return
	}
	byID := map[string]domain.WorkflowNode{}
	for _, n := range latest.Nodes {
		byID[n.ID] = n
	}
	for i := range r.WorkflowSnapshot.Nodes {
		n := &r.WorkflowSnapshot.Nodes[i]
		if !nodeIDs[n.ID] || frozenNodes[n.ID] {
			continue
		}
		src, ok := byID[n.ID]
		if !ok {
			continue
		}
		if n.AgentID != src.AgentID || n.ExecutorID != src.ExecutorID {
			delete(r.WorkflowSnapshot.ExecutorDefaults, n.ID)
		}
		n.AgentID, n.ExecutorID = src.AgentID, src.ExecutorID
		n.NodePrompt = src.NodePrompt
		n.ArtifactOutputs = src.ArtifactOutputs
		n.OnFailure, n.RejectTo = src.OnFailure, src.RejectTo
		n.RetryStrategy, n.CarryOver = src.RetryStrategy, src.CarryOver
		if src.MaxAttempts > n.MaxAttempts {
			n.MaxAttempts = src.MaxAttempts
		}
		if src.MaxReworks > n.MaxReworks {
			n.MaxReworks = src.MaxReworks
		}
	}
	// A frozen binding keeps part of the snapshot on its old revision, so the
	// version stamp must not claim the graph is fully up to date: RefreshRunSOP
	// treats an equal version as "nothing to do" and would leave the frozen
	// binding behind for good.
	if len(frozenNodes) == 0 && latest.Version > r.WorkflowSnapshot.Version {
		r.WorkflowSnapshot.Version = latest.Version
	}
}

// frozenArtifactContractSources lists the nodes whose artifact contracts an
// edge condition resolves by name at run time. Their whole binding is frozen so
// the contract cannot drift away from the role that produces it.
func frozenArtifactContractSources(wf domain.WorkflowDefinition) map[string]bool {
	frozen := map[string]bool{}
	for _, edge := range wf.Edges {
		if edge.Condition != nil && edge.Condition.Type == "artifact" {
			frozen[edge.Source] = true
		}
	}
	return frozen
}

func resumeNodeID(r domain.Run) string {
	wf := r.WorkflowSnapshot
	order := make([]string, 0, len(wf.Nodes))
	for _, n := range wf.Nodes {
		order = append(order, n.ID)
	}
	if len(order) == 0 {
		for id := range r.Nodes {
			order = append(order, id)
		}
	}
	for _, id := range order {
		if nr := r.Nodes[id]; nr != nil && (nr.Status == "cancelled" || nr.Status == "failed") {
			return id
		}
	}
	ready := readyNodes(wf, r)
	if len(ready) > 0 {
		return ready[0].ID
	}
	// 兜底：选第一个尚未成功且不是 skipped 的节点（例如取消/中断后仍停留在
	// running 的节点）。skipped 是条件路由或汇合规则的终态，重试它不会改变
	// 上游路由决策，只会再次得到 skipped，因而不能作为重试起点。
	for _, id := range order {
		if nr := r.Nodes[id]; nr != nil && nr.Status != "succeeded" && nr.Status != "skipped" {
			return id
		}
	}
	return ""
}
