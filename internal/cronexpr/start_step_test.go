package cronexpr

import "testing"

func TestNumericStepExpandsToFieldMaximum(t *testing.T) {
	for _, tc := range []struct {
		field    string
		min, max int
		want     uint64
	}{
		{"15/20", 0, 59, 1<<15 | 1<<35 | 1<<55},
		{"3/8", 0, 23, 1<<3 | 1<<11 | 1<<19},
		{"2/4", 1, 12, 1<<2 | 1<<6 | 1<<10},
		{"15", 0, 59, 1 << 15},
		{"15-35/20", 0, 59, 1<<15 | 1<<35},
		{"15/20,0", 0, 59, 1<<0 | 1<<15 | 1<<35 | 1<<55},
	} {
		got, err := parseField(tc.field, tc.min, tc.max)
		if err != nil || got != tc.want {
			t.Errorf("parseField(%q) = %#x, %v; want %#x", tc.field, got, err, tc.want)
		}
	}
}
