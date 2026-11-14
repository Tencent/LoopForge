package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

// TestVerifyDeliveryCompletenessPerRepository pins how verifyDeliveryCompleteness
// consumes expectedDeliveryFiles and committedFiles in a multi-repository run:
// each repository dir is checked against its own checkout, missing files are
// labeled with that dir, and a repository whose checkout cannot be listed
// (not a git repo) is skipped instead of reporting every file as lost.
func TestVerifyDeliveryCompletenessPerRepository(t *testing.T) {
	t.Parallel()
	ws := t.TempDir() // the root itself is not a git checkout
	web := filepath.Join(ws, "web")
	mustMkdir(t, web)
	gitInit(t, web)
	gitCommitFile(t, web, "src/kept.ts", "export {}\n")
	e, _ := newRecoveryEngine(t)

	newFile := func(path string) string {
		return "diff --git a/" + path + " b/" + path + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n+x\n"
	}
	run := domain.Run{
		ID: "run-multi-repo",
		Repositories: []domain.RepositoryContext{
			{WorkspaceDir: "web"},
			{WorkspaceDir: "api"}, // bound but never checked out
		},
		Artifacts: []domain.Artifact{
			{Name: "repository-patch", NodeID: "dev", RepositoryDir: "web", Content: newFile("src/kept.ts") + newFile("src/app.ts")},
			{Name: "repository-patch", NodeID: "dev", RepositoryDir: "api", Content: newFile("main.go")},
			{Name: "repository-patch", NodeID: "test", Content: newFile("root.sh")},
		},
	}

	got := e.verifyDeliveryCompleteness(run, ws)
	want := []string{"web/src/app.ts (produced by dev)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verifyDeliveryCompleteness = %v, want %v (only the web checkout can be verified)", got, want)
	}
}

// TestVerifyDeliveryCompletenessSortsAcrossRepositories checks that losses from
// several checkouts are merged into one deterministic, sorted list.
func TestVerifyDeliveryCompletenessSortsAcrossRepositories(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	gitInit(t, ws)
	mustMkdir(t, filepath.Join(ws, "svc"))
	gitInit(t, filepath.Join(ws, "svc"))
	e, _ := newRecoveryEngine(t)

	newFile := func(path string) string {
		return "diff --git a/" + path + " b/" + path + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n+x\n"
	}
	run := domain.Run{ID: "run-sorted", Artifacts: []domain.Artifact{
		{Name: "repository-patch", NodeID: "dev", RepositoryDir: "svc", Content: newFile("z.go") + newFile("a.go")},
		{Name: "repository-patch", NodeID: "mr", Content: newFile("b.sh")},
	}}
	got := e.verifyDeliveryCompleteness(run, ws)
	want := []string{"b.sh (produced by mr)", "svc/a.go (produced by dev)", "svc/z.go (produced by dev)"}
	for i := 0; i < 5; i++ { // map iteration order must not leak into the result
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("verifyDeliveryCompleteness = %v, want %v", got, want)
		}
		got = e.verifyDeliveryCompleteness(run, ws)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}
