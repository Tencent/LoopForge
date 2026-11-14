package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerMachineArchitectureMapsUnameToHelperArch(t *testing.T) {
	for machine, want := range map[string]string{
		"x86_64":     "amd64",
		"amd64":      "amd64",
		"aarch64":    "arm64",
		"arm64":      "arm64",
		" aarch64\n": "arm64",
		"x86_64\r\n": "amd64",
		"armv7l":     "",
		"riscv64":    "",
		"i686":       "",
		"":           "",
	} {
		if got := dockerMachineArchitecture(machine); got != want {
			t.Fatalf("dockerMachineArchitecture(%q) = %q, want %q", machine, got, want)
		}
	}
}

// installArchFallbackDocker wraps cvRInstallFakeDocker: the workspace image can
// no longer be inspected and `uname -m` in the workspace container reports
// machine (exit unameExit). Every other call reaches the wrapped fake and is
// appended to the returned log.
func installArchFallbackDocker(t *testing.T, workspace, machine, unameExit string) string {
	t.Helper()
	cvRInstallFakeDocker(t, workspace)
	inner, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "docker.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$ARCH_FALLBACK_LOG"
case "$*" in
  "image inspect --format {{.Architecture}} "*) echo "Error response from daemon: No such image: $5" >&2; exit 1 ;;
  "exec box uname -m")
    if [ "$ARCH_FALLBACK_UNAME_EXIT" = 0 ]; then printf '%s\n' "$ARCH_FALLBACK_MACHINE"; else printf '%s\n' "$ARCH_FALLBACK_MACHINE" >&2; fi
    exit "$ARCH_FALLBACK_UNAME_EXIT" ;;
esac
exec "$ARCH_FALLBACK_INNER" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARCH_FALLBACK_LOG", log)
	t.Setenv("ARCH_FALLBACK_INNER", inner)
	t.Setenv("ARCH_FALLBACK_MACHINE", machine)
	t.Setenv("ARCH_FALLBACK_UNAME_EXIT", unameExit)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// Issue #660 end to end: the running workspace container's image metadata is
// gone, so ensureScriptContainer must take the helper architecture from the
// container itself. Only the arm64 helper is deployed, so resolving any other
// architecture (or skipping the fallback) fails the setup.
func TestEnsureScriptContainerUsesContainerArchWhenImageMetadataIsGone(t *testing.T) {
	workspace := t.TempDir()
	log := installArchFallbackDocker(t, workspace, "aarch64", "0")
	gate := filepath.Join(t.TempDir(), "gate-{os}-{arch}")
	if err := os.WriteFile(strings.NewReplacer("{os}", "linux", "{arch}", "arm64").Replace(gate), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", gate)
	sb := &dockerSandbox{name: "box", workspace: workspace, started: true}
	if err := sb.ensureScriptContainer(context.Background()); err != nil {
		t.Fatalf("ensureScriptContainer: %v", err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(raw)
	for _, want := range []string{"image inspect --format {{.Architecture}} base-image", "exec box uname -m", "create --name box-scripts"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("docker calls lack %q:\n%s", want, calls)
		}
	}
}

func TestEnsureScriptContainerStopsBeforeCreateWhenArchIsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, machine, exit, want string
	}{
		{"uname fails", "OCI runtime exec failed: container is restarting", "1", "OCI runtime exec failed"},
		{"unsupported machine", "riscv64", "0", `unrecognized machine "riscv64"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			log := installArchFallbackDocker(t, workspace, tc.machine, tc.exit)
			sb := &dockerSandbox{name: "box", workspace: workspace, started: true}
			err := sb.ensureScriptContainer(context.Background())
			if err == nil {
				t.Fatal("expected setup error")
			}
			for _, part := range []string{"inspect script environment architecture", "image base-image", "No such image: base-image", tc.want} {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("error %q lacks %q", err, part)
				}
			}
			raw, _ := os.ReadFile(log)
			if strings.Contains(string(raw), "create ") {
				t.Fatalf("script container created without a known architecture:\n%s", raw)
			}
			if !sb.started {
				t.Fatal("a running workspace with unreadable metadata must stay started")
			}
		})
	}
}
