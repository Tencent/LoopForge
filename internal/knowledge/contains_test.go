package knowledge

import (
	"slices"
	"testing"
)

func TestIsReadOnlyToolTable(t *testing.T) {
	cases := map[string]bool{
		ToolSearch: true, ToolFetch: true, ToolListSources: true, ToolGetSource: true,
		ToolWrite: false, "": false, " kb_search": false, "KB_SEARCH": false,
	}
	for name, want := range cases {
		if got := IsReadOnlyTool(name); got != want {
			t.Errorf("IsReadOnlyTool(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSourceAllowedTable(t *testing.T) {
	cases := []struct {
		allowed []string
		source  string
		want    bool
	}{
		{nil, "x", true},
		{[]string{}, "x", true},
		{[]string{" ", ""}, "x", true},
		{[]string{"a"}, "a", true},
		{[]string{" a "}, "a", true},
		{[]string{"a"}, " a ", true},
		{[]string{"a", "b"}, "b", true},
		{[]string{"a"}, "x", false},
		{[]string{"a"}, "", false},
		{[]string{"a"}, "A", false},
	}
	for _, c := range cases {
		if got := SourceAllowed(c.allowed, c.source); got != c.want {
			t.Errorf("SourceAllowed(%q, %q) = %v, want %v", c.allowed, c.source, got, c.want)
		}
	}
}

func TestAppendUniqueKeepsOrder(t *testing.T) {
	var list []string
	for _, v := range []string{"b", "a", "b", "c", "a"} {
		list = appendUnique(list, v)
	}
	if want := []string{"b", "a", "c"}; !slices.Equal(list, want) {
		t.Fatalf("appendUnique = %#v, want %#v", list, want)
	}
}
