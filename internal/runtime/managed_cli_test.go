package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
)

func TestExecutorRunnerPinsManagedNativeCLI(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", root)
	t.Setenv("AGENTFLOW_PI_BIN", "")
	publish := func(version string) string {
		t.Helper()
		binary := filepath.Join(root, "providers", "pi", version, "pi")
		if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		state := managedcli.State{Current: managedcli.Inventory{"pi": {Version: version, Binary: binary}}}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "state.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		return binary
	}
	runner := NewExecutorRunner()
	check := func(driver, runID, binary, want string) {
		t.Helper()
		sandbox := &capturingSandbox{workspace: t.TempDir(), driver: driver}
		executor := domain.ExecutorDefinition{Kind: "pi", Binary: binary}
		agent := domain.AgentDefinition{ID: "agent"}
		if _, err := runner.Run(context.Background(), sandbox, "invocation", executor, agent, domain.AgentAssetBundle{}, "hello", InvocationOptions{RunID: runID}, nil); err != nil {
			t.Fatal(err)
		}
		if len(sandbox.argv) == 0 || sandbox.argv[0] != want {
			t.Fatalf("%s run %q got %v, want %s", driver, runID, sandbox.argv, want)
		}
	}
	check("local", "legacy", "pi", "pi")
	first := publish("1.5.2")
	check("local", "legacy", "pi", "pi")
	check("local", "run1", "pi", first)
	second := publish("1.6.0")
	check("local", "run1", "pi", first)
	check("seatbelt", "run2", "", second)
	check("bwrap", "run3", "pi", second)
	override := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(override, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_PI_BIN", override)
	check("docker", "run4", "pi", "pi")
	check("local", "run5", "/custom/bin/pi", "/custom/bin/pi")
	check("local", "run6", "pi", override)
}

func TestBwrapMountsManagedInstallationUnderPrivateTmp(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bwrap is available on Linux")
	}
	t.Setenv("TMPDIR", "/tmp")
	root := t.TempDir()
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", root)
	installation := filepath.Join(root, "providers", "pi", "1.5.2-test")
	binary := filepath.Join(installation, "node_modules", ".bin", "pi")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(installation, "pi.cjs")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../pi.cjs", binary); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	prepared, err := NewExecutorRunner().PrepareRemote(workspace, RemoteInvocation{
		InvocationID: "invocation", Executor: domain.ExecutorDefinition{Kind: "pi", Binary: binary},
		Agent: domain.AgentDefinition{ID: "agent"}, Driver: "bwrap", Prompt: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &processSandbox{workspace: workspace, driver: "bwrap", binary: "bwrap"}
	command, err := sandbox.command(context.Background(), prepared.Command, nil)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(command.Args, "\x00")
	privateTmp := strings.Index(args, "\x00--tmpfs\x00/tmp\x00")
	bind := "\x00--ro-bind\x00" + installation + "\x00" + installation + "\x00"
	if index := strings.Index(args, bind); privateTmp < 0 || index <= privateTmp {
		t.Fatalf("expected a read-only installation bind after the private /tmp mount: %q", command.Args)
	}
	if strings.Contains(args, "\x00--ro-bind\x00"+root+"\x00") {
		t.Fatal("bwrap exposed the entire updater directory")
	}
}

func TestBwrapUsesCanonicalManagedPathWhenTemporarySymlinkIsHidden(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bwrap is available on Linux")
	}
	storage, err := os.MkdirTemp("/var/tmp", "loopforge-managed-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storage) })
	t.Setenv("TMPDIR", "/tmp")
	alias := filepath.Join(t.TempDir(), "storage")
	if err := os.Symlink(storage, alias); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(alias, "managed")
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", root)
	relative := filepath.Join("providers", "pi", "1.5.2-test", "pi")
	binary := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{binary, "--version"}
	command := withBwrapManagedCLI(CommandSpec{Provider: "pi", Argv: args}, "bwrap")
	want := filepath.Join(storage, "managed", relative)
	if command.Argv[0] != want || command.managedCLIInstall != "" {
		t.Fatalf("expected the visible path %s without another mount, got %q and %q", want, command.Argv[0], command.managedCLIInstall)
	}
	if args[0] != binary {
		t.Fatal("managed CLI path normalization changed the caller's arguments")
	}
}
