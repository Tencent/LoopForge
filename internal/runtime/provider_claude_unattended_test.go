package runtime

import (
	"testing"

	"agentflow/internal/domain"
)

func TestClaudeBypassesPermissionsOnEveryDriver(t *testing.T) {
	ex := domain.ExecutorDefinition{Binary: "claude", Args: []string{"-p", "--output-format", "stream-json", "--permission-mode", "dontAsk"}}
	docker, err := claudeProvider{}.Prepare(providerInvocation{Executor: ex, Prompt: "task", Driver: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	mode := indexOf(docker.Argv, "--permission-mode")
	if mode < 0 || docker.Argv[mode+1] != "bypassPermissions" || hasArg(docker.Argv, "dontAsk") {
		t.Fatalf("docker argv = %v", docker.Argv)
	}
	if docker.environment["IS_SANDBOX"] != "1" || docker.Argv[len(docker.Argv)-1] != "task" {
		t.Fatalf("docker env = %v argv = %v", docker.environment, docker.Argv)
	}

	local, err := claudeProvider{}.Prepare(providerInvocation{Executor: ex, Prompt: "task", Driver: "local"})
	if err != nil {
		t.Fatal(err)
	}
	// Aligned with CodeBuddy -y: dontAsk without allow rules denies Write/Bash.
	if hasArg(local.Argv, "dontAsk") || !hasArg(local.Argv, "bypassPermissions") || local.environment["IS_SANDBOX"] != "1" {
		t.Fatalf("local driver must bypass permissions too: %v %v", local.Argv, local.environment)
	}
}
