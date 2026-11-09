package domain

import (
	"strings"
	"testing"
)

func TestApprovalWaitReason(t *testing.T) {
	run := Run{WorkflowSnapshot: WorkflowDefinition{
		Name: "通用后端交付 SOP",
		Nodes: []WorkflowNode{
			{ID: "impl", Name: "代码实现"}, {ID: "test", Name: "测试"},
			{ID: "gate", Name: "上线确认", Type: "approval", RejectTo: "impl", TaskPrompt: "检查测试报告和 MR 改动范围"},
			{ID: "release", Name: "发布"},
		},
		Edges: []WorkflowEdge{{Source: "impl", Target: "gate"}, {Source: "test", Target: "gate"}, {Source: "gate", Target: "release"}},
	}}
	got := ApprovalWaitReason(run, "gate")
	want := "SOP「通用后端交付 SOP」在这一步设置了人工门禁：「代码实现」、「测试」已完成，需要有权限的成员检查结果后确认，确认后继续「发布」；不通过可打回到「代码实现」重做。确认要求：检查测试报告和 MR 改动范围"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	run.WorkflowSnapshot.Edges = nil
	run.WorkflowSnapshot.Nodes[2].TaskPrompt = ""
	run.WorkflowSnapshot.Nodes[2].RejectTo = ""
	if got := ApprovalWaitReason(run, "gate"); !strings.Contains(got, "「测试」已完成") || !strings.Contains(got, "确认后继续「发布」") || strings.Contains(got, "确认要求") {
		t.Fatalf("list-order fallback: %s", got)
	}
	if ApprovalWaitReason(run, "missing") != "" {
		t.Fatal("unknown node must yield empty reason")
	}
}
