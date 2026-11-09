package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNativePromptsAreToolFirst(t *testing.T) {
	prompts := []string{
		assistantSystemPromptText(),
		assistantScheduledSystemPromptText(assistantExecMode{AutoWrite: true}),
		assistantScheduledSystemPromptText(assistantExecMode{AutoWrite: true, DeferSOP: true}),
		scheduledAssistantPrompt(assistantTask{Name: "n", Prompt: "p", ProjectID: "platform"}, assistantExecMode{AutoWrite: true}),
		clarifySystemPrompt("intro", "mcp-step", "focus"),
		clarifyNativeRetryPrompt(),
		assistantNativeRetryPrompt(),
		agentToolInstructions(AgentToolClarify),
		agentToolInstructions(AgentToolAssistant),
		agentToolInstructions(AgentToolScheduled),
		clarifyResumeTurnPrompt("标题", "hello", 4),
	}
	forbidden := []string{
		"不要再打印",
		"不要输出 JSON",
		"不要输出 json",
		"mcpCalls",
		"只返回 JSON",
		"只返回协议 JSON",
		"不要使用 drafts",
		"不要使用 mcpCalls",
		"走 mcpCalls",
		`{"message"`,
		`"drafts":`,
		"Dockerfile body",
		"参数为：",
		"参数：message",
		"参数：summary",
	}
	for i, prompt := range prompts {
		lower := strings.ToLower(prompt)
		for _, phrase := range forbidden {
			if strings.Contains(prompt, phrase) || strings.Contains(lower, strings.ToLower(phrase)) {
				t.Fatalf("native prompt %d contains patch language %q:\n%s", i, phrase, prompt)
			}
		}
	}
	if !strings.Contains(assistantSystemPromptText(), "submit_assistant_reply") {
		t.Fatal("native assistant prompt must require submit_assistant_reply")
	}
	if prompt := assistantSystemPromptText(); !strings.Contains(prompt, "mode=direct") || !strings.Contains(prompt, "targetBranch") {
		t.Fatalf("native assistant prompt must teach mode=direct for shared branches:\n%s", prompt)
	}
	if !strings.Contains(clarifySystemPrompt("i", "m", "f"), "submit_clarification") {
		t.Fatal("native clarify prompt must require submit_clarification")
	}
}

func TestClarifyPromptsAnchorScopeWithoutScoring(t *testing.T) {
	system := clarifySystemPrompt("i", "m", "f")
	for _, want := range []string{"范围规则", "就是本次范围的边界", "不要提出会扩大范围的问题或选项", "不在本次范围", "没有阻塞开工的缺口就设 ready=true"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, system)
		}
	}
	for _, banned := range []string{"assessment", "打分", "0-100", "主要改哪个"} {
		if strings.Contains(system, banned) || strings.Contains(clarifyNativeRetryPrompt(), banned) {
			t.Fatalf("clarify prompts must not mention %q", banned)
		}
	}
	resume := clarifyResumeTurnPrompt("国际站绑定对齐", "顺便把国内站也改了", 1)
	if !strings.Contains(resume, "「国际站绑定对齐」") || !strings.Contains(resume, "不在本次范围") || strings.Contains(resume, "已经澄清了") {
		t.Fatalf("resume prompt must re-anchor on the title:\n%s", resume)
	}
	if late := clarifyResumeTurnPrompt("x", "y", 3); !strings.Contains(late, "已经澄清了 3 轮") || !strings.Contains(late, "ready=true") {
		t.Fatalf("resume prompt must push for a conclusion after several rounds:\n%s", late)
	}
}

func TestNativeShouldRetryTerminal(t *testing.T) {
	if !nativeShouldRetryTerminal(context.Background(), nil) {
		t.Fatal("missing tool should retry")
	}
	if !nativeShouldRetryTerminal(context.Background(), errors.New("max turns reached")) {
		t.Fatal("max-turns without submit should retry")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if nativeShouldRetryTerminal(ctx, errors.New("canceled")) {
		t.Fatal("canceled context must not retry")
	}
	if nativeShouldRetryTerminal(context.Background(), errors.New("请先配置 CodeBuddy API Key")) {
		t.Fatal("credential errors must not retry")
	}
}

func TestRecoverNativeAssistantReply(t *testing.T) {
	if reply, ok := recoverNativeAssistantReply("submit_assistant_reply 用于提交待确认草稿。", ""); !ok || reply.Message == "" {
		t.Fatal("tool explanations are ordinary answers")
	}
	reply, ok := recoverNativeAssistantReply("角色是执行者，SOP 是执行剧本。", "")
	if !ok || !strings.Contains(reply.Message, "SOP") {
		t.Fatalf("recover text: %+v %v", reply, ok)
	}
	if _, ok := recoverNativeAssistantReply("submit_assistant_reply 被调用，参数如下：", ""); ok {
		t.Fatal("tool-call dump must not be recovered as the user answer")
	}
	reply, ok = recoverNativeAssistantReply(`{"message":"已查到待办","drafts":[]}`, "")
	if !ok || reply.Message != "已查到待办" || strings.Contains(reply.Message, `"drafts"`) {
		t.Fatalf("recover structured: %+v %v", reply, ok)
	}
	raw := "本轮只读工具不可用\n```json\n{\"message\":\"已生成角色草稿\",\"drafts\":[{\"tool\":\"agents_save\",\"body\":{\"id\":\"p-role\",\"name\":\"评审\",\"projectId\":\"p\"}}]}\n```"
	reply, ok = recoverNativeAssistantReply(raw, "p")
	if !ok || reply.Message != "已生成角色草稿" || len(reply.Drafts) != 1 || reply.Drafts[0].Tool != "agents_save" {
		t.Fatalf("recover drafts from fence: %+v %v", reply, ok)
	}
	if strings.Contains(reply.Message, "```") || strings.Contains(reply.Message, `"drafts"`) {
		t.Fatalf("must not show protocol JSON: %+v", reply)
	}
}

func TestRecoverNativeClarification(t *testing.T) {
	raw := `{"summary":"需要补充验收","questions":[{"id":"q1","question":"怎么验收？","options":["接口回归","手工验收"],"multiSelect":false}],"ready":false}`
	parsed, _, ok := recoverNativeClarification(raw)
	if !ok {
		t.Fatal("expected recovered clarification")
	}
	m, _ := parsed.(map[string]any)
	if m["summary"] != "需要补充验收" {
		t.Fatalf("parsed=%v", parsed)
	}
}

func TestTakeNativeClarification(t *testing.T) {
	raw, got, ok := takeNativeClarification(nil, "x")
	if ok || raw != nil || got != "x" {
		t.Fatalf("nil session: %v %v %v", raw, got, ok)
	}
	session := &AgentToolSession{Final: make(chan AgentTerminalResult, 1)}
	if _, _, ok := takeNativeClarification(session, "x"); ok {
		t.Fatal("empty channel should miss")
	}
	session.Final <- AgentTerminalResult{Clarification: &clarifyModelResult{raw: map[string]any{"summary": "s", "ready": true}}}
	parsed, _, ok := takeNativeClarification(session, "raw")
	if !ok {
		t.Fatal("expected captured clarification")
	}
	m, _ := parsed.(map[string]any)
	if m["summary"] != "s" {
		t.Fatalf("parsed=%v", parsed)
	}
}
