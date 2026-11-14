package tracker

import "testing"

func TestHeaderHas(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"nil map", nil, false},
		{"exact", map[string]string{"Authorization": "Bearer x"}, true},
		{"lower case key", map[string]string{"authorization": "Bearer x"}, true},
		{"padded key", map[string]string{" Authorization ": "Bearer x"}, true},
		{"empty value", map[string]string{"Authorization": ""}, false},
		{"blank value", map[string]string{"Authorization": "  "}, false},
		{"other header", map[string]string{"X-Token": "x"}, false},
		{"one of many", map[string]string{"X-Token": "x", "AUTHORIZATION": "t"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := headerHas(tc.headers, "Authorization"); got != tc.want {
				t.Fatalf("headerHas(%v) = %v, want %v", tc.headers, got, tc.want)
			}
		})
	}
}
