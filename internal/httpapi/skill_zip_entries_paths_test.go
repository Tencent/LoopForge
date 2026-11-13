package httpapi

import (
	"slices"
	"strings"
	"testing"
)

func TestSkillZipEntriesSkipsDirsAndNormalizesPaths(t *testing.T) {
	// Directory entries are skipped, backslashes and "./" are normalized,
	// and the shared top-level directory is still stripped afterwards.
	raw := zipBytes(t, map[string]string{
		"pkg/":           "",
		"pkg/sub/":       "",
		"./pkg/SKILL.md": "# a",
		`pkg\sub\run.sh`: "echo",
	}, nil)
	names, files, err := skillZipEntries(openTestZip(t, raw), "pkg.zip")
	if err != nil {
		t.Fatal(err)
	}
	got := slices.Clone(names)
	slices.Sort(got)
	if !slices.Equal(got, []string{"SKILL.md", "sub/run.sh"}) {
		t.Fatalf("names = %v", got)
	}
	if files["sub/run.sh"] == nil || files["sub/run.sh"].Name != `pkg\sub\run.sh` {
		t.Fatalf("entry for sub/run.sh = %+v", files["sub/run.sh"])
	}
	if len(files) != 2 {
		t.Fatalf("files map = %v", files)
	}
}

func TestSkillZipEntriesRejectsInvalidPaths(t *testing.T) {
	for _, name := range []string{"../escape.txt", "pkg/../../escape.txt", "/abs.txt", "c:/x.txt"} {
		t.Run(name, func(t *testing.T) {
			raw := zipBytes(t, map[string]string{"pkg/SKILL.md": "x", name: "evil"}, nil)
			names, files, err := skillZipEntries(openTestZip(t, raw), "bad.zip")
			if err == nil || !strings.Contains(err.Error(), "zip 内含无效路径") || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v", err)
			}
			if names != nil || files != nil {
				t.Fatalf("invalid archive returned entries: %v %v", names, files)
			}
		})
	}
}
