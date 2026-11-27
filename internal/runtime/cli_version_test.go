package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCLIVersionFromOutput(t *testing.T) {
	for _, tc := range []struct{ provider, output, want string }{
		{"codebuddy", "2.185.0\n", "2.185.0"},
		{"codex", "codex-cli 0.163.0\n", "0.163.0"},
		{"claude-code", "2.1.326 (Claude Code)\n", "2.1.326"},
		{AgentCLIOpenCode, "1.19.6\n", "1.19.6"},
		{AgentCLICursor, "2026.11.13-eacade1\n", "2026.11.13-eacade1"},
		{AgentCLIAntigravity, "1.5.13\n", "1.5.13"},
		{AgentCLIKimi, "2.7.1\n", "2.7.1"},
		{AgentCLIPi, "1.5.2\n", "1.5.2"},
		{AgentCLIDeepSeek, "0.2.2-rc.1+build\n", "0.2.2-rc.1+build"},
		{AgentCLIZCode, "0.17.1\n", "0.17.1"},
		{AgentCLIZCode, "ZCode CLI v0.17.1\n", "0.17.1"},
		{AgentCLIZCode, "Node 20.20.2\nUpgrade available: 0.17.1\n", ""},
	} {
		if got := cliVersionFromOutput(tc.provider, tc.output); got != tc.want {
			t.Errorf("%s version from %q = %q, want %q", tc.provider, tc.output, got, tc.want)
		}
	}
}

func TestCLIVersionProbeDiscardsOversizedProcessOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "printf '1.2.3\\n'; head -c 50000 /dev/zero")
	if got := runCLIVersionProbe(cmd); got != "" {
		t.Fatalf("accepted %d bytes of oversized CLI version output", len(got))
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
}

func TestObserveCLIVersionUsesExecutablePackageMetadata(t *testing.T) {
	pkg := t.TempDir()
	bin := filepath.Join(pkg, "bin", "dsh")
	if err := os.Mkdir(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@deepseek-ai/dsh","version":"0.2.2-rc.1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "dsh")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	metadata := make(chan [2]string, 1)
	defer ObserveCLIVersion(context.Background(), nil, AgentCLIDeepSeek, link, func(version, source string) {
		metadata <- [2]string{version, source}
	})()
	select {
	case got := <-metadata:
		if got != [2]string{"0.2.2-rc.1", "package_metadata"} {
			t.Fatalf("version = %q", got)
		}
	default:
		t.Fatal("installation metadata was not reported immediately")
	}
}

func TestObserveCLIVersionUsesIsolatedHomeAndEnvironment(t *testing.T) {
	t.Setenv("PROBE_FIXTURE_SECRET", "only-for-the-normal-process")
	bin := filepath.Join(t.TempDir(), "codebuddy")
	script := `#!/bin/sh
test "$1" = "--version" || exit 1
test -z "${CODEBUDDY_API_KEY:-}${PROBE_FIXTURE_SECRET:-}" || exit 2
test "$HOME" = "$TMPDIR" && test . -ef "$HOME" || exit 3
printf 'Node.js 20.20.2\nCodeBuddy v2.185.0-beta.1+fixture\n'
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	metadata := make(chan [2]string, 1)
	defer ObserveCLIVersion(ctx, nil, "codebuddy", bin, func(version, source string) {
		metadata <- [2]string{version, source}
	})()
	select {
	case got := <-metadata:
		if got != [2]string{"2.185.0-beta.1+fixture", "version_probe"} {
			t.Fatalf("version = %q", got)
		}
	case <-ctx.Done():
		t.Fatal("isolated CLI probe did not report a version")
	}
}
