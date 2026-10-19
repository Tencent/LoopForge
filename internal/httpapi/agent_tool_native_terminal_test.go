package httpapi

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

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
	names := []string{submitAssistantReplyTool, submitClarificationTool, submitNotificationReportTool}
	if len(server.Tools) != len(names) {
		t.Fatal("only terminal tools should have a loading override")
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
	for _, tool := range []string{"CronCreate", "CronList", "CronDelete"} {
		if !strings.Contains(nativeDisallowedTools, tool) {
			t.Fatalf("ephemeral tool %s remains enabled", tool)
		}
	}
	prompt := assistantSystemPromptForMode(AssistantApprovalAuto)
	if !strings.Contains(prompt, "平台持久化定时任务") || !strings.Contains(prompt, "未实际保存成功不得承诺") {
		t.Fatal("missing durable scheduling contract")
	}
}
