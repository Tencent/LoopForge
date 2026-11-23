package httpapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssistantDiagnosticEnvironmentUsesPackagedBuildMetadata(t *testing.T) {
	const version = "v0.4.2"
	const commit = "4ff0e73c84987966baa109d578c93b855ab8a26f"
	commitFile := filepath.Join(t.TempDir(), "loopforge-commit")
	if err := os.WriteFile(commitFile, []byte(commit), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOPFORGE_RELEASE_VERSION", version)
	t.Setenv("LOOPFORGE_COMMIT", "")
	t.Setenv("LOOPFORGE_COMMIT_FILE", commitFile)

	build := assistantDiagnosticEnvironment()
	if build.PlatformVersion != version || build.Revision != commit {
		t.Fatalf("diagnostics build version=%q revision=%q, want %q / %q", build.PlatformVersion, build.Revision, version, commit)
	}
}
