package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAssistantCLIReportsResultUsage(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codebuddy")
	script := `#!/bin/sh
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"完成"}]}}
{"type":"result","result":"完成","usage":{"input_tokens":4,"output_tokens":493,"cache_read_input_tokens":253736,"cache_creation_input_tokens":454},"total_cost_usd":0.12}
JSON
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	var got []domain.TokenUsage
	raw, _, err := runAssistantCLI(context.Background(), assistantCLIRequest{Prompt: "检查", SessionRoot: dir, OnUsage: func(u domain.TokenUsage) { got = append(got, u) }}, domain.User{ID: 1, CodeBuddyAPIKey: "test-key"}, nil)
	if err != nil || raw != "完成" {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
	if len(got) != 1 {
		t.Fatalf("usage reported %d times", len(got))
	}
	u := got[0]
	if u.InputTokens != 4 || u.OutputTokens != 493 || u.CacheReadTokens != 253736 || u.CacheWriteTokens != 454 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestAssistantCLISkipsUsageWithoutTokens(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codebuddy")
	script := "#!/bin/sh\necho '{\"type\":\"result\",\"result\":\"完成\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	called := false
	if _, _, err := runAssistantCLI(context.Background(), assistantCLIRequest{Prompt: "检查", SessionRoot: dir, OnUsage: func(domain.TokenUsage) { called = true }}, domain.User{ID: 1, CodeBuddyAPIKey: "test-key"}, nil); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("zero usage should not be reported")
	}
}

// generateWithFakeCLI runs one private generation against a fake CodeBuddy
// binary and returns the stored assistant message.
func generateWithFakeCLI(t *testing.T, script string) (assistantMessage, []byte) {
	t.Helper()
	h, user := workbenchFixture(t)
	bin := filepath.Join(t.TempDir(), "codebuddy")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	sid := "usage-" + filepath.Base(t.TempDir())
	body, _ := json.Marshal(assistantRequest{SessionID: sid, ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "检查文件"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		g, _ := h.repo.AssistantGeneration(user.ID, sid)
		if g.Status == "completed" {
			break
		}
		if g.Status != "running" || time.Now().After(deadline) {
			t.Fatalf("generation: %+v", g)
		}
		time.Sleep(10 * time.Millisecond)
	}
	saved, ok := h.repo.AssistantSession(user.ID, sid)
	if !ok || len(saved.Messages) != 2 {
		t.Fatalf("saved session: ok=%v %+v", ok, saved.Messages)
	}
	raw, _ := json.Marshal(saved.Messages[1])
	return saved.Messages[1], raw
}

func TestAssistantReplySumsUsageAcrossRetries(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "calls")
	script := `#!/bin/sh
if [ -f "` + counter + `" ]; then
cat <<'JSON'
{"type":"result","result":"检查结果正常，没有问题。","usage":{"input_tokens":20,"output_tokens":30,"cache_read_input_tokens":400,"cache_creation_input_tokens":5}}
JSON
else
touch "` + counter + `"
cat <<'JSON'
{"type":"result","result":"我先查看一下文件。","usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":100,"cache_creation_input_tokens":3}}
JSON
fi
`
	msg, raw := generateWithFakeCLI(t, script)
	if msg.Usage == nil {
		t.Fatalf("usage missing: %s", raw)
	}
	u := *msg.Usage
	if u.InputTokens != 21 || u.OutputTokens != 32 || u.CacheReadTokens != 500 || u.CacheWriteTokens != 8 {
		t.Fatalf("retry usage not summed: %+v", u)
	}
	if msg.Model == "" || !bytes.Contains(raw, []byte(`"model":"`+msg.Model+`"`)) {
		t.Fatalf("model not stored: %s", raw)
	}
}

func TestAssistantReplyOmitsUnreportedUsage(t *testing.T) {
	msg, raw := generateWithFakeCLI(t, "#!/bin/sh\necho '{\"type\":\"result\",\"result\":\"检查结果正常。\"}'\n")
	if msg.Usage != nil || bytes.Contains(raw, []byte(`"usage"`)) {
		t.Fatalf("usage should be omitted: %s", raw)
	}
	if msg.Model == "" {
		t.Fatalf("model missing: %s", raw)
	}
}

func TestAssistantPromptMessagesDropReplyMetadata(t *testing.T) {
	usage := domain.TokenUsage{InputTokens: 1}
	out := assistantPromptMessages([]assistantMessage{{Role: "assistant", Content: "ok", Model: "m", Usage: &usage}})
	if out[0].Model != "" || out[0].Usage != nil {
		t.Fatalf("metadata leaked into prompt: %+v", out[0])
	}
}
