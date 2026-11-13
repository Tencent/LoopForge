package preview

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
