package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAgentToolCatalogAndProtocolHelpers(t *testing.T) {
	for _, tc := range []struct{ id, name, want string }{
		{"My MCP/One", "", "my_mcp_one"},
		{"", "Fancy  Tool", "fancy_tool"},
		{"tapd", "", "mcp"},
		{"!!!", "", "mcp"},
	} {
		if got := sanitizeAgentToolNamespace(tc.id, tc.name); got != tc.want {
			t.Fatalf("namespace=%q want=%q", got, tc.want)
		}
	}
	if namespaceAgentTool("", "x") != "x" || namespaceAgentTool("tapd", "get") != "tapd__get" {
		t.Fatal("namespace helper")
	}
	for _, name := range []string{"tapd__stories_get", "mcp__agentflow_session__tapd__stories_get", "agentflow_session__tapd__stories_get"} {
		kind, tool, ok := parseNamespacedAgentTool(name)
		if !ok || kind != "tapd" || tool != "stories_get" {
			t.Fatalf("parse %q = %q %q %v", name, kind, tool, ok)
		}
	}
	if _, tool, ok := parseNamespacedAgentTool("plain"); ok || tool != "plain" {
		t.Fatal("plain tool parsed as namespaced")
	}
	for _, tc := range []struct {
		mode       AgentToolMode
		kind, tool string
		want       bool
	}{
		{AgentToolReport, "tapd", "stories_get", false},
		{AgentToolClarify, "tapd", "stories_get", true},
		{AgentToolClarify, "tapd", "stories_save", false},
		{AgentToolClarify, "other", "read_document", true},
		{AgentToolClarify, "other", "create_document", false},
		{AgentToolScheduled, "custom", "anything", true},
	} {
		if got := agentToolNameAllowed(tc.mode, tc.kind, tc.tool); got != tc.want {
			t.Fatalf("allowed(%s,%s,%s)=%v", tc.mode, tc.kind, tc.tool, got)
		}
	}
	if tapdInnerToolAllowed(AgentToolClarify, "proxy_execute_tool") ||
		!tapdInnerToolAllowed(AgentToolClarify, "comments_list") ||
		!tapdInnerToolAllowed(AgentToolScheduled, "unknown") {
		t.Fatal("tapd inner policy")
	}
	cache := &agentToolCatalogCache{}
	cache.put("k", nil)
	if _, ok := cache.get("k"); !ok {
		t.Fatal("cache miss")
	}
	cache.items["old"] = agentToolCatalogCacheEntry{expiresAt: time.Now().Add(-time.Second)}
	if _, ok := cache.get("old"); ok {
		t.Fatal("expired cache hit")
	}
	if got := string(defaultToolSchema(nil)); !strings.Contains(got, "object") {
		t.Fatalf("default schema=%s", got)
	}
	redacted := redactAuthorizationValues(`{"Authorization":"Bearer secret","authorization": token}`)
	if strings.Contains(redacted, "secret") || strings.Contains(redacted, " token") {
		t.Fatalf("authorization leaked: %s", redacted)
	}
	result := mcpToolTextResult("ok", true)
	if result["isError"] != true {
		t.Fatalf("tool result=%v", result)
	}

	if reply, ok := recoverAssistantProtocol("prefix ```json\n{\"message\":\"hidden\"}\n``` visible suffix", ""); !ok || reply.Message == "" {
		t.Fatalf("recover=%+v %v", reply, ok)
	}
	if _, ok := recoverAssistantProtocol("not json", ""); ok {
		t.Fatal("invalid protocol recovered")
	}
	if got := stripAssistantProtocolFence("before\n```json\n{\"x\":1}\n```\nafter"); got != "before\nafter" {
		t.Fatalf("strip fence=%q", got)
	}
	if _, ok := extractAssistantProtocolJSON(`{"other":1}`); ok {
		t.Fatal("unrelated JSON detected")
	}
	if got := assistantPathParamNames("/api/projects/:projectId/things/{id}/:id"); len(got) != 1 || got[0] != "id" {
		t.Fatalf("path params=%v", got)
	}
}

func TestClarifyMCPAndTerminalValidation(t *testing.T) {
	values := []any{nil, " 7 ", json.Number("8"), json.RawMessage(`"9"`), float64(10), 11, int64(12), true}
	wants := []string{"", "7", "8", "9", "10", "11", "12", "true"}
	for i := range values {
		if got := stringifyJSONID(values[i]); got != wants[i] {
			t.Fatalf("stringify %#v=%q", values[i], got)
		}
	}
	ids := stringifyTAPDIDs(map[string]any{"workspace_id": float64(123), "nested": map[string]any{"story_id": 9}, "name": "x"})
	if ids["workspace_id"] != "123" || ids["nested"].(map[string]any)["story_id"] != "9" {
		t.Fatalf("tapd ids=%#v", ids)
	}
	for _, tc := range []struct {
		item domain.MCPServerDefinition
		want string
	}{
		{domain.MCPServerDefinition{MarketID: "70"}, "tapd"},
		{domain.MCPServerDefinition{Name: "工蜂"}, "gongfeng"},
		{domain.MCPServerDefinition{ID: "iwiki-doc"}, "iwiki"},
		{domain.MCPServerDefinition{Name: "other"}, "other"},
	} {
		if got := classifyClarifyMCP(tc.item); got != tc.want {
			t.Fatalf("classify=%q want=%q", got, tc.want)
		}
	}
	gf := "https://git.example.com/group/proj/-/issues/42?x=1"
	if gongfengProjectPathFromClarifyURL(gf) != "group/proj" || gongfengIssueIIDFromURL(gf) != 42 {
		t.Fatal("gongfeng URL parsing")
	}
	if gongfengIssueIIDFromURL("bad") != 0 || iwikiDocIDFromURL("https://iwiki.example.com/pages/12345") != "12345" {
		t.Fatal("URL fallback parsing")
	}
	issue := domain.Issue{Title: "see " + gf}
	hydrateClarifyIssueFromText(&issue)
	if issue.Source != "gongfeng" || issue.ExternalRef == nil || issue.ExternalRef.ExternalIID != 42 {
		t.Fatalf("hydrated=%+v", issue)
	}
	if urls := extractIWikiURLs("https://iwiki.example.com/p/12345.", "https://iwiki.example.com/p/12345"); len(urls) != 1 {
		t.Fatalf("iwiki urls=%v", urls)
	}
	call := assistantMCPCall{Tool: "comments_list", Arguments: map[string]any{"workspace_id": float64(3), "entry_id": 4}}
	wrapped := wrapClarifyTAPDCall(call)
	if wrapped.Tool != "proxy_execute_tool" || wrapped.Arguments["tool_name"] != "comments_get" {
		t.Fatalf("wrapped=%+v", wrapped)
	}
	if !clarifyMCPCallWrite("tapd", assistantMCPCall{Tool: "proxy_execute_tool", Arguments: map[string]any{"tool_name": "stories_update"}}) {
		t.Fatal("nested TAPD write allowed")
	}
	if clarifyMCPWriteName("tools/list") || !clarifyMCPWriteName("create_issue") {
		t.Fatal("write name policy")
	}

	valid := json.RawMessage(`{"summary":"","ready":false,"questions":[{"id":" q ","question":" choose ","options":[" a "," b "]}]}`)
	result, err := parseSubmitClarificationArgs(valid)
	if err != nil || len(result.Questions) != 1 || result.Questions[0].ID != "q" {
		t.Fatalf("clarification=%+v %v", result, err)
	}
	for _, raw := range []string{
		`{"ready":true}`,
		`{"summary":"x","ready":false}`,
		`{"summary":"","ready":false,"questions":[{"id":"q","question":"","options":["a","b"]}]}`,
		`{"summary":"","ready":false,"questions":[{"id":"q","question":"x","options":["a"]}]}`,
		`{"summary":"","ready":false,"questions":[{"id":"q","question":"x","freeText":true,"options":["a"]}]}`,
		`{"summary":"","ready":false,"mcpCalls":[],"questions":[{"id":"q","question":"x","options":["a","b"]}]}`,
	} {
		if _, err := parseSubmitClarificationArgs(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid clarification accepted: %s", raw)
		}
	}
	if _, err := parseSubmitAssistantReplyArgs(json.RawMessage(`{"message":"ok","lookups":[]}`), "p"); err == nil {
		t.Fatal("assistant lookups accepted")
	}
	if _, err := parseSubmitAssistantReplyArgs(json.RawMessage(`{"message":" "}`), "p"); err == nil {
		t.Fatal("blank assistant message accepted")
	}
	for _, raw := range []string{
		`{"body":""}`,
		`{"body":"ok","unknown":1}`,
		`{"body":"ok"} {}`,
	} {
		if _, err := parseSubmitNotificationReportArgs(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid report accepted: %s", raw)
		}
	}
	if report, err := parseSubmitNotificationReportArgs(json.RawMessage(`{"title":" t ","summary":" s ","body":" b "}`)); err != nil || report.Body != "b" {
		t.Fatalf("report=%+v %v", report, err)
	}
}

func TestClarifyResultCoercionAndPersistence(t *testing.T) {
	raw := map[string]any{
		"summary": "done", "ready": false,
		"questions": []any{
			" plain question ",
			map[string]any{"id": "choice", "prompt": "Pick", "options": `[{"label":"A"},{"text":"B"}]`, "multiSelect": true},
			map[string]any{"text": "Free", "freeText": true, "options": []any{}},
			17,
		},
	}
	result := parseClarifyModel(raw, "")
	if len(result.Questions) != 3 || result.Questions[0].ID != "q1" || len(result.Questions[1].Options) != 2 {
		t.Fatalf("coerced result=%+v", result)
	}
	for input, want := range map[string]string{
		`{"label":"Label"}`: "Label",
		`["bad"]`:           "",
		"":                  "",
		" plain ":           "plain",
	} {
		if got := clarifyOptionLabel(input); got != want {
			t.Fatalf("option %q=%q want=%q", input, got, want)
		}
	}
	if got := coerceClarifyOptions([]string{" a ", "", "b"}); len(got) != 2 ||
		stringValueOf(nil) != "" || stringValueOf(42) != "42" {
		t.Fatalf("option/value helpers=%v", got)
	}
	if _, ok := anySlice(12); ok {
		t.Fatal("scalar treated as slice")
	}
	if err := (clarifyModelResult{}).userFacingOK(); err == nil {
		t.Fatal("empty result accepted")
	}
	readyMissing := clarifyModelResult{Ready: true, raw: map[string]any{"ready": true}}
	if err := readyMissing.userFacingOK(); err == nil {
		t.Fatal("ready result without summary accepted")
	}
	ready := clarifyModelResult{Summary: "complete", Ready: true, raw: map[string]any{"summary": "complete", "ready": true}}
	if err := ready.userFacingOK(); err != nil {
		t.Fatal(err)
	}
	payload := clarificationAPIPayload(domain.ClarificationAssessment{Summary: "s", Status: domain.ClarificationReady})
	if payload["ready"] != true {
		t.Fatalf("payload=%v", payload)
	}

	st := cvAStore(t)
	_ = st.PutProject(domain.Project{ID: "clarify-p", Name: "P"})
	_ = st.PutIssue(domain.Issue{ID: "clarify-i", ProjectID: "clarify-p", Title: "Need", Description: "Original"})
	h := &streamHandlers{deps: &deps{repo: st}}
	ready.Assessment.Dimensions = map[string]int{"goal": 100}
	saved, err := h.saveClarificationAssessment("clarify-i", ready)
	if err != nil || saved.Clarification.Status != domain.ClarificationReady || !strings.Contains(saved.Description, "complete") {
		t.Fatalf("ready save=%+v %v", saved.Clarification, err)
	}
	pending := clarifyModelResult{
		Questions: []domain.ClarificationQuestion{{ID: "q", Question: "Pick", Options: []string{"A", "B"}}},
		raw:       map[string]any{"ready": false, "questions": []any{}},
	}
	pending.Assessment.Dimensions = map[string]int{"goal": 20}
	saved, err = h.saveClarificationAssessment("clarify-i", pending)
	if err != nil || saved.Clarification.Status != domain.ClarificationInProgress {
		t.Fatalf("pending save=%+v %v", saved.Clarification, err)
	}
	if rawSummaryFallback(nil) != "" || rawSummaryFallback(map[string]any{"summary": " fallback "}) != " fallback " {
		t.Fatal("summary fallback")
	}
}

func cvAPPTX(t *testing.T, slides map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range slides {
		w, err := zw.Create("ppt/slides/" + name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWorkbenchDeckAndClarifySessionHelpers(t *testing.T) {
	t.Log("pptx")
	data := cvAPPTX(t, map[string]string{
		"slide2.xml": `<a:p><a:r><a:t>Second</a:t></a:r></a:p>`,
		"slide1.xml": `<a:p><a:r><a:t>Title</a:t></a:r></a:p><a:p><a:r><a:t>Body</a:t></a:r></a:p>`,
	})
	if got := pptxSlides(data); got != "Title\nBody\n---\nSecond" {
		t.Fatalf("slides=%q", got)
	}
	if pptxSlides([]byte("bad")) != "" || pptxSlideText("<xml/>") != "" {
		t.Fatal("invalid pptx produced text")
	}
	if got := previewSlidesMessage("Done", "Slide"); !strings.Contains(got, "```slides") {
		t.Fatalf("preview=%q", got)
	}
	if got := stripWorkbuddyLinks("Done\nhttps://foo.workbuddy.cn/a\n点击打开"); got != "Done" {
		t.Fatalf("strip links=%q", got)
	}
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Log("filesystem")
	_ = os.MkdirAll(filepath.Join(root, "ws", "nested"), 0o700)
	_ = os.WriteFile(filepath.Join(root, "ws", "nested", "deck.PPTX"), data, 0o600)
	_ = os.Symlink(filepath.Join(root, "ws", "nested", "deck.PPTX"), filepath.Join(root, "ws", "link.pptx"))
	paths := deckFilesUnder(filepath.Join(root, "ws"), 0)
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "deck.PPTX") {
		t.Fatalf("recent decks=%v", paths)
	}
	if got := uniquePaths([]string{"", "a", "a", "b"}); len(got) != 2 {
		t.Fatalf("unique=%v", got)
	}
	content := `{"message":"saved /tmp/generated-images/a.png and keep","drafts":[]}`
	clean := stripMediaPathsInContent(content)
	if clean == content || strings.Contains(clean, "/tmp/generated-images") {
		t.Fatalf("media path not stripped: %s", clean)
	}
	if stripMediaPathsInContent("plain") != "plain" {
		t.Fatal("plain content changed")
	}

	t.Log("clarify")
	t.Setenv("AGENTFLOW_CLARIFY_SESSION_DIR", filepath.Join(root, "sessions"))
	if !strings.Contains(clarifySessionBase(), "sessions") || !strings.HasSuffix(clarifyCLIHome(9), filepath.Join("9", "home")) {
		t.Fatal("clarify paths")
	}
	if got := clarifyWorkspaceDir(1, "../"); !strings.HasSuffix(got, "draft") {
		t.Fatalf("workspace=%q", got)
	}
	if _, err := ensureClarifyWorkspace(1, "issue"); err != nil {
		t.Fatal(err)
	}
	if err := materializeClarifyRepo(root, root, true); err == nil {
		t.Fatal("same source accepted")
	}
	file := filepath.Join(root, "file")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	copied := filepath.Join(root, "copied")
	if err := copyClarifyFile(file, copied); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(copied); string(body) != "x" {
		t.Fatalf("copied body=%q", body)
	}
	if err := cloneClarifyTree(file, filepath.Join(root, "dest")); err == nil {
		t.Fatal("file cloned as tree")
	}
	if captureClarifySessionID(`{"session_id":" abc "}`) != "abc" || captureClarifySessionID("bad") != "" {
		t.Fatal("session capture")
	}
	if !strings.Contains(clarifyResumeTurnPrompt(""), "继续澄清") || !isClarifyProtocolObject(map[string]any{"ready": true}) || isClarifyProtocolObject("x") {
		t.Fatal("clarify protocol helpers")
	}
	for msg, want := range map[string]string{
		"": "ok", "未返回 JSON": "invalid_json", "缺少 summary": "ready_missing_summary",
		"既没有 questions": "empty_questions", "问题文本为空": "empty_question_text", "other": "invalid_schema",
	} {
		var err error
		if msg != "" {
			err = &cvAError{msg}
		}
		if got := classifyClarifyProtocolError(err); got != want {
			t.Fatalf("classify error=%q want=%q", got, want)
		}
	}
}

type cvAError struct{ text string }

func (e *cvAError) Error() string { return e.text }
