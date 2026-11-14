package engine

import (
	"context"
	"testing"
	"time"
)

func TestWorkspaceSaveLeaseHeld(t *testing.T) {
	e, id := checkpointFixture(t)
	if e.workspaceSaveLeaseHeld("missing-run", "token") {
		t.Fatal("missing checkpoint reported as held")
	}
	use, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if !e.workspaceSaveLeaseHeld(id, use) {
		t.Fatal("current lease not reported as held")
	}
	if e.workspaceSaveLeaseHeld(id, use+"-other") {
		t.Fatal("foreign token reported as held")
	}
	if err = e.repo.FinishWorkspaceUse(id, use, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
	if e.workspaceSaveLeaseHeld(id, use) {
		t.Fatal("released lease still reported as held")
	}
}

func TestWatchWorkspaceSaveLeaseCancelsWhenLeaseIsLost(t *testing.T) {
	e, id := checkpointFixture(t)
	use, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := e.watchWorkspaceSaveLease(ctx, id, use, cancel)
	select {
	case <-done:
		t.Fatal("watcher exited while lease was still held")
	case <-time.After(250 * time.Millisecond):
	}
	if err = e.repo.FinishWorkspaceUse(id, use, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher ignored lost lease")
	}
	if ctx.Err() == nil {
		t.Fatal("watcher did not cancel the save")
	}
}

func TestWatchWorkspaceSaveLeaseStopsWithContext(t *testing.T) {
	e, id := checkpointFixture(t)
	use, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := false
	done := e.watchWorkspaceSaveLease(ctx, id, use, func() { cancelled = true })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher outlived its context")
	}
	if cancelled {
		t.Fatal("watcher cancelled a save whose lease was still held")
	}
	if err = e.repo.FinishWorkspaceUse(id, use, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
}
