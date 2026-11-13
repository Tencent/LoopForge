package preview

import (
	"archive/tar"
	"errors"
	"testing"
)

func TestTarRegularMember(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		hdr      tar.Header
		wantName string
		wantSkip bool
		wantErr  error
	}{
		{"directory", tar.Header{Name: "assets/", Typeflag: tar.TypeDir}, "", true, nil},
		{"regular", tar.Header{Name: "./assets/app.js", Typeflag: tar.TypeReg}, "assets/app.js", false, nil},
		{"legacy regular", tar.Header{Name: "index.html", Typeflag: tar.TypeRegA}, "index.html", false, nil},
		{"symlink", tar.Header{Name: "index.html", Typeflag: tar.TypeSymlink, Linkname: "secret"}, "", false, ErrUnsupportedEntry},
		{"hardlink", tar.Header{Name: "index.html", Typeflag: tar.TypeLink, Linkname: "secret"}, "", false, ErrUnsupportedEntry},
	}
	for _, tc := range cases {
		name, skip, err := tarRegularMember(&tc.hdr)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || name != tc.wantName || skip != tc.wantSkip {
			t.Fatalf("%s: got (%q, %v, %v), want (%q, %v, nil)", tc.name, name, skip, err, tc.wantName, tc.wantSkip)
		}
	}
	if _, _, err := tarRegularMember(&tar.Header{Name: "../escape.html", Typeflag: tar.TypeReg}); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("path escaping the archive root must be rejected")
	}
}

func TestCheckTarFileCount(t *testing.T) {
	t.Parallel()
	if err := checkTarFileCount(100, Limits{}); err != nil {
		t.Fatalf("zero MaxFiles must mean unlimited: %v", err)
	}
	if err := checkTarFileCount(2, Limits{MaxFiles: 2}); err != nil {
		t.Fatalf("count equal to limit must pass: %v", err)
	}
	err := checkTarFileCount(3, Limits{MaxFiles: 2})
	if !errors.Is(err, ErrLimitExceeded) || err.Error() != ErrLimitExceeded.Error()+": more than 2 files" {
		t.Fatalf("over limit err = %v", err)
	}
}
