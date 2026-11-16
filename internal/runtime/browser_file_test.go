package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// workspaceFileTree lays out a workspace plus a file outside it.
func workspaceFileTree(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	for name, body := range map[string]string{
		"ws/demo.html":       "<h1>demo</h1>",
		"ws/site/index.html": "<p>site</p>",
		"ws/site/app.js":     "console.log(1)",
		"ws/empty/.keep":     "",
		"ws/big.txt":         strings.Repeat("x", 64),
		"secret.txt":         "secret",
	} {
		p := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(root, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	return root
}

var workspaceFileCases = []struct {
	path string
	code int
	body string
}{
	{"/demo.html", workspaceFileOK, "<h1>demo</h1>"},
	{"/site/", workspaceFileOK, "<p>site</p>"},
	{"/site/app.js", workspaceFileOK, "console.log(1)"},
	{"/site", workspaceFileDirectory, ""},
	{"/empty/", workspaceFileNoIndexHTML, ""},
	{"/missing.html", workspaceFileMissing, ""},
	{"/leak.txt", workspaceFileOutside, ""},
	{"/../secret.txt", workspaceFileOutside, ""},
	{"/big.txt", workspaceFileTooLarge, ""},
}

func TestServeWorkspaceFileStaysInTheWorkspace(t *testing.T) {
	root := workspaceFileTree(t)
	for _, tc := range workspaceFileCases {
		body, code := serveWorkspaceFile(root, root+tc.path, 32)
		if code != tc.code || string(body) != tc.body {
			t.Fatalf("%s: code %d body %q", tc.path, code, body)
		}
	}
}

// The sidecar script must agree with the host reader on every case.
func TestWorkspaceFileScriptMatchesHostReader(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	root := workspaceFileTree(t)
	for _, tc := range workspaceFileCases {
		out, err := exec.Command("node", "-e", workspaceFileScript, root, root+tc.path, "32").Output()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != tc.code || (code == 0 && string(out) != tc.body) {
			t.Fatalf("%s: code %d body %q", tc.path, code, out)
		}
	}
}

func TestHandleWorkspaceFileResponses(t *testing.T) {
	root := workspaceFileTree(t)
	lookup, execFile := devServerLookup, workspaceFileExec
	t.Cleanup(func() { devServerLookup, workspaceFileExec = lookup, execFile })
	devServerLookup = func(context.Context, string) (browserEndpoint, bool) {
		return browserEndpoint{Kind: "docker", Container: "sidecar"}, true
	}
	var seen []string
	workspaceFileExec = func(_ context.Context, container, file string) ([]byte, int, error) {
		seen = append(seen, container+" "+file)
		body, code := serveWorkspaceFile(root, root+strings.TrimPrefix(file, dockerWorkspaceRoot), 32)
		return body, code, nil
	}
	get := func(file string) domain.BrowserState {
		t.Helper()
		state, err := HandleBrowser(context.Background(), "file-run", domain.BrowserRequest{Operation: "file", File: file})
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	for _, tc := range []struct {
		file, contentType string
		status            int
	}{
		{"/workspace/demo.html", "text/html; charset=utf-8", 200},
		{"/workspace/site/", "text/html; charset=utf-8", 200},
		{"/workspace/site/app.js", "text/javascript; charset=utf-8", 200},
		{"/workspace/site", "", 301},
		{"/workspace/missing.html", "text/plain; charset=utf-8", 404},
		{"/workspace/leak.txt", "text/plain; charset=utf-8", 403},
	} {
		state := get(tc.file)
		if state.HTTP == nil || state.HTTP.Status != tc.status {
			t.Fatalf("%s: %+v", tc.file, state)
		}
		if got := strings.Join(state.HTTP.Header["Content-Type"], ""); got != tc.contentType {
			t.Fatalf("%s: content type %q", tc.file, got)
		}
	}
	if state := get("/workspace/big.txt"); state.HTTP != nil || !strings.Contains(state.Reason, strconv.Itoa(DevServerBodyLimit>>20)+" MB") {
		t.Fatalf("too large: %+v", state)
	}
	if seen[0] != "sidecar /workspace/demo.html" {
		t.Fatalf("exec: %v", seen)
	}
	if _, err := HandleBrowser(context.Background(), "file-run", domain.BrowserRequest{Operation: "file", File: "relative.html"}); err == nil {
		t.Fatal("relative path accepted")
	}

	// A host browser reads its own workspace directly.
	devServerLookup = func(context.Context, string) (browserEndpoint, bool) {
		return browserEndpoint{Kind: "local", Workspace: root}, true
	}
	if state := get(root + "/site/"); state.HTTP == nil || string(state.HTTP.Body) != "<p>site</p>" {
		t.Fatalf("host: %+v", state)
	}
	devServerLookup = func(context.Context, string) (browserEndpoint, bool) { return browserEndpoint{}, false }
	if state := get("/workspace/demo.html"); state.Available || state.HTTP != nil {
		t.Fatalf("no browser: %+v", state)
	}
}
