package managedcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Claude registers --system-prompt-file with hideHelp(). Its --help also exits
// successfully for UNKNOWN flags, so flag+help is not a valid support probe.
// Require both an unknown-option negative control and an exact missing-file
// positive control. Neither supplies a prompt; stdin is closed, the file cannot
// be read and credentials remain isolated. Real-vendor validation additionally
// confirmed this parser path in an offline container, before model execution.
func (m *Manager) validateClaudeHiddenFileArgument(ctx context.Context, installed Installation) error {
	probeDir, err := os.MkdirTemp(filepath.Join(m.Root, "tmp"), "argument-probe-*")
	if err != nil {
		return err
	}
	defer os.Remove(probeDir) // only this newly created empty directory, never recursive
	missing := filepath.Join(probeDir, "nonexistent-system-prompt.txt")
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		return errors.New("hidden-argument probe path unexpectedly exists")
	}
	const unknown = "--agentflow-invalid-option-probe"
	negative, negativeErr := m.command(ctx, installed.Binary, []string{unknown, missing, "--print"}, m.Root)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if negativeErr == nil || !strings.Contains(strings.ToLower(string(negative)), "unknown option '"+unknown+"'") {
		return errors.New("cannot verify hidden --system-prompt-file: unknown-option control was not rejected")
	}
	positive, positiveErr := m.command(ctx, installed.Binary, []string{"--system-prompt-file", missing, "--print"}, m.Root)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if positiveErr == nil || strings.TrimSpace(string(positive)) != "Error: System prompt file not found: "+missing {
		return fmt.Errorf("candidate CLI did not confirm required hidden argument --system-prompt-file with the missing-file probe")
	}
	return nil
}
