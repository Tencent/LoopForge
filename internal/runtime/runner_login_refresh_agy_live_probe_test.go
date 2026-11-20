package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goRuntime "runtime"
	"strings"
	"testing"
	"time"
)

// This is the ONLY experiment that may use real credentials. It requires an
// explicit opt-in, reads only the existing Antigravity keychain item, executes
// the official CLI's model-list command, and preserves refreshed credentials.
// It never opens Codex/Claude credentials or invokes a billed model response.
func TestWritebackProbeAntigravityLiveRefresh(t *testing.T) {
	if os.Getenv("LOOPFORGE_PROBE_AGY_LIVE") != "1" {
		t.Skip("explicit Antigravity live-auth opt-in required")
	}
	if goRuntime.GOOS != "darwin" {
		t.Fatal("live probe currently requires the macOS Antigravity keychain")
	}
	binary := os.Getenv("LOOPFORGE_PROBE_AGY_BIN")
	if !filepath.IsAbs(binary) {
		t.Fatal("set LOOPFORGE_PROBE_AGY_BIN to the official absolute binary path")
	}
	image := os.Getenv("LOOPFORGE_PROBE_AGY_DOCKER_IMAGE")
	if image == "" {
		t.Fatal("set LOOPFORGE_PROBE_AGY_DOCKER_IMAGE to an existing credential-free official CLI image")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("Docker unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, docker, "image", "inspect", image).Run(); err != nil {
		t.Fatal("prebuilt Antigravity image unavailable")
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cacheDir, "lf-agy-refresh-live-*")
	if err != nil {
		t.Fatal(err)
	}
	keepRecovery := false
	defer func() {
		if keepRecovery {
			t.Logf("private recovery credentials retained at %s", root)
		} else if err := os.RemoveAll(root); err != nil {
			t.Error("could not remove private probe credentials")
		}
	}()
	type loginRecord struct {
		Token struct {
			Access  string    `json:"access_token"`
			Refresh string    `json:"refresh_token"`
			Expiry  time.Time `json:"expiry"`
		} `json:"token"`
	}
	decode := func(raw []byte) (loginRecord, error) {
		var record loginRecord
		err := json.Unmarshal(raw, &record)
		if err == nil && record.Token.Access == "" {
			err = errors.New("no access token")
		}
		return record, err
	}
	original, err := readAntigravityKeyring()
	if err != nil {
		t.Fatal("cannot read existing Antigravity login")
	}
	initial, err := decode([]byte(original))
	if err != nil || initial.Token.Refresh == "" {
		t.Fatal("existing Antigravity login has no refresh token")
	}
	expectedHost := []byte(original)
	if err := os.WriteFile(filepath.Join(root, "original.json"), expectedHost, 0600); err != nil {
		t.Fatal(err)
	}
	preserve := func(raw []byte) bool {
		t.Helper()
		candidate, err := decode(raw)
		if err != nil {
			t.Error("official CLI produced an invalid credential")
			return false
		}
		current, keep, err := antigravityKeyringLogin(readAntigravityKeyring, writeAntigravityKeyring)
		if err != nil {
			keepRecovery = true
			t.Error("cannot re-read host Antigravity login; private responses retained")
			return false
		}
		if !bytes.Equal(current, expectedHost) {
			host, hostErr := decode(current)
			if hostErr == nil && host.Token.Refresh == candidate.Token.Refresh && sameAntigravityLogin(current, raw) {
				expectedHost = append([]byte(nil), current...)
				t.Log("host Antigravity source advanced with the same refresh token; kept the host update")
				return true
			}
			keepRecovery = true
			t.Error("host Antigravity login changed identity or refresh token; private responses retained")
			return false
		}
		err = keep(raw)
		stored, readErr := readAntigravityKeyring()
		if err != nil || readErr != nil {
			keepRecovery = true
			t.Error("could not preserve refreshed Antigravity login; private responses retained")
			return false
		}
		if !bytes.Equal([]byte(stored), raw) {
			host, hostErr := decode([]byte(stored))
			if hostErr != nil || host.Token.Refresh != candidate.Token.Refresh || !sameAntigravityLogin([]byte(stored), raw) {
				keepRecovery = true
				t.Error("host changed during preservation; private responses retained")
				return false
			}
		}
		expectedHost = []byte(stored)
		return true
	}
	type result struct {
		Name   string
		Raw    []byte
		Record loginRecord
		Models int
		Err    error
	}
	run := func(name, driver string, seed []byte, expire, accessOnly bool) result {
		r := result{Name: name}
		home := filepath.Join(root, name)
		dir := filepath.Join(home, ".gemini", "antigravity-cli")
		if r.Err = os.MkdirAll(dir, 0700); r.Err != nil {
			return r
		}
		var doc map[string]any
		if r.Err = json.Unmarshal(seed, &doc); r.Err != nil {
			return r
		}
		token, ok := doc["token"].(map[string]any)
		if !ok {
			r.Err = errors.New("missing token record")
			return r
		}
		if expire {
			token["access_token"], token["expiry"] = "probe-expired-access", "2000-01-01T00:00:00Z"
		}
		if accessOnly {
			delete(token, "refresh_token")
		}
		raw, err := json.Marshal(doc)
		path := filepath.Join(dir, antigravityTokenFile)
		if err == nil {
			err = os.WriteFile(path, raw, 0600)
		}
		if err != nil {
			r.Err = err
			return r
		}
		childCtx, stop := context.WithTimeout(ctx, 75*time.Second)
		defer stop()
		var cmd *exec.Cmd
		if driver == "docker" {
			cmd = exec.CommandContext(childCtx, docker, "run", "--rm", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
				"--mount", "type=bind,src="+home+",dst=/home/probe", "-e", "HOME=/home/probe", "-e", "SSH_CONNECTION=127.0.0.1 1 127.0.0.1 2",
				"-e", "NO_COLOR=1", "--entrypoint", "/usr/local/bin/agy", image, "models")
		} else {
			cmd = exec.CommandContext(childCtx, binary, "models")
			cmd.Dir = home
			cmd.Env = append(terminalBaseEnvironment(home), "SSH_CONNECTION=127.0.0.1 1 127.0.0.1 2", "NO_COLOR=1")
		}
		output, runErr := cmd.Output() // Never echo CLI diagnostics: they may contain account details.
		r.Raw, err = os.ReadFile(path)
		if err == nil {
			r.Record, err = decode(r.Raw)
		}
		if runErr != nil || err != nil {
			var exit *exec.ExitError
			if errors.As(runErr, &exit) {
				message := strings.ToLower(string(exit.Stderr))
				r.Err = fmt.Errorf("model-list failed: exit=%d, auth=%v, permission=%v, network=%v", exit.ExitCode(), strings.Contains(message, "auth"), strings.Contains(message, "permission"), strings.Contains(message, "connect") || strings.Contains(message, "network"))
			} else {
				r.Err = errors.New("official model-list command or credential read failed")
			}
			return r
		}
		for _, line := range strings.Split(string(output), "\n") {
			if id, _, found := strings.Cut(strings.TrimSpace(line), "\t"); found && id != "" {
				r.Models++
			}
		}
		if r.Models == 0 || r.Record.Token.Access == "probe-expired-access" || !r.Record.Token.Expiry.After(time.Now().Add(5*time.Minute)) {
			r.Err = errors.New("official CLI did not provide a usable refreshed login and model list")
		}
		return r
	}
	first := run("sequential-preflight", "native", []byte(original), true, false)
	if len(first.Raw) > 0 && first.Record.Token.Access != "" && first.Record.Token.Access != "probe-expired-access" {
		if !preserve(first.Raw) { // Preserve a rotated pair even if the later model-list request failed.
			t.Fatal("could not preserve preflight response")
		}
	}
	if first.Err != nil {
		t.Fatal(first.Err)
	}
	rotated := first.Record.Token.Refresh != initial.Token.Refresh
	t.Logf("live Antigravity preflight: models=%d, access renewed=true, refresh token rotated=%v", first.Models, rotated)
	if rotated {
		t.Log("stopped before concurrent reuse because the real provider rotated the refresh token")
		return
	}
	seed := append([]byte(nil), first.Raw...)
	outcomes := make(chan result, 3)
	start := make(chan struct{})
	for _, entry := range []struct{ name, driver string }{{"host-copy", "native"}, {"sop-native", "native"}, {"sop-docker", "docker"}} {
		go func(name, driver string) {
			<-start
			outcomes <- run(name, driver, seed, true, false)
		}(entry.name, entry.driver)
	}
	close(start)
	var last []byte
	var failures int
	for range 3 {
		r := <-outcomes
		if r.Record.Token.Access != "" && r.Record.Token.Access != "probe-expired-access" {
			if !preserve(r.Raw) {
				failures++
			}
			last = r.Raw
		}
		if r.Err != nil {
			failures++
			t.Logf("sanitized %s failure: %v", r.Name, r.Err)
		}
		changed := r.Record.Token.Refresh != first.Record.Token.Refresh
		t.Logf("live Antigravity concurrent %s: passed=%v, models=%d, refresh token rotated=%v", r.Name, r.Err == nil, r.Models, changed)
		if changed && r.Record.Token.Refresh != "" {
			keepRecovery = true
			failures++
		}
	}
	if failures != 0 || len(last) == 0 {
		t.Fatal("concurrent refresh was not uniformly reusable; inspect the sanitized results and retained private responses")
	}
	// Explicitly reuse the same refresh token after all three requests complete.
	reuse := run("reuse-after-concurrent", "native", last, true, false)
	if reuse.Record.Token.Access != "" && reuse.Record.Token.Access != "probe-expired-access" {
		if !preserve(reuse.Raw) {
			t.Fatal("could not preserve post-concurrency response")
		}
	}
	if reuse.Err != nil {
		t.Fatal("same refresh token was not reusable after the concurrent requests")
	}
	t.Logf("live Antigravity post-concurrency reuse: passed=true, refresh token rotated=%v", reuse.Record.Token.Refresh != first.Record.Token.Refresh)
	for _, driver := range []string{"native", "docker"} {
		r := run("access-only-"+driver, driver, reuse.Raw, false, true)
		t.Logf("live Antigravity access-only %s: passed=%v, models=%d, refresh token absent=%v", driver, r.Err == nil, r.Models, r.Record.Token.Refresh == "")
		if r.Err != nil {
			t.Error("access-only model-list invocation failed")
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+reuse.Record.Token.Access)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("could not verify final access token with Google userinfo")
	}
	defer resp.Body.Close()
	var profile struct {
		Subject string `json:"sub"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&profile) != nil || profile.Subject == "" {
		t.Fatal("final Google identity lookup failed")
	}
	t.Log("live Antigravity final access token accepted by Google userinfo; no account identifiers logged")
}
