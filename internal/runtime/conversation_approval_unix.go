//go:build !windows

package runtime

import (
	"context"
	"os/exec"
)

func conversationApprovalLaunch(executionRoot string, arguments ...string) (string, error) {
	if len(arguments) == 1 && arguments[0] == "codex" {
		arguments = nil // Windows-only protocol adaptation; preserve Unix hooks
	}
	return posixConversationApprovalLaunch(executionRoot, arguments...), nil
}

func conversationApprovalFailureGuard(_ string, launch string) string { return launch + " || exit 2" }

func checkConversationApprovalRuntime(ctx context.Context) error {
	return exec.CommandContext(ctx, "python3", "-c", "import json,urllib.request").Run()
}

func adaptPlatformConversationApproval(_ string, command CommandSpec) (CommandSpec, error) {
	return command, nil
}
