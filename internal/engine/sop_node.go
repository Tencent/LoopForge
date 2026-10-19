package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const maxSOPNesting = 6

func (e *Engine) executeSOPNode(ctx context.Context, runID string, wf domain.WorkflowDefinition, node domain.WorkflowNode, attempt, generation int) {
	fail := func(err error) {
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		e.nodeFailedAt(runID, node, attempt, generation, err, nodeRejects(node))
	}
	parent, ok := e.repo.RunState(runID)
	if !ok {
		fail(fmt.Errorf("parent run disappeared"))
		return
	}
	childID := strings.TrimSpace(node.SOPWorkflowID)
	if childID == "" {
		fail(fmt.Errorf("SOP 节点未选择要跑的流程"))
		return
	}
	if err := e.guardSOPCycle(parent, childID); err != nil {
		fail(err)
		return
	}
	if _, found := e.repo.Workflow(childID); !found {
		fail(fmt.Errorf("SOP 流程 %q 不存在", childID))
		return
	}
	var child domain.Run
	if nr := parent.Nodes[node.ID]; nr != nil && nr.ChildRunID != "" {
		// A deploy, pause or lease takeover interrupted the parent while its
		// child kept going (or finished); keep following that child, including
		// its outcome, instead of starting the SOP over. Reset paths that want a
		// fresh child clear ChildRunID.
		if prev, ok := e.repo.RunState(nr.ChildRunID); ok {
			if prev.WorkflowID == childID {
				child = prev
			} else if isLiveStatus(prev.Status) {
				// The node was re-pointed to another SOP (RefreshRunSOP); the old
				// child's work does not belong to this node any more.
				_ = e.Cancel(prev.ID)
			}
		}
	}
	if child.ID == "" {
		childInput := sopChildInput(parent, node)
		exec := parent.ExecutionContext
		exec.ParentRunID = parent.ID
		exec.ParentNodeID = node.ID
		exec.RunID = ""
		exec.TraceID = parent.ExecutionContext.TraceID
		started, err := e.StartWithRepositoriesContext(childID, childInput, parent.RepositoryContexts(), exec)
		if err != nil {
			fail(fmt.Errorf("启动子 SOP：%w", err))
			return
		}
		child = started
		e.updateRun(runID, func(r *domain.Run) {
			if nr := r.Nodes[node.ID]; nr != nil {
				nr.ChildRunID = child.ID
				nr.Activity = "正在执行子 SOP " + childID
			}
			e.event(r, node.ID, "sop.started", node.Name+" 启动子流程 "+childID)
		})
		e.observe(runID, node.ID, domain.TelemetryEvent{
			Category: "orchestration", Name: "sop.child.started", Phase: "start",
			Message:    "子 SOP 已启动",
			Attributes: map[string]any{"childRunId": child.ID, "sopWorkflowId": childID},
		})
	} else {
		e.updateRun(runID, func(r *domain.Run) {
			if nr := r.Nodes[node.ID]; nr != nil {
				nr.Activity = "正在执行子 SOP " + childID
			}
		})
		e.observe(runID, node.ID, domain.TelemetryEvent{
			Category: "orchestration", Name: "sop.child.reattached", Phase: "start",
			Message:    "重新接上仍在运行的子 SOP",
			Attributes: map[string]any{"childRunId": child.ID, "sopWorkflowId": childID},
		})
	}
	finished, err := e.waitChildRun(ctx, runID, child.ID)
	if err != nil {
		fail(err)
		return
	}
	if finished.Status != "succeeded" {
		fail(fmt.Errorf("子 SOP %s %s", childID, finished.Status))
		return
	}
	imported, err := importChildArtifacts(parent.ID, node, attempt, finished)
	if err != nil {
		fail(err)
		return
	}
	e.updateRun(runID, func(r *domain.Run) {
		nr := r.Nodes[node.ID]
		if !shouldCommitNodeOutcome(*r, nr, generation, attempt, true) {
			return
		}
		if nr != nil {
			nr.ChildRunID = finished.ID
		}
		r.Artifacts = append(r.Artifacts, imported...)
		nr.Output = fmt.Sprintf("子 SOP %s 已完成，交接 %d 个产物", childID, len(imported))
		nr.Error = ""
		nr.Status = "succeeded"
		nr.FinishedAt = time.Now().UTC()
		e.event(r, node.ID, "node.succeeded", node.Name+" 已完成")
	})
	for _, artifact := range imported {
		e.observe(runID, node.ID, domain.TelemetryEvent{
			Category: "artifact", Name: "artifact.published", Phase: "finish",
			Message: artifact.Name, Attributes: map[string]any{"artifactId": artifact.ID, "path": artifact.Path, "childRunId": finished.ID},
		})
	}
}

func (e *Engine) guardSOPCycle(parent domain.Run, childWorkflowID string) error {
	childWorkflowID = strings.TrimSpace(childWorkflowID)
	seen := map[string]bool{}
	depth := 0
	current := parent
	for {
		wfID := strings.TrimSpace(current.WorkflowID)
		if wfID != "" {
			if wfID == childWorkflowID {
				return fmt.Errorf("SOP 节点不能循环引用流程 %q", childWorkflowID)
			}
			if seen[wfID] {
				return fmt.Errorf("SOP 嵌套链出现循环")
			}
			seen[wfID] = true
		}
		depth++
		if depth >= maxSOPNesting {
			return fmt.Errorf("SOP 嵌套超过 %d 层", maxSOPNesting)
		}
		parentID := strings.TrimSpace(current.ExecutionContext.ParentRunID)
		if parentID == "" {
			return nil
		}
		next, ok := e.repo.RunState(parentID)
		if !ok {
			return nil
		}
		current = next
	}
}

func sopChildInput(parent domain.Run, node domain.WorkflowNode) string {
	input := strings.TrimSpace(parent.Input)
	prior := ancestors(parent.WorkflowSnapshot, node.ID)
	var names []string
	seen := map[string]bool{}
	for _, art := range parent.Artifacts {
		if art.Name == "" || art.Name == "repository-patch" || seen[art.Name] {
			continue
		}
		if len(prior) > 0 && !prior[art.NodeID] {
			continue
		}
		seen[art.Name] = true
		names = append(names, art.Name)
	}
	if len(node.ArtifactOutputs) > 0 {
		input += "\n\nThis SOP node will publish these artifacts to the next parent step: " + joinArtifactNames(node.ArtifactOutputs)
	}
	if len(names) == 0 {
		return input
	}
	input += "\n\nHandoff from previous nodes in the parent flow:"
	for _, name := range names {
		input += "\n- " + name
	}
	input += "\nContinue from those deliverables; do not redo the previous SOP."
	return input
}

func joinArtifactNames(contracts []domain.ArtifactContract) string {
	names := make([]string, 0, len(contracts))
	for _, c := range contracts {
		if name := strings.TrimSpace(c.Name); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ", ")
}

func (e *Engine) waitChildRun(ctx context.Context, parentID, childID string) (domain.Run, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Drain, pause or lease loss: the child is resumed on its own and the
			// parent re-attaches to it. Only an explicit parent cancel stops it.
			if parent, ok := e.repo.RunState(parentID); ok && (parent.CancelRequested || parent.Status == "cancelled") {
				_ = e.Cancel(childID)
			}
			return domain.Run{}, ctx.Err()
		case <-ticker.C:
			if parent, ok := e.repo.RunState(parentID); ok && (parent.CancelRequested || parent.Status == "cancelled") {
				_ = e.Cancel(childID)
				return domain.Run{}, fmt.Errorf("父流程已取消")
			}
			child, ok := e.repo.RunState(childID)
			if !ok {
				return domain.Run{}, fmt.Errorf("子 SOP 运行消失")
			}
			if isTerminalRunStatus(child.Status) {
				return child, nil
			}
		}
	}
}

func importChildArtifacts(parentID string, node domain.WorkflowNode, attempt int, child domain.Run) ([]domain.Artifact, error) {
	want := map[string]bool{}
	for _, contract := range node.ArtifactOutputs {
		if name := strings.TrimSpace(contract.Name); name != "" {
			want[name] = true
		}
	}
	now := time.Now().UTC()
	// A reworked child node publishes the same name again; hand off the newest.
	latest := map[string]domain.Artifact{}
	var order []string
	for _, art := range child.Artifacts {
		if ref := child.WorkflowSnapshot.GroupSOPHandoff; ref != nil && art.NodeID == groupSOPHandoffNode && (art.ID == ref.SummaryArtifactID || art.ID == ref.TranscriptArtifactID) {
			continue // inherited context is not a child deliverable
		}
		name := strings.TrimSpace(art.Name)
		if name == "" || name == "repository-patch" {
			continue
		}
		if len(want) > 0 && !want[name] {
			continue
		}
		prev, seen := latest[name]
		if !seen {
			order = append(order, name)
		}
		if !seen || art.CreatedAt.After(prev.CreatedAt) {
			latest[name] = art
		}
	}
	imported := make([]domain.Artifact, 0, len(order))
	have := map[string]bool{}
	for _, name := range order {
		art := latest[name]
		have[name] = true
		art.ID = fmt.Sprintf("art-%s-%s-%d", parentID, name, now.UnixNano())
		art.RunID = parentID
		art.NodeID = node.ID
		art.Attempt = attempt
		art.CreatedAt = now
		imported = append(imported, art)
	}
	for name := range want {
		if !have[name] {
			return nil, fmt.Errorf("子 SOP 没有交出产物 %q", name)
		}
	}
	return imported, nil
}
