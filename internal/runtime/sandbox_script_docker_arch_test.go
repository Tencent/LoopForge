package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// installArchFakeDocker answers `docker image inspect` with imageReply (exit
// code imageExit) and `docker exec box uname -m` with machine (exit unameExit).
func installArchFakeDocker(t *testing.T, imageReply string, imageExit int, machine string, unameExit int) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  "image inspect --format {{.Architecture}} "*)
    if [ "$ARCH_IMAGE_EXIT" = 0 ]; then printf '%s\n' "$ARCH_IMAGE_REPLY"; else printf '%s\n' "$ARCH_IMAGE_REPLY" >&2; fi
    exit "$ARCH_IMAGE_EXIT" ;;
  "exec box uname -m") printf '%s\n' "$ARCH_MACHINE"; exit "$ARCH_UNAME_EXIT" ;;
esac
exit 99
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARCH_IMAGE_REPLY", imageReply)
	t.Setenv("ARCH_IMAGE_EXIT", strconv.Itoa(imageExit))
	t.Setenv("ARCH_MACHINE", machine)
	t.Setenv("ARCH_UNAME_EXIT", strconv.Itoa(unameExit))
}

func TestDockerScriptArchitecturePrefersImageMetadata(t *testing.T) {
	installArchFakeDocker(t, "arm64", 0, "x86_64", 0)
	arch, err := dockerScriptArchitecture(context.Background(), "box", "sha256:abc")
	if err != nil || arch != "arm64" {
		t.Fatalf("arch=%q err=%v", arch, err)
	}
}

// Issue #660: the workspace container kept running but its image could no
// longer be inspected, so every turn failed before the CLI started.
func TestDockerScriptArchitectureFallsBackToContainerUname(t *testing.T) {
	for machine, want := range map[string]string{"x86_64": "amd64", "aarch64": "arm64"} {
		installArchFakeDocker(t, "Error response from daemon: No such image: sha256:abc", 1, machine, 0)
		arch, err := dockerScriptArchitecture(context.Background(), "box", "sha256:abc")
		if err != nil || arch != want {
			t.Fatalf("machine %s: arch=%q err=%v", machine, arch, err)
		}
	}
}

func TestDockerScriptArchitectureReportsBothCauses(t *testing.T) {
	installArchFakeDocker(t, "Error response from daemon: No such image: sha256:abc", 1, "OCI runtime exec failed", 1)
	_, err := dockerScriptArchitecture(context.Background(), "box", "sha256:abc")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, part := range []string{"inspect script environment architecture", "No such image: sha256:abc", "OCI runtime exec failed"} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q lacks %q", err, part)
		}
	}

	installArchFakeDocker(t, "Error response from daemon: No such image: sha256:abc", 1, "riscv64", 0)
	if _, err := dockerScriptArchitecture(context.Background(), "box", "sha256:abc"); err == nil || !strings.Contains(err.Error(), `unrecognized machine "riscv64"`) {
		t.Fatalf("unsupported machine err=%v", err)
	}
}
