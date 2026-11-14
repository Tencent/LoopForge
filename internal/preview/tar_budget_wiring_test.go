package preview

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// tarBudgetTestBundle is a bundle with one directory and two regular files
// (2 + 3 bytes). Only the regular files may be charged to the tar budget.
func tarBudgetTestBundle(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	zw, err := zstd.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for _, m := range []struct {
		hdr  tar.Header
		body string
	}{
		{tar.Header{Name: "assets/", Typeflag: tar.TypeDir, Mode: 0o755}, ""},
		{tar.Header{Name: "index.html", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2}, "ok"},
		{tar.Header{Name: "assets/app.js", Typeflag: tar.TypeReg, Mode: 0o644, Size: 3}, "abc"},
	} {
		hdr := m.hdr
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(m.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestInspectTarZstReportsBudgetTotalsAtExactLimits(t *testing.T) {
	bundle := tarBudgetTestBundle(t)
	limits := Limits{MaxFiles: 2, MaxCompressed: 1 << 20, MaxExpanded: 5}
	res, err := InspectTarZst(bytes.NewReader(bundle), "index.html", limits)
	if err != nil {
		t.Fatalf("bundle exactly at limits rejected: %v", err)
	}
	if res.FileCount != 2 || res.ExpandedSize != 5 {
		t.Fatalf("result = %d files / %d bytes, want 2 / 5 (directories not counted)", res.FileCount, res.ExpandedSize)
	}
}

func TestExtractTarZstAcceptsBundleAtExactLimits(t *testing.T) {
	bundle := tarBudgetTestBundle(t)
	dest := filepath.Join(t.TempDir(), "site")
	limits := Limits{MaxFiles: 2, MaxCompressed: 1 << 20, MaxExpanded: 5}
	if err := ExtractTarZst(bytes.NewReader(bundle), dest, limits); err != nil {
		t.Fatalf("bundle exactly at limits rejected: %v", err)
	}
	for name, want := range map[string]string{"index.html": "ok", "assets/app.js": "abc"} {
		if body, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name))); err != nil || string(body) != want {
			t.Fatalf("%s = %q, %v; want %q", name, body, err, want)
		}
	}
}

// Both readers must share the same limit semantics: the file count is checked
// before the expanded size, and a rejected extract never publishes dest.
func TestTarZstReadersEnforceBudgetConsistently(t *testing.T) {
	bundle := tarBudgetTestBundle(t)
	for _, tc := range []struct {
		name   string
		limits Limits
		want   string
	}{
		// The second file pushes both totals over; the file count wins.
		{"file count first", Limits{MaxFiles: 1, MaxCompressed: 1 << 20, MaxExpanded: 2}, "more than 1 files"},
		{"expanded size", Limits{MaxFiles: 2, MaxCompressed: 1 << 20, MaxExpanded: 4}, "expanded size"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, inspectErr := InspectTarZst(bytes.NewReader(bundle), "index.html", tc.limits)
			dest := filepath.Join(t.TempDir(), "site")
			extractErr := ExtractTarZst(bytes.NewReader(bundle), dest, tc.limits)
			for label, err := range map[string]error{"inspect": inspectErr, "extract": extractErr} {
				if !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("%s err = %v, want ErrLimitExceeded containing %q", label, err, tc.want)
				}
			}
			if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected extract left dest behind: %v", err)
			}
		})
	}
}
