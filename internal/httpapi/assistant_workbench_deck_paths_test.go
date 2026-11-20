package httpapi

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestFilterRecentRegularFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	write := func(name string, mod time.Time) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
		return path
	}
	old := write("old.pptx", base.Add(-time.Minute))
	grace := write("grace.pptx", base.Add(-20*time.Second))
	inside := write("inside.pptx", base.Add(time.Minute))
	future := write("future.pptx", base.Add(time.Hour))
	subdir := filepath.Join(dir, "dir.pptx")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.pptx")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.pptx")
	all := []string{old, grace, inside, future, subdir, link, missing}

	got := filterRecentRegularFiles(all, base, base.Add(2*time.Minute))
	if want := []string{grace, inside}; !slices.Equal(got, want) {
		t.Fatalf("windowed=%v want %v", got, want)
	}
	got = filterRecentRegularFiles(all, time.Time{}, time.Time{})
	if want := []string{old, grace, inside, future}; !slices.Equal(got, want) {
		t.Fatalf("unbounded=%v want %v", got, want)
	}
}

func TestTempDirDeckFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	mk := func(rel string) string {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	top := mk("top.PPTX")
	mk("notes.txt")
	inPPT := mk("my-ppt-job/deck.pptx")
	inSlide := mk("Slides/a/b.pptx")
	inLF := mk("loopforge-x/c.pptx")
	mk("unrelated/d.pptx")

	got, err := tempDirDeckFiles()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{inSlide, inLF, inPPT, top}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("temp decks=%v want %v", got, want)
	}

	t.Setenv("TMPDIR", filepath.Join(root, "does-not-exist"))
	if _, err := tempDirDeckFiles(); err == nil {
		t.Fatal("expected error for missing temp dir")
	}
}
