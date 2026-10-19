package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestClarifyAllowedModelFallsBackToCatalog(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "public-model")
	if got := clarifyAllowedModel(""); got != clarifyDefaultModel {
		t.Fatalf("empty model = %q", got)
	}
	if got := clarifyAllowedModel("not-a-model"); got != clarifyDefaultModel {
		t.Fatalf("unknown model = %q", got)
	}
	if got := clarifyAllowedModel("public-model"); got != "public-model" {
		t.Fatalf("listed model = %q", got)
	}
}

func TestClarifyCommandKeepsReadGrepGlob(t *testing.T) {
	command, err := prepareClarifyCommand("codebuddy", clarifyDefaultModel, "prompt", "clarify-test", "af1", false)
	if err != nil {
		t.Fatal(err)
	}
	foundSession, foundResume, foundNoPersist := false, false, false
	for i, arg := range command.Argv {
		if arg == "--tools" && i+1 < len(command.Argv) {
			tools := command.Argv[i+1]
			if tools != "" && tools != "default" {
				t.Fatalf("--tools %q hides MCP", tools)
			}
		}
		if arg == "--session-id" && i+1 < len(command.Argv) && command.Argv[i+1] == "af1" {
			foundSession = true
		}
		if arg == "--resume" {
			foundResume = true
		}
		if arg == "--no-session-persistence" {
			foundNoPersist = true
		}
		if arg == "--model" && i+1 < len(command.Argv) && command.Argv[i+1] != clarifyDefaultModel {
			t.Fatalf("model = %q", command.Argv[i+1])
		}
	}
	if !foundSession {
		t.Fatalf("missing --session-id af1: %v", command.Argv)
	}
	if foundResume {
		t.Fatalf("first turn should not --resume: %v", command.Argv)
	}
	if foundNoPersist {
		t.Fatalf("session persistence should stay enabled: %v", command.Argv)
	}
}

func TestPrepareClarifyCommandSubmitOnlyDisallowsRead(t *testing.T) {
	command, err := prepareClarifyCommandWith(clarifyCommandOpts{
		Bin: "codebuddy", Model: clarifyDefaultModel, Prompt: "submit", RunID: "clarify-test", SessionID: "af1",
		Native: true, SubmitOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	denied, ok := argvFlagValue(command.Argv, "--disallowedTools")
	if !ok || !strings.Contains(denied, "Read") || !strings.Contains(denied, "Grep") {
		t.Fatalf("submit-only must deny Read/Grep: %q argv=%v", denied, command.Argv)
	}
}

func TestPrepareClarifyCommandResumeUsesResumeFlag(t *testing.T) {
	command, err := prepareClarifyCommand("codebuddy", clarifyDefaultModel, "继续", "clarify-test", "af1", true)
	if err != nil {
		t.Fatal(err)
	}
	foundResume, foundSession := false, false
	for i, arg := range command.Argv {
		if arg == "--resume" && i+1 < len(command.Argv) && command.Argv[i+1] == "af1" {
			foundResume = true
		}
		if arg == "--session-id" {
			foundSession = true
		}
		if arg == "--no-session-persistence" {
			t.Fatalf("resume turn still has --no-session-persistence: %v", command.Argv)
		}
	}
	if !foundResume {
		t.Fatalf("missing --resume af1: %v", command.Argv)
	}
	if foundSession {
		t.Fatalf("resume turn should not also pass --session-id: %v", command.Argv)
	}
}

func TestStripClarifyMCPCalls(t *testing.T) {
	parsed := map[string]any{
		"summary": "need comments",
		"mcpCalls": []any{
			map[string]any{"server": "tapd-p", "tool": "comments_get", "arguments": map[string]any{"workspace_id": "101"}},
		},
	}
	stripped := stripClarifyMCPCalls(parsed).(map[string]any)
	if _, ok := stripped["mcpCalls"]; ok {
		t.Fatal("mcpCalls should be stripped")
	}
	if stripped["summary"] != "need comments" {
		t.Fatalf("summary=%v", stripped["summary"])
	}
}

func TestWrapClarifyTAPDCallKeepsLongIDs(t *testing.T) {
	const storyID = "1010000000100001332"
	got := wrapClarifyTAPDCall(assistantMCPCall{
		Server: "tapd-p",
		Tool:   "stories_get",
		Arguments: map[string]any{
			"workspace_id": json.Number("10000000"),
			"id":           json.Number(storyID),
		},
	})
	args, _ := got.Arguments["tool_args"].(map[string]any)
	if fmt.Sprint(args["id"]) != storyID {
		t.Fatalf("id=%v want %s (float64 would truncate to ...100)", args["id"], storyID)
	}
	if fmt.Sprint(args["workspace_id"]) != "10000000" {
		t.Fatalf("workspace_id=%v", args["workspace_id"])
	}
}

func TestWrapClarifyTAPDCallUsesProxy(t *testing.T) {
	got := wrapClarifyTAPDCall(assistantMCPCall{Server: "tapd-p", Tool: "comments_list", Arguments: map[string]any{"workspace_id": "101", "entry_type": "story", "entry_id": "202"}})
	if got.Tool != "proxy_execute_tool" {
		t.Fatalf("tool=%q", got.Tool)
	}
	if got.Arguments["tool_name"] != "comments_get" {
		t.Fatalf("inner tool=%v", got.Arguments["tool_name"])
	}
	args, _ := got.Arguments["tool_args"].(map[string]any)
	if args["entry_id"] != "202" {
		t.Fatalf("tool_args=%v", got.Arguments["tool_args"])
	}
	if _, ok := got.Arguments["params"]; ok {
		t.Fatalf("params leaked: %v", got.Arguments)
	}
}

func TestWrapClarifyTAPDCallRewritesParamsToToolArgs(t *testing.T) {
	const storyID = "1010000000100001332"
	got := wrapClarifyTAPDCall(assistantMCPCall{
		Server: "tapd-p",
		Tool:   "proxy_execute_tool",
		Arguments: map[string]any{
			"tool_name": "stories_get",
			"params":    map[string]any{"workspace_id": json.Number("10000000"), "id": json.Number(storyID), "limit": 1},
		},
	})
	if got.Tool != "proxy_execute_tool" {
		t.Fatalf("tool=%q", got.Tool)
	}
	if _, ok := got.Arguments["params"]; ok {
		t.Fatalf("params should be rewritten: %v", got.Arguments)
	}
	args, _ := got.Arguments["tool_args"].(map[string]any)
	if fmt.Sprint(args["id"]) != storyID || fmt.Sprint(args["workspace_id"]) != "10000000" {
		t.Fatalf("tool_args=%v", args)
	}
}

func TestClarifyMCPRejectsWrites(t *testing.T) {
	if !clarifyMCPCallWrite("tapd", wrapClarifyTAPDCall(assistantMCPCall{Tool: "comments_create", Arguments: map[string]any{"description": "x"}})) {
		t.Fatal("comments_create must be blocked")
	}
	if clarifyMCPCallWrite("tapd", wrapClarifyTAPDCall(assistantMCPCall{Tool: "comments_get", Arguments: map[string]any{"entry_id": "1"}})) {
		t.Fatal("comments_get must stay allowed")
	}
	if !clarifyMCPWriteName("saveDocument") || !clarifyMCPWriteName("create_issue_note") {
		t.Fatal("wiki/gongfeng writes must be blocked")
	}
}

func TestHydrateClarifyIssueFromDraftText(t *testing.T) {
	tapd := domain.Issue{Title: "x", Description: "见 https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100000555"}
	hydrateClarifyIssueFromText(&tapd)
	if tapd.Source != "tapd" || tapd.ExternalRef == nil || tapd.ExternalRef.ExternalID != "1010000000100000555" {
		t.Fatalf("tapd hydrate=%+v ref=%+v", tapd.Source, tapd.ExternalRef)
	}
	gf := domain.Issue{Title: "x", Description: "见 https://git.example.com/gslb/gslbweb/-/issues/18"}
	hydrateClarifyIssueFromText(&gf)
	if gf.Source != "gongfeng" || gf.ExternalRef == nil || gf.ExternalRef.ExternalIID != 18 {
		t.Fatalf("gongfeng hydrate=%+v ref=%+v", gf.Source, gf.ExternalRef)
	}
	if path := gongfengProjectPathFromClarifyURL(gf.ExternalRef.ExternalURL); path != "gslb/gslbweb" {
		t.Fatalf("gongfeng path=%q", path)
	}
}

func TestClarifyMCPInstructionsRequireTAPDCommentsAndIWiki(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "mcp-70-project-platform", Name: "TAPD MCP", ProjectID: "platform", MarketID: "70", Scope: "project", Transport: "http", URL: "https://example.test/tapd"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "mcp-58-project-platform", Name: "iWiki", ProjectID: "platform", MarketID: "58", Scope: "project", Transport: "http", URL: "https://example.test/iwiki"}); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: st}}
	issue := domain.Issue{
		ProjectID: "platform", Source: "tapd", Title: "读评论",
		Description: "方案见 https://iwiki.example.com/pages/viewpage.action?pageId=999001",
		ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1010000000100000555", ExternalType: "story", ExternalURL: "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100000555"},
	}
	prompt := h.clarifyMCPInstructions(issue, user, false)
	for _, want := range []string{"submit_clarification", "tapd__proxy_execute_tool", "comments_get", "stories_get", "getDocument", "docid=999001"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "mcpCalls") {
		t.Fatalf("clarify instructions still mention mcpCalls:\n%s", prompt)
	}
	empty := h.clarifyMCPInstructions(domain.Issue{ProjectID: "platform", Source: "local", Title: "x"}, user, false)
	if strings.Contains(empty, "comments_get") {
		t.Fatalf("local issue should not require TAPD comments:\n%s", empty)
	}
	draft := domain.Issue{ProjectID: "platform", Title: "新建", Description: "参考 https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100000555"}
	hydrateClarifyIssueFromText(&draft)
	draftPrompt := h.clarifyMCPInstructions(draft, user, false)
	for _, want := range []string{"stories_get", "comments_get", "1010000000100000555"} {
		if !strings.Contains(draftPrompt, want) {
			t.Fatalf("draft with TAPD URL missing %q:\n%s", want, draftPrompt)
		}
	}
	plainDraft := h.clarifyMCPInstructions(domain.Issue{ProjectID: "platform", Title: "纯新建"}, user, false)
	if !strings.Contains(plainDraft, "尚未建单的需求草稿") || strings.Contains(plainDraft, "先静默读取原单和评论") {
		t.Fatalf("plain draft should not force TAPD fetch:\n%s", plainDraft)
	}
	resumed := h.clarifyMCPInstructions(issue, user, true)
	if strings.Contains(resumed, "先静默读取原单和评论") {
		t.Fatalf("resumed clarify should not re-read TAPD:\n%s", resumed)
	}
	if !strings.Contains(resumed, "不要再重拉原单和评论") {
		t.Fatalf("resumed clarify missing skip hint:\n%s", resumed)
	}
	native := h.clarifyMCPInstructionsFor(issue, user, false)
	if strings.Contains(native, "mcpCalls") || strings.Contains(native, "不要输出 JSON") || strings.Contains(native, `{"tool_name"`) {
		t.Fatalf("native MCP instructions still mention JSON protocol:\n%s", native)
	}
	if !strings.Contains(native, "submit_clarification") || !strings.Contains(native, "tapd__proxy_execute_tool") {
		t.Fatalf("native MCP instructions missing tools:\n%s", native)
	}
}

func TestClarifyMCPInstructionsReadTAPDTask(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, _ := st.UserByLoginName("demo-owner")
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "tapd-task", Name: "TAPD MCP", ProjectID: "platform", MarketID: "70", Scope: "project", Transport: "http", URL: "https://example.test/tapd"}); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ProjectID: "platform", Source: "tapd", ExternalRef: &domain.ExternalRef{
		Source: "tapd", ExternalID: "1010000000100001332", ExternalType: "task",
		ExternalURL: "https://tapd.example.com/tapd_fe/10000000/task/detail/1010000000100001332",
	}}
	h := &streamHandlers{deps: &deps{repo: st}}
	prompt := h.clarifyMCPInstructions(issue, user, false)
	for _, want := range []string{"tool_name=tasks_get", "tool_args.entry_type=task", "tool_args.entry_id=1010000000100001332", "tool_args.workspace_id=10000000"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("task prompt missing %q: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, "tool_name=stories_get") || strings.Contains(prompt, "tool_name=bugs_get") {
		t.Fatalf("task prompt uses another entity's getter: %s", prompt)
	}
	call := wrapClarifyTAPDCall(assistantMCPCall{Tool: "tasks_list", Arguments: map[string]any{
		"workspace_id": json.Number("10000000"), "id": json.Number("1010000000100001332"),
	}})
	args, _ := call.Arguments["tool_args"].(map[string]any)
	if call.Tool != "proxy_execute_tool" || call.Arguments["tool_name"] != "tasks_get" || args["id"] != "1010000000100001332" {
		t.Fatalf("task call must use tasks_get and string IDs: %+v", call)
	}
	if !clarifyMCPWriteName("tasks_update") {
		t.Fatal("task updates must remain blocked in read-only clarification")
	}
}

func TestClarifyMCPInstructionsRequireGongfengIssue(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "mcp-61-project-platform", Name: "Gongfeng MCP", ProjectID: "platform", MarketID: "61", Scope: "project", Transport: "http", URL: "https://example.test/gf"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "repo-gf", ProjectID: "platform", Name: "svc", GongfengProjectID: "dns/svc"}); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: st}}
	issue := domain.Issue{
		ProjectID: "platform", Source: "gongfeng", Kind: "issue", RepositoryID: "repo-gf",
		ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalID: "88", ExternalIID: 12, ExternalType: "issue"},
	}
	prompt := h.clarifyMCPInstructions(issue, user, false)
	for _, want := range []string{"get_issue_detail", "get_issue_notes", "dns/svc", "issue_iid=12", "issue_id=88"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestDispatchClarifyMCPCallReadOnlyTAPD(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	var tools []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name string         `json:"name"`
				Args map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/call":
			tools = append(tools, req.Params.Name)
			inner, _ := req.Params.Args["tool_name"].(string)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok ` + inner + `"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	t.Cleanup(gateway.Close)
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "tapd-p", Name: "TAPD", ProjectID: "platform", MarketID: "70", Scope: "project", Transport: "http", URL: gateway.URL}); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: st}}
	asst := &assistantHandlers{deps: h.deps}
	got, err := h.dispatchClarifyMCPCall(asst, user, "platform", assistantMCPCall{
		Server: "tapd-p", Tool: "comments_get", Arguments: map[string]any{"workspace_id": "101", "entry_type": "story", "entry_id": "202"},
	}, nil)
	if err != nil || !strings.Contains(got, "ok comments_get") {
		t.Fatalf("read call: %q %v tools=%v", got, err, tools)
	}
	if _, err := h.dispatchClarifyMCPCall(asst, user, "platform", assistantMCPCall{
		Server: "tapd-p", Tool: "comments_create", Arguments: map[string]any{"description": "nope"},
	}, nil); err == nil || !strings.Contains(err.Error(), "只读") {
		t.Fatalf("write should be rejected: %v", err)
	}
}
