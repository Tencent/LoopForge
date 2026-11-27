package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
)

const cursorCLIBinary = "cursor-agent"

var cursorInstallMu sync.Mutex

func sharedCursorCLIPath() string {
	root := RuntimeCLIDir()
	if !filepath.IsAbs(root) {
		return ""
	}
	return filepath.Join(root, "cursor", goruntime.GOOS+"-"+goruntime.GOARCH, "current", cursorCLIBinary)
}

// prepareSharedCursorCLI publishes just the distribution, not an installer's
// HOME or the platform user's Cursor settings. Docker mounts the CLI directory
// read-only. An existing platform install can be shared without another download.
func prepareSharedCursorCLI(ctx context.Context, download bool) (string, error) {
	binary := sharedCursorCLIPath()
	if binary == "" || fileIsExecutable(binary) {
		return binary, nil
	}
	cursorInstallMu.Lock()
	defer cursorInstallMu.Unlock()
	if fileIsExecutable(binary) {
		return binary, nil
	}
	home, _ := os.UserHomeDir()
	source := installedCursorPackage(home)
	if source == "" && !download {
		return "", nil
	}
	target := filepath.Dir(binary)
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	var stagedPackage string
	if source != "" {
		if err := copyPreserving(stage, source); err != nil {
			return "", fmt.Errorf("复制已安装的 Cursor CLI 失败：%w", err)
		}
		stagedPackage = filepath.Join(stage, filepath.Base(source))
	} else {
		installHome := filepath.Join(stage, "home")
		if err := os.Mkdir(installHome, 0o700); err != nil {
			return "", err
		}
		if _, err := installScriptCLI(ctx, onDemandCLICatalog()[AgentCLICursor], installHome); err != nil {
			return "", err
		}
		stagedPackage = installedCursorPackage(installHome)
		if stagedPackage == "" {
			return "", fmt.Errorf("未找到官方 Cursor CLI 安装包")
		}
	}
	if err := os.Chmod(stagedPackage, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(stagedPackage, target); err != nil && !fileIsExecutable(binary) {
		return "", fmt.Errorf("启用共享 Cursor CLI 失败：%w", err)
	}
	return binary, nil
}

// The vendor's launcher is a symlink into a versioned directory containing
// its own node and runtime files. Copying only the launcher would break it.
func installedCursorPackage(home string) string {
	if home == "" {
		return ""
	}
	versions := filepath.Join(home, ".local", "share", cursorCLIBinary, "versions")
	binary, err := filepath.EvalSymlinks(filepath.Join(home, ".local", "bin", cursorCLIBinary))
	if err != nil || !fileIsExecutable(binary) {
		return ""
	}
	versions, err = filepath.EvalSymlinks(versions)
	if err != nil {
		return ""
	}
	relative, err := filepath.Rel(versions, binary)
	version, name, ok := strings.Cut(relative, string(filepath.Separator))
	if err != nil || !ok || version == ".." || name != cursorCLIBinary {
		return ""
	}
	return filepath.Dir(binary)
}
