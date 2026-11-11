package runtime

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Parallel SOP branches materialize and execute in one shared checkout. The
// helpers here keep the few workspace-wide control writes from interleaving;
// per-Agent directories are serialized by the engine (one invocation per
// Agent and checkout), so only state shared across Agents needs care.

var workspaceLocks sync.Map // cleaned workspace path -> *sync.Mutex

func lockWorkspace(workspace string) func() {
	key := filepath.Clean(workspace)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	value, _ := workspaceLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// refreshSharedControlDirectory rebuilds a control directory that concurrent
// invocations of different Agents read (skills/<id>). The new content is built
// beside the live directory and swapped in only when it differs, so a running
// CLI never sees an unchanged skill disappear underneath it.
func refreshSharedControlDirectory(workspace string, build func(dir string) error, elements ...string) (string, error) {
	if len(elements) == 0 {
		return "", fmt.Errorf("missing AgentFlow control directory")
	}
	name := elements[len(elements)-1]
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid AgentFlow control path segment %q", name)
	}
	parent, err := EnsureControlDirectory(workspace, elements[:len(elements)-1]...)
	if err != nil {
		return "", err
	}
	target := filepath.Join(parent, name)
	if info, err := os.Lstat(target); err == nil && !info.IsDir() {
		return "", fmt.Errorf("managed path is not a real directory: %s", target)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	staging := filepath.Join(parent, "."+name+".new-"+rand.Text())
	if err := os.Mkdir(staging, 0o700); err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	if err := build(staging); err != nil {
		return "", err
	}
	if same, err := sameDirectoryTree(staging, target); err == nil && same {
		return target, nil
	}
	retired := ""
	if _, err := os.Lstat(target); err == nil {
		retired = filepath.Join(parent, "."+name+".old-"+rand.Text())
		if err := os.Rename(target, retired); err != nil {
			return "", err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		if retired != "" {
			_ = os.Rename(retired, target)
		}
		return "", err
	}
	if retired != "" {
		_ = os.RemoveAll(retired)
	}
	return target, nil
}

// sameDirectoryTree compares names, kinds, permission bits, file contents and
// symlink targets of two trees.
func sameDirectoryTree(a, b string) (bool, error) {
	collect := func(root string) (map[string]fs.FileInfo, error) {
		entries := map[string]fs.FileInfo{}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			entries[rel] = info
			return nil
		})
		return entries, err
	}
	left, err := collect(a)
	if err != nil {
		return false, err
	}
	right, err := collect(b)
	if err != nil {
		return false, err
	}
	if len(left) != len(right) {
		return false, nil
	}
	for rel, li := range left {
		ri, ok := right[rel]
		if !ok || li.Mode().Type() != ri.Mode().Type() {
			return false, nil
		}
		if rel != "." && li.Mode().Perm() != ri.Mode().Perm() {
			return false, nil
		}
		switch {
		case li.Mode().IsRegular():
			if li.Size() != ri.Size() {
				return false, nil
			}
			lb, err := os.ReadFile(filepath.Join(a, rel))
			if err != nil {
				return false, err
			}
			rb, err := os.ReadFile(filepath.Join(b, rel))
			if err != nil {
				return false, err
			}
			if !bytes.Equal(lb, rb) {
				return false, nil
			}
		case li.Mode()&os.ModeSymlink != 0:
			lt, err := os.Readlink(filepath.Join(a, rel))
			if err != nil {
				return false, err
			}
			rt, err := os.Readlink(filepath.Join(b, rel))
			if err != nil {
				return false, err
			}
			if lt != rt {
				return false, nil
			}
		}
	}
	return true, nil
}
