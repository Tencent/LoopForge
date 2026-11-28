package knowledge

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mcpSemaphoreTestKey(ownerUserID int64, connectorID string, maxConcur int) string {
	return fmt.Sprintf("u:%d|%s|%d", ownerUserID, connectorID, maxConcur)
}

func mcpSemaphoreTestState(key string) (*mcpSemaphoreEntry, int, int) {
	mcpSemaphoresMu.Lock()
	defer mcpSemaphoresMu.Unlock()
	entry := mcpSemaphores[key]
	if entry == nil {
		return nil, 0, len(mcpSemaphores)
	}
	return entry, entry.refs, len(mcpSemaphores)
}

func waitMCPSemaphoreRefs(t *testing.T, key string, want int) *mcpSemaphoreEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entry, refs, _ := mcpSemaphoreTestState(key)
		if entry != nil && refs == want {
			return entry
		}
		time.Sleep(time.Millisecond)
	}
	entry, refs, _ := mcpSemaphoreTestState(key)
	t.Fatalf("semaphore %q refs = %d (entry=%p), want %d", key, refs, entry, want)
	return nil
}

func TestMCPConnectorSlotCancellationAndRelease(t *testing.T) {
	key := mcpSemaphoreTestKey(901, "slot-cancel", 1)
	_, _, baseline := mcpSemaphoreTestState(key)

	release, err := acquireConnectorSlot(context.Background(), 901, "slot-cancel", 1)
	if err != nil {
		t.Fatal(err)
	}
	entry := waitMCPSemaphoreRefs(t, key, 1)
	if got := len(entry.sem); got != 1 {
		t.Fatalf("acquired semaphore len = %d, want 1", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	go func() {
		_, err := acquireConnectorSlot(ctx, 901, "slot-cancel", 1)
		waiting <- err
	}()
	if got := waitMCPSemaphoreRefs(t, key, 2); got != entry {
		t.Fatal("waiting acquisition replaced the existing semaphore")
	}
	cancel()
	if err := <-waiting; err != context.Canceled {
		t.Fatalf("cancelled acquire error = %v, want context.Canceled", err)
	}
	if _, refs, _ := mcpSemaphoreTestState(key); refs != 1 {
		t.Fatalf("refs after cancellation = %d, want 1", refs)
	}
	release()
	if entry, _, size := mcpSemaphoreTestState(key); entry != nil || size != baseline {
		t.Fatalf("released semaphore leaked: entry=%p map size=%d baseline=%d", entry, size, baseline)
	}
}

func TestMCPConnectorSlotKeysAreIsolated(t *testing.T) {
	_, _, baseline := mcpSemaphoreTestState("")
	type acquisition struct {
		key     string
		release func()
	}
	acquisitions := make([]acquisition, 0, 4)
	for _, tc := range []struct {
		owner     int64
		connector string
		maxConcur int
	}{
		{owner: 910, connector: "shared-id", maxConcur: 1},
		{owner: 911, connector: "shared-id", maxConcur: 1},
		{owner: 910, connector: "other-id", maxConcur: 1},
		{owner: 910, connector: "shared-id", maxConcur: 2},
	} {
		release, err := acquireConnectorSlot(context.Background(), tc.owner, tc.connector, tc.maxConcur)
		if err != nil {
			t.Fatalf("acquire %+v: %v", tc, err)
		}
		acquisitions = append(acquisitions, acquisition{
			key:     mcpSemaphoreTestKey(tc.owner, tc.connector, tc.maxConcur),
			release: release,
		})
	}
	if _, _, size := mcpSemaphoreTestState(""); size != baseline+len(acquisitions) {
		t.Fatalf("isolated acquisitions map size = %d, want %d", size, baseline+len(acquisitions))
	}
	for i := len(acquisitions) - 1; i >= 0; i-- {
		acquisitions[i].release()
		if entry, _, _ := mcpSemaphoreTestState(acquisitions[i].key); entry != nil {
			t.Fatalf("released key %q still present", acquisitions[i].key)
		}
	}
	if _, _, size := mcpSemaphoreTestState(""); size != baseline {
		t.Fatalf("map size after isolated releases = %d, baseline %d", size, baseline)
	}
}

func TestMCPConnectorSlotConcurrentRelease(t *testing.T) {
	const (
		workers   = 32
		maxConcur = 4
	)
	key := mcpSemaphoreTestKey(920, "slot-concurrent", maxConcur)
	_, _, baseline := mcpSemaphoreTestState(key)
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	var active atomic.Int32
	var peak atomic.Int32
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			release, err := acquireConnectorSlot(context.Background(), 920, "slot-concurrent", maxConcur)
			if err != nil {
				errs <- err
				return
			}
			defer release()
			current := active.Add(1)
			defer active.Add(-1)
			for {
				previous := peak.Load()
				if current <= previous || peak.CompareAndSwap(previous, current) {
					break
				}
			}
			if current > maxConcur {
				errs <- fmt.Errorf("concurrency = %d, limit = %d", current, maxConcur)
			}
			time.Sleep(time.Millisecond)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := peak.Load(); got > maxConcur {
		t.Fatalf("peak concurrency = %d, limit = %d", got, maxConcur)
	}
	if entry, _, size := mcpSemaphoreTestState(key); entry != nil || size != baseline {
		t.Fatalf("concurrent releases leaked semaphore: entry=%p map size=%d baseline=%d", entry, size, baseline)
	}
}

func TestMCPConnectorSlotReleaseIsIdempotent(t *testing.T) {
	key := mcpSemaphoreTestKey(930, "slot-idempotent", 2)
	_, _, baseline := mcpSemaphoreTestState(key)

	release1, err := acquireConnectorSlot(context.Background(), 930, "slot-idempotent", 2)
	if err != nil {
		t.Fatal(err)
	}
	release2, err := acquireConnectorSlot(context.Background(), 930, "slot-idempotent", 2)
	if err != nil {
		t.Fatal(err)
	}

	// Calling release1 multiple times (including concurrently) must neither block
	// nor steal slot 2 or underflow refs.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release1()
		}()
	}
	wg.Wait()

	entry, refs, _ := mcpSemaphoreTestState(key)
	if entry == nil || refs != 1 || len(entry.sem) != 1 {
		t.Fatalf("after idempotent release1: entry=%p refs=%d semLen=%d, want non-nil refs=1 semLen=1", entry, refs, func() int {
			if entry == nil {
				return -1
			}
			return len(entry.sem)
		}())
	}

	release2()
	release2()
	if entry, _, size := mcpSemaphoreTestState(key); entry != nil || size != baseline {
		t.Fatalf("after release2: entry=%p map size=%d baseline=%d", entry, size, baseline)
	}
}
