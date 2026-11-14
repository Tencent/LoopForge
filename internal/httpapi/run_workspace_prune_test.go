package httpapi

import (
	"reflect"
	"testing"
)

func TestPruneEmptyWorkspaceDirs(t *testing.T) {
	dir := func(path string) workspaceEntry { return workspaceEntry{Path: path, Type: "directory"} }
	file := func(path string, size int64) workspaceEntry {
		return workspaceEntry{Path: path, Type: "file", Size: size}
	}
	for _, tc := range []struct {
		name string
		in   []workspaceEntry
		want []workspaceEntry
	}{
		{name: "empty input", in: nil, want: nil},
		{name: "files only", in: []workspaceEntry{file("b.txt", 2), file("a.txt", 1)}, want: []workspaceEntry{file("b.txt", 2), file("a.txt", 1)}},
		{name: "empty directories dropped", in: []workspaceEntry{dir("empty"), dir("empty/nested"), file("a.txt", 1)}, want: []workspaceEntry{file("a.txt", 1)}},
		{
			name: "ancestors of nested file kept in order",
			in:   []workspaceEntry{dir("src"), dir("src/pkg"), dir("src/pkg/empty"), file("src/pkg/a.go", 3), dir("docs"), file("README.md", 4)},
			want: []workspaceEntry{dir("src"), dir("src/pkg"), file("src/pkg/a.go", 3), file("README.md", 4)},
		},
		{name: "directory prefix is not an ancestor", in: []workspaceEntry{dir("src"), file("src2/a.go", 1)}, want: []workspaceEntry{file("src2/a.go", 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pruneEmptyWorkspaceDirs(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pruneEmptyWorkspaceDirs() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
