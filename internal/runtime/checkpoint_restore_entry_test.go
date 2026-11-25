package runtime

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openCheckpointTestRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	base, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	return base, dir
}

func TestRestoreCheckpointEntryMaterializesEntryTypes(t *testing.T) {
	base, dir := openCheckpointTestRoot(t)
	if err := restoreCheckpointEntry(base, "a/b", &tar.Header{Typeflag: tar.TypeDir, Mode: 0750}, nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "a", "b")); err != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
	body := "hello"
	if err := restoreCheckpointEntry(base, "nested/file.txt", &tar.Header{Typeflag: tar.TypeReg, Mode: 0640, Size: int64(len(body))}, strings.NewReader(body+"trailing")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "nested", "file.txt"))
	if err != nil || string(got) != body {
		t.Fatalf("file content = %q, %v", got, err)
	}
	if err := restoreCheckpointEntry(base, "link", &tar.Header{Typeflag: tar.TypeSymlink, Linkname: "../outside"}, nil); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "link")); err != nil || target != "../outside" {
		t.Fatalf("symlink target = %q, %v", target, err)
	}
}

func TestRestoreCheckpointEntryRejectsInvalidEntries(t *testing.T) {
	base, _ := openCheckpointTestRoot(t)
	if err := restoreCheckpointEntry(base, "dev", &tar.Header{Typeflag: tar.TypeChar}, nil); err == nil || err.Error() != "unsupported checkpoint entry" {
		t.Fatalf("unsupported entry error = %v", err)
	}
	header := &tar.Header{Typeflag: tar.TypeReg, Mode: 0600, Size: 1}
	if err := restoreCheckpointEntry(base, "dup", header, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := restoreCheckpointEntry(base, "dup", header, strings.NewReader("y")); err == nil {
		t.Fatal("existing file must not be overwritten")
	}
	if err := restoreCheckpointEntry(base, "short", &tar.Header{Typeflag: tar.TypeReg, Mode: 0600, Size: 10}, strings.NewReader("abc")); err == nil {
		t.Fatal("truncated body must fail")
	}
}
