package runtime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
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

func TestOpenRemoteSandboxInstallsEnvironmentBeforeCommands(t *testing.T) {
	workspace := t.TempDir()
	profile := domain.SandboxProfile{Driver: "local", EnvironmentID: "runner-tools"}
	resources := SandboxResources{Environment: domain.EnvironmentDefinition{ID: "runner-tools", InstallScript: `mkdir -p "$PREFIX/bin"
printf '#!/bin/sh\nprintf runner-ready' > "$PREFIX/bin/runner-ready"
chmod +x "$PREFIX/bin/runner-ready"
printf x >> install-count`}}
	for i := 0; i < 2; i++ {
		sandbox, err := OpenRemoteSandbox(context.Background(), "runner-task", workspace, profile, SandboxRequirements{}, resources)
		if err != nil {
			t.Fatal(err)
		}
		output, err := sandbox.Exec(context.Background(), CommandSpec{Argv: []string{"sh", "-c", "runner-ready"}}, nil)
		if err != nil || strings.TrimSpace(output) != "runner-ready" {
			t.Fatalf("environment not installed: %q %v", output, err)
		}
		_ = sandbox.Close(context.Background())
	}
	if body, err := os.ReadFile(filepath.Join(workspace, "install-count")); err != nil || string(body) != "x" {
		t.Fatalf("unchanged environment installed repeatedly: %q %v", body, err)
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
