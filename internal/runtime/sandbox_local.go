package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type localSandbox struct {
	id, workspace string
	environment   map[string]string
	driver        string
}

func (s *localSandbox) ID() string        { return s.id }
func (s *localSandbox) Workspace() string { return s.workspace }
func (s *localSandbox) Driver() string {
	if s.driver != "" {
		return s.driver
	}
	return "local"
}
func (s *localSandbox) Close(context.Context) error { return nil }
func (s *localSandbox) Exec(ctx context.Context, command CommandSpec, sink EventSink) (string, error) {
	cmd, err := s.command(ctx, command, controlledBaseEnvironment(os.Environ()))
	if err != nil {
		return "", err
	}
	return runCommand(cmd, sink)
}

func (s *localSandbox) command(ctx context.Context, command CommandSpec, base []string) (*exec.Cmd, error) {
	if len(command.Argv) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	workspace, err := filepath.Abs(s.workspace)
	if err != nil {
		return nil, fmt.Errorf("sandbox workspace: %w", err)
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("sandbox workspace missing %s: %w", workspace, err)
	}
	cmd := exec.CommandContext(ctx, command.Argv[0], command.Argv[1:]...)
	cmd.Dir = workspace
	cmd.Env = appendEnvironment(base, s.environment, command.environment)
	cmd.Env = upsertEnv(cmd.Env, "PWD", workspace)
	cmd.Env = upsertEnv(cmd.Env, "GIT_CEILING_DIRECTORIES", filepath.Dir(workspace))
	return cmd, nil
}

// ExecuteLocalCommand runs a prepared provider command with the same closed
// credential binding used by managed sandboxes. A temporary private HOME keeps
// control-plane utilities (for example requirement clarification) from reading
// or modifying the operator's CLI state or the inspected repository.
func ExecuteLocalCommand(ctx context.Context, workspace string, command CommandSpec, credentials RunCredentials, sink EventSink) (string, error) {
	sandbox := &localSandbox{id: "control-plane", workspace: workspace}
	var cleanup func()
	if command.PrivateHome {
		root, err := os.MkdirTemp("", "agentflow-private-home-")
		if err != nil {
			return "", err
		}
		cleanup = func() { _ = os.RemoveAll(root) }
		command = command.WithPrivateHomeDir(root)
		if err := os.MkdirAll(command.environment["TMPDIR"], 0o700); err != nil {
			cleanup()
			return "", err
		}
	}
	if cleanup != nil {
		defer cleanup()
	}
	bound, err := bindCommand(sandbox, workspace, command, credentials)
	if err != nil {
		return "", err
	}
	return sandbox.Exec(ctx, bound, sink)
}
