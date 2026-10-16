package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// GroupActivity is a read-only projection of durable execution facts. It never
// interprets an agent's prose or terminal decision as proof of verification.
type GroupActivity struct {
	RunID             string                `json:"runId"`
	CollaborationMode string                `json:"collaborationMode"`
	Status            string                `json:"status"`
	Members           []GroupMemberActivity `json:"members"`
	Receipt           GroupExecutionReceipt `json:"receipt"`
}

type GroupMemberActivity struct {
	AgentID         string     `json:"agentId"`
	Name            string     `json:"name"`
	Status          string     `json:"status"` // running, waiting, failed, idle
	Activity        string     `json:"activity"`
	NodeID          string     `json:"nodeId,omitempty"`
	Task            string     `json:"task,omitempty"`
	ActiveTaskCount int        `json:"activeTaskCount"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
}

type GroupExecutionReceipt struct {
	Status       string                 `json:"status"` // running, waiting, failed, completed, idle
	NodeIDs      []string               `json:"nodeIds"`
	StartedAt    *time.Time             `json:"startedAt,omitempty"`
	FinishedAt   *time.Time             `json:"finishedAt,omitempty"`
	Artifacts    []GroupReceiptArtifact `json:"artifacts"`
	Verification GroupVerification      `json:"verification"`
}

type GroupReceiptArtifact struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"nodeId"`
	Attempt   int       `json:"attempt"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"createdAt"`
}

type GroupVerification struct {
	Status string                   `json:"status"` // unverified, running, passed, failed, historical
	Reason string                   `json:"reason"`
	Checks []GroupVerificationCheck `json:"checks"`
}

type GroupVerificationCheck struct {
	ID            string     `json:"id"`
	NodeID        string     `json:"nodeId"`
	GateID        string     `json:"gateId"`
	Name          string     `json:"name"`
	Attempt       int        `json:"attempt"`
	RepositoryDir string     `json:"repositoryDir,omitempty"`
	Status        string     `json:"status"` // running, passed, failed
	Historical    bool       `json:"historical"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
}

type groupActivityTask struct {
	node  WorkflowNode
	state NodeRun
	start time.Time
	end   time.Time
}

// BuildGroupActivity has no clock, database or model dependency. Only recorded
// command-gate lifecycle events provide verification evidence. Even those are
// historical once another execution or manual workspace access could change files.
func BuildGroupActivity(run Run, agents []AgentDefinition) GroupActivity {
	tasks := groupActivityTasks(run)
	out := GroupActivity{RunID: run.ID, CollaborationMode: run.WorkflowSnapshot.CollaborationMode, Status: run.Status, Members: []GroupMemberActivity{}}
	names := map[string]string{}
	for _, a := range agents {
		names[a.ID] = a.Name
	}
	for _, id := range run.WorkflowSnapshot.GroupMemberIDs() {
		member := GroupMemberActivity{AgentID: id, Name: names[id], Status: "idle", Activity: "空闲"}
		if member.Name == "" {
			member.Name = id
		}
		var selected *groupActivityTask
		for i := range tasks {
			task := &tasks[i]
			if task.node.AgentID != id {
				continue
			}
			active := groupMemberStatus(task.state.Status) == "running" || groupMemberStatus(task.state.Status) == "waiting"
			if active {
				member.ActiveTaskCount++
			}
			if selected == nil || groupTaskPriority(*task) >= groupTaskPriority(*selected) {
				selected = task
			}
		}
		if selected != nil {
			member.NodeID, member.Task = selected.node.ID, groupActivityTaskText(selected.node.TaskPrompt)
			if member.Task == "" {
				member.Task = selected.node.Name
			}
			member.Status = groupMemberStatus(selected.state.Status)
			member.StartedAt, member.FinishedAt = groupActivityTime(selected.state.StartedAt), groupActivityTime(selected.state.FinishedAt)
			switch member.Status {
			case "running":
				member.Activity = "正在处理任务"
			case "waiting":
				member.Activity = "等待执行"
			case "failed":
				member.Activity = "执行失败，等待处理"
			default:
				member.Activity = "空闲"
			}
			if selected.state.Status == "waiting_approval" {
				member.Activity = "等待确认"
			}
			if selected.state.Status == "waiting_retry" {
				member.Activity = "等待重试"
			}
			if run.PauseRequested && member.Status == "waiting" {
				member.Activity = "协作已暂停"
			}
			if member.Status == "running" {
				for i := len(run.Telemetry) - 1; i >= 0; i-- {
					e := run.Telemetry[i]
					if e.NodeID == selected.node.ID && !e.Timestamp.Before(selected.state.StartedAt) && e.Tool != nil && e.Tool.Name != "" {
						member.Activity = "正在使用 " + groupActivityText(e.Tool.Name, 48)
						break
					}
				}
			}
		}
		out.Members = append(out.Members, member)
	}
	out.Receipt = groupActivityReceipt(run, tasks)
	return out
}

func groupMemberStatus(status string) string {
	switch status {
	case "running":
		return "running"
	case "pending", "queued", "waiting_approval", "waiting_retry", "waiting":
		return "waiting"
	case "failed":
		return "failed"
	default:
		return "idle"
	}
}
func groupTaskPriority(task groupActivityTask) int {
	// Tasks are chronological; selecting equal priority replaces with the latest.
	switch groupMemberStatus(task.state.Status) {
	case "running":
		return 3
	case "waiting":
		return 2
	default:
		return 1
	}
}
func groupActivityTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
func groupActivityTaskText(prompt string) string {
	const mentionPrefix = "群成员 @你：请以自己的身份直接回应并处理这条消息，不要替其他成员发言。\n"
	return groupActivityText(strings.TrimPrefix(prompt, mentionPrefix), 240)
}
func groupActivityText(s string, limit int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return string(r)
}

func groupActivityTasks(run Run) []groupActivityTask {
	dispatch := map[string]time.Time{}
	for _, e := range run.Events {
		if e.Type == "group.dispatched" {
			dispatch[e.NodeID] = e.Timestamp
		}
	}
	tasks := []groupActivityTask{}
	seen := map[string]bool{}
	add := func(node WorkflowNode, state *NodeRun) {
		if state == nil || seen[node.ID] {
			return
		}
		seen[node.ID] = true
		start := state.StartedAt
		if start.IsZero() {
			start = dispatch[node.ID]
		}
		if start.IsZero() && state.Attempt == 0 {
			return
		} // A frozen, unexecuted SOP node is not a group task.
		tasks = append(tasks, groupActivityTask{node: node, state: *state, start: start, end: state.FinishedAt})
	}
	for _, node := range run.WorkflowSnapshot.Nodes {
		add(node, run.Nodes[node.ID])
	}
	for _, history := range run.WorkflowSnapshot.GroupHistory {
		state := history.State
		add(history.Node, &state)
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].start.Equal(tasks[j].start) {
			return tasks[i].node.ID < tasks[j].node.ID
		}
		return tasks[i].start.Before(tasks[j].start)
	})
	return tasks
}

func groupActivityReceipt(run Run, tasks []groupActivityTask) GroupExecutionReceipt {
	receipt := GroupExecutionReceipt{Status: "idle", NodeIDs: []string{}, Artifacts: []GroupReceiptArtifact{}, Verification: GroupVerification{Status: "unverified", Reason: "尚无平台实际执行的检查记录", Checks: []GroupVerificationCheck{}}}
	if len(tasks) == 0 {
		return receipt
	}
	// A batch is one continuous period of overlapping execution, not all old
	// attempts of the task. An idle gap starts a fresh receipt.
	start := 0
	end := tasks[0].end
	open := end.IsZero()
	for i := 1; i < len(tasks); i++ {
		if !open && tasks[i].start.After(end) {
			start = i
			end = tasks[i].end
			open = end.IsZero()
		} else if tasks[i].end.IsZero() {
			open = true
		} else if tasks[i].end.After(end) {
			end = tasks[i].end
		}
	}
	selected := map[string]NodeRun{}
	receipt.Status = "completed"
	for _, task := range tasks[start:] {
		selected[task.node.ID] = task.state
		receipt.NodeIDs = append(receipt.NodeIDs, task.node.ID)
		switch groupMemberStatus(task.state.Status) {
		case "running":
			receipt.Status = "running"
		case "waiting":
			if receipt.Status != "running" {
				receipt.Status = "waiting"
			}
		case "failed":
			if receipt.Status == "completed" {
				receipt.Status = "failed"
			}
		}
	}
	receipt.StartedAt = groupActivityTime(tasks[start].start)
	if !open {
		receipt.FinishedAt = groupActivityTime(end)
	}
	for _, a := range run.Artifacts {
		nr, ok := selected[a.NodeID]
		if !ok || nr.Attempt != a.Attempt || a.CreatedAt.Before(nr.StartedAt) {
			continue
		}
		receipt.Artifacts = append(receipt.Artifacts, GroupReceiptArtifact{ID: a.ID, NodeID: a.NodeID, Attempt: a.Attempt, Name: a.Name, Path: a.Path, CreatedAt: a.CreatedAt})
	}
	sort.Slice(receipt.Artifacts, func(i, j int) bool {
		if receipt.Artifacts[i].CreatedAt.Equal(receipt.Artifacts[j].CreatedAt) {
			return receipt.Artifacts[i].ID < receipt.Artifacts[j].ID
		}
		return receipt.Artifacts[i].CreatedAt.Before(receipt.Artifacts[j].CreatedAt)
	})
	receipt.Verification = groupActivityVerification(run, tasks, selected)
	return receipt
}

func groupActivityVerification(run Run, tasks []groupActivityTask, selected map[string]NodeRun) GroupVerification {
	verification := GroupVerification{Status: "unverified", Reason: "本轮没有平台实际执行的检查；成员完成不代表检查通过", Checks: []GroupVerificationCheck{}}
	// Match each platform gate/repository/iteration independently. Later retries
	// supersede earlier results but stay historical if workspace changed afterwards.
	checks := map[string]GroupVerificationCheck{}
	checkTimes := map[string]time.Time{}
	allStates := map[string]NodeRun{}
	for _, task := range tasks {
		allStates[task.node.ID] = task.state
	}
	hasCurrent := false
	for _, e := range run.Telemetry {
		nr, ok := allStates[e.NodeID]
		_, inBatch := selected[e.NodeID]
		if !ok || e.Category != "verification" || (e.Name != "process.started" && e.Name != "process.finished") || strings.TrimSpace(groupActivityAttr(e.Attributes, "gateId")) == "" || strings.TrimSpace(groupActivityAttr(e.Attributes, "command")) == "" {
			continue
		}
		attempt := e.Context.Attempt
		if attempt == 0 {
			if nr.StartedAt.IsZero() || e.Timestamp.Before(nr.StartedAt) {
				continue
			}
			attempt = nr.Attempt
		}
		// Wrong-attempt events are still useful historical evidence, never current.
		gateID := groupActivityAttr(e.Attributes, "gateId")
		repo := groupActivityAttr(e.Attributes, "repositoryDir")
		key := e.NodeID + "\x00" + gateID + "\x00" + repo
		if t, ok := checkTimes[key]; ok && e.Timestamp.Before(t) {
			continue
		}
		check := GroupVerificationCheck{ID: e.ID, NodeID: e.NodeID, GateID: gateID, Name: groupActivityText(groupActivityAttr(e.Attributes, "gateName"), 100), Attempt: attempt, RepositoryDir: repo, Status: "running"}
		if check.Name == "" {
			check.Name = gateID
		}
		if e.Name == "process.finished" {
			check.Status = "passed"
			check.FinishedAt = groupActivityTime(e.Timestamp)
			if e.Level == "error" || e.Error != nil {
				check.Status = "failed"
			}
		}
		check.Historical = !inBatch || attempt != nr.Attempt || groupActivityChangedAfter(run, tasks, e)
		if inBatch {
			hasCurrent = true
		}
		checks[key], checkTimes[key] = check, e.Timestamp
	}
	for _, check := range checks {
		if hasCurrent {
			if _, ok := selected[check.NodeID]; !ok {
				continue
			}
		}
		verification.Checks = append(verification.Checks, check)
	}
	sort.Slice(verification.Checks, func(i, j int) bool {
		return verification.Checks[i].NodeID+verification.Checks[i].GateID+verification.Checks[i].RepositoryDir < verification.Checks[j].NodeID+verification.Checks[j].GateID+verification.Checks[j].RepositoryDir
	})
	current, failed, running, historical := 0, false, false, false
	for _, check := range verification.Checks {
		if check.Historical {
			historical = true
			continue
		}
		current++
		failed = failed || check.Status == "failed"
		running = running || check.Status == "running"
	}
	switch {
	case failed:
		verification.Status = "failed"
		verification.Reason = "本轮平台检查未通过"
	case running:
		verification.Status = "running"
		verification.Reason = "平台正在执行检查"
	case historical:
		verification.Status = "historical"
		verification.Reason = "上次检查后还有执行或工作区变更，当前文件尚未验证"
	case current > 0:
		verification.Status = "passed"
		verification.Reason = "已记录的平台命令检查通过；不代表未配置的测试已执行"
	}
	return verification
}
func groupActivityAttr(attrs map[string]any, key string) string {
	v := attrs[key]
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func groupActivityChangedAfter(run Run, tasks []groupActivityTask, check TelemetryEvent) bool {
	for _, task := range tasks {
		if task.start.After(check.Timestamp) {
			return true
		}
		if task.node.ID != check.NodeID && (task.end.IsZero() || task.end.After(check.Timestamp)) && !task.start.After(check.Timestamp) {
			return true
		}
	}
	for _, e := range run.Events {
		if e.Type == "workspace.changed" && !e.Timestamp.Before(check.Timestamp) {
			return true
		}
	}
	// An agent can edit during its own correction loop after a gate has finished.
	for _, e := range run.Telemetry {
		if e.Timestamp.After(check.Timestamp) && e.Category == "tool" && e.Tool != nil && groupActivityPotentialWrite(e.Tool.Name) {
			return true
		}
	}
	return false
}
func groupActivityPotentialWrite(name string) bool {
	switch strings.ToLower(name) {
	case "read", "glob", "grep", "toolsearch", "websearch", "webfetch":
		return false
	default:
		return true
	}
}
