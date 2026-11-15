package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsConversationSyncPreservesJSON(t *testing.T) {
	root := t.TempDir()
	const source = "/platform/conversations/session"
	data := []byte(`{"cwd":"/platform/conversations/session/ws"}`)
	pool := &ConversationProcessPool{}
	_, err := pool.Handle(context.Background(), root, "", nil, ConversationProcessRequest{
		Operation: "sync", SourceRoot: source,
		Files: []ConversationFile{{Path: "current-settings.json", Data: data, Mode: 0600, Rewrite: true}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(root, "current-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var frame struct{ Cwd string }
	if err := json.Unmarshal(written, &frame); err != nil {
		t.Fatalf("remapped settings are invalid JSON: %v", err)
	}
	if frame.Cwd != filepath.ToSlash(root)+"/ws" {
		t.Fatalf("remapped cwd = %q", frame.Cwd)
	}
}

func TestWindowsConversationStdinPreservesJSON(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	const source = "/platform/conversations/session"
	pool := &ConversationProcessPool{}
	started, err := pool.Handle(ctx, root, "", nil, ConversationProcessRequest{
		Operation: "start", SourceRoot: source,
		Command: &ConversationCommand{Argv: []string{"sh", "-c", `IFS= read -r line; printf '%s\n' "$line"`}, Interactive: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := `{"method":"thread/start","params":{"cwd":"/platform/conversations/session/ws"}}` + "\n"
	if _, err := pool.Handle(ctx, root, "", nil, ConversationProcessRequest{
		Operation: "write", ID: started.ID, SourceRoot: source, Data: input,
	}, nil); err != nil {
		t.Fatal(err)
	}
	output := conversationPoll(t, pool, ctx, root, started.ID)
	var frame struct {
		Method string
		Params struct{ Cwd string }
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &frame); err != nil {
		t.Fatalf("CLI received invalid JSON: %v", err)
	}
	if frame.Method != "thread/start" || frame.Params.Cwd != filepath.ToSlash(root)+"/ws" {
		t.Fatalf("CLI received incorrect frame: %+v", frame)
	}
}
