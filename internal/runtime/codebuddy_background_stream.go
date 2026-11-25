package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"agentflow/internal/domain"
)

const CodeBuddyBackgroundAgentInstructions = `CodeBuddy can run an Agent in the background with run_in_background=true. Continue independent work after dispatch and wait when you need the result.
If the Agent call assigns a team, ask the member to send its result to the leader and wait for the matching inbox message. TaskOutput reports teammate status.
For a standalone background Agent, use TaskOutput with block=true to collect the result.`

// CodeBuddy enables background Agent calls when stdin stays open after the
// initial user frame. Keep it open through the parent result and outstanding
// child Agents, then send EOF so the CLI can shut down normally.
type codeBuddyBackgroundStream struct {
	input      *os.File
	active     map[string]struct{}
	parentDone bool
	closed     bool
	drained    chan struct{}
}

func newCodeBuddyBackgroundStream(input *os.File) *codeBuddyBackgroundStream {
	return &codeBuddyBackgroundStream{input: input, active: make(map[string]struct{}), drained: make(chan struct{})}
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
	a.codeBuddyStream = newCodeBuddyBackgroundStream(writer)
	go func() { _, _ = writer.Write(command.codeBuddySOPInput) }()
	return command, func() {
		_ = reader.Close()
		_ = writer.Close()
	}, nil
}

func (s *codeBuddyBackgroundStream) observeStdout(line string) {
	line = strings.TrimSpace(line)
	if s.closed || line == "" || line[0] != '{' {
		return
	}
	var event map[string]any
	if json.Unmarshal([]byte(line), &event) != nil {
		return
	}
	switch stringValue(event["type"]) {
	case "result":
		if stringValue(event["parent_tool_use_id"]) == "" {
			failed, _ := event["is_error"].(bool)
			s.finishParent(failed)
		}
	case "system":
		s.observeTask(event, nil)
	}
}

func (s *codeBuddyBackgroundStream) finishParent(failed bool) {
	s.parentDone = true
	if failed {
		clear(s.active)
	}
	s.finishIfReady()
}

func (s *codeBuddyBackgroundStream) finishIfReady() {
	if s.closed || !s.parentDone || len(s.active) != 0 {
		return
	}
	s.closed = true
	_ = s.input.Close()
	close(s.drained)
}

func (s *codeBuddyBackgroundStream) observeTask(raw map[string]any, emit EventSink) {
	if s.closed {
		return
	}
	id := stringValue(raw["task_id"])
	if id == "" {
		return
	}
	phase, message, status := "", "", ""
	switch stringValue(raw["subtype"]) {
	case "task_started":
		if !strings.EqualFold(stringValue(raw["task_type"]), "Agent") && !strings.HasPrefix(id, "agent-") {
			return
		}
		s.active[id] = struct{}{}
		phase, message = "start", "CodeBuddy 子 Agent 已后台启动"
	case "task_updated", "task_notification":
		if _, active := s.active[id]; !active {
			return
		}
		patch, _ := raw["patch"].(map[string]any)
		status = firstNonEmpty(stringValue(raw["status"]), stringValue(patch["status"]))
		switch status {
		case "completed", "failed", "stopped", "killed", "cancelled":
		default:
			return
		}
		delete(s.active, id)
		phase, message = "finish", "CodeBuddy 子 Agent 后台任务结束"
	default:
		return
	}
	if emit != nil {
		attributes := map[string]any{"taskId": id}
		if status != "" {
			attributes["status"] = status
		}
		emit(domain.TelemetryEvent{Category: "lifecycle", Name: "agent.background", Phase: phase, Message: message,
			SessionID: stringValue(raw["session_id"]), ToolCallID: stringValue(raw["tool_use_id"]), Attributes: attributes})
	}
	s.finishIfReady()
}
