package engine

import (
	"agentflow/internal/domain"
	"strings"
	"testing"
)

// Approval instructions stay with approval nodes. The run input and upstream
// output reach the agent exactly once.
func TestRunContextReachesRolesAndNodeTaskPromptDoesNot(t *testing.T) {
	t.Parallel()
	wf := domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{{Source: "source", Target: "report"}}}
	node := domain.WorkflowNode{ID: "report", TaskPrompt: "撰写中文简报"}
	run := domain.Run{Input: "验证图片、PPT 和网页", Nodes: map[string]*domain.NodeRun{"source": {Output: "已完成前置调研"}}}
	for _, role := range []*domain.AgentDefinition{nil, {Name: "分析师"}, {Prompt: "完成分析"}, {Prompt: "{{input}}\n{{upstream}}"}} {
		got := applyPromptPlaceholders(nil, wf, node, role, run)
		if strings.Contains(got, node.TaskPrompt) || strings.Contains(got, "本节点任务") {
			t.Fatalf("node taskPrompt must not reach a role node: %s", got)
		}
		for _, content := range []string{run.Input, "已完成前置调研"} {
			if strings.Count(got, content) != 1 {
				t.Fatalf("run context must appear exactly once (%q): %s", content, got)
			}
		}
	}
}
