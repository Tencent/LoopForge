package runtime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestOpenRemoteSandboxUsesExactRunnerWorkspace(t *testing.T) {
	workspace := t.TempDir()
	sandbox, err := OpenRemoteSandbox(context.Background(), "runner-task", workspace, domain.SandboxProfile{Driver: "local"}, SandboxRequirements{}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close(context.Background())
	if sandbox.Workspace() != workspace {
		t.Fatalf("workspace = %q, want %q", sandbox.Workspace(), workspace)
	}
	command := WithCommandEnvironment(CommandSpec{Argv: []string{"sh", "-lc", `printf '%s' "$RUNNER_SANDBOX_MARKER"`}}, map[string]string{"RUNNER_SANDBOX_MARKER": "runner-owned"})
	output, err := sandbox.Exec(context.Background(), command, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output) != "runner-owned" {
		t.Fatalf("output = %q", output)
	}
}

func TestWithSandboxCredentialsRefreshesExplicitBindings(t *testing.T) {
	workspace := t.TempDir()
	original := &localSandbox{workspace: workspace}
	sandbox := WithSandboxCredentials(original, RunCredentials{GitHTTPCredentials: testHTTPSCredentials("first-owned-token")})
	command := CommandSpec{Argv: []string{"sh", "-c", `test -z "$AGENTFLOW_GIT_TOKEN" && printf 'protocol=https\nhost=github.com\n\n' | git credential fill | python3 -c 'import sys,hashlib; c=dict(l.strip().split("=",1) for l in sys.stdin if "=" in l); print(hashlib.sha256(c["password"].encode()).hexdigest())'`}, CredentialBindings: []CredentialBinding{BindGit}}
	if output, err := sandbox.Exec(context.Background(), command, nil); err != nil || strings.TrimSpace(output) != fmt.Sprintf("%x", sha256.Sum256([]byte("first-owned-token"))) {
		t.Fatalf("explicit credential binding failed: %q %v", output, err)
	}
	refreshed := WithSandboxCredentials(sandbox, RunCredentials{GitHTTPCredentials: testHTTPSCredentials("next-owned-token")})
	if refreshed != sandbox {
		t.Fatal("already managed sandbox nested instead of refreshing credentials")
	}
	if output, err := refreshed.Exec(context.Background(), command, nil); err != nil || strings.TrimSpace(output) != fmt.Sprintf("%x", sha256.Sum256([]byte("next-owned-token"))) {
		t.Fatalf("stale nested binding: %q %v", output, err)
	}
	if output, err := refreshed.Exec(context.Background(), CommandSpec{Argv: command.Argv}, nil); err == nil {
		t.Fatalf("unrequested credentials inherited: %q %v", output, err)
	}
}

func TestCodeBuddyBaseURLIsBoundPerInvocation(t *testing.T) {
	t.Setenv("CODEBUDDY_BASE_URL", "https://ambient.example/v1")
	const gateway = "https://personal.example/v1"
	command := CommandSpec{Argv: []string{"sh", "-c", `printf '%s' "$CODEBUDDY_BASE_URL"`}, Provider: "codebuddy", CredentialBindings: []CredentialBinding{BindCodeBuddy}}
	for _, baseURL := range []string{gateway, ""} {
		credentials := RunCredentials{CodeBuddy: CodeBuddyCredential{APIKey: "fixture-key", BaseURL: baseURL}}
		output, err := ExecuteLocalCommand(context.Background(), t.TempDir(), command, credentials, nil)
		if err != nil || strings.TrimSpace(output) != baseURL {
			t.Fatalf("CodeBuddy received %q, want %q: %v", output, baseURL, err)
		}
	}
}
