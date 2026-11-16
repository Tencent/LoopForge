package auth

import "testing"

// legacyNormalizePhotoSpec is the pre-refactor nested implementation, kept
// here only to prove the flattened switch returns identical values.
func legacyNormalizePhotoSpec(spec int) int {
	switch {
	case spec <= 0:
		return 100
	case spec <= 48:
		return 48
	case spec <= 100:
		if spec <= 74 {
			return 48
		}
		return 100
	case spec <= 150:
		if spec <= 125 {
			return 100
		}
		return 150
	default:
		if spec <= 300 {
			return 150
		}
		return 500
	}
}

func TestNormalizePhotoSpecBoundaries(t *testing.T) {
	tests := []struct{ input, want int }{
		{-1, 100}, {0, 100}, {1, 48}, {48, 48}, {74, 48}, {75, 100},
		{100, 100}, {125, 100}, {126, 150}, {150, 150}, {300, 150},
		{301, 500}, {500, 500}, {10000, 500},
	}
	for _, tt := range tests {
		if got := NormalizePhotoSpec(tt.input); got != tt.want {
			t.Errorf("NormalizePhotoSpec(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestNormalizePhotoSpecMatchesLegacy(t *testing.T) {
	for spec := -1000; spec <= 1000; spec++ {
		if got, want := NormalizePhotoSpec(spec), legacyNormalizePhotoSpec(spec); got != want {
			t.Fatalf("NormalizePhotoSpec(%d) = %d, legacy = %d", spec, got, want)
		}
	}
}
