package gitwork

import (
	"reflect"
	"testing"
)

func TestParseNameStatusNumstatMergesAndSorts(t *testing.T) {
	nameStatus := []byte("M\tz.go\r\nR087\told/a.go\tnew/a.go\nC100\tsrc.go\tcopy.go\nA\tadded.png\nD\tgone.go\n\n   \nbad\n\tmissing-letter\n")
	numstat := []byte("1\t2\tz.go\n4\t0\tnew/a.go\n-\t-\tadded.png\n-\t-\tonly-numstat.bin\n7\t8\tonly-numstat.go\nshort\t1\n")
	got := ParseNameStatusNumstat(nameStatus, numstat)
	want := []PatchFileSummary{
		{Path: "added.png", Status: "A", Binary: true},
		{Path: "copy.go", Status: "C", RenameFrom: "src.go"},
		{Path: "gone.go", Status: "D"},
		{Path: "new/a.go", Status: "R", Added: 4, RenameFrom: "old/a.go"},
		{Path: "only-numstat.bin", Status: "B", Binary: true},
		{Path: "only-numstat.go", Status: "M", Added: 7, Deleted: 8},
		{Path: "z.go", Status: "M", Added: 1, Deleted: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files =\n%#v\nwant\n%#v", got, want)
	}
}

func TestParseNameStatusNumstatUnquotesAndSkipsEmptyPaths(t *testing.T) {
	nameStatus := []byte("M\t\"dir/\\344\\270\\255.go\"\nM\t\"\"\n")
	numstat := []byte("3\t1\t\"dir/\\344\\270\\255.go\"\n1\t1\t\n")
	got := ParseNameStatusNumstat(nameStatus, numstat)
	want := []PatchFileSummary{{Path: "dir/中.go", Status: "M", Added: 3, Deleted: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
	if got := ParseNameStatusNumstat(nil, nil); len(got) != 0 {
		t.Fatalf("empty input = %#v", got)
	}
}
