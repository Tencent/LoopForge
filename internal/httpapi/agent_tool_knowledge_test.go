package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
)

func newKnowledgeAgentToolSession(t *testing.T, projectID string, mode AgentToolMode, scheduled bool) *AgentToolSession {
	t.Helper()
	session := &AgentToolSession{
		ProjectID: projectID, Mode: mode, Scheduled: scheduled,
		ExpiresAt: time.Now().Add(time.Minute),
		Final:     make(chan AgentTerminalResult, 1),
	}
	if err := agentToolSessions.create(session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	return session
}

func agentToolListNames(t *testing.T, handler http.Handler, session *AgentToolSession) []string {
	t.Helper()
	rec := doJSON(t, handler, http.MethodPost, "/internal/agent-tools/"+session.Token+"/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode tools/list: %v body=%s", err, rec.Body.String())
	}
	names := make([]string, 0, len(response.Result.Tools))
	for _, tool := range response.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func agentToolCall(t *testing.T, handler http.Handler, session *AgentToolSession, name, arguments string) (string, bool) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + arguments + `}}`
	rec := doJSON(t, handler, http.MethodPost, "/internal/agent-tools/"+session.Token+"/mcp", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", name, rec.Code, rec.Body.String())
	}
	text, isError, protocolError := rpcText(t, rec)
	if protocolError != "" {
		t.Fatalf("%s protocol error: %s", name, protocolError)
	}
	return text, isError
}

func assertKnowledgeToolsAbsent(t *testing.T, names []string) {
	t.Helper()
	for _, name := range knowledge.AgentTools {
		if slices.Contains(names, name) {
			t.Fatalf("knowledge tool %q must not be in catalog: %v", name, names)
		}
	}
}

func assertKnowledgeToolsPresent(t *testing.T, names []string) {
	t.Helper()
	for _, name := range knowledge.AgentTools {
		if !slices.Contains(names, name) {
			t.Fatalf("knowledge tool %q missing from catalog: %v", name, names)
		}
	}
}

func TestMainBotKnowledgeCatalogFollowsConversationScope(t *testing.T) {
	resetAgentToolSessionsForTest()
	_, handler := newKnowledgeTestServer(t)

	assistant := newKnowledgeAgentToolSession(t, "p1", AgentToolAssistant, false)
	assertKnowledgeToolsPresent(t, agentToolListNames(t, handler, assistant))

	clarify := newKnowledgeAgentToolSession(t, "p1", AgentToolClarify, false)
	assertKnowledgeToolsAbsent(t, agentToolListNames(t, handler, clarify))

	scheduled := newKnowledgeAgentToolSession(t, "p1", AgentToolAssistant, true)
	assertKnowledgeToolsAbsent(t, agentToolListNames(t, handler, scheduled))

	legacyScheduled := newKnowledgeAgentToolSession(t, "p1", AgentToolScheduled, false)
	assertKnowledgeToolsAbsent(t, agentToolListNames(t, handler, legacyScheduled))

	noProject := newKnowledgeAgentToolSession(t, "", AgentToolAssistant, false)
	assertKnowledgeToolsAbsent(t, agentToolListNames(t, handler, noProject))
}

func TestMainBotKnowledgeCallsUseSessionProject(t *testing.T) {
	resetAgentToolSessionsForTest()
	_, handler := newKnowledgeTestServer(t)

	projectA := newKnowledgeAgentToolSession(t, "p1", AgentToolAssistant, false)
	text, isError := agentToolCall(t, handler, projectA, knowledge.ToolSearch, `{"query":"检索"}`)
	if isError || !strings.Contains(text, `"source":"a"`) {
		t.Fatalf("project search: isError=%t body=%s", isError, text)
	}
	text, isError = agentToolCall(t, handler, projectA, knowledge.ToolFetch, `{"source":"a","path":"x.md"}`)
	if isError || !strings.Contains(text, "检索正文") {
		t.Fatalf("project fetch: isError=%t body=%s", isError, text)
	}
	text, isError = agentToolCall(t, handler, projectA, knowledge.ToolListSources, `{}`)
	if isError || !strings.Contains(text, `"id":"a"`) || strings.Contains(text, `"id":"b"`) {
		t.Fatalf("project sources: isError=%t body=%s", isError, text)
	}
	text, isError = agentToolCall(t, handler, projectA, knowledge.ToolGetSource, `{"source":"a"}`)
	if isError || !strings.Contains(text, `"path":"x.md"`) {
		t.Fatalf("project source detail: isError=%t body=%s", isError, text)
	}

	if text, isError = agentToolCall(t, handler, projectA, knowledge.ToolSearch, `{"query":"检索","sources":["b"]}`); !isError || !strings.Contains(text, "授权范围") {
		t.Fatalf("out-of-scope source search must fail: isError=%t body=%s", isError, text)
	}
	if text, isError = agentToolCall(t, handler, projectA, knowledge.ToolFetch, `{"source":"b","path":"x.md"}`); !isError || !strings.Contains(text, "授权范围") {
		t.Fatalf("out-of-scope source fetch must fail: isError=%t body=%s", isError, text)
	}

	projectB := newKnowledgeAgentToolSession(t, "p2", AgentToolAssistant, false)
	if text, isError = agentToolCall(t, handler, projectB, knowledge.ToolSearch, `{"query":"检索"}`); isError || strings.Contains(text, `"source":"a"`) || !strings.Contains(text, `"results":[]`) {
		t.Fatalf("other project must not search project A's documents: isError=%t body=%s", isError, text)
	}
	if text, isError = agentToolCall(t, handler, projectB, knowledge.ToolFetch, `{"source":"a","path":"x.md"}`); !isError || !strings.Contains(text, "授权范围") {
		t.Fatalf("other project must not fetch project A's documents: isError=%t body=%s", isError, text)
	}
	if text, isError = agentToolCall(t, handler, projectB, knowledge.ToolGetSource, `{"source":"a"}`); !isError || !strings.Contains(text, "授权范围") {
		t.Fatalf("other project must not read project A's source detail: isError=%t body=%s", isError, text)
	}
}

func TestMainBotKnowledgeActivityRecordsSessionAndSurvivesRestart(t *testing.T) {
	resetAgentToolSessionsForTest()
	s, handler := newKnowledgeTestServer(t)

	session := newKnowledgeAgentToolSession(t, "p1", AgentToolAssistant, false)
	session.RunID = "run-bot"
	session.ChatSessionID = "chat-bot"
	if text, isError := agentToolCall(t, handler, session, knowledge.ToolSearch, `{"query":"检索"}`); isError || !strings.Contains(text, `"source":"a"`) {
		t.Fatalf("Main Bot search: isError=%t body=%s", isError, text)
	}

	restarted := newKnowledgeTestHandler(t, s)
	usage := doJSON(t, restarted, http.MethodGet, "/api/knowledge/usage?projectId=p1", "", nil)
	var payload struct {
		Activity []domain.KnowledgeActivity `json:"activity"`
	}
	if err := json.Unmarshal(usage.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode usage: %v body=%s", err, usage.Body.String())
	}
	if len(payload.Activity) != 1 {
		t.Fatalf("Main Bot activity after restart = %#v", payload.Activity)
	}
	item := payload.Activity[0]
	if item.Tool != knowledge.ToolSearch || !item.OK || item.Query != "检索" || item.Hits != 1 ||
		item.RunID != "run-bot" || item.SessionID != "chat-bot" || item.ProjectID != "p1" {
		t.Fatalf("Main Bot activity = %#v", item)
	}
	if item.At.IsZero() {
		t.Fatal("Main Bot activity time must be recorded")
	}

	other := newKnowledgeAgentToolSession(t, "p2", AgentToolAssistant, false)
	other.RunID = "run-other"
	if _, isError := agentToolCall(t, handler, other, knowledge.ToolSearch, `{"query":"检索"}`); isError {
		t.Fatal("project p2 search failed")
	}
	usage = doJSON(t, restarted, http.MethodGet, "/api/knowledge/usage?projectId=p1", "", nil)
	if err := json.Unmarshal(usage.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Activity) != 1 || payload.Activity[0].ProjectID != "p1" {
		t.Fatalf("project p1 saw another project's Main Bot activity: %#v", payload.Activity)
	}
}

func TestScheduledToolSessionMarksItselfUnattended(t *testing.T) {
	h, user := workbenchFixture(t)
	scheduled, files, err := h.openScheduledToolSession(context.Background(), user, "workbench", "scheduled-knowledge", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduled.Close()
	defer files.Cleanup()
	if !scheduled.Scheduled {
		t.Fatal("scheduled tool session must opt out of interactive-only tools")
	}
}

func TestAssistantSystemPromptIncludesKnowledgeUsageRule(t *testing.T) {
	rule := knowledge.UsageRule().Content
	prompt := assistantSystemPromptText()
	for _, name := range knowledge.AgentTools {
		if strings.Contains(rule, name) && !strings.Contains(prompt, name) {
			t.Errorf("assistant system prompt missing knowledge tool %q", name)
		}
	}
	if !strings.Contains(prompt, rule) {
		t.Fatal("assistant system prompt must embed knowledge.UsageRule() verbatim")
	}
	if !strings.Contains(rule, knowledge.ToolReportIssue) || !strings.Contains(prompt, knowledge.ToolReportIssue) {
		t.Fatalf("system prompt must advertise %s", knowledge.ToolReportIssue)
	}
	if strings.Contains(prompt, knowledge.ToolWrite) {
		t.Fatalf("assistant system prompt must not advertise the knowledge write tool %q", knowledge.ToolWrite)
	}
}

func TestMainBotReportIssueRecordsGovernanceFeedback(t *testing.T) {
	resetAgentToolSessionsForTest()
	s, handler := newKnowledgeTestServer(t)
	session := newKnowledgeAgentToolSession(t, "p1", AgentToolAssistant, false)
	session.RunID = "run-1"
	session.ChatSessionID = "chat-1"
	text, isError := agentToolCall(t, handler, session, knowledge.ToolReportIssue, `{"source":"a","path":"x.md","issue":"命令已经过时"}`)
	if isError || !strings.Contains(text, `"recorded":true`) {
		t.Fatalf("report issue: isError=%t body=%s", isError, text)
	}
	list := s.KnowledgeFeedbacks("p1", 10)
	if len(list) != 1 || list[0].Kind != domain.KnowledgeFeedbackIssue || list[0].Note != "命令已经过时" || list[0].RunID != "run-1" {
		t.Fatalf("feedback = %+v", list)
	}
}
