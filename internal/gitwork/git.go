package gitwork

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Run executes git with the context token applied through AuthConfigArgs and
// Env. Output is trimmed and redacted; on failure it is part of the error.
func Run(ctx context.Context, workspace string, args ...string) (string, error) {
	token := TokenFrom(ctx)
	dir := CommandDir(workspace)
	cmd := exec.CommandContext(ctx, "git", append(AuthConfigArgs(token), args...)...)
	cmd.Dir = dir
	cmd.Env = WithEnvValue(Env(token), "PWD", dir)
	// A run without a checkout must not discover the platform's own .git
	// when the operator keeps workspaces below a source checkout.
	if absolute, err := filepath.Abs(dir); err == nil {
		cmd.Env = WithEnvValue(cmd.Env, "GIT_CEILING_DIRECTORIES", filepath.Dir(absolute))
	}
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
