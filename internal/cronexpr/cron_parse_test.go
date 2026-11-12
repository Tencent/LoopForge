package cronexpr

import (
	"strings"
	"testing"
)

func TestParseErrorNamesTheFailingField(t *testing.T) {
	cases := map[string]string{
		"60 * * * *": "分钟: ",
		"* 24 * * *": "小时: ",
		"* * 0 * *":  "日: ",
		"* * * 13 *": "月: ",
		"* * * * 8":  "周: ",
	}
	for expr, prefix := range cases {
		_, err := Parse(expr)
		if err == nil || !strings.HasPrefix(err.Error(), prefix) {
			t.Errorf("Parse(%q) error = %v, want prefix %q", expr, err, prefix)
		}
	}
}

func TestParseRange(t *testing.T) {
	cases := []struct {
		in         string
		start, end int
		wantErr    string
	}{
		{"*", 1, 31, ""},
		{"7", 7, 7, ""},
		{"3-9", 3, 9, ""},
		{"9-3", 9, 3, ""}, // ordering is checked by parseField
		{"x", 0, 0, "不是数字"},
		{"1-x", 0, 0, "范围无效"},
		{"1-2-3", 0, 0, "范围无效"},
	}
	for _, tc := range cases {
		start, end, err := parseRange(tc.in, 1, 31)
		if tc.wantErr != "" {
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("parseRange(%q) error = %v, want %q", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil || start != tc.start || end != tc.end {
			t.Errorf("parseRange(%q) = %d, %d, %v; want %d, %d", tc.in, start, end, err, tc.start, tc.end)
		}
	}
}

func TestParseFieldBits(t *testing.T) {
	cases := map[string]uint64{
		"*/20":    1<<0 | 1<<20 | 1<<40,
		"5":       1 << 5,
		"1-3,10":  1<<1 | 1<<2 | 1<<3 | 1<<10,
		"10-20/5": 1<<10 | 1<<15 | 1<<20,
	}
	for in, want := range cases {
		got, err := parseField(in, 0, 59)
		if err != nil || got != want {
			t.Errorf("parseField(%q) = %b, %v; want %b", in, got, err, want)
		}
	}
}
