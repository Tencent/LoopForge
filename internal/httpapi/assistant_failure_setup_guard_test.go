package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/runtime"
)

// Issue #660: a workspace step that fails before the CLI is launched must be
// reported as a setup failure the user can act on, not as a CLI exit.
func TestAssistantFailureReportsWorkspaceSetupBeforeLaunch(t *testing.T) {
	setup := fmt.Errorf("%w: %w", runtime.ErrConversationNotStarted,
		errors.New("inspect script environment architecture: image sha256:abc: exit status 1: Error response from daemon: No such image: sha256:abc; container uname: exit status 1: OCI runtime exec failed"))
	for name, err := range map[string]error{
		"setup error":                     setup,
		"setup error with stderr noise":   fmt.Errorf("%w; stderr=exit status 1 | docker: Error response from daemon", setup),
		"setup error wrapping exec error": fmt.Errorf("%w: %w", runtime.ErrConversationNotStarted, &exitStatusError{}),
	} {
		t.Run(name, func(t *testing.T) {
			f := classifyAssistantFailure(context.Background(), err)
			if f.Code != "WORKSPACE_SETUP_FAILED" || f.Phase != "setup" {
				t.Fatalf("failure = %+v", f)
			}
			if !strings.Contains(f.Message, "CLI 未启动") || !strings.Contains(f.Suggestion, "Docker") {
				t.Fatalf("message/suggestion = %q / %q", f.Message, f.Suggestion)
			}
			status, message := assistantGenerationFailure(context.Background(), err)
			if status != http.StatusBadGateway || message != f.Message {
				t.Fatalf("generation failure = %d %q, want %d %q", status, message, http.StatusBadGateway, f.Message)
			}
		})
	}
	// Without the not-started marker the same exit is still a CLI exit.
	if f := classifyAssistantFailure(context.Background(), errors.New("inspect script environment architecture: exit status 1")); f.Code != "CLI_EXIT_NONZERO" {
		t.Fatalf("unmarked exit classified as %s", f.Code)
	}
}

// exitStatusError mimics an error that reads like a CLI exit.
type exitStatusError struct{}

func (*exitStatusError) Error() string { return "exit status 125" }
