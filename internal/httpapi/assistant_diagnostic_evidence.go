package httpapi

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const diagnosticTimelineLimit = 160

// Keep the beginning and tail. Repeated warnings must not evict the first
// failure, the initial configuration, or the process's start.
func (d *assistantDiagnosticsCollector) appendTimelineLocked(e domain.AssistantDiagnosticEvent) {
	d.timelineSequence++
	e.Sequence = d.timelineSequence
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if len(d.report.Timeline) == diagnosticTimelineLimit {
		d.report.Timeline = append(d.report.Timeline[:24], d.report.Timeline[25:]...)
		d.dropLocked("timeline", 1)
	}
	d.report.Timeline = append(d.report.Timeline, e)
}

func (d *assistantDiagnosticsCollector) dropLocked(kind string, n int) {
	d.report.Truncated = true
	if d.report.Dropped == nil {
		d.report.Dropped = map[string]int{}
	}
	d.report.Dropped[kind] += n
}

func (d *assistantDiagnosticsCollector) platform(kind, message string, user domain.User) {
	if d == nil {
		return
	}
	e := domain.AssistantDiagnosticEvent{Kind: kind, Source: "platform", Message: sanitizeAssistantDiagnostic(message, user, 2048)}
	d.mu.Lock()
	d.appendTimelineLocked(e)
	id := d.report.GenerationID
	d.mu.Unlock()
	log.Printf("assistant diagnostic=%s instance=%s event=%s: %s", id, assistantDiagnosticInstance.InstanceID, kind, e.Message)
}

// The first explicit cause wins: a process killed after a cancellation must
// not replace the reason that caused that cancellation.
func (d *assistantDiagnosticsCollector) terminate(code, message string, user domain.User) {
	if d == nil {
		return
	}
	message = sanitizeAssistantDiagnostic(message, user, 2048)
	d.mu.Lock()
	if d.report.Termination != nil {
		d.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	d.report.Termination = &domain.AssistantDiagnosticTermination{At: now, Code: code, Source: "platform", Message: message}
	d.appendTimelineLocked(domain.AssistantDiagnosticEvent{At: now, Source: "platform", Kind: "generation.termination", Code: code, Message: message})
	id := d.report.GenerationID
	d.mu.Unlock()
	log.Printf("assistant diagnostic=%s instance=%s termination=%s: %s", id, assistantDiagnosticInstance.InstanceID, code, message)
}

func (a *assistantDiagnosticAttempt) process(event domain.TelemetryEvent) {
	if a == nil {
		return
	}
	now := time.Now().UTC()
	if !event.Timestamp.IsZero() {
		now = event.Timestamp
	}
	a.update(func(attempt *domain.AssistantCLIAttempt) {
		if attempt.Process == nil {
			attempt.Process = &domain.AssistantDiagnosticProcess{State: "not_started"}
		}
		p := attempt.Process
		switch event.Name {
		case "process.started":
			p.PID, _ = event.Attributes["pid"].(int)
			p.State, p.StartedAt = "running", &now
		case "process.start_failed":
			p.State, p.FinishedAt = "start_failed", &now
		case "process.finished":
			if p.State != "start_failed" {
				p.State = "exited"
			}
			p.FinishedAt = &now
			p.Signal, _ = event.Attributes["signal"].(string)
			p.UserCPUMs, _ = event.Attributes["userCpuMs"].(int64)
			p.SystemCPUMs, _ = event.Attributes["systemCpuMs"].(int64)
			if code, ok := event.Attributes["exitCode"].(int); ok {
				attempt.ExitCode = &code
			}
			if incomplete, _ := event.Attributes["outputIncomplete"].(bool); incomplete {
				a.d.dropLocked("processOutputIncomplete", 1)
			}
		case "process.stdout":
			p.StdoutLines++
			if p.FirstStdoutAt == nil {
				p.FirstStdoutAt = &now
			}
			p.LastStdoutAt = &now
		case "process.stderr":
			p.StderrLines++
			if p.FirstStderrAt == nil {
				p.FirstStderrAt = &now
			}
			p.LastStderrAt = &now
		}
		if event.Name != "process.stdout" && event.Name != "process.stderr" {
			// process.started.Message contains argv and is intentionally discarded.
			e := domain.AssistantDiagnosticEvent{At: now, Source: "process", Kind: event.Name, AttemptID: a.id}
			if event.Name == "process.start_failed" {
				e.Message = sanitizeAssistantDiagnostic(event.Message, a.user, 1024)
			}
			if event.Name == "process.finished" && p.Signal != "" {
				e.Message = "signal: " + p.Signal
			}
			a.d.appendTimelineLocked(e)
		}
	})
}

var (
	diagnosticRequestID  = regexp.MustCompile(`(?i)\b(?:request[ _-]?id|x-request-id)\s*[=:]\s*["']?([A-Za-z0-9_.:/-]{4,160})`)
	diagnosticTraceID    = regexp.MustCompile(`(?i)\b(?:trace[ _-]?id|x-trace-id)\s*[=:]\s*["']?([A-Za-z0-9_.:/-]{4,160})`)
	diagnosticHTTPStatus = regexp.MustCompile(`(?i)\b(?:HTTP(?:/[0-9.]+)?\s+|status(?:\s+code)?[\s:=]+)([1-5][0-9]{2})\b`)
)

func diagnosticTextMetadata(e *domain.AssistantDiagnosticEvent) {
	if match := diagnosticRequestID.FindStringSubmatch(e.Message); e.RequestID == "" && len(match) > 1 {
		e.RequestID = match[1]
	}
	if match := diagnosticTraceID.FindStringSubmatch(e.Message); e.TraceID == "" && len(match) > 1 {
		e.TraceID = match[1]
	}
	if match := diagnosticHTTPStatus.FindStringSubmatch(e.Message); e.StatusCode == 0 && len(match) > 1 {
		e.StatusCode, _ = strconv.Atoi(match[1])
	}
}

func isPlaceholderRetryEvent(e *domain.AssistantDiagnosticEvent) bool {
	if e == nil {
		return false
	}
	return e.RequestID == "" && strings.HasPrefix(strings.TrimSpace(e.Message), "Reconnecting...")
}

func (a *assistantDiagnosticAttempt) recordEvent(e domain.AssistantDiagnosticEvent) {
	e.Source, e.AttemptID = "cli", a.id
	diagnosticTextMetadata(&e)
	a.update(func(attempt *domain.AssistantCLIAttempt) {
		isErr := strings.Contains(e.Kind, "error") || strings.Contains(e.Kind, "fail") || e.StatusCode >= 400
		if isErr && (attempt.FirstError == nil || (isPlaceholderRetryEvent(attempt.FirstError) && !isPlaceholderRetryEvent(&e))) {
			first := e
			attempt.FirstError = &first
		}
		if len(attempt.Events) == 40 {
			attempt.Events = append(attempt.Events[:8], attempt.Events[9:]...)
			a.d.dropLocked("protocolEvents", 1)
		}
		attempt.Events = append(attempt.Events, e)
		a.d.appendTimelineLocked(e)
	})
}

func (d *assistantDiagnosticsCollector) toolStart(source, id, name, parent, attemptID string, user domain.User) string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if id == "" {
		d.toolSequence++
		id = fmt.Sprintf("gateway/%d", d.toolSequence)
	}
	id = sanitizeAssistantDiagnostic(id, user, 160)
	for i := range d.report.Tools {
		t := &d.report.Tools[i]
		if t.ID == id && t.Source == source && t.AttemptID == attemptID {
			if name != "" {
				t.Name = sanitizeAssistantDiagnostic(name, user, 120)
			}
			return id
		}
	}
	if len(d.report.Tools) >= 80 {
		drop := -1
		for i, t := range d.report.Tools {
			if t.Status == "completed" {
				drop = i
				break
			}
		}
		if drop == -1 {
			drop = 0
		}
		d.report.Tools = append(d.report.Tools[:drop], d.report.Tools[drop+1:]...)
		d.dropLocked("tools", 1)
	}
	now := time.Now().UTC()
	name = sanitizeAssistantDiagnostic(name, user, 120)
	d.report.Tools = append(d.report.Tools, domain.AssistantDiagnosticTool{ID: id, AttemptID: attemptID, Source: source, Name: name, ParentID: sanitizeAssistantDiagnostic(parent, user, 160), Status: "running", StartedAt: now})
	d.appendTimelineLocked(domain.AssistantDiagnosticEvent{At: now, Source: source, Kind: "tool.started", AttemptID: attemptID, ToolID: id, Tool: name})
	return id
}

func (d *assistantDiagnosticsCollector) toolEnd(source, id, attemptID, message string, failed bool, exitCode *int, user domain.User) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.report.Tools) - 1; i >= 0; i-- {
		t := &d.report.Tools[i]
		if t.ID != id || t.Source != source || t.AttemptID != attemptID || t.FinishedAt != nil {
			continue
		}
		now := time.Now().UTC()
		t.FinishedAt, t.ExitCode, t.Status = &now, exitCode, "completed"
		e := domain.AssistantDiagnosticEvent{At: now, Source: source, Kind: "tool.completed", AttemptID: attemptID, ToolID: id, Tool: t.Name}
		if failed {
			t.Status, e.Kind = "failed", "tool.failed"
			t.Error = sanitizeAssistantDiagnostic(message, user, 1024)
			if t.Error == "" {
				t.Error = "工具返回失败标记，未提供错误详情"
			}
			e.Message = t.Error
			diagnosticTextMetadata(&e)
			t.StatusCode, t.RequestID, t.TraceID = e.StatusCode, e.RequestID, e.TraceID
		}
		d.appendTimelineLocked(e)
		return
	}
}

// Only failing tool responses contribute text. Successful outputs, arguments,
// commands, model prose and reasoning are never retained by this collector.
func diagnosticErrorText(raw json.RawMessage) string {
	if len(raw) > 256<<10 {
		raw = raw[:256<<10]
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return truncateRunes(text, 2048)
	}
	var blocks []struct{ Type, Text string }
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" {
				text += b.Text + "\n"
				if len(text) > 8192 {
					break
				}
			}
		}
		return truncateRunes(strings.TrimSpace(text), 2048)
	}
	var detail struct{ Message, Error string }
	if json.Unmarshal(raw, &detail) == nil {
		return truncateRunes(strings.TrimSpace(detail.Message+" "+detail.Error), 2048)
	}
	return ""
}

func (a *assistantDiagnosticAttempt) tools(line string) {
	if a == nil || len(line) > 256<<10 {
		return
	}
	var event struct {
		Type    string `json:"type"`
		Parent  string `json:"parent_tool_use_id"`
		Message struct {
			Content []struct {
				Type, ID, Name string
				ToolUseID      string          `json:"tool_use_id"`
				IsError        bool            `json:"is_error"`
				Content        json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
		Event struct {
			Type         string
			ContentBlock struct{ Type, ID, Name string } `json:"content_block"`
		} `json:"event"`
	}
	if json.Unmarshal([]byte(line), &event) != nil {
		return
	}
	if event.Type == "stream_event" && event.Event.Type == "content_block_start" && event.Event.ContentBlock.Type == "tool_use" {
		b := event.Event.ContentBlock
		a.d.toolStart("cli", b.ID, b.Name, event.Parent, a.id, a.user)
	}
	for _, b := range event.Message.Content {
		if event.Type == "assistant" && b.Type == "tool_use" && b.ID != "" {
			a.d.toolStart("cli", b.ID, b.Name, event.Parent, a.id, a.user)
		}
		if event.Type == "user" && b.Type == "tool_result" && b.ToolUseID != "" {
			message := ""
			if b.IsError {
				message = diagnosticErrorText(b.Content)
			}
			a.d.toolEnd("cli", b.ToolUseID, a.id, message, b.IsError, nil, a.user)
		}
	}
}

func (a *assistantDiagnosticAttempt) nativeMetadata(raw map[string]any) {
	get := func(v map[string]any, key string) string { value, _ := v[key].(string); return value }
	params, _ := raw["params"].(map[string]any)
	result, _ := raw["result"].(map[string]any)
	version := ""
	for _, key := range []string{"claude_code_version", "codebuddy_version", "cliVersion"} {
		if value := get(raw, key); diagnosticVersion.MatchString(value) {
			version = value
		}
	}
	// Codex initialize returns a userAgent; extract only the version, not host
	// descriptions or other fields from the initialization response.
	if agent := get(result, "userAgent"); strings.Contains(agent, "codex") {
		if match := regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+\b`).FindString(agent); match != "" {
			version = match
		}
	}
	method, kind := get(raw, "method"), get(raw, "type")
	eventSub, _ := raw["event"].(map[string]any)
	a.update(func(attempt *domain.AssistantCLIAttempt) {
		if version != "" {
			attempt.CLIVersion, attempt.VersionSource = version, "native_handshake"
		}
		for _, key := range []string{"session_id", "sessionId", "sessionID", "conversation_id"} {
			if id := get(raw, key); id != "" {
				attempt.NativeSessionID = sanitizeAssistantDiagnostic(id, a.user, 160)
			}
		}
		if id := get(params, "threadId"); id != "" {
			attempt.NativeSessionID = sanitizeAssistantDiagnostic(id, a.user, 160)
		}
		if thread, ok := result["thread"].(map[string]any); ok && get(thread, "id") != "" {
			attempt.NativeSessionID = sanitizeAssistantDiagnostic(get(thread, "id"), a.user, 160)
		}
		if turn, ok := params["turn"].(map[string]any); ok && get(turn, "id") != "" {
			attempt.NativeTurnID = sanitizeAssistantDiagnostic(get(turn, "id"), a.user, 160)
		}
		milestone := ""
		dedupe := false
		switch {
		case kind == "system" && get(raw, "subtype") == "init" || get(result, "userAgent") != "" || get(raw, "event") == "init":
			milestone, dedupe = "cli.initialized", true
		case method == "turn/started" || kind == "turn_start":
			milestone = "model.turn_started"
		case method == "item/agentMessage/delta" || method == "item/reasoning/summaryTextDelta" || method == "item/reasoning/textDelta" ||
			(kind == "stream_event" && get(eventSub, "type") == "content_block_delta") || kind == "model.streaming" || kind == "message_update":
			milestone, dedupe = "model.first_output_delta", true
		case method == "turn/completed":
			milestone = "model.turn_finished"
		case kind == "result" || kind == "final" || kind == "agent_end":
			milestone = "protocol.result_received"
		}
		if milestone != "" {
			if dedupe {
				for _, existing := range a.d.report.Timeline {
					if existing.AttemptID == a.id && existing.Kind == milestone {
						return
					}
				}
			}
			a.d.appendTimelineLocked(domain.AssistantDiagnosticEvent{Source: "cli", Kind: milestone, AttemptID: a.id})
		}
	})
	if method != "item/started" && method != "item/completed" {
		return
	}
	item, _ := params["item"].(map[string]any)
	id, name := get(item, "id"), ""
	switch get(item, "type") {
	case "mcpToolCall":
		name = "mcp__" + get(item, "server") + "__" + get(item, "tool")
	case "commandExecution":
		name = "Bash"
	case "webSearch":
		name = "WebSearch"
	}
	if id == "" || name == "" {
		return
	}
	a.d.toolStart("cli", id, name, "", a.id, a.user)
	if method == "item/completed" {
		var exitCode *int
		if n, ok := item["exitCode"].(float64); ok {
			code := int(n)
			exitCode = &code
		}
		status := get(item, "status")
		failed := status == "failed" || status == "declined" || item["error"] != nil || exitCode != nil && *exitCode != 0
		message := ""
		if failed {
			data, _ := json.Marshal(item["error"])
			message = diagnosticErrorText(data)
			if message == "" && exitCode != nil {
				message = fmt.Sprintf("工具进程退出码 %d", *exitCode)
			}
		}
		a.d.toolEnd("cli", id, a.id, message, failed, exitCode, a.user)
	}
}
