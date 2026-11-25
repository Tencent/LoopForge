package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
)

const CodeBuddyBackgroundAgentInstructions = `CodeBuddy can run an Agent in the background with run_in_background=true. Continue independent work after dispatch and wait when you need the result.
If the Agent call assigns a team, ask the member to send its result to the leader and wait for the matching inbox message. TaskOutput reports teammate status.
For a standalone background Agent, use TaskOutput with block=true to collect the result.`

const (
	CodeBuddyBackgroundTimeoutEvent = "agent.background.timeout"
	codeBuddyBackgroundResultGrace  = 5 * time.Second
)

// CodeBuddy enables background Agent calls when stdin stays open after the
// initial user frame. A successful parent result allows a short window for
// trailing child notifications. Some CodeBuddy versions stop emitting them
// after the parent finishes, so the wait must expire.
type codeBuddyBackgroundStream struct {
	mu         sync.Mutex
	input      *os.File
	active     map[string]struct{}
	parentDone bool
	closed     bool
	drained    chan struct{}
	graceTimer *time.Timer
	timedOut   bool
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
	stream := newCodeBuddyBackgroundStream(writer)
	a.codeBuddyStream = stream
	go func() { _, _ = writer.Write(command.codeBuddySOPInput) }()
	return command, func() {
		stream.stop()
		_ = reader.Close()
	}, nil
}

func (s *codeBuddyBackgroundStream) observeStdout(line string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
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
	case "error":
		if stringValue(event["parent_tool_use_id"]) == "" {
			s.finishParent(true)
		}
	case "system":
		s.observeTask(event, nil)
	}
}

func (s *codeBuddyBackgroundStream) finishParent(failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if failed || len(s.active) == 0 {
		s.closeLocked(true)
		return
	}
	if !s.parentDone {
		s.parentDone = true
		s.graceTimer = time.AfterFunc(codeBuddyBackgroundResultGrace, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.closed {
				s.timedOut = true
				s.closeLocked(true)
			}
		})
	}
}

func (s *codeBuddyBackgroundStream) closeLocked(drained bool) {
	if s.closed {
		return
	}
	s.closed = true
	if s.graceTimer != nil {
		s.graceTimer.Stop()
	}
	// Publish the terminal state before closing the writer so any racing
	// input relay can let the buffered terminal event reach the API.
	if drained {
		close(s.drained)
	}
	_ = s.input.Close()
}

func (s *codeBuddyBackgroundStream) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked(false)
}

func (s *codeBuddyBackgroundStream) isDrained() bool {
	select {
	case <-s.drained:
		return true
	default:
		return false
	}
}

func (s *codeBuddyBackgroundStream) timeoutEvent() domain.TelemetryEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.timedOut {
		return domain.TelemetryEvent{}
	}
	return domain.TelemetryEvent{
		Category: "lifecycle", Name: CodeBuddyBackgroundTimeoutEvent, Phase: "finish", Level: "warn",
		Message:    fmt.Sprintf("CodeBuddy 父 Agent 已结束，%d 个后台子 Agent 未在 %s 内报告完成，关闭命令输入", len(s.active), codeBuddyBackgroundResultGrace),
		Attributes: map[string]any{"taskIds": slices.Sorted(maps.Keys(s.active)), "graceMs": codeBuddyBackgroundResultGrace.Milliseconds()},
	}
}

func (s *codeBuddyBackgroundStream) observeTask(raw map[string]any, emit EventSink) {
	id := stringValue(raw["task_id"])
	if id == "" {
		return
	}
	s.mu.Lock()
	phase, status := s.updateTaskLocked(raw, id)
	s.mu.Unlock()
	if phase == "" || emit == nil {
		return
	}
	message := "CodeBuddy 子 Agent 已后台启动"
	attributes := map[string]any{"taskId": id}
	if status != "" {
		attributes["status"] = status
		message = "CodeBuddy 子 Agent 后台任务结束"
	}
	emit(domain.TelemetryEvent{Category: "lifecycle", Name: "agent.background", Phase: phase, Message: message,
		SessionID: stringValue(raw["session_id"]), ToolCallID: stringValue(raw["tool_use_id"]), Attributes: attributes})
}

func (s *codeBuddyBackgroundStream) updateTaskLocked(raw map[string]any, id string) (phase, status string) {
	if s.closed {
		return "", ""
	}
	switch stringValue(raw["subtype"]) {
	case "task_started":
		if !strings.EqualFold(stringValue(raw["task_type"]), "Agent") && !strings.HasPrefix(id, "agent-") {
			return "", ""
		}
		s.active[id] = struct{}{}
		return "start", ""
	case "task_updated", "task_notification":
		if _, active := s.active[id]; !active {
			return "", ""
		}
		patch, _ := raw["patch"].(map[string]any)
		status = firstNonEmpty(stringValue(raw["status"]), stringValue(patch["status"]))
		switch status {
		case "completed", "failed", "stopped", "killed", "cancelled":
		default:
			return "", ""
		}
		delete(s.active, id)
		if s.parentDone && len(s.active) == 0 {
			s.closeLocked(true)
		}
		return "finish", status
	default:
		return "", ""
	}
}

// CodeBuddySOPDrainDeadline signals when a Runner can stop a CodeBuddy SOP
// process that is still alive after EOF and the usual trailing output drain.
// Other Runner processes retain their existing lifetime.
func (o *ProtocolObserver) CodeBuddySOPDrainDeadline(ctx context.Context) <-chan struct{} {
	if o == nil || o.adapter == nil || o.adapter.codeBuddyStream == nil {
		return nil
	}
	drained := o.adapter.codeBuddyStream.drained
	stop := make(chan struct{})
	go func() {
		if waitForProcessTerminalDrain(ctx, drained, nil) {
			close(stop)
		}
	}()
	return stop
}
