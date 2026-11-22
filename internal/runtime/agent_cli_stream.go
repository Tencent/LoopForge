package runtime

import (
	"encoding/json"
	"fmt"
	"regexp"
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
	// hold is a timestamped assistant chunk identical to the text already
	// streamed. cursor-agent also resends that whole buffer, with timestamp_ms,
	// immediately before a tool call. The next event decides which it was.
	hold   string
	held   bool
	routes cursorRouteFilter
}

func (c *cursorStream) flushText(s *ClaudeStream) []string {
	text := stripCursorAutoRoute(c.partial.String())
	c.resetPartial()
	return s.Text(text)
}

func (c *cursorStream) resetPartial() {
	c.partial.Reset()
	c.routes = cursorRouteFilter{}
	c.segment++
}

// releaseHold commits or drops the identical chunk saved by acceptPartial.
// A tool call means it was the CLI resending the buffer, so it is dropped.
func (c *cursorStream) releaseHold(discard bool) string {
	if !c.held {
		return ""
	}
	text := c.hold
	c.hold = ""
	c.held = false
	if discard {
		return ""
	}
	return c.appendPartial(text)
}

// acceptPartial returns the newly visible text. A chunk identical to the
// buffer is held until the next event; every other chunk is an increment,
// even when it happens to start with the buffered text.
func (c *cursorStream) acceptPartial(text string) string {
	accumulated := c.partial.String()
	if accumulated != "" && text == accumulated {
		c.hold = text
		c.held = true
		return ""
	}
	return c.appendPartial(text)
}

func (c *cursorStream) appendPartial(text string) string {
	c.partial.WriteString(text)
	return c.routes.feed(text)
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
	kind := stringValue(raw["type"])
	var prelude []string
	if text := c.releaseHold(kind == "tool_call"); text != "" {
		prelude = s.Delta(fmt.Sprintf("text#%d", c.segment), "text", text)
	}
	return append(prelude, c.translateEvent(s, raw)...)
}

func (c *cursorStream) translateEvent(s *ClaudeStream, raw map[string]any) []string {
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
			if text = c.acceptPartial(text); text != "" {
				return append(out, s.Delta(fmt.Sprintf("text#%d", c.segment), "text", text)...)
			}
			return out
		}
		c.resetPartial()
		return append(out, s.Text(stripCursorAutoRoute(text))...)
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
		text := stripCursorAutoRoute(stringValue(raw["result"]))
		if failed && text == "" {
			text = errorText(raw["error"])
		}
		return append(out, s.Result(text, failed)...)
	}
	return nil
}

const cursorAutoRoutePrefix = "Auto routed to "

var (
	// The notice ends the line: either the whole line, or glued on with ">"
	// ("回复。> Auto routed to GPT-5.6 Luna"). The model is a few ASCII words
	// with a version digit, so prose that mentions the phrase is kept.
	cursorAutoRouteNoticeRe = regexp.MustCompile(`(?:^|[ \t]*>)[ \t]*Auto routed to ([A-Za-z][A-Za-z0-9.\-]*(?: [A-Za-z0-9][A-Za-z0-9.\-]*){0,4})[ \t]*$`)
	cursorAutoRouteModelRe  = regexp.MustCompile(`^[A-Za-z0-9.\- ]{0,64}$`)
)

// cursorAutoRouteNotice returns the text before a routing notice that ends line.
func cursorAutoRouteNotice(line string) (string, bool) {
	m := cursorAutoRouteNoticeRe.FindStringSubmatchIndex(line)
	if m == nil {
		return "", false
	}
	model := line[m[2]:m[3]]
	if !strings.ContainsAny(model, "0123456789") || strings.HasSuffix(model, ".") {
		return "", false
	}
	return line[:m[0]], true
}

// cursorAutoRouteMayStart reports whether rest can still grow into a notice.
// Once it cannot, appending more text never makes it one again.
func cursorAutoRouteMayStart(rest string, afterMarker bool) bool {
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) <= len(cursorAutoRoutePrefix) {
		return strings.HasPrefix(cursorAutoRoutePrefix, rest) && (afterMarker || rest != "")
	}
	return strings.HasPrefix(rest, cursorAutoRoutePrefix) && cursorAutoRouteModelRe.MatchString(rest[len(cursorAutoRoutePrefix):])
}

// cursorRouteFilter drops Cursor Auto's routing notice from assistant text as
// it streams. Only the first non-empty line after a notice is compared with
// the line before it; a repeat there is the same progress sentence and is
// dropped. Repeated lines elsewhere stay. Text that may still turn into a
// notice or that repeat is held until the line settles, so every chunk only
// costs work on the unfinished line.
type cursorRouteFilter struct {
	line    string   // unfinished line
	shown   int      // bytes of line already emitted
	open    bool     // line is started in the output
	any     bool     // output has at least one line
	last    string   // last non-empty output line, trimmed
	anchor  string   // line before the latest notice
	pending bool     // waiting for the first non-empty line after a notice
	blanks  []string // blank lines held while pending
}

func (f *cursorRouteFilter) feed(text string) string {
	var w strings.Builder
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			break
		}
		f.line += text[:i]
		text = text[i+1:]
		f.endLine(&w)
	}
	f.line += text
	if safe := f.safe(); safe > f.shown {
		f.startLine(&w)
		w.WriteString(f.line[f.shown:safe])
		f.shown = safe
		f.pending = false
	}
	return w.String()
}

func (f *cursorRouteFilter) finish() string {
	var w strings.Builder
	f.endLine(&w)
	return w.String()
}

// safe is how much of the unfinished line can be shown now.
func (f *cursorRouteFilter) safe() int {
	line := f.line
	if f.pending && !f.open {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || f.anchor != "" && strings.HasPrefix(f.anchor, trimmed) {
			return 0
		}
	}
	if !f.open && cursorAutoRouteMayStart(line, false) {
		return 0
	}
	limit := len(strings.TrimRight(line, " \t"))
	for i := f.shown; i < limit; i++ {
		if line[i] == '>' && cursorAutoRouteMayStart(line[i+1:], true) {
			return min(limit, len(strings.TrimRight(line[:i], " \t")))
		}
	}
	return limit
}

func (f *cursorRouteFilter) startLine(w *strings.Builder) {
	if f.open {
		return
	}
	for _, blank := range f.blanks {
		f.newline(w)
		w.WriteString(blank)
	}
	f.blanks = nil
	f.newline(w)
	f.open = true
}

func (f *cursorRouteFilter) newline(w *strings.Builder) {
	if f.any {
		w.WriteByte('\n')
	}
	f.any = true
}

func (f *cursorRouteFilter) endLine(w *strings.Builder) {
	line := f.line
	defer func() { f.line, f.shown, f.open = "", 0, false }()
	if before, ok := cursorAutoRouteNotice(line); ok {
		if trimmed := strings.TrimSpace(before); trimmed != "" {
			f.startLine(w)
			w.WriteString(before[f.shown:])
			f.last = trimmed
		}
		f.anchor, f.pending, f.blanks = f.last, true, nil
		return
	}
	trimmed := strings.TrimSpace(line)
	if f.pending && !f.open {
		if trimmed == "" {
			f.blanks = append(f.blanks, line)
			return
		}
		f.pending = false
		if f.anchor != "" && trimmed == f.anchor {
			f.blanks = nil
			return
		}
	}
	f.pending = false
	f.startLine(w)
	w.WriteString(line[f.shown:])
	if trimmed != "" {
		f.last = trimmed
	}
}

// stripCursorAutoRoute is cursorRouteFilter applied to a finished text.
func stripCursorAutoRoute(text string) string {
	if !strings.Contains(text, cursorAutoRoutePrefix) {
		return text
	}
	var f cursorRouteFilter
	return f.feed(text) + f.finish()
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
		return a.translateStep(s, step)
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

// translateStep dispatches one `step_update` payload by its step_type.
// error_message steps only remember the failure for the final result.
func (a *antigravityStream) translateStep(s *ClaudeStream, step map[string]any) []string {
	index := int64Value(step["step_index"])
	done := stringValue(step["state"]) == "DONE"
	switch stringValue(step["step_type"]) {
	case "agent_response":
		return a.responseStep(s, step, index, done)
	case "tool":
		return antigravityToolStep(s, step, index, done)
	case "error_message":
		if text := errorText(step["error"]); text != "" {
			a.failure = text
		}
	}
	return nil
}

// responseStep streams the text delta and, once the step is DONE, records its
// usage and emits the accumulated text as one block.
func (a *antigravityStream) responseStep(s *ClaudeStream, step map[string]any, index int64, done bool) []string {
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
}

// antigravityToolStep reports the tool call (unwrapping call_mcp_tool into the
// mcp__<server>__<tool> name) and, once the step is DONE, its result.
func antigravityToolStep(s *ClaudeStream, step map[string]any, index int64, done bool) []string {
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
