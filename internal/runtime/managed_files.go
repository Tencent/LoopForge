package runtime

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// openWorkspacePath anchors operations before resolving any untrusted components.
// Reject symlinks as a policy; os.Root also confines operations if a component is
// replaced between validation and use. The workspace itself is a trusted root.
func openWorkspacePath(workspace, target string) (*os.Root, string, error) {
	base, err := filepath.Abs(workspace)
	if err != nil {
		return nil, "", err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return nil, "", err
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return nil, "", fmt.Errorf("invalid workspace file path %q", target)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, "", err
	}
	if err := rejectWorkspaceSymlinks(root, rel); err != nil {
		root.Close()
		return nil, "", err
	}
	return root, rel, nil
}

func rejectWorkspaceSymlinks(root *os.Root, rel string) error {
	current := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace path is a symlink: %s", current)
		}
	}
	return nil
}

func readWorkspaceFile(workspace, target string) ([]byte, error) {
	root, rel, err := openWorkspacePath(workspace, target)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(rel)
}

func writeManagedFile(workspace, target string, content []byte, mode os.FileMode) error {
	return writeManagedReader(workspace, target, bytes.NewReader(content), mode)
}

func writeManagedReader(workspace, target string, content io.Reader, mode os.FileMode) error {
	root, rel, err := openWorkspacePath(workspace, target)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat(rel); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("managed path is not a regular file: %s", target)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Never truncate an existing inode: a sandbox may have hard-linked it to
	// another file. A fresh inode also applies the requested permissions on reuse.
	temp := filepath.Join(filepath.Dir(rel), ".agentflow-write-"+rand.Text())
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = io.Copy(file, content)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(temp, rel)
}
