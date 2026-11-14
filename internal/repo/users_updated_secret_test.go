package repo

import "testing"

func TestUpdatedSecret(t *testing.T) {
	cases := []struct {
		name    string
		current string
		value   string
		clear   bool
		want    string
	}{
		{name: "clear drops stored secret", current: "old", clear: true, want: ""},
		{name: "clear wins over new value", current: "old", value: "new", clear: true, want: ""},
		{name: "empty value keeps stored secret", current: "old", want: "old"},
		{name: "blank value keeps stored secret", current: "old", value: " \t\n", want: "old"},
		{name: "new value replaces trimmed", current: "old", value: "  new  ", want: "new"},
		{name: "new value fills empty secret", value: "new", want: "new"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := updatedSecret(tc.current, tc.value, tc.clear); got != tc.want {
				t.Fatalf("updatedSecret(%q, %q, %v) = %q, want %q", tc.current, tc.value, tc.clear, got, tc.want)
			}
		})
	}
}
