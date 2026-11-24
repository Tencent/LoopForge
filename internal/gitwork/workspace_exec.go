package gitwork

import (
	"context"
	"io"
	"os/exec"
	"strings"

	"agentflow/internal/procwatch"
)

// WorkspaceCommand describes a Git process against a checkout. The sandbox
// supplies its working directory, executable and credential bindings.
type WorkspaceCommand struct {
	Args       []string
	Stdin      io.Reader
	StdoutOnly bool
	Watch      *procwatch.Options
}

type WorkspaceExecutor func(context.Context, string, WorkspaceCommand) (string, error)

type workspaceExecutorKey struct{}

func WithWorkspaceExecutor(ctx context.Context, executor WorkspaceExecutor) context.Context {
	return context.WithValue(ctx, workspaceExecutorKey{}, executor)
}

func workspaceExecutor(ctx context.Context) WorkspaceExecutor {
	executor, _ := ctx.Value(workspaceExecutorKey{}).(WorkspaceExecutor)
	return executor
}

func HasWorkspaceExecutor(ctx context.Context) bool { return workspaceExecutor(ctx) != nil }

// RunWorkspaceCommand runs Git plumbing that needs stdin or stdout-only
// output, using the same executor as the rest of checkout preparation.
func RunWorkspaceCommand(ctx context.Context, dir string, spec WorkspaceCommand) (string, error) {
	if run := workspaceExecutor(ctx); run != nil {
		return run(ctx, dir, spec)
	}
	cmd := exec.CommandContext(ctx, "git", spec.Args...)
	cmd.Dir, cmd.Stdin = dir, spec.Stdin
	output := cmd.CombinedOutput
	if spec.StdoutOnly {
		output = cmd.Output
	}
	out, err := output()
	return string(out), err
}

// WorkspaceRemote returns the expected transport endpoint for the command.
// Fetches and pushes naming origin use the repository selected by the caller.
func WorkspaceRemote(ctx context.Context, args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "https://") || strings.HasPrefix(arg, "http://") || IsSSHReference(arg) {
			return arg
		}
	}
	if remote, _ := ctx.Value(sshRemoteContext{}).(string); remote != "" {
		return remote
	}
	remote, _ := ctx.Value(httpRemoteKey{}).(string)
	return remote
}
