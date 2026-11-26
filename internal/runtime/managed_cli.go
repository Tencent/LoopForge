package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
)

func explicitNativeCLIPath(provider string) string {
	override := "AGENTFLOW_" + strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_BIN"
	if provider == "claude-code" {
		override = "AGENTFLOW_CLAUDE_BIN"
	}
	if binary := strings.TrimSpace(os.Getenv(override)); binary != "" && fileIsExecutable(binary) {
		return binary
	}
	return ""
}

// Pin host executables for workflow runs in native sandboxes. Docker
// sandboxes install the CLI inside their container instead.
func pinNativeExecutorCLI(ctx context.Context, driver, runID string, executor domain.ExecutorDefinition) (string, *managedcli.ClaimLease, error) {
	switch driver {
	case "local", "seatbelt", "bwrap":
	default:
		return "", nil, nil
	}
	kind := strings.TrimSpace(executor.Kind)
	if binary := strings.TrimSpace(executor.Binary); binary != "" && binary != conversationBinary(kind) {
		return "", nil, nil
	}
	root := strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT"))
	managed := root != "" && managedcli.Supports(kind)
	if path := explicitNativeCLIPath(kind); path != "" && (managed || IsAgentCLI(kind)) {
		return path, nil, nil
	}
	if managed {
		binary, lease, err := pinManagedCLI(ctx, root, runID, kind)
		if err != nil || binary != "" {
			return binary, lease, err
		}
	}
	// An agent CLI the managed root does not provide may still be installed on
	// this machine, for example on demand from a chat. Run the executable a
	// native conversation would, instead of a bare name PATH may not resolve.
	if IsAgentCLI(kind) {
		if path, err := hostCLIPath(kind, conversationBinary(kind)); err == nil {
			return path, nil, nil
		}
	}
	return "", nil, nil
}

func pinManagedCLI(ctx context.Context, root, runID, kind string) (string, *managedcli.ClaimLease, error) {
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

// bwrap overlays a private /tmp. Restore only the selected installation, as a
// read-only bind, when the managed executable lives beneath that directory.
func withBwrapManagedCLI(command CommandSpec, driver string) CommandSpec {
	if driver != "bwrap" || !managedcli.Supports(command.Provider) || len(command.Argv) == 0 || !filepath.IsAbs(command.Argv[0]) {
		return command
	}
	root := strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT"))
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return command
	}
	base := filepath.Join(root, "providers", command.Provider)
	relative, err := filepath.Rel(base, command.Argv[0])
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return command
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return command
	}
	releaseDir, _, _ := strings.Cut(relative, string(filepath.Separator))
	providerDir := filepath.Join(resolvedRoot, "providers", command.Provider)
	install := filepath.Join(providerDir, releaseDir)
	binary, err := filepath.EvalSymlinks(command.Argv[0])
	if err != nil {
		return command
	}
	resolvedRelative, err := filepath.Rel(install, binary)
	if err != nil || !filepath.IsLocal(resolvedRelative) {
		return command
	}
	// A symlinked ancestor in /tmp can be hidden even when its target is
	// visible. Resolve the root prefix without changing an npm bin symlink.
	if path := filepath.Join(providerDir, relative); path != command.Argv[0] {
		command.Argv = slices.Clone(command.Argv)
		command.Argv[0] = path
	}
	privateTmp, err := filepath.EvalSymlinks("/tmp")
	if err == nil && strings.HasPrefix(install, privateTmp+string(filepath.Separator)) {
		command.managedCLIInstall = install
	}
	return command
}
