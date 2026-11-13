package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func conversationRequest(t *testing.T, req runtime.ConversationProcessRequest) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestConversationProcessRejectsInvalidRequests(t *testing.T) {
	key := strings.Repeat("a", 64)
	forward := runtime.ConversationToolForward(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	for _, tc := range []struct {
		name    string
		forward bool
		request domain.RunnerWorkspaceRequest
		want    string
	}{
		{"malformed body", true, domain.RunnerWorkspaceRequest{Conversation: json.RawMessage(`{`)}, "invalid conversation process request"},
		{"short key", true, domain.RunnerWorkspaceRequest{Conversation: conversationRequest(t, runtime.ConversationProcessRequest{Operation: "prepare", SessionKey: "abc"})}, "invalid conversation process request"},
		{"traversal key", true, domain.RunnerWorkspaceRequest{Conversation: conversationRequest(t, runtime.ConversationProcessRequest{Operation: "prepare", SessionKey: "../" + key[3:]})}, "invalid conversation process request"},
		{"upper-case key", true, domain.RunnerWorkspaceRequest{Conversation: conversationRequest(t, runtime.ConversationProcessRequest{Operation: "prepare", SessionKey: strings.Repeat("A", 64)})}, "invalid conversation process request"},
		{"invalid run id", true, domain.RunnerWorkspaceRequest{RunID: "..", Conversation: conversationRequest(t, runtime.ConversationProcessRequest{Operation: "prepare", SessionKey: key})}, "invalid run id"},
		{"run without sandbox", true, domain.RunnerWorkspaceRequest{RunID: "run-1", Conversation: conversationRequest(t, runtime.ConversationProcessRequest{Operation: "prepare", SessionKey: key})}, "仅支持 Docker"},
		{"non-docker sandbox", true, domain.RunnerWorkspaceRequest{RunID: "run-1", Sandbox: &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{Driver: "process"}}, Conversation: conversationRequest(t, runtime.ConversationProcessRequest{Operation: "prepare", SessionKey: key})}, "仅支持 Docker"},
		{"no control channel", false, domain.RunnerWorkspaceRequest{Conversation: conversationRequest(t, runtime.ConversationProcessRequest{Operation: "prepare", SessionKey: key})}, "control channel unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
			if tc.forward {
				d.conversationForward = forward
			}
			got := d.conversationProcess(context.Background(), tc.request)
			if got.Status != 400 || !strings.Contains(got.Error, tc.want) {
				t.Fatalf("response = %+v, want 400 containing %q", got, tc.want)
			}
			if _, err := os.Stat(filepath.Join(root, ".conversations")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected request touched the conversation root: %v", err)
			}
		})
	}
}

func TestConversationProcessPreparesNativeRootUnderWorkspaceRoot(t *testing.T) {
	key := strings.Repeat("0123456789abcdef", 4)
	root := t.TempDir()
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
	d.conversationForward = func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return nil, nil }

	got := d.conversationProcess(context.Background(), domain.RunnerWorkspaceRequest{
		Conversation: conversationRequest(t, runtime.ConversationProcessRequest{Operation: "prepare", SessionKey: key}),
	})
	if got.Status != 200 {
		t.Fatalf("prepare = %+v", got)
	}
	var result runtime.ConversationProcessResult
	if err := json.Unmarshal(got.Payload, &result); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".conversations", key)
	if result.Root != want {
		t.Fatalf("root = %q, want %q", result.Root, want)
	}
	for _, dir := range []string{"home/tmp", "ws"} {
		if info, err := os.Stat(filepath.Join(want, filepath.FromSlash(dir))); err != nil || !info.IsDir() {
			t.Fatalf("%s not prepared: %v", dir, err)
		}
	}
}

func TestLockConversationWorkspace(t *testing.T) {
	var gate sync.RWMutex
	if err := lockConversationWorkspace(context.Background(), &gate); err != nil {
		t.Fatalf("free gate: %v", err)
	}
	gate.Unlock()

	// A gate released shortly after the first attempt is acquired by polling.
	gate.RLock()
	go func() {
		time.Sleep(60 * time.Millisecond)
		gate.RUnlock()
	}()
	if err := lockConversationWorkspace(context.Background(), &gate); err != nil {
		t.Fatalf("gate released while waiting: %v", err)
	}
	gate.Unlock()

	// A held gate reports busy after the deadline, never blocks forever.
	gate.RLock()
	started := time.Now()
	err := lockConversationWorkspace(context.Background(), &gate)
	if !isWorkspaceBusyError(err) {
		t.Fatalf("held gate err = %v, want workspace busy", err)
	}
	if waited := time.Since(started); waited < time.Second || waited > 5*time.Second {
		t.Fatalf("busy after %s, want about 2s", waited)
	}

	// A cancelled context stops waiting immediately without taking the gate.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lockConversationWorkspace(ctx, &gate); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx err = %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := lockConversationWorkspace(ctx, &gate); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expiring ctx err = %v", err)
	}
	gate.RUnlock()
	if !gate.TryLock() {
		t.Fatal("failed waits must not leave the gate locked")
	}
	gate.Unlock()
}

func TestConversationProcessNativeStartRequiresInstalledCLI(t *testing.T) {
	key := strings.Repeat("b", 64)
	root := t.TempDir()
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
	d.conversationForward = func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return nil, nil }

	got := d.conversationProcess(context.Background(), domain.RunnerWorkspaceRequest{
		Conversation: conversationRequest(t, runtime.ConversationProcessRequest{
			Operation:  "start",
			SessionKey: key,
			Command:    &runtime.ConversationCommand{Argv: []string{"fixture-missing-cli", "--version"}, Provider: "fixture-missing-cli"},
		}),
	})
	if got.Status != 400 || !strings.Contains(got.Error, "执行机器未安装 CLI fixture-missing-cli") {
		t.Fatalf("start without CLI = %+v", got)
	}
	// The failed start releases its root, so a later prepare is not blocked.
	again := d.conversationProcess(context.Background(), domain.RunnerWorkspaceRequest{
		Conversation: conversationRequest(t, runtime.ConversationProcessRequest{Operation: "prepare", SessionKey: key}),
	})
	if again.Status != 200 {
		t.Fatalf("prepare after failed start = %+v", again)
	}
}
