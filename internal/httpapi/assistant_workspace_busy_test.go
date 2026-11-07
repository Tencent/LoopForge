package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentflow/internal/runtime"
)

// Two roles running workbench_workspace_command at once: the second open hits
// the first one's environment preparation and must wait, not fail.
func TestOpenMainBotTerminalWaitsForParallelRole(t *testing.T) {
	busy := &mainBotWorkspaceError{status: 409, body: `{"code":"workspace_busy","error":"工作区正在使用或保存，请稍后重试（workspace is in use; preparation waits for active execution or terminal to finish）"}`}
	calls := 0
	opened, err := openMainBotTerminal(context.Background(), func(context.Context) (runtime.TerminalResult, error) {
		calls++
		if calls < 3 {
			return runtime.TerminalResult{}, busy
		}
		return runtime.TerminalResult{ID: "term-1"}, nil
	})
	if err != nil || opened.ID != "term-1" || calls != 3 {
		t.Fatalf("busy open not retried: %+v %v calls=%d", opened, err, calls)
	}
}

func TestOpenMainBotTerminalDoesNotRetryRealFailures(t *testing.T) {
	for _, failure := range []error{
		&mainBotWorkspaceError{status: 409, body: `{"error":"stop execution before editing"}`},
		&mainBotWorkspaceError{status: 500, body: `{"code":"workspace_busy"}`},
		errors.New("transport down"),
	} {
		calls := 0
		_, err := openMainBotTerminal(context.Background(), func(context.Context) (runtime.TerminalResult, error) {
			calls++
			return runtime.TerminalResult{}, failure
		})
		if err != failure || calls != 1 {
			t.Fatalf("%v retried %d times (err %v)", failure, calls, err)
		}
	}
}

func TestOpenMainBotTerminalBusyWaitIsBounded(t *testing.T) {
	previous := mainBotTerminalBusyWait
	mainBotTerminalBusyWait = 50 * time.Millisecond
	defer func() { mainBotTerminalBusyWait = previous }()
	busy := &mainBotWorkspaceError{status: 409, body: `{"code":"workspace_busy","error":"busy"}`}
	_, err := openMainBotTerminal(context.Background(), func(context.Context) (runtime.TerminalResult, error) {
		return runtime.TerminalResult{}, busy
	})
	if err == nil || !strings.Contains(err.Error(), "超时") || !errors.Is(err, busy) {
		t.Fatalf("busy wait not bounded: %v", err)
	}
}
