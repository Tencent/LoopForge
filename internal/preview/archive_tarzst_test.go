package preview

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarZstTestEntries(t *testing.T) []fileEntry {
	t.Helper()
	dir := t.TempDir()
	var entries []fileEntry
	for _, name := range []string{"index.html", "app.js"} {
		abs := filepath.Join(dir, name)
		body := []byte("content of " + name)
		if err := os.WriteFile(abs, body, 0o644); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, fileEntry{abs: abs, name: name, mode: 0o644, size: int64(len(body))})
	}
	return entries
}

func TestWriteTarZstReportsHashAndSizeOfWrittenBytes(t *testing.T) {
	entries := tarZstTestEntries(t)
	var buf bytes.Buffer
	sum, n, err := writeTarZst(&buf, entries, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(buf.Bytes())
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("sum %s does not match written bytes", sum)
	}
	if n != int64(buf.Len()) || n == 0 {
		t.Fatalf("compressed size %d, written %d", n, buf.Len())
	}
	res, err := InspectTarZst(bytes.NewReader(buf.Bytes()), "index.html", Limits{MaxFiles: 10, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if res.SHA256 != sum || res.CompressedSize != n || res.FileCount != len(entries) {
		t.Fatalf("inspect mismatch: %+v", res)
	}
}

func TestWriteTarZstEnforcesCompressedLimit(t *testing.T) {
	var buf bytes.Buffer
	if _, _, err := writeTarZst(&buf, tarZstTestEntries(t), 8); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("expected ErrLimitExceeded, got %v", err)
	}
}

func TestWriteTarZstPropagatesMissingSourceFile(t *testing.T) {
	entries := []fileEntry{{abs: filepath.Join(t.TempDir(), "gone.html"), name: "gone.html", mode: 0o644, size: 1}}
	var buf bytes.Buffer
	if _, _, err := writeTarZst(&buf, entries, 0); err == nil {
		t.Fatal("expected error for missing source file")
	}
}

func TestInspectTarZstEnforcesExpandedLimit(t *testing.T) {
	entries := tarZstTestEntries(t)
	var total int64
	for _, entry := range entries {
		total += entry.size
	}
	var buf bytes.Buffer
	if _, _, err := writeTarZst(&buf, entries, 0); err != nil {
		t.Fatal(err)
	}
	limits := Limits{MaxFiles: 10, MaxCompressed: 1 << 20, MaxExpanded: total}
	if _, err := InspectTarZst(bytes.NewReader(buf.Bytes()), "index.html", limits); err != nil {
		t.Fatalf("expanded size equal to limit must pass: %v", err)
	}
	limits.MaxExpanded = total - 1
	_, err := InspectTarZst(bytes.NewReader(buf.Bytes()), "index.html", limits)
	if !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "expanded size") {
		t.Fatalf("expected expanded size ErrLimitExceeded, got %v", err)
	}
}
