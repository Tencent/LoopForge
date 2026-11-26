package runtime

import (
	"agentflow/internal/domain"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
		if _, err := archivedArtifactPath(f.Path); err != nil {
			return nil, err
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
		created, err := restoreMissingWorkspaceArtifact(base, rel, func(out io.Writer) error {
			_, err := io.WriteString(out, f.Content)
			return err
		})
		if err != nil {
			return restored, err
		}
		if created {
			restored = append(restored, filepath.ToSlash(rel))
		}
	}
	return restored, nil
}

// RestoreMissingWorkspaceArtifact streams an archived output only when its
// workspace path is absent. A failed transfer leaves the destination untouched.
func RestoreMissingWorkspaceArtifact(root, path, digest string, source func() (io.ReadCloser, error)) (bool, error) {
	rel, err := archivedArtifactPath(path)
	if err != nil {
		return false, err
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return false, err
	}
	defer base.Close()
	return restoreMissingWorkspaceArtifact(base, rel, func(out io.Writer) error {
		body, err := source()
		if err != nil {
			return err
		}
		defer body.Close()
		return copyArtifact(out, body, digest)
	})
}

// WriteArtifactInput atomically materializes a read-only handoff file under
// the runtime control directory.
func WriteArtifactInput(workspace, rel, digest string, source io.Reader) error {
	if !domain.SafeWorkspaceRelPath(rel) || !strings.HasPrefix(rel, ".agentflow/inputs/") {
		return fmt.Errorf("unsafe input path %q", rel)
	}
	target := filepath.FromSlash(rel)
	parent := filepath.Dir(target)
	segments := strings.Split(filepath.ToSlash(parent), "/")
	if _, err := EnsureControlDirectory(workspace, segments[1:]...); err != nil {
		return err
	}
	base, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer base.Close()
	if st, err := base.Lstat(target); err == nil {
		if !st.Mode().IsRegular() {
			return fmt.Errorf("artifact input path is not a regular file: %s", rel)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	temp, err := stageArchivedArtifact(base, parent, func(out io.Writer) error {
		return copyArtifact(out, source, digest)
	})
	if err != nil {
		return err
	}
	defer base.Remove(temp)
	if err := base.Chmod(temp, 0o444); err != nil {
		return err
	}
	return base.Rename(temp, target)
}

func copyArtifact(out io.Writer, source io.Reader, digest string) error {
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(source, domain.MaxSOPArtifactBytes+1))
	if err != nil {
		return err
	}
	if n > domain.MaxSOPArtifactBytes {
		return domain.ErrArtifactTooLarge
	}
	if digest != "" && hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("artifact download checksum mismatch")
	}
	return nil
}

func archivedArtifactPath(path string) (string, error) {
	rel := filepath.Clean(filepath.FromSlash(path))
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid archived artifact path: %s", path)
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("hidden archived artifact path: %s", path)
		}
	}
	return rel, nil
}

func restoreMissingWorkspaceArtifact(base *os.Root, rel string, write func(io.Writer) error) (bool, error) {
	if _, err := base.Lstat(rel); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	parent := filepath.Dir(rel)
	for current := parent; current != "."; current = filepath.Dir(current) {
		st, err := base.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !st.IsDir() {
			return false, nil
		}
	}
	if err := base.MkdirAll(parent, 0755); err != nil {
		return false, err
	}
	// Link is create-only, so a concurrent workspace write wins.
	temp, err := stageArchivedArtifact(base, parent, write)
	if err != nil {
		return false, err
	}
	defer base.Remove(temp)
	if err := base.Link(temp, rel); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// stageArchivedArtifact writes content to a fresh private temp file under
// parent (0644 after sync) and returns its root-relative path. It retries name
// collisions up to 10 times; any write failure removes the partial temp file.
func stageArchivedArtifact(base *os.Root, parent string, write func(io.Writer) error) (string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		candidate := filepath.Join(parent, fmt.Sprintf(".agentflow-artifact-%d-%d", os.Getpid(), artifactTempSequence.Add(1)))
		out, err := base.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		err = write(out)
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
			_ = base.Remove(candidate)
			return "", err
		}
		return candidate, nil
	}
	return "", fmt.Errorf("cannot stage archived artifact")
}

var artifactTempSequence atomic.Uint64
