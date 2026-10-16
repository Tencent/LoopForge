package runner

import (
	"errors"
	"io"
	"os"
	"testing"
)

func TestIsBenignStreamClose(t *testing.T) {
	if !isBenignStreamClose(os.ErrClosed) {
		t.Fatal("os.ErrClosed should be ignored")
	}
	if !isBenignStreamClose(io.ErrClosedPipe) {
		t.Fatal("closed pipe should be ignored")
	}
	if !isBenignStreamClose(errors.New("read |0: file already closed")) {
		t.Fatal("linux pipe close should be ignored")
	}
	if isBenignStreamClose(errors.New("read timeout")) {
		t.Fatal("real stream errors should surface")
	}
}
