package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
)

func TestExecutorRunnerPinsManagedNativeCLI(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", root)
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
	check("docker", "run4", "pi", "pi")
	check("local", "run5", "/custom/bin/pi", "/custom/bin/pi")
}
