package runtime

import (
	"errors"
	"fmt"
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

// makeDirectoriesOwnerWritable adds owner rwx to path and every real
// directory below it. All mode changes go through an os.Root opened on path,
// so a concurrently swapped symlink cannot reach outside: below the root
// os.Root refuses it, and for path itself the opened directory must be the
// one Lstat saw (OpenRoot follows a symlink in the last element). A path
// without owner read cannot be opened and is left to the caller's error.
func makeDirectoriesOwnerWritable(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return fmt.Errorf("%s was replaced while it was being removed", path)
	}
	// WalkDir visits "." first, so the root itself is made writable here too.
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
