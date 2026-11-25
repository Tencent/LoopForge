package runtime

import (
	"fmt"
	"os"
	"strings"

	"agentflow/internal/domain"
)

// CodeBuddy enables background Agent calls when stdin stays open after the
// initial user frame. Keep it open through the parent result and outstanding
// child Agents, then send EOF so the CLI can shut down normally.
type codeBuddySOPStream struct {
	input      *os.File
	active     map[string]struct{}
	parentDone bool
	closed     bool
	drained    chan struct{}
}

// OpenSOPInput connects a prepared SOP command to the same stream observer
// that consumes its stdout. The caller closes the pipe after execution, even
// if startup fails or the process exits without a result.
func (o *ProtocolObserver) OpenSOPInput(command CommandSpec) (CommandSpec, func(), error) {
	if len(command.codeBuddySOPInput) == 0 {
		return command, func() {}, nil
	}
	if o == nil || o.adapter == nil {
		return command, nil, fmt.Errorf("CodeBuddy SOP stdin requires a protocol observer")
	}
	return o.adapter.openSOPInput(command)
}

func (a *protocolAdapter) openSOPInput(command CommandSpec) (CommandSpec, func(), error) {
	if len(command.codeBuddySOPInput) == 0 {
		return command, func() {}, nil
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return command, nil, fmt.Errorf("open CodeBuddy SOP stdin: %w", err)
	}
	// A file descriptor keeps os/exec independent of the open input stream.
	command.Stdin = reader
	a.codeBuddyStream = &codeBuddySOPStream{input: writer, active: make(map[string]struct{}), drained: make(chan struct{})}
	go func() { _, _ = writer.Write(command.codeBuddySOPInput) }()
	return command, func() {
		_ = reader.Close()
		_ = writer.Close()
	}, nil
}

func (s *codeBuddySOPStream) finishIfReady() {
	if s.closed || !s.parentDone || len(s.active) != 0 {
		return
	}
	s.closed = true
	_ = s.input.Close()
	close(s.drained)
}

func (a *protocolAdapter) handleCodeBuddyBackground(raw map[string]any, sessionID string) {
	stream := a.codeBuddyStream
	if stream == nil || stream.closed {
		return
	}
	id := stringValue(raw["task_id"])
	if id == "" {
		return
	}
	phase, message := "", ""
	attributes := map[string]any{"taskId": id}
	switch stringValue(raw["subtype"]) {
	case "task_started":
		if !strings.EqualFold(stringValue(raw["task_type"]), "Agent") && !strings.HasPrefix(id, "agent-") {
			return
		}
		stream.active[id] = struct{}{}
		phase, message = "start", "CodeBuddy 子 Agent 已后台启动"
	case "task_updated", "task_notification":
		if _, active := stream.active[id]; !active {
			return
		}
		patch, _ := raw["patch"].(map[string]any)
		status := firstNonEmpty(stringValue(raw["status"]), stringValue(patch["status"]))
		switch status {
		case "completed", "failed", "stopped", "killed", "cancelled":
		default:
			return
		}
		delete(stream.active, id)
		attributes["status"] = status
		phase, message = "finish", "CodeBuddy 子 Agent 后台任务结束"
	default:
		return
	}
	a.emit(domain.TelemetryEvent{Category: "lifecycle", Name: "agent.background", Phase: phase, Message: message,
		SessionID: sessionID, ToolCallID: stringValue(raw["tool_use_id"]), Attributes: attributes})
	stream.finishIfReady()
}
