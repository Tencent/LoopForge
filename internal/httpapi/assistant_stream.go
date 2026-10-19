package httpapi

import (
	"encoding/json"
	"strings"
)

type assistantStreamEvent struct {
	Kind   string `json:"kind"`
	Text   string `json:"text,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Detail string `json:"detail,omitempty"`
	ID     string `json:"id,omitempty"`
	Failed bool   `json:"failed,omitempty"`
	// Full 标记该文本是整段回复文本块（assistant 事件），而非增量 delta；
	// 用于过滤 include-partial-messages 模式下的重复文本。
	Full bool `json:"full,omitempty"`
}

type assistantStreamFilter struct {
	// inReply is true while the model streams submit_assistant_reply input,
	// so draftDelta chunks from other tools' arguments are dropped.
	inReply    bool
	lastStatus string
	lastTool   string
	lastDetail string
	seenTools  map[string]struct{}
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
				f.seenTools = map[string]struct{}{}
			}
			if _, ok := f.seenTools[ev.ID]; ok {
				return false
			}
			f.seenTools[ev.ID] = struct{}{}
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

func assistantProtocolTool(name string) bool {
	switch strings.TrimPrefix(strings.TrimSpace(name), "mcp__agentflow_session__") {
	case "submit_assistant_reply", "submit_clarification":
		return true
	default:
		return false
	}
}

func assistantStreamEvents(line string) []assistantStreamEvent {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return nil
	}
	var evt struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		Message   struct {
			Content []struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				ToolUseID string          `json:"tool_use_id"`
				IsError   bool            `json:"is_error"`
				Thinking  string          `json:"thinking"`
				Text      string          `json:"text"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
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
				if text := strings.TrimSpace(block.Text); text != "" && !strings.HasPrefix(text, "{") {
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
				if strings.TrimPrefix(strings.TrimSpace(evt.Event.ContentBlock.Name), "mcp__agentflow_session__") == "submit_assistant_reply" {
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
			if evt.Event.Delta.PartialJSON != "" {
				return []assistantStreamEvent{{Kind: "draftDelta", Detail: evt.Event.Delta.PartialJSON}}
			}
		case "text_delta":
			// 最终回答按 token 增量流式输出（kind=answer）。
			if evt.Event.Delta.Text != "" {
				return []assistantStreamEvent{{Kind: "answer", Text: evt.Event.Delta.Text}}
			}
		}
	case "user":
		// 工具结果（tool_result）标记对应调用结束，前端据此移除"正在调用"行。
		var out []assistantStreamEvent
		for _, block := range evt.Message.Content {
			if block.Type == "tool_result" && strings.TrimSpace(block.ToolUseID) != "" {
				// Forward only completion state, never tool results or draft arguments.
				out = append(out, assistantStreamEvent{Kind: "toolDone", ID: strings.TrimSpace(block.ToolUseID), Failed: block.IsError})
			}
		}
		return out
	case "system":
		return []assistantStreamEvent{{Kind: "status", Text: "已连接模型，开始思考…"}}
	}
	return nil
}

func assistantToolStreamEvent(id, name string, input json.RawMessage) (assistantStreamEvent, bool) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "mcp__agentflow_session__")
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
	return assistantStreamEvent{Kind: "tool", Tool: tool, Detail: detail, Text: text, ID: strings.TrimSpace(id)}, true
}

func assistantToolDisplay(name string, input json.RawMessage) (string, string) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "mcp__agentflow_session__")
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
	case "DeferExecuteTool", "MCP":
		inner := strings.TrimPrefix(str("toolName", "tool", "tool_name", "name", "mcp_tool"), "mcp__agentflow_session__")
		if inner == "" || inner == name {
			// CodeBuddy streams an empty wrapper input first. Wait for the
			// complete snapshot instead of consuming its tool ID with an
			// opaque DeferExecuteTool label that the filter would keep forever.
			return "", ""
		}
		params := input
		if nested, ok := m["params"]; ok {
			if text, ok := nested.(string); ok {
				params = json.RawMessage(text)
			} else if raw, err := json.Marshal(nested); err == nil {
				params = raw
			}
		}
		return inner, clarifyToolDetail(inner, params)
	case "ToolSearch":
		if detail := str("query", "q", "search", "pattern"); detail != "" {
			return name, detail
		}
		for _, key := range []string{"tool_names", "queries"} {
			if values, ok := m[key].([]any); ok {
				var names []string
				for _, value := range values {
					if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
						names = append(names, strings.TrimSpace(text))
					}
				}
				if len(names) > 0 {
					return name, truncateRunes(strings.Join(names, ", "), 160)
				}
			}
		}
	}
	return name, clarifyToolDetail(name, input)
}
