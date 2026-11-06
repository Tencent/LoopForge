package runtime

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"agentflow/internal/domain"
)

func TestCodexRunsUnattendedOnEveryDriver(t *testing.T) {
	ex := domain.ExecutorDefinition{Binary: "codex", Args: []string{"exec", "--json", "--sandbox", "workspace-write", "--skip-git-repo-check", "-a", "on-request", "--full-auto"}}
	opts := InvocationOptions{ResumeSessionID: "sess-1"}
	docker, err := codexProvider{}.Prepare(providerInvocation{Executor: ex, Prompt: "task", Driver: "docker", Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(docker.Argv, " ")
	for _, banned := range []string{"--sandbox", "workspace-write", "-a on-request", "--full-auto"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("docker argv kept %q: %v", banned, docker.Argv)
		}
	}
	bypass := indexOf(docker.Argv, "--dangerously-bypass-approvals-and-sandbox")
	resume := indexOf(docker.Argv, "resume")
	if bypass < 0 || resume < 0 || bypass > resume || docker.Argv[len(docker.Argv)-1] != "task" {
		t.Fatalf("bypass must precede resume and the prompt: %v", docker.Argv)
	}

	local, err := codexProvider{}.Prepare(providerInvocation{Executor: ex, Prompt: "task", Driver: "local"})
	if err != nil {
		t.Fatal(err)
	}
	// Aligned with CodeBuddy -y: no driver can answer approvals.
	if hasArg(local.Argv, "workspace-write") || !hasArg(local.Argv, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("local driver must run Codex unattended too: %v", local.Argv)
	}
}

func indexOf(values []string, wanted string) int {
	for i, value := range values {
		if value == wanted {
			return i
		}
	}
	return -1
}

func TestProcessWriterCollapsesCodexStreamNoise(t *testing.T) {
	var lines []string
	sink := func(event domain.TelemetryEvent) { lines = append(lines, event.Message) }
	stderr := &processWriter{output: &bytes.Buffer{}, mu: new(sync.Mutex), sink: sink, name: "process.stderr", level: "error"}
	_, _ = stderr.Write([]byte("2026-11-06T08:20:46Z ERROR codex_core::util: ReasoningSummaryDelta without active item\n" +
		"2026-11-06T08:20:46Z ERROR codex_core::util: OutputTextDelta without active item\n" +
		"real failure\n" +
		"2026-11-06T08:20:47Z ERROR codex_core::util: OutputTextDelta without active item\n"))
	if len(lines) != 2 || lines[0] != codexStreamNoticeLine || lines[1] != "real failure" {
		t.Fatalf("stderr lines = %q", lines)
	}
}
