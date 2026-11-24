package engine

import (
	"agentflow/internal/domain"
	"strings"
	"testing"
)

// A role node's brief comes from the role prompt alone: the node's taskPrompt
// (an approval node's 「需要确认什么」) never reaches it, while the run input
// and upstream output always do, exactly once.
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

func TestNodePromptAppendsOnlyToAgentTaskbook(t *testing.T) {
	t.Parallel()
	role := &domain.AgentDefinition{Name: "同一角色", Prompt: "负责通用检查"}
	run := domain.Run{}
	first := applyPromptPlaceholders(nil, domain.WorkflowDefinition{}, domain.WorkflowNode{ID: "small", Type: "agent", NodePrompt: "只检查小流量配置"}, role, run)
	second := applyPromptPlaceholders(nil, domain.WorkflowDefinition{}, domain.WorkflowNode{ID: "large", Type: "agent", NodePrompt: "只检查大流量配置"}, role, run)
	if !strings.Contains(first, "## 本节点补充要求\n\n只检查小流量配置") || strings.Contains(first, "只检查大流量配置") {
		t.Fatalf("first node prompt was not isolated: %s", first)
	}
	if !strings.Contains(second, "## 本节点补充要求\n\n只检查大流量配置") || strings.Contains(second, "只检查小流量配置") {
		t.Fatalf("second node prompt was not isolated: %s", second)
	}
	approval := applyPromptPlaceholders(nil, domain.WorkflowDefinition{}, domain.WorkflowNode{ID: "review", Type: "approval", TaskPrompt: "确认结果", NodePrompt: "不应注入"}, role, run)
	if strings.Contains(approval, "不应注入") {
		t.Fatalf("approval node received execution prompt: %s", approval)
	}
}

func TestPromptArtifactPlaceholderIgnoresStaleArtifacts(t *testing.T) {
	t.Parallel()
	wf := domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{{Source: "source", Target: "report", Condition: &domain.WorkflowCondition{Type: "route", Values: []string{"small"}}}}}
	node := domain.WorkflowNode{ID: "report"}
	run := domain.Run{WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{
		"source": {Status: "succeeded", Attempt: 2},
	}, Artifacts: []domain.Artifact{
		{NodeID: "source", Attempt: 1, Name: "classification", Content: "stale"},
		{NodeID: "source", Attempt: 2, Name: "classification", Content: "active"},
	}}
	role := &domain.AgentDefinition{Prompt: "{{artifact.source.classification}}"}
	got := applyPromptPlaceholders(nil, wf, node, role, run)
	if strings.Contains(got, "stale") || !strings.Contains(got, "active") {
		t.Fatalf("prompt artifact selection = %q", got)
	}
}
