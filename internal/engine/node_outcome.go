package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (e *Engine) waitApproval(runID string, node domain.WorkflowNode, msg string) {
	e.updateRun(runID, func(r *domain.Run) {
		nr := r.Nodes[node.ID]
		nr.Status = "waiting_approval"
		nr.FinishedAt = time.Now().UTC()
		e.event(r, node.ID, "gate.waiting", msg)
	})
}

func (e *Engine) nodeFailed(runID string, node domain.WorkflowNode, err error, allowReject bool) {
	r, ok := e.repo.RunState(runID)
	if !ok || r.Nodes[node.ID] == nil {
		return
	}
	e.nodeFailedAt(runID, node, r.Nodes[node.ID].Attempt, r.Generation, err, allowReject)
}

// shouldCommitNodeOutcome is the exclusive-execute write fence for one node.
// Generation mismatch means another instance took the lease. A successful
// outcome may still land after retries bumped Attempt, so a live agent that
// passed gates is not dropped. Failures stay attempt-scoped and never
// overwrite succeeded or waiting_approval.
func shouldCommitNodeOutcome(r domain.Run, nr *domain.NodeRun, generation, attempt int, success bool) bool {
	if r.Generation != generation || nr == nil {
		return false
	}
	if nr.Status == "succeeded" || nr.Status == "waiting_approval" {
		return false
	}
	if success {
		return true
	}
	return nr.Attempt == attempt
}

func (e *Engine) nodeFailedAt(runID string, node domain.WorkflowNode, attempt, generation int, err error, allowReject bool) {
	shouldReject := false
	configurationFailure := errors.Is(err, ErrWorkspaceControlConfiguration)
	e.updateRun(runID, func(r *domain.Run) {
		nr := r.Nodes[node.ID]
		if !shouldCommitNodeOutcome(*r, nr, generation, attempt, false) {
			return
		}
		nr.Error = err.Error()
		nr.FinishedAt = time.Now().UTC()
		reworkBudget := node.RejectTo != "" && r.Nodes[node.RejectTo] != nil && r.Nodes[node.RejectTo].ReworkCount >= max(1, node.MaxReworks)
		if shouldRetryFailedAttempt(node, nr.Attempt, err) && !gateReworkReject(allowReject, nr) {
			// The retry gets a clean workspace, so carry the concrete failure into
			// {{feedback}} instead of making the next attempt rediscover it.
			nr.Feedback = err.Error()
			nr.Status = "pending"
			nr.ChildRunID = ""
			e.event(r, node.ID, "node.retry", "执行失败，准备重试："+err.Error())
		} else if !configurationFailure && gateReworkReject(allowReject, nr) && reworkBudget {
			// The agent keeps asking for upstream rework but the RejectTo node
			// has burned its MaxReworks budget. Terminating as failed would hide
			// a routing-capable verdict behind an infrastructure cap, so park
			// the node on a human gate instead: approve accepts the current
			// upstream output and continues; anything else stays a human call.
			nr.Status = "waiting_approval"
			nr.Activity = "返工预算耗尽，等待人工决策"
			nr.Error = fmt.Sprintf("上游节点 %s 返工次数已用尽（MaxReworks=%d），无法再次打回，等待人工决策（批准=接受当前结果继续下游）。Agent 理由：%s", node.RejectTo, max(1, node.MaxReworks), err.Error())
			e.event(r, node.ID, "gate.waiting", "返工预算耗尽，等待人工决策："+err.Error())
		} else {
			nr.Status = "failed"
			e.event(r, node.ID, "node.failed", err.Error())
		}
		shouldReject = !configurationFailure && allowReject && nr.Status == "failed" && node.RejectTo != "" && r.Nodes[node.RejectTo] != nil && r.Nodes[node.RejectTo].ReworkCount < max(1, node.MaxReworks)
	})
	if shouldReject {
		_ = e.Reject(runID, domain.RejectRequest{NodeID: node.ID, TargetID: node.RejectTo, Reason: "节点失败自动打回：" + err.Error()})
	}
}

func shouldRetryFailedAttempt(node domain.WorkflowNode, attempt int, err error) bool {
	if errors.Is(err, ErrWorkspaceControlConfiguration) {
		return false
	}
	if err != nil && (isMaxTurnsErr(err) || strings.Contains(err.Error(), "verification ") || runtime.IsCwdMountFailure(err)) {
		return false
	}
	return attempt < max(1, node.MaxAttempts)
}

// gateReworkReject reports whether this failure is the node's explicit
// submit_gate_result "rework" verdict and the engine can actually route it
// upstream. The agent already concluded the fix belongs to the RejectTo node,
// so retrying this node would burn local attempts on a terminal decision
// instead of sending the work back.
func gateReworkReject(allowReject bool, nr *domain.NodeRun) bool {
	return allowReject && nr != nil && nr.GateResult != nil && strings.EqualFold(strings.TrimSpace(nr.GateResult.Decision), "rework")
}

func resolveGateOnFailure(gateOnFailure, fallback string) string {
	for _, candidate := range []string{gateOnFailure, fallback} {
		switch strings.ToLower(strings.TrimSpace(candidate)) {
		case "loop", "reject", "fail", "human":
			return strings.ToLower(strings.TrimSpace(candidate))
		}
	}
	return "fail"
}

func isMaxTurnsErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, runtime.ErrMaxTurns) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "max turns") || strings.Contains(msg, "executor reached max turns")
}

var errGongfengIssueClosed = fmt.Errorf("工蜂 Issue 已关闭，不能触发 SOP")

func forbidClosedGongfengIssue(st *repo.Store, r domain.Run) error {
	id := strings.TrimSpace(r.ExecutionContext.IssueID)
	if id == "" {
		return nil
	}
	issue, ok := st.Issue(id)
	if ok && issue.GongfengClosed() {
		return errGongfengIssueClosed
	}
	return nil
}

func nodeRejects(node domain.WorkflowNode) bool {
	return node.RejectTo != "" && (node.OnFailure == "" || node.OnFailure == "reject")
}

func allowRejectOnFailure(node domain.WorkflowNode, err error, gateRejected bool) bool {
	if gateRejected {
		return true
	}
	if !nodeRejects(node) || err == nil {
		return false
	}
	if node.Type == "script" || node.Type == "command" {
		return true
	}
	msg := err.Error()
	if strings.Contains(msg, "without a terminal result") {
		return false
	}
	if isMaxTurnsErr(err) {
		return false
	}
	return strings.Contains(msg, "verification ")
}

func mergeArtifactContracts(agentContracts, nodeContracts []domain.ArtifactContract) []domain.ArtifactContract {
	merged := make([]domain.ArtifactContract, 0, len(agentContracts)+len(nodeContracts))
	index := map[string]int{}
	for _, contract := range append(append([]domain.ArtifactContract{}, agentContracts...), nodeContracts...) {
		key := strings.TrimSpace(contract.Name)
		if key == "" {
			key = strings.TrimSpace(contract.Path)
		}
		if i, exists := index[key]; exists {
			merged[i] = contract // the SOP node may refine an Agent-owned contract
			continue
		}
		index[key] = len(merged)
		merged = append(merged, contract)
	}
	return merged
}

func (e *Engine) nodeCancelled(runID string, node domain.WorkflowNode) {
	e.updateRun(runID, func(r *domain.Run) {
		nr := r.Nodes[node.ID]
		if r.PauseRequested {
			nr.Status = "waiting_approval"
			nr.Error = ""
			nr.Activity = "已由用户暂停，等待补充信息"
			if isScriptNode(node) {
				nr.Activity = "已由用户暂停，继续后脚本将从头执行"
			}
		} else {
			nr.Status = "cancelled"
			nr.Error = ""
			nr.Activity = "已由用户取消"
		}
		nr.FinishedAt = time.Now().UTC()
		if r.PauseRequested {
			e.event(r, node.ID, "node.paused", node.Name+" 已暂停")
		} else {
			e.event(r, node.ID, "node.cancelled", node.Name+" 已取消")
		}
	})
}

func (e *Engine) finishInterruptedAt(runID string, generation int) {
	current, ok := e.repo.RunState(runID)
	if ok && current.PauseRequested {
		e.pauseAt(runID, generation, "运行已由用户暂停")
		return
	}
	if ok && current.CancelRequested {
		e.finishAt(runID, generation, "cancelled", "流水线已取消")
		return
	}
	if ok {
		status, msg, done := settleStatusForCompletedGraph(workflowForRun(e.repo, current), current)
		if done && !(status == "succeeded" && scheduledIntegratePending(current)) {
			e.finishAt(runID, generation, status, msg)
			return
		}
	}
	e.parkForResumeAt(runID, generation, "执行实例正在退出，已挂起等待接管")
}

func (e *Engine) parkForResumeAt(runID string, generation int, message string) {
	e.stopRunScheduler(runID)
	parked := false
	e.updateRun(runID, func(r *domain.Run) {
		if r.Generation != generation || ((r.OwnerInstanceID != "" || r.Generation > 0) && !e.ownsRun(*r)) {
			return
		}
		if isTerminalRunStatus(r.Status) {
			return
		}
		resetInFlightNodesForResume(r, "执行实例退出，等待恢复")
		r.Status = "running"
		e.event(r, "", "run.parked", message)
		parked = true
	})
	if parked {
		e.detachRunEnvironment(runID)
		e.observe(runID, "", domain.TelemetryEvent{Category: "orchestration", Name: "run.parked", Phase: "finish", Message: message})
	}
}

func (e *Engine) requeueInterruptedNode(runID, nodeID string, attempt, generation int) {
	e.updateRun(runID, func(r *domain.Run) {
		nr := r.Nodes[nodeID]
		if !shouldCommitNodeOutcome(*r, nr, generation, attempt, false) {
			return
		}
		if nr.Status != "running" && nr.Status != "pending_input" {
			return
		}
		resetNodeForResume(r, nr, "沙箱执行中断，等待恢复")
		r.Status = "running"
		e.event(r, nodeID, "node.interrupted", "执行中断，节点已挂起等待恢复")
	})
}

func (e *Engine) pauseAt(runID string, generation int, message string) {
	e.stopRunScheduler(runID)
	e.updateRun(runID, func(r *domain.Run) {
		if r.Generation != generation || ((r.OwnerInstanceID != "" || r.Generation > 0) && !e.ownsRun(*r)) {
			return
		}
		now := time.Now().UTC()
		for nodeID, nr := range r.Nodes {
			if nr == nil || (nr.Status != "running" && nr.Status != "pending_input") {
				continue
			}
			nr.Status = "waiting_approval"
			nr.Error = ""
			nr.Activity = "已由用户暂停，等待补充信息"
			for _, node := range r.WorkflowSnapshot.Nodes {
				if node.ID == nodeID && isScriptNode(node) {
					nr.Activity = "已由用户暂停，继续后脚本将从头执行"
					break
				}
			}
			nr.FinishedAt = now
			e.event(r, nodeID, "node.paused", "执行已暂停，等待人工补充信息")
		}
		r.Status = "waiting"
		r.CancelRequested = false
		r.PauseRequested = true
		e.event(r, "", "run.paused", message)
	})
}

func (e *Engine) finish(id, status, msg string) {
	r, ok := e.repo.RunState(id)
	if !ok {
		return
	}
	e.finishAt(id, r.Generation, status, msg)
}

// stalePipelineFailure reports that a "graph failed" settle is looking at a
// failure the rework has already cleared. Stopping the scheduler or writing
// run.failed in that window aborts the rewind.
func (e *Engine) stalePipelineFailure(id, status, msg string) bool {
	if status != "failed" || msg != pipelineFailedMessage {
		return false
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	current, ok := e.repo.RunState(id)
	if !ok {
		return false
	}
	return !hasFailed(workflowForRun(e.repo, current), current)
}

func (e *Engine) finishAt(id string, generation int, status, msg string) {
	if e.stalePipelineFailure(id, status, msg) {
		return
	}
	e.stopRunScheduler(id)
	ignored := false
	var delivered *domain.Run
	var deliveredAt time.Time
	if !e.updateRunSettling(id, func(r *domain.Run) {
		unowned := (r.OwnerInstanceID != "" || r.Generation > 0) && !e.ownsRun(*r)
		if (r.Generation != generation || unowned) && !canSettleUnowned(*r, status) {
			ignored = true
			return
		}
		if isTerminalRunStatus(r.Status) {
			ignored = true
			return
		}
		if status == "failed" && msg == pipelineFailedMessage && !hasFailed(workflowForRun(e.repo, *r), *r) {
			ignored = true
			e.event(r, "", "run.finish_ignored", "失败节点已被打回，忽略过期的失败收尾")
			return
		}
		if status == "failed" {
			for _, nr := range r.Nodes {
				if nr != nil && (nr.Status == "running" || nr.Status == "waiting_approval" || nr.Status == "pending_input") {
					ignored = true
					e.event(r, "", "run.finish_ignored", "仍有活动节点，忽略过期的失败收尾")
					return
				}
			}
		}
		// 运行被取消或失败时，把仍停留在"执行中"的节点收敛为已取消。
		// 否则一旦执行协程卡死（如子进程无超时阻塞），节点会残留在 running，
		// 使后续重试的 resumeNodeID 既选不到失败/取消节点、也选不到就绪节点，
		// 最终报 "retry node not found"。
		if status == "cancelled" || status == "failed" {
			for nodeID, nr := range r.Nodes {
				if nr == nil || nr.Status != "running" {
					continue
				}
				nr.Status = "cancelled"
				if nr.FinishedAt.IsZero() {
					nr.FinishedAt = time.Now().UTC()
				}
				if strings.TrimSpace(nr.Activity) == "" {
					nr.Activity = "运行终止时仍在执行，已收敛为已取消"
				}
				e.event(r, nodeID, "node.cancelled", "运行终止，节点由执行中收敛为已取消")
			}
		}
		r.Status = status
		e.event(r, "", "run."+status, msg)
		// A child SOP shares the parent's issue; only the top-level run delivers it.
		if status == "succeeded" && r.ExecutionContext.IssueID != "" && r.ExecutionContext.ParentRunID == "" {
			snapshot := *r
			delivered, deliveredAt = &snapshot, time.Now().UTC()
		}
	}) || ignored {
		return
	}
	// Resolving MRs calls Gongfeng, so it runs outside the run state lock.
	if delivered != nil {
		_ = e.repo.MarkIssueDeliveredFromSOPMRs(delivered.ExecutionContext.IssueID, e.deliveryMRsFromRun(*delivered), deliveredAt)
	}
	// Failed runs can be retried in the same workspace. Successful and
	// cancelled runs release their workspace immediately to keep control-plane
	// and low-capacity Runner disks bounded.
	if status == "succeeded" || status == "cancelled" {
		e.observe(id, "", domain.TelemetryEvent{Category: "sandbox", Name: "sandbox.closed", Phase: "finish", Message: "run sandbox released"})
		if status == "succeeded" && e.preview != nil {
			if current, ok := e.repo.RunState(id); ok && e.preview.BeginFinalization(current) {
				return
			}
		}
		e.cleanupRunWorkspace(id)
	}
}
