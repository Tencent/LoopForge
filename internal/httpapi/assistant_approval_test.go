package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestNormalizeAssistantApprovalMode(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", AssistantApprovalConfirm},
		{"confirm", AssistantApprovalConfirm},
		{"assisted", AssistantApprovalAssisted},
		{"AUTO", AssistantApprovalAuto},
		{" auto ", AssistantApprovalAuto},
		{"bogus", AssistantApprovalConfirm},
	} {
		if got := normalizeAssistantApprovalMode(tc.in); got != tc.want {
			t.Fatalf("normalizeAssistantApprovalMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAssistantExecModeForApproval(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want assistantExecMode
	}{
		{AssistantApprovalConfirm, assistantExecMode{}},
		{AssistantApprovalAssisted, assistantExecMode{AutoWrite: true, ConfirmDangerous: true}},
		{AssistantApprovalAuto, assistantExecMode{AutoWrite: true}},
		{"bogus", assistantExecMode{}},
	} {
		if got := assistantExecModeForApproval(tc.mode); got != tc.want {
			t.Fatalf("assistantExecModeForApproval(%q) = %+v, want %+v", tc.mode, got, tc.want)
		}
	}
	for _, tool := range []string{"agents_save", "rules_save", "issues_save", "workflows_save"} {
		if !assistantExecModeForApproval(AssistantApprovalAuto).AutoWrite {
			t.Fatal("auto mode must enable AutoWrite")
		}
		_ = tool
	}
}

func TestAssistantRiskyToolName(t *testing.T) {
	for _, tool := range []string{"agents_delete", "workflows_publish", "issues_dispatch", "runs_start", "schedule_jobs_run", "sync_start"} {
		if !assistantRiskyToolName(tool) {
			t.Fatalf("%q should be risky", tool)
		}
	}
	for _, tool := range []string{"agents_save", "rules_save", "issues_save", "mcp_servers_save", "workflows_get"} {
		if assistantRiskyToolName(tool) {
			t.Fatalf("%q should not be risky", tool)
		}
	}
}

func TestAssistantDraftRisky(t *testing.T) {
	for _, tc := range []struct {
		tool  string
		risky bool
	}{
		{"agents_save", false},
		{"issues_save", false},
		{"assistant_tasks_save", false},
		{"agents_delete", true},
		{"workflows_publish", true},
		{"issues_dispatch", true},
		{"runs_start", true},
		{"schedule_jobs_run", true},
		{"sync_start", true},
	} {
		if got := assistantDraftRisky(assistantDraft{Tool: tc.tool}); got != tc.risky {
			t.Fatalf("assistantDraftRisky(%q) = %v, want %v", tc.tool, got, tc.risky)
		}
	}
}

func TestAssistantDraftWarnings(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutProject(domain.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	h := &assistantHandlers{deps: &deps{repo: st}}
	if warnings := h.assistantDraftWarnings(assistantDraft{Tool: "agents_delete", Path: map[string]any{"id": "ghost"}}); len(warnings) != 1 || !strings.Contains(warnings[0], "不存在") {
		t.Fatalf("missing agent warnings = %#v", warnings)
	}
	if warnings := h.assistantDraftWarnings(assistantDraft{Tool: "agents_save", Body: json.RawMessage(`{"id":"a","name":"A","projectId":"p"}`)}); len(warnings) != 0 {
		t.Fatalf("save draft should have no warnings: %#v", warnings)
	}
	if warnings := h.assistantDraftWarnings(assistantDraft{Tool: "workflows_delete", Query: map[string]any{"id": "no-such"}}); len(warnings) != 1 || !strings.Contains(warnings[0], "不存在") {
		t.Fatalf("missing workflow warnings = %#v", warnings)
	}
	if warnings := h.assistantDraftWarnings(assistantDraft{Tool: "sandboxes_delete"}); len(warnings) != 1 || !strings.Contains(warnings[0], "缺少目标资源 id") {
		t.Fatalf("missing-id warnings = %#v", warnings)
	}
	if err := validateAssistantDraft(&assistantDraft{Tool: "sandboxes_delete"}, "p"); err == nil || !strings.Contains(err.Error(), "缺少目标资源 id") {
		t.Fatalf("delete draft without id should be rejected: %v", err)
	}
}

func TestResolveAssistantDraftsByApprovalMode(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &assistantHandlers{deps: &deps{repo: st}}
	drafts := []assistantDraft{
		{Tool: "workflows_publish", Path: map[string]any{"id": "wf"}},
		{Tool: "agents_save", Body: json.RawMessage(`{"id":"a","name":"A","projectId":"p"}`)},
	}
	if out := h.resolveAssistantDrafts(context.Background(), domain.User{}, "p", drafts, AssistantApprovalConfirm); len(out) != 2 {
		t.Fatalf("confirm mode should keep all drafts: %#v", out)
	}
	out := h.resolveAssistantDrafts(context.Background(), domain.User{}, "p", drafts, AssistantApprovalAssisted)
	if len(out) != 2 {
		t.Fatalf("assisted mode should keep both drafts: %#v", out)
	}
	if out[0].Auto || !out[1].Auto {
		t.Fatalf("assisted mode should auto-apply only low-risk drafts: %#v", out)
	}
	out = h.resolveAssistantDrafts(context.Background(), domain.User{}, "p", drafts, AssistantApprovalAuto)
	for _, draft := range out {
		if !draft.Auto {
			t.Fatalf("auto mode should auto-apply all drafts: %#v", out)
		}
	}
	if out := h.resolveAssistantDrafts(context.Background(), domain.User{}, "p", []assistantDraft{{Tool: "agents_delete", Path: map[string]any{"id": "ghost"}}}, AssistantApprovalAssisted); len(out) != 1 || out[0].Auto {
		t.Fatalf("assisted mode must confirm drafts with dependency warnings: %#v", out)
	}
}

func TestWorkflowDraftNodeWarnings(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutProject(domain.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAgent(domain.AgentDefinition{ID: "p-reviewer", Name: "评审", ProjectID: "p", Version: 1, Instructions: "review"}); err != nil {
		t.Fatal(err)
	}
	h := &assistantHandlers{deps: &deps{repo: st}}
	unbound := assistantDraft{Tool: "workflows_save", Body: json.RawMessage(`{"id":"p-wf","name":"WF","projectId":"p","nodes":[{"id":"n1","name":"澄清","type":"agent"},{"id":"n2","name":"评审","type":"agent","agentId":"p-reviewer"},{"id":"n3","name":"检查","type":"agent","agentId":"p-ghost"}]}`)}
	warnings := h.assistantDraftWarnings(unbound)
	if len(warnings) != 2 || !strings.Contains(warnings[0], "未绑定角色") || !strings.Contains(warnings[1], "不存在") {
		t.Fatalf("unbound/missing agent warnings = %#v", warnings)
	}
	bound := assistantDraft{Tool: "workflows_save", Body: json.RawMessage(`{"id":"p-wf","name":"WF","projectId":"p","nodes":[{"id":"n2","name":"评审","type":"agent","agentId":"p-reviewer"},{"id":"n4","name":"脚本","type":"script","script":"echo ok"}]}`)}
	if warnings := h.assistantDraftWarnings(bound); len(warnings) != 0 {
		t.Fatalf("fully bound workflow should have no warnings: %#v", warnings)
	}
	emptyScript := assistantDraft{Tool: "workflows_save", Body: json.RawMessage(`{"id":"p-wf","nodes":[{"id":"n5","type":"script"}]}`)}
	if warnings := h.assistantDraftWarnings(emptyScript); len(warnings) != 1 || !strings.Contains(warnings[0], "缺少脚本内容") {
		t.Fatalf("empty script warnings = %#v", warnings)
	}
}

func TestAutoModeDowngradesDraftsWithWarnings(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutProject(domain.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	h := &assistantHandlers{deps: &deps{repo: st}}
	broken := assistantDraft{Tool: "workflows_save", Body: json.RawMessage(`{"id":"p-wf","name":"WF","projectId":"p","nodes":[{"id":"n1","name":"澄清","type":"agent"}]}`)}
	out := h.resolveAssistantDrafts(context.Background(), domain.User{}, "p", []assistantDraft{broken}, AssistantApprovalAuto)
	if len(out) != 1 || out[0].Auto {
		t.Fatalf("auto mode must downgrade drafts with warnings to confirmation: %#v", out)
	}
	if len(out[0].Warnings) != 1 || !strings.Contains(out[0].Warnings[0], "未绑定角色") {
		t.Fatalf("downgraded draft should carry warnings: %#v", out[0])
	}
}

func TestAssistantStreamAnswerDeltasAndFullBlockDedup(t *testing.T) {
	var filter assistantStreamFilter
	deltas := []string{"你好", "，", "世界"}
	for _, text := range deltas {
		events := assistantStreamEvents(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"` + text + `"}}}`)
		if len(events) != 1 || events[0].Kind != "answer" || events[0].Text != text {
			t.Fatalf("text_delta event = %#v", events)
		}
		if !filter.accept(events[0]) {
			t.Fatalf("delta %q should be accepted", text)
		}
	}
	full := assistantStreamEvents(`{"type":"assistant","message":{"content":[{"type":"text","text":"你好，世界"}]}}`)
	if len(full) != 1 || full[0].Kind != "answer" || !full[0].Full {
		t.Fatalf("full text block event = %#v", full)
	}
	if filter.accept(full[0]) {
		t.Fatal("full block already streamed by deltas should be dropped")
	}
	// 没有增量时整段文本块仍应透出（partial messages 关闭的场景）。
	var fresh assistantStreamFilter
	if !fresh.accept(full[0]) {
		t.Fatal("full block without prior deltas should be accepted")
	}
}

func TestAssistantSystemPromptForMode(t *testing.T) {
	base := assistantSystemPromptForMode(AssistantApprovalConfirm)
	if !strings.Contains(base, "每轮只提交 1 个草稿") || !strings.Contains(base, "submit_assistant_reply") {
		t.Fatal("base prompt must require a single draft per turn via submit_assistant_reply")
	}
	if !strings.Contains(base, "检查引用关系") {
		t.Fatal("base prompt must require dependency checks before drafts")
	}
	if !strings.Contains(base, "不要直接调用平台写入工具") {
		t.Fatal("confirm prompt must block direct write tools")
	}
	assisted := assistantSystemPromptForMode(AssistantApprovalAssisted)
	if !strings.Contains(assisted, "调用即生效") || !strings.Contains(assisted, "放入 drafts") {
		t.Fatal("assisted prompt must direct-exec saves and draft dangerous writes")
	}
	if strings.Contains(assisted, "不要直接调用平台写入工具") {
		t.Fatal("assisted prompt must allow direct write calls for saves")
	}
	auto := assistantSystemPromptForMode(AssistantApprovalAuto)
	if !strings.Contains(auto, "一次性完成") || !strings.Contains(auto, "不要把写入放进 drafts") {
		t.Fatal("auto prompt must require one-shot direct execution without drafts")
	}
}
