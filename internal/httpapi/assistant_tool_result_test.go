package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

const cliBashNotice = `Tool "Bash" does not exist in the current tool set.`

func TestAssistantStreamKeepsSubAgentProseOutOfReply(t *testing.T) {
	sub := `{"type":"assistant","parent_tool_use_id":"agent-call","message":{"content":[{"type":"thinking","thinking":"plan"},{"type":"text","text":"Let me run this python script"},{"type":"tool_use","id":"sub-read","name":"Read","input":{"file_path":"/tmp/x"}}]}}`
	events := assistantStreamEvents(sub)
	// Sub-agent prose is its own kind, shown in its dispatch, never in the reply.
	if len(events) != 3 || events[0].Kind != "thinking" || events[1].Kind != "agentText" || events[2].Kind != "tool" || events[2].ID != "sub-read" || events[2].Parent != "agent-call" {
		t.Fatalf("sub-agent events=%+v", events)
	}
	for _, line := range []string{
		`{"type":"stream_event","parent_tool_use_id":"agent-call","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"NO_EXEC_TOOL"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"agent-call","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"message\":"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"agent-call","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"sub-reply","name":"mcp__agentflow_session__submit_assistant_reply","input":{}}}}`,
	} {
		if got := assistantStreamEvents(line); len(got) != 0 {
			t.Fatalf("sub-agent line leaked %+v from %s", got, line)
		}
	}
	if got := assistantStreamEvents(`{"type":"stream_event","parent_tool_use_id":"agent-call","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hm"}}}`); len(got) != 1 || got[0].Kind != "thinking" {
		t.Fatalf("sub-agent thinking=%+v", got)
	}
	// The main agent (no or null parent) is unchanged.
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"main"}}}`,
		`{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"main"}}}`,
		`{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"text","text":"main"}]}}`,
	} {
		if got := assistantStreamEvents(line); len(got) != 1 || got[0].Kind != "answer" || got[0].Text != "main" {
			t.Fatalf("main answer=%+v from %s", got, line)
		}
	}
	if got := assistantStreamEvents(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"reply","name":"mcp__agentflow_session__submit_assistant_reply","input":{}}}}`); len(got) != 1 || got[0].Kind != "draftStart" {
		t.Fatalf("main draft start=%+v", got)
	}
}

func TestAssistantStreamDropsCLIToolNotFoundNotice(t *testing.T) {
	line := fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":%q}]}}`, cliBashNotice)
	if got := assistantStreamEvents(line); len(got) != 0 {
		t.Fatalf("CLI notice became reply text: %+v", got)
	}
	quoted := `{"type":"assistant","message":{"content":[{"type":"text","text":"报错原文是 Tool \"Bash\" does not exist in the current tool set.，原因是未开放。"}]}}`
	if got := assistantStreamEvents(quoted); len(got) != 1 || got[0].Kind != "answer" {
		t.Fatalf("prose quoting the notice must stay: %+v", got)
	}
}

func TestAssistantVisibleReplyStripsCLINotice(t *testing.T) {
	events := []domain.AssistantActivityEvent{
		{Kind: "text", Text: "我先读取角色列表。"},
		{Kind: "tool", Tool: "agentflow__agents_list"},
		{Kind: "text", Text: cliBashNotice},
		{Kind: "text", Text: "核对完成。"},
	}
	got := assistantVisibleReply("核对完成。\n\n"+cliBashNotice, events)
	if strings.Contains(got, "does not exist") || !strings.Contains(got, "我先读取角色列表。") || !strings.Contains(got, "核对完成。") {
		t.Fatalf("visible reply=%q", got)
	}
	// A reply made only of the notice is kept rather than stored empty.
	if got := assistantVisibleReply(cliBashNotice, nil); got != cliBashNotice {
		t.Fatalf("notice-only reply=%q", got)
	}
	if got := stripAssistantCLINotices("a\n" + cliBashNotice + "\nb"); got != "a\nb" {
		t.Fatalf("stripped=%q", got)
	}
}

func testAgentsJSON(n, instructionRunes int) string {
	items := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, map[string]any{
			"id":           fmt.Sprintf("agent-%03d", i),
			"name":         fmt.Sprintf("角色 %d <a&b>", i),
			"model":        "claude-opus-5-5",
			"version":      json.Number("1234567890123456789"),
			"instructions": strings.Repeat("指", instructionRunes),
		})
	}
	raw, _ := json.Marshal(items)
	return string(raw)
}

func decodeFitted(t *testing.T, got string, result any) string {
	t.Helper()
	if len(got) > assistantToolResultBudget {
		t.Fatalf("fitted result %d bytes exceeds budget", len(got))
	}
	wrapped := struct {
		Notice string `json:"notice"`
		Result any    `json:"result"`
	}{Result: result}
	dec := json.NewDecoder(strings.NewReader(got))
	dec.UseNumber()
	if err := dec.Decode(&wrapped); err != nil {
		t.Fatalf("fitted result is not JSON: %v", err)
	}
	return wrapped.Notice
}

func TestFitAssistantToolResultKeepsWholeLeadingListItems(t *testing.T) {
	small := testAgentsJSON(2, 10)
	if got := fitAssistantToolResult("agents_list", small); got != small {
		t.Fatal("result within budget must stay byte-identical")
	}
	big := testAgentsJSON(60, 3000)
	if len(big) <= assistantToolResultBudget {
		t.Fatalf("fixture too small: %d", len(big))
	}
	var items []map[string]any
	notice := decodeFitted(t, fitAssistantToolResult("agents_list", big), &items)
	n := len(items)
	if n == 0 || n >= 60 || !strings.Contains(notice, fmt.Sprintf("前 %d 条（共 60 条）", n)) || !strings.Contains(notice, "不代表不存在") || !strings.Contains(notice, "fields=") {
		t.Fatalf("items=%d notice=%s", n, notice)
	}
	for i, item := range items {
		// Every kept item is complete: no value is shortened.
		if item["id"] != fmt.Sprintf("agent-%03d", i) || item["instructions"] != strings.Repeat("指", 3000) || item["name"] != fmt.Sprintf("角色 %d <a&b>", i) || item["version"] != json.Number("1234567890123456789") {
			t.Fatalf("item %d changed: %.200v", i, item)
		}
	}
}

func TestFitAssistantToolResultTrimsWrappedList(t *testing.T) {
	items := make([]map[string]any, 0, 5000)
	for i := 0; i < 5000; i++ {
		items = append(items, map[string]any{"id": fmt.Sprintf("issue-%05d", i), "status": "open"})
	}
	raw, _ := json.Marshal(map[string]any{"total": 5000, "items": items})
	var result struct {
		Total int              `json:"total"`
		Items []map[string]any `json:"items"`
	}
	notice := decodeFitted(t, fitAssistantToolResult("issues_list", string(raw)), &result)
	n := len(result.Items)
	if result.Total != 5000 || n == 0 || n >= 5000 || result.Items[0]["id"] != "issue-00000" || !strings.Contains(notice, fmt.Sprintf("前 %d 条（共 5000 条）", n)) {
		t.Fatalf("items=%d total=%d notice=%s", n, result.Total, notice)
	}
}

func TestFitAssistantToolResultLeavesDefinitionsAndTextUnchanged(t *testing.T) {
	// A *_get result is copied back into *_save (workflows_save overwrites the
	// whole graph), so it must never lose nodes or text.
	nodes := make([]map[string]any, 0, 40)
	for i := 0; i < 40; i++ {
		nodes = append(nodes, map[string]any{"id": fmt.Sprintf("n%d", i), "prompt": strings.Repeat("词", 2000)})
	}
	raw, _ := json.Marshal(map[string]any{"id": "wf", "nodes": nodes})
	workflow := string(raw)
	if len(workflow) <= assistantToolResultBudget {
		t.Fatalf("fixture too small: %d", len(workflow))
	}
	for _, tool := range []string{"workflows_get", "workflows_revision_get", "runs_artifact_get"} {
		if got := assistantPlatformResultForModel(tool, workflow); got != workflow {
			t.Fatalf("%s result changed", tool)
		}
	}
	text := strings.Repeat("日志行\n", assistantToolResultBudget)
	if got := fitAssistantToolResult("runs_logs_list", text); got != text {
		t.Fatal("multi-line text must stay readable in chunks, unchanged")
	}
	// Indented JSON that fits once compacted keeps every item.
	short := make([]map[string]any, 0, 20)
	for i := 0; i < 20; i++ {
		short = append(short, map[string]any{"id": fmt.Sprintf("wf%d", i), "name": "SOP", "status": "published"})
	}
	indented, _ := json.MarshalIndent(map[string]any{"items": short}, strings.Repeat(" ", 600), "\t")
	if len(indented) <= assistantToolResultBudget {
		t.Fatalf("indented fixture too small: %d", len(indented))
	}
	var compact struct{ Items []any }
	if got := fitAssistantToolResult("workflows_list", string(indented)); strings.Contains(got, "notice") || json.Unmarshal([]byte(got), &compact) != nil || len(compact.Items) != 20 {
		t.Fatalf("compacted list lost items: %.200s", got)
	}
	if got := assistantPlatformResultForModel("agents_list", testAgentsJSON(60, 3000)); len(got) > assistantToolResultBudget {
		t.Fatalf("model-facing list result not fitted: %d", len(got))
	}
}

func TestProjectAssistantResultFields(t *testing.T) {
	fields := assistantResultFields(" id, model ,,id")
	if strings.Join(fields, "|") != "id|model" {
		t.Fatalf("fields=%v", fields)
	}
	for _, tc := range []struct{ in, want string }{
		{`[{"id":"a","model":"m","instructions":"long"},{"id":"b"}]`, `[{"id":"a","model":"m"},{"id":"b"}]`},
		{`{"id":"a","model":"m","instructions":"long"}`, `{"id":"a","model":"m"}`},
		{`{"total":2,"items":[{"id":"a","x":1},{"id":"b","x":2}]}`, `{"items":[{"id":"a"},{"id":"b"}],"total":2}`},
		{`{"id":12345678901234567890,"x":1}`, `{"id":12345678901234567890}`},
		{`not json`, `not json`},
	} {
		if got := projectAssistantResult(tc.in, fields); got != tc.want {
			t.Fatalf("project(%s)=%s want %s", tc.in, got, tc.want)
		}
	}
	if got := projectAssistantResult(`[{"id":"a"}]`, nil); got != `[{"id":"a"}]` {
		t.Fatal("no fields must leave the result unchanged")
	}
}

func TestFilterAssistantResultIDs(t *testing.T) {
	ids := assistantResultFields("b, 12345678901234567890 ,b")
	for _, tc := range []struct{ in, want string }{
		{`[{"id":"a","x":1},{"id":"b","x":"<&>"},"c",{"x":3}]`, `[{"id":"b","x":"<&>"}]`},
		{`{"total":3,"items":[{"id":"a"},{"id":"b","y":2}]}`, `{"items":[{"id":"b","y":2}],"total":3}`},
		{`[{"id":12345678901234567890},{"id":1}]`, `[{"id":12345678901234567890}]`},
		{`{"id":"a","nodes":["x"]}`, `{"id":"a","nodes":["x"]}`},
		{`not json`, `not json`},
	} {
		if got := filterAssistantResultIDs(tc.in, ids); got != tc.want {
			t.Fatalf("filter(%s)=%s want %s", tc.in, got, tc.want)
		}
	}
	if got := filterAssistantResultIDs(`[{"id":"a"}]`, nil); got != `[{"id":"a"}]` {
		t.Fatal("no ids must leave the result unchanged")
	}
	// A trimmed list says how to reach the items it left out, including those
	// of lists without a *_get tool.
	var items []map[string]any
	if notice := decodeFitted(t, fitAssistantToolResult("skills_list", testAgentsJSON(60, 3000)), &items); !strings.Contains(notice, "ids") {
		t.Fatalf("notice=%s", notice)
	}
}

func TestAssistantPlatformFieldsAndModelCatalog(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	if err := st.PutProject(domain.Project{ID: "proj-fields", Name: "Fields"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAgent(domain.AgentDefinition{ID: "proj-fields-planner", Name: "Planner", ProjectID: "proj-fields", ExecutorID: "claude", Model: "claude-opus-5-5", Prompt: "long instructions"}); err != nil {
		t.Fatal(err)
	}
	api := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	h := &assistantHandlers{deps: &deps{repo: st}, api: api}
	got, err := h.callAssistantPlatform(context.Background(), user, "proj-fields", assistantMCPCall{Server: "agentflow", Tool: "agents_list", Arguments: map[string]any{"query": map[string]any{"fields": "id,model"}}})
	if err != nil {
		t.Fatal(err)
	}
	var agents []map[string]any
	if err := json.Unmarshal([]byte(got), &agents); err != nil || len(agents) == 0 {
		t.Fatalf("agents_list fields: %s %v", got, err)
	}
	found := false
	for _, agent := range agents {
		for key := range agent {
			if key != "id" && key != "model" {
				t.Fatalf("unrequested field %q in %v", key, agent)
			}
		}
		found = found || (agent["id"] == "proj-fields-planner" && agent["model"] == "claude-opus-5-5")
	}
	if !found {
		t.Fatalf("agent missing: %s", got)
	}
	// Skills have no *_get: ids reaches one skill in full, and is consumed by
	// the platform instead of reaching the handler.
	for _, id := range []string{"proj-fields-skill-a", "proj-fields-skill-b"} {
		if err := st.PutSkill(domain.SkillDefinition{ID: id, Name: id, ProjectID: "proj-fields", Content: "content of " + id}); err != nil {
			t.Fatal(err)
		}
	}
	got, err = h.callAssistantPlatform(context.Background(), user, "proj-fields", assistantMCPCall{Server: "agentflow", Tool: "skills_list", Arguments: map[string]any{"query": map[string]any{"ids": "proj-fields-skill-b"}}})
	if err != nil {
		t.Fatal(err)
	}
	var skills []map[string]any
	if err := json.Unmarshal([]byte(got), &skills); err != nil || len(skills) != 1 || skills[0]["id"] != "proj-fields-skill-b" || skills[0]["content"] != "content of proj-fields-skill-b" {
		t.Fatalf("skills_list ids: %s %v", got, err)
	}
	models, err := h.callAssistantPlatform(context.Background(), user, "proj-fields", assistantMCPCall{Server: "agentflow", Tool: "executor_models_list"})
	if err != nil || !strings.Contains(models, `"claude-opus-5-5"`) || !strings.Contains(models, `"codebuddy"`) {
		t.Fatalf("executor_models_list: %.300s %v", models, err)
	}
	if !assistantPlatformToolAllowed(assistantExecMode{}, "executor_models_list") {
		t.Fatal("model catalog must be readable in every approval mode")
	}
}

func TestAssistantPromptExplainsLargeResultsWithoutRestrictingSubAgents(t *testing.T) {
	for _, mode := range []string{AssistantApprovalConfirm, AssistantApprovalAssisted, AssistantApprovalAuto} {
		prompt := assistantSystemPromptForMode(mode)
		for _, want := range []string{"workbench_workspace_command", "fields=id,name,model", "ids（逗号分隔的 id）", "executor_models_list"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("mode %s prompt lacks %q", mode, want)
			}
		}
		// Sub-agent prose is filtered out of the reply by parent_tool_use_id, so
		// the prompt must not discourage delegating to sub-agents.
		if strings.Contains(prompt, "子代理") {
			t.Fatalf("mode %s prompt must not restrict sub-agents", mode)
		}
	}
}

func TestConversationWorkspaceGoalIsNotAPendingTask(t *testing.T) {
	run := domain.Run{ID: "run-ws", Kind: domain.RunKindWorkspace, Input: "对话改代码：只读解析 JSON"}
	if !run.IsWorkspace() {
		t.Fatal("fixture must be a conversation workspace")
	}
	got := describeConversationWorkspace(run)
	if !strings.Contains(got, "run-ws「只读解析 JSON」（「」里是开区时的目标") || !strings.Contains(got, "不代表还有待办任务") || !strings.Contains(got, "run-ws") {
		t.Fatalf("workspace description=%s", got)
	}
}
