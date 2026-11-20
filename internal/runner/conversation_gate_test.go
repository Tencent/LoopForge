package runner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestConversationWaitsForPreemptedExportBeforeStarting(t *testing.T) {
	var gate sync.RWMutex
	gate.Lock() // a previously queued export acquired the gate after the API probe
	ready := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		err := lockConversationWorkspace(ctx, &gate)
		if err == nil {
			gate.Unlock()
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		t.Fatalf("start did not wait for the old export: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	gate.Unlock() // cancellation of the old background save releases it
	if err := <-ready; err != nil {
		t.Fatal("foreground start failed instead of waiting for export cancellation", err)
	}
}

func TestConversationGateWaitHonorsCancellation(t *testing.T) {
	var gate sync.RWMutex
	gate.Lock()
	defer gate.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan error, 1)
	go func() { ready <- lockConversationWorkspace(ctx, &gate) }()
	cancel()
	select {
	case err := <-ready:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cancelled start waited for export")
	}
}
