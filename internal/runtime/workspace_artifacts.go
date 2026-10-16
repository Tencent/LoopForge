package runtime

import (
	"agentflow/internal/domain"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// RestoreMissingWorkspaceArtifacts publishes complete archived files atomically.
// Link is create-only: concurrent Agent writes, files, directories and symlinks
// already at the destination always win. Neither credentials nor Git metadata
// may be supplied as archived output paths.
func RestoreMissingWorkspaceArtifacts(root string, files []domain.WorkspaceArtifactFile) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	total := 0
	for _, f := range files {
		rel := filepath.Clean(filepath.FromSlash(f.Path))
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("invalid archived artifact path: %s", f.Path)
		}
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if strings.HasPrefix(part, ".") {
				return nil, fmt.Errorf("hidden archived artifact path: %s", f.Path)
			}
		}
		total += len(f.Content)
		if len(f.Content) > 1<<20 || total > WorkspaceCheckpointMaxBytes || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) || f.SHA256 == "" || WorkspaceContentSHA(f.Content) != f.SHA256 {
			return nil, fmt.Errorf("archived artifact failed validation: %s", f.Path)
		}
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	var restored []string
	for _, f := range files {
		rel := filepath.Clean(filepath.FromSlash(f.Path))
		if _, err := base.Lstat(rel); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return restored, err
		}
		parent := filepath.Dir(rel)
		safe := true
		for current := parent; current != "."; current = filepath.Dir(current) {
			st, err := base.Lstat(current)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return restored, err
			}
			if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
				safe = false
				break
			}
		}
		if !safe {
			continue
		}
		if err := base.MkdirAll(parent, 0755); err != nil {
			return restored, err
		}
		// Stage privately, then link without replacing the destination. os.Root
		// confines every operation even if a parent directory changes concurrently.
		var temp string
		for attempt := 0; attempt < 10; attempt++ {
			candidate := filepath.Join(parent, fmt.Sprintf(".agentflow-artifact-%d-%d", os.Getpid(), artifactTempSequence.Add(1)))
			out, err := base.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if os.IsExist(err) {
				continue
			}
			if err != nil {
				return restored, err
			}
			temp = candidate
			_, err = out.WriteString(f.Content)
			if err == nil {
				err = out.Chmod(0644)
			}
			if err == nil {
				err = out.Sync()
			}
			closeErr := out.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				_ = base.Remove(temp)
				return restored, err
			}
			break
		}
		if temp == "" {
			return restored, fmt.Errorf("cannot stage archived artifact")
		}
		err := base.Link(temp, rel)
		_ = base.Remove(temp)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return restored, err
		}
		restored = append(restored, filepath.ToSlash(rel))
	}
	return restored, nil
}

var artifactTempSequence atomic.Uint64
