package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestBuildRequirementSnapshotGroupsLanes(t *testing.T) {
	now := time.Now().UTC()
	issue := domain.Issue{ID: "iss-1", ProjectID: "p1", Kind: "requirement", Title: "多泳道"}
	product := domain.WorkflowDefinition{ID: "wf-product", Name: "产品 SOP", SOPLane: "product", Nodes: []domain.WorkflowNode{{ID: "prd", Name: "写 PRD"}}}
	backend := domain.WorkflowDefinition{ID: "wf-backend", Name: "后端 SOP", SOPLane: "backend", Nodes: []domain.WorkflowNode{{ID: "code", Name: "实现"}}}
	frontend := domain.WorkflowDefinition{ID: "wf-frontend", Name: "前端 SOP", SOPLane: "frontend", Nodes: []domain.WorkflowNode{{ID: "ui", Name: "页面"}}}
	runs := []domain.Run{
		{
			ID: "run-p", WorkflowID: product.ID, WorkflowSnapshot: product, Status: "succeeded",
			Usage:     domain.TokenUsage{TotalTokens: 10, CostUSD: 0.1, CostStatus: "reported"},
			Nodes:     map[string]*domain.NodeRun{"prd": {NodeID: "prd", Status: "succeeded", StartedAt: now, FinishedAt: now.Add(time.Second)}},
			CreatedAt: now, UpdatedAt: now.Add(time.Second),
		},
		{
			ID: "run-b", WorkflowID: backend.ID, WorkflowSnapshot: backend, Status: "running",
			Usage:     domain.TokenUsage{TotalTokens: 20, CostUSD: 0.2, CostStatus: "reported"},
			Nodes:     map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "running", StartedAt: now.Add(time.Minute)}},
			CreatedAt: now.Add(time.Minute),
		},
	}
	workflows := map[string]domain.WorkflowDefinition{product.ID: product, backend.ID: backend, frontend.ID: frontend}
	snap := BuildRequirementSnapshot(issue, domain.Project{}, runs, nil, workflows, now.Add(2*time.Minute))
	if snap.Overall.RunCount != 2 || snap.Overall.Tokens != 30 {
		t.Fatalf("overall=%#v", snap.Overall)
	}
	if snap.Overall.Status != "active" {
		t.Fatalf("overall status=%q", snap.Overall.Status)
	}
	if len(snap.Lanes) != 2 {
		t.Fatalf("lanes=%d %#v", len(snap.Lanes), snap.Lanes)
	}
	if snap.Lanes[0].WorkflowID != product.ID || !snap.Lanes[0].Started || snap.Lanes[0].Status != "succeeded" {
		t.Fatalf("product lane=%#v", snap.Lanes[0])
	}
	if snap.Lanes[1].WorkflowID != backend.ID || snap.Lanes[1].Status != "active" || snap.Lanes[1].Tokens != 20 {
		t.Fatalf("backend lane=%#v", snap.Lanes[1])
	}
	if len(snap.Stages) == 0 || snap.Stages[0].Key == "clarify" {
		t.Fatalf("stages must be the bound SOP nodes, not 澄清: %#v", snap.Stages)
	}
}

func TestBuildRequirementSnapshotNestsChildSOPRuns(t *testing.T) {
	now := time.Now().UTC()
	issue := domain.Issue{ID: "iss-n", ProjectID: "p1", Kind: "requirement", WorkflowID: "wf-parent"}
	child := domain.WorkflowDefinition{ID: "wf-mcp", Name: "MCP测试", Nodes: []domain.WorkflowNode{{ID: "probe", Name: "探测"}, {ID: "report", Name: "汇报"}}}
	parent := domain.WorkflowDefinition{ID: "wf-parent", Name: "多 SOP 串联", Nodes: []domain.WorkflowNode{
		{ID: "mcp1", Name: "MCP测试", Type: "sop", SOPWorkflowID: child.ID},
		{ID: "gate", Name: "AI 门禁"},
		{ID: "mcp2", Name: "MCP测试", Type: "sop", SOPWorkflowID: child.ID},
	}}
	runs := []domain.Run{
		{
			ID: "run-parent", WorkflowID: parent.ID, WorkflowSnapshot: parent, Status: "running",
			Usage:     domain.TokenUsage{TotalTokens: 5},
			Nodes:     map[string]*domain.NodeRun{"mcp1": {NodeID: "mcp1", Status: "running", StartedAt: now, ChildRunID: "run-child"}},
			CreatedAt: now,
		},
		{
			ID: "run-child", WorkflowID: child.ID, WorkflowSnapshot: child, Status: "running",
			ExecutionContext: domain.TelemetryContext{IssueID: issue.ID, ParentRunID: "run-parent", ParentNodeID: "mcp1"},
			Usage:            domain.TokenUsage{TotalTokens: 7},
			Nodes:            map[string]*domain.NodeRun{"probe": {NodeID: "probe", Status: "succeeded"}, "report": {NodeID: "report", Status: "running"}},
			CreatedAt:        now.Add(time.Second),
		},
	}
	workflows := map[string]domain.WorkflowDefinition{parent.ID: parent, child.ID: child}
	snap := BuildRequirementSnapshot(issue, domain.Project{}, runs, nil, workflows, now.Add(time.Minute))
	if len(snap.Lanes) != 1 || snap.Lanes[0].Orphan {
		t.Fatalf("a child SOP run must not become its own lane: %#v", snap.Lanes)
	}
	if snap.Overall.RunCount != 1 || snap.Overall.Tokens != 12 || snap.Lanes[0].Tokens != 12 {
		t.Fatalf("overall=%#v lane tokens=%d", snap.Overall, snap.Lanes[0].Tokens)
	}
	stages := snap.Lanes[0].Stages
	if len(stages) != 3 {
		t.Fatalf("stages=%#v", stages)
	}
	first := stages[0]
	if first.ChildRunID != "run-child" || len(first.Children) != 2 || first.Children[0].Status != "succeeded" || first.Children[1].Status != "running" {
		t.Fatalf("running SOP node must carry its child run stages: %#v", first)
	}
	if snap.Lanes[0].Status != "active" {
		t.Fatalf("lane status=%q", snap.Lanes[0].Status)
	}
	runs[1].Nodes["report"].Status = "waiting_approval"
	if waiting := BuildRequirementSnapshot(issue, domain.Project{}, runs, nil, workflows, now.Add(time.Minute)); waiting.Lanes[0].Status != "waiting" {
		t.Fatalf("a child waiting for approval must surface on the parent lane: %q", waiting.Lanes[0].Status)
	}
	last := stages[2]
	if last.ChildRunID != "" || len(last.Children) != 2 || last.Children[0].Status != "pending" {
		t.Fatalf("an SOP node that has not started must list the planned child nodes: %#v", last)
	}
	// A reset cleared the node's link: the orphaned child must neither show as
	// the node's stages nor hold the lane in "waiting".
	runs[0].Nodes["mcp1"] = &domain.NodeRun{NodeID: "mcp1", Status: "pending"}
	reset := BuildRequirementSnapshot(issue, domain.Project{}, runs, nil, workflows, now.Add(time.Minute))
	if got := reset.Lanes[0].Stages[0]; got.ChildRunID != "" || got.Children[0].Status != "pending" {
		t.Fatalf("reset SOP node still shows the orphaned child: %#v", got)
	}
	if reset.Lanes[0].Status == "waiting" {
		t.Fatalf("an orphaned child's approval must not mark the lane waiting")
	}
}

func TestOrderNodesByEdgesFollowsExecutionOrder(t *testing.T) {
	// The Studio canvas order (nodes array) differs from the wired execution
	// order (edges): env runs first, then grep, then the rest.
	workflow := domain.WorkflowDefinition{
		ID: "wf-order", Nodes: []domain.WorkflowNode{
			{ID: "grep", Name: "Zevc-Grep索引建立"},
			{ID: "design", Name: "方案设计"},
			{ID: "env", Name: "准备环境"},
			{ID: "dev", Name: "开发实践"},
		},
		Edges: []domain.WorkflowEdge{
			{ID: "e1", Source: "env", Target: "grep"},
			{ID: "e2", Source: "grep", Target: "design"},
			{ID: "e3", Source: "design", Target: "dev"},
		},
	}
	issue := domain.Issue{ID: "iss-order", ProjectID: "p1", Kind: "requirement", WorkflowID: workflow.ID}
	snap := BuildRequirementSnapshot(issue, domain.Project{}, nil, nil, map[string]domain.WorkflowDefinition{workflow.ID: workflow}, time.Now().UTC())
	got := snap.Lanes[0].Stages
	want := []string{"准备环境", "Zevc-Grep索引建立", "方案设计", "开发实践"}
	if len(got) != len(want) {
		t.Fatalf("stages=%d", len(got))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("stage %d = %q, want %q", i, got[i].Name, name)
		}
	}
	// Cyclic edges must not drop or reorder into an infinite loop: the array
	// order is the last-resort fallback.
	cyclic := workflow
	cyclic.Edges = append(cyclic.Edges, domain.WorkflowEdge{ID: "e4", Source: "dev", Target: "env"})
	if got := orderNodesByEdges(cyclic); len(got) != 4 {
		t.Fatalf("cyclic graph must keep all nodes: %d", len(got))
	}
}

func TestNodeChildRunFallsBackWithoutChildRunID(t *testing.T) {
	now := time.Now().UTC()
	child := domain.Run{
		ID: "run-child", Status: "running",
		ExecutionContext: domain.TelemetryContext{ParentRunID: "run-parent", ParentNodeID: "gate"},
	}
	// Summary projection dropped ChildRunID but the node is running.
	parent := domain.Run{
		ID: "run-parent", Status: "running",
		Nodes: map[string]*domain.NodeRun{"gate": {NodeID: "gate", Status: "running", StartedAt: now}},
	}
	if got, ok := nodeChildRun(parent, []domain.Run{child}, "gate"); !ok || got.ID != "run-child" {
		t.Fatalf("running node without projected ChildRunID must fall back to the nested run: %q %v", got.ID, ok)
	}
	// A pending node keeps its planned stages even when an older nested run exists.
	parent.Nodes["gate"] = &domain.NodeRun{NodeID: "gate", Status: "pending"}
	if _, ok := nodeChildRun(parent, []domain.Run{child}, "gate"); ok {
		t.Fatalf("pending node must not adopt an orphaned child run")
	}
}

func TestBuildRequirementSnapshotUsesBindings(t *testing.T) {
	design := domain.WorkflowDefinition{ID: "wf-design", Name: "设计 SOP", SOPLane: "design"}
	backend := domain.WorkflowDefinition{ID: "wf-backend", Name: "后端 SOP", SOPLane: "backend"}
	frontend := domain.WorkflowDefinition{ID: "wf-frontend", Name: "前端 SOP", SOPLane: "frontend"}
	issue := domain.Issue{
		ID: "iss-bind", Kind: "requirement", WorkflowID: backend.ID,
		SOPBindings: []domain.IssueSOPBinding{
			{WorkflowID: design.ID, Order: 1, AssigneeUserID: 8},
			{WorkflowID: backend.ID, Order: 2, AssigneeUserID: 8, Handoff: []string{"design.md"}},
		},
	}
	snap := BuildRequirementSnapshot(issue, domain.Project{}, nil, nil, map[string]domain.WorkflowDefinition{
		design.ID: design, backend.ID: backend, frontend.ID: frontend,
	}, time.Now().UTC())
	if len(snap.Lanes) != 2 {
		t.Fatalf("lanes=%#v", snap.Lanes)
	}
	if snap.Lanes[0].WorkflowID != design.ID || snap.Lanes[0].AssigneeUserID != 8 || snap.Lanes[0].Started {
		t.Fatalf("design=%#v", snap.Lanes[0])
	}
	if snap.Lanes[1].WorkflowID != backend.ID || snap.Lanes[1].Handoff[0] != "design.md" {
		t.Fatalf("backend=%#v", snap.Lanes[1])
	}
	if len(snap.Stages) != 0 && snap.Stages[0].Key == "clarify" {
		t.Fatalf("stages must not start with 澄清: %#v", snap.Stages)
	}
}

func TestBuildRequirementSnapshotSingleBindingUsesFrozenRun(t *testing.T) {
	now := time.Now().UTC()
	bound := domain.WorkflowDefinition{ID: "issue-fix-production", Name: "Issue 修复 · 生产闭环"}
	frozen := domain.WorkflowDefinition{ID: "sop-sandbox", Name: "Issue修复·沙箱", Nodes: []domain.WorkflowNode{{ID: "comments", Name: "评论"}}}
	issue := domain.Issue{ID: "iss-one", Kind: "issue", WorkflowID: bound.ID}
	runs := []domain.Run{{
		ID: "run-sandbox", WorkflowID: frozen.ID, WorkflowSnapshot: frozen, Status: "succeeded",
		CreatedAt: now, UpdatedAt: now,
	}}
	snap := BuildRequirementSnapshot(issue, domain.Project{}, runs, nil, map[string]domain.WorkflowDefinition{
		bound.ID: bound, frozen.ID: frozen,
	}, now.Add(time.Minute))
	if len(snap.Lanes) != 1 || snap.Lanes[0].Orphan || snap.Lanes[0].WorkflowID != frozen.ID || snap.Lanes[0].WorkflowName != frozen.Name {
		t.Fatalf("single SOP must be the frozen run, not a second binding: %#v", snap.Lanes)
	}
}

func TestBuildRequirementSnapshotFollowsBoundWorkflow(t *testing.T) {
	product := domain.WorkflowDefinition{ID: "wf-product", Name: "产品 SOP", SOPLane: "product", Nodes: []domain.WorkflowNode{{ID: "prd", Name: "写 PRD"}}}
	backend := domain.WorkflowDefinition{ID: "wf-backend", Name: "后端 SOP", SOPLane: "backend", Nodes: []domain.WorkflowNode{{ID: "code", Name: "实现"}, {ID: "gate", Name: "门禁"}}}
	issue := domain.Issue{ID: "iss-inherit", Kind: "requirement", WorkflowID: backend.ID, AssigneeUserID: 3}
	project := domain.Project{ID: "p1", SOPBindings: []domain.IssueSOPBinding{
		{WorkflowID: product.ID, Order: 1},
		{WorkflowID: backend.ID, Order: 2},
	}}
	snap := BuildRequirementSnapshot(issue, project, nil, nil, map[string]domain.WorkflowDefinition{
		product.ID: product, backend.ID: backend,
	}, time.Now().UTC())
	if len(snap.Lanes) != 1 || snap.Lanes[0].WorkflowID != backend.ID {
		t.Fatalf("lanes=%#v", snap.Lanes)
	}
	if snap.Lanes[0].AssigneeUserID != 3 {
		t.Fatalf("owner=%d", snap.Lanes[0].AssigneeUserID)
	}
	if len(snap.Lanes[0].Stages) != 2 || snap.Lanes[0].Stages[0].Name != "实现" || snap.Lanes[0].Stages[0].Status != "pending" {
		t.Fatalf("planned nodes=%#v", snap.Lanes[0].Stages)
	}
	if len(snap.Stages) != 2 || snap.Stages[0].Label != "实现" || snap.Stages[0].Key == "clarify" {
		t.Fatalf("lifecycle=%#v", snap.Stages)
	}
}

func TestRequirementSnapshotHTTPStore(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "generic-frontend-delivery", Name: "前端交付", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "shape", Name: "造型"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "iss-req", ProjectID: "platform", Kind: "requirement", Title: "看泳道"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.PutRun(domain.Run{
		ID: "run-fe", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{IssueID: "iss-req", ProjectID: "platform"},
		Usage:            domain.TokenUsage{TotalTokens: 5, CostUSD: 0.01, CostStatus: "reported"},
		Nodes:            map[string]*domain.NodeRun{"shape": {NodeID: "shape", Status: "succeeded", StartedAt: now, FinishedAt: now.Add(time.Second)}},
		CreatedAt:        now, UpdatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	snap, ok := s.RequirementSnapshot("iss-req")
	if !ok {
		t.Fatal("missing snapshot")
	}
	var frontend domain.RequirementLane
	for _, lane := range snap.Lanes {
		if lane.Lane == domain.SOPLaneFrontend {
			frontend = lane
		}
	}
	if !frontend.Started || frontend.Status != "succeeded" {
		t.Fatalf("frontend lane=%#v", frontend)
	}
}
