package preview

import "testing"

func TestContainsIndex(t *testing.T) {
	entries := []fileEntry{{name: "assets/app.js"}, {name: "index.html"}, {name: "sub/index.html"}}
	cases := []struct {
		name    string
		entries []fileEntry
		index   string
		want    bool
	}{
		{"root index", entries, "index.html", true},
		{"nested index", entries, "sub/index.html", true},
		{"missing", entries, "home.html", false},
		{"exact match only", entries, "INDEX.html", false},
		{"empty entries", nil, "index.html", false},
	}
	for _, tc := range cases {
		if got := containsIndex(tc.entries, tc.index); got != tc.want {
			t.Errorf("%s: containsIndex(%q) = %v, want %v", tc.name, tc.index, got, tc.want)
		}
	}
}
