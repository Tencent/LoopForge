//go:build linux || darwin

package runner

import (
	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedExecutionUsesPinnedBinaryForSimpleAndWorkflowTasks(t *testing.T) {
	for _, provider := range []string{"claude-code", "codebuddy", "codex"} {
		for _, workflow := range []bool{false, true} {
			name := provider + "/simple"
			if workflow {
				name = provider + "/workflow"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("DISABLE_AUTOUPDATER", "user-auto")
				t.Setenv("DISABLE_UPDATES", "user-updates")
				root := t.TempDir()
				first := managedTestBinary(t, root, provider, "1.0.0")
				second := managedTestBinary(t, root, provider, "2.0.0")
				writeManagedTestInventory(t, root, managedcli.Inventory{provider: {Version: "1.0.0", Binary: first}})
				tasks := []domain.RunnerTask{{ID: "one", RunID: "same-run", Provider: provider, Prompt: "test"}, {ID: "two", RunID: "same-run", Provider: provider, Prompt: "test"}, {ID: "three", RunID: "new-run", Provider: provider, Prompt: "test"}}
				if workflow {
					for i := range tasks {
						inv := &domain.RunnerInvocation{}
						inv.InvocationID = tasks[i].ID
						inv.RunID = tasks[i].RunID
						inv.Executor.ID = provider
						inv.Executor.Kind = provider
						inv.Executor.Binary = defaultProviderBinary(provider)
						inv.Agent.ID = "agent"
						inv.CredentialSource = domain.RuntimeAuthLocal
						inv.CodeBuddyAPIKey = "test-only-key"
						tasks[i].Invocation = inv
					}
				}
				d, _, completed := managedTestDaemon(t, root, tasks, nil, nil)
				// Legacy preflight can itself lease work and needs the same protection.
				d.cfg.DisableAutoUpdate = false
				for i := range tasks {
					if i == 1 {
						writeManagedTestInventory(t, root, managedcli.Inventory{provider: {Version: "2.0.0", Binary: second}})
						d.runtimes = []domain.RunnerRuntime{{Provider: provider, BinaryPath: second}}
					}
					task, found, err := d.claimTask(context.Background(), []string{provider}, 1)
					if err != nil || !found || task == nil {
						t.Fatalf("claim: found=%v err=%v", found, err)
					}
					d.executeTask(context.Background(), *task)
					req := <-completed
					if req.Status != domain.RunnerTaskSuccess {
						t.Fatalf("completion=%+v", req)
					}
					raw, err := os.ReadFile(filepath.Join(d.taskWorkspace(*task), "execution-marker"))
					if err != nil {
						t.Fatal(err)
					}
					wantBinary := first
					if i == 2 {
						wantBinary = second
					}
					wantEnv := "1|1"
					if provider == "codex" {
						wantEnv = "user-auto|user-updates"
					}
					if string(raw) != wantBinary+"|"+wantEnv {
						t.Fatalf("actual child=%q, want %q", raw, wantBinary+"|"+wantEnv)
					}
					if task.Invocation != nil && task.Invocation.Executor.Binary != defaultProviderBinary(provider) {
						t.Fatal("mutated task executor instead of execution copy")
					}
				}
				if os.Getenv("DISABLE_AUTOUPDATER") != "user-auto" || os.Getenv("DISABLE_UPDATES") != "user-updates" {
					t.Fatal("changed daemon environment")
				}
			})
		}
	}
}
