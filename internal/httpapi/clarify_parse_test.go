package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractJSONObjectPrefersStreamResultOverToolDump(t *testing.T) {
	dump := `{"type":"conversation_record","id":"x","payload":{"comments":[{"body":"getDomainLastPackageInfo"}]}}`
	clarify := `{"summary":"查不到套餐时不应报参数不合法","questions":[{"id":"q1","question":"查不到套餐时返回什么？","options":["返回空数据","保持参数不合法"]}],"ready":false}`
	raw := "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":" + jsonQuote(t, dump) + "}]}}\n" +
		"{\"type\":\"user\",\"message\":{\"content\":[{\"type\":\"tool_result\",\"content\":" + jsonQuote(t, dump) + "}]}}\n" +
		"{\"type\":\"result\",\"subtype\":\"success\",\"result\":" + jsonQuote(t, clarify) + ",\"is_error\":false}"

	parsed, _ := extractClarifyResultJSON(raw)
	m, ok := parsed.(map[string]any)
	if !ok {
		t.Fatalf("parsed type %T", parsed)
	}
	if m["summary"] != "查不到套餐时不应报参数不合法" {
		t.Fatalf("picked %#v", parsed)
	}
	questions, _ := m["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("questions=%v", m["questions"])
	}
}

func TestExtractJSONObjectRejectsDumpResult(t *testing.T) {
	dump := "正在读取 TAPD 评论\n" + `{"type":"conversation_record","id":"x","payload":{"comments":[{"body":"blob"}]}}`
	raw := "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":" + jsonQuote(t, dump) + "}]}}\n" +
		"{\"type\":\"result\",\"subtype\":\"success\",\"result\":" + jsonQuote(t, dump) + ",\"is_error\":false}"
	parsed, src := extractClarifyResultJSON(raw)
	if parsed != nil || src != "" {
		t.Fatalf("dump should not persist as summary: parsed=%#v src=%q", parsed, src)
	}
}

func TestExtractJSONObjectUnwrapsBareResultEnvelope(t *testing.T) {
	raw := `{"type":"result","result":"{\"summary\":\"enough\",\"questions\":[],\"ready\":true}"}`
	parsed, _ := extractClarifyResultJSON(raw)
	m, _ := parsed.(map[string]any)
	if m["summary"] != "enough" {
		t.Fatalf("parsed=%#v", parsed)
	}
}

func TestExtractClarifyResultJSONFromAssistantWhenResultIsChat(t *testing.T) {
	clarify := `{"summary":"需要额外支持高级路由型 CLB","questions":[{"id":"q1","question":"先改哪条接入链路？","options":["部署","托管"]}],"ready":false}`
	raw := "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":" + jsonQuote(t, "好的，我继续确认范围。\n"+clarify) + "}]}}\n" +
		"{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"好的，我继续确认范围。\",\"is_error\":false}"
	parsed, _ := extractClarifyResultJSON(raw)
	m, ok := parsed.(map[string]any)
	if !ok {
		t.Fatalf("parsed type %T", parsed)
	}
	if m["summary"] != "需要额外支持高级路由型 CLB" {
		t.Fatalf("picked %#v", parsed)
	}
}

func TestExtractClarifyResultJSONPrefersProtocolOverDumpResult(t *testing.T) {
	dump := `{"type":"conversation_record","id":"x","payload":{"comments":[{"body":"blob"}]}}`
	clarify := `{"summary":"国际站取回对齐国内站","questions":[{"id":"q1","question":"过户失败怎么办？","options":["阻断","告警"]}],"ready":false}`
	raw := "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":" + jsonQuote(t, clarify) + "}]}}\n" +
		"{\"type\":\"result\",\"subtype\":\"success\",\"result\":" + jsonQuote(t, dump) + ",\"is_error\":false}"
	parsed, _ := extractClarifyResultJSON(raw)
	m, ok := parsed.(map[string]any)
	if !ok || m["summary"] != "国际站取回对齐国内站" {
		t.Fatalf("picked %#v", parsed)
	}
}

func TestClarifyModelUserFacingOK(t *testing.T) {
	if err := parseClarifyModel(nil, "").userFacingOK(); err == nil {
		t.Fatal("empty payload should fail")
	}
	got := parseClarifyModel(map[string]any{
		"summary":   "还缺验收",
		"questions": []any{map[string]any{"question": "怎么验收？", "options": []any{"接口回归", "手工验收"}}},
		"ready":     false,
	}, "")
	if err := got.userFacingOK(); err != nil {
		t.Fatal(err)
	}
	mcp := parseClarifyModel(map[string]any{
		"summary":  "先读原单",
		"mcpCalls": []any{map[string]any{"tool": "stories_get"}},
		"ready":    false,
	}, "")
	if err := mcp.userFacingOK(); err == nil {
		t.Fatal("mcp-only payload is not a user-facing clarification")
	}
}

func TestParseClarifyModelDropsJSONDumpOptions(t *testing.T) {
	dump := `{"type":"conversation_record","id":"tapd-chat","payload":{"comments":[{"body":"DescribeDomainLastPackageInfo"}]}}`
	parsed := map[string]any{
		"summary": "接口查不到套餐时返回了参数不合法",
		"questions": []any{
			map[string]any{
				"id":       "q1",
				"question": "查不到套餐时应该返回什么？",
				"options": []any{
					"返回空数据",
					map[string]any{"id": "keep", "label": "保持参数不合法错误"},
					dump,
					map[string]any{"type": "conversation_record", "id": "blob", "payload": dump},
				},
			},
		},
		"ready": false,
	}
	got := parseClarifyModel(parsed, "")
	if len(got.Questions) != 1 {
		t.Fatalf("questions=%+v", got.Questions)
	}
	opts := got.Questions[0].Options
	if len(opts) != 2 || opts[0] != "返回空数据" || opts[1] != "保持参数不合法错误" {
		t.Fatalf("options=%v", opts)
	}
	for _, opt := range opts {
		if strings.Contains(opt, "conversation_record") || strings.Contains(opt, "{") {
			t.Fatalf("json dump leaked into option %q", opt)
		}
	}
}

func TestParseClarifyModelParsesStringifiedOptionArray(t *testing.T) {
	parsed := map[string]any{
		"summary": "还缺验收",
		"questions": []any{
			map[string]any{
				"question": "怎么验收？",
				"options":  `["接口回归","手工验收"]`,
			},
		},
	}
	got := parseClarifyModel(parsed, "")
	if len(got.Questions) != 1 {
		t.Fatalf("questions=%+v", got.Questions)
	}
	opts := got.Questions[0].Options
	if len(opts) != 2 || opts[0] != "接口回归" || opts[1] != "手工验收" {
		t.Fatalf("options=%v", opts)
	}
}

func TestParseClarifyModelExtractsQuestionsFromMixedDump(t *testing.T) {
	mixed := "代码已核实：取回拦截共 4 处\n" + `{"summary":"国际站取回对齐国内站","questions":[{"id":"q4","question":"过户失败怎么办？","options":["阻断过户","记录告警"]}],"ready":false,"mcpCalls":[]}`
	got := parseClarifyModel(map[string]any{"summary": mixed, "questions": []any{}, "ready": false}, mixed)
	if got.Summary != "国际站取回对齐国内站" {
		t.Fatalf("summary=%q", got.Summary)
	}
	if len(got.Questions) != 1 || got.Questions[0].Question != "过户失败怎么办？" {
		t.Fatalf("questions=%+v", got.Questions)
	}
	if len(got.Questions[0].Options) != 2 {
		t.Fatalf("options=%v", got.Questions[0].Options)
	}
}

func jsonQuote(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRecoverNativeClarificationRequiresAnswerableQuestions(t *testing.T) {
	for _, raw := range []string{
		`{"summary":"还需确认业务原因","questions":[],"ready":false,"assessment":{"missing":["业务原因未明确"]}}`,
		`{"summary":"还需确认输入方式","questions":[{"id":"q1","question":"如何输入 ID？","options":[]}],"ready":false}`,
		`{"summary":"还需确认输入方式","questions":[{"id":"q1","question":"如何输入 ID？","options":["文件"]}],"ready":false}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"已阅读代码，需要确认具体业务目的及输入方式才能继续实施。"}]}}`,
	} {
		if _, _, ok := recoverNativeClarification(raw); ok {
			t.Fatalf("unanswerable output must not bypass terminal validation: %s", raw)
		}
	}
	raw := `{"summary":"需要文件路径","questions":[{"id":"q1","question":"ID 文件在哪里？","options":[],"freeText":true}],"ready":false}`
	parsed, _, ok := recoverNativeClarification(raw)
	if !ok {
		t.Fatal("explicit free-text questions must recover")
	}
	got := parseClarifyModel(parsed, "")
	if len(got.Questions) != 1 || !got.Questions[0].FreeText {
		t.Fatalf("freeText lost during normalization: %+v", got.Questions)
	}
}

func TestClarifyOptionLabelKeepsFullText(t *testing.T) {
	long := strings.Repeat("国际站校验与限额", 30)
	got := clarifyOptionLabel(long)
	if got != long || strings.Contains(got, "…") {
		t.Fatalf("option truncated to %d runes: %q", len([]rune(got)), got)
	}
}

func TestStripGitCLINoise(t *testing.T) {
	raw := "clone clarify workspace: exit status 128: \x1b[31mAuthentication failed\x1b[0m for 'https://git.example.com/a/b.git'"
	got := stripGitCLINoise(raw)
	if strings.Contains(got, "[31m") || strings.Contains(got, "\x1b") {
		t.Fatalf("ansi left in %q", got)
	}
	if !strings.Contains(got, "Authentication failed") {
		t.Fatalf("reason lost: %q", got)
	}
}
