package httpapi

import (
	"slices"
	"testing"
)

func TestTAPDImportRepositoryIDs(t *testing.T) {
	cases := []struct {
		name    string
		primary string
		ids     []string
		want    []string
	}{
		{name: "empty", want: []string{}},
		{name: "trims and dedupes in order", ids: []string{" repo-b ", "", "repo-a", "repo-b", "  "}, want: []string{"repo-b", "repo-a"}},
		{name: "primary only", primary: " repo-a ", want: []string{"repo-a"}},
		{name: "primary goes first when missing", primary: "repo-c", ids: []string{"repo-a", "repo-b"}, want: []string{"repo-c", "repo-a", "repo-b"}},
		{name: "primary already listed keeps list order", primary: " repo-b", ids: []string{"repo-a", "repo-b"}, want: []string{"repo-a", "repo-b"}},
		{name: "blank primary ignored", primary: "  ", ids: []string{"repo-a"}, want: []string{"repo-a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tapdImportRepositoryIDs(tc.primary, tc.ids); !slices.Equal(got, tc.want) {
				t.Fatalf("tapdImportRepositoryIDs(%q, %q) = %q, want %q", tc.primary, tc.ids, got, tc.want)
			}
		})
	}
}
