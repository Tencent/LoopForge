package runtime

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"slices"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestCodeBuddySOPInputRemainsOpenForBackgroundAgents(t *testing.T) {
	executor := domain.ExecutorDefinition{Kind: "codebuddy", Binary: "codebuddy", Protocol: "stream-json",
		Args: []string{"-p", "--input-format=text", "--output-format", "text"}}
	prepared, err := NewExecutorRunner().PrepareRemote(t.TempDir(), RemoteInvocation{
		InvocationID: "sop-node",
		Executor:     executor,
		Agent:        domain.AgentDefinition{ID: "test-agent", Name: "Test Agent"},
		Prompt:       "line 1\nline 2",
		Options:      InvocationOptions{ResumeSessionID: "session-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	argv := prepared.Command.Argv
	if valueAfter(argv, "--input-format") != "stream-json" || valueAfter(argv, "--output-format") != "stream-json" ||
		valueAfter(argv, "--resume") != "session-1" || slices.Contains(argv, "line 1\nline 2") {
		t.Fatalf("unexpected CodeBuddy argv: %v", argv)
	}

	var phases []string
	observer := NewProtocolObserver(executor, func(event domain.TelemetryEvent) {
		if event.Name == "agent.background" {
			phases = append(phases, event.Phase)
		}
	})
	command, closeInput, err := observer.OpenSOPInput(prepared.Command)
	if err != nil {
		t.Fatal(err)
	}
	defer closeInput()
	stdin := command.Stdin.(*os.File)
	if err := stdin.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	input := bufio.NewReader(stdin)
	line, err := input.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Type    string
		Message struct{ Role, Content string }
	}
	if err := json.Unmarshal(line, &frame); err != nil || frame.Type != "user" || frame.Message.Role != "user" || frame.Message.Content != "line 1\nline 2" {
		t.Fatalf("unexpected input frame: %q, %v", line, err)
	}

	handle := func(line string) {
		observer.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: line})
	}
	handle(`{"type":"system","subtype":"task_started","task_type":"Agent","task_id":"agent-1"}`)
	handle(`{"type":"system","subtype":"task_started","task_type":"Agent","task_id":"agent-2"}`)
	handle(`{"type":"system","subtype":"task_started","task_type":"Bash","task_id":"bash-1"}`)
	handle(`{"type":"result","parent_tool_use_id":"agent-call","result":"child output"}`)
	if observer.adapter.Finished() {
		t.Fatal("a child result finished the parent")
	}
	handle(`{"type":"result","result":"parent output"}`)
	handle(`{"type":"system","subtype":"task_updated","task_id":"agent-1","patch":{"status":"completed"}}`)
	select {
	case <-observer.adapter.Completed():
		t.Fatal("input closed while the second child was active")
	default:
	}
	handle(`{"type":"system","subtype":"task_notification","task_id":"agent-2","status":"completed"}`)
	if _, err := input.ReadByte(); err != io.EOF {
		t.Fatalf("child completion should close the input stream, got %v", err)
	}
	if observer.VisibleOutput() != "parent output" || !slices.Equal(phases, []string{"start", "start", "finish", "finish"}) {
		t.Fatalf("result %q, background event phases %v", observer.VisibleOutput(), phases)
	}
}
