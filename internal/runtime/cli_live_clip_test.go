//go:build clilive

package runtime

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipLiveKeepsMultiByteRunesWhole(t *testing.T) {
	// 700 bytes is not a multiple of 3, so the byte cut lands inside a rune.
	got := clipLive(strings.Repeat("失", 300))
	if !utf8.ValidString(got) {
		t.Fatalf("clipped live output is not valid UTF-8: %q", got[len(got)-8:])
	}
	if !strings.HasSuffix(got, "…") || len(got) > 700+len("…") {
		t.Fatalf("expected clipped live output of at most 700 bytes, got %d bytes", len(got))
	}
}
