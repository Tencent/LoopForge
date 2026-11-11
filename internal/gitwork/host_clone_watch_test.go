package gitwork

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/procwatch"
)

func TestWatchRedactsHostCredentialInProgressAndFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Git requires a POSIX shell")
	}
	const token = "watch-host-secret"
	bin := t.TempDir()
	script := `#!/bin/sh
set -eu
test "${AGENTFLOW_GIT_HTTP_PASSWORD:-}" = watch-host-secret || exit 61
test "${AGENTFLOW_GIT_HTTP_USER:-}" = oauth2 || exit 62
case "$*" in *watch-host-secret*|*legacy-secret*) exit 63 ;; esac
case "$*" in *credential.https://gitlab.example.test.helper=*) ;; *) exit 64 ;; esac
printf 'Receiving objects: 50%% watch-host-secret\r' >&2
printf 'clone rejected watch-host-secret\n'
exit 65
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := WithToken(context.Background(), "legacy-secret")
	ctx = WithHostCredential(ctx, "https://gitlab.example.test", token)
	var events []procwatch.Event
	ctx = procwatch.WithObserver(ctx, func(event procwatch.Event) { events = append(events, event) })
	output, err := Watch(ctx, t.TempDir(), procwatch.Options{Stage: "clone", Label: "clone repository", Timeout: 5 * time.Second}, "clone", "--progress", "--", "https://gitlab.example.test/team/repo.git", ".")
	if err == nil || !strings.Contains(output, "clone rejected ***") || !strings.Contains(err.Error(), "clone rejected ***") {
		t.Fatalf("authenticated clone failure = %q, %v", output, err)
	}
	if strings.Contains(output+err.Error(), token) {
		t.Fatal("host credential leaked through clone output or error")
	}
	var progressed, failed bool
	for _, event := range events {
		if strings.Contains(event.Message, token) {
			t.Fatal("host credential leaked through clone observer")
		}
		progressed = progressed || event.Kind == procwatch.Progress && strings.Contains(event.Message, "Receiving objects: 50% ***")
		failed = failed || event.Kind == procwatch.Failed && strings.Contains(event.Message, "clone rejected ***")
	}
	if !progressed || !failed {
		t.Fatalf("missing redacted progress or failure event: %+v", events)
	}
}

func TestWatchCloneUsesHostCredentialWithoutPersistingIt(t *testing.T) {
	const token = "watch-clone-secret"
	remote, _ := workBranchFixture(t)
	workBranchGit(t, remote, "update-server-info")
	files := http.StripPrefix("/team/repo.git/", http.FileServer(http.Dir(remote)))
	var authenticated atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "oauth2" || password != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated.Add(1)
		files.ServeHTTP(w, r)
	}))
	defer server.Close()
	workspace := t.TempDir()
	url := server.URL + "/team/repo.git"
	ctx := WithHostCredential(context.Background(), server.URL, token)
	if output, err := Watch(ctx, workspace, procwatch.Options{Timeout: 5 * time.Second}, "clone", "--progress", "--", url, "."); err != nil {
		t.Fatalf("clone: %v (%s)", err, output)
	}
	if authenticated.Load() == 0 {
		t.Fatal("clone did not authenticate to configured host")
	}
	if got := workBranchGit(t, workspace, "remote", "get-url", "origin"); got != url {
		t.Fatalf("origin = %q; want clean URL %q", got, url)
	}
	config, err := os.ReadFile(filepath.Join(workspace, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), token) || strings.Contains(string(config), "credential") {
		t.Fatal("clone persisted authentication in repository config")
	}
	if raw, err := os.ReadFile(filepath.Join(workspace, "README.md")); err != nil || string(raw) != "baseline\n" {
		t.Fatalf("cloned content = %q, %v", raw, err)
	}
}

func TestWatchCloneNeverAuthenticatesToForeignHostOrFollowsRedirect(t *testing.T) {
	const token = "watch-redirect-secret"
	var redirected, authenticated, foreignAuthenticated atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		if _, password, ok := r.BasicAuth(); ok && password == token {
			foreignAuthenticated.Add(1)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, password, ok := r.BasicAuth(); !ok || password != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated.Add(1)
		http.Redirect(w, r, foreign.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	defer origin.Close()
	ctx := WithHostCredential(context.Background(), origin.URL, token)
	clone := func(url string) {
		t.Helper()
		output, err := Watch(ctx, t.TempDir(), procwatch.Options{Timeout: 5 * time.Second}, "clone", "--progress", "--", url+"/team/repo.git", ".")
		if err == nil {
			t.Fatal("unauthorized clone unexpectedly succeeded")
		}
		if strings.Contains(output+err.Error(), token) {
			t.Fatal("host credential leaked through clone failure")
		}
	}
	clone(origin.URL)
	if authenticated.Load() == 0 || redirected.Load() != 0 {
		t.Fatalf("clone redirect guard: authenticated=%d redirected=%d", authenticated.Load(), redirected.Load())
	}
	clone(foreign.URL)
	if redirected.Load() == 0 || foreignAuthenticated.Load() != 0 {
		t.Fatalf("foreign host guard: requests=%d authenticated=%d", redirected.Load(), foreignAuthenticated.Load())
	}
}
