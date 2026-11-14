package runtime

import "testing"

func TestHasArg(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		wanted string
		want   bool
	}{
		{name: "nil slice", values: nil, wanted: "--model", want: false},
		{name: "empty slice", values: []string{}, wanted: "--model", want: false},
		{name: "exact match", values: []string{"-p", "--model", "x"}, wanted: "--model", want: true},
		{name: "case sensitive", values: []string{"--Model"}, wanted: "--model", want: false},
		{name: "no prefix match", values: []string{"--model=x"}, wanted: "--model", want: false},
		{name: "empty wanted", values: []string{"a", ""}, wanted: "", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasArg(tc.values, tc.wanted); got != tc.want {
				t.Fatalf("hasArg(%q, %q) = %v, want %v", tc.values, tc.wanted, got, tc.want)
			}
		})
	}
}
