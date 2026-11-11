package gitwork

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"agentflow/internal/procwatch"
)

// Settings that bound a clone, read on whichever host runs it (the platform or
// an execution machine). See docs/deployment.md.
const (
	CloneStallEnv   = "AGENTFLOW_GIT_CLONE_STALL_TIMEOUT"
	CloneTimeoutEnv = "AGENTFLOW_GIT_CLONE_TIMEOUT"
)

// Watch runs a long git command (clone) like Run, but through procwatch:
// output is streamed to the context's observer, a command that stops printing
// is killed, and the error carries only the last lines of output.
func Watch(ctx context.Context, workspace string, opts procwatch.Options, args ...string) (string, error) {
	token := TokenFrom(ctx)
	if credential, ok := HostCredentialFrom(ctx); ok {
		token = credential.Token
	}
	opts.Redact = func(line string) string { return Redact(line, token) }
	text, err := procwatch.Run(ctx, func(ctx context.Context) *exec.Cmd { return Command(ctx, workspace, args...) }, opts)
	if err != nil && text != "" {
		return text, fmt.Errorf("%w: %s", err, text)
	}
	return text, err
}

// Command builds a Git command with process-scoped authentication. Callers that
// need custom cancellation can configure the returned command before starting it.
// Any captured output must be redacted before it is returned or recorded.
func Command(ctx context.Context, workspace string, args ...string) *exec.Cmd {
	token := TokenFrom(ctx)
	dir := CommandDir(workspace)
	authArgs := AuthConfigArgs(token)
	env := Env(token)
	if hostCredential, ok := HostCredentialFrom(ctx); ok {
		authArgs = hostAuthConfigArgs(hostCredential)
		env = hostEnv(hostCredential)
	}
	cmd := exec.CommandContext(ctx, "git", append(authArgs, args...)...)
	cmd.Dir = dir
	cmd.Env = WithEnvValue(env, "PWD", dir)
	// A run without a checkout must not discover the platform's own .git
	// when the operator keeps workspaces below a source checkout.
	if absolute, err := filepath.Abs(dir); err == nil {
		cmd.Env = WithEnvValue(cmd.Env, "GIT_CEILING_DIRECTORIES", filepath.Dir(absolute))
	}
	return cmd
}

// Run executes Git and redacts command output before returning it.
func Run(ctx context.Context, workspace string, args ...string) (string, error) {
	token := TokenFrom(ctx)
	if credential, ok := HostCredentialFrom(ctx); ok {
		token = credential.Token
	}
	cmd := Command(ctx, workspace, args...)
	out, err := cmd.CombinedOutput()
	text := Redact(strings.TrimSpace(string(out)), token)
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("%w: %s", err, text)
		}
		return text, err
	}
	return text, nil
}

// C runs `git -C workspace args...`.
func C(ctx context.Context, workspace string, args ...string) (string, error) {
	return Run(ctx, workspace, append([]string{"-C", workspace}, args...)...)
}

// RevParse resolves ref to a commit SHA.
func RevParse(ctx context.Context, workspace, ref string) (string, bool) {
	out, err := C(ctx, workspace, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}
