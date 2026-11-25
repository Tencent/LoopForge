package runtime

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/procwatch"
)

func SandboxCheckouts(ctx context.Context, sandbox Sandbox) ([]gitwork.Checkout, error) {
	ctx = WithSandboxGit(ctx, sandbox, RunCredentials{})
	if _, err := gitwork.Run(ctx, sandbox.Workspace(), "--version"); err != nil {
		return nil, fmt.Errorf("check Git in the SOP sandbox: %w", err)
	}
	return gitwork.CheckoutsIncludingNested(ctx, sandbox.Workspace()), ctx.Err()
}

// WithSandboxGit routes Git workspace preparation through the selected SOP
// sandbox. Resolve guest paths relative to the checkout owner's workspace.
func WithSandboxGit(ctx context.Context, sandbox Sandbox, credentials RunCredentials) context.Context {
	return gitwork.WithWorkspaceExecutor(ctx, func(ctx context.Context, dir string, git gitwork.WorkspaceCommand) (string, error) {
		// Automated checkout uses Git authentication scoped to the remote.
		ctx = context.WithValue(ctx, projectCredentialEnvKey{}, nil)
		if sandbox == nil {
			return "", fmt.Errorf("shared checkout preparation requires the SOP sandbox")
		}
		workspace := sandbox.Workspace()
		rel, err := filepath.Rel(workspace, dir)
		if err != nil || !filepath.IsLocal(rel) {
			return "", fmt.Errorf("Git directory is outside the SOP workspace")
		}
		guestDir := dir
		docker := sandboxDriver(sandbox) == domain.SandboxDriverDocker
		if docker {
			guestDir = path.Join("/workspace", filepath.ToSlash(rel))
		}
		command := CommandSpec{
			WorkingDir: guestDir, Provider: "git", Stdin: git.Stdin, OwnProcessGroup: !docker,
			Argv: append([]string{"git", "-c", "safe.directory=*", "-c", "core.hooksPath=" + os.DevNull,
				"-c", "core.fsmonitor=", "-c", "core.pager=cat", "-c", "core.editor=true", "-c", "core.askPass="}, git.Args...),
		}
		execution := unwrapSandbox(sandbox)
		environment := map[string]string{
			"GIT_TERMINAL_PROMPT": "0", "GIT_ALLOW_PROTOCOL": gitwork.AllowedProtocols,
			"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_COUNT": "1",
			"GIT_CONFIG_KEY_0": "credential.helper", "GIT_CONFIG_VALUE_0": "",
			"GIT_OPTIONAL_LOCKS": "0", "GIT_PAGER": "cat", "GIT_SSH_COMMAND": "false",
			"GIT_CEILING_DIRECTORIES": path.Dir(filepath.ToSlash(guestDir)),
		}
		for _, key := range workspaceGitSecretEnvironment {
			environment[key] = ""
		}
		if len(git.Args) > 0 {
			switch git.Args[0] {
			case "clone", "fetch", "pull", "push", "ls-remote":
				remote := gitwork.WorkspaceRemote(ctx, git.Args)
				if host, ok := gitwork.HTTPSRemoteHost(remote); ok && credentials.GitHTTPCredentials[host].Token != "" {
					scoped := RunCredentials{GitHTTPCredentials: map[string]domain.GitHTTPExecutionCredential{host: credentials.GitHTTPCredentials[host]}}
					gitEnv, stop, err := RunGitEnvironment(ctx, execution, workspace, scoped)
					if err != nil {
						return "", err
					}
					defer stop()
					maps.Copy(environment, gitEnv)
				}
				if gitwork.IsSSHReference(remote) || gitwork.UsesSSH(ctx) {
					key := credentials.GitSSHKeys[remote]
					if key == "" {
						return "", fmt.Errorf("%s: %w", remote, gitwork.ErrSSHKeyRequired)
					}
					stop, err := BindGitSSH(ctx, execution, workspace, environment, map[string]string{remote: key})
					if err != nil {
						return "", err
					}
					defer stop()
					environment["GIT_ALLOW_PROTOCOL"] = "ssh"
				}
			}
		}
		command = WithCommandEnvironment(command, environment)
		if git.Watch != nil {
			watch := *git.Watch
			redact := watch.Redact
			watch.Redact = func(line string) string {
				line = redactRunGitSecrets(line, credentials)
				if redact != nil {
					return redact(line)
				}
				return line
			}
			return procwatch.RunWithOutput(ctx, func(ctx context.Context, out io.Writer) error {
				command.observeOutput = func(_ string, data []byte) { _, _ = out.Write(data) }
				_, err := execution.Exec(ctx, command, nil)
				return err
			}, watch)
		}
		var stdout strings.Builder
		if git.StdoutOnly {
			command.observeOutput = func(stream string, data []byte) {
				if stream == "process.stdout" {
					stdout.Write(data)
				}
			}
		}
		out, err := execution.Exec(ctx, command, nil)
		out = redactRunGitSecrets(out, credentials)
		if err != nil && strings.TrimSpace(out) != "" {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(out))
		}
		if git.StdoutOnly {
			return redactRunGitSecrets(stdout.String(), credentials), err
		}
		return out, err
	})
}
