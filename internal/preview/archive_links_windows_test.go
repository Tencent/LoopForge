package preview

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectSpecialFailsClosedWhenWindowsLinkMetadataUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "removed.html")
	if err := os.WriteFile(path, []byte("preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := rejectSpecial(path, info); err == nil {
		t.Fatal("missing hardlink metadata was accepted")
	}
}

func TestArchiveDirAcceptsLongWindowsPaths(t *testing.T) {
	workspace := t.TempDir()
	dist := filepath.Join(workspace, "dist")
	assetDir := dist
	for len(assetDir) < 280 {
		assetDir = filepath.Join(assetDir, strings.Repeat("nested", 8))
	}
	if err := os.MkdirAll(assetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dist, "index.html"), filepath.Join(assetDir, "app.js")} {
		if err := os.WriteFile(path, []byte("preview content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var archive bytes.Buffer
	result, err := ArchiveDir(workspace, "dist", "index.html", Limits{MaxFiles: 10, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20}, &archive)
	if err != nil || result.FileCount != 2 {
		t.Fatalf("archive a nested Windows path: count=%d, error=%v", result.FileCount, err)
	}
}
