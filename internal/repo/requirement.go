package repo

import (
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// RequirementSnapshot aggregates a requirement's issue, runs, tasks and bound
// workflows into the read-only progress view. It lives on AnalyticsRepo (the
// read-only cross-domain analytics plane), not on Store.
func (r *AnalyticsRepo) RequirementSnapshot(issueID string) (domain.RequirementSnapshot, bool) {
	issue, ok := r.issues.Issue(strings.TrimSpace(issueID))
	if !ok {
		return domain.RequirementSnapshot{}, false
	}
	runs, _ := r.runs.RunSummariesFor(domain.AnalyticsFilter{}, issue.ID)
	if r.DB != nil && len(runs) > 0 {
		ids := make([]string, 0, len(runs))
		for _, run := range runs {
			ids = append(ids, run.ID)
		}
		if snapshots, err := queryAnalyticsWorkflowSnapshots(r.DB, ids); err == nil {
			for i := range runs {
				if snapshot, ok := snapshots[runs[i].ID]; ok {
					runs[i].WorkflowSnapshot = snapshot
				}
			}
		}
	}
	workflows := map[string]domain.WorkflowDefinition{}
	for _, workflow := range r.catalog.Workflows() {
		workflows[workflow.ID] = workflow
	}
	var tasks []domain.Task
	for _, task := range r.tasks.Tasks() {
		if task.IssueID == issue.ID {
			tasks = append(tasks, task)
		}
	}
	project, _ := r.projects.Project(issue.ProjectID)
	return BuildRequirementSnapshot(issue, project, runs, tasks, workflows, time.Now().UTC()), true
}

func BuildRequirementSnapshot(issue domain.Issue, project domain.Project, runs []domain.Run, tasks []domain.Task, workflows map[string]domain.WorkflowDefinition, now time.Time) domain.RequirementSnapshot {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	top, nested := splitNestedRuns(runs)
	lanes := buildRequirementLanes(issue, project, top, nested, tasks, workflows, now)
	overall := requirementOverall(issue, lanes, top, nested, now)
	return domain.RequirementSnapshot{
		Issue:   issue,
		Overall: overall,
		Lanes:   lanes,
		Stages:  requirementLifecycle(issue, lanes),
	}
}

// splitNestedRuns separates runs started by an SOP node from top-level runs.
// A child shares its parent's issue, so without this it would show up as a
// separate lane. The result maps a parent run ID to its child runs.
func splitNestedRuns(runs []domain.Run) ([]domain.Run, map[string][]domain.Run) {
	ids := make(map[string]bool, len(runs))
	for _, run := range runs {
		ids[run.ID] = true
	}
	top := make([]domain.Run, 0, len(runs))
	nested := map[string][]domain.Run{}
	for _, run := range runs {
		parent := strings.TrimSpace(run.ExecutionContext.ParentRunID)
		if parent != "" && parent != run.ID && ids[parent] {
			nested[parent] = append(nested[parent], run)
			continue
		}
		top = append(top, run)
	}
	return top, nested
}

func nestedRunUsage(runID string, nested map[string][]domain.Run, depth int) (int64, float64) {
	if depth > maxNestedStageDepth {
		return 0, 0
	}
	var tokens int64
	var cost float64
	for _, child := range nested[runID] {
		t, c := nestedRunUsage(child.ID, nested, depth+1)
		tokens += child.Usage.TotalTokens + t
		cost += child.Usage.CostUSD + c
	}
	return tokens, cost
}

func buildRequirementLanes(issue domain.Issue, project domain.Project, runs []domain.Run, nested map[string][]domain.Run, tasks []domain.Task, workflows map[string]domain.WorkflowDefinition, now time.Time) []domain.RequirementLane {
	type wfAcc struct {
		lane      domain.RequirementLane
		latest    time.Time
		latestRun domain.Run
	}
	byWF := map[string]*wfAcc{}
	accumulate := func(workflowID string, workflow domain.WorkflowDefinition) *wfAcc {
		workflowID = strings.TrimSpace(workflowID)
		if workflowID == "" {
			return nil
		}
		acc := byWF[workflowID]
		if acc == nil {
			if workflow.ID == "" {
				workflow = workflows[workflowID]
			}
			lane := domain.InferSOPLane(workflow)
			name := workflow.Name
			if name == "" {
				name = workflowID
			}
			acc = &wfAcc{lane: domain.RequirementLane{
				Lane: lane, Label: name, WorkflowID: workflowID, WorkflowName: name,
			}}
			byWF[workflowID] = acc
		}
		return acc
	}
	for _, run := range runs {
		workflow := run.WorkflowSnapshot
		if workflow.ID == "" {
			workflow = workflows[run.WorkflowID]
		}
		acc := accumulate(run.WorkflowID, workflow)
		if acc == nil {
			continue
		}
		acc.lane.Started = true
		acc.lane.RunCount++
		childTokens, childCost := nestedRunUsage(run.ID, nested, 0)
		acc.lane.Tokens += run.Usage.TotalTokens + childTokens
		acc.lane.CostUSD += run.Usage.CostUSD + childCost
		acc.lane.ExecutionDurationMs += runExecutionDuration(run)
		if run.Status == "failed" {
			acc.lane.FailedRuns++
		}
		if acc.latest.IsZero() || run.CreatedAt.After(acc.latest) {
			acc.latest = run.CreatedAt
			acc.latestRun = run
			acc.lane.LatestRunID = run.ID
			if acc.lane.WorkflowName == "" || acc.lane.WorkflowName == run.WorkflowID {
				if workflow.Name != "" {
					acc.lane.WorkflowName = workflow.Name
					acc.lane.Label = workflow.Name
				}
			}
		}
	}
	for _, task := range tasks {
		workflow := workflows[task.WorkflowID]
		acc := accumulate(task.WorkflowID, workflow)
		if acc == nil {
			continue
		}
		if acc.latest.IsZero() || task.CreatedAt.After(acc.latest) {
			acc.lane.LatestTaskID = task.ID
		} else if acc.lane.LatestRunID != "" {
			for _, runTask := range tasks {
				if runTask.RunID == acc.lane.LatestRunID {
					acc.lane.LatestTaskID = runTask.ID
					break
				}
			}
		}
	}
	for _, acc := range byWF {
		if acc.latestRun.ID != "" {
			acc.lane.Status = laneStatusFromRun(acc.latestRun)
			acc.lane.Stages = laneNodeStages(acc.latestRun, workflows, nested, now, 0)
			if acc.lane.Status == "active" && stagesWaitForHuman(acc.lane.Stages) {
				acc.lane.Status = "waiting"
			}
		} else if acc.lane.LatestTaskID != "" {
			acc.lane.Started = true
			acc.lane.Status = "active"
			if len(acc.lane.Stages) == 0 {
				acc.lane.Stages = plannedNodeStages(workflows[acc.lane.WorkflowID], workflows, 0)
			}
		} else {
			acc.lane.Status = "not_started"
			acc.lane.Stages = plannedNodeStages(workflows[acc.lane.WorkflowID], workflows, 0)
		}
	}
	bindings := domain.ResolveSOPBindings(issue, project)
	out := make([]domain.RequirementLane, 0, len(bindings)+len(byWF))
	bound := map[string]bool{}
	for _, binding := range bindings {
		bound[binding.WorkflowID] = true
		lane := domain.RequirementLane{
			BindingID: binding.ID, WorkflowID: binding.WorkflowID, Status: "not_started",
			AssigneeUserID: binding.AssigneeUserID, Order: binding.Order,
			DependsOn: binding.DependsOn, Handoff: binding.Handoff,
		}
		workflow := workflows[binding.WorkflowID]
		lane.Lane = domain.InferSOPLane(workflow)
		if workflow.ID == "" {
			lane.Lane = domain.InferSOPLane(domain.WorkflowDefinition{ID: binding.WorkflowID})
		}
		lane.WorkflowName = workflow.Name
		if lane.WorkflowName == "" {
			lane.WorkflowName = binding.WorkflowID
		}
		lane.Label = lane.WorkflowName
		if acc := byWF[binding.WorkflowID]; acc != nil {
			copied := acc.lane
			copied.BindingID = binding.ID
			copied.AssigneeUserID = binding.AssigneeUserID
			copied.Order = binding.Order
			copied.DependsOn = binding.DependsOn
			copied.Handoff = binding.Handoff
			if copied.Label == "" {
				copied.Label = lane.Label
			}
			if len(copied.Stages) == 0 {
				copied.Stages = plannedNodeStages(workflow, workflows, 0)
			}
			out = append(out, copied)
			continue
		}
		lane.Stages = plannedNodeStages(workflow, workflows, 0)
		out = append(out, lane)
	}
	// A single binding with one started workflow is one slot. That run's
	// frozen graph is the slot, even when its workflow id differs. Two
	// workflows that both started stay as separate lanes.
	if len(bindings) == 1 {
		started := 0
		var latest *wfAcc
		for _, acc := range byWF {
			if !acc.lane.Started {
				continue
			}
			started++
			if latest == nil || acc.latest.After(latest.latest) {
				latest = acc
			}
		}
		if latest != nil && started == 1 {
			if latest.lane.WorkflowID == bindings[0].WorkflowID {
				return out
			}
			lane := latest.lane
			lane.Orphan = false
			lane.BindingID = bindings[0].ID
			lane.AssigneeUserID = bindings[0].AssigneeUserID
			lane.Order = bindings[0].Order
			lane.DependsOn = bindings[0].DependsOn
			lane.Handoff = bindings[0].Handoff
			return []domain.RequirementLane{lane}
		}
	}
	var orphans []domain.RequirementLane
	for wf, acc := range byWF {
		if bound[wf] || !acc.lane.Started {
			continue
		}
		orphan := acc.lane
		orphan.Orphan = true
		orphans = append(orphans, orphan)
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].WorkflowName < orphans[j].WorkflowName })
	out = append(out, orphans...)
	return out
}

func laneStatusFromRun(run domain.Run) string {
	if run.ID == "" {
		return "not_started"
	}
	for _, node := range run.Nodes {
		if node == nil {
			continue
		}
		if node.Status == "failed" {
			return "attention"
		}
		if node.Status == "waiting_approval" {
			return "waiting"
		}
	}
	switch run.Status {
	case "running", "queued":
		return "active"
	case "waiting":
		return "waiting"
	case "succeeded":
		return "succeeded"
	case "failed", "cancelled":
		return "attention"
	default:
		return "active"
	}
}

// orderNodesByEdges returns workflow.Nodes in execution order: a stable
// topological sort over the edges (among nodes whose dependencies are met,
// the original array order wins). The saved nodes array order is whatever the
// Studio editor happened to serialize, so without this the progress view
// lists nodes in a different order than the engine actually runs them.
// Nodes unreachable from the edges (or cyclic graphs) keep array order.
func orderNodesByEdges(workflow domain.WorkflowDefinition) []domain.WorkflowNode {
	nodes := workflow.Nodes
	if len(nodes) < 2 || len(workflow.Edges) == 0 {
		return nodes
	}
	known := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if node.ID == "" {
			// Degenerate definition; keep the stored order untouched.
			return nodes
		}
		known[node.ID] = true
	}
	indegree := make(map[string]int, len(nodes))
	children := map[string][]string{}
	for _, edge := range workflow.Edges {
		if edge.Source == edge.Target || !known[edge.Source] || !known[edge.Target] {
			continue
		}
		indegree[edge.Target]++
		children[edge.Source] = append(children[edge.Source], edge.Target)
	}
	placed := make(map[string]bool, len(nodes))
	out := make([]domain.WorkflowNode, 0, len(nodes))
	for len(out) < len(nodes) {
		progress := false
		for _, node := range nodes {
			if placed[node.ID] || indegree[node.ID] > 0 {
				continue
			}
			out = append(out, node)
			placed[node.ID] = true
			for _, child := range children[node.ID] {
				indegree[child]--
			}
			progress = true
			break
		}
		if !progress {
			// Cycle or dangling dependency: append the rest in array order.
			for _, node := range nodes {
				if !placed[node.ID] {
					out = append(out, node)
					placed[node.ID] = true
				}
			}
		}
	}
	return out
}

func plannedNodeStages(workflow domain.WorkflowDefinition, workflows map[string]domain.WorkflowDefinition, depth int) []domain.RequirementNodeStage {
	out := make([]domain.RequirementNodeStage, 0, len(workflow.Nodes))
	for _, node := range orderNodesByEdges(workflow) {
		name := node.Name
		if name == "" {
			name = node.ID
		}
		if name == "" {
			continue
		}
		item := domain.RequirementNodeStage{NodeID: node.ID, Name: name, Status: "pending"}
		item.Children = plannedSOPChildren(node, workflows, depth)
		out = append(out, item)
	}
	return out
}

func plannedSOPChildren(node domain.WorkflowNode, workflows map[string]domain.WorkflowDefinition, depth int) []domain.RequirementNodeStage {
	ref := strings.TrimSpace(node.SOPWorkflowID)
	if node.Type != "sop" || ref == "" || depth >= maxNestedStageDepth {
		return nil
	}
	child, ok := workflows[ref]
	if !ok {
		return nil
	}
	return plannedNodeStages(child, workflows, depth+1)
}

const maxNestedStageDepth = 6

func stagesWaitForHuman(stages []domain.RequirementNodeStage) bool {
	for _, stage := range stages {
		if stage.Status == "waiting_approval" || stagesWaitForHuman(stage.Children) {
			return true
		}
	}
	return false
}

// nodeChildRun returns the child run the SOP node currently references. A node
// whose link was cleared by a reset shows its planned stages, not an orphaned
// older child. Reset and retry both clear ChildRunID while leaving the node
// "pending", so a pending node never falls back. For any other status a
// missing ChildRunID only means the summary projection dropped the field (or
// the row predates it), so recover via the node's most recent nested run.
func nodeChildRun(parent domain.Run, runs []domain.Run, nodeID string) (domain.Run, bool) {
	if nr := parent.Nodes[nodeID]; nr != nil && nr.ChildRunID != "" {
		for _, run := range runs {
			if run.ID == nr.ChildRunID {
				return run, true
			}
		}
	}
	if nr := parent.Nodes[nodeID]; nr != nil && nr.Status == "pending" {
		return domain.Run{}, false
	}
	return latestNestedRun(runs, nodeID)
}

func latestNestedRun(runs []domain.Run, nodeID string) (domain.Run, bool) {
	var latest domain.Run
	for _, run := range runs {
		if run.ExecutionContext.ParentNodeID != nodeID {
			continue
		}
		if latest.ID == "" || run.CreatedAt.After(latest.CreatedAt) {
			latest = run
		}
	}
	return latest, latest.ID != ""
}

func laneNodeStages(run domain.Run, workflows map[string]domain.WorkflowDefinition, nested map[string][]domain.Run, now time.Time, depth int) []domain.RequirementNodeStage {
	workflow := run.WorkflowSnapshot
	if workflow.ID == "" {
		workflow = workflows[run.WorkflowID]
	}
	withChild := func(item domain.RequirementNodeStage, node domain.WorkflowNode) domain.RequirementNodeStage {
		if depth >= maxNestedStageDepth {
			return item
		}
		if child, ok := nodeChildRun(run, nested[run.ID], item.NodeID); ok {
			item.ChildRunID = child.ID
			item.Children = laneNodeStages(child, workflows, nested, now, depth+1)
		} else {
			item.Children = plannedSOPChildren(node, workflows, depth)
		}
		return item
	}
	out := make([]domain.RequirementNodeStage, 0, len(workflow.Nodes))
	for _, node := range orderNodesByEdges(workflow) {
		item := domain.RequirementNodeStage{NodeID: node.ID, Name: node.Name, Status: "pending"}
		if item.Name == "" {
			item.Name = node.ID
		}
		if nr := run.Nodes[node.ID]; nr != nil {
			item.Status = nr.Status
			if !nr.StartedAt.IsZero() && !nr.FinishedAt.IsZero() && nr.FinishedAt.After(nr.StartedAt) {
				item.DurationMs = nr.FinishedAt.Sub(nr.StartedAt).Milliseconds()
			} else if !nr.StartedAt.IsZero() && nr.Status == "running" && now.After(nr.StartedAt) {
				item.DurationMs = now.Sub(nr.StartedAt).Milliseconds()
			}
		}
		out = append(out, withChild(item, node))
	}
	if len(out) == 0 {
		ids := make([]string, 0, len(run.Nodes))
		for id := range run.Nodes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			nr := run.Nodes[id]
			if nr == nil {
				continue
			}
			out = append(out, withChild(domain.RequirementNodeStage{NodeID: id, Name: id, Status: nr.Status}, domain.WorkflowNode{}))
		}
	}
	return out
}

// requirementOverall counts top-level runs only. Child runs add their usage
// but not their time, which already lies inside the parent run.
func requirementOverall(issue domain.Issue, lanes []domain.RequirementLane, runs []domain.Run, nested map[string][]domain.Run, now time.Time) domain.RequirementOverall {
	out := domain.RequirementOverall{Status: "not_started"}
	for _, children := range nested {
		for _, child := range children {
			out.Tokens += child.Usage.TotalTokens
			out.CostUSD += child.Usage.CostUSD
			if usageCostKnown(child.Usage) {
				out.CostKnown = true
			}
		}
	}
	var first, last time.Time
	for _, run := range runs {
		out.RunCount++
		out.Tokens += run.Usage.TotalTokens
		out.CostUSD += run.Usage.CostUSD
		if usageCostKnown(run.Usage) {
			out.CostKnown = true
		}
		out.ExecutionDurationMs += runExecutionDuration(run)
		if run.Status == "failed" {
			out.FailedRuns++
		}
		if first.IsZero() || run.CreatedAt.Before(first) {
			first = run.CreatedAt
		}
		ended := runEndedAt(run, now)
		if ended.After(last) {
			last = ended
		}
	}
	out.FirstRunAt = first
	out.LastRunAt = last
	if !first.IsZero() && last.After(first) {
		out.CycleDurationMs = last.Sub(first).Milliseconds()
	}
	for _, lane := range lanes {
		switch lane.Status {
		case "succeeded":
			out.SucceededLanes++
		case "active", "waiting":
			out.ActiveLanes++
		}
	}
	out.Status = overallRequirementStatus(issue, lanes)
	return out
}

func overallRequirementStatus(issue domain.Issue, lanes []domain.RequirementLane) string {
	if issue.DeliveryLocked() {
		return "closed"
	}
	rank := map[string]int{"attention": 0, "waiting": 1, "active": 2, "succeeded": 3, "closed": 4, "not_started": 5}
	best := "not_started"
	started := 0
	for _, lane := range lanes {
		if lane.Started {
			started++
		}
		if r, ok := rank[lane.Status]; ok && r < rank[best] {
			best = lane.Status
		}
	}
	if started == 0 {
		return "not_started"
	}
	return best
}

func requirementLifecycle(issue domain.Issue, lanes []domain.RequirementLane) []domain.RequirementStage {
	primary := primaryRequirementLane(issue, lanes)
	if primary == nil {
		return nil
	}
	if len(primary.Stages) > 0 {
		out := make([]domain.RequirementStage, 0, len(primary.Stages))
		for _, node := range primary.Stages {
			label := node.Name
			if label == "" {
				label = node.NodeID
			}
			out = append(out, domain.RequirementStage{Key: node.NodeID, Label: label, Status: node.Status})
		}
		return out
	}
	status := primary.Status
	if status == "" {
		status = "not_started"
	}
	label := primary.Label
	if label == "" {
		label = primary.WorkflowName
	}
	if label == "" {
		label = domain.SOPLaneLabel(primary.Lane)
	}
	key := primary.WorkflowID
	if key == "" {
		key = primary.BindingID
	}
	return []domain.RequirementStage{{Key: key, Label: label, Status: status, Hint: primary.WorkflowName}}
}

func primaryRequirementLane(issue domain.Issue, lanes []domain.RequirementLane) *domain.RequirementLane {
	want := strings.TrimSpace(issue.WorkflowID)
	if want != "" {
		for i := range lanes {
			if lanes[i].WorkflowID == want && !lanes[i].Orphan {
				return &lanes[i]
			}
		}
	}
	for i := range lanes {
		if !lanes[i].Orphan {
			return &lanes[i]
		}
	}
	if len(lanes) > 0 {
		return &lanes[0]
	}
	return nil
}
