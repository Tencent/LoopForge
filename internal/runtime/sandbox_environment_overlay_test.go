package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestEnvironmentOverlayDefaultsIDAndPrefersProfilePath(t *testing.T) {
	workspace := t.TempDir()
	root := mkEnvironmentRoot(t, workspace, "agentflow-environment")
	profile := domain.SandboxProfile{Driver: "bwrap", Environment: map[string]string{"PATH": "/profile/bin"}}

	overlay, err := environmentOverlay(workspace, root, "", profile, domain.EnvironmentDefinition{})
	if err != nil {
		t.Fatal(err)
	}
	if overlay[environmentIDVar] != workspaceEnvironmentID {
		t.Fatalf("empty env id should fall back to %q, got %+v", workspaceEnvironmentID, overlay)
	}
	if overlay[environmentRootVar] != root || overlay[environmentPrefixVar] != root {
		t.Fatalf("root identity = %+v", overlay)
	}
	want := filepath.Join(root, "bin") + string(os.PathListSeparator) + "/profile/bin"
	if overlay["PATH"] != want {
		t.Fatalf("PATH = %q, want %q", overlay["PATH"], want)
	}
	zdot := filepath.Join(root, environmentZdotDir)
	if overlay["ZDOTDIR"] != zdot || overlay["BASH_ENV"] != filepath.Join(zdot, environmentBashEnvName) || overlay["ENV"] != overlay["BASH_ENV"] {
		t.Fatalf("shell hooks = %+v", overlay)
	}
	raw, err := os.ReadFile(overlay["BASH_ENV"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), filepath.Join(root, "bin")) {
		t.Fatalf("bash env hook did not pin env bin: %s", raw)
	}
}

func TestEnvironmentOverlayFallsBackToProcessPath(t *testing.T) {
	workspace := t.TempDir()
	root := mkEnvironmentRoot(t, workspace, "env")
	t.Setenv("PATH", "/process/bin")

	overlay, err := environmentOverlay(workspace, root, "custom", domain.SandboxProfile{Driver: "bwrap"}, domain.EnvironmentDefinition{})
	if err != nil {
		t.Fatal(err)
	}
	if overlay[environmentIDVar] != "custom" {
		t.Fatalf("explicit env id lost: %+v", overlay)
	}
	if !strings.HasSuffix(overlay["PATH"], string(os.PathListSeparator)+"/process/bin") {
		t.Fatalf("PATH should end with process PATH, got %q", overlay["PATH"])
	}
}

func TestEnvironmentOverlayCatalogVariablesRequireCatalogID(t *testing.T) {
	workspace := t.TempDir()
	root := mkEnvironmentRoot(t, workspace, "env")
	vars := map[string]string{"COMPOSER_HOME": "${PREFIX}/composer"}

	without, err := environmentOverlay(workspace, root, "php", domain.SandboxProfile{}, domain.EnvironmentDefinition{Variables: vars})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := without["COMPOSER_HOME"]; ok {
		t.Fatalf("catalog variables without catalog id should be ignored: %+v", without)
	}

	with, err := environmentOverlay(workspace, root, "php", domain.SandboxProfile{}, domain.EnvironmentDefinition{ID: "php", Variables: vars, PathEntries: []string{"sbin", "/abs/ignored", "sbin"}})
	if err != nil {
		t.Fatal(err)
	}
	if with["COMPOSER_HOME"] != filepath.Join(root, "composer") {
		t.Fatalf("PREFIX placeholder was not expanded: %+v", with)
	}
	prefix := filepath.Join(root, "bin") + string(os.PathListSeparator) + filepath.Join(root, "sbin") + string(os.PathListSeparator)
	if !strings.HasPrefix(with["PATH"], prefix) {
		t.Fatalf("PATH = %q, want prefix %q", with["PATH"], prefix)
	}
}

func mkEnvironmentRoot(t *testing.T, workspace, name string) string {
	t.Helper()
	root := filepath.Join(workspace, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}
