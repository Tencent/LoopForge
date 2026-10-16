package engine

import (
	"log"
	"strings"
	"time"

	"agentflow/internal/domain"
)

func continuingInterruptedAttempt(nr *domain.NodeRun) bool {
	if nr == nil || nr.Attempt <= 0 || strings.TrimSpace(nr.Error) != "" {
		return false
	}
	return strings.Contains(nr.Activity, "等待恢复")
}

func resetNodeForResume(r *domain.Run, nr *domain.NodeRun, activity string) {
	if nr == nil {
		return
	}
	switch nr.Status {
	case "running", "pending_input":
	default:
		return
	}
	nr.Status = "pending"
	nr.Error = ""
	nr.Activity = activity
	script := false
	for _, node := range r.WorkflowSnapshot.Nodes {
		if node.ID == nr.NodeID && isScriptNode(node) {
			script = true
			break
		}
	}
	if script {
		nr.ResumeSessionID = ""
		return
	}
	if sid := strings.TrimSpace(nr.SessionID); sid != "" {
		nr.ResumeSessionID = sid
	}
}

func resetInFlightNodesForResume(r *domain.Run, activity string) {
	if r == nil {
		return
	}
	for _, nr := range r.Nodes {
		resetNodeForResume(r, nr, activity)
	}
}

func isTerminalRunStatus(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func hasRunningNode(r domain.Run) bool {
	for _, nr := range r.Nodes {
		if nr != nil && (nr.Status == "running" || nr.Status == "pending_input") {
			return true
		}
	}
	return false
}

// settleStatusForCompletedGraph reports the terminal run status when every
// snapshot node is already finished and nothing is in flight. An empty graph
// is not settled: ResumeInterruptedRuns uses that case to recover orphans
// whose workflow snapshot was never frozen.
func settleStatusForCompletedGraph(wf domain.WorkflowDefinition, r domain.Run) (status, msg string, ok bool) {
	if wf.CollaborationMode == "group" || len(wf.Nodes) == 0 || hasRunningNode(r) {
		return "", "", false
	}
	if allSucceeded(wf, r) {
		return "succeeded", "流水线执行完成", true
	}
	if hasWaiting(wf, r) {
		return "", "", false
	}
	if hasFailed(wf, r) {
		return "failed", pipelineFailedMessage, true
	}
	return "", "", false
}

// pipelineFailedMessage is the settle reason used when a node is still failed
// and nothing is left to run. A rework can clear that failure before the
// settle is written; callers must drop the settle in that case.
const pipelineFailedMessage = "流水线执行失败"

// scheduledIntegratePending reports that a scheduled run still has to merge its
// work branch into the integration branch. Node success is not the end of that
// run: integrateScheduledRun performs the merge afterwards. Marking the run
// succeeded in that window deletes the checkout while the push is using it.
func scheduledIntegratePending(r domain.Run) bool {
	if strings.TrimSpace(r.ExecutionContext.ScheduleJobID) == "" {
		return false
	}
	for _, rc := range r.RepositoryContexts() {
		if strings.TrimSpace(rc.IntegrateBranch) != "" && strings.TrimSpace(rc.WorkBranch) != "" {
			return true
		}
	}
	return false
}

func canSettleUnowned(r domain.Run, status string) bool {
	if status == "succeeded" && scheduledIntegratePending(r) {
		return false
	}
	got, _, ok := settleStatusForCompletedGraph(workflowForRun(nil, r), r)
	return ok && got == status
}

func (e *Engine) stopRunScheduler(runID string) {
	e.mu.Lock()
	cancel := e.running[runID]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *Engine) isRunSchedulerActive(runID string) bool {
	e.mu.Lock()
	_, ok := e.running[runID]
	e.mu.Unlock()
	return ok
}

// forceTerminate settles a run whose aggregate status disagrees with in-flight
// workers, for example after a partial failure or split-brain recovery.
func (e *Engine) forceTerminate(runID, status, msg string) {
	e.stopRunScheduler(runID)
	e.updateRun(runID, func(r *domain.Run) {
		now := time.Now().UTC()
		for _, nr := range r.Nodes {
			if nr == nil {
				continue
			}
			switch nr.Status {
			case "running", "pending_input", "waiting_approval":
				if status == "cancelled" {
					nr.Status = "cancelled"
					nr.Error = ""
					nr.Activity = "已由用户取消"
				} else {
					nr.Status = "failed"
					nr.Error = msg
				}
				nr.FinishedAt = now
			}
		}
		if status == "cancelled" || !isTerminalRunStatus(r.Status) {
			r.Status = status
			e.event(r, "", "run."+status, msg)
		}
	})
	if status == "succeeded" || status == "cancelled" {
		if status == "succeeded" && e.preview != nil {
			if current, ok := e.repo.RunState(runID); ok && e.preview.BeginFinalization(current) {
				return
			}
		}
		e.cleanupRunWorkspace(runID)
	}
}

func orphanSettleStatus(reason string) string {
	if strings.Contains(reason, "手动终止") || strings.Contains(reason, "取消") {
		return "cancelled"
	}
	return "failed"
}

// deliveryMRsFromRun resolves one delivery MR per run repository: Gongfeng
// lookup by work branch first, MR-bearing artifacts of the same project next.
func (e *Engine) deliveryMRsFromRun(run domain.Run) []domain.DeliveryMR {
	var byBranch func(domain.RepositoryContext) (domain.DeliveryMR, bool)
	if e.FindBranchMR != nil {
		byBranch = func(rc domain.RepositoryContext) (domain.DeliveryMR, bool) {
			mr, ok, err := e.FindBranchMR(rc.GongfengProjectID, rc.WorkBranch)
			if err != nil {
				log.Printf("resolve delivery MR run=%s project=%s branch=%s: %v", run.ID, rc.GongfengProjectID, rc.WorkBranch, err)
				return domain.DeliveryMR{}, false
			}
			return mr, ok
		}
	}
	return domain.ResolveRunDeliveryMRs(run, byBranch)
}
