package httpapi

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type codexTestRPC struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params map[string]any  `json:"params"`
	Error  map[string]any  `json:"error"`
}

// codexTestTurn prepares a Codex invocation and reads what it writes to the
// app server's stdin.
func codexTestTurn(t *testing.T, resume bool) (assistantCLIInvocation, *bufio.Reader, string) {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "mcp.json")
	if err := os.WriteFile(config, []byte(`{"mcpServers":{"agentflow_session":{"url":"http://gateway/mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if resume {
		if err := writeCodexThread(root, "s1", "019a-old"); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := prepareAssistantCodexInvocation(assistantEngineInvocationOpts{Bin: "codex", Root: root, Config: config, Prompt: "暗号是什么？", Model: "gpt-5.5", Effort: "high", SessionID: "s1", SystemPrompt: "你是平台助手", Resume: resume})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inv.finish(false) })
	stdin, ok := inv.command.Stdin.(*os.File)
	if !ok {
		t.Fatalf("stdin is %T", inv.command.Stdin)
	}
	return inv, bufio.NewReader(stdin), root
}

func readCodexTestRPC(t *testing.T, r *bufio.Reader) codexTestRPC {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	var message codexTestRPC
	if err := json.Unmarshal([]byte(line), &message); err != nil {
		t.Fatalf("stdin line %q: %v", line, err)
	}
	return message
}

func codexLineTypes(t *testing.T, lines []string) []string {
	t.Helper()
	var out []string
	for _, line := range lines {
		var v struct {
			Type  string `json:"type"`
			Event struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
				} `json:"delta"`
			} `json:"event"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		kind := v.Type
		switch {
		case v.Type == "stream_event" && v.Event.Delta.Type != "":
			kind += ":" + v.Event.Delta.Type
		case v.Type == "stream_event":
			kind += ":" + v.Event.Type
		case len(v.Message.Content) > 0:
			kind += ":" + v.Message.Content[0].Type
		}
		out = append(out, kind)
	}
	return out
}

func TestCodexAppServerTurn(t *testing.T) {
	inv, stdin, root := codexTestTurn(t, false)
	argv := inv.command.Argv
	if argv[1] != "app-server" || argv[len(argv)-1] != "--stdio" || slices.Contains(argv, "exec") || inv.sessionRef != "" {
		t.Fatalf("argv: %v", argv)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{`model_reasoning_effort="high"`, "features.shell_tool=false", "features.view_image=false", "features.goals=false", "features.hooks=true", "hooks.PreToolUse=", `mcp_servers.agentflow_session.url="http://gateway/mcp"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, argv)
		}
	}
	if strings.Contains(joined, "--dangerously-bypass-hook-trust") {
		t.Fatalf("app-server argv must not use the exec-only trust flag: %v", argv)
	}
	guard, err := os.ReadFile(filepath.Join(root, "codex-native-guard.py"))
	if err != nil || !strings.Contains(string(guard), "native tools are disabled") {
		t.Fatalf("guard script = %q err=%v", guard, err)
	}
	if m := readCodexTestRPC(t, stdin); m.Method != "initialize" || string(m.ID) != "1" {
		t.Fatalf("first request: %+v", m)
	}
	if out := inv.translate(`{"id":1,"result":{"userAgent":"codex"}}`); len(out) != 0 {
		t.Fatalf("initialize response produced %v", out)
	}
	if m := readCodexTestRPC(t, stdin); m.Method != "initialized" || len(m.ID) != 0 {
		t.Fatalf("initialized notification: %+v", m)
	}
	start := readCodexTestRPC(t, stdin)
	if start.Method != "thread/start" || start.Params["model"] != "gpt-5.5" || start.Params["developerInstructions"] != "你是平台助手" || start.Params["sandbox"] != "read-only" || start.Params["approvalPolicy"] != "never" {
		t.Fatalf("thread/start: %+v", start)
	}
	if config, _ := start.Params["config"].(map[string]any); config["bypass_hook_trust"] != true {
		t.Fatalf("platform denial hook not trusted by thread: %+v", start)
	}
	if out := inv.translate(`{"id":2,"result":{"thread":{"id":"019a-thread"}}}`); len(out) != 1 || !strings.Contains(out[0], `"session_id":"019a-thread"`) {
		t.Fatalf("thread started: %v", out)
	}
	turn := readCodexTestRPC(t, stdin)
	input, _ := turn.Params["input"].([]any)
	if turn.Method != "turn/start" || turn.Params["threadId"] != "019a-thread" || len(input) != 1 || input[0].(map[string]any)["text"] != "暗号是什么？" {
		t.Fatalf("turn/start: %+v", turn)
	}
	// An approval request cannot be answered in chat; it is refused instead of hanging the turn.
	inv.translate(`{"id":"req-1","method":"item/commandExecution/requestApproval","params":{}}`)
	if m := readCodexTestRPC(t, stdin); string(m.ID) != `"req-1"` || m.Error == nil {
		t.Fatalf("server request answer: %+v", m)
	}
	var lines []string
	for _, event := range []string{
		`{"method":"item/started","params":{"item":{"type":"reasoning","id":"r1","summary":[]}}}`,
		`{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","summaryIndex":0,"delta":"想"}}`,
		`{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","summaryIndex":0,"delta":"一想"}}`,
		`{"method":"item/completed","params":{"item":{"type":"reasoning","id":"r1","summary":["想一想"]}}}`,
		`{"method":"item/started","params":{"item":{"type":"mcpToolCall","id":"m1","server":"agentflow","tool":"read_secret","arguments":{"k":1},"status":"inProgress"}}}`,
		`{"method":"item/completed","params":{"item":{"type":"mcpToolCall","id":"m1","server":"agentflow","tool":"read_secret","arguments":{"k":1},"status":"completed","error":null}}}`,
		`{"method":"item/agentMessage/delta","params":{"itemId":"a1","delta":"暗号"}}`,
		`{"method":"item/agentMessage/delta","params":{"itemId":"a1","delta":"是 42"}}`,
		`{"method":"item/completed","params":{"item":{"type":"agentMessage","id":"a1","text":"暗号是 42"}}}`,
		`{"method":"thread/tokenUsage/updated","params":{"tokenUsage":{"last":{"inputTokens":10,"cachedInputTokens":4,"outputTokens":3},"total":{"inputTokens":99}}}}`,
		`{"method":"turn/completed","params":{"turn":{"id":"t1","status":"completed","items":[]}}}`,
		`not json`,
	} {
		lines = append(lines, inv.translate(event)...)
	}
	want := []string{
		"stream_event:content_block_start", "stream_event:thinking_delta", "stream_event:thinking_delta", "stream_event:content_block_stop", "assistant:thinking",
		"assistant:tool_use", "user:tool_result",
		"stream_event:content_block_start", "stream_event:text_delta", "stream_event:text_delta", "stream_event:content_block_stop", "assistant:text",
		"result",
	}
	if got := codexLineTypes(t, lines); !slices.Equal(got, want) {
		t.Fatalf("lines:\n got %v\nwant %v", got, want)
	}
	// The pipeline sees streamed deltas: thinking and the reply arrive piece by piece.
	var streamed []assistantStreamEvent
	for _, line := range lines {
		streamed = append(streamed, assistantStreamEvents(line)...)
	}
	var answer []string
	for _, ev := range streamed {
		if ev.Kind == "answer" && !ev.Full {
			answer = append(answer, ev.Text)
		}
	}
	if !slices.Equal(answer, []string{"暗号", "是 42"}) {
		t.Fatalf("streamed answer: %+v", streamed)
	}
	var result struct {
		Result string         `json:"result"`
		Usage  map[string]any `json:"usage"`
	}
	_ = json.Unmarshal([]byte(lines[len(lines)-1]), &result)
	if result.Result != "暗号是 42" || result.Usage["input_tokens"] != float64(10) || result.Usage["cached_input_tokens"] != float64(4) {
		t.Fatalf("result line: %s", lines[len(lines)-1])
	}
	// turn/completed closes stdin so the app server exits.
	done := make(chan error, 1)
	go func() { _, err := stdin.ReadByte(); done <- err }()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatalf("stdin after turn: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stdin still open after turn/completed")
	}
	inv.finish(true)
	if got := readCodexThread(root, "s1"); got != "019a-thread" {
		t.Fatalf("thread not recorded: %q", got)
	}
}

func TestCodexAppServerRawReasoning(t *testing.T) {
	inv, _, _ := codexTestTurn(t, false)
	var lines []string
	for _, event := range []string{
		`{"method":"item/reasoning/textDelta","params":{"itemId":"r1","contentIndex":0,"delta":"先"}}`,
		`{"method":"item/reasoning/textDelta","params":{"itemId":"r1","contentIndex":0,"delta":"想想"}}`,
		`{"method":"item/completed","params":{"item":{"type":"reasoning","id":"r1","summary":[],"content":["先想想"]}}}`,
	} {
		lines = append(lines, inv.translate(event)...)
	}
	want := []string{"stream_event:content_block_start", "stream_event:thinking_delta", "stream_event:thinking_delta", "stream_event:content_block_stop", "assistant:thinking"}
	if got := codexLineTypes(t, lines); !slices.Equal(got, want) {
		t.Fatalf("lines:\n got %v\nwant %v", got, want)
	}
	if !strings.Contains(lines[len(lines)-1], `"thinking":"先想想"`) {
		t.Fatalf("thinking block: %s", lines[len(lines)-1])
	}
}

func TestCodexAppServerResume(t *testing.T) {
	inv, stdin, _ := codexTestTurn(t, true)
	if inv.sessionRef != "019a-old" {
		t.Fatalf("sessionRef %q", inv.sessionRef)
	}
	readCodexTestRPC(t, stdin)
	inv.translate(`{"id":1,"result":{}}`)
	readCodexTestRPC(t, stdin)
	m := readCodexTestRPC(t, stdin)
	if m.Method != "thread/resume" || m.Params["threadId"] != "019a-old" || m.Params["model"] != "gpt-5.5" {
		t.Fatalf("thread/resume: %+v", m)
	}
	if config, _ := m.Params["config"].(map[string]any); config["bypass_hook_trust"] != true {
		t.Fatalf("resumed thread lost denial hook trust: %+v", m)
	}
	// A thread the server cannot load reads as a missing session.
	out := inv.translate(`{"id":2,"error":{"code":-32600,"message":"no rollout found for thread id 019a-old"}}`)
	if len(out) != 1 || !strings.Contains(out[0], "No conversation found with session ID: 019a-old") {
		t.Fatalf("missing thread: %v", out)
	}
	if _, err := stdin.ReadByte(); err != io.EOF {
		t.Fatalf("stdin after failure: %v", err)
	}
	// Without a recorded thread there is nothing to resume, and no process starts.
	root := t.TempDir()
	config := filepath.Join(root, "mcp.json")
	if err := os.WriteFile(config, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if inv, err := prepareAssistantCodexInvocation(assistantEngineInvocationOpts{Root: root, Config: config, SessionID: "s2", Resume: true}); err != nil || !inv.resumeMissing || inv.command.Stdin != nil {
		t.Fatalf("unrecorded thread: %v %+v", err, inv)
	}
}

func TestCodexAppServerTurnFailure(t *testing.T) {
	inv, stdin, _ := codexTestTurn(t, false)
	readCodexTestRPC(t, stdin)
	inv.translate(`{"id":1,"result":{}}`)
	readCodexTestRPC(t, stdin)
	readCodexTestRPC(t, stdin)
	inv.translate(`{"id":2,"result":{"thread":{"id":"019a-thread"}}}`)
	readCodexTestRPC(t, stdin)
	inv.translate(`{"method":"item/agentMessage/delta","params":{"itemId":"a1","delta":"半"}}`)
	// A retried error is not final; the turn's own error wins.
	inv.translate(`{"method":"error","params":{"error":{"message":"reconnecting"},"willRetry":true}}`)
	inv.translate(`{"method":"error","params":{"error":{"message":"quota exceeded"},"willRetry":false}}`)
	out := inv.translate(`{"method":"turn/completed","params":{"turn":{"id":"t1","status":"failed","items":[]}}}`)
	if got := codexLineTypes(t, out); !slices.Equal(got, []string{"stream_event:content_block_stop", "error"}) {
		t.Fatalf("failed turn: %v", out)
	}
	if err := assistantCLIStreamFailure(out[1]); err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("stream failure: %v", err)
	}
}
