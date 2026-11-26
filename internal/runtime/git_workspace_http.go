package runtime

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"agentflow/internal/gitwork"
)

const (
	workspaceGitHTTPBin          = "http-bin"
	workspaceGitDistribution     = "distribution"
	workspaceGitBinsEnv          = "AGENTFLOW_WORKSPACE_GIT_BINS"
	workspaceGitHTTPOriginEnv    = "AGENTFLOW_GIT_HTTP_ORIGIN"
	workspaceGitHTTPSystemGitEnv = "AGENTFLOW_GIT_HTTP_SYSTEM_GIT"
)

// Git invokes git-remote-http with the effective URL, after resolving remotes
// and URL rewrites. Preserve the installed helpers and override only HTTP.
func workspaceGitMain(args []string) int {
	installed, err := workspaceGitDistributionPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 126
	}
	env := gitwork.WithEnvValue(os.Environ(), "GIT_EXEC_PATH", filepath.Join(installed, "exec"))
	return execWorkspaceGit(filepath.Join(installed, "git"), args, env)
}

func workspaceGitDistributionPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(filepath.Dir(self)), workspaceGitDistribution), nil
}

func workspaceGitHTTPMain(args []string) int {
	installed, err := workspaceGitDistributionPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 126
	}
	env := gitwork.WithEnvValue(os.Environ(), workspaceGitHTTPOriginEnv, "")
	if len(args) > 0 {
		remote, err := url.Parse(args[len(args)-1])
		if err == nil && remote.Scheme == "http" {
			if scope := workspacePlainHTTPHost(remote.Host); scope != "" {
				env = gitwork.WithPlainHTTPRedirectsDisabled(env, remote)
				env = gitwork.WithEnvValue(env, workspaceGitHTTPOriginEnv, scope)
			}
		}
	}
	return execWorkspaceGit(filepath.Join(installed, "git-remote-http"), args, env)
}

func workspacePlainHTTPHost(authority string) string {
	scope, ok := gitwork.GitCredentialScope("http", authority)
	if ok && strings.Contains(","+os.Getenv(GitHostsEnv)+",", ","+scope+"=other,") {
		return scope
	}
	return ""
}

func execWorkspaceGit(path string, args, env []string) int {
	if err := syscall.Exec(path, append([]string{path}, args...), env); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "git: executable not found: %s\n", path)
			return 127
		}
		fmt.Fprintf(os.Stderr, "git: execute %s: %v\n", path, err)
		return 126
	}
	return 0
}
