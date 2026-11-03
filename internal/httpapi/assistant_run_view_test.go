package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func diagnosisTestRun() domain.Run {
	prompt := strings.Repeat("节点任务说明，", 400)
	wf := domain.WorkflowDefinition{ID: "wf", Name: "通用后端交付 SOP"}
	for _, n := range []struct{ id, name, typ string }{
		{"requirement", "需求分析", "agent"}, {"design", "方案设计", "agent"}, {"implement", "代码实现", "agent"},
		{"review", "代码评审", "agent"}, {"mr", "提交 MR", "agent"}, {"approve", "上线确认", "approval"},
	} {
		wf.Nodes = append(wf.Nodes, domain.WorkflowNode{ID: n.id, Name: n.name, Type: n.typ, TaskPrompt: prompt})
	}
	return domain.Run{
		ID: "run-1", WorkflowID: "wf", WorkflowSnapshot: wf, Status: "failed",
		ExecutionContext: domain.TelemetryContext{ProjectID: "p", IssueNumber: 7},
		Nodes: map[string]*domain.NodeRun{
			"requirement": {NodeID: "requirement", Status: "succeeded", Attempt: 1, Output: strings.Repeat("需求产出", 2000)},
			"design": {NodeID: "design", Status: "failed", Attempt: 2,
				Error: "repository checkout: git clone https://git.example.com/a/b.git: exit status 128 (fatal: could not read Username for 'https://git.example.com': terminal prompts disabled) (exit 0)"},
			"mr": {NodeID: "mr", Status: "waiting_approval", Attempt: 1,
				Error:      "agent requested human confirmation: 推送被拒绝",
				GateResult: &domain.GateResult{Decision: "human", Reason: "运行账号对仓库没有写权限", ActionRequired: "把运行账号加为 Developer 后重跑"}},
			"approve": {NodeID: "approve", Status: "waiting_approval"},
		},
		UpdatedAt: time.Now(),
	}
}

func TestAssistantRunDetailKeepsDiagnosis(t *testing.T) {
	run := diagnosisTestRun()
	raw, _ := json.Marshal(run)
	if !strings.Contains(string(raw), "could not read Username") {
		t.Fatal("fixture missing git error")
	}
	out := string((&assistantHandlers{}).assistantRunResult("runs_get", raw))
	if len(out) >= len(raw) || !strings.Contains(out, "could not read Username") {
		t.Fatal("diagnosis view must drop the bulky snapshot and keep the git error")
	}
	var view assistantRunView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	if view.WorkflowName != "通用后端交付 SOP" || len(view.Nodes) != 6 || view.Nodes[1].ID != "design" || view.Nodes[1].Name != "方案设计" {
		t.Fatalf("nodes must follow workflow order with names: %+v", view.Nodes)
	}
	if len(view.Problems) != 3 {
		t.Fatalf("problems = %+v", view.Problems)
	}
	design, mr, approve := view.Problems[0], view.Problems[1], view.Problems[2]
	if design.NodeName != "方案设计" || design.Kind != "failed" || !strings.Contains(design.Reason, "could not read Username") {
		t.Fatalf("design problem = %+v", design)
	}
	if design.Credential == nil || design.Credential.Field != "gitToken" || !strings.Contains(strings.Join(design.Credential.Steps, ""), "Pull requests") {
		t.Fatalf("git failure must carry the token fix: %+v", design.Credential)
	}
	if mr.Kind != "waiting_approval" || !strings.Contains(mr.Reason, "没有写权限") || !strings.Contains(mr.Reason, "Developer") {
		t.Fatalf("human gate must explain what it waits for: %+v", mr)
	}
	if !strings.Contains(approve.Reason, "人工门禁") || !strings.Contains(approve.Reason, "「提交 MR」已完成") || !strings.Contains(approve.Reason, "确认后运行结束") {
		t.Fatalf("approval node reason = %q", approve.Reason)
	}
}

func TestAssistantRunListIsCompactAndNewestFirst(t *testing.T) {
	old, recent := diagnosisTestRun(), diagnosisTestRun()
	old.ID, old.UpdatedAt, old.Status = "run-old", time.Now().Add(-time.Hour), "succeeded"
	recent.ID = "run-new"
	raw, _ := json.Marshal([]domain.Run{old, recent})
	out := (&assistantHandlers{}).assistantRunResult("runs_list", raw)
	var list struct {
		Total int                 `json:"total"`
		Runs  []assistantRunBrief `json:"runs"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 || list.Runs[0].ID != "run-new" || list.Runs[0].Progress != "1/6" {
		t.Fatalf("list = %+v", list)
	}
	if len(list.Runs[0].Problems) != 3 || list.Runs[0].Problems[0].Credential == nil || len(list.Runs[1].Problems) != 0 {
		t.Fatalf("problems = %+v / %+v", list.Runs[0].Problems, list.Runs[1].Problems)
	}
	if len(out) > len(raw)/4 {
		t.Fatalf("list view should drop snapshots: %d of %d bytes", len(out), len(raw))
	}
}

func TestAssistantRunResultPassesOtherPayloadsThrough(t *testing.T) {
	body := []byte(`{"id":"x"}`)
	if got := (&assistantHandlers{}).assistantRunResult("issues_get", body); string(got) != string(body) {
		t.Fatalf("got %s", got)
	}
	if got := (&assistantHandlers{}).assistantRunResult("runs_get", []byte(`not json`)); string(got) != "not json" {
		t.Fatalf("got %s", got)
	}
}

func TestAssistantCredentialProblemMatchesGitToken(t *testing.T) {
	fix := assistantCredentialProblem("fatal: could not read Username for 'https://github.com': terminal prompts disabled", "alice")
	if fix == nil || fix.Field != "gitToken" || fix.Owner != "alice" || fix.HelpURL != "https://github.com/settings/tokens" {
		t.Fatalf("fix = %+v", fix)
	}
	if fix := assistantCredentialProblem("当前责任人未配置太湖个人令牌", ""); fix != nil {
		t.Fatalf("legacy Taihu errors must not point at a hidden credential: %+v", fix)
	}
	if assistantCredentialProblem("单元测试失败：expected 1 got 2", "") != nil {
		t.Fatal("ordinary failures must not suggest credentials")
	}
}
