package runtime

import (
	"encoding/json"
	"strings"
	"sync"

	"agentflow/internal/domain"
)

type toolUseBuf struct {
	id, name, sessionID, parentID string
	json                          strings.Builder
	emitted                       bool
}

type protocolParser uint8

const (
	protocolGeneric protocolParser = iota
	protocolAnthropic
	protocolCodex
)

type protocolAdapter struct {
	ex            domain.ExecutorDefinition
	sink          EventSink
	parser        protocolParser
	adapterID     string
	mu            sync.Mutex
	result        string
	lastOutput    string
	failed        bool
	reasoningBuf  strings.Builder
	done          chan struct{}
	once          sync.Once
	tools         map[string]*toolUseBuf
	currentToolID string
}

// ProtocolObserver normalizes process output from a remotely executed Agent
// CLI into the same provider-neutral telemetry emitted by ExecutorRunner.Run.
// It does not own the process. VisibleOutput exposes the terminal result text,
// or the latest assistant text when the CLI never emits a result frame, so a
// runner can store that instead of the raw stream-json transcript.
type ProtocolObserver struct {
	adapter *protocolAdapter
}

func NewProtocolObserver(ex domain.ExecutorDefinition, sink EventSink) *ProtocolObserver {
	return &ProtocolObserver{adapter: newProtocolAdapter(ex, sink)}
}

func (o *ProtocolObserver) Handle(event domain.TelemetryEvent) {
	if o == nil || o.adapter == nil {
		return
	}
	o.adapter.Handle(event)
}

// Finalize mirrors ExecutorRunner.Run's terminal lifecycle event for remote
// processes whose protocol does not carry a dedicated terminal result frame
// (notably Codex JSONL). Providers that already emitted agent.completed are
// left untouched.
func (o *ProtocolObserver) VisibleOutput() string {
	if o == nil || o.adapter == nil {
		return ""
	}
	return o.adapter.VisibleOutput()
}

func (o *ProtocolObserver) Finalize(success bool) {
	if o == nil || o.adapter == nil || o.adapter.Finished() {
		return
	}
	level := "error"
	message := "executor exited without terminal result"
	if success {
		level = "warn"
		message = "executor exited without terminal result; deferring to gate verification"
	}
	o.adapter.emit(domain.TelemetryEvent{Category: "lifecycle", Name: "agent.completed", Phase: "finish", Level: level, Message: message})
}

func newProtocolAdapter(ex domain.ExecutorDefinition, sink EventSink) *protocolAdapter {
	return providerForKind(ex.Kind).Protocol(ex, sink)
}

func makeProtocolAdapter(ex domain.ExecutorDefinition, sink EventSink, parser protocolParser, adapterID string) *protocolAdapter {
	return &protocolAdapter{ex: ex, sink: sink, parser: parser, adapterID: adapterID, done: make(chan struct{})}
}

func (a *protocolAdapter) Completed() <-chan struct{} { return a.done }

func (a *protocolAdapter) Finished() bool {
	select {
	case <-a.done:
		return true
	default:
		return false
	}
}

func (a *protocolAdapter) Failed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.failed
}

func ObservabilitySpec() domain.ObservabilitySpec {
	return domain.ObservabilitySpec{
		Version:           domain.ObservabilitySpecVersion,
		Categories:        []string{"lifecycle", "model", "reasoning", "output", "tool", "usage", "process", "verification", "artifact", "delivery", "sandbox", "error"},
		Phases:            []string{"start", "progress", "finish"},
		RequiredContext:   []string{"runId", "traceId"},
		MetricConventions: map[string]string{"tokens": "count", "cost": "USD", "duration": "ms", "calls": "count"},
		Adapters: []domain.AdapterDescriptor{
			{ID: "anthropic-stream-v1", SpecVersion: domain.ObservabilitySpecVersion, ExecutorKinds: []string{"claude-code", "codebuddy"}, NativeProtocols: []string{"stream-json"}, Capabilities: []string{"session", "model", "reasoning", "output-delta", "tool-lifecycle", "token-usage", "cache-usage", "cost", "duration"}, RawEventPolicy: "discard-after-normalization"},
			{ID: "codex-jsonl-v1", SpecVersion: domain.ObservabilitySpecVersion, ExecutorKinds: []string{"codex"}, NativeProtocols: []string{"jsonl"}, Capabilities: []string{"session", "reasoning", "output", "tool-lifecycle", "token-usage", "cache-usage"}, RawEventPolicy: "discard-after-normalization"},
			{ID: "generic-json-v1", SpecVersion: domain.ObservabilitySpecVersion, ExecutorKinds: []string{"custom"}, NativeProtocols: []string{"jsonl", "stream-json"}, Capabilities: []string{"lifecycle", "message", "extensible-metrics"}, RawEventPolicy: "sanitized-attributes-only"},
		},
	}
}

func (a *protocolAdapter) Result() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.result
}

// VisibleOutput is the text that belongs in a node inspector. A terminal
// result frame wins. Otherwise the latest assistant message is used, so a
// missing result frame does not surface the raw stream-json envelope.
func (a *protocolAdapter) VisibleOutput() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(a.result) != "" {
		return a.result
	}
	return a.lastOutput
}

func (a *protocolAdapter) noteOutput(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	a.mu.Lock()
	a.lastOutput = text
	a.mu.Unlock()
}

func (a *protocolAdapter) emit(event domain.TelemetryEvent) {
	if event.Provider == "" {
		event.Provider = a.ex.Kind
	}
	if event.Model == "" {
		event.Model = a.ex.Model
	}
	event.SpecVersion = domain.ObservabilitySpecVersion
	event.Source = domain.TelemetrySource{Layer: "executor", Adapter: a.adapterID, Provider: event.Provider, Protocol: a.ex.Protocol}
	event.Context.SessionID = event.SessionID
	if event.Usage != nil {
		event.Metrics = usageMetrics(*event.Usage)
	}
	if event.Category == "tool" && event.Tool == nil {
		event.Tool = &domain.ToolObservation{Name: event.Message, Operation: event.Name, CallID: event.ToolCallID}
	}
	if event.Category == "model" {
		event.ModelOperation = &domain.ModelObservation{Provider: event.Provider, Model: event.Model, Operation: event.Name}
	}
	if a.sink != nil {
		a.sink(event)
	}
}

func (a *protocolAdapter) Handle(event domain.TelemetryEvent) {
	if event.Name != "process.stdout" || (a.ex.Protocol != "stream-json" && a.ex.Protocol != "jsonl") {
		a.emit(event)
		return
	}
	var raw map[string]any
	if json.Unmarshal([]byte(event.Message), &raw) != nil {
		a.emit(event)
		return
	}
	switch a.parser {
	case protocolAnthropic:
		a.handleClaudeStream(raw)
	case protocolCodex:
		a.handleCodexJSON(raw)
	default:
		a.handleGenericJSON(raw)
	}
}

func usageMetrics(usage domain.TokenUsage) []domain.MetricPoint {
	values := []struct {
		name  string
		value float64
		unit  string
	}{{"tokens.input", float64(usage.InputTokens), "count"}, {"tokens.output", float64(usage.OutputTokens), "count"}, {"tokens.cache_read", float64(usage.CacheReadTokens), "count"}, {"tokens.cache_write", float64(usage.CacheWriteTokens), "count"}, {"tokens.reasoning", float64(usage.ReasoningTokens), "count"}, {"cost", usage.CostUSD, "USD"}}
	out := []domain.MetricPoint{}
	for _, v := range values {
		if v.value != 0 {
			out = append(out, domain.MetricPoint{Name: v.name, Value: v.value, Unit: v.unit, Kind: "counter"})
		}
	}
	return out
}

func (a *protocolAdapter) handleClaudeStream(raw map[string]any) {
	typ := stringValue(raw["type"])
	sessionID := stringValue(raw["session_id"])
	switch typ {
	case "system":
		if stringValue(raw["subtype"]) != "init" {
			return
		}
		a.emit(domain.TelemetryEvent{Category: "lifecycle", Name: "agent.initialized", Phase: "start", Message: "agent session initialized", Model: stringValue(raw["model"]), SessionID: sessionID, Attributes: map[string]any{"permissionMode": raw["permissionMode"]}})
	case "stream_event":
		inner, _ := raw["event"].(map[string]any)
		a.handleAnthropicEvent(inner, sessionID, stringValue(raw["parent_tool_use_id"]))
	case "assistant":
		message, _ := raw["message"].(map[string]any)
		var parts []string
		for _, block := range sliceValue(message["content"]) {
			content, _ := block.(map[string]any)
			switch stringValue(content["type"]) {
			case "text":
				if text := stringValue(content["text"]); text != "" {
					parts = append(parts, text)
					a.emit(domain.TelemetryEvent{Category: "output", Name: "agent.output", Phase: "progress", Message: text, Model: stringValue(message["model"]), SessionID: sessionID})
				}
			case "tool_use":
				a.emitToolStart(stringValue(content["id"]), stringValue(content["name"]), sessionID, "", content["input"])
			}
		}
		if len(parts) > 0 {
			a.noteOutput(strings.Join(parts, "\n"))
		}
	case "user":
		message, _ := raw["message"].(map[string]any)
		for _, value := range sliceValue(message["content"]) {
			content, _ := value.(map[string]any)
			if stringValue(content["type"]) != "tool_result" {
				continue
			}
			failed, _ := content["is_error"].(bool)
			success := !failed
			id := stringValue(content["tool_use_id"])
			name := a.toolName(id)
			msg := summarizeToolResult(name, failed, content["content"])
			a.emit(domain.TelemetryEvent{Category: "tool", Name: "tool.call", Phase: "finish", Level: map[bool]string{true: "error", false: "info"}[failed], Message: msg, SessionID: sessionID, ToolCallID: id, Tool: &domain.ToolObservation{Name: name, Operation: "tool.result", CallID: id, Success: &success}})
		}
	case "result":
		result := stringValue(raw["result"])
		failed, _ := raw["is_error"].(bool)
		a.mu.Lock()
		a.result = result
		a.failed = failed
		a.mu.Unlock()
		a.once.Do(func() { close(a.done) })
		usage := tokenUsage(raw["usage"])
		usage.CostUSD = float64Value(raw["total_cost_usd"])
		// 适配器显式上报成本时标记 reported，用于区分"真实零成本"与"成本未识别"。
		if _, ok := raw["total_cost_usd"]; ok {
			usage.CostStatus = "reported"
		}
		a.emit(domain.TelemetryEvent{Category: "usage", Name: "usage.final", Phase: "finish", Message: "final model usage", SessionID: sessionID, Usage: &usage, Attributes: map[string]any{"durationMs": raw["duration_ms"], "turns": raw["num_turns"], "isError": raw["is_error"]}})
		a.emit(domain.TelemetryEvent{Category: "lifecycle", Name: "agent.completed", Phase: "finish", Message: result, SessionID: sessionID})
	default:
		// File snapshots and provider-specific housekeeping events are intentionally hidden from the portable view.
	}
}

func (a *protocolAdapter) handleAnthropicEvent(event map[string]any, sessionID, parentID string) {
	typ := stringValue(event["type"])
	switch typ {
	case "message_start":
		message, _ := event["message"].(map[string]any)
		a.emit(domain.TelemetryEvent{Category: "model", Name: "model.request", Phase: "start", Message: "model generation started", Model: stringValue(message["model"]), SessionID: sessionID, ParentID: parentID})
	case "content_block_start":
		block, _ := event["content_block"].(map[string]any)
		if stringValue(block["type"]) == "tool_use" {
			id := stringValue(block["id"])
			name := stringValue(block["name"])
			a.beginToolUse(id, name, sessionID, parentID)
			if toolInputReady(block["input"]) {
				a.emitToolStart(id, name, sessionID, parentID, block["input"])
			}
		} else {
			a.mu.Lock()
			a.currentToolID = ""
			a.mu.Unlock()
		}
	case "content_block_delta":
		delta, _ := event["delta"].(map[string]any)
		switch stringValue(delta["type"]) {
		case "thinking_delta":
			a.reasoningBuf.WriteString(stringValue(delta["thinking"]))
		case "input_json_delta":
			a.appendToolJSON(stringValue(delta["partial_json"]))
			// Accumulate in memory only; emit once on content_block_stop.
		}
	case "content_block_stop":
		a.flushCurrentTool()
	case "message_stop":
		if reasoning := a.reasoningBuf.String(); reasoning != "" {
			a.emit(domain.TelemetryEvent{Category: "reasoning", Name: "agent.reasoning", Phase: "progress", Message: reasoning, SessionID: sessionID, ParentID: parentID})
			a.reasoningBuf.Reset()
		}
		a.emit(domain.TelemetryEvent{Category: "model", Name: "model.request", Phase: "finish", Message: "model generation finished", SessionID: sessionID, ParentID: parentID})
	case "message_delta":
		if usage := tokenUsage(event["usage"]); usage.InputTokens+usage.OutputTokens+usage.CacheReadTokens+usage.CacheWriteTokens > 0 {
			a.emit(domain.TelemetryEvent{Category: "usage", Name: "usage.update", Phase: "progress", SessionID: sessionID, ParentID: parentID, Usage: &usage})
		}
	}
}

func (a *protocolAdapter) handleCodexJSON(raw map[string]any) {
	typ := stringValue(raw["type"])
	session := stringValue(raw["thread_id"])
	if session == "" {
		session = stringValue(raw["threadId"])
	}
	switch typ {
	case "thread.started":
		a.emit(domain.TelemetryEvent{Category: "lifecycle", Name: "agent.initialized", Phase: "start", Message: "codex thread initialized", SessionID: session})
	case "turn.started":
		a.emit(domain.TelemetryEvent{Category: "model", Name: "model.request", Phase: "start", Message: "model generation started", SessionID: session})
	case "turn.completed":
		usage := tokenUsage(raw["usage"])
		a.emit(domain.TelemetryEvent{Category: "usage", Name: "usage.final", Phase: "finish", Message: "final model usage", SessionID: session, Usage: &usage})
		a.emit(domain.TelemetryEvent{Category: "model", Name: "model.request", Phase: "finish", Message: "model generation finished", SessionID: session})
	case "turn.failed":
		message := stringValue(raw["message"])
		if message == "" {
			message = "codex turn failed"
		}
		a.emit(domain.TelemetryEvent{Category: "error", Name: "model.error", Phase: "finish", Level: "error", Message: message, SessionID: session, Error: &domain.ErrorObservation{Type: "provider", Message: message}})
	case "item.started", "item.completed", "item.updated":
		item, _ := raw["item"].(map[string]any)
		itemType := stringValue(item["type"])
		phase := "progress"
		if typ == "item.started" {
			phase = "start"
		} else if typ == "item.completed" {
			phase = "finish"
		}
		id := stringValue(item["id"])
		switch itemType {
		case "command_execution", "mcp_tool_call", "tool_call":
			name := stringValue(item["command"])
			if name == "" {
				name = stringValue(item["name"])
			}
			msg := summarizeToolCall(name, item)
			success := int64Value(item["exit_code"]) == 0 && stringValue(item["status"]) != "failed"
			if phase == "finish" {
				out := firstNonEmpty(stringValue(item["aggregated_output"]), stringValue(item["output"]), stringValue(item["text"]))
				msg = summarizeToolResult(name, !success, out)
			}
			e := domain.TelemetryEvent{Category: "tool", Name: "tool.call", Phase: phase, Message: msg, SessionID: session, ToolCallID: id, Attributes: map[string]any{"status": item["status"], "exitCode": item["exit_code"]}}
			if phase == "finish" {
				e.Tool = &domain.ToolObservation{Name: name, Operation: itemType, CallID: id, Success: &success}
			}
			a.emit(e)
		case "agent_message":
			if text := stringValue(item["text"]); strings.TrimSpace(text) != "" {
				a.noteOutput(text)
			}
			a.emit(domain.TelemetryEvent{Category: "output", Name: "agent.output", Phase: phase, Message: stringValue(item["text"]), SessionID: session})
		case "reasoning":
			a.emit(domain.TelemetryEvent{Category: "reasoning", Name: "agent.reasoning", Phase: phase, Message: stringValue(item["text"]), SessionID: session})
		}
	default:
		a.handleGenericJSON(raw)
	}
}

func (a *protocolAdapter) handleGenericJSON(raw map[string]any) {
	typ := stringValue(raw["type"])
	message := stringValue(raw["message"])
	if message == "" {
		message = stringValue(raw["text"])
	}
	category := "lifecycle"
	if strings.Contains(typ, "tool") {
		category = "tool"
	} else if strings.Contains(typ, "reason") || strings.Contains(typ, "thought") {
		category = "reasoning"
	} else if strings.Contains(typ, "usage") {
		category = "usage"
	}
	phase := stringValue(raw["phase"])
	if phase == "" {
		phase = "progress"
	}
	event := domain.TelemetryEvent{Category: category, Name: typ, Phase: phase, Message: message, SessionID: stringValue(raw["session_id"]), ToolCallID: stringValue(raw["tool_call_id"]), Attributes: safeNativeAttributes(raw)}
	if category == "usage" {
		usage := tokenUsage(raw["usage"])
		if usage.TotalTokens == 0 {
			usage = tokenUsage(raw)
		}
		event.Usage = &usage
	}
	a.emit(event)
}

func stringValue(value any) string {
	v, _ := value.(string)
	return v
}

func sliceValue(value any) []any {
	v, _ := value.([]any)
	return v
}

func int64Value(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func float64Value(value any) float64 {
	v, _ := value.(float64)
	return v
}
