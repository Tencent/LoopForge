package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConversationStopFencesQueuedStartAndRepeatedIDs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p := &ConversationProcessPool{}
	root := t.TempDir()
	id, err := NewConversationProcessID()
	if err != nil {
		t.Fatal(err)
	}
	stop := ConversationProcessRequest{Operation: "stop", ID: id}
	for range 2 {
		result, err := p.Handle(ctx, root, "", nil, stop, nil)
		if err != nil || !result.Done {
			t.Fatalf("stop did not fence queued start: %+v %v", result, err)
		}
	}
	start := ConversationProcessRequest{Operation: "start", ID: id, Command: &ConversationCommand{Argv: []string{"sh", "-c", "touch leaked"}}}
	if _, err := p.Handle(ctx, root, "", nil, start, nil); err == nil {
		t.Fatal("cancelled queued start executed")
	}
	if _, err := p.Handle(ctx, t.TempDir(), "", nil, stop, nil); err == nil {
		t.Fatal("stop crossed its root binding")
	}
	next, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "start", Command: &ConversationCommand{Argv: []string{"sh", "-c", "printf next"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := conversationPoll(t, p, ctx, root, next.ID); got != "next" {
		t.Fatal(got)
	}
	if _, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "start", ID: next.ID, Command: start.Command}, nil); err == nil {
		t.Fatal("completed ID was replayed")
	}
	if _, err := os.Stat(filepath.Join(root, "ws", "leaked")); !os.IsNotExist(err) {
		t.Fatal("delayed or replayed command changed files", err)
	}
}

func TestConversationStopCancelsPreparationAndReleasesResources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p := &ConversationProcessPool{}
	root := t.TempDir()
	id, err := NewConversationProcessID()
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	var released atomic.Int32
	factory := func(ctx context.Context, _ string) (Sandbox, func(), error) {
		close(entered)
		<-ctx.Done()
		return nil, func() { released.Add(1) }, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "start", ID: id, Command: &ConversationCommand{Argv: []string{"sh", "-c", "touch leaked"}, Interactive: true}}, nil, factory)
		done <- err
	}()
	<-entered
	before := time.Now()
	result, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "stop", ID: id}, nil)
	if err != nil || !result.Done {
		t.Fatalf("stop preparation: %+v %v", result, err)
	}
	if time.Since(before) > time.Second || released.Load() != 1 {
		t.Fatalf("preparation was not promptly cleaned up: %v resources=%d", time.Since(before), released.Load())
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "prepare"}, nil); err != nil {
		t.Fatal("root stayed reserved", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ws", "leaked")); !os.IsNotExist(err) {
		t.Fatal("cancelled preparation executed", err)
	}
}

func TestConversationStopCancelsStartWaitingForAssets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p := &ConversationProcessPool{}
	root := t.TempDir()
	unlock, err := p.lockAssets(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	id, err := NewConversationProcessID()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "start", ID: id, Command: &ConversationCommand{Argv: []string{"sh", "-c", "touch leaked"}}}, nil)
		done <- err
	}()
	// Wait until the startup reservation is addressable while the asset lock is held.
	for {
		p.mu.Lock()
		registered := p.entries[id] != nil
		p.mu.Unlock()
		if registered {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	stopped, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "stop", ID: id}, nil)
	if err != nil || !stopped.Done {
		t.Fatalf("stop waited on the asset lock: %+v %v", stopped, err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConversationStartRejectsExpiredIDsAfterTombstoneExpiry(t *testing.T) {
	p := &ConversationProcessPool{}
	id := fmt.Sprintf("%016x", time.Now().Add(-conversationStartWindow-time.Minute).UnixMilli()) + strings.Repeat("a", 32)
	_, err := p.Handle(context.Background(), t.TempDir(), "", nil, ConversationProcessRequest{Operation: "start", ID: id, Command: &ConversationCommand{Argv: []string{"sh", "-c", "true"}}}, nil)
	if err == nil {
		t.Fatal("an expired queued start was allowed after its cancellation tombstone could be reaped")
	}
	stopped, err := p.Handle(context.Background(), t.TempDir(), "", nil, ConversationProcessRequest{Operation: "stop", ID: id}, nil)
	if err != nil || !stopped.Done {
		t.Fatalf("reaped expired process did not confirm stop: %+v %v", stopped, err)
	}
}
