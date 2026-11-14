package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/gitwork"
)

func TestWorkspaceCheckpointRoundTrip(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{"README.md": []byte("uncommitted\n"), "src/tool.sh": []byte("#!/bin/sh\n"), "binary": {0, 255, 1}, ".git/HEAD": []byte("ref: refs/heads/main\n"), ".agentflow/auth/token": []byte("secret"), "node_modules/huge": []byte("cache")}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("src/tool.sh", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	archive, err := CreateWorkspaceCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if err = RestoreWorkspaceCheckpoint(dest, archive); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"README.md", "src/tool.sh", "binary", ".git/HEAD"} {
		body, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || !bytes.Equal(body, files[name]) {
			t.Fatalf("%s lost: %q %v", name, body, err)
		}
	}
	for _, name := range []string{".agentflow", "node_modules"} {
		if _, err := os.Stat(filepath.Join(dest, name)); !os.IsNotExist(err) {
			t.Fatalf("excluded path restored: %s", name)
		}
	}
	if info, err := os.Stat(filepath.Join(dest, "src/tool.sh")); err != nil || info.Mode().Perm()&0100 == 0 {
		t.Fatal("executable mode lost")
	}
	if link, err := os.Readlink(filepath.Join(dest, "link")); err != nil || link != "src/tool.sh" {
		t.Fatal("link lost")
	}
	if err = RestoreWorkspaceCheckpoint(dest, archive); err == nil {
		t.Fatal("existing destination overwritten")
	}
}

func TestWorkspaceArchiveExcludesPrivateNodeRefs(t *testing.T) {
	for _, packed := range []bool{false, true} {
		t.Run(map[bool]string{false: "loose", true: "packed"}[packed], func(t *testing.T) {
			root := t.TempDir()
			git := func(dir string, args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %s: %v", args, out, err)
				}
				return strings.TrimSpace(string(out))
			}
			git(root, "init", "-q")
			git(root, "config", "core.logAllRefUpdates", "always")
			git(root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "base")
			if _, err := gitwork.CreateNodeCheckpoint(context.Background(), root, "123456789abcdef0123456789abcdef0"); err != nil {
				t.Fatal(err)
			}
			if packed {
				git(root, "pack-refs", "--all")
			}
			var archive bytes.Buffer
			if err := CreateWorkspaceCheckpointToFiltered(root, &archive, true); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "restored")
			if err := RestoreWorkspaceCheckpointFrom(dest, &archive); err != nil {
				t.Fatal(err)
			}
			if refs := git(dest, "for-each-ref", "--format=%(refname)", "refs/loopforge"); refs != "" {
				t.Fatalf("private refs survived: %s", refs)
			}
			if _, err := os.Stat(filepath.Join(dest, ".git", "logs", "refs", "loopforge")); !os.IsNotExist(err) {
				t.Fatalf("private reflog survived: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".git", "logs", "refs", "loopforge")); err != nil {
				t.Fatalf("expected source reflog: %v", err)
			}
			if git(dest, "rev-parse", "HEAD") != git(root, "rev-parse", "HEAD") {
				t.Fatal("normal Git history was not preserved")
			}
			if refs := git(root, "for-each-ref", "--format=%(refname)", "refs/loopforge"); refs == "" {
				t.Fatal("archive modified live checkpoints")
			}
		})
	}
}
func makeCheckpoint(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	for _, h := range headers {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestWorkspaceCheckpointRejectsUnsafeAndCorruptArchivesAtomically(t *testing.T) {
	cases := map[string][]byte{"traversal": makeCheckpoint(t, &tar.Header{Name: "../escape", Mode: 0600, Typeflag: tar.TypeReg}), "absolute": makeCheckpoint(t, &tar.Header{Name: "/escape", Mode: 0600, Typeflag: tar.TypeReg}), "symlink-parent": makeCheckpoint(t, &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../escape"}, &tar.Header{Name: "link/pwned", Mode: 0600, Typeflag: tar.TypeReg}), "secret": makeCheckpoint(t, &tar.Header{Name: ".agentflow/token", Typeflag: tar.TypeReg}), "duplicate": makeCheckpoint(t, &tar.Header{Name: "file", Typeflag: tar.TypeReg}, &tar.Header{Name: "file", Typeflag: tar.TypeReg}), "hardlink": makeCheckpoint(t, &tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "escape"})}
	good := EmptyWorkspaceCheckpoint()
	cases["truncated"] = good[:len(good)-4]
	bad := append([]byte{}, good...)
	bad[len(bad)-5] ^= 0xff
	cases["checksum"] = bad
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "workspace")
			if err := RestoreWorkspaceCheckpoint(dest, data); err == nil {
				t.Fatal("invalid archive accepted")
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatal("partial workspace exposed")
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatal("staging files leaked")
			}
		})
	}
}

// Repositories may commit absolute or dangling links (for example an editor
// skills directory pointing at a developer's home). They must not block the
// save that authorizes reclamation, and restore must reproduce them exactly.
func TestWorkspaceCheckpointPreservesExternalSymlinks(t *testing.T) {
	root := t.TempDir()
	links := map[string]string{"server/.claude/skills": "/workdir/Users/dev/skills", "tools/run/app": "../../../outside/app", "local": "server"}
	for name, target := range links {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := CreateWorkspaceCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateWorkspaceCheckpoint(archive); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if err = RestoreWorkspaceCheckpoint(dest, archive); err != nil {
		t.Fatal(err)
	}
	for name, target := range links {
		if got, err := os.Readlink(filepath.Join(dest, name)); err != nil || got != target {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
}

// Restoring an external link is safe only because nothing is ever written
// through it: a later entry below the link must fail atomically.
func TestWorkspaceCheckpointRestoreNeverWritesThroughExternalSymlinks(t *testing.T) {
	for name, target := range map[string]func(outside string) string{
		"absolute": func(outside string) string { return outside },
		"relative": func(string) string { return "../outside" },
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			outside := filepath.Join(parent, "outside")
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			for _, entry := range []*tar.Header{
				{Name: "pwned", Mode: 0600, Typeflag: tar.TypeReg},
				{Name: "sub/pwned", Mode: 0600, Typeflag: tar.TypeReg},
				{Name: "sub", Mode: 0700, Typeflag: tar.TypeDir},
			} {
				data := makeCheckpoint(t, &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: target(outside)}, &tar.Header{Name: "link/" + entry.Name, Mode: entry.Mode, Typeflag: entry.Typeflag})
				dest := filepath.Join(parent, "workspace")
				if err := RestoreWorkspaceCheckpoint(dest, data); err == nil {
					t.Fatalf("%s written through external link", entry.Name)
				}
				if _, err := os.Lstat(dest); !os.IsNotExist(err) {
					t.Fatal("partial workspace exposed")
				}
				if escaped, _ := os.ReadDir(outside); len(escaped) != 0 {
					t.Fatalf("restore escaped workspace: %v", escaped)
				}
				if entries, _ := os.ReadDir(parent); len(entries) != 1 {
					t.Fatal("staging files leaked")
				}
			}
		})
	}
}
