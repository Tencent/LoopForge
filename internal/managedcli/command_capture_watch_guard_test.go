package managedcli

import (
	"sync/atomic"
	"testing"
	"time"
)

// A capture of exactly commandOutputLimit bytes is still accepted by
// readCapturedOutput, so the watcher must not cancel it either.
func TestWatchCaptureSizeDoesNotCancelAtExactLimit(t *testing.T) {
	file := newCaptureFile(t)
	must(t, file.Truncate(commandOutputLimit))
	var calls atomic.Int32
	stop := watchCaptureSize(file, func() { calls.Add(1) })
	time.Sleep(60 * time.Millisecond) // several 10ms ticks
	stop()
	if n := calls.Load(); n != 0 {
		t.Fatalf("watcher canceled an exact-limit capture %d time(s)", n)
	}
}

// The watcher cancels once and then exits on its own instead of re-canceling
// on every tick until stop.
func TestWatchCaptureSizeCancelsOnce(t *testing.T) {
	file := newCaptureFile(t)
	must(t, file.Truncate(commandOutputLimit+1))
	var calls atomic.Int32
	stop := watchCaptureSize(file, func() { calls.Add(1) })
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond) // several more ticks
	stop()
	if n := calls.Load(); n != 1 {
		t.Fatalf("cancel calls = %d, want exactly 1", n)
	}
}

// stop joins the watcher goroutine: while cancel is still running, stop must
// not return, so executeCommand never reads the capture with the watcher alive.
func TestWatchCaptureSizeStopWaitsForRunningCancel(t *testing.T) {
	file := newCaptureFile(t)
	must(t, file.Truncate(commandOutputLimit+1))
	entered, release := make(chan struct{}), make(chan struct{})
	stop := watchCaptureSize(file, func() {
		close(entered)
		<-release
	})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("oversize capture did not cancel the command")
	}
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("stop returned while the watcher was still inside cancel")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not return after cancel finished")
	}
}
