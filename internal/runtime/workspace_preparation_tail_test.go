package runtime

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTailAtRuneBoundaryKeepsShortInputUnchanged(t *testing.T) {
	for _, s := range []string{"", "abc", "错误"} {
		if got := tailAtRuneBoundary(s, len(s)); got != s {
			t.Fatalf("tailAtRuneBoundary(%q, %d) = %q, want unchanged", s, len(s), got)
		}
	}
}

func TestTailAtRuneBoundaryKeepsLastBytesOfASCII(t *testing.T) {
	if got := tailAtRuneBoundary("0123456789", 4); got != "6789" {
		t.Fatalf("tail = %q, want %q", got, "6789")
	}
}

func TestTailAtRuneBoundarySkipsPartialRune(t *testing.T) {
	// "错" is three bytes; a 4-byte tail of "a错误" would start inside "错".
	got := tailAtRuneBoundary("a错误", 4)
	if got != "误" {
		t.Fatalf("tail = %q, want %q", got, "误")
	}
	long := strings.Repeat("错误", 1000)
	for _, limit := range []int{1, 2, 3, 4, 5, 100, 1001} {
		got := tailAtRuneBoundary(long, limit)
		if len(got) > limit || !utf8.ValidString(got) || !strings.HasSuffix(long, got) {
			t.Fatalf("limit %d: tail %q is not a valid bounded suffix", limit, got)
		}
		if limit >= utf8.UTFMax && len(got) <= limit-utf8.UTFMax {
			t.Fatalf("limit %d: tail kept only %d bytes", limit, len(got))
		}
	}
}

func TestPreparationFailureDetailTailStaysWithinLimit(t *testing.T) {
	detail := preparationFailureDetail(strings.Repeat("错", 3000))
	if !strings.HasPrefix(detail, "…\n") || len(detail) > 2<<10 || !utf8.ValidString(detail) {
		t.Fatalf("detail is not a valid bounded tail: len=%d prefix=%q", len(detail), detail[:min(len(detail), 8)])
	}
}
