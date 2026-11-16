package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// blockEnvironmentZdot puts a regular file where the login-shell hook
// directory goes, so writing the PATH hooks fails.
func blockEnvironmentZdot(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, environmentZdotDir), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// prepareSandboxEnvironment must surface the overlay error instead of
// activating the environment with a partial or missing overlay.
func TestPrepareSandboxEnvironmentPropagatesOverlayError(t *testing.T) {
	workspace := t.TempDir()
	root := mkEnvironmentRoot(t, workspace, workspaceEnvironmentDir)
	blockEnvironmentZdot(t, root)

	activated, err := prepareSandboxEnvironment(workspace, domain.SandboxProfile{Driver: "bwrap"}, domain.EnvironmentDefinition{})
	if err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("err = %v, want shell hook directory error", err)
	}
	if activated.Overlay != nil || activated.Root != "" || activated.InstallRel != "" {
		t.Fatalf("activated on error = %+v, want zero value", activated)
	}
}

// The drop-in environment discovered in the workspace is activated with the
// overlay environmentOverlay built for that root.
func TestPrepareSandboxEnvironmentActivatesDiscoveredDropIn(t *testing.T) {
	workspace := t.TempDir()
	root := mkEnvironmentRoot(t, workspace, workspaceEnvironmentDir)

	activated, err := prepareSandboxEnvironment(workspace, domain.SandboxProfile{Driver: "bwrap", Environment: map[string]string{"PATH": "/profile/bin"}}, domain.EnvironmentDefinition{})
	if err != nil {
		t.Fatal(err)
	}
	if activated.Root != root || activated.InstallRel != "" {
		t.Fatalf("activated = %+v, want root %q", activated, root)
	}
	if activated.Overlay[environmentIDVar] != workspaceEnvironmentID || activated.Overlay[environmentRootVar] != root {
		t.Fatalf("overlay identity = %+v", activated.Overlay)
	}
	if want := filepath.Join(root, "bin") + string(os.PathListSeparator) + "/profile/bin"; activated.Overlay["PATH"] != want {
		t.Fatalf("PATH = %q, want %q", activated.Overlay["PATH"], want)
	}
}
