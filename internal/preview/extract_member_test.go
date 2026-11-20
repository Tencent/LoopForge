package preview

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func TestExtractTarMemberWritesNestedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := extractTarMember(strings.NewReader("hello, preview!"), root, "assets/js/app.js", 5); err != nil {
		t.Fatalf("extractTarMember: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "assets", "js", "app.js"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("extracted body = %q, want only the declared 5 bytes", got)
	}
}

func TestExtractTarMemberReportsShortBody(t *testing.T) {
	t.Parallel()
	err := extractTarMember(strings.NewReader("abc"), t.TempDir(), "index.html", 10)
	if err == nil || err.Error() != "extracted index.html size mismatch" {
		t.Fatalf("err = %v, want size mismatch", err)
	}
}

func TestExtractTarMemberRefusesExistingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := extractTarMember(strings.NewReader("new"), root, "index.html", 3)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("err = %v, want fs.ErrExist", err)
	}
}

func TestExtractTarMemberFailsWhenParentIsFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A previous member "assets" was a regular file, so "assets/app.js" cannot
	// get a parent directory and must fail before anything is written.
	if err := os.WriteFile(filepath.Join(root, "assets"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := extractTarMember(strings.NewReader("body"), root, "assets/app.js", 4)
	if err == nil {
		t.Fatal("extractTarMember succeeded with a regular file as parent directory")
	}
	if strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("err = %v, want the directory creation error, not a size mismatch", err)
	}
	got, readErr := os.ReadFile(filepath.Join(root, "assets"))
	if readErr != nil || string(got) != "file" {
		t.Fatalf("existing file changed: %q, %v", got, readErr)
	}
}

func TestExtractTarMemberPropagatesReadError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	readErr := errors.New("zstd: corrupt input")
	body := io.MultiReader(strings.NewReader("ab"), iotest.ErrReader(readErr))
	err := extractTarMember(body, root, "index.html", 10)
	if !errors.Is(err, readErr) {
		t.Fatalf("err = %v, want the underlying read error instead of a size mismatch", err)
	}
	// The file is created before the copy fails; ExtractTarZst removes the whole
	// temporary root afterwards, so here only the bytes already read remain.
	got, statErr := os.ReadFile(filepath.Join(root, "index.html"))
	if statErr != nil || string(got) != "ab" {
		t.Fatalf("partial file = %q, %v; want the bytes read before the error", got, statErr)
	}
}
