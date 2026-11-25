package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// The CLIs the platform image bundles are scoped npm packages
// (@openai/codex, @tencent-ai/codebuddy-code, @deepseek-ai/dsh), so the
// launcher parser must keep the scope, and npm itself stays in the base copy.
func TestToolchainPackageParsesLauncherTargets(t *testing.T) {
	for _, tc := range []struct {
		link string
		want string
		ok   bool
	}{
		{"../lib/node_modules/@openai/codex/bin/codex.js", "@openai/codex", true},
		{"../lib/node_modules/@tencent-ai/codebuddy-code/bin/codebuddy", "@tencent-ai/codebuddy-code", true},
		{"../lib/node_modules/fakepkg/bin/fake.js", "fakepkg", true},
		{"../lib/node_modules/npm/bin/npm-cli.js", "", false},
		{"../lib/node_modules/@scope/only", "", false},
		{"../lib/node_modules/pkg", "", false},
		{"/usr/local/lib/node_modules/fakepkg/bin/fake.js", "", false},
		{"node", "", false},
		{"", "", false},
	} {
		got, ok := toolchainPackage(tc.link)
		if got != tc.want || ok != tc.ok {
			t.Errorf("toolchainPackage(%q) = %q, %v; want %q, %v", tc.link, got, ok, tc.want, tc.ok)
		}
	}
}

// The toolchain directory is keyed by the platform build; an unusable commit
// file falls back to the node_modules mtime and then to "default".
func TestToolchainKeyFallbacks(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	if got := toolchainKey(); got != "testbuild" {
		t.Fatalf("commit key = %q, want testbuild", got)
	}
	stamp := time.Unix(1700000000, 0)
	if err := os.Chtimes(filepath.Join(prefix, "lib", "node_modules"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	mtimeKey := "mtime-" + strconv.FormatInt(stamp.Unix(), 10)
	for _, commit := range []string{"", "  \n", "abc def", "../escape", `a\b`, "two\nlines"} {
		if err := os.WriteFile(toolchainCommitFile, []byte(commit), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := toolchainKey(); got != mtimeKey {
			t.Errorf("commit %q: key = %q, want %q", commit, got, mtimeKey)
		}
	}
	if err := os.Remove(toolchainCommitFile); err != nil {
		t.Fatal(err)
	}
	if got := toolchainKey(); got != mtimeKey {
		t.Fatalf("missing commit file: key = %q, want %q", got, mtimeKey)
	}
	toolchainNodePrefix = t.TempDir()
	if got := toolchainKey(); got != "default" {
		t.Fatalf("no commit and no node_modules: key = %q, want default", got)
	}
}

// Outside the platform image (no executable node in the prefix) or without a
// CLI directory there is nothing to mount, and nothing may be written.
func TestRuntimeToolchainDirDisabledOutsidePlatformImage(t *testing.T) {
	prefix := writeToolchainFixture(t)
	cliDir := t.TempDir()
	useToolchainFixture(t, prefix, "")
	if dir, err := RuntimeToolchainDir(); dir != "" || err != nil {
		t.Fatalf("no CLI dir: got %q, %v", dir, err)
	}
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", cliDir)
	if err := os.Chmod(filepath.Join(prefix, "bin", "node"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dir, err := RuntimeToolchainDir(); dir != "" || err != nil {
		t.Fatalf("non-executable node: got %q, %v", dir, err)
	}
	if err := os.Remove(filepath.Join(prefix, "bin", "node")); err != nil {
		t.Fatal(err)
	}
	if dir, err := RuntimeToolchainDir(); dir != "" || err != nil {
		t.Fatalf("missing node: got %q, %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(cliDir, "toolchain")); !os.IsNotExist(err) {
		t.Fatalf("toolchain directory created outside the platform image: %v", err)
	}
}

// The base copy holds node, npm, npx and Python only; bundled CLIs are copied
// on demand. A half-written copy without .ready is replaced, a ready one reused.
func TestRuntimeToolchainDirCopiesBaseToolsAndPython(t *testing.T) {
	prefix := writeToolchainFixture(t)
	cliDir := t.TempDir()
	useToolchainFixture(t, prefix, cliDir)
	toolchainPythonDir = filepath.Join(prefix, "python")
	if err := os.MkdirAll(filepath.Join(toolchainPythonDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolchainPythonDir, "bin", "python3"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cliDir, "toolchain", "testbuild")
	if err := os.MkdirAll(filepath.Join(want, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(want, "bin", "stale")
	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	dir, err := RuntimeToolchainDir()
	if err != nil || dir != want {
		t.Fatalf("RuntimeToolchainDir() = %q, %v; want %q", dir, err, want)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("unfinished copy without .ready was kept: %v", err)
	}
	for _, path := range []string{"bin/node", "bin/npm", "bin/npx", "lib/node_modules/npm/bin/npm-cli.js", "python/bin/python3"} {
		if !fileIsExecutable(filepath.Join(dir, path)) {
			t.Errorf("%s missing or not executable in the toolchain", path)
		}
	}
	if link, err := os.Readlink(filepath.Join(dir, "bin", "npm")); err != nil || link != "../lib/node_modules/npm/bin/npm-cli.js" {
		t.Errorf("npm launcher = %q, %v; want the relative link kept", link, err)
	}
	for _, path := range []string{"bin/fakecli", "lib/node_modules/fakepkg"} {
		if _, err := os.Lstat(filepath.Join(dir, path)); !os.IsNotExist(err) {
			t.Errorf("bundled CLI %s copied eagerly: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".ready")); err != nil {
		t.Fatalf("ready marker: %v", err)
	}

	marker := filepath.Join(dir, "bin", "kept")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if again, err := RuntimeToolchainDir(); err != nil || again != dir {
		t.Fatalf("second call = %q, %v; want %q", again, err, dir)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("ready toolchain was recopied: %v", err)
	}
}

func TestRuntimeToolchainDirWithoutPythonDir(t *testing.T) {
	prefix := writeToolchainFixture(t)
	cliDir := t.TempDir()
	useToolchainFixture(t, prefix, cliDir)
	dir, err := RuntimeToolchainDir()
	if err != nil || dir == "" {
		t.Fatalf("RuntimeToolchainDir() = %q, %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "python")); !os.IsNotExist(err) {
		t.Fatalf("python directory appeared without a platform Python: %v", err)
	}
}

// A scoped package with several launchers is copied once and every launcher
// pointing into it is linked, so `codex` and its aliases all resolve.
func TestEnsureToolchainCLICopiesScopedPackageAndAllItsLaunchers(t *testing.T) {
	prefix := writeToolchainFixture(t)
	cliDir := t.TempDir()
	useToolchainFixture(t, prefix, cliDir)
	pkg := filepath.Join(prefix, "lib", "node_modules", "@scope", "agent")
	if err := os.MkdirAll(filepath.Join(pkg, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"bin/agent.js", "bin/agent-mcp.js"} {
		if err := os.WriteFile(filepath.Join(pkg, file), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{
		"agent":     "../lib/node_modules/@scope/agent/bin/agent.js",
		"agent-mcp": "../lib/node_modules/@scope/agent/bin/agent-mcp.js",
	} {
		if err := os.Symlink(target, filepath.Join(prefix, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := RuntimeToolchainDir()
	if err != nil || dir == "" {
		t.Fatalf("RuntimeToolchainDir() = %q, %v", dir, err)
	}
	if err := EnsureToolchainCLI(dir, "agent"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent", "agent-mcp"} {
		if !fileIsExecutable(filepath.Join(dir, "bin", name)) {
			t.Errorf("launcher %s does not resolve inside the toolchain", name)
		}
	}
	if !fileIsExecutable(filepath.Join(dir, "lib", "node_modules", "@scope", "agent", "bin", "agent.js")) {
		t.Fatal("scoped package not copied under its scope")
	}
	for _, path := range []string{"bin/fakecli", "lib/node_modules/fakepkg"} {
		if _, err := os.Lstat(filepath.Join(dir, path)); !os.IsNotExist(err) {
			t.Errorf("unrelated package %s copied: %v", path, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "lib", "node_modules", "@scope"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") {
			t.Fatalf("leftover stage %s", entry.Name())
		}
	}

	// Once the launcher exists, a later call must not recopy the package.
	kept := filepath.Join(dir, "lib", "node_modules", "@scope", "agent", "kept")
	if err := os.WriteFile(kept, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureToolchainCLI(dir, "agent-mcp"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("installed package was recopied: %v", err)
	}
}

// Binaries the platform image does not bundle as an npm launcher are left to
// the sandbox installer: nothing is copied and no lock is taken.
func TestEnsureToolchainCLIIgnoresUnbundledBinaries(t *testing.T) {
	prefix := writeToolchainFixture(t)
	cliDir := t.TempDir()
	useToolchainFixture(t, prefix, cliDir)
	dir := filepath.Join(cliDir, "toolchain", "testbuild")
	for _, tc := range []struct{ dir, binary string }{
		{"", "fakecli"},
		{dir, ""},
		{dir, "../bin/fakecli"},
		{dir, "bin/fakecli"},
		{dir, `bin\fakecli`},
		{dir, "npm"},
		{dir, "node"},
		{dir, "missing"},
	} {
		if err := EnsureToolchainCLI(tc.dir, tc.binary); err != nil {
			t.Errorf("EnsureToolchainCLI(%q, %q) = %v, want nil", tc.dir, tc.binary, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cliDir, "toolchain")); !os.IsNotExist(err) {
		t.Fatalf("unbundled binary touched the toolchain directory: %v", err)
	}
}

// withToolchainPath wraps commands docker exec starts directly (the Python
// process supervisor): the toolchain is appended to PATH, so a tool the custom
// image ships still wins, and the original argv runs unchanged.
func TestWithToolchainPathAppendsMountedToolchain(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	image := filepath.Dir(sh)
	toolchain := t.TempDir()
	if err := os.WriteFile(filepath.Join(toolchain, ".interp"), []byte(sh+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(env ...string) []string {
		t.Helper()
		argv := withToolchainPath([]string{"sh", "-c", `printf '%s|%s|%s' "$PATH" "$1" "$2"`, "argv0", "two words", "--flag"})
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append([]string{"PATH=" + image}, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return strings.Split(string(out), "|")
	}
	got := run(toolchainEnv + "=" + toolchain)
	if want := image + ":" + toolchain + "/bin:" + toolchain + "/python/bin"; got[0] != want {
		t.Fatalf("PATH = %q, want %q", got[0], want)
	}
	if got[1] != "two words" || got[2] != "--flag" {
		t.Fatalf("arguments not passed through: %q", got[1:])
	}
	for _, env := range [][]string{nil, {toolchainEnv + "="}, {toolchainEnv + "=" + filepath.Join(toolchain, "missing")}} {
		if got := run(env...); got[0] != image {
			t.Errorf("env %v: PATH = %q, want unchanged", env, got[0])
		}
	}
	for _, argv := range [][]string{dockerConversationProcessArgv("/tmp/p", []string{"codex"}), dockerConversationStopArgv("/tmp/p")} {
		if len(argv) < 4 || argv[0] != "sh" || argv[1] != "-c" || !strings.HasPrefix(argv[2], toolchainPathSetup) || argv[3] != "python3" {
			t.Fatalf("supervisor argv does not set up the toolchain PATH first: %q", argv)
		}
	}
}

// The base copy records node's ELF loader in .interp, which the sandbox PATH
// setup requires; a node that is not a dynamic ELF file leaves no .interp.
func TestRuntimeToolchainDirRecordsNodeLoader(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	sh, err = filepath.EvalSymlinks(sh)
	if err != nil {
		t.Fatal(err)
	}
	interp := elfInterpreter(sh)
	if interp == "" {
		t.Skipf("%s is not a dynamic ELF executable", sh)
	}
	if !strings.HasPrefix(interp, "/") || strings.ContainsAny(interp, "\x00\r\n") {
		t.Fatalf("elfInterpreter(%s) = %q", sh, interp)
	}
	if _, err := os.Stat(interp); err != nil {
		t.Fatalf("loader %q of %s does not exist: %v", interp, sh, err)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), writeScript(t, "#!/bin/sh\n"), writeScript(t, "\x7fELF but truncated")} {
		if got := elfInterpreter(path); got != "" {
			t.Errorf("elfInterpreter(%s) = %q, want empty", path, got)
		}
	}

	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	dir, err := RuntimeToolchainDir()
	if err != nil || dir == "" {
		t.Fatalf("RuntimeToolchainDir() = %q, %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".interp")); !os.IsNotExist(err) {
		t.Fatalf("script node produced .interp: %v", err)
	}

	raw, err := os.ReadFile(sh)
	if err != nil {
		t.Fatal(err)
	}
	prefix = writeToolchainFixture(t)
	if err := os.WriteFile(filepath.Join(prefix, "bin", "node"), raw, 0o755); err != nil {
		t.Fatal(err)
	}
	useToolchainFixture(t, prefix, t.TempDir())
	dir, err = RuntimeToolchainDir()
	if err != nil || dir == "" {
		t.Fatalf("RuntimeToolchainDir() = %q, %v", dir, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, ".interp")); err != nil || string(got) != interp+"\n" {
		t.Fatalf(".interp = %q, %v; want %q", got, err, interp+"\n")
	}
}

func writeScript(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// Native CLI preparation creates the toolchain for custom images only:
// sandboxes of the platform's isolated image already carry these tools.
func TestToolchainCLIExecEnvOnlyForCustomImages(t *testing.T) {
	prefix := writeToolchainFixture(t)
	cliDir := t.TempDir()
	useToolchainFixture(t, prefix, cliDir)
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "loopforge-runtime:test")

	platform := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	if env, err := platform.toolchainCLIExecEnv("fakecli"); err != nil || env != nil {
		t.Fatalf("platform image CLI exec env = %v, %v; want none", env, err)
	}
	if _, err := os.Stat(filepath.Join(cliDir, "toolchain")); !os.IsNotExist(err) {
		t.Fatalf("platform image sandbox copied the toolchain: %v", err)
	}

	custom := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: "FROM debian:bookworm\n"}}
	dir := filepath.Join(cliDir, "toolchain", "testbuild")
	env, err := custom.toolchainCLIExecEnv("fakecli")
	if err != nil || !slices.Equal(env, []string{"-e", toolchainEnv + "=" + dir}) {
		t.Fatalf("custom image CLI exec env = %v, %v", env, err)
	}
	if !fileIsExecutable(filepath.Join(dir, "bin", "fakecli")) {
		t.Fatal("CLI preparation did not make the bundled CLI available")
	}
	if got := custom.toolchainExecEnv(); !slices.Equal(got, env) {
		t.Fatalf("sandbox commands after preparation: env = %v, want %v", got, env)
	}
	// A dockerfile with extra files is a custom image even with the same content.
	withFiles := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile, Files: map[string]string{"extra": "x"}}}
	if got := withFiles.toolchainExecEnv(); !slices.Equal(got, env) {
		t.Fatalf("isolated content with extra files: env = %v, want %v", got, env)
	}

	// Outside the platform image even a custom-image sandbox gets no mount.
	if err := os.Remove(filepath.Join(prefix, "bin", "node")); err != nil {
		t.Fatal(err)
	}
	useToolchainFixture(t, prefix, t.TempDir())
	if env, err := custom.toolchainCLIExecEnv("fakecli"); err != nil || env != nil {
		t.Fatalf("no platform toolchain: env = %v, %v; want none", env, err)
	}
	if got := custom.toolchainExecEnv(); got != nil {
		t.Fatalf("no platform toolchain: exec env = %v, want none", got)
	}
}
