package httpapi

import (
	"agentflow/internal/domain"
	"encoding/json"
	"regexp"
	"strings"
)

type assistantStreamEvent = domain.AssistantActivityEvent

type assistantStreamFilter struct {
	// inReply is true while the model streams submit_assistant_reply input,
	// so draftDelta chunks from other tools' arguments are dropped.
	inReply    bool
	lastStatus string
	lastTool   string
	lastDetail string
	seenTools  map[string]string
	answer     string
	thinking   string
}

func (f *assistantStreamFilter) accept(ev assistantStreamEvent) bool {
	switch ev.Kind {
	case "draftStart":
		f.inReply = true
		return true
	case "draftDelta":
		return f.inReply && ev.Detail != ""
	case "status":
		if ev.Text == "" || ev.Text == f.lastStatus {
			return false
		}
		f.lastStatus = ev.Text
		return true
	case "answer":
		if ev.Text == "" {
			return false
		}
		if ev.Full {
			// 增量已经流过同样的文本时丢弃整段重复块。
			if f.answer != "" && strings.Contains(f.answer, strings.TrimSpace(ev.Text)) {
				return false
			}
			f.answer += ev.Text
			return true
		}
		f.answer += ev.Text
		return true
	case "tool":
		f.inReply = false
		if assistantProtocolTool(ev.Tool) {
			return false
		}
		if ev.ID != "" {
			if f.seenTools == nil {
				f.seenTools = map[string]string{}
			}
			// The CLI announces a call with empty input and repeats it once
			// the input is complete. Keep the repeat only when it adds
			// something: the detail, or an Agent call's type and task.
			seen := ev.Detail + "\x00" + ev.Subagent + "\x00" + ev.Input + "\x00" + ev.AgentID
			if previous, ok := f.seenTools[ev.ID]; ok {
				if seen == previous || (ev.Detail == "" && ev.Subagent == "" && ev.Input == "") {
					return false
				}
			}
			f.seenTools[ev.ID] = seen
			f.lastTool, f.lastDetail = ev.Tool, ev.Detail
			return true
		}
		if ev.Tool == "" {
			return false
		}
		if ev.Tool == f.lastTool && ev.Detail == f.lastDetail {
			return false
		}
		f.lastTool, f.lastDetail = ev.Tool, ev.Detail
		return true
	case "thinkingStart":
		f.inReply = false
		return true
	case "toolDone":
		return ev.ID != ""
	case "thinking":
		if ev.Text == "" {
			return false
		}
		if ev.Full {
			if f.thinking != "" && strings.Contains(f.thinking, strings.TrimSpace(ev.Text)) {
				return false
			}
			f.thinking += ev.Text
			return true
		}
		f.thinking += ev.Text
		return true
	default:
		return ev.Text != "" || ev.Tool != ""
	}
}

// assistantSessionToolPrefix namespaces the platform's own session MCP tools.
const assistantSessionToolPrefix = "mcp__agentflow_session__"

// assistantSessionToolName returns name without surrounding space and the
// session MCP prefix, so session tools compare by their bare tool name.
func assistantSessionToolName(name string) string {
	return strings.TrimPrefix(strings.TrimSpace(name), assistantSessionToolPrefix)
}

func assistantProtocolTool(name string) bool {
	switch assistantSessionToolName(name) {
	case "submit_assistant_reply", "submit_clarification":
		return true
	default:
		return false
	}
}

// assistantStreamEvents parses one stream-json line. Events a sub-agent
// produced carry Parent, the ID of the Agent tool call that dispatched it, so
// the UI can show each subagent's own process apart from Main Bot's.
func assistantStreamEvents(line string) []assistantStreamEvent {
	events := assistantLineEvents(line)
	var origin struct {
		ParentToolUseID string `json:"parent_tool_use_id"`
	}
	if len(events) > 0 && json.Unmarshal([]byte(strings.TrimSpace(line)), &origin) == nil {
		if parent := strings.TrimSpace(origin.ParentToolUseID); parent != "" {
			for i := range events {
				events[i].Parent = parent
			}
		}
	}
	return events
}

func assistantLineEvents(line string) []assistantStreamEvent {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return nil
	}
	var evt struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		// Set on lines a sub-agent (Agent/Task tool) produced.
		ParentToolUseID string `json:"parent_tool_use_id"`
		Message         struct {
			Content []struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				ToolUseID string          `json:"tool_use_id"`
				IsError   bool            `json:"is_error"`
				Thinking  string          `json:"thinking"`
				Text      string          `json:"text"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
				Content   json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
		Event struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Thinking    string `json:"thinking"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		} `json:"event"`
	}
	if json.Unmarshal([]byte(line), &evt) != nil {
		return nil
	}
	// A sub-agent's prose is its report to the main model, not the reply: it
	// is kept as agentText under its dispatch.
	subAgent := strings.TrimSpace(evt.ParentToolUseID) != ""
	switch evt.Type {
	case "assistant":
		var out []assistantStreamEvent
		for _, block := range evt.Message.Content {
			switch block.Type {
			case "thinking":
				if text := strings.TrimSpace(block.Thinking); text != "" {
					out = append(out, assistantStreamEvent{Kind: "thinking", Text: text, Full: true})
				}
			case "text":
				text := strings.TrimSpace(block.Text)
				if text == "" || strings.HasPrefix(text, "{") || assistantCLINoticeText(text) {
					continue
				}
				if subAgent {
					out = append(out, assistantStreamEvent{Kind: "agentText", Text: text, Full: true})
				} else {
					out = append(out, assistantStreamEvent{Kind: "answer", Text: text, Full: true})
				}
			case "tool_use":
				if ev, ok := assistantToolStreamEvent(block.ID, block.Name, block.Input); ok {
					out = append(out, ev)
				}
			}
		}
		return out
	case "stream_event":
		if evt.Event.Type == "content_block_start" {
			switch evt.Event.ContentBlock.Type {
			case "tool_use":
				if assistantSessionToolName(evt.Event.ContentBlock.Name) == "submit_assistant_reply" {
					if subAgent {
						return nil
					}
					// The reply arguments carry the drafts; stream them so the
					// canvas can build a workflow while the model is still writing.
					return []assistantStreamEvent{{Kind: "draftStart", ID: strings.TrimSpace(evt.Event.ContentBlock.ID)}}
				}
				if ev, ok := assistantToolStreamEvent(evt.Event.ContentBlock.ID, evt.Event.ContentBlock.Name, evt.Event.ContentBlock.Input); ok {
					return []assistantStreamEvent{ev}
				}
			case "thinking":
				// 每个思考块开一个新分段，避免多段思考拼进同一个框。
				return []assistantStreamEvent{{Kind: "thinkingStart"}}
			}
		}
		switch evt.Event.Delta.Type {
		case "thinking_delta":
			if evt.Event.Delta.Thinking != "" {
				return []assistantStreamEvent{{Kind: "thinking", Text: evt.Event.Delta.Thinking}}
			}
		case "input_json_delta":
			if evt.Event.Delta.PartialJSON != "" && !subAgent {
				return []assistantStreamEvent{{Kind: "draftDelta", Detail: evt.Event.Delta.PartialJSON}}
			}
		case "text_delta":
			// 最终回答按 token 增量流式输出（kind=answer）。
			if evt.Event.Delta.Text != "" && !subAgent {
				return []assistantStreamEvent{{Kind: "answer", Text: evt.Event.Delta.Text}}
			}
		}
	case "user":
		// 工具结果（tool_result）标记对应调用结束，前端据此移除"正在调用"行。
		var out []assistantStreamEvent
		for _, block := range evt.Message.Content {
			if block.Type == "tool_result" && strings.TrimSpace(block.ToolUseID) != "" {
				// Forward only completion state, never tool results or draft
				// arguments. An Agent result names the agent ID it can resume.
				done := assistantStreamEvent{Kind: "toolDone", ID: strings.TrimSpace(block.ToolUseID), Failed: block.IsError}
				// An Agent result is the subagent's report to Main Bot; it is
				// kept for the side panel, other tool results are not.
				if report := assistantResultText(block.Content); assistantAgentIDRe.MatchString(report) {
					done.AgentID = assistantAgentIDRe.FindStringSubmatch(report)[1]
					done.Output = truncateRunes(assistantAgentReport(report), 8000)
				}
				out = append(out, done)
			}
		}
		return out
	case "system":
		return []assistantStreamEvent{{Kind: "status", Text: "已连接模型，开始思考…"}}
	}
	return nil
}

// assistantCLINoticeRe matches the CLI's own error-recovery text, which it
// records as an assistant message when the model calls a tool that is not in
// the tool set (for example Bash, which assistant sessions deny).
var assistantCLINoticeRe = regexp.MustCompile(`^Tool "[^"]+" does not exist in the current tool set\.?$`)

func assistantCLINoticeText(text string) bool {
	return assistantCLINoticeRe.MatchString(strings.TrimSpace(text))
}

func assistantToolStreamEvent(id, name string, input json.RawMessage) (assistantStreamEvent, bool) {
	name = assistantSessionToolName(name)
	if name == "" || assistantProtocolTool(name) {
		return assistantStreamEvent{}, false
	}
	tool, detail := assistantToolDisplay(name, input)
	if tool == "" || assistantProtocolTool(tool) {
		return assistantStreamEvent{}, false
	}
	text := "正在调用 " + tool
	if detail != "" {
		text += " · " + detail
	}
	event := assistantStreamEvent{Kind: "tool", Tool: tool, Detail: detail, Text: text, ID: strings.TrimSpace(id)}
	if name == "Agent" || name == "Task" {
		var dispatch struct {
			SubagentType string `json:"subagent_type"`
			Prompt       string `json:"prompt"`
			Resume       string `json:"resume"`
		}
		_ = json.Unmarshal(input, &dispatch)
		event.Subagent = strings.TrimSpace(dispatch.SubagentType)
		event.Input = truncateRunes(strings.TrimSpace(dispatch.Prompt), 8000)
		// A resumed dispatch names its agent up front, so the panel can put
		// it on that agent's row while it runs.
		if m := assistantAgentIDRe.FindStringSubmatch("Agent ID: " + strings.TrimSpace(dispatch.Resume)); m != nil {
			event.AgentID = m[1]
		}
	}
	return event, true
}

var assistantAgentIDRe = regexp.MustCompile(`Agent ID:\s*(agent-[A-Za-z0-9-]+)`)

// assistantResultText reads a tool result, which is either a string or a list
// of text blocks.
func assistantResultText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var plain string
	if json.Unmarshal(content, &plain) == nil {
		return plain
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	text := ""
	if json.Unmarshal(content, &blocks) == nil {
		for _, block := range blocks {
			text += block.Text + "\n"
		}
	}
	return text
}

var assistantAgentTrailerRe = regexp.MustCompile(`(?m)^\s*\[?\s*(Agent ID|agentId)\s*:.*$`)

// assistantAgentReport drops the "Agent ID: …" bookkeeping the CLI appends to
// a subagent's report.
func assistantAgentReport(text string) string {
	return strings.TrimSpace(assistantAgentTrailerRe.ReplaceAllString(text, ""))
}

// assistantWrappedToolParams returns the inner tool input carried by a
// DeferExecuteTool/MCP wrapper: a string "params" is used verbatim, any other
// value is re-encoded, and a missing "params" falls back to the wrapper input.
func assistantWrappedToolParams(input json.RawMessage, m map[string]any) json.RawMessage {
	nested, ok := m["params"]
	if !ok {
		return input
	}
	if text, ok := nested.(string); ok {
		return json.RawMessage(text)
	}
	if raw, err := json.Marshal(nested); err == nil {
		return raw
	}
	return input
}

// assistantToolSearchNames joins the non-blank entries of the first
// non-empty "tool_names"/"queries" list of a ToolSearch input.
func assistantToolSearchNames(m map[string]any) string {
	for _, key := range []string{"tool_names", "queries"} {
		values, ok := m[key].([]any)
		if !ok {
			continue
		}
		var names []string
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				names = append(names, strings.TrimSpace(text))
			}
		}
		if len(names) > 0 {
			return strings.Join(names, ", ")
		}
	}
	return ""
}

func assistantToolDisplay(name string, input json.RawMessage) (string, string) {
	name = assistantSessionToolName(name)
	var m map[string]any
	if len(input) > 0 {
		_ = json.Unmarshal(input, &m)
	}
	str := func(keys ...string) string {
		for _, key := range keys {
			if v, ok := m[key].(string); ok {
				if s := strings.TrimSpace(v); s != "" {
					return s
				}
			}
		}
		return ""
	}
	switch name {
	case "Agent", "Task":
		return name, truncateRunes(str("description"), 160)
	case "workbench_publish_artifact":
		if title := str("title"); title != "" {
			return "发布产物", title
		}
		return "发布产物", ""
	case "DeferExecuteTool", "MCP":
		inner := assistantSessionToolName(str("toolName", "tool", "tool_name", "name", "mcp_tool"))
		if inner == "" || inner == name {
			// CodeBuddy streams an empty wrapper input first. Wait for the
			// complete snapshot instead of consuming its tool ID with an
			// opaque DeferExecuteTool label that the filter would keep forever.
			return "", ""
		}
		return assistantToolDisplay(inner, assistantWrappedToolParams(input, m))
	case "ToolSearch":
		if detail := str("query", "q", "search", "pattern"); detail != "" {
			return name, detail
		}
		if names := assistantToolSearchNames(m); names != "" {
			return name, truncateRunes(names, 160)
		}
	}
	if strings.Contains(strings.ToLower(name), "command") || strings.EqualFold(name, "bash") || strings.EqualFold(name, "shell") || strings.EqualFold(name, "exec") {
		if command := str("command", "cmd", "script"); command != "" {
			return name, truncateRunes(command, 4000)
		}
		if body, ok := m["body"]; ok {
			if raw, err := json.Marshal(body); err == nil {
				return assistantToolDisplay(name, raw)
			}
		}
	}
	if detail := clarifyToolDetail(name, input); detail != "" {
		return name, detail
	}
	if path, ok := m["path"].(map[string]any); ok {
		if id, ok := path["id"].(string); ok {
			return name, truncateRunes(id, 300)
		}
	}
	return name, ""
}
