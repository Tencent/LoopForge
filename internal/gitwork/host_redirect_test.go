package gitwork

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHostCredentialRefusesGitHTTPRedirect(t *testing.T) {
	var redirected, authenticated atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, token, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if token == "redirect-test-secret" {
			authenticated.Add(1)
		}
		http.Redirect(w, r, destination.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	defer origin.Close()
	ctx := WithHostCredential(context.Background(), origin.URL, "redirect-test-secret")
	output, err := Run(ctx, t.TempDir(), "ls-remote", origin.URL+"/team/repo.git")
	if err == nil || authenticated.Load() == 0 || redirected.Load() != 0 {
		t.Fatalf("redirect guard: err=%v authenticated=%d redirected=%d", err, authenticated.Load(), redirected.Load())
	}
	if strings.Contains(output+err.Error(), "redirect-test-secret") {
		t.Fatal("token in redirected Git error")
	}
}
