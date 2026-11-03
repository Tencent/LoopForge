package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The browser runs on the machine that holds the workspace: the task run of a
// task conversation, or the temporary workspace of an ordinary conversation.
func TestMainBotBrowserUsesConversationWorkspace(t *testing.T) {
	chat := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p", UserID: 1, ChatSessionID: "chat", ChatOwnerID: 1}
	if runID, err := mainBotBrowserRun(chat); runID != "" || !errors.Is(err, errMainBotBrowserNoWorkspace) {
		t.Fatalf("conversation without a workspace: run=%q err=%v", runID, err)
	}
	chat.workspaceRunID = "conv-workspace"
	if runID, err := mainBotBrowserRun(chat); err != nil || runID != "conv-workspace" {
		t.Fatalf("conversation workspace must host the browser: run=%q err=%v", runID, err)
	}
	task := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p", UserID: 1, RunID: "task-run", workspaceRunID: "stale-conversation"}
	if runID, err := mainBotBrowserRun(task); err != nil || runID != "task-run" {
		t.Fatalf("task conversation must keep its task run: run=%q err=%v", runID, err)
	}
	consult := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p", UserID: 1, ChatSessionID: "chat", ChatOwnerID: 1, workspaceRunID: "conv-workspace"}
	consult.ExecMode.Consultation = true
	if _, err := mainBotBrowserRun(consult); !errors.Is(err, errAgentToolForbidden) {
		t.Fatalf("consultation got a browser: %v", err)
	}
	peer := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p", UserID: 1, ChatSessionID: "chat", ChatOwnerID: 2, workspaceRunID: "conv-workspace"}
	if runID, err := mainBotBrowserRun(peer); err != nil || runID != "conv-workspace" {
		t.Fatalf("project participant lost the shared browser: run=%q err=%v", runID, err)
	}
}

// An ordinary conversation that has not opened a workspace yet must learn what
// to do, not be told the tool does not exist.
func TestMainBotBrowserWithoutWorkspacePointsToWorkspaceOpen(t *testing.T) {
	h, owner := workbenchFixture(t)
	gateway := &agentToolGateway{deps: h.deps}
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "chat-no-workspace", ChatOwnerID: owner.ID}
	result, err := gateway.callMainBotBrowser(context.Background(), s, mainBotBrowserPrefix+"snapshot", nil)
	if err != nil {
		t.Fatalf("missing workspace must be a readable tool result, got %v", err)
	}
	content, _ := result["content"].([]map[string]any)
	if result["isError"] != true || len(content) == 0 {
		t.Fatalf("result = %+v", result)
	}
	if text, _ := content[0]["text"].(string); !strings.Contains(text, "workbench_workspace_open") {
		t.Fatalf("browser without a workspace must point to workbench_workspace_open: %q", text)
	}
}
