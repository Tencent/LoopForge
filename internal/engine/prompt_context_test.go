package engine

import (
	"agentflow/internal/domain"
	"strings"
	"testing"
)

// The operator's note reaches the node once, in the human-context section:
// the copy composeHumanFeedback folded into the retried node's feedback is
// dropped when the prompt is rendered.
func TestHumanContextReachesTheNodeOnce(t *testing.T) {
	t.Parallel()
	wf := domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{{Source: "source", Target: "report"}}}
	node := domain.WorkflowNode{ID: "report"}
	note := "把结论写成人话，别贴原始 JSON"
	role := &domain.AgentDefinition{Prompt: "完成分析"}
	// The retried node: its feedback already carries the note through
	// composeHumanFeedback, and run.HumanContext holds the same text.
	run := domain.Run{Input: "整理数据", HumanContext: note,
		Nodes: map[string]*domain.NodeRun{"report": {Feedback: composeHumanFeedback("补充信息，重跑当前节点", note)}}}
	if got := applyPromptPlaceholders(nil, wf, node, role, run); strings.Count(got, note) != 1 {
		t.Fatalf("human context must appear once: %s", got)
	}
	// A later node whose feedback does not carry the note still receives it.
	run.Nodes = map[string]*domain.NodeRun{"report": {Feedback: "上一轮失败"}}
	if got := applyPromptPlaceholders(nil, wf, node, role, run); strings.Count(got, note) != 1 || !strings.Contains(got, "## 人工补充上下文") {
		t.Fatalf("human context must reach a node without feedback: %s", got)
	}
	// Notes accumulate with "---"; each of them reaches the node once.
	older, newer := "第一轮的补充", "第二轮的补充"
	run.HumanContext = older + "\n\n---\n" + newer
	run.Nodes = map[string]*domain.NodeRun{"report": {Feedback: composeHumanFeedback("", newer)}}
	got := applyPromptPlaceholders(nil, wf, node, role, run)
	if strings.Count(got, newer) != 1 || !strings.Contains(got, older) {
		t.Fatalf("every accumulated note must appear once: %s", got)
	}
	// A note whose text also appears elsewhere in the prompt, such as the run
	// input, still reaches the node as an operator note.
	run.Input = "整理数据，" + note
	run.HumanContext = note
	run.Nodes = map[string]*domain.NodeRun{"report": {Feedback: composeHumanFeedback("补充信息，重跑当前节点", note)}}
	got = applyPromptPlaceholders(nil, wf, node, role, run)
	if strings.Count(got, note) != 2 || !strings.Contains(got, "## 人工补充上下文") || !strings.Contains(got, "补充信息，重跑当前节点") {
		t.Fatalf("an operator note repeated in the input must keep its section: %s", got)
	}
}

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
