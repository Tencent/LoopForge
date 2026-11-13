package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func writeToolchainFixture(t *testing.T) string {
	t.Helper()
	prefix := t.TempDir()
	for _, dir := range []string{"bin", "lib/node_modules/npm/bin", "lib/node_modules/fakepkg/bin"} {
		if err := os.MkdirAll(filepath.Join(prefix, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"bin/node", "lib/node_modules/npm/bin/npm-cli.js", "lib/node_modules/fakepkg/bin/fake.js"} {
		if err := os.WriteFile(filepath.Join(prefix, file), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{
		"npm":     "../lib/node_modules/npm/bin/npm-cli.js",
		"npx":     "../lib/node_modules/npm/bin/npm-cli.js",
		"fakecli": "../lib/node_modules/fakepkg/bin/fake.js",
	} {
		if err := os.Symlink(target, filepath.Join(prefix, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	return prefix
}

func useToolchainFixture(t *testing.T, prefix, cliDir string) {
	t.Helper()
	oldPrefix, oldPython, oldCommit := toolchainNodePrefix, toolchainPythonDir, toolchainCommitFile
	toolchainNodePrefix = prefix
	toolchainPythonDir = filepath.Join(prefix, "no-python")
	toolchainCommitFile = filepath.Join(prefix, "commit")
	t.Cleanup(func() {
		toolchainNodePrefix, toolchainPythonDir, toolchainCommitFile = oldPrefix, oldPython, oldCommit
	})
	if err := os.WriteFile(toolchainCommitFile, []byte("testbuild\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", cliDir)
}

func startToolchainChild(t *testing.T, prefix, cliDir, marker string) (*exec.Cmd, *strings.Builder) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestToolchainChildProcess$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "LF_TOOLCHAIN_CHILD=1", "LF_TOOLCHAIN_PREFIX="+prefix, "LF_TOOLCHAIN_CLI="+cliDir, "LF_TOOLCHAIN_MARKER="+marker)
	out := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, out
}

// TestToolchainChildProcess is the second platform process of the tests below.
func TestToolchainChildProcess(t *testing.T) {
	if os.Getenv("LF_TOOLCHAIN_CHILD") != "1" {
		t.Skip("helper process")
	}
	useToolchainFixture(t, os.Getenv("LF_TOOLCHAIN_PREFIX"), os.Getenv("LF_TOOLCHAIN_CLI"))
	if marker := os.Getenv("LF_TOOLCHAIN_MARKER"); marker != "" {
		toolchainBeforeLock = func() { _ = os.WriteFile(marker, nil, 0o644) }
	}
	dir, err := RuntimeToolchainDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureToolchainCLI(dir, "fakecli"); err != nil {
		t.Fatal(err)
	}
	t.Log("TOOLCHAIN_DIR=" + dir)
}

// A process that passed the unlocked .ready check while another process
// published the toolchain must reuse it, not replace it.
func TestToolchainLatePublisherKeepsPublishedCLI(t *testing.T) {
	prefix := writeToolchainFixture(t)
	cliDir := t.TempDir()
	useToolchainFixture(t, prefix, cliDir)
	parent := filepath.Join(cliDir, "toolchain")
	dir := filepath.Join(parent, "testbuild")
	unlock, err := lockToolchain(parent)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "waiting")
	child, out := startToolchainChild(t, prefix, cliDir, marker)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			unlock()
			t.Fatalf("child never reached the toolchain lock: %s", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Publish as the other process would while the child waits on the lock.
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	published := filepath.Join(dir, "bin", "published-cli")
	if err := os.WriteFile(published, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ready"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := child.Wait(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "TOOLCHAIN_DIR="+dir) {
		t.Fatalf("child used another directory:\n%s", out)
	}
	if !fileIsExecutable(published) {
		t.Fatal("the late process replaced the published toolchain")
	}
	if !fileIsExecutable(filepath.Join(dir, "bin", "fakecli")) {
		t.Fatal("the late process did not add its CLI to the published toolchain")
	}
}

func TestToolchainConcurrentProcessesShareOneCopy(t *testing.T) {
	prefix := writeToolchainFixture(t)
	cliDir := t.TempDir()
	useToolchainFixture(t, prefix, cliDir)
	var wg sync.WaitGroup
	errs := make([]string, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			child, out := startToolchainChild(t, prefix, cliDir, "")
			if err := child.Wait(); err != nil {
				errs[i] = err.Error() + "\n" + out.String()
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != "" {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(cliDir, "toolchain", "testbuild")
	for _, name := range []string{"node", "npm", "npx", "fakecli"} {
		if _, err := os.Lstat(filepath.Join(dir, "bin", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(cliDir, "toolchain"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") {
			t.Fatalf("leftover stage %s", entry.Name())
		}
	}
}

// A sandbox command only uses a published toolchain: it never copies one and
// never fails without it.
func TestToolchainExecEnvUsesOnlyPublishedToolchain(t *testing.T) {
	prefix := writeToolchainFixture(t)
	cliDir := t.TempDir()
	useToolchainFixture(t, prefix, cliDir)
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "loopforge:test")
	custom := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: "FROM debian:bookworm\n"}}
	if env := custom.toolchainExecEnv(); env != nil {
		t.Fatalf("unpublished toolchain exposed: %q", env)
	}
	if _, err := os.Stat(filepath.Join(cliDir, "toolchain")); !os.IsNotExist(err) {
		t.Fatalf("a sandbox command started a toolchain copy: %v", err)
	}
	dir, err := RuntimeToolchainDir()
	if err != nil || dir == "" {
		t.Fatalf("dir=%q err=%v", dir, err)
	}
	if env := custom.toolchainExecEnv(); strings.Join(env, " ") != "-e "+toolchainEnv+"="+dir {
		t.Fatalf("published toolchain env = %q", env)
	}
	platform := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	if env := platform.toolchainExecEnv(); env != nil {
		t.Fatalf("platform image sandbox got the toolchain: %q", env)
	}
}

func TestToolchainPathSetupRequiresItsLoader(t *testing.T) {
	toolchain := t.TempDir()
	run := func(interp string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(toolchain, ".interp"), []byte(interp+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", "set -eu\nPATH=/usr/bin:/bin\n"+toolchainPathSetup+`printf %s "$PATH"`)
		cmd.Env = append(os.Environ(), toolchainEnv+"="+toolchain)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("setup failed: %v: %s", err, out)
		}
		return string(out)
	}
	if got, want := run("/bin/sh"), "/usr/bin:/bin:"+toolchain+"/bin:"+toolchain+"/python/bin"; got != want {
		t.Fatalf("PATH with loader present = %q, want %q", got, want)
	}
	if got := run(filepath.Join(toolchain, "missing-ld.so")); got != "/usr/bin:/bin" {
		t.Fatalf("PATH without loader = %q", got)
	}
	if err := os.Remove(filepath.Join(toolchain, ".interp")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "set -eu\nPATH=/usr/bin:/bin\n"+toolchainPathSetup+`printf %s "$PATH"`)
	cmd.Env = append(os.Environ(), toolchainEnv+"="+toolchain)
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "/usr/bin:/bin" {
		t.Fatalf("PATH without .interp = %q, %v", out, err)
	}
}
