package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestAssistantProgressSubmissionCannotEndToolExecution(t *testing.T) {
	h, user := workbenchFixture(t)
	gateway := &agentToolGateway{deps: h.deps}
	for _, mode := range []assistantExecMode{{}, {AutoWrite: true, ConfirmDangerous: true}, {AutoWrite: true}} {
		cancelled := false
		session := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", User: user, UserID: user.ID, ExecMode: mode, Final: make(chan AgentTerminalResult, 1), CancelModel: func() { cancelled = true }}
		result, err := gateway.callTerminal(session, submitAssistantReplyTool, json.RawMessage(`{"message":"稍等，我先看看项目里有哪些可用 SOP。","drafts":[]}`))
		if err != nil || result["isError"] != true || session.finished() || cancelled {
			t.Fatalf("progress reply terminated execution: result=%v err=%v cancelled=%v", result, err, cancelled)
		}
		if _, ok := session.takeFinal(); ok {
			t.Fatal("progress reply became the final answer")
		}
		result, err = gateway.callTerminal(session, submitAssistantReplyTool, json.RawMessage(`{"message":"创建需求","drafts":[{"body":{"title":"演练需求"}}]}`))
		encoded, _ := json.Marshal(result)
		if err != nil || result["isError"] != true || !strings.Contains(string(encoded), "tool 字段") || session.finished() || cancelled {
			t.Fatalf("missing tool did not give recoverable validation: result=%v err=%v", result, err)
		}
		// Read tools must remain callable so the model can finish preparing its draft.
		if _, err := gateway.callTool(context.Background(), session, json.RawMessage(`{"name":"workbench_tool_schema","arguments":{"name":"issues_dispatch"}}`)); err != nil {
			t.Fatalf("cannot continue querying after rejected progress: %v", err)
		}
		result, err = gateway.callTerminal(session, submitAssistantReplyTool, json.RawMessage(`{"message":"已核对需求，确认后派发。","drafts":[{"tool":"issues_dispatch","path":{"id":"workbench-issue"}}]}`))
		if err != nil || result["isError"] == true || !session.finished() || !cancelled {
			t.Fatalf("valid draft did not finish: result=%v err=%v", result, err)
		}
		final, ok := session.takeFinal()
		if !ok || final.Assistant == nil || len(final.Assistant.Drafts) != 1 {
			t.Fatal("lost valid confirmation draft")
		}
	}
}

func TestCodeBuddyTerminalToolsDoNotRequireDeferredSearch(t *testing.T) {
	files, err := writeAgentToolFiles(t.TempDir(), strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.Cleanup)
	raw, err := os.ReadFile(files.MCPConfig)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers map[string]struct {
			Type  string `json:"type"`
			URL   string `json:"url"`
			Defer *bool  `json:"defer_loading"`
			Tools map[string]struct {
				Defer *bool `json:"defer_loading"`
			} `json:"tools"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	server, ok := cfg.Servers[agentToolSessionMCPServerID]
	if !ok || len(cfg.Servers) != 1 || server.Type != "http" || server.URL == "" {
		t.Fatal("missing CodeBuddy session gateway")
	}
	if server.Defer != nil {
		t.Fatal("business tools must retain normal deferred discovery")
	}
	names := []string{submitAssistantReplyTool, submitClarificationTool, submitNotificationReportTool, "workbench_tool_schema"}
	if len(server.Tools) != len(names) {
		t.Fatal("only terminal tools and draft schema helper should have a loading override")
	}
	for _, name := range names {
		tool, ok := server.Tools[name]
		if !ok || tool.Defer == nil || *tool.Defer {
			t.Fatalf("terminal %s still depends on ToolSearch", name)
		}
	}
	// These files only feed the host-side CodeBuddy path. Generic runtime
	// providers retain their own materialized MCP files.
	command, err := prepareAssistantCommand(assistantCommandOpts{Bin: "codebuddy", Config: files.MCPConfig, Settings: files.Settings, Prompt: "hi", Native: true})
	if err != nil {
		t.Fatal(err)
	}
	if command.Provider != "codebuddy" {
		t.Fatalf("unexpected provider %s", command.Provider)
	}
}

// Pinned terminal tools are absent from the first model request when the
// gateway connects after CodeBuddy's short headless MCP wait, and they are
// not in the deferred index, so every session-gateway process must wait.
func TestCodeBuddyWaitsForSessionGatewayBeforeFirstRequest(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "codebuddy")
	script := "#!/bin/sh\nprintf '{\"type\":\"result\",\"result\":\"wait=%s\"}\\n' \"$CODEBUDDY_FIRST_RUN_MCP_PREWAIT_TIMEOUT_MS\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", filepath.Join(root, "sessions"))
	files, err := writeAgentToolFiles(t.TempDir(), strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.Cleanup)
	want := "wait=" + strconv.FormatInt(agentToolSessionMCPStartupWait.Milliseconds(), 10)
	user := domain.User{ID: 7, CodeBuddyAPIKey: "test-key"}
	for _, resume := range []bool{false, true} {
		raw, _, err := runAssistantCLI(context.Background(), assistantCLIRequest{Prompt: "hi", SessionID: "af-wait", Resume: resume, UserID: user.ID, Native: true, MCPConfig: files.MCPConfig, Settings: files.Settings}, user, nil)
		if err != nil || raw != want {
			t.Fatalf("assistant resume=%v: got %q err=%v, want %q", resume, raw, err, want)
		}
	}
	h := &streamHandlers{}
	_, out, _, err := h.runClarifyTurn(context.Background(), clarifyTurn{
		WorkDir: root, Engine: clarifyEngineOf(clarifyDefaultModel), Bin: bin, Prompt: "p",
		RunID: "clarify-wait", SessionID: "af-wait", HomeDir: filepath.Join(root, "clarify-home"),
		Credentials: runtime.RunCredentials{CodeBuddy: runtime.CodeBuddyCredential{APIKey: "test-key"}},
		Opts:        clarifyTurnOpts{Native: true, MCPConfig: files.MCPConfig, Settings: files.Settings},
	}, func(string, any) {})
	if err != nil || !strings.Contains(out, want) {
		t.Fatalf("clarify: got %q err=%v, want %q", out, err, want)
	}
}

func TestAssistantPromptsNameCallableMCPSubmissionTool(t *testing.T) {
	name := "mcp__agentflow_session__" + submitAssistantReplyTool
	for _, mode := range []string{AssistantApprovalConfirm, AssistantApprovalAssisted, AssistantApprovalAuto} {
		prompt := assistantSystemPromptForMode(mode)
		if !strings.Contains(prompt, name) || !strings.Contains(prompt, "不要把 submit_assistant_reply 裸名当成内置工具") {
			t.Fatalf("mode %s lacks qualified submission guidance", mode)
		}
	}
	if !strings.Contains(assistantNativeRetryPrompt(), name) {
		t.Fatal("terminal retry must name the actual MCP tool")
	}
	h := &assistantHandlers{}
	prompt := h.turnPrompt(assistantRequest{Messages: []assistantMessage{{Role: "user", Content: "save"}}}, t.TempDir(), true)
	if !strings.Contains(prompt, "待确认写入仍须通过 "+name) {
		t.Fatal("resumed turns must refresh the qualified submission name")
	}
}

func TestAssistantDeferredWorkCannotDependOnSessionCron(t *testing.T) {
	for _, tool := range []string{"CronCreate", "CronList", "CronDelete", "Monitor"} {
		if !strings.Contains(nativeDisallowedTools, tool) {
			t.Fatalf("ephemeral tool %s remains enabled", tool)
		}
	}
	prompt := assistantSystemPromptForMode(AssistantApprovalAuto)
	if !strings.Contains(prompt, "平台持久化定时任务") || !strings.Contains(prompt, "未实际保存成功不得承诺") {
		t.Fatal("missing durable scheduling contract")
	}
}
