package runtime

import (
	"errors"
	"io/fs"
	"os"
)

// RemoveWorkspaceTree removes a workspace directory, including directories
// without owner write or search permission. Tools such as the Go module cache
// leave such directories behind, and checkpoint restore now reproduces them.
// A partial removal must not stop at the first one: the next idle sweep would
// capture what is left and replace the complete checkpoint.
func RemoveWorkspaceTree(path string) error {
	err := os.RemoveAll(path)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if writableErr := makeDirectoriesOwnerWritable(path); writableErr != nil {
		return errors.Join(err, writableErr)
	}
	return os.RemoveAll(path)
}

// makeDirectoriesOwnerWritable adds owner rwx to every real directory below
// path. os.Root keeps a concurrently swapped symlink from reaching outside.
func makeDirectoriesOwnerWritable(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return err
	}
	if mode := info.Mode().Perm(); mode&0o700 != 0o700 {
		if err := os.Chmod(path, mode|0o700); err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Best effort: the final RemoveAll reports anything left behind.
			if entry != nil && entry.IsDir() && name != "." {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if mode := info.Mode().Perm(); mode&0o700 != 0o700 {
			// WalkDir reads a directory only after this callback returns.
			_ = root.Chmod(name, mode|0o700)
		}
		return nil
	})
}
