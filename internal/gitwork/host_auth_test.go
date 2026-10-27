package gitwork

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostCredentialOnlySentToConfiguredOrigin(t *testing.T) {
	var gotConfigured, gotOther string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pass, ok := r.BasicAuth(); ok {
			gotConfigured = pass
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(401)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pass, ok := r.BasicAuth(); ok {
			gotOther = pass
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(401)
	}))
	defer b.Close()
	ctx := WithHostCredential(context.Background(), a.URL, "unique-test-token")
	_, _ = Run(ctx, t.TempDir(), "ls-remote", a.URL+"/team/repo.git")
	_, _ = Run(ctx, t.TempDir(), "ls-remote", b.URL+"/team/repo.git")
	if gotConfigured != "unique-test-token" {
		t.Error("configured host did not receive credential")
	}
	if gotOther != "" {
		t.Error("credential sent to different origin")
	}
}
