package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
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
	cases := map[string][]byte{"traversal": makeCheckpoint(t, &tar.Header{Name: "../escape", Mode: 0600, Typeflag: tar.TypeReg}), "absolute": makeCheckpoint(t, &tar.Header{Name: "/escape", Mode: 0600, Typeflag: tar.TypeReg}), "symlink": makeCheckpoint(t, &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../escape"}), "secret": makeCheckpoint(t, &tar.Header{Name: ".agentflow/token", Typeflag: tar.TypeReg}), "duplicate": makeCheckpoint(t, &tar.Header{Name: "file", Typeflag: tar.TypeReg}, &tar.Header{Name: "file", Typeflag: tar.TypeReg}), "hardlink": makeCheckpoint(t, &tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "escape"})}
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
	root := t.TempDir()
	if err := os.Symlink("../escape", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateWorkspaceCheckpoint(root); err == nil {
		t.Fatal("external link captured")
	}
}
