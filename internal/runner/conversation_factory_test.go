package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
	"agentflow/internal/runtime"
)

func TestConversationCLIBinaryResolutionOrder(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	inventory := managedcli.Inventory{"codex": {Binary: "/managed/codex"}}
	runtimes := []domain.RunnerRuntime{{Provider: "codex", BinaryPath: "/detected/codex"}}

	for _, tc := range []struct {
		name      string
		extra     map[string]string
		inventory managedcli.Inventory
		runtimes  []domain.RunnerRuntime
		want      string
	}{
		{"explicit binary wins", map[string]string{"codex": "  /explicit/codex  "}, inventory, runtimes, "/explicit/codex"},
		{"managed inventory before runtimes", nil, inventory, runtimes, "/managed/codex"},
		{"detected runtime fallback", map[string]string{"codex": "   "}, nil, runtimes, "/detected/codex"},
		{"disabled runtime is skipped", nil, inventory, []domain.RunnerRuntime{{Provider: "codex", BinaryPath: "/detected/codex", Status: domain.RuntimeStatusDisabled}}, "/managed/codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Daemon{cfg: Config{ExtraBinaries: tc.extra}, runtimes: tc.runtimes}
			if got := d.conversationCLIBinary("codex", tc.inventory); got != tc.want {
				t.Fatalf("conversationCLIBinary() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConversationCLIBinaryEmptyWhenNothingResolves(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	d := &Daemon{runtimes: []domain.RunnerRuntime{{Provider: "missing-cli", BinaryPath: "/detected/missing", Status: domain.RuntimeStatusDisabled}}}
	if got := d.conversationCLIBinary("missing-cli", nil); got != "" {
		t.Fatalf("conversationCLIBinary() = %q, want empty", got)
	}
}

func TestConversationCLIBinaryFallsBackToPATH(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir())
	d := &Daemon{}
	if got := d.conversationCLIBinary("codex", nil); got != binary {
		t.Fatalf("conversationCLIBinary() = %q, want %q", got, binary)
	}
}

func TestHostConversationFactoryRewritesArgv(t *testing.T) {
	d := &Daemon{cfg: Config{ExtraBinaries: map[string]string{"codex": "/explicit/codex"}}}
	command := &runtime.ConversationCommand{Provider: "codex", Argv: []string{"codex", "exec"}}
	sb, release, err := d.hostConversationFactory(command)(context.Background(), "")
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if sb != nil {
		t.Fatalf("host factory returned sandbox %v, want nil", sb)
	}
	if release == nil {
		t.Fatal("host factory returned nil release")
	}
	release()
	if command.Argv[0] != "/explicit/codex" || command.Argv[1] != "exec" {
		t.Fatalf("argv = %v, want [/explicit/codex exec]", command.Argv)
	}
}

func TestHostConversationFactoryReportsMissingCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	d := &Daemon{}
	command := &runtime.ConversationCommand{Provider: "missing-cli", Argv: []string{"missing-cli"}}
	sb, release, err := d.hostConversationFactory(command)(context.Background(), "")
	if err == nil || err.Error() != "执行机器未安装 CLI missing-cli" {
		t.Fatalf("err = %v, want missing CLI error", err)
	}
	if sb != nil || release != nil {
		t.Fatalf("got sandbox %v release %v, want nil on error", sb, release != nil)
	}
	if command.Argv[0] != "missing-cli" {
		t.Fatalf("argv rewritten on error: %v", command.Argv)
	}
}
