package runtime

import (
	"context"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestExecReportsWaitingForASlot(t *testing.T) {
	inner := &localSandbox{id: "l", workspace: t.TempDir()}
	sb := &managedSandbox{Sandbox: inner, slots: make(chan struct{}, 1), workspace: inner.workspace}
	sb.slots <- struct{}{} // occupy the only slot

	events := make(chan domain.TelemetryEvent, 4)
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		defer close(done)
		_, _ = sb.Exec(ctx, CommandSpec{Argv: []string{"true"}}, func(event domain.TelemetryEvent) {
			events <- event
		})
	}()

	select {
	case event := <-events:
		if event.Name != "sandbox.slot_wait" {
			t.Fatalf("event = %q, want sandbox.slot_wait", event.Name)
		}
		if event.Category != "sandbox" {
			t.Fatalf("category = %q, want sandbox", event.Category)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no slot wait event emitted")
	}

	<-sb.slots // release the slot so Exec proceeds
	<-done
}

func TestExecDoesNotReportWaitingWhenASlotIsFree(t *testing.T) {
	inner := &localSandbox{id: "l", workspace: t.TempDir()}
	sb := &managedSandbox{Sandbox: inner, slots: make(chan struct{}, 1), workspace: inner.workspace}

	events := make(chan domain.TelemetryEvent, 4)
	_, _ = sb.Exec(context.Background(), CommandSpec{Argv: []string{"true"}}, func(event domain.TelemetryEvent) {
		events <- event
	})

	select {
	case event := <-events:
		if event.Name == "sandbox.slot_wait" {
			t.Fatal("reported waiting although a slot was free")
		}
	default:
	}
}
