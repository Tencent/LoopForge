package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A sandbox of a custom image may lack Node.js, npm or Python 3, which the
// native CLIs and their hooks need. The platform container cannot bind paths
// of its own image into a sibling container, so it copies its Node.js prefix,
// the CLIs bundled there and a relocatable Python into the host-visible CLI
// directory once per platform build. Sandboxes mount it read-only and append
// it to PATH, so a tool shipped by the image still wins.
const toolchainEnv = "AGENTFLOW_TOOLCHAIN"

// Paths inside the platform image; tests point them at a fixture.
var (
	toolchainNodePrefix = "/usr/local"
	toolchainPythonDir  = "/opt/loopforge/python"
	toolchainCommitFile = "/app/loopforge-commit"
)

// toolchainPathSetup appends the mounted toolchain to PATH when exec passed it.
const toolchainPathSetup = `if [ -n "${AGENTFLOW_TOOLCHAIN:-}" ] && [ -d "$AGENTFLOW_TOOLCHAIN" ]; then
  PATH=$PATH:$AGENTFLOW_TOOLCHAIN/bin:$AGENTFLOW_TOOLCHAIN/python/bin
  export PATH
fi
`

// toolchainLockTimeout bounds the wait for another process copying the
// toolchain; a cold copy onto a slow data directory takes minutes.
const toolchainLockTimeout = 15 * time.Minute

// toolchainBeforeLock lets a test observe a caller that passed the unlocked
// fast path and is about to wait for the lock.
var toolchainBeforeLock = func() {}

// lockToolchain serializes toolchain writers across every platform process
// sharing the CLI directory. Writers recheck under the lock, so a directory
// another process published is never replaced.
func lockToolchain(parent string) (func(), error) {
	toolchainBeforeLock()
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("准备工具链目录失败：%w", err)
	}
	f, err := os.OpenFile(filepath.Join(parent, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolchainLockTimeout)
	defer cancel()
	if err := flockExclusive(ctx, f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("等待工具链锁失败：%w", err)
	}
	return func() { _ = unlockFile(f) }, nil
}

// RuntimeToolchainDir returns this build's toolchain copy, creating it on
// first use. It returns "" when the CLI directory is not configured or the
// process does not run in the platform image.
func RuntimeToolchainDir() (string, error) {
	root := RuntimeCLIDir()
	node := filepath.Join(toolchainNodePrefix, "bin", "node")
	if root == "" || !fileIsExecutable(node) {
		return "", nil
	}
	parent := filepath.Join(root, "toolchain")
	dir := filepath.Join(parent, toolchainKey())
	ready := filepath.Join(dir, ".ready")
	if _, err := os.Stat(ready); err == nil {
		return dir, nil
	}
	unlock, err := lockToolchain(parent)
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, err := os.Stat(ready); err == nil {
		return dir, nil
	}
	stage, err := os.MkdirTemp(parent, ".stage-")
	if err != nil {
		return "", fmt.Errorf("准备工具链目录失败：%w", err)
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o755); err != nil {
		return "", err
	}
	if err := copyToolchain(stage); err != nil {
		return "", fmt.Errorf("复制工具链失败：%w", err)
	}
	if err := os.WriteFile(filepath.Join(stage, ".ready"), nil, 0o644); err != nil {
		return "", err
	}
	// Only a copy that never got its .ready marker can be here.
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.Rename(stage, dir); err != nil {
		return "", fmt.Errorf("启用工具链失败：%w", err)
	}
	return dir, nil
}

func toolchainKey() string {
	if data, err := os.ReadFile(toolchainCommitFile); err == nil {
		if key := strings.TrimSpace(string(data)); key != "" && !strings.ContainsAny(key, "/\\ \t\r\n") {
			return key
		}
	}
	if info, err := os.Stat(filepath.Join(toolchainNodePrefix, "lib", "node_modules")); err == nil {
		return "mtime-" + strconv.FormatInt(info.ModTime().Unix(), 10)
	}
	return "default"
}

// copyToolchain copies node, npm and Python. The bundled CLIs are hundreds of
// megabytes each, so EnsureToolchainCLI copies one only when a sandbox needs it.
// Other files in the prefix (such as the docker CLI) stay out.
func copyToolchain(stage string) error {
	bin := filepath.Join(stage, "bin")
	modules := filepath.Join(stage, "lib", "node_modules")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(modules, 0o755); err != nil {
		return err
	}
	prefixBin := filepath.Join(toolchainNodePrefix, "bin")
	if err := copyPreserving(bin, filepath.Join(prefixBin, "node"), filepath.Join(prefixBin, "npm"), filepath.Join(prefixBin, "npx")); err != nil {
		return err
	}
	if err := copyPreserving(modules, filepath.Join(toolchainNodePrefix, "lib", "node_modules", "npm")); err != nil {
		return err
	}
	if info, err := os.Stat(toolchainPythonDir); err == nil && info.IsDir() {
		return copyPreserving(stage, toolchainPythonDir)
	}
	return nil
}

// EnsureToolchainCLI copies the package behind a CLI bundled in the platform
// image, with every launcher pointing into it, into the toolchain at dir. A
// binary the platform image does not bundle is left to the sandbox installer.
func EnsureToolchainCLI(dir, binary string) error {
	if dir == "" || binary == "" || strings.ContainsAny(binary, "/\\") {
		return nil
	}
	prefixBin := filepath.Join(toolchainNodePrefix, "bin")
	target, err := os.Readlink(filepath.Join(prefixBin, binary))
	pkg, ok := toolchainPackage(target)
	if err != nil || !ok {
		return nil
	}
	launcher := filepath.Join(dir, "bin", binary)
	if fileIsExecutable(launcher) {
		return nil
	}
	unlock, err := lockToolchain(filepath.Dir(dir))
	if err != nil {
		return err
	}
	defer unlock()
	if fileIsExecutable(launcher) {
		return nil
	}
	modules := filepath.Join(dir, "lib", "node_modules")
	dest := filepath.Join(modules, filepath.FromSlash(pkg))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dest), ".stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o755); err != nil {
		return err
	}
	if err := copyPreserving(stage, filepath.Join(toolchainNodePrefix, "lib", "node_modules", filepath.FromSlash(pkg))); err != nil {
		return fmt.Errorf("复制 %s 失败：%w", binary, err)
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(stage, filepath.Base(dest)), dest); err != nil {
		return err
	}
	entries, err := os.ReadDir(prefixBin)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		link, err := os.Readlink(filepath.Join(prefixBin, entry.Name()))
		if other, ok := toolchainPackage(link); err != nil || !ok || other != pkg {
			continue
		}
		path := filepath.Join(dir, "bin", entry.Name())
		_ = os.Remove(path)
		if err := os.Symlink(link, path); err != nil {
			return err
		}
	}
	return nil
}

// toolchainPackage returns the package ("name" or "@scope/name") an npm
// launcher in the prefix bin points into.
func toolchainPackage(link string) (string, bool) {
	rest, ok := strings.CutPrefix(link, "../lib/node_modules/")
	if !ok {
		return "", false
	}
	parts := strings.Split(rest, "/")
	if strings.HasPrefix(parts[0], "@") {
		if len(parts) < 3 {
			return "", false
		}
		return parts[0] + "/" + parts[1], true
	}
	if len(parts) < 2 || parts[0] == "npm" {
		return "", false
	}
	return parts[0], true
}

func copyPreserving(dst string, sources ...string) error {
	args := append([]string{"-a"}, sources...)
	out, err := exec.Command("cp", append(args, dst+"/")...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sandboxToolchain is the toolchain a sandbox of a custom image should see.
// Sandboxes of the platform image already carry these tools.
func (s *dockerSandbox) sandboxToolchain() (string, error) {
	if isolatedRuntimeImage(s.dockerfile) != "" {
		return "", nil
	}
	return RuntimeToolchainDir()
}

func (s *dockerSandbox) toolchainExecEnv() ([]string, error) {
	dir, err := s.sandboxToolchain()
	if err != nil || dir == "" {
		return nil, err
	}
	return []string{"-e", toolchainEnv + "=" + dir}, nil
}

// toolchainCLIExecEnv is toolchainExecEnv after making binary available in
// the toolchain when the platform image bundles it.
func (s *dockerSandbox) toolchainCLIExecEnv(binary string) ([]string, error) {
	dir, err := s.sandboxToolchain()
	if err != nil || dir == "" {
		return nil, err
	}
	if err := EnsureToolchainCLI(dir, binary); err != nil {
		return nil, err
	}
	return []string{"-e", toolchainEnv + "=" + dir}, nil
}

// withToolchainPath runs argv after toolchainPathSetup, for commands that
// docker exec starts directly instead of through a login shell.
func withToolchainPath(argv []string) []string {
	return append([]string{"sh", "-c", toolchainPathSetup + `exec "$0" "$@"`}, argv...)
}
