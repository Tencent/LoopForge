package runtime

import (
	"path/filepath"
	"slices"

	"agentflow/internal/gitwork"
)

// BindGitSSH keeps the SSH agent alive for exactly one command. Only the
// public configuration and socket are visible in the sandbox's control dir.
func BindGitSSH(sandbox Sandbox, workspace string, environment map[string]string, keys map[string]string) (func(), error) {
	if len(keys) == 0 {
		return func() {}, nil
	}
	root, err := EnsureControlDirectory(workspace, "runtime")
	if err != nil {
		return nil, err
	}
	clientRoot := ""
	if sandboxDriver(sandbox) == "docker" {
		clientRoot = "/workspace/.agentflow/runtime"
	}
	environment["GIT_ALLOW_PROTOCOL"] = gitwork.AllowedProtocols
	return gitwork.StartSSH(environment, keys, root, clientRoot)
}

func bindCommandGitSSH(sandbox Sandbox, workspace string, command CommandSpec, credentials RunCredentials) (func(), error) {
	if !slices.Contains(command.CredentialBindings, BindGit) {
		return func() {}, nil
	}
	return BindGitSSH(sandbox, filepath.Clean(workspace), command.environment, credentials.GitSSHKeys)
}
