package runtime

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitLabEnvironmentScopesCredential(t *testing.T) {
	var aToken, bToken string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, p, ok := r.BasicAuth(); ok {
			aToken = p
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(401)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, p, ok := r.BasicAuth(); ok {
			bToken = p
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(401)
	}))
	defer b.Close()
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	if err := BindGitLabEnvironment(env, a.URL, "example-test-token"); err != nil {
		t.Fatal(err)
	}
	run := func(url string) {
		c := exec.Command("git", "ls-remote", url+"/g/r.git")
		c.Env = os.Environ()
		for k, v := range env {
			c.Env = append(c.Env, k+"="+v)
		}
		_ = c.Run()
	}
	run(a.URL)
	run(b.URL)
	if aToken != "example-test-token" || bToken != "" {
		t.Fatal("credential was not scoped to configured GitLab host")
	}
}

func TestGitLabEnvironmentResetsInheritedCredentialHelpers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, p, ok := r.BasicAuth(); ok && p == "example-test-token" {
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			_, _ = w.Write([]byte("001e# service=git-upload-pack\n0000"))
			_, _ = w.Write([]byte("0000"))
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(401)
	}))
	defer srv.Close()
	home := t.TempDir()
	store := filepath.Join(home, ".git-credentials")
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[credential]\n\thelper = store --file "+store+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0", "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "GIT_CONFIG_NOSYSTEM": "1"}
	if err := BindGitLabEnvironment(env, srv.URL, "example-test-token"); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("git", "ls-remote", srv.URL+"/g/r.git")
	c.Env = os.Environ()
	for k, v := range env {
		c.Env = append(c.Env, k+"="+v)
	}
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("authenticated ls-remote failed: %v\n%s", err, out)
	}
	if data, err := os.ReadFile(store); err == nil && strings.Contains(string(data), "example-test-token") {
		t.Fatalf("inherited credential.helper=store persisted the GitLab PAT: %q", data)
	}
}
