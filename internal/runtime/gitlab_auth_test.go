package runtime

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
