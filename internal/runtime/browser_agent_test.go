package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestReapOrphanHostBrowsersKillsBrowsersOfDeadOwners(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	orphan := exec.Command("sleep", "60")
	orphan.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = orphan.Wait(); close(exited) }()
	defer func() { _ = orphan.Process.Kill() }()
	write := func(runID string, record hostBrowserRecord) string {
		dir := hostBrowserDir(runID)
		raw, _ := json.Marshal(record)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "endpoint.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	orphanDir := write("orphan", hostBrowserRecord{PID: orphan.Process.Pid, Owner: dead.Process.Pid})
	ownedDir := write("owned", hostBrowserRecord{PID: orphan.Process.Pid, Owner: os.Getpid()})
	reapOrphanHostBrowsers()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("orphan browser still running")
	}
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("orphan record kept: %v", err)
	}
	if _, err := os.Stat(ownedDir); err != nil {
		t.Fatalf("live owner's record removed: %v", err)
	}
}

func TestBrowserRefSelectorRejectsInjection(t *testing.T) {
	for _, ref := range []string{"", "e", "x1", "e1\"]", "e1'); alert(1); ('"} {
		if _, err := browserRefSelector(ref); err == nil {
			t.Fatalf("ref %q accepted", ref)
		}
	}
	if sel, err := browserRefSelector("e12"); err != nil || !strings.Contains(sel, `"e12"`) {
		t.Fatalf("sel=%q err=%v", sel, err)
	}
}

// TestBrowserAgentOperationsDriveRealChromium exercises snapshot, type, press
// and click against the host Chromium a local run would use.
func TestBrowserAgentOperationsDriveRealChromium(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := FindChromium(); err != nil {
		t.Skip("chromium not installed")
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/next" {
			fmt.Fprintf(w, `<title>结果</title><p>你搜索了 %s</p>`, r.URL.Query().Get("q"))
			return
		}
		fmt.Fprint(w, `<title>首页</title><h1>欢迎</h1>
<form action="/next"><input name="q" placeholder="搜索内容"><button>搜索</button></form>
<a href="/next?q=link">直接跳转</a><div style="display:none"><button>隐藏</button></div>`)
	}))
	defer page.Close()
	workspace := t.TempDir()
	runID := "browser-agent-" + fmt.Sprint(time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := ensureHostBrowser(ctx, workspace, runID); err != nil {
		t.Fatal(err)
	}
	defer stopHostBrowsers(workspace)

	state, err := HandleBrowser(ctx, runID, domain.BrowserRequest{Operation: "navigate", URL: page.URL, WithSnapshot: true})
	if err != nil || state.Snapshot == nil {
		t.Fatalf("navigate state=%+v err=%v", state, err)
	}
	refs := map[string]string{}
	for _, el := range state.Snapshot.Elements {
		refs[el.Name] = el.Ref
		if el.Name == "隐藏" {
			t.Fatalf("hidden element listed: %+v", state.Snapshot.Elements)
		}
	}
	if !strings.Contains(state.Snapshot.Text, "欢迎") || refs["搜索内容"] == "" || refs["直接跳转"] == "" {
		t.Fatalf("snapshot=%+v", state.Snapshot)
	}

	state, err = HandleBrowser(ctx, runID, domain.BrowserRequest{Operation: "type", Ref: refs["搜索内容"], Text: "你好 agent", Submit: true})
	if err != nil || state.Snapshot == nil || !strings.Contains(state.Snapshot.Text, "你搜索了 你好 agent") {
		t.Fatalf("type+submit state=%+v err=%v", state.Snapshot, err)
	}

	if _, err = HandleBrowser(ctx, runID, domain.BrowserRequest{Operation: "back", WithSnapshot: true}); err != nil {
		t.Fatal(err)
	}
	state, err = HandleBrowser(ctx, runID, domain.BrowserRequest{Operation: "snapshot"})
	if err != nil || state.Snapshot == nil {
		t.Fatalf("snapshot err=%v", err)
	}
	link := ""
	for _, el := range state.Snapshot.Elements {
		if el.Name == "直接跳转" {
			link = el.Ref
		}
	}
	state, err = HandleBrowser(ctx, runID, domain.BrowserRequest{Operation: "click", Ref: link})
	if err != nil || state.Snapshot == nil || !strings.Contains(state.Snapshot.Text, "你搜索了 link") {
		t.Fatalf("click state=%+v err=%v", state.Snapshot, err)
	}

	if _, err = HandleBrowser(ctx, runID, domain.BrowserRequest{Operation: "click", Ref: "e999"}); err == nil || !strings.Contains(err.Error(), "重新获取快照") {
		t.Fatalf("stale ref err=%v", err)
	}
	state, err = HandleBrowser(ctx, runID, domain.BrowserRequest{Operation: "screenshot"})
	if err != nil || len(state.Frame) < 100 {
		t.Fatalf("screenshot len=%d err=%v", len(state.Frame), err)
	}
}
