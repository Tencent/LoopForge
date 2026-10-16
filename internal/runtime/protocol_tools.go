package runtime

import (
	"agentflow/internal/domain"
	"encoding/json"
	"strings"
)

func safeNativeAttributes(raw map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"type", "status", "subtype", "duration_ms", "num_turns", "exit_code", "finish_reason"} {
		if value, ok := raw[key]; ok {
			out[key] = value
		}
	}
	return out
}

func tokenUsage(value any) domain.TokenUsage {
	v, _ := value.(map[string]any)
	first := func(keys ...string) int64 {
		for _, key := range keys {
			if n := int64Value(v[key]); n != 0 {
				return n
			}
		}
		return 0
	}
	usage := domain.TokenUsage{InputTokens: first("input_tokens", "prompt_tokens", "inputTokens"), OutputTokens: first("output_tokens", "completion_tokens", "outputTokens"), CacheReadTokens: first("cache_read_input_tokens", "cached_input_tokens", "cache_read_tokens"), CacheWriteTokens: first("cache_creation_input_tokens", "cache_write_tokens"), ReasoningTokens: first("reasoning_tokens"), ContextWindow: first("context_window", "contextWindow")}
	// 适配器显式上报成本（即使为 0）标记 reported，用于区分"真实零成本"与"成本未识别"。
	if _, ok := v["total_cost_usd"]; ok {
		usage.CostUSD = float64Value(v["total_cost_usd"])
		usage.CostStatus = "reported"
	}
	usage.TotalTokens = first("total_tokens", "totalTokens")
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	return usage
}

func (a *protocolAdapter) beginToolUse(id, name, sessionID, parentID string) {
	if id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tools == nil {
		a.tools = map[string]*toolUseBuf{}
	}
	buf := a.tools[id]
	if buf == nil {
		buf = &toolUseBuf{id: id, name: name, sessionID: sessionID, parentID: parentID}
		a.tools[id] = buf
	}
	if name != "" {
		buf.name = name
	}
	if sessionID != "" {
		buf.sessionID = sessionID
	}
	if parentID != "" {
		buf.parentID = parentID
	}
	a.currentToolID = id
}

func (a *protocolAdapter) appendToolJSON(partial string) {
	if partial == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	buf := a.tools[a.currentToolID]
	if buf == nil {
		return
	}
	buf.json.WriteString(partial)
}

func (a *protocolAdapter) flushCurrentTool() {
	a.mu.Lock()
	id := a.currentToolID
	buf := a.tools[id]
	raw := ""
	name, sessionID, parentID := "", "", ""
	if buf != nil {
		raw = buf.json.String()
		name, sessionID, parentID = buf.name, buf.sessionID, buf.parentID
	}
	a.mu.Unlock()
	if id == "" || buf == nil {
		return
	}
	var input any
	if strings.TrimSpace(raw) != "" {
		if json.Unmarshal([]byte(raw), &input) != nil {
			input = raw
		}
	}
	a.emitToolStart(id, name, sessionID, parentID, input)
}

func (a *protocolAdapter) emitToolStart(id, name, sessionID, parentID string, input any) {
	if id == "" && name == "" {
		return
	}
	a.mu.Lock()
	if a.tools == nil {
		a.tools = map[string]*toolUseBuf{}
	}
	buf := a.tools[id]
	if buf == nil && id != "" {
		buf = &toolUseBuf{id: id, name: name, sessionID: sessionID, parentID: parentID}
		a.tools[id] = buf
	}
	if buf != nil && buf.emitted {
		a.mu.Unlock()
		return
	}
	if buf != nil {
		buf.emitted = true
		if name == "" {
			name = buf.name
		}
		if sessionID == "" {
			sessionID = buf.sessionID
		}
		if parentID == "" {
			parentID = buf.parentID
		}
	}
	a.mu.Unlock()
	msg := summarizeToolCall(name, input)
	a.emit(domain.TelemetryEvent{Category: "tool", Name: "tool.call", Phase: "start", Message: msg, SessionID: sessionID, ParentID: parentID, ToolCallID: id, Attributes: slimToolInput(input), Tool: &domain.ToolObservation{Name: name, Operation: "tool.use", CallID: id}})
}

func (a *protocolAdapter) toolName(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if buf := a.tools[id]; buf != nil {
		return buf.name
	}
	return ""
}

func toolInputReady(input any) bool {
	switch v := input.(type) {
	case map[string]any:
		return len(v) > 0
	case string:
		return strings.TrimSpace(v) != ""
	default:
		return v != nil
	}
}

func slimToolInput(input any) map[string]any {
	fields := map[string]string{}
	if v, ok := input.(map[string]any); ok {
		for _, k := range []string{"command", "cmd", "path", "file_path", "filePath", "pattern", "glob", "query", "url", "target", "skill", "skill_name", "name"} {
			if s := strings.TrimSpace(stringValue(v[k])); s != "" {
				fields[k] = clipToolMsg(s)
			}
		}
	}
	if len(fields) == 0 {
		if s := clipToolMsg(toolResultText(input)); s != "" {
			return map[string]any{"input": s}
		}
		return nil
	}
	return map[string]any{"input": fields}
}

func toolResultText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s := toolResultText(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		return firstNonEmpty(stringValue(v["text"]), stringValue(v["content"]), stringValue(v["stdout"]), stringValue(v["output"]))
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
}

func firstLogLine(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n")
	if s == "" {
		return ""
	}
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

func looksLikeSource(s string) bool {
	if strings.Count(s, "\n") > 2 {
		return true
	}
	return strings.Contains(s, "t.Fatalf") || strings.Contains(s, "func ") || strings.Contains(s, "package ") || strings.Contains(s, " := ") || strings.Contains(s, "import (")
}

func summarizeToolResult(name string, failed bool, content any) string {
	label := strings.TrimSpace(name)
	if label == "" {
		label = "工具"
	}
	text := strings.TrimSpace(toolResultText(content))
	line := firstLogLine(text)
	if failed {
		if line == "" {
			return label + " 失败"
		}
		return clipToolMsg(label + " 失败: " + line)
	}
	lower := strings.ToLower(label)
	dump := strings.Contains(lower, "read") || strings.Contains(lower, "write") || strings.Contains(lower, "edit") || strings.Contains(lower, "grep") || strings.Contains(lower, "glob") || strings.Contains(lower, "notebook")
	if dump || looksLikeSource(text) || line == "" {
		return label + " 完成"
	}
	if strings.Contains(lower, "bash") || strings.Contains(lower, "shell") || !strings.Contains(text, "\n") {
		return clipToolMsg(label + " 完成: " + line)
	}
	return label + " 完成"
}

func clipToolMsg(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	const n = 180
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func summarizeToolCall(name string, input any) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	fields := map[string]string{}
	switch v := input.(type) {
	case map[string]any:
		for _, k := range []string{"command", "cmd", "path", "file_path", "filePath", "pattern", "glob", "query", "url", "target", "skill", "skill_name", "name"} {
			if s := strings.TrimSpace(stringValue(v[k])); s != "" {
				fields[k] = s
			}
		}
	case string:
		if strings.TrimSpace(v) != "" {
			return clipToolMsg(name + " " + strings.Join(strings.Fields(v), " "))
		}
	}
	lower := strings.ToLower(name)
	detail := ""
	switch {
	case strings.Contains(lower, "bash") || strings.Contains(lower, "shell"):
		detail = firstNonEmpty(fields["command"], fields["cmd"])
	case strings.Contains(lower, "write") || strings.Contains(lower, "edit") || strings.Contains(lower, "read"):
		detail = firstNonEmpty(fields["path"], fields["file_path"], fields["filePath"])
	case strings.Contains(lower, "grep") || strings.Contains(lower, "glob"):
		detail = firstNonEmpty(fields["pattern"], fields["glob"], fields["path"])
	case strings.Contains(lower, "skill"):
		detail = firstNonEmpty(fields["skill"], fields["skill_name"], fields["name"])
	default:
		detail = firstNonEmpty(fields["command"], fields["cmd"], fields["path"], fields["file_path"], fields["filePath"], fields["query"], fields["url"], fields["pattern"], fields["glob"], fields["skill"], fields["name"])
	}
	if detail == "" {
		return name
	}
	return clipToolMsg(name + " " + strings.Join(strings.Fields(detail), " "))
}
