package gitwork

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RemoveIncompleteClone discards only an empty interrupted clone, preserving
// any checkout with files, an index, baseline or refs.
func RemoveIncompleteClone(ctx context.Context, workspace string) error {
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			return fmt.Errorf("repository checkout is incomplete but contains files; preserving workspace %s", workspace)
		}
	}
	for _, name := range []string{"index", "agentflow-baseline"} {
		if _, err := os.Lstat(filepath.Join(workspace, ".git", name)); !os.IsNotExist(err) {
			return fmt.Errorf("repository checkout is incomplete but has %s; preserving workspace %s", name, workspace)
		}
	}
	refs, err := C(ctx, workspace, "for-each-ref", "--format=%(objectname)")
	if err != nil || strings.TrimSpace(refs) != "" {
		return fmt.Errorf("repository checkout is incomplete but refs cannot be safely discarded; preserving workspace %s", workspace)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(".git")
}
