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
	if owned, err := ownedMask(root, "missing/AGENTS.md", nil, nil); err != nil || owned {
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
	if owned, err := ownedMask(root, "AGENTS.md", nil, []unix.Stat_t{stat}); err != nil || owned {
		t.Fatalf("writable target = %v, %v", owned, err)
	}
	if owned, err := ownedMask(root, "AGENTS.md", nil, nil); err != nil || owned {
		t.Fatalf("foreign target = %v, %v", owned, err)
	}
}

func TestMountTargetsReportsScannerErrors(t *testing.T) {
	line := "36 35 98:0 / /workspace/" + strings.Repeat("a", 1024*1024) + " rw - ext4 /dev/root rw\n"
	if _, err := mountTargets(strings.NewReader(line)); err == nil {
		t.Fatal("expected oversized mountinfo line to fail")
	}
}

func TestOwnedMaskTrimsWorkspacePrefixAndReportsInspectErrors(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Targets come from mountinfo as absolute /workspace paths; they must be
	// resolved relative to the opened root rather than rejected as escapes.
	if owned, err := ownedMask(root, "/workspace/AGENTS.md", nil, nil); err != nil || owned {
		t.Fatalf("workspace-prefixed target = %v, %v", owned, err)
	}
	if owned, err := ownedMask(root, "/workspace/missing.md", nil, nil); err != nil || owned {
		t.Fatalf("workspace-prefixed missing target = %v, %v", owned, err)
	}
	// A path through a regular file is not "missing": it must fail closed
	// instead of being silently skipped.
	owned, err := ownedMask(root, "/workspace/AGENTS.md/nested", nil, nil)
	if err == nil || owned || !strings.Contains(err.Error(), "inspect script instruction mask /workspace/AGENTS.md/nested") {
		t.Fatalf("not-a-directory target = %v, %v", owned, err)
	}
}

func TestOwnedMaskResolvesAliasRoots(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(dir, "alias")
	if err := os.MkdirAll(alias, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alias, "AGENTS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	target := alias + "/AGENTS.md"
	// Without the alias the target is outside the workspace and not owned; a
	// missing file under an alias is likewise not owned, not an error.
	if owned, err := ownedMask(root, target, nil, nil); err != nil || owned {
		t.Fatalf("alias target without alias = %v, %v", owned, err)
	}
	if owned, err := ownedMask(root, alias+"/missing.md", []string{alias}, nil); err != nil || owned {
		t.Fatalf("alias missing target = %v, %v", owned, err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(target, &stat); err != nil {
		t.Fatal(err)
	}
	// Writable filesystem: same inode as the "source" but still not a mask.
	if owned, err := ownedMask(root, target, []string{alias}, []unix.Stat_t{stat}); err != nil || owned {
		t.Fatalf("alias writable target = %v, %v", owned, err)
	}
}
