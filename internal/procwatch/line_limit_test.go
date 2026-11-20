package procwatch

import (
	"bytes"
	"strings"
	"testing"
)

func TestLineWriterBoundsUnterminatedOutput(t *testing.T) {
	out := &lineWriter{}
	chunk := bytes.Repeat([]byte("synthetic-long-output-"), 4096)
	const limit = 64 << 10
	for range 8 {
		if n, err := out.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if size := out.pending.Len(); size > limit {
		t.Fatalf("buffer grew to %d bytes", size)
	}
	out.Write([]byte("discarded-suffix\nnext line\n"))
	tail := out.tail()
	if !strings.HasSuffix(tail, "\nnext line") || strings.Contains(tail, "discarded-suffix") {
		t.Fatalf("watcher did not cleanly resume after the oversized line: %q", tail)
	}
}
