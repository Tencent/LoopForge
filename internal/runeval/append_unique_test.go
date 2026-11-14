package runeval

import (
	"slices"
	"testing"
)

func TestAppendUnique(t *testing.T) {
	cases := []struct {
		name  string
		list  []string
		value string
		want  []string
	}{
		{name: "nil list", list: nil, value: "a", want: []string{"a"}},
		{name: "empty list", list: []string{}, value: "a", want: []string{"a"}},
		{name: "already present", list: []string{"a", "b"}, value: "b", want: []string{"a", "b"}},
		{name: "appended at end", list: []string{"a", "b"}, value: "c", want: []string{"a", "b", "c"}},
		{name: "empty value appended once", list: []string{"a"}, value: "", want: []string{"a", ""}},
		{name: "empty value already present", list: []string{"", "a"}, value: "", want: []string{"", "a"}},
		{name: "case sensitive", list: []string{"A"}, value: "a", want: []string{"A", "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendUnique(tc.list, tc.value); !slices.Equal(got, tc.want) {
				t.Fatalf("appendUnique(%q, %q) = %q, want %q", tc.list, tc.value, got, tc.want)
			}
		})
	}
}
