package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestNodeCheckpointCleansPartialRefs(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	if err := os.Mkdir(first, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", first}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return string(out)
	}
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "base")
	refs := []domain.RepositoryContext{{RepositoryID: "first", WorkspaceDir: "first"}, {RepositoryID: "missing", WorkspaceDir: "missing"}}
	if _, err := CreateNodeWorkspaceCheckpoint(context.Background(), root, refs, 1); err == nil {
		t.Fatal("expected second repository to fail")
	}
	if got := git("for-each-ref", "--format=%(refname)", "refs/loopforge/checkpoints/"); strings.TrimSpace(got) != "" {
		t.Fatalf("partial checkpoint refs remain: %s", got)
	}
}
