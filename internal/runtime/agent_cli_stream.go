package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ClaudeStream renders the Claude-style stream-json lines that both the SOP
// protocol adapter (handleClaudeStream) and AI 对话 parse: deltas become
// stream_event content blocks, finished text / thinking / tool calls become
// assistant blocks, tool outputs become user tool_result blocks and the turn
// ends with a result line. Every line carries the CLI's session ID once it is
// known, so a node can resume the session.
type ClaudeStream struct {
	SessionID string
	// LastText is the latest finished assistant text, the turn's answer when
	// the CLI reports none of its own.
	LastText string
	block    string
	started  map[string]bool
	usage    map[string]int64
	done     bool
}

func (s *ClaudeStream) line(v map[string]any) string {
	if s.SessionID != "" {
		v["session_id"] = s.SessionID
	}
	data, _ := json.Marshal(v)
	return string(data)
}

// Init announces the session; model may be empty.
func (s *ClaudeStream) Init(sessionID, model string) []string {
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		s.SessionID = sessionID
	}
	event := map[string]any{"type": "system", "subtype": "init"}
	if model != "" {
		event["model"] = model
	}
	return []string{s.line(event)}
}

// Delta streams text into the content block key (kind "text" or
// "thinking"), opening it first.
func (s *ClaudeStream) Delta(key, kind, text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	if s.block != key {
		out = append(s.CloseBlock(), s.line(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": kind}}}))
		s.block = key
	}
	delta := map[string]any{"type": "text_delta", "text": text}
	if kind == "thinking" {
		delta = map[string]any{"type": "thinking_delta", "thinking": text}
	}
	return append(out, s.line(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": delta}}))
}

func (s *ClaudeStream) CloseBlock() []string {
	if s.block == "" {
		return nil
	}
	s.block = ""
	return []string{s.line(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_stop"}})}
}

func (s *ClaudeStream) assistant(block map[string]any) []string {
	return append(s.CloseBlock(), s.line(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []map[string]any{block}}}))
}

// Text records a finished assistant text block.
func (s *ClaudeStream) Text(text string) []string {
	if strings.TrimSpace(text) == "" {
		return s.CloseBlock()
	}
	s.LastText = text
	return s.assistant(map[string]any{"type": "text", "text": text})
}

// Thinking records a finished reasoning block.
func (s *ClaudeStream) Thinking(text string) []string {
	if strings.TrimSpace(text) == "" {
		return s.CloseBlock()
	}
	return s.assistant(map[string]any{"type": "thinking", "thinking": text})
}

// ToolUse records a tool call once per id.
func (s *ClaudeStream) ToolUse(id, name string, input any) []string {
	if id == "" || name == "" {
		return nil
	}
	if s.started == nil {
		s.started = map[string]bool{}
	}
	if s.started[id] {
		return nil
	}
	s.started[id] = true
	if input == nil {
		input = map[string]any{}
	}
	return s.assistant(map[string]any{"type": "tool_use", "id": id, "name": name, "input": input})
}

// ToolResult records a finished tool call.
func (s *ClaudeStream) ToolResult(id string, failed bool, content string) []string {
	if id == "" {
		return nil
	}
	return append(s.CloseBlock(), s.line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "is_error": failed, "content": content}}}}))
}

// AddUsage accumulates token counts reported per step.
func (s *ClaudeStream) AddUsage(input, output, cacheRead, cacheWrite, reasoning int64) {
	if s.usage == nil {
		s.usage = map[string]int64{}
	}
	s.usage["input_tokens"] += input
	s.usage["output_tokens"] += output
	s.usage["cache_read_input_tokens"] += cacheRead
	s.usage["cache_creation_input_tokens"] += cacheWrite
	s.usage["reasoning_tokens"] += reasoning
}

// SetUsage replaces the accumulated usage with a turn total.
func (s *ClaudeStream) SetUsage(input, output, cacheRead, cacheWrite, reasoning int64) {
	s.usage = nil
	s.AddUsage(input, output, cacheRead, cacheWrite, reasoning)
}

// Result ends the turn once; an empty text falls back to LastText.
func (s *ClaudeStream) Result(text string, failed bool) []string {
	if s.done {
		return nil
	}
	s.done = true
	if strings.TrimSpace(text) == "" && !failed {
		text = s.LastText
	}
	subtype := "success"
	if failed {
		subtype = "error"
	}
	event := map[string]any{"type": "result", "subtype": subtype, "is_error": failed, "result": text}
	if s.usage != nil {
		event["usage"] = s.usage
	}
	return append(s.CloseBlock(), s.line(event))
}

// Finished reports whether Result was emitted.
func (s *ClaudeStream) Finished() bool { return s.done }

// NewAgentCLITranslator returns the translator from an agent CLI's native
// headless output (one JSON object per line) to ClaudeStream lines.
func NewAgentCLITranslator(kind string) func(string) []string {
	var translate func(*ClaudeStream, map[string]any) []string
	switch kind {
	case AgentCLIOpenCode:
		translate = (&openCodeStream{}).translate
	case AgentCLICursor:
		translate = (&cursorStream{}).translate
	case AgentCLIAntigravity:
		translate = (&antigravityStream{}).translate
	case AgentCLIKimi:
		translate = (&kimiStream{}).translate
	case AgentCLIPi:
		translate = (&piStream{}).translate
	case AgentCLIDeepSeek:
		translate = (&dshStream{}).translate
	case AgentCLIZCode:
		translate = (&zcodeStream{}).translate
	case AgentCLIQoder:
		// Qoder's stream uses the same system/assistant/user/result envelope.
		return func(line string) []string {
			var event map[string]any
			if json.Unmarshal([]byte(line), &event) != nil {
				return nil
			}
			if stringValue(event["type"]) == "result" && strings.HasPrefix(stringValue(event["subtype"]), "error") {
				event["is_error"] = true
				if stringValue(event["result"]) == "" {
					var messages []string
					for _, item := range sliceValue(event["errors"]) {
						if message := stringValue(item); message != "" {
							messages = append(messages, message)
						}
					}
					event["result"] = strings.Join(messages, "\n")
				}
				data, _ := json.Marshal(event)
				return []string{string(data)}
			}
			switch stringValue(event["type"]) {
			case "system", "assistant", "user", "stream_event", "result":
				return []string{line}
			}
			return nil
		}
	default:
		return nil
	}
	stream := &ClaudeStream{}
	return func(line string) []string {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			return nil
		}
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			return nil
		}
		return translate(stream, raw)
	}
}

func jsonText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	data, _ := json.Marshal(value)
	return string(data)
}

// contentText joins the text parts of an MCP / chat content value.
func contentText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	var parts []string
	for _, item := range sliceValue(value) {
		part, _ := item.(map[string]any)
		if text := stringValue(part["text"]); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 && value != nil {
		return jsonText(value)
	}
	return strings.Join(parts, "\n")
}

func errorText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case map[string]any:
		for _, key := range []string{"message", "error"} {
			if text := errorText(v[key]); text != "" {
				return text
			}
		}
		if data, ok := v["data"].(map[string]any); ok {
			if text := errorText(data); text != "" {
				return text
			}
		}
		if name := stringValue(v["name"]); name != "" {
			return name
		}
	}
	return ""
}

// openCodeStream: `opencode run --format json` reports finished parts.
type openCodeStream struct {
	started bool
	failure string
}

func (o *openCodeStream) translate(s *ClaudeStream, raw map[string]any) []string {
	var out []string
	if !o.started {
		if session := stringValue(raw["sessionID"]); session != "" {
			o.started = true
			out = s.Init(session, "")
		}
	}
	part, _ := raw["part"].(map[string]any)
	switch stringValue(raw["type"]) {
	case "reasoning":
		out = append(out, s.Thinking(stringValue(part["text"]))...)
	case "text":
		out = append(out, s.Text(stringValue(part["text"]))...)
	case "tool_use":
		state, _ := part["state"].(map[string]any)
		id := stringValue(part["callID"])
		out = append(out, s.ToolUse(id, stringValue(part["tool"]), state["input"])...)
		switch stringValue(state["status"]) {
		case "completed":
			out = append(out, s.ToolResult(id, false, jsonText(state["output"]))...)
		case "error":
			out = append(out, s.ToolResult(id, true, jsonText(state["error"]))...)
		}
	case "step_finish":
		tokens, _ := part["tokens"].(map[string]any)
		cache, _ := tokens["cache"].(map[string]any)
		s.AddUsage(int64Value(tokens["input"]), int64Value(tokens["output"]), int64Value(cache["read"]), int64Value(cache["write"]), int64Value(tokens["reasoning"]))
		if reason := stringValue(part["reason"]); reason != "tool-calls" && reason != "" {
			out = append(out, s.Result("", o.failure != "" && s.LastText == "")...)
		}
	case "error":
		o.failure = errorText(raw["error"])
		if o.failure == "" {
			o.failure = "OpenCode 运行失败"
		}
		out = append(out, s.Result(o.failure, true)...)
	}
	return out
}

// cursorStream: `cursor-agent -p --output-format stream-json
// --stream-partial-output`. Partial assistant events carry timestamp_ms and
// one text chunk each; the final assistant event repeats the whole text.
type cursorStream struct {
	segment  int
	partial  strings.Builder
	thinking strings.Builder
}

func (c *cursorStream) flushText(s *ClaudeStream) []string {
	text := c.partial.String()
	c.partial.Reset()
	c.segment++
	return s.Text(text)
}

func (c *cursorStream) flushThinking(s *ClaudeStream) []string {
	text := c.thinking.String()
	c.thinking.Reset()
	if text == "" {
		return nil
	}
	c.segment++
	return s.Thinking(text)
}

func cursorToolCall(raw map[string]any) (name string, input any, result map[string]any) {
	call, _ := raw["tool_call"].(map[string]any)
	for key, value := range call {
		if !strings.HasSuffix(key, "ToolCall") {
			continue
		}
		body, _ := value.(map[string]any)
		args, _ := body["args"].(map[string]any)
		result, _ = body["result"].(map[string]any)
		switch key {
		case "mcpToolCall":
			server := stringValue(args["providerIdentifier"])
			if server == "" {
				server = stringValue(args["serverIdentifier"])
			}
			return "mcp__" + server + "__" + stringValue(args["toolName"]), args["args"], result
		case "shellToolCall":
			return "Bash", map[string]any{"command": args["command"]}, result
		}
		name = strings.TrimSuffix(key, "ToolCall")
		if name != "" {
			name = strings.ToUpper(name[:1]) + name[1:]
		}
		delete(args, "toolCallId")
		return name, args, result
	}
	return "", nil, nil
}

func (c *cursorStream) translate(s *ClaudeStream, raw map[string]any) []string {
	switch stringValue(raw["type"]) {
	case "system":
		if stringValue(raw["subtype"]) == "init" {
			return s.Init(stringValue(raw["session_id"]), stringValue(raw["model"]))
		}
	case "thinking":
		if stringValue(raw["subtype"]) == "delta" {
			text := stringValue(raw["text"])
			c.thinking.WriteString(text)
			return s.Delta(fmt.Sprintf("thinking#%d", c.segment), "thinking", text)
		}
		return c.flushThinking(s)
	case "assistant":
		message, _ := raw["message"].(map[string]any)
		text := contentText(message["content"])
		out := c.flushThinking(s)
		if _, partial := raw["timestamp_ms"]; partial {
			c.partial.WriteString(text)
			return append(out, s.Delta(fmt.Sprintf("text#%d", c.segment), "text", text)...)
		}
		c.partial.Reset()
		c.segment++
		return append(out, s.Text(text)...)
	case "tool_call":
		out := append(c.flushThinking(s), c.flushText(s)...)
		id := stringValue(raw["call_id"])
		name, input, result := cursorToolCall(raw)
		out = append(out, s.ToolUse(id, name, input)...)
		if stringValue(raw["subtype"]) == "completed" {
			success, ok := result["success"]
			failure := result["error"]
			if !ok && failure == nil {
				failure = result["rejected"]
			}
			if failure != nil {
				out = append(out, s.ToolResult(id, true, jsonText(failure))...)
			} else {
				body, _ := success.(map[string]any)
				content := body["content"]
				if content == nil {
					content = success
				}
				out = append(out, s.ToolResult(id, false, contentText(content))...)
			}
		}
		return out
	case "result":
		out := append(c.flushThinking(s), c.flushText(s)...)
		usage, _ := raw["usage"].(map[string]any)
		s.SetUsage(int64Value(usage["inputTokens"]), int64Value(usage["outputTokens"]), int64Value(usage["cacheReadTokens"]), int64Value(usage["cacheWriteTokens"]), 0)
		failed, _ := raw["is_error"].(bool)
		text := stringValue(raw["result"])
		if failed && text == "" {
			text = errorText(raw["error"])
		}
		return append(out, s.Result(text, failed)...)
	}
	return nil
}

// antigravityStream: `agy -p --output-format stream-json`.
type antigravityStream struct {
	text    strings.Builder
	failure string
}

func (a *antigravityStream) translate(s *ClaudeStream, raw map[string]any) []string {
	switch stringValue(raw["event"]) {
	case "init":
		init, _ := raw["init"].(map[string]any)
		return s.Init(stringValue(raw["conversation_id"]), stringValue(init["model"]))
	case "step_update":
		step, _ := raw["step_update"].(map[string]any)
		index := int64Value(step["step_index"])
		done := stringValue(step["state"]) == "DONE"
		switch stringValue(step["step_type"]) {
		case "agent_response":
			delta := stringValue(step["text_delta"])
			a.text.WriteString(delta)
			out := s.Delta(fmt.Sprintf("text#%d", index), "text", delta)
			if done {
				if usage, ok := step["usage"].(map[string]any); ok {
					s.AddUsage(int64Value(usage["input_tokens"]), int64Value(usage["output_tokens"]), int64Value(usage["cache_read_tokens"]), 0, int64Value(usage["thinking_tokens"]))
				}
				out = append(out, s.Text(a.text.String())...)
				a.text.Reset()
			}
			return out
		case "tool":
			info, _ := step["tool_info"].(map[string]any)
			id := fmt.Sprintf("%s#%d", stringValue(step["conversation_id"]), index)
			name := stringValue(info["name"])
			input := info["parameters"]
			if params, ok := input.(map[string]any); ok && name == "call_mcp_tool" {
				name, input = "mcp__"+stringValue(params["ServerName"])+"__"+stringValue(params["ToolName"]), params["Arguments"]
			}
			out := s.ToolUse(id, name, input)
			if done {
				failure := errorText(info["error"])
				if failure != "" {
					out = append(out, s.ToolResult(id, true, failure)...)
				} else {
					out = append(out, s.ToolResult(id, false, jsonText(info["output"]))...)
				}
			}
			return out
		case "error_message":
			if text := errorText(step["error"]); text != "" {
				a.failure = text
			}
		}
	case "result":
		result, _ := raw["result"].(map[string]any)
		failed := stringValue(result["status"]) != "SUCCESS"
		text := stringValue(result["response"])
		if failed {
			if text = errorText(result["error"]); text == "" {
				text = a.failure
			}
			if text == "" {
				text = "Antigravity 运行失败"
			}
		}
		if usage, ok := result["usage"].(map[string]any); ok {
			s.SetUsage(int64Value(usage["input_tokens"]), int64Value(usage["output_tokens"]), int64Value(usage["cache_read_tokens"]), 0, int64Value(usage["thinking_tokens"]))
		}
		return s.Result(text, failed)
	}
	return nil
}

// kimiStream: `kimi -p --output-format stream-json` prints whole messages;
// the session ID only arrives with the closing resume hint.
type kimiStream struct{ started bool }

func (k *kimiStream) translate(s *ClaudeStream, raw map[string]any) []string {
	var out []string
	if !k.started {
		k.started = true
		out = s.Init("", "")
	}
	switch stringValue(raw["role"]) {
	case "assistant":
		if text, ok := raw["content"].(string); ok {
			out = append(out, s.Text(text)...)
		}
		for _, item := range sliceValue(raw["content"]) {
			part, _ := item.(map[string]any)
			switch stringValue(part["type"]) {
			case "text":
				out = append(out, s.Text(stringValue(part["text"]))...)
			case "think", "thinking":
				text := stringValue(part["think"])
				if text == "" {
					text = stringValue(part["thinking"])
				}
				out = append(out, s.Thinking(text)...)
			}
		}
		for _, item := range sliceValue(raw["tool_calls"]) {
			call, _ := item.(map[string]any)
			function, _ := call["function"].(map[string]any)
			var input any = map[string]any{}
			if arguments := stringValue(function["arguments"]); arguments != "" {
				_ = json.Unmarshal([]byte(arguments), &input)
			}
			out = append(out, s.ToolUse(stringValue(call["id"]), stringValue(function["name"]), input)...)
		}
	case "tool":
		out = append(out, s.ToolResult(stringValue(raw["tool_call_id"]), false, contentText(raw["content"]))...)
	case "meta":
		if stringValue(raw["type"]) == "session.resume_hint" {
			s.SessionID = stringValue(raw["session_id"])
			out = append(out, s.Init(s.SessionID, "")...)
			out = append(out, s.Result("", false)...)
		}
	}
	return out
}

// piStream: `pi --mode json` streams deltas per content index and reports
// finished messages and tool executions.
type piStream struct {
	turn    int
	failure string
}

func (p *piStream) translate(s *ClaudeStream, raw map[string]any) []string {
	switch stringValue(raw["type"]) {
	case "session":
		return s.Init(stringValue(raw["id"]), "")
	case "turn_start":
		p.turn++
	case "message_update":
		event, _ := raw["assistantMessageEvent"].(map[string]any)
		key := fmt.Sprintf("%d#%d", p.turn, int64Value(event["contentIndex"]))
		switch stringValue(event["type"]) {
		case "text_delta":
			return s.Delta(key, "text", stringValue(event["delta"]))
		case "thinking_delta":
			return s.Delta(key, "thinking", stringValue(event["delta"]))
		}
	case "message_end":
		message, _ := raw["message"].(map[string]any)
		if stringValue(message["role"]) != "assistant" {
			return nil
		}
		usage, _ := message["usage"].(map[string]any)
		s.AddUsage(int64Value(usage["input"]), int64Value(usage["output"]), int64Value(usage["cacheRead"]), int64Value(usage["cacheWrite"]), int64Value(usage["reasoning"]))
		if stringValue(message["stopReason"]) == "error" {
			p.failure = stringValue(message["errorMessage"])
		}
		var out []string
		for _, item := range sliceValue(message["content"]) {
			part, _ := item.(map[string]any)
			switch stringValue(part["type"]) {
			case "text":
				out = append(out, s.Text(stringValue(part["text"]))...)
			case "thinking":
				out = append(out, s.Thinking(stringValue(part["thinking"]))...)
			case "toolCall":
				out = append(out, s.ToolUse(stringValue(part["id"]), stringValue(part["name"]), part["arguments"])...)
			}
		}
		return append(out, s.CloseBlock()...)
	case "tool_execution_start":
		// Top-level calls are reported from the assistant message; nested ones
		// (MCP tools called from codemode) only appear here.
		if stringValue(raw["parentToolCallId"]) == "" {
			return nil
		}
		return s.ToolUse(stringValue(raw["toolCallId"]), stringValue(raw["toolName"]), raw["args"])
	case "tool_execution_end":
		result, _ := raw["result"].(map[string]any)
		failed, _ := raw["isError"].(bool)
		return s.ToolResult(stringValue(raw["toolCallId"]), failed, contentText(result["content"]))
	case "agent_end":
		if p.failure != "" && s.LastText == "" {
			return s.Result(p.failure, true)
		}
		return s.Result("", false)
	}
	return nil
}

// dshStream: `dsh --profile headless --json` run events.
// dshStream: `dsh --json` caps every event string at 8 KiB and flags the event
// `truncated`; only the terminal `final` carries the whole answer. A truncated
// text is held back so `final` can replace it instead of being appended to it.
type dshStream struct{ failure, truncatedText string }

func (d *dshStream) translate(s *ClaudeStream, raw map[string]any) []string {
	kind := stringValue(raw["type"])
	var out []string
	if d.truncatedText != "" && (kind == "thinking" || kind == "text" || kind == "tool_call" || kind == "tool_result") {
		out = s.Text(d.truncatedText)
		d.truncatedText = ""
	}
	switch kind {
	case "session":
		return append(out, s.Init(stringValue(raw["sessionId"]), "")...)
	case "thinking":
		return append(out, s.Thinking(stringValue(raw["text"]))...)
	case "text":
		if raw["truncated"] == true {
			d.truncatedText = stringValue(raw["text"])
			return out
		}
		return append(out, s.Text(stringValue(raw["text"]))...)
	case "tool_call":
		return append(out, s.ToolUse(stringValue(raw["callId"]), stringValue(raw["tool"]), raw["input"])...)
	case "tool_result":
		return append(out, s.ToolResult(stringValue(raw["callId"]), stringValue(raw["status"]) != "completed", jsonText(raw["result"]))...)
	case "status":
		if stringValue(raw["phase"]) == "step_end" {
			usage, _ := raw["usage"].(map[string]any)
			s.AddUsage(int64Value(usage["inputTokens"]), int64Value(usage["outputTokens"]), int64Value(usage["cacheReadTokens"]), int64Value(usage["cacheWriteTokens"]), int64Value(usage["reasoningTokens"]))
		}
	case "error":
		d.failure = errorText(raw)
	case "final":
		if session := stringValue(raw["sessionId"]); session != "" {
			s.SessionID = session
		}
		reason, _ := raw["reason"].(map[string]any)
		failed := stringValue(reason["kind"]) != "completed"
		text := stringValue(raw["text"])
		if held := d.truncatedText; held != "" {
			d.truncatedText = ""
			if strings.HasPrefix(text, held) {
				held = text
			}
			out = append(out, s.Text(held)...)
		}
		if failed && text == "" {
			if text = d.failure; text == "" {
				text = "DeepSeek Harness 运行结束：" + stringValue(reason["kind"])
			}
		}
		return append(out, s.Result(text, failed)...)
	}
	return out
}
