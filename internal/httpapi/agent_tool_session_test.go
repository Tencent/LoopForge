package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAgentToolSessionTokenIsRandomAndOpaque(t *testing.T) {
	resetAgentToolSessionsForTest()
	a, err := newAgentToolSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newAgentToolSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) != 64 {
		t.Fatalf("token entropy too low: %s %s", a, b)
	}
	if strings.Contains(a, "user") || strings.Contains(a, "project") {
		t.Fatal("token must not encode identity")
	}
}

func TestAgentToolSessionExpiresAndIsolatesUsers(t *testing.T) {
	resetAgentToolSessionsForTest()
	first := &AgentToolSession{UserID: 1, ProjectID: "p1", Mode: AgentToolClarify, ExpiresAt: time.Now().Add(time.Minute), Final: make(chan AgentTerminalResult, 1)}
	second := &AgentToolSession{UserID: 2, ProjectID: "p2", Mode: AgentToolAssistant, ExpiresAt: time.Now().Add(-time.Second), Final: make(chan AgentTerminalResult, 1)}
	if err := agentToolSessions.create(first); err != nil {
		t.Fatal(err)
	}
	if err := agentToolSessions.create(second); err != nil {
		t.Fatal(err)
	}
	if _, ok := agentToolSessions.get(first.Token); !ok {
		t.Fatal("live session missing")
	}
	if got, ok := agentToolSessions.get(second.Token); ok || got != nil {
		t.Fatal("expired session must be rejected")
	}
	if _, ok := agentToolSessions.get(first.Token + "00"); ok {
		t.Fatal("foreign token must be rejected")
	}
}

func TestAgentToolSessionRejectsDuplicateTerminal(t *testing.T) {
	resetAgentToolSessionsForTest()
	session := &AgentToolSession{Mode: AgentToolClarify, ExpiresAt: time.Now().Add(time.Minute), Final: make(chan AgentTerminalResult, 1)}
	if err := agentToolSessions.create(session); err != nil {
		t.Fatal(err)
	}
	result := AgentTerminalResult{Kind: AgentTerminalClarification, Clarification: &clarifyModelResult{Summary: "ok", Ready: true, raw: map[string]any{"summary": "ok", "ready": true}}}
	if err := session.commitFinal(result); err != nil {
		t.Fatal(err)
	}
	if err := session.commitFinal(result); err == nil {
		t.Fatal("duplicate terminal must fail")
	}
	got, ok := session.takeFinal()
	if !ok || got.Clarification == nil || got.Clarification.Summary != "ok" {
		t.Fatalf("missing captured terminal: %+v", got)
	}
}

func TestAgentToolSessionJanitorPurgesExpired(t *testing.T) {
	resetAgentToolSessionsForTest()
	session := &AgentToolSession{Mode: AgentToolClarify, ExpiresAt: time.Now().Add(-time.Minute), Final: make(chan AgentTerminalResult, 1)}
	if err := agentToolSessions.create(session); err != nil {
		t.Fatal(err)
	}
	agentToolSessions.purgeExpired(time.Now())
	if _, ok := agentToolSessions.get(session.Token); ok {
		t.Fatal("janitor must drop expired session")
	}
}

func TestSubmitClarificationValidation(t *testing.T) {
	_, err := parseSubmitClarificationArgs(json.RawMessage(`{"summary":"","questions":[],"ready":true}`))
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("ready without summary: %v", err)
	}
	_, err = parseSubmitClarificationArgs(json.RawMessage(`{"summary":"x","questions":[],"ready":false}`))
	if err == nil || !strings.Contains(err.Error(), "questions") {
		t.Fatalf("summary-only not-ready must fail: %v", err)
	}
	_, err = parseSubmitClarificationArgs(json.RawMessage(`{"summary":"","questions":[],"ready":false}`))
	if err == nil || !strings.Contains(err.Error(), "问题") && !strings.Contains(err.Error(), "summary") {
		t.Fatalf("not ready without summary or questions: %v", err)
	}
	_, err = parseSubmitClarificationArgs(json.RawMessage(`{"summary":"x","questions":[{"id":"q1","question":"A","options":["1","2"],"multiSelect":false},{"id":"q1","question":"B","options":["1","2"],"multiSelect":false}],"ready":false}`))
	if err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("duplicate ids: %v", err)
	}
	_, err = parseSubmitClarificationArgs(json.RawMessage(`{"summary":"x","questions":[{"id":"q1","question":"A","options":[" "],"multiSelect":false}],"ready":false,"mcpCalls":[{"server":"tapd"}]}`))
	if err == nil || !strings.Contains(err.Error(), "mcpCalls") {
		t.Fatalf("mcpCalls must be rejected: %v", err)
	}
	got, err := parseSubmitClarificationArgs(json.RawMessage(`{"summary":"enough","questions":[{"id":"q1","question":"改哪个模块？","options":["  A  ","","B"],"multiSelect":false}],"ready":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Questions) != 1 || len(got.Questions[0].Options) != 2 {
		t.Fatalf("options not sanitized: %+v", got.Questions)
	}
}

func TestSubmitAssistantReplyValidatesDrafts(t *testing.T) {
	_, err := parseSubmitAssistantReplyArgs(json.RawMessage(`{"message":"","drafts":[]}`), "p1")
	if err == nil {
		t.Fatal("empty message")
	}
	_, err = parseSubmitAssistantReplyArgs(json.RawMessage(`{"message":"ok","drafts":[],"mcpCalls":[]}`), "p1")
	if err == nil || !strings.Contains(err.Error(), "mcpCalls") {
		t.Fatalf("mcpCalls rejected: %v", err)
	}
	got, err := parseSubmitAssistantReplyArgs(json.RawMessage(`{"message":"hello","drafts":[]}`), "p1")
	if err != nil || got.Message != "hello" || got.Drafts == nil {
		t.Fatalf("plain reply: %+v %v", got, err)
	}
	_, err = parseSubmitAssistantReplyArgs(json.RawMessage(`{"message":"save","drafts":[{"tool":"issues_save","body":{"projectId":"other"}}]}`), "p1")
	if err == nil {
		t.Fatal("foreign project draft must fail")
	}
	ok, err := parseSubmitAssistantReplyArgs(json.RawMessage(`{"message":"save","drafts":[{"tool":"projects_save","body":{"id":"p1","name":"P","gitToken":"SECRET","projectId":"p1"}}]}`), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ok.Drafts[0].Body), "SECRET") || strings.Contains(string(ok.Drafts[0].Body), "gitToken") {
		t.Fatalf("secret not stripped: %s", ok.Drafts[0].Body)
	}
}

func TestAgentToolAllowlists(t *testing.T) {
	if !agentToolNameAllowed(AgentToolClarify, "tapd", "proxy_execute_tool") || agentToolNameAllowed(AgentToolClarify, "tapd", "comments_create") {
		t.Fatal("tapd clarify allowlist")
	}
	if !tapdInnerToolAllowed(AgentToolClarify, "comments_get") || tapdInnerToolAllowed(AgentToolClarify, "comments_create") {
		t.Fatal("tapd inner write must be denied")
	}
	if agentToolNameAllowed(AgentToolClarify, "gongfeng", "create_issue_note") || !agentToolNameAllowed(AgentToolClarify, "gongfeng", "get_issue_detail") {
		t.Fatal("gongfeng clarify allowlist")
	}
	if agentToolNameAllowed(AgentToolClarify, "agentflow", "issues_save") || !agentToolNameAllowed(AgentToolAssistant, "agentflow", "issues_list") {
		t.Fatal("agentflow write must stay hidden from native tools")
	}
	if parseNamespacedAgentTool("mcp__agentflow_session__tapd__proxy_execute_tool"); false {
		t.Fatal("unreachable")
	}
	kind, tool, ok := parseNamespacedAgentTool("mcp__agentflow_session__tapd__proxy_execute_tool")
	if !ok || kind != "tapd" || tool != "proxy_execute_tool" {
		t.Fatalf("namespace parse: %s %s %v", kind, tool, ok)
	}
}

func TestAgentToolCatalogTerminalOnlyHidesReads(t *testing.T) {
	resetAgentToolSessionsForTest()
	resetAgentToolCatalogCacheForTest()
	session := &AgentToolSession{
		Mode:       AgentToolClarify,
		ExpiresAt:  time.Now().Add(time.Minute),
		MCPServers: []domain.MCPServerDefinition{{ID: "tapd", Name: "TAPD"}},
		Final:      make(chan AgentTerminalResult, 1),
	}
	h := &agentToolGateway{}
	session.SetTerminalOnly()
	got := h.buildCatalog(context.Background(), session)
	if len(got) != 1 || got[0].Name != submitClarificationTool {
		t.Fatalf("terminal-only catalog = %+v", got)
	}
}

func TestNativeClarifyCommandKeepsReadAndSessionMCP(t *testing.T) {
	dir := t.TempDir()
	files, err := writeAgentToolFiles(dir, "ab"+strings.Repeat("cd", 31))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.Cleanup)
	command, err := prepareClarifyCommandWith(clarifyCommandOpts{
		Bin: "codebuddy", Model: clarifyDefaultModel, Prompt: "p", RunID: "r", SessionID: "af1",
		Native: true, MCPConfig: files.MCPConfig, Settings: files.Settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, ok := argvFlagValue(command.Argv, "--tools")
	if ok && tools != "" && tools != "default" {
		t.Fatalf("native clarify --tools %q hides MCP; CodeBuddy applies --tools to MCP tools", tools)
	}
	denied, ok := argvFlagValue(command.Argv, "--disallowedTools")
	if !ok || !strings.Contains(denied, "Bash") || !strings.Contains(denied, "Write") || !strings.Contains(denied, "Edit") {
		t.Fatalf("dangerous tools not denied: %q argv=%v", denied, command.Argv)
	}
	if !argvHasFlag(command.Argv, "--strict-mcp-config") {
		t.Fatal("missing --strict-mcp-config")
	}
	if src, _ := argvFlagValue(command.Argv, "--setting-sources"); src != "none" {
		t.Fatalf("setting-sources=%q", src)
	}
	raw, _ := os.ReadFile(files.MCPConfig)
	if !strings.Contains(string(raw), "agentflow_session") || strings.Contains(strings.ToLower(string(raw)), "authorization") {
		t.Fatalf("mcp.json leaked credentials or missed gateway: %s", raw)
	}
	settings, _ := os.ReadFile(files.Settings)
	if strings.Contains(string(settings), `"allow"`) {
		t.Fatalf("settings allowlist would hide MCP tools: %s", settings)
	}
	if nativeCommandContainsSecret(command, files, "ab"+strings.Repeat("cd", 31)) {
		t.Fatal("token must not appear in command argv")
	}
	info, err := os.Stat(files.MCPConfig)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mcp.json mode = %v", info.Mode())
	}
}

func TestNativeAssistantCommandDisablesBuiltinWrites(t *testing.T) {
	dir := t.TempDir()
	files, err := writeAgentToolFiles(dir, strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.Cleanup)
	command, err := prepareAssistantCommand(assistantCommandOpts{Bin: "codebuddy", Config: files.MCPConfig, Settings: files.Settings, Prompt: "hi", Native: true})
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := argvFlagValue(command.Argv, "--tools")
	if tools != "" && tools != "default" {
		t.Fatalf("assistant --tools %q hides MCP; CodeBuddy applies --tools to MCP tools", tools)
	}
	if strings.Contains(tools, "Bash") || strings.Contains(tools, "Write") {
		t.Fatalf("assistant tools = %q", tools)
	}
	denied, _ := argvFlagValue(command.Argv, "--disallowedTools")
	if !strings.Contains(denied, "Bash") || !strings.Contains(denied, "Write") || !strings.Contains(denied, "Edit") {
		t.Fatalf("assistant deny = %q", denied)
	}
	if len(command.CredentialBindings) != 1 || command.CredentialBindings[0] != "codebuddy" {
		t.Fatalf("bindings=%v", command.CredentialBindings)
	}
}

func TestRedactAgentToolPathAndAuthorization(t *testing.T) {
	path := redactAgentToolPath("/internal/agent-tools/" + strings.Repeat("ab", 32) + "/mcp")
	if strings.Contains(path, "abab") || path != "/internal/agent-tools/[redacted]/mcp" {
		t.Fatalf("path=%s", path)
	}
	got := redactAuthorizationValues(`{"Authorization":"Bearer secret-token"}`)
	if strings.Contains(got, "secret-token") {
		t.Fatalf("authorization not redacted: %s", got)
	}
	_ = context.Background()
	_ = domain.User{}
}
