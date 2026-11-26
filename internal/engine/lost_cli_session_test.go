package engine

import (
	"errors"
	"testing"
)

// TestLostCLISession covers the phrasings the CLIs print when the session a
// node wants to resume no longer exists on the execution machine. Every one of
// them must drop the stored session and restart the node, not park it on a
// human gate.
func TestLostCLISession(t *testing.T) {
	lost := []string{
		"no conversation found",
		"Session not found",
		"no such session",
		"conversation does not exist",
		// Codex JSON-RPC reply after the sandbox recreated .agentflow.
		"Error: thread/resume: thread/resume failed: no rollout found for thread id 01a1f321-9110-7af0-8a19-2b32b06025d4 (code -32600)",
		"thread/resume failed",
		"code -32600",
	}
	for _, msg := range lost {
		if !lostCLISession(msg, nil) {
			t.Errorf("lostCLISession(%q) = false, want true", msg)
		}
		if !lostCLISession("", errors.New(msg)) {
			t.Errorf("lostCLISession(err=%q) = false, want true", msg)
		}
	}
	for _, msg := range []string{
		"",
		"unexpected status 503 Service Unavailable",
		"exit status 1",
		"REJECT: 独立评审未通过",
	} {
		if lostCLISession(msg, nil) {
			t.Errorf("lostCLISession(%q) = true, want false", msg)
		}
	}
}
