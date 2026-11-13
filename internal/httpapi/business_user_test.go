package httpapi

import "testing"

func TestHasBusinessUser(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []int64
		id   int64
		want bool
	}{
		{"hit", []int64{3, 7, 9}, 7, true},
		{"miss", []int64{3, 9}, 7, false},
		{"nil slice", nil, 7, false},
		{"empty slice", []int64{}, 7, false},
		{"zero id never matches", []int64{0, 1}, 0, false},
		{"negative id never matches", []int64{-1, 1}, -1, false},
	} {
		if got := hasBusinessUser(tc.ids, tc.id); got != tc.want {
			t.Errorf("%s: hasBusinessUser(%v, %d) = %v want %v", tc.name, tc.ids, tc.id, got, tc.want)
		}
	}
}
