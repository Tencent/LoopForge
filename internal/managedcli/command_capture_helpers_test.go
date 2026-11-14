package managedcli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newCaptureFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "capture"))
	must(t, err)
	t.Cleanup(func() { file.Close() })
	return file
}

func TestReadCapturedOutputReadsFromStart(t *testing.T) {
	file := newCaptureFile(t)
	_, err := file.WriteString("help --flag\n")
	must(t, err)
	// The child leaves the shared offset at EOF; reading must rewind first.
	data, err := readCapturedOutput(file)
	must(t, err)
	if string(data) != "help --flag\n" {
		t.Fatalf("data = %q", data)
	}
}

func TestReadCapturedOutputRejectsOversize(t *testing.T) {
	file := newCaptureFile(t)
	must(t, file.Truncate(commandOutputLimit+1))
	data, err := readCapturedOutput(file)
	if !errors.Is(err, errCommandOutputTooLarge) || data != nil {
		t.Fatalf("data=%d err=%v", len(data), err)
	}
	if err.Error() != "CLI validation output exceeds 1 MiB" {
		t.Fatalf("error text changed: %v", err)
	}
}

func TestReadCapturedOutputAcceptsExactLimit(t *testing.T) {
	file := newCaptureFile(t)
	must(t, file.Truncate(commandOutputLimit))
	data, err := readCapturedOutput(file)
	must(t, err)
	if len(data) != commandOutputLimit {
		t.Fatalf("len = %d", len(data))
	}
}

func TestWatchCaptureSizeCancelsOnOversize(t *testing.T) {
	file := newCaptureFile(t)
	canceled := make(chan struct{})
	stop := watchCaptureSize(file, func() { close(canceled) })
	defer stop()
	must(t, file.Truncate(commandOutputLimit+1))
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("oversize capture did not cancel the command")
	}
}

func TestWatchCaptureSizeStopWithoutCancel(t *testing.T) {
	file := newCaptureFile(t)
	canceled := false
	stop := watchCaptureSize(file, func() { canceled = true })
	time.Sleep(30 * time.Millisecond)
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not join the watcher")
	}
	// stop joins the goroutine, so reading canceled here is race-free.
	if canceled {
		t.Fatal("watcher canceled an in-limit capture")
	}
}
