package runtime

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"agentflow/internal/gitwork"
)

const (
	workspaceGitBin           = "git-bin"
	workspaceGitExec          = "git-exec"
	workspaceGitRealExecEnv   = "AGENTFLOW_GIT_REAL_EXEC_PATH"
	workspaceGitHTTPOriginEnv = "AGENTFLOW_GIT_HTTP_ORIGIN"
)

// Git invokes git-remote-http with the effective URL, after resolving remotes
// and URL rewrites. Keep the system helpers on PATH and override only HTTP.
func workspaceGitMain(args []string) int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 126
	}
	base := filepath.Dir(filepath.Dir(self))
	path := os.Getenv("PATH")
	git, err := lookPathSkipping("git", filepath.Join(base, workspaceGitBin), path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "git: command not found in this workspace")
		return 127
	}
	if os.Getenv(workspaceGitRealExecEnv) != "" || (len(args) == 1 && args[0] == "--exec-path") {
		return execWorkspaceGit(git, args, os.Environ())
	}
	output, err := exec.Command(git, "--exec-path").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "git: read the transport helper path: %v\n", err)
		return 126
	}
	realExec := strings.TrimSpace(string(output))
	env := gitwork.WithEnvValue(os.Environ(), workspaceGitRealExecEnv, realExec)
	env = gitwork.WithEnvValue(env, "GIT_EXEC_PATH", filepath.Join(base, workspaceGitExec))
	env = gitwork.WithEnvValue(env, "PATH", realExec+string(os.PathListSeparator)+path)
	return execWorkspaceGit(git, args, env)
}

func workspaceGitHTTPMain(args []string) int {
	transport, err := lookPathSkipping("git-remote-http", os.Getenv("GIT_EXEC_PATH"), os.Getenv("PATH"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "git-remote-http: command not found in this workspace")
		return 127
	}
	env := gitwork.WithEnvValue(os.Environ(), "GIT_EXEC_PATH", os.Getenv(workspaceGitRealExecEnv))
	env = gitwork.WithEnvValue(env, workspaceGitHTTPOriginEnv, "")
	if len(args) > 0 {
		remote, err := url.Parse(args[len(args)-1])
		if err == nil && remote.Scheme == "http" {
			if scope := workspacePlainHTTPHost(remote.Host); scope != "" {
				count, _ := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
				index := strconv.Itoa(count)
				env = gitwork.WithEnvValue(env, "GIT_CONFIG_COUNT", strconv.Itoa(count+1))
				env = gitwork.WithEnvValue(env, "GIT_CONFIG_KEY_"+index, gitwork.PlainHTTPRedirectConfig("http://"+remote.Host))
				env = gitwork.WithEnvValue(env, "GIT_CONFIG_VALUE_"+index, "false")
				env = gitwork.WithEnvValue(env, workspaceGitHTTPOriginEnv, scope)
			}
		}
	}
	return execWorkspaceGit(transport, args, env)
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
		fmt.Fprintln(os.Stderr, err)
		return 126
	}
	return 0
}
