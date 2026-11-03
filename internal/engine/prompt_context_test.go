package engine

import (
	"agentflow/internal/domain"
	"strings"
	"testing"
)

func TestNodeTaskAndRunContextReachRolesWithoutTaskbooks(t *testing.T) {
	t.Parallel()
	wf := domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{{Source: "source", Target: "report"}}}
	node := domain.WorkflowNode{ID: "report", TaskPrompt: "撰写中文简报"}
	run := domain.Run{Input: "验证图片、PPT 和网页", Nodes: map[string]*domain.NodeRun{"source": {Output: "已完成前置调研"}}}
	for _, spec := range []*domain.TaskPromptSpec{nil, {Objective: "完成分析"}, {Inputs: []string{"{{input}}", "{{upstream}}"}}} {
		got := applyPromptPlaceholders(nil, wf, node, spec, nil, run)
		for _, content := range []string{node.TaskPrompt, run.Input, "已完成前置调研"} {
			if strings.Count(got, content) != 1 {
				t.Fatalf("task context must appear exactly once (%q): %s", content, got)
			}
		}
	}
}
