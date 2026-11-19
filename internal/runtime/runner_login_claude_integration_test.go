package runtime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	gort "runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The official client, real stdin/stdout bridge, and production OAuth adapter
// run against a loopback fixture. No Claude subscription or live auth is used.
func TestManagedLoginOfficialClaude(t *testing.T) {
	binary := os.Getenv("LOOPFORGE_PROBE_CLAUDE_BIN")
	if binary == "" {
		t.Skip("set LOOPFORGE_PROBE_CLAUDE_BIN for the offline official-client test")
	}
	if gort.GOOS != "darwin" || !filepath.IsAbs(binary) {
		t.Fatal("requires macOS and an absolute official binary path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	fixture := exec.CommandContext(ctx, "python3", "../../scripts/probe-cli-auth-control.py", "claude", "--serve-only")
	input, err := fixture.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := fixture.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	fixture.Stderr = &diagnostics
	if err := fixture.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); cancel(); _ = fixture.Wait() }()
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() {
		t.Fatalf("offline fixture failed: %s", diagnostics.String())
	}
	var settings struct{ URL, Proxy, CA string }
	if err := json.Unmarshal(scanner.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(settings.CA)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("invalid fixture CA")
	}
	proxy, err := url.Parse(settings.Proxy)
	if err != nil {
		t.Fatal(err)
	}
	manager := &runnerLoginManager{stateDir: t.TempDir(), client: &http.Client{Timeout: 15 * time.Second,
		Transport:     &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	source, _, _ := managedLoginFixture(t, "claude-code")
	initial, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "sk-ant-" + "oat01-probe-not-a-real-token-0", "refreshToken": "runner-owned-fake-refresh-0",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:inference", "user:profile"}}})
	if err := os.WriteFile(source.path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	var leases []*runnerLoginLease
	for range 3 {
		lease, err := newRunnerLoginLease(ctx, manager, source, false)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.close()
		leases = append(leases, lease)
	}
	realHome, _ := os.UserHomeDir()
	profile := `(version 1) (allow default) (deny network*) (allow network-outbound (remote ip "localhost:*"))`
	for _, path := range []string{filepath.Join(realHome, ".codex"), filepath.Join(realHome, ".claude"), filepath.Join(realHome, ".gemini"), filepath.Join(realHome, "Library", "Keychains")} {
		quoted, _ := json.Marshal(path)
		profile += ` (deny file-read* file-write* (subpath ` + string(quoted) + `))`
	}
	quoted, _ := json.Marshal(realHome)
	profile += ` (deny file-write* (subpath ` + string(quoted) + `))`
	var workers sync.WaitGroup
	for _, lease := range leases {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		config := filepath.Join(home, ".claude")
		if err := os.MkdirAll(config, 0700); err != nil {
			t.Fatal(err)
		}
		workers.Add(1)
		go func(lease *runnerLoginLease, root, home, config string) {
			defer workers.Done()
			command := WithCommandEnvironment(CommandSpec{Provider: "claude-code", Argv: []string{
				"/usr/bin/sandbox-exec", "-p", profile, binary, "--print", "--output-format", "stream-json", "--verbose",
				"--model", "claude-sonnet-4-6", "--max-turns", "1", "--tools", "", "--strict-mcp-config", "--setting-sources", "", "Reply OFFLINE_AUTH_OK."}}, lease.environment())
			cmd := exec.CommandContext(ctx, command.Argv[0], command.Argv[1:]...)
			cmd.Dir = root
			cmd.Env = appendEnvironment([]string{"PATH=/usr/bin:/bin", "HOME=" + home, "CLAUDE_CONFIG_DIR=" + config, "TMPDIR=" + root,
				"HTTP_PROXY=" + settings.Proxy, "HTTPS_PROXY=" + settings.Proxy, "ALL_PROXY=" + settings.Proxy, "NO_PROXY=127.0.0.1,localhost",
				"NODE_EXTRA_CA_CERTS=" + settings.CA, "SSL_CERT_FILE=" + settings.CA, "DO_NOT_TRACK=1", "NO_COLOR=1",
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1"}, command.environment)
			out, err := runCommand(cmd, nil, command)
			if err != nil || !strings.Contains(out, "OFFLINE_AUTH_OK") || strings.Contains(out, "oauth_token_refresh") || strings.Contains(out, "runner-owned-fake-refresh") {
				t.Errorf("official client did not recover through production bridge: %v; fake diagnostics %.800s", err, out)
			}
		}(lease, root, home, config)
	}
	workers.Wait()
	_ = input.Close()
	if !scanner.Scan() {
		t.Fatal("missing fixture results")
	}
	var result struct{ Refreshes, Refusals, Successes int }
	if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Refreshes != 1 || result.Refusals != 3 || result.Successes != 3 {
		t.Fatalf("offline fixture results: %+v", result)
	}
	raw, err := os.ReadFile(source.path)
	if err != nil || !bytes.Contains(raw, []byte("runner-owned-fake-refresh-1")) {
		t.Fatal("rotated pair not saved on host")
	}
	t.Log("3 official Claude processes recovered from 401 through 1 host exchange; no live account used")
}
