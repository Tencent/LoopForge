package preview

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func fileHasMultipleLinks(path string, _ os.FileInfo) (bool, error) {
	nativePath, err := previewNativePath(path)
	if err != nil {
		return false, err
	}
	name, err := windows.UTF16PtrFromString(nativePath)
	if err != nil {
		return false, err
	}
	// Inspect the entry itself; do not follow a reparse point introduced after
	// the archive walk inspected it. Metadata errors must stop the archive.
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return false, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false, fmt.Errorf("%w: reparse point %s", ErrUnsupportedEntry, path)
	}
	return info.NumberOfLinks > 1, nil
}

// previewNativePath preserves no-follow CreateFile metadata reads on long paths
// without relying on the process manifest or the machine long-path policy.
func previewNativePath(path string) (string, error) {
	if len(path) < windows.MAX_PATH || strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return path, nil
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = absolute
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(path, `\\`), nil
	}
	return `\\?\` + path, nil
}
