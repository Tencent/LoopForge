package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func activityFixture() (Run, time.Time) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	run := Run{ID: "run", Status: "waiting", WorkflowSnapshot: WorkflowDefinition{CollaborationMode: "group", GroupMembers: []string{"dev", "test", "unused"}, Nodes: []WorkflowNode{{ID: "dev-1", AgentID: "dev", Name: "开发", TaskPrompt: "修复登录", RequestID: "one"}, {ID: "test-1", AgentID: "test", Name: "测试", TaskPrompt: "验证登录", RequestID: "two"}}}, Nodes: map[string]*NodeRun{"dev-1": {NodeID: "dev-1", Status: "succeeded", Attempt: 1, StartedAt: at, FinishedAt: at.Add(time.Minute)}, "test-1": {NodeID: "test-1", Status: "pending"}}, Events: []RunEvent{{NodeID: "dev-1", Type: "group.dispatched", Timestamp: at}, {NodeID: "test-1", Type: "group.dispatched", Timestamp: at.Add(30 * time.Second)}}}
	return run, at
}
func platformGate(node string, at time.Time, level string) TelemetryEvent {
	return TelemetryEvent{ID: "check", NodeID: node, Timestamp: at, Context: TelemetryContext{Attempt: 1}, Category: "verification", Name: "process.finished", Level: level, Attributes: map[string]any{"gateId": "tests", "gateName": "单元测试", "command": "TOKEN=SECRET go test ./..."}}
}

func TestGroupActivityTracksConcurrentMembersAndLatestTask(t *testing.T) {
	run, at := activityFixture()
	run.Nodes["dev-1"].Status = "running"
	run.Nodes["dev-1"].FinishedAt = time.Time{}
	run.WorkflowSnapshot.Nodes = append(run.WorkflowSnapshot.Nodes, WorkflowNode{ID: "dev-2", AgentID: "dev", RequestID: "three", TaskPrompt: "另一项任务"})
	run.Nodes["dev-2"] = &NodeRun{NodeID: "dev-2", Status: "pending"}
	run.Events = append(run.Events, RunEvent{NodeID: "dev-2", Type: "group.dispatched", Timestamp: at.Add(time.Second)})
	run.Telemetry = []TelemetryEvent{{NodeID: "dev-1", Timestamp: at.Add(time.Second), Tool: &ToolObservation{Name: "Edit"}}}
	got := BuildGroupActivity(run, []AgentDefinition{{ID: "dev", Name: "开发"}})
	if len(got.Members) != 3 || got.Members[0].Status != "running" || got.Members[0].ActiveTaskCount != 2 || got.Members[0].NodeID != "dev-1" || got.Members[0].Activity != "正在使用 Edit" {
		t.Fatalf("wrong members: %+v", got.Members)
	}
	if got.Members[1].Status != "waiting" || got.Members[2].Status != "idle" || got.Receipt.Status != "running" {
		t.Fatalf("wrong activity: %+v", got)
	}
}
func TestGroupActivityCannotInferVerificationFromAgentPass(t *testing.T) {
	run, at := activityFixture()
	delete(run.Nodes, "test-1")
	run.Nodes["dev-1"].Output = "all tests passed, pass 100%"
	run.Nodes["dev-1"].GateResult = &GateResult{Decision: "pass"}
	run.Telemetry = []TelemetryEvent{{NodeID: "dev-1", Category: "verification", Name: "gate.submit.received", Timestamp: at.Add(time.Minute), Attributes: map[string]any{"decision": "pass"}}, {NodeID: "dev-1", Category: "tool", Name: "tool.call", Tool: &ToolObservation{Name: "Bash", Success: ptrActivityBool(true)}, Timestamp: at.Add(time.Minute)}}
	got := BuildGroupActivity(run, nil)
	if got.Receipt.Status != "completed" || got.Receipt.Verification.Status != "unverified" || len(got.Receipt.Verification.Checks) != 0 {
		t.Fatalf("agent report fabricated checks: %+v", got.Receipt)
	}
}
func TestGroupActivityKeepsOnlyCurrentBatchAndAttemptArtifacts(t *testing.T) {
	run, at := activityFixture()
	delete(run.Nodes, "test-1")
	run.WorkflowSnapshot.Nodes = append(run.WorkflowSnapshot.Nodes, WorkflowNode{ID: "dev-2", AgentID: "dev", RequestID: "three"})
	run.Nodes["dev-2"] = &NodeRun{NodeID: "dev-2", Status: "succeeded", Attempt: 2, StartedAt: at.Add(2 * time.Minute), FinishedAt: at.Add(3 * time.Minute)}
	run.Artifacts = []Artifact{{ID: "old-batch", NodeID: "dev-1", Attempt: 1, CreatedAt: at.Add(time.Minute)}, {ID: "old-attempt", NodeID: "dev-2", Attempt: 1, CreatedAt: at.Add(150 * time.Second)}, {ID: "current", NodeID: "dev-2", Attempt: 2, CreatedAt: at.Add(3 * time.Minute)}}
	got := BuildGroupActivity(run, nil).Receipt
	if len(got.NodeIDs) != 1 || got.NodeIDs[0] != "dev-2" || len(got.Artifacts) != 1 || got.Artifacts[0].ID != "current" {
		t.Fatalf("mixed execution artifacts: %+v", got)
	}
}
func TestGroupActivityOnlyRealCommandGateMayPassAndCommandsStayPrivate(t *testing.T) {
	run, at := activityFixture()
	delete(run.Nodes, "test-1")
	run.Telemetry = []TelemetryEvent{platformGate("dev-1", at.Add(50*time.Second), "info")}
	got := BuildGroupActivity(run, nil)
	if got.Receipt.Verification.Status != "passed" {
		t.Fatalf("missing gate evidence: %+v", got.Receipt.Verification)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "command") {
		t.Fatalf("command leaked: %s", raw)
	}
	run.Telemetry[0].Level = "error"
	if got := BuildGroupActivity(run, nil).Receipt.Verification; got.Status != "failed" {
		t.Fatalf("failed gate passed: %+v", got)
	}
}
func TestGroupActivityInvalidatesChecksAfterAnyFurtherExecutionOrEdit(t *testing.T) {
	for _, cause := range []string{"later-task", "concurrent-writer", "manual-write", "later-tool", "new-attempt"} {
		t.Run(cause, func(t *testing.T) {
			run, at := activityFixture()
			delete(run.Nodes, "test-1")
			run.Telemetry = []TelemetryEvent{platformGate("dev-1", at.Add(50*time.Second), "info")}
			switch cause {
			case "later-task":
				run.Nodes["test-1"] = &NodeRun{NodeID: "test-1", Status: "succeeded", Attempt: 1, StartedAt: at.Add(55 * time.Second), FinishedAt: at.Add(2 * time.Minute)}
			case "concurrent-writer":
				run.Nodes["test-1"] = &NodeRun{NodeID: "test-1", Status: "running", Attempt: 1, StartedAt: at.Add(20 * time.Second)}
			case "manual-write":
				run.Events = append(run.Events, RunEvent{Type: "workspace.changed", Timestamp: at.Add(2 * time.Minute)})
			case "later-tool":
				run.Telemetry = append(run.Telemetry, TelemetryEvent{NodeID: "dev-1", Category: "tool", Tool: &ToolObservation{Name: "Bash"}, Timestamp: at.Add(55 * time.Second)})
			case "new-attempt":
				run.Nodes["dev-1"].Attempt = 2
				run.Nodes["dev-1"].StartedAt = at.Add(2 * time.Minute)
			}
			got := BuildGroupActivity(run, nil).Receipt.Verification
			if got.Status != "historical" {
				t.Fatalf("stale check represented as current for %s: %+v", cause, got)
			}
		})
	}
}
func TestGroupActivityIgnoresEarlierAttemptWithoutCorrelation(t *testing.T) {
	run, at := activityFixture()
	delete(run.Nodes, "test-1")
	e := platformGate("dev-1", at.Add(-time.Minute), "info")
	e.Context.Attempt = 0
	run.Telemetry = []TelemetryEvent{e}
	if got := BuildGroupActivity(run, nil).Receipt.Verification; got.Status != "unverified" {
		t.Fatalf("old event is current: %+v", got)
	}
}
func ptrActivityBool(b bool) *bool { return &b }

func TestGroupActivityShowsPreviousCheckAsHistoricalInNewBatch(t *testing.T) {
	run, at := activityFixture()
	run.Nodes["test-1"] = &NodeRun{NodeID: "test-1", Status: "succeeded", Attempt: 1, StartedAt: at.Add(2 * time.Minute), FinishedAt: at.Add(3 * time.Minute)}
	run.Telemetry = []TelemetryEvent{platformGate("dev-1", at.Add(50*time.Second), "info")}
	got := BuildGroupActivity(run, nil).Receipt.Verification
	if got.Status != "historical" || len(got.Checks) != 1 || !got.Checks[0].Historical {
		t.Fatalf("previous check no longer shown as historical: %+v", got)
	}
}

func TestGroupActivityTaskShowsUserGoalWithoutDispatchInstructions(t *testing.T) {
	goal := "@开发 把按钮文案改成安全登录"
	wrapped := "群成员 @你：请以自己的身份直接回应并处理这条消息，不要替其他成员发言。\n" + goal
	if got := groupActivityTaskText(wrapped); got != goal {
		t.Fatal(got)
	}
	ordinary := "第一行需求\n第二行需求"
	if got := groupActivityTaskText(ordinary); got != ordinary {
		t.Fatal("removed user content", got)
	}
}
