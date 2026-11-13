package httpapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"agentflow/internal/runtime"
)

// prepareAssistantCodexInvocation runs one chat turn on `codex app-server`.
// `codex exec --json` reports only finished items; the app server also
// streams reasoning and reply deltas over its stdio JSON-RPC.
func prepareAssistantCodexInvocation(o assistantEngineInvocationOpts) (assistantCLIInvocation, error) {
	servers, err := readAssistantMCPServers(o.Config)
	if err != nil {
		return assistantCLIInvocation{}, err
	}
	thread := ""
	if o.Resume {
		if thread = readCodexThread(o.Root, o.SessionID); thread == "" || !codexConfigKeyRe.MatchString(thread) {
			return assistantCLIInvocation{resumeMissing: true}, nil
		}
	}
	guard := filepath.Join(o.Root, "codex-native-guard.py")
	if err = os.WriteFile(guard, []byte(assistantCodexGuard), 0o600); err != nil {
		return assistantCLIInvocation{}, err
	}
	argv := []string{o.Bin, "app-server", "-c", `web_search="disabled"`}
	for _, feature := range codexAssistantDisabledFeatures {
		argv = append(argv, "-c", "features."+feature+"=false")
	}
	argv = append(argv, codexAssistantGuardArgs(guard)...)
	argv = append(argv, codexAssistantMCPArgs(servers)...)
	if effort := strings.TrimSpace(o.Effort); effort != "" {
		argv = append(argv, "-c", "model_reasoning_effort="+strconv.Quote(effort))
	}
	// BindCodex inserts its provider overrides before the last argument.
	argv = append(argv, "--stdio")
	stdin, input, err := os.Pipe()
	if err != nil {
		return assistantCLIInvocation{}, err
	}
	server := &codexAppServer{input: input, opts: o, resume: thread, pending: map[string]string{}, started: map[string]bool{}}
	server.request("initialize", map[string]any{"clientInfo": map[string]string{"name": "agentflow", "title": "AgentFlow", "version": "1"}})
	return assistantCLIInvocation{
		command:     runtime.CommandSpec{Argv: argv, Provider: "codex", CredentialBindings: []runtime.CredentialBinding{runtime.BindCodex}, Stdin: stdin},
		credentials: runtime.RunCredentials{Codex: o.User},
		sessionRef:  thread,
		translate:   server.translate,
		finish: func(ok bool) {
			server.closeInput()
			_ = stdin.Close()
			if ok && server.threadID != "" && codexConfigKeyRe.MatchString(server.threadID) {
				_ = writeCodexThread(o.Root, o.SessionID, server.threadID)
			}
		},
	}, nil
}

// codexAppServer drives one turn: initialize, thread/start (or
// thread/resume), turn/start, then closes stdin on turn/completed so the
// server exits. translate runs on the process's stdout goroutine and rewrites
// notifications as the CodeBuddy-style stream-json lines the chat pipeline
// parses: deltas become stream_event blocks, finished messages and reasoning
// become assistant blocks, tool items become tool_use / tool_result, and the
// turn's end becomes the result line.
type codexAppServer struct {
	input       *os.File
	inputOnce   sync.Once
	opts        assistantEngineInvocationOpts
	resume      string
	nextID      int
	pending     map[string]string
	threadID    string
	lastMessage string
	turnError   string
	usage       map[string]any
	started     map[string]bool
	// block is the key of the open stream_event content block.
	block string
}

type codexRPCMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type codexAppItem struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Summary   []string        `json:"summary"`
	Content   []string        `json:"content"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Status    string          `json:"status"`
	Error     json.RawMessage `json:"error"`
	Command   string          `json:"command"`
	Query     string          `json:"query"`
	ExitCode  *int            `json:"exitCode"`
}

func codexLine(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func (s *codexAppServer) write(message map[string]any) {
	data, err := json.Marshal(message)
	if err != nil {
		return
	}
	// A failed write means the server already exited; its exit status reports why.
	_, _ = s.input.Write(append(data, '\n'))
}

func (s *codexAppServer) request(method string, params any) {
	s.nextID++
	s.pending[strconv.Itoa(s.nextID)] = method
	s.write(map[string]any{"id": s.nextID, "method": method, "params": params})
}

func (s *codexAppServer) closeInput() {
	s.inputOnce.Do(func() { _ = s.input.Close() })
}

// fail ends the turn with an error line the pipeline reports.
func (s *codexAppServer) fail(message string) []string {
	s.closeInput()
	return append(s.closeBlock(), codexLine(map[string]any{"type": "error", "error": message}))
}

func (s *codexAppServer) translate(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return nil
	}
	var message codexRPCMessage
	if json.Unmarshal([]byte(line), &message) != nil {
		return nil
	}
	switch {
	case message.Method != "" && len(message.ID) > 0:
		// Approvals and elicitations cannot be answered in a chat turn; the
		// platform gateway applies the user's approval mode itself.
		s.write(map[string]any{"id": message.ID, "error": map[string]any{"code": -32601, "message": "AgentFlow 对话不支持该请求"}})
		return nil
	case message.Method == "":
		return s.response(message)
	default:
		return s.notification(message.Method, message.Params)
	}
}

func (s *codexAppServer) response(message codexRPCMessage) []string {
	id := string(message.ID)
	method := s.pending[id]
	delete(s.pending, id)
	failure := ""
	if message.Error != nil {
		failure = strings.TrimSpace(message.Error.Message)
		if failure == "" {
			failure = method + " failed"
		}
	}
	switch method {
	case "initialize":
		if failure != "" {
			return s.fail("Codex app-server 初始化失败：" + failure)
		}
		s.write(map[string]any{"method": "initialized", "params": map[string]any{}})
		params := map[string]any{"developerInstructions": s.opts.SystemPrompt, "sandbox": "read-only", "approvalPolicy": "never"}
		if model := strings.TrimSpace(s.opts.Model); model != "" {
			// Without a model the app server uses Codex's configured default.
			params["model"] = model
		}
		if s.resume != "" {
			params["threadId"] = s.resume
			s.request("thread/resume", params)
		} else {
			s.request("thread/start", params)
		}
	case "thread/start", "thread/resume":
		if failure != "" {
			if method == "thread/resume" {
				// runAssistantCLI recognises this text as a missing session.
				return s.fail("No conversation found with session ID: " + s.resume)
			}
			return s.fail(failure)
		}
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		_ = json.Unmarshal(message.Result, &result)
		s.threadID = strings.TrimSpace(result.Thread.ID)
		s.request("turn/start", map[string]any{"threadId": s.threadID, "input": []map[string]any{{"type": "text", "text": s.opts.Prompt, "text_elements": []any{}}}})
		return []string{codexLine(map[string]any{"type": "system", "subtype": "init", "session_id": s.threadID})}
	case "turn/start":
		if failure != "" {
			return s.fail(failure)
		}
	}
	return nil
}

func (s *codexAppServer) notification(method string, raw json.RawMessage) []string {
	var params struct {
		ItemID       string       `json:"itemId"`
		Delta        string       `json:"delta"`
		SummaryIndex int          `json:"summaryIndex"`
		ContentIndex int          `json:"contentIndex"`
		Item         codexAppItem `json:"item"`
		WillRetry    bool         `json:"willRetry"`
		Error        struct {
			Message string `json:"message"`
		} `json:"error"`
		TokenUsage struct {
			Last struct {
				InputTokens       int64 `json:"inputTokens"`
				CachedInputTokens int64 `json:"cachedInputTokens"`
				OutputTokens      int64 `json:"outputTokens"`
			} `json:"last"`
		} `json:"tokenUsage"`
		Turn struct {
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return nil
	}
	switch method {
	case "item/agentMessage/delta":
		return s.delta(params.ItemID, "text", params.Delta)
	case "item/reasoning/summaryTextDelta":
		return s.delta(fmt.Sprintf("%s#%d", params.ItemID, params.SummaryIndex), "thinking", params.Delta)
	case "item/reasoning/textDelta":
		return s.delta(fmt.Sprintf("%s#raw%d", params.ItemID, params.ContentIndex), "thinking", params.Delta)
	case "item/started":
		return s.item(params.Item, false)
	case "item/completed":
		return s.item(params.Item, true)
	case "thread/tokenUsage/updated":
		last := params.TokenUsage.Last
		s.usage = map[string]any{"input_tokens": last.InputTokens, "cached_input_tokens": last.CachedInputTokens, "output_tokens": last.OutputTokens}
	case "error":
		if !params.WillRetry {
			s.turnError = strings.TrimSpace(params.Error.Message)
		}
	case "turn/completed":
		if params.Turn.Status == "completed" {
			s.closeInput()
			return append(s.closeBlock(), codexLine(map[string]any{"type": "result", "result": s.lastMessage, "is_error": false, "usage": s.usage, "session_id": s.threadID}))
		}
		message := s.turnError
		if params.Turn.Error != nil && strings.TrimSpace(params.Turn.Error.Message) != "" {
			message = strings.TrimSpace(params.Turn.Error.Message)
		}
		if message == "" {
			message = "Codex turn " + params.Turn.Status
		}
		return s.fail(message)
	}
	return nil
}

// delta streams text into the content block key, opening it first.
func (s *codexAppServer) delta(key, kind, text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	if s.block != key {
		out = append(s.closeBlock(), codexLine(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": kind}}}))
		s.block = key
	}
	delta := map[string]any{"type": "text_delta", "text": text}
	if kind == "thinking" {
		delta = map[string]any{"type": "thinking_delta", "thinking": text}
	}
	return append(out, codexLine(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": delta}}))
}

func (s *codexAppServer) closeBlock() []string {
	if s.block == "" {
		return nil
	}
	s.block = ""
	return []string{codexLine(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_stop"}})}
}

func codexAssistantBlock(block map[string]any) string {
	return codexLine(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{block}}})
}

func (s *codexAppServer) item(item codexAppItem, completed bool) []string {
	id := strings.TrimSpace(item.ID)
	var name string
	var input any
	switch item.Type {
	case "agentMessage":
		if !completed || strings.TrimSpace(item.Text) == "" {
			return nil
		}
		s.lastMessage = item.Text
		return append(s.closeBlock(), codexAssistantBlock(map[string]any{"type": "text", "text": item.Text}))
	case "reasoning":
		// Models without reasoning summaries (e.g. DeepSeek) return raw reasoning text instead.
		text := strings.Join(item.Summary, "\n\n")
		if strings.TrimSpace(text) == "" {
			text = strings.Join(item.Content, "\n\n")
		}
		if !completed || strings.TrimSpace(text) == "" {
			return nil
		}
		return append(s.closeBlock(), codexAssistantBlock(map[string]any{"type": "thinking", "thinking": text}))
	case "mcpToolCall":
		name = "mcp__" + item.Server + "__" + item.Tool
		input = item.Arguments
		if len(item.Arguments) == 0 || string(item.Arguments) == "null" {
			input = map[string]any{}
		}
	case "commandExecution":
		name, input = "Bash", map[string]string{"command": item.Command}
	case "webSearch":
		name, input = "WebSearch", map[string]string{"query": item.Query}
	default:
		return nil
	}
	if id == "" {
		return nil
	}
	out := s.closeBlock()
	if !s.started[id] {
		s.started[id] = true
		out = append(out, codexAssistantBlock(map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}))
	}
	if completed {
		failed := item.Status == "failed" || item.Status == "declined" || (len(item.Error) > 0 && string(item.Error) != "null") || (item.ExitCode != nil && *item.ExitCode != 0)
		out = append(out, codexLine(map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "is_error": failed, "content": ""}}}}))
	}
	return out
}
