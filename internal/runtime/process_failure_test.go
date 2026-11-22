package runtime

import (
	"os/exec"
	"strings"
	"testing"
)

func TestFailedProcessCarriesStderrTail(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf 'dsh: QUOTA: Insufficient Balance (request_id: abc)\\n' >&2; exit 1")
	out, err := runCommand(cmd, nil)
	if err == nil {
		t.Fatal("want process failure")
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("error = %q", err.Error())
	}
	if !strings.Contains(out, "QUOTA") {
		t.Fatalf("combined output = %q", out)
	}
	tail := ProcessFailureStderr(err)
	if !strings.Contains(tail, "dsh: QUOTA: Insufficient Balance") {
		t.Fatalf("stderr tail = %q", tail)
	}
}

func TestFailedProcessStderrTailIsBounded(t *testing.T) {
	cmd := exec.Command("sh", "-c", "head -c 12000 /dev/zero | tr '\\0' 'x' >&2; printf '\\nTAIL-MARKER\\n' >&2; exit 2")
	_, err := runCommand(cmd, nil)
	if err == nil {
		t.Fatal("want process failure")
	}
	tail := ProcessFailureStderr(err)
	if len(tail) > maxProcessStderrTail+1 {
		t.Fatalf("stderr tail is not bounded: %d bytes", len(tail))
	}
	if !strings.Contains(tail, "TAIL-MARKER") {
		t.Fatalf("stderr tail dropped the end: %q", tail[len(tail)-40:])
	}
}

func TestSuccessfulProcessHasNoStderrTail(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf 'warning noise\\n' >&2; exit 0")
	if _, err := runCommand(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecutorStderrAppendedToFailure(t *testing.T) {
	failure := &ProcessError{Err: exec.ErrNotFound, StderrTail: "dsh: QUOTA: Insufficient Balance"}
	got := withExecutorStderr(failure)
	if !strings.Contains(got.Error(), "执行器 stderr") || !strings.Contains(got.Error(), "QUOTA: Insufficient Balance") {
		t.Fatalf("error = %q", got.Error())
	}
	if withExecutorStderr(nil) != nil {
		t.Fatal("nil error must stay nil")
	}
	plain := exec.ErrNotFound
	if got := withExecutorStderr(plain); got != plain {
		t.Fatalf("plain error changed: %q", got.Error())
	}
}
