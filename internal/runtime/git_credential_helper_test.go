package runtime

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGitCredentialHelperAsksTheSessionGateway(t *testing.T) {
	var asked []map[string]string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Method string            `json:"method"`
			Params map[string]string `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rpc)
		if rpc.Method != gitCredentialMethod {
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		asked = append(asked, rpc.Params)
		result := map[string]any{}
		if rpc.Params["host"] == "gitlab.example.org:8443" {
			result = map[string]any{"username": "oauth2", "password": "glpat-secret", "password_expiry_utc": 1900000000}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer gateway.Close()
	t.Setenv(SessionGatewayEnv, gateway.URL+"/internal/agent-tools/token/mcp")

	run := func(op, input string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := gitCredentialMain([]string{op}, strings.NewReader(input), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	code, out, _ := run("get", "protocol=https\nhost=gitlab.example.org:8443\npath=group/app.git\n\n")
	if code != 0 || out != "username=oauth2\npassword=glpat-secret\npassword_expiry_utc=1900000000\n" {
		t.Fatalf("get = %d %q", code, out)
	}
	if asked[0]["caller"] != "git" || asked[0]["protocol"] != "https" {
		t.Fatalf("gateway params = %v", asked[0])
	}
	if code, out, _ := run("get", "protocol=https\nhost=unknown.example.org\n\n"); code != 0 || out != "" {
		t.Fatalf("a host without a credential must produce no output: %d %q", code, out)
	}
	before := len(asked)
	for _, input := range []string{"protocol=http\nhost=gitlab.example.org:8443\n\n", "protocol=https\n\n"} {
		if code, out, _ := run("get", input); code != 0 || out != "" {
			t.Fatalf("get %q = %d %q", input, code, out)
		}
	}
	for _, op := range []string{"store", "erase"} {
		if code, out, _ := run(op, "protocol=https\nhost=gitlab.example.org:8443\nusername=u\npassword=p\n\n"); code != 0 || out != "" {
			t.Fatalf("%s must be a no-op: %d %q", op, code, out)
		}
	}
	if len(asked) != before {
		t.Fatal("plain HTTP, hostless, store and erase requests must not reach the gateway")
	}

	gateway.Close()
	if code, out, errOut := run("get", "protocol=https\nhost=gitlab.example.org:8443\n\n"); code != 0 || out != "" || !strings.Contains(errOut, "unreachable") {
		t.Fatalf("an ended turn must fail quietly for Git: %d %q %q", code, out, errOut)
	}
	t.Setenv(SessionGatewayEnv, "")
	if code, out, _ := run("get", "protocol=https\nhost=gitlab.example.org:8443\n\n"); code != 0 || out != "" {
		t.Fatalf("outside a turn: %d %q", code, out)
	}
}

func TestCLIShimHost(t *testing.T) {
	env := map[string]string{}
	getenv := func(key string) string { return env[key] }
	origin := ""
	remote := func() string { return origin }
	cases := []struct {
		name, origin string
		env          map[string]string
		args         []string
		want         string
	}{
		{name: "gh", want: "github.com"},
		{name: "glab", want: "gitlab.com"},
		{name: "gh", origin: "https://ghe.example.com/org/app.git", want: "ghe.example.com"},
		{name: "glab", origin: "git@gitlab.example.org:group/app.git", want: "gitlab.example.org"},
		{name: "glab", origin: "https://gitlab.example.org:8443/group/app.git", want: "gitlab.example.org:8443"},
		{name: "glab", origin: "https://[FD00:0:0::2]:8443/group/app.git", want: "[fd00::2]:8443"},
		{name: "glab", args: []string{"mr", "list", "--repo", "[FD00:0:0::2]:8443/group/app"}, want: "[fd00::2]:8443"},
		{name: "gh", env: map[string]string{"GH_HOST": "GHE.example.com"}, origin: "https://github.com/o/r", want: "ghe.example.com"},
		{name: "glab", env: map[string]string{"GITLAB_HOST": "https://gitlab.example.org"}, want: "gitlab.example.org"},
		{name: "glab", env: map[string]string{"GL_HOST": "gl.example.org"}, want: "gl.example.org"},
		{name: "gh", args: []string{"api", "--hostname", "ghe.example.com", "user"}, want: "ghe.example.com"},
		{name: "gh", args: []string{"api", "--hostname=ghe.example.com"}, want: "ghe.example.com"},
		{name: "gh", args: []string{"pr", "list", "-R", "ghe.example.com/org/app"}, origin: "https://github.com/o/r", want: "ghe.example.com"},
		{name: "gh", args: []string{"pr", "list", "--repo", "org/app"}, origin: "https://ghe.example.com/o/r", want: "ghe.example.com"},
		{name: "glab", args: []string{"mr", "list", "--repo", "https://gitlab.example.org/group/app"}, want: "gitlab.example.org"},
		{name: "gh", args: []string{"--", "--hostname", "evil.example.net"}, want: "github.com"},
	}
	for _, c := range cases {
		env, origin = c.env, c.origin
		if env == nil {
			env = map[string]string{}
		}
		if got := cliShimHost(c.name, c.args, getenv, remote); got != c.want {
			t.Errorf("%s %v (origin %q, env %v) = %q, want %q", c.name, c.args, c.origin, c.env, got, c.want)
		}
	}
}

func TestCLIShimEnv(t *testing.T) {
	base := []string{"PATH=/bin", "GH_TOKEN=stale", "GITLAB_TOKEN=stale"}
	has := func(env []string, kv string) bool { return slices.Contains(env, kv) }
	env := cliShimEnv("gh", "github.com", "tok", base)
	if !has(env, "GH_TOKEN=tok") || has(env, "GH_TOKEN=stale") || !has(env, "PATH=/bin") {
		t.Fatalf("gh github.com env = %v", env)
	}
	env = cliShimEnv("gh", "ghe.example.com", "tok", base)
	if !has(env, "GH_ENTERPRISE_TOKEN=tok") || !has(env, "GH_HOST=ghe.example.com") {
		t.Fatalf("gh enterprise env = %v", env)
	}
	env = cliShimEnv("glab", "gitlab.example.org:8443", "tok", base)
	if !has(env, "GITLAB_TOKEN=tok") || !has(env, "GITLAB_HOST=https://gitlab.example.org:8443") || has(env, "GITLAB_TOKEN=stale") {
		t.Fatalf("glab env = %v", env)
	}
}

func TestLookPathSkippingIgnoresTheShimDirectory(t *testing.T) {
	shims, tools := t.TempDir(), t.TempDir()
	for _, dir := range []string{shims, tools} {
		if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := lookPathSkipping("gh", shims, shims+string(os.PathListSeparator)+tools)
	if err != nil || got != filepath.Join(tools, "gh") {
		t.Fatalf("lookPathSkipping = %q, %v", got, err)
	}
	if _, err := lookPathSkipping("gh", shims, shims); err == nil {
		t.Fatal("the shim directory alone must not satisfy the lookup")
	}
}
