package runtime

import (
	"context"
	"debug/elf"
	"fmt"
	"io"
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
	toolchainPythonDir  = "/app/python"
	toolchainCommitFile = "/app/loopforge-commit"
)

// toolchainPathSetup appends the mounted toolchain to PATH when exec passed it
// and the container has the ELF interpreter its node needs. A musl (Alpine)
// or foreign-architecture image keeps its PATH and installs CLIs and python3
// with its own package manager, because `command -v` cannot tell that the
// toolchain binaries would not start there.
const toolchainPathSetup = `if [ -n "${AGENTFLOW_TOOLCHAIN:-}" ] && [ -r "$AGENTFLOW_TOOLCHAIN/.interp" ]; then
  toolchain_interp=
  read -r toolchain_interp < "$AGENTFLOW_TOOLCHAIN/.interp" || :
  if [ -n "$toolchain_interp" ] && [ -e "$toolchain_interp" ]; then
    PATH=$PATH:$AGENTFLOW_TOOLCHAIN/bin:$AGENTFLOW_TOOLCHAIN/python/bin
    export PATH
  fi
  unset toolchain_interp
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
	dir, ok := toolchainLocation()
	if !ok {
		return "", nil
	}
	parent := filepath.Dir(dir)
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

// toolchainLocation is where this build's toolchain lives; ok is false when
// the CLI directory is not configured or the process does not run in the
// platform image.
func toolchainLocation() (string, bool) {
	root := RuntimeCLIDir()
	if root == "" || !fileIsExecutable(filepath.Join(toolchainNodePrefix, "bin", "node")) {
		return "", false
	}
	return filepath.Join(root, "toolchain", toolchainKey()), true
}

// readyToolchainDir returns the toolchain only when it is already published.
// It never copies, so a sandbox command does not wait for or fail on it.
func readyToolchainDir() string {
	dir, ok := toolchainLocation()
	if !ok {
		return ""
	}
	if _, err := os.Stat(filepath.Join(dir, ".ready")); err != nil {
		return ""
	}
	return dir
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
	// Without .interp the sandbox launch never adds the toolchain to PATH.
	if interp := elfInterpreter(filepath.Join(prefixBin, "node")); interp != "" {
		if err := os.WriteFile(filepath.Join(stage, ".interp"), []byte(interp+"\n"), 0o644); err != nil {
			return err
		}
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
	if err := installToolchainPackage(dir, binary, pkg); err != nil {
		return err
	}
	return linkToolchainLaunchers(dir, pkg)
}

// installToolchainPackage copies npm package pkg from the platform image into
// the toolchain at dir through a stage directory, replacing any earlier copy.
// binary only names the CLI in the copy error.
func installToolchainPackage(dir, binary, pkg string) error {
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
	return os.Rename(filepath.Join(stage, filepath.Base(dest)), dest)
}

// linkToolchainLaunchers links, under dir/bin, every launcher in the platform
// image bin that points into package pkg, replacing whatever was there.
func linkToolchainLaunchers(dir, pkg string) error {
	prefixBin := filepath.Join(toolchainNodePrefix, "bin")
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

// elfInterpreter returns the dynamic loader an ELF executable requests, such
// as /lib/ld-linux-aarch64.so.1, or "" when path is not a dynamic ELF file.
func elfInterpreter(path string) string {
	f, err := elf.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	for _, prog := range f.Progs {
		if prog.Type != elf.PT_INTERP {
			continue
		}
		data, err := io.ReadAll(prog.Open())
		if err != nil {
			return ""
		}
		interp := strings.TrimRight(string(data), "\x00")
		if !strings.HasPrefix(interp, "/") || strings.ContainsAny(interp, "\r\n") {
			return ""
		}
		return interp
	}
	return ""
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

// toolchainExecEnv exposes an already published toolchain to a sandbox
// command. The toolchain is only a fallback, so a command never copies it and
// never fails because it is missing; native CLI preparation creates it.
func (s *dockerSandbox) toolchainExecEnv() []string {
	if isolatedRuntimeImage(s.dockerfile) != "" {
		return nil
	}
	dir := readyToolchainDir()
	if dir == "" {
		return nil
	}
	return []string{"-e", toolchainEnv + "=" + dir}
}

// toolchainCLIExecEnv creates the toolchain, with binary when the platform
// image bundles it, for native CLI preparation.
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
