package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestPromptRendersEachHumanContextNoteOnce(t *testing.T) {
	t.Parallel()
	retry := func(r *domain.Run, reason, context string) {
		r.Nodes["build"].Feedback = recordHumanContext(r, "build", reason, context)
	}
	render := func(r domain.Run) string {
		return renderPromptSpec(domain.WorkflowDefinition{ID: "lab"}, domain.WorkflowNode{ID: "build"}, "do work", r)
	}
	newRun := func() domain.Run {
		return domain.Run{Input: "input", Nodes: map[string]*domain.NodeRun{"build": {Attempt: 1}}}
	}

	t.Run("reason and context", func(t *testing.T) {
		r := newRun()
		retry(&r, "人工确认重试本节点", "[CTX1] 请把错误信息改成中文")
		prompt := render(r)
		if got := strings.Count(prompt, "[CTX1]"); got != 1 {
			t.Fatalf("context copies = %d, prompt:\n%s", got, prompt)
		}
		if !strings.Contains(prompt, "上一轮反馈：人工确认重试本节点\n") || !strings.Contains(prompt, "## 人工补充上下文") {
			t.Fatalf("reason or section missing:\n%s", prompt)
		}
	})

	t.Run("context only", func(t *testing.T) {
		r := newRun()
		retry(&r, "", "[CTX2] 只补充上下文")
		prompt := render(r)
		if got := strings.Count(prompt, "[CTX2]"); got != 1 || strings.Contains(prompt, humanContextLabel) {
			t.Fatalf("context copies = %d, prompt:\n%s", got, prompt)
		}
	})

	t.Run("successive retries", func(t *testing.T) {
		r := newRun()
		retry(&r, "第一次重试", "[CTX3] 第一条")
		retry(&r, "第二次重试", "[CTX4] 第二条")
		prompt := render(r)
		for _, marker := range []string{"[CTX3]", "[CTX4]"} {
			if got := strings.Count(prompt, marker); got != 1 {
				t.Fatalf("%s copies = %d, prompt:\n%s", marker, got, prompt)
			}
		}
		if !strings.Contains(prompt, "上一轮反馈：第二次重试\n") {
			t.Fatalf("latest reason missing:\n%s", prompt)
		}
	})

	t.Run("resume restores earlier feedback", func(t *testing.T) {
		r := newRun()
		earlier := recordHumanContext(&r, "build", "切回 SOP", "[CTX5] 早先的说明")
		note := recordHumanContext(&r, "build", "", "[CTX6] 继续时的说明")
		r.Nodes["build"].Feedback = strings.TrimSpace(earlier + "\n" + note)
		prompt := render(r)
		for _, marker := range []string{"[CTX5]", "[CTX6]"} {
			if got := strings.Count(prompt, marker); got != 1 {
				t.Fatalf("%s copies = %d, prompt:\n%s", marker, got, prompt)
			}
		}
		if !strings.Contains(prompt, "上一轮反馈：切回 SOP\n") {
			t.Fatalf("restored reason missing:\n%s", prompt)
		}
	})

	t.Run("feedback without human context is untouched", func(t *testing.T) {
		r := newRun()
		r.HumanContext = "[CTX7] 来自别的节点"
		r.Nodes["build"].Feedback = "门禁失败：go test 退出 1"
		prompt := render(r)
		if !strings.Contains(prompt, "上一轮反馈：门禁失败：go test 退出 1\n") || strings.Count(prompt, "[CTX7]") != 1 {
			t.Fatalf("prompt:\n%s", prompt)
		}
	})

	t.Run("no human context keeps feedback verbatim", func(t *testing.T) {
		r := newRun()
		r.Nodes["build"].Feedback = "人工补充上下文：\n不是本次运行记录的说明"
		prompt := render(r)
		if !strings.Contains(prompt, "上一轮反馈：人工补充上下文：\n不是本次运行记录的说明") || strings.Contains(prompt, "## 人工补充上下文") {
			t.Fatalf("prompt:\n%s", prompt)
		}
	})
}
