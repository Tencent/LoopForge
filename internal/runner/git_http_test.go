package runner

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestMain(m *testing.M) {
	if runtime.RunMCPCallCLI(os.Args[1:]) || runtime.RunGitHelperCLI(os.Args[1:]) {
		return
	}
	cleanup := repo.UseSandboxVisibleTempDir()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func TestRunnerGitHelperUsesInvocationHostCredentials(t *testing.T) {
	inv := &domain.RunnerInvocation{GitHTTPCredentials: map[string]domain.GitHTTPExecutionCredential{
		"unfamiliar.example.org": {GitHTTPCredential: domain.GitHTTPCredential{Host: "unfamiliar.example.org", Username: "required-user"}, Token: "fake-runner-token"},
	}, Repository: &domain.RunnerRepository{GitToken: "obsolete-token-must-not-answer"}}
	workspace := t.TempDir()
	env, closeRelay, err := runtime.RunGitEnvironment(context.Background(), nil, workspace, invocationGitCredentials(inv))
	if err != nil {
		t.Fatal(err)
	}
	defer closeRelay()
	for _, host := range []string{"unfamiliar.example.org", "other.example.org"} {
		cmd := exec.Command("git", "credential", "fill")
		cmd.Dir, cmd.Env = workspace, os.Environ()
		for k, v := range env {
			cmd.Env = gitwork.WithEnvValue(cmd.Env, k, v)
		}
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
		out, err := cmd.CombinedOutput()
		if host == "unfamiliar.example.org" {
			if err != nil || !strings.Contains(string(out), "username=required-user\npassword=fake-runner-token") {
				t.Fatalf("configured host failed: %v %s", err, out)
			}
		} else if err == nil || strings.Contains(string(out), "password=") {
			t.Fatal("unconfigured host received a credential")
		}
	}
	for k, v := range env {
		if strings.Contains(v, "fake-runner-token") || strings.Contains(v, "obsolete-token-must-not-answer") {
			t.Fatalf("credential leaked into %s", k)
		}
	}
}
