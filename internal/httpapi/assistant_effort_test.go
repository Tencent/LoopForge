package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"strings"
	"testing"
)

func TestAssistantEffortReachesNativeCLIAndPersists(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", assistantChatDefaultModel)
	h, user := workbenchFixture(t)
	_, calls := nativeCLIRecorder(t)
	messages := []assistantMessage{{Role: "user", Content: "回答一个简单问题"}}
	for _, effort := range []string{"max", "high", ""} {
		body, _ := json.Marshal(assistantRequest{SessionID: "effort-native", Model: assistantChatDefaultModel, Effort: effort, Messages: messages})
		c, rec := workbenchContext(user, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
		h.chat(c)
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		got := calls()
		if got[len(got)-1].flag("--effort") != effort {
			t.Fatalf("effort missing from native CLI: %#v", got[len(got)-1].args)
		}
		saved, ok := h.repo.AssistantSession(user.ID, "effort-native")
		if !ok || saved.Effort != effort {
			t.Fatalf("effort was not saved: %+v", saved)
		}
		messages = append(saved.Messages, assistantMessage{Role: "user", Content: "继续"})
	}
	if got := calls(); got[1].flag("--resume") == "" {
		t.Fatal("effort change lost conversation continuation")
	}
	// An unsupported effort must fail before any native invocation or persistence.
	body, _ := json.Marshal(assistantRequest{ProjectID: "workbench", SessionID: "effort-invalid", Model: "hy4-preview-ioa", Effort: "max", Messages: []assistantMessage{{Role: "user", Content: "你好"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 400 || len(calls()) != 3 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if _, ok := h.repo.AssistantSession(user.ID, "effort-invalid"); ok {
		t.Fatal("invalid effort created a conversation")
	}
}

func TestAssistantEffortSessionPutPreservesAbsentValue(t *testing.T) {
	h, user := workbenchFixture(t)
	if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: "effort-put", Model: assistantChatDefaultModel, Effort: "max"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ body, effort string }{{`{"title":"renamed"}`, "max"}, {`{"effort":""}`, ""}} {
		c, rec := workbenchContext(user, "PUT", "/api/assistant/sessions/effort-put", bytes.NewBufferString(tc.body))
		c.Params = gin.Params{{Key: "id", Value: "effort-put"}}
		h.session(c)
		saved, ok := h.repo.AssistantSession(user.ID, "effort-put")
		if rec.Code != 200 || !ok || saved.Effort != tc.effort {
			t.Fatal(rec.Code, saved.Effort, rec.Body.String())
		}
	}
}

func TestAssistantReviewCommandHasNoToolsOrPersistentSession(t *testing.T) {
	command, err := prepareAssistantCommand(assistantCommandOpts{Bin: "codebuddy", Config: "/tmp/empty-review-mcp.json", Prompt: "review data", Model: assistantChatDefaultModel, Effort: "high", ReviewOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	call := nativeCLICall{args: command.Argv}
	if call.flag("--tools") != "" || call.flag("--max-turns") != "1" || call.flag("--effort") != "high" {
		t.Fatal(command.Argv)
	}
	for _, flag := range []string{"--tools", "--no-session-persistence", "--strict-mcp-config"} {
		if !strings.Contains(strings.Join(command.Argv, "\n"), flag) {
			t.Fatal("review isolation flag missing", flag)
		}
	}
	for _, binding := range command.CredentialBindings {
		if string(binding) != "codebuddy" {
			t.Fatal("reviewer inherited non-model credential", binding)
		}
	}
}
