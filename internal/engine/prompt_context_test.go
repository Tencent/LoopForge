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
	for _, role := range []*domain.AgentDefinition{nil, {Name: "分析师"}, {Prompt: "完成分析"}, {Prompt: "{{input}}\n{{upstream}}"}} {
		got := applyPromptPlaceholders(nil, wf, node, role, run)
		for _, content := range []string{node.TaskPrompt, run.Input, "已完成前置调研"} {
			if strings.Count(got, content) != 1 {
				t.Fatalf("task context must appear exactly once (%q): %s", content, got)
			}
		}
	}
}
