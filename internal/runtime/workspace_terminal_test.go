package runtime

import (
	"agentflow/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceTerminalRealPTYAndScope(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTFLOW_TEST_SECRET", "do-not-inherit")
	var pool TerminalPool
	opened, err := pool.Handle(root, "run:1", domain.WorkspaceTerminalRequest{Operation: "open", ExpiresAt: time.Now().Add(time.Minute)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.CloseScope("run:1")
	if _, err = pool.Handle(root, "run:2", domain.WorkspaceTerminalRequest{Operation: "input", ID: opened.ID, Data: "pwd\n"}, nil); err == nil {
		t.Fatal("foreign actor accessed terminal")
	}
	if _, err = pool.Handle(root, "run:1", domain.WorkspaceTerminalRequest{Operation: "resize", ID: opened.ID, Cols: 120, Rows: 40}, nil); err != nil {
		t.Fatal(err)
	}
	input := "mkdir child; cd child; printf 'persistent-terminal' > note.txt; printf '%s' \"$AGENTFLOW_TEST_SECRET\" > secret.txt\n"
	if _, err = pool.Handle(root, "run:1", domain.WorkspaceTerminalRequest{Operation: "input", ID: opened.ID, Data: input}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(filepath.Join(root, "child/note.txt"))
		if string(body) == "persistent-terminal" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if body, _ := os.ReadFile(filepath.Join(root, "child/note.txt")); string(body) != "persistent-terminal" {
		t.Fatal("terminal did not run in workspace")
	}
	if body, _ := os.ReadFile(filepath.Join(root, "child/secret.txt")); len(body) != 0 {
		t.Fatal("server environment leaked")
	}
	if _, err = pool.Handle(root, "run:1", domain.WorkspaceTerminalRequest{Operation: "input", ID: opened.ID, Data: "pwd\n"}, nil); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for time.Now().Before(deadline) {
		result, err := pool.Handle(root, "run:1", domain.WorkspaceTerminalRequest{Operation: "read", ID: opened.ID}, nil)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(result.Data)
		if strings.Contains(output.String(), filepath.Join(root, "child")) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(output.String(), filepath.Join(root, "child")) {
		t.Fatal("terminal lost working directory")
	}
	pool.CloseScope("run:1")
	if _, err = pool.Handle(root, "run:1", domain.WorkspaceTerminalRequest{Operation: "read", ID: opened.ID}, nil); err == nil {
		t.Fatal("closed shell accessible")
	}
}
func TestWorkspaceTerminalExpiresEvenWhenPolled(t *testing.T) {
	var pool TerminalPool
	release := make(chan struct{})
	req := domain.WorkspaceTerminalRequest{Operation: "open", ExpiresAt: time.Now().Add(1200 * time.Millisecond)}
	opened, err := pool.Handle(t.TempDir(), "expiring:1", req, func() { close(release) })
	if err != nil {
		t.Fatal(err)
	}
	defer pool.CloseScope("expiring:1")
	select {
	case <-release:
	case <-time.After(2 * time.Second):
		t.Fatal("shell outlived editor lease")
	}
	if _, err := pool.Handle(t.TempDir(), "expiring:1", domain.WorkspaceTerminalRequest{Operation: "read", ID: opened.ID}, nil); err == nil {
		t.Fatal("expired terminal still accessible")
	}
}
