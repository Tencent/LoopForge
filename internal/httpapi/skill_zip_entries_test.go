package httpapi

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"slices"
	"strings"
	"testing"
)

func openTestZip(t *testing.T, raw []byte) *zip.Reader {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func TestSkillZipEntriesStripsSharedTopDirAndMacMetadata(t *testing.T) {
	raw := zipBytes(t, map[string]string{
		"pkg/SKILL.md":            "# a",
		"pkg/b/c.txt":             "c",
		"pkg/.DS_Store":           "",
		"__MACOSX/pkg/._SKILL.md": "",
	}, nil)
	names, files, err := skillZipEntries(openTestZip(t, raw), "pkg.zip")
	if err != nil {
		t.Fatal(err)
	}
	got := slices.Clone(names)
	slices.Sort(got)
	if !slices.Equal(got, []string{"SKILL.md", "b/c.txt"}) {
		t.Fatalf("names = %v", got)
	}
	if len(files) != len(names) {
		t.Fatalf("files map has %d entries, want %d", len(files), len(names))
	}
	for _, name := range names {
		if files[name] == nil {
			t.Fatalf("missing zip entry for %s", name)
		}
	}
}

func TestSkillZipEntriesKeepsMultipleTopDirs(t *testing.T) {
	raw := zipBytes(t, map[string]string{"a/one.md": "1", "b/two.md": "2"}, nil)
	names, _, err := skillZipEntries(openTestZip(t, raw), "multi.zip")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"a/one.md", "b/two.md"}) {
		t.Fatalf("names = %v", names)
	}
}

func TestSkillZipEntriesRejectsInvalidArchives(t *testing.T) {
	cases := []struct {
		name    string
		entries map[string]string
		modes   map[string]fs.FileMode
		want    string
	}{
		{name: "symlink", entries: map[string]string{"SKILL.md": "x", "link": "SKILL.md"}, modes: map[string]fs.FileMode{"link": fs.ModeSymlink | 0o777}, want: "非常规文件条目 link"},
		{name: "only metadata", entries: map[string]string{"__MACOSX/._x": "", ".DS_Store": ""}, want: "zip empty.zip 中没有可用的文件"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := zipBytes(t, tc.entries, tc.modes)
			if _, _, err := skillZipEntries(openTestZip(t, raw), "empty.zip"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
