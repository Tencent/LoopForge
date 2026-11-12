package runtime

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
)

func TestWindowsWorkspaceFileURLReadsOnlyItsWorkspace(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, "demo 页面.html")
	if err := os.WriteFile(page, []byte("<h1>Windows preview</h1>"), 0600); err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(root, "site")
	if err := os.Mkdir(site, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("<p>index</p>"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.html")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "outside")
	createManagedBindingTestJunction(t, junction, filepath.Dir(outside))
	if content, err := os.ReadFile(filepath.Join(junction, "private.html")); err != nil || string(content) != "private" {
		t.Fatalf("junction fixture did not point outside the workspace: %q %v", content, err)
	}
	lookup := devServerLookup
	devServerLookup = func(context.Context, string) (browserEndpoint, bool) {
		return browserEndpoint{Kind: "local", Workspace: root}, true
	}
	t.Cleanup(func() { devServerLookup = lookup })
	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{
		{"/" + filepath.ToSlash(page), http.StatusOK, "<h1>Windows preview</h1>"},
		{"/" + filepath.ToSlash(site) + "/", http.StatusOK, "<p>index</p>"},
		{"/" + filepath.ToSlash(site), http.StatusMovedPermanently, ""},
		{"/" + filepath.ToSlash(outside), http.StatusForbidden, ""},
	} {
		state, err := HandleBrowser(context.Background(), t.Name(), domain.BrowserRequest{Operation: "file", File: tc.path})
		if err != nil || state.HTTP == nil || state.HTTP.Status != tc.status {
			t.Fatalf("file %q: HTTP=%+v err=%v", tc.path, state.HTTP, err)
		}
		if tc.body != "" && string(state.HTTP.Body) != tc.body {
			t.Fatalf("file %q: body=%q", tc.path, state.HTTP.Body)
		}
	}
	// Windows may reject the reparse traversal before it can classify it as outside.
	state, err := HandleBrowser(context.Background(), t.Name(), domain.BrowserRequest{Operation: "file", File: "/" + filepath.ToSlash(filepath.Join(junction, "private.html"))})
	if err != nil || state.HTTP == nil || state.HTTP.Status != http.StatusForbidden && state.HTTP.Status != http.StatusNotFound {
		t.Fatalf("outside junction was not rejected: HTTP=%+v err=%v", state.HTTP, err)
	}
}

func TestWindowsWorkspaceFileURLRejectsUNCAndAmbiguousPaths(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"//server/share/file.html", "//?/C:/file.html", "//./C:/file.html", "/C:relative.html", "C:relative.html", "/root-relative.html", "relative.html"} {
		if body, code := serveWorkspaceFile(root, path, 1024); code != workspaceFileOutside || len(body) != 0 {
			t.Fatalf("unsupported file path %q was accepted: code=%d body=%q", path, code, body)
		}
	}
}
