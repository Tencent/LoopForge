package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestWindowsTerminalLongScriptCleansUpOnCancel(t *testing.T) {
	gitBin := `C:\Program Files\Git\bin`
	if _, err := os.Stat(filepath.Join(gitBin, "sh.exe")); err != nil {
		t.Skip("Git/MSYS shell is not installed")
	}
	t.Setenv("PATH", gitBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	temporary := t.TempDir()
	t.Setenv("TMP", temporary)
	t.Setenv("TEMP", temporary)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}, client: NewClient(server.URL), controls: map[string]chan string{}, pendingControls: map[string]string{}}
	task := domain.RunnerTask{ID: "long-cancel", Workspace: "task", Command: ": \"" + strings.Repeat("x", 25000) + "\"\nprintf ready > ready; while :; do sleep 1; done"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan execResult, 1)
	go func() { done <- d.executeTerminal(ctx, task) }()
	marker := filepath.Join(d.taskWorkspace(task), "ready")
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			result := <-done
			t.Fatalf("script did not start: %s %s", result.errMsg, result.result)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case result := <-done:
		if result.status != domain.RunnerTaskCanceled {
			t.Fatalf("cancel result=%s err=%s", result.status, result.errMsg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop the terminal script")
	}
	entries, err := os.ReadDir(temporary)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancel leaked private shell script: entries=%d err=%v", len(entries), err)
	}
}
