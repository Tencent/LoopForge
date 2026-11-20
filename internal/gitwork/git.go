package gitwork

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"agentflow/internal/procwatch"
)

// Run executes git with the context's host-scoped credential helper or SSH
// key. Output is trimmed and redacted; on failure it is part of the error.
func Run(ctx context.Context, workspace string, args ...string) (string, error) {
	token := TokenFrom(ctx)
	sshEnv, cleanup, err := sshCommandEnvironment(ctx, args)
	if err != nil {
		return "", err
	}
	defer cleanup()
	home, closeHome, err := privateHTTPHome(ctx)
	if err != nil {
		return "", err
	}
	defer closeHome()
	cmd := command(ctx, workspace, sshEnv, args)
	if home != "" {
		cmd.Env = WithEnvValue(cmd.Env, "HOME", home)
	}
	out, err := cmd.CombinedOutput()
	text := redactContextGitSecrets(ctx, Redact(strings.TrimSpace(string(out)), token))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("%w: %s", err, text)
		}
		return text, err
	}
	return text, nil
}

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
	sshEnv, cleanup, err := sshCommandEnvironment(ctx, args)
	if err != nil {
		return "", err
	}
	defer cleanup()
	home, closeHome, err := privateHTTPHome(ctx)
	if err != nil {
		return "", err
	}
	defer closeHome()
	opts.Redact = func(line string) string { return redactContextGitSecrets(ctx, Redact(line, token)) }
	text, err := procwatch.Run(ctx, func(ctx context.Context) *exec.Cmd {
		cmd := command(ctx, workspace, sshEnv, args)
		if home != "" {
			cmd.Env = WithEnvValue(cmd.Env, "HOME", home)
		}
		return cmd
	}, opts)
	if err != nil && text != "" {
		return text, fmt.Errorf("%w: %s", err, text)
	}
	return text, err
}

func command(ctx context.Context, workspace string, sshEnv map[string]string, args []string) *exec.Cmd {
	dir := CommandDir(workspace)
	credential, host, _ := httpCredentialForCommand(ctx, args)
	authArgs := scopedHTTPAuthArgs(credential)
	cmd := exec.CommandContext(ctx, "git", append(authArgs, args...)...)
	cmd.Dir = dir
	cmd.Env = WithEnvValue(Env(""), "PWD", dir)
	if len(sshEnv) == 0 {
		cmd.Env = WithEnvValue(cmd.Env, "GIT_CONFIG_GLOBAL", os.DevNull)
		cmd.Env = WithEnvValue(cmd.Env, "GIT_CONFIG_NOSYSTEM", "1")
		cmd.Env = WithEnvValue(cmd.Env, "GIT_CONFIG_PARAMETERS", "")
		cmd.Env = WithEnvValue(cmd.Env, gitHTTPHostEnv, host)
		cmd.Env = WithEnvValue(cmd.Env, gitHTTPUserEnv, credential.Username)
		cmd.Env = WithEnvValue(cmd.Env, gitHTTPPassEnv, credential.Token)
	}
	if len(sshEnv) > 0 {
		// Do not let URL rewrites switch a managed SSH operation to another
		// transport. SSH itself only connects to the requested pinned host.
		cmd.Env = WithEnvValue(cmd.Env, "GIT_ALLOW_PROTOCOL", "ssh")
		cmd.Env = WithEnvValue(cmd.Env, "GIT_CONFIG_GLOBAL", os.DevNull)
		cmd.Env = WithEnvValue(cmd.Env, "GIT_CONFIG_NOSYSTEM", "1")
		cmd.Env = WithEnvValue(cmd.Env, "GIT_CONFIG_PARAMETERS", "")
		for k, v := range sshEnv {
			cmd.Env = WithEnvValue(cmd.Env, k, v)
		}
	}
	// A run without a checkout must not discover the platform's own .git
	// when the operator keeps workspaces below a source checkout.
	if absolute, err := filepath.Abs(dir); err == nil {
		cmd.Env = WithEnvValue(cmd.Env, "GIT_CEILING_DIRECTORIES", filepath.Dir(absolute))
	}
	return cmd
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
