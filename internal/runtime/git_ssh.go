package runtime

import (
	"os"
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

// terminalGitSSHUnavailable is the GIT_SSH_COMMAND of a terminal whose sandbox
// cannot keep the requester's SSH agent private to that one terminal.
const terminalGitSSHUnavailable = `sh -c 'echo "当前工作区环境无法把个人 Git SSH 密钥只开放给这个终端，终端内不能直接用 SSH 访问仓库；请使用平台的提交/推送" >&2; exit 1' --`

// BindTerminalGitSSH gives one interactive terminal its requester's SSH
// identity. Unlike BindGitSSH, the agent socket never lives in the workspace:
// the workspace is shared by every terminal and Agent of the run, all running
// as the same uid, so a socket there would let anyone sign as the requester.
//
//   - bwrap: the session lives in a fresh 0700 directory under the host /tmp.
//     Every bwrap process gets its own tmpfs /tmp, and only this terminal's
//     process binds the directory back in.
//   - local: no filesystem isolation exists; the session at least stays
//     outside the workspace in a 0700 directory.
//   - docker, seatbelt and anything else share one filesystem view among all
//     processes of the workspace, so the requester's key is not offered and
//     SSH remotes fail with a hint to use the platform's submit/push.
func BindTerminalGitSSH(sandbox Sandbox, command *CommandSpec, keys map[string]string) (func(), error) {
	if len(keys) == 0 {
		return func() {}, nil
	}
	if command.environment == nil {
		command.environment = map[string]string{}
	}
	command.environment["GIT_ALLOW_PROTOCOL"] = gitwork.AllowedProtocols
	driver := sandboxDriver(sandbox)
	if driver != "bwrap" && driver != "local" {
		command.environment["GIT_SSH_VARIANT"] = "ssh"
		command.environment["GIT_SSH_COMMAND"] = terminalGitSSHUnavailable
		return func() {}, nil
	}
	root, err := os.MkdirTemp("/tmp", "lf-term-ssh-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	stop, err := gitwork.StartSSH(command.environment, keys, root, "")
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if driver == "bwrap" {
		command.privateBinds = append(command.privateBinds, root)
	}
	return func() {
		stop()
		_ = os.RemoveAll(root)
	}, nil
}
