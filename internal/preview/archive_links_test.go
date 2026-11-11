package preview

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveDirRejectsHardlinkToFileOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	dist := filepath.Join(workspace, "dist")
	if err := os.Mkdir(dist, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.html")
	if err := os.WriteFile(outside, []byte("private content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(dist, "index.html")); err != nil {
		t.Skipf("filesystem does not support hard links: %v", err)
	}
	var archive bytes.Buffer
	_, err := ArchiveDir(workspace, "dist", "index.html", Limits{MaxFiles: 10, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20}, &archive)
	if !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("hardlinked preview file was not rejected: %v", err)
	}
	if archive.Len() != 0 {
		t.Fatal("archive wrote content before rejecting the hardlink")
	}
}
