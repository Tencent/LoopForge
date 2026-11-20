package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
)

// Pin host executables for platform-owned workflow runs. Docker sandboxes and
// connected runners resolve the CLI on their own execution machines.
func pinNativeExecutorCLI(ctx context.Context, driver, runID string, executor domain.ExecutorDefinition) (string, *managedcli.ClaimLease, error) {
	switch driver {
	case "local", "seatbelt", "bwrap":
	default:
		return "", nil, nil
	}
	root := strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT"))
	kind := strings.TrimSpace(executor.Kind)
	if root == "" || !managedcli.Supports(kind) {
		return "", nil, nil
	}
	if binary := strings.TrimSpace(executor.Binary); binary != "" && binary != conversationBinary(kind) {
		return "", nil, nil
	}
	lease, err := managedcli.BeginClaim(ctx, root)
	if err != nil {
		return "", nil, fmt.Errorf("managed CLI usage for %s: %w", kind, err)
	}
	var inventory managedcli.Inventory
	if runID = strings.TrimSpace(runID); runID != "" {
		inventory, err = managedcli.PinInventory(root, "platform:"+runID)
	} else {
		inventory, err = managedcli.ReadInventory(root)
	}
	if err != nil {
		lease.Close()
		return "", nil, fmt.Errorf("managed CLI inventory for %s: %w", kind, err)
	}
	if binary, ok := inventory.BinaryFor(kind); ok {
		return binary, lease, nil
	}
	return "", nil, lease.Close()
}
