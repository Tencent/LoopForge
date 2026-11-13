package runtime

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointEntryHeaderNormalizesEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/outside/target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	for _, tc := range []struct {
		path     string
		linkname string
		size     int64
	}{
		{path: "dir"},
		{path: filepath.Join("dir", "file.txt"), size: 5},
		{path: "link", linkname: "/outside/target"},
	} {
		info, err := os.Lstat(filepath.Join(root, tc.path))
		if err != nil {
			t.Fatal(err)
		}
		header, err := checkpointEntryHeader(base, tc.path, info)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if header.Name != filepath.ToSlash(tc.path) || header.Linkname != tc.linkname || header.Size != tc.size {
			t.Fatalf("%s: unexpected header %+v", tc.path, header)
		}
		if header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
			t.Fatalf("%s: ownership not cleared: %+v", tc.path, header)
		}
	}
}

func TestCheckpointEntryHeaderRejectsSpecialFiles(t *testing.T) {
	root := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(root, "sock"))
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	defer listener.Close()
	base, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	info, err := os.Lstat(filepath.Join(root, "sock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = checkpointEntryHeader(base, "sock", info); err == nil || !strings.Contains(err.Error(), "unsupported checkpoint file: sock") {
		t.Fatalf("expected unsupported file error, got %v", err)
	}
}

func TestCopyCheckpointFileCopiesDeclaredSize(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	var out bytes.Buffer
	if err = copyCheckpointFile(base, &out, "a.txt", 7); err != nil || out.String() != "payload" {
		t.Fatalf("copy = %q, %v", out.String(), err)
	}
	if err = copyCheckpointFile(base, &out, "missing.txt", 1); err == nil {
		t.Fatal("expected error for missing file")
	}
	out.Reset()
	if err = copyCheckpointFile(base, &out, "a.txt", 100); err == nil {
		t.Fatal("expected short-file error when declared size exceeds content")
	}
}
