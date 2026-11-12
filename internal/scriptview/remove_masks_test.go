//go:build linux

package scriptview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMountTargetsDecodesMountPoints(t *testing.T) {
	mountinfo := strings.Join([]string{
		`36 35 98:0 /mnt1 /workspace/project\040name/AGENTS.md rw,noatime master:1 - ext3 /dev/root rw`,
		`37 35 98:0 / /workspace/.claude ro - overlay overlay ro`,
		`short line`,
		``,
	}, "\n")
	mounts, err := mountTargets(strings.NewReader(mountinfo))
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 2 || !mounts["/workspace/project name/AGENTS.md"] || !mounts["/workspace/.claude"] {
		t.Fatalf("mounts = %v", mounts)
	}
}

func TestMaskSourcesRequiresReadOnlyPlatformSources(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := maskSources(root); err == nil || !strings.Contains(err.Error(), "open platform instruction mask source") {
		t.Fatalf("missing source error = %v", err)
	}
	isolation := filepath.Join(dir, ".agentflow", "isolation")
	if err := os.MkdirAll(filepath.Join(isolation, "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(isolation, "empty-file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := maskSources(root); err == nil || !strings.Contains(err.Error(), "not read-only") {
		t.Fatalf("writable source error = %v", err)
	}
}

func TestOwnedMaskIgnoresMissingAndForeignTargets(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if owned, err := ownedMask(root, "missing/AGENTS.md", nil); err != nil || owned {
		t.Fatalf("missing target = %v, %v", owned, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(filepath.Join(dir, "AGENTS.md"), &stat); err != nil {
		t.Fatal(err)
	}
	// Same inode but on a writable filesystem: not a platform mask.
	if owned, err := ownedMask(root, "AGENTS.md", []unix.Stat_t{stat}); err != nil || owned {
		t.Fatalf("writable target = %v, %v", owned, err)
	}
	if owned, err := ownedMask(root, "AGENTS.md", nil); err != nil || owned {
		t.Fatalf("foreign target = %v, %v", owned, err)
	}
}
