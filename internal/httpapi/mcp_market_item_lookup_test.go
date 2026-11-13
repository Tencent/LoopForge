package httpapi

import "testing"

func TestMarketItemMatchesIDOrAlias(t *testing.T) {
	saved := mcpMarketItems
	t.Cleanup(func() { mcpMarketItems = saved })
	mcpMarketItems = []mcpMarketItem{
		{ID: "github", Aliases: []string{"gh"}},
		{ID: "docs", Aliases: []string{"wiki", "kb"}},
	}

	cases := []struct {
		in     string
		wantID string
		wantOK bool
	}{
		{"github", "github", true},
		{"gh", "github", true},
		{"kb", "docs", true},
		{"  wiki  ", "docs", true},
		{"", "", false},
		{"   ", "", false},
		{"missing", "", false},
	}
	for _, tc := range cases {
		item, ok := marketItem(tc.in)
		if ok != tc.wantOK || item.ID != tc.wantID {
			t.Errorf("marketItem(%q) = (%q, %v), want (%q, %v)", tc.in, item.ID, ok, tc.wantID, tc.wantOK)
		}
	}
}
