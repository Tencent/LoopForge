package runner

import (
	"context"
	"fmt"
	"log"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
)

// managedTask retains pin failures so already-claimed tasks are completed as
// failures, never discarded as claim errors. The usage lock spans completion.
type managedTask struct {
	lease     *managedcli.ClaimLease
	inventory managedcli.Inventory
	err       error
}

func (d *Daemon) claimTask(ctx context.Context, providers []string, capacity int) (*domain.RunnerTask, bool, error) {
	if d.cfg.ManagedCLIRoot == "" {
		return d.claimTaskUncoordinated(ctx, providers, capacity)
	}
	lease, err := managedcli.BeginClaim(ctx, d.cfg.ManagedCLIRoot)
	if err != nil {
		return nil, false, fmt.Errorf("managed CLI claim coordination: %w", err)
	}
	task, found, err := d.claimTaskUncoordinated(ctx, providers, capacity)
	if err != nil || !found || task == nil {
		lease.Close()
		return task, found, err
	}
	// Legacy WaitForTask can claim; both it and Claim retain the usage lease.
	state := &managedTask{lease: lease}
	state.inventory, state.err = managedcli.PinInventory(d.cfg.ManagedCLIRoot, d.managedRunKey(*task))
	d.mu.Lock()
	if d.managedTasks == nil {
		d.managedTasks = make(map[string]*managedTask)
	}
	d.managedTasks[task.ID] = state
	d.mu.Unlock()
	return task, true, nil
}

func (d *Daemon) managedRunKey(task domain.RunnerTask) string {
	runID := task.RunID
	if runID == "" && task.Invocation != nil {
		runID = task.Invocation.RunID
	}
	if runID == "" {
		runID = task.ID
	}
	return d.runnerID() + ":" + runID
}

func (d *Daemon) managedTaskState(taskID string) *managedTask {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.managedTasks[taskID]
}

func defaultProviderBinary(provider string) string {
	for _, spec := range defaultProviders {
		if spec.Kind == provider {
			return spec.Binary
		}
	}
	return ""
}

func (d *Daemon) managedBinary(task domain.RunnerTask) (string, bool) {
	if d.cfg.ManagedCLIRoot == "" || strings.EqualFold(runnerTaskSandboxDriver(task), "docker") {
		return "", false
	}
	if strings.TrimSpace(d.cfg.ExtraBinaries[task.Provider]) != "" {
		return "", false
	}
	if task.Invocation != nil {
		binary := strings.TrimSpace(task.Invocation.Executor.Binary)
		if binary != "" && binary != defaultProviderBinary(task.Provider) {
			return "", false
		}
	}
	state := d.managedTaskState(task.ID)
	if state == nil || state.err != nil {
		return "", false
	}
	return state.inventory.BinaryFor(task.Provider)
}

// Do not fall back to refreshed managed discovery: a run pinned to an empty
// inventory must remain on the legacy host path after a newer activation.
func (d *Daemon) taskProviderBinary(task domain.RunnerTask) (string, bool) {
	if binary, ok := d.managedBinary(task); ok {
		return binary, true
	}
	if d.cfg.ManagedCLIRoot != "" {
		binary := defaultProviderBinary(task.Provider)
		if strings.EqualFold(runnerTaskSandboxDriver(task), "docker") {
			return binary, binary != ""
		}
		if explicit := strings.TrimSpace(d.cfg.ExtraBinaries[task.Provider]); explicit != "" {
			binary = explicit
		}
		if binary != "" {
			return lookupCLI(binary)
		}
	}
	return providerBinaryPath(d.currentRuntimes(), task.Provider)
}

func managedCLIEnvironment(provider string) map[string]string {
	if provider == "claude-code" || provider == "codebuddy" {
		return map[string]string{"DISABLE_AUTOUPDATER": "1", "DISABLE_UPDATES": "1"}
	}
	return nil
}

func (d *Daemon) taskManagedEnvironment(task domain.RunnerTask) map[string]string {
	if _, managed := d.managedBinary(task); managed {
		return managedCLIEnvironment(task.Provider)
	}
	return nil
}

func (d *Daemon) invocationExecutor(task domain.RunnerTask) domain.ExecutorDefinition {
	executor := task.Invocation.Executor
	if binary, ok := d.managedBinary(task); ok {
		executor.Binary = binary
	}
	return executor
}

func (d *Daemon) detectManagedRuntimes(ctx context.Context) ([]domain.RunnerRuntime, error) {
	lease, err := managedcli.BeginClaim(ctx, d.cfg.ManagedCLIRoot)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	inventory, err := managedcli.ReadInventory(d.cfg.ManagedCLIRoot)
	if err != nil {
		return nil, err
	}
	// Retain usage during --version so activation cannot race a probe.
	return detectRuntimes(ctx, d.cfg.ExtraBinaries, inventory), nil
}

func (d *Daemon) releaseManagedTask(taskID string) {
	d.mu.Lock()
	state := d.managedTasks[taskID]
	delete(d.managedTasks, taskID)
	d.mu.Unlock()
	if state != nil {
		if err := state.lease.Close(); err != nil {
			log.Printf("task %s release managed CLI lease: %v", taskID, err)
		}
	}
}
