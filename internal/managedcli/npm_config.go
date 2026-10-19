package managedcli

import (
	"errors"
	"os"
	"path/filepath"
)

// npm rejects loading the same physical config source twice. Use two private,
// empty files rather than /dev/null for both userconfig and globalconfig.
func emptyNPMConfig(path string) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("refuse non-regular updater npm config")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".npmrc-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.WriteString("# managed updater: no host npm credentials\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
