package managedcli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type updaterFixture struct {
	manager      *Manager
	mu           sync.Mutex
	versions     map[string]string
	published    time.Time
	installs     map[string]int
	failHelp     string
	badIntegrity bool
	tamperLock   bool
	environments [][]string
	rebuildCalls int
}

func newUpdaterFixture(t *testing.T) *updaterFixture {
	t.Helper()
	f := &updaterFixture{versions: map[string]string{}, installs: map[string]int{}, published: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	for _, provider := range providerNames() {
		f.versions[provider] = "1.0.0"
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for provider, spec := range providerSpecs {
			if r.URL.Path != "/"+spec.Package {
				continue
			}
			version := f.versions[provider]
			entry := release{Name: spec.Package, Version: version}
			entry.Dist.Integrity = testIntegrity()
			published, _ := json.Marshal(f.published)
			_ = json.NewEncoder(w).Encode(packageMetadata{Name: spec.Package, Tags: map[string]string{"latest": version}, Versions: map[string]release{version: entry}, Times: map[string]json.RawMessage{version: published}})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.Client = fixtureClient(t, server)
	m.nodeBinary, m.npmBinary = "/fake/node", "/fake/npm"
	m.Now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	m.RunCommand = f.run
	f.manager = m
	return f
}

func testIntegrity() string {
	return "sha512-" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 64))
}

func (f *updaterFixture) run(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.environments = append(f.environments, append([]string(nil), env...))
	if name == "/fake/node" {
		return []byte("v22.16.0\n"), nil
	}
	if name == "/fake/npm" {
		if len(args) > 0 && args[0] == "rebuild" {
			if !reflect.DeepEqual(args, []string{"rebuild", "--prefix", dir, "--registry", "https://registry.npmjs.org", "--no-audit", "--no-fund"}) {
				return nil, errors.New("rebuild bypassed fixed registry or safety arguments")
			}
			if f.badIntegrity {
				return nil, errors.New("lifecycle hooks ran before integrity verification")
			}
			f.rebuildCalls++
			if f.tamperLock {
				return nil, os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(`{}`), 0600)
			}
			return nil, nil
		}
		if !strings.Contains(strings.Join(args, " "), "--ignore-scripts") {
			return nil, errors.New("initial package download must not run lifecycle hooks")
		}
		if strings.Contains(strings.Join(args, " "), " -g ") {
			return nil, errors.New("global installation forbidden")
		}
		if !strings.Contains(strings.Join(args, " "), "--engine-strict") {
			return nil, errors.New("missing engine check")
		}
		specArg := args[len(args)-1]
		for provider, spec := range providerSpecs {
			prefix := spec.Package + "@"
			if !strings.HasPrefix(specArg, prefix) {
				continue
			}
			version := strings.TrimPrefix(specArg, prefix)
			want := []string{"install", "--prefix", dir, "--registry", "https://registry.npmjs.org", "--no-audit", "--no-fund", "--engine-strict", "--ignore-scripts", "--include=optional", "--save-exact", spec.Package + "@" + version}
			if !reflect.DeepEqual(args, want) || !releaseVersion.MatchString(version) {
				return nil, errors.New("install bypassed exact stable version, fixed registry or safety arguments")
			}
			f.installs[provider]++
			binDir := filepath.Join(dir, "node_modules", ".bin")
			if err := os.MkdirAll(binDir, 0700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(binDir, spec.Binary), []byte("#!/bin/sh\n"+version+"\n"), 0700); err != nil {
				return nil, err
			}
			integrity := testIntegrity()
			if f.badIntegrity {
				integrity = "sha512-wrong"
			}
			lock := map[string]any{"packages": map[string]any{"node_modules/" + spec.Package: map[string]string{"version": version, "integrity": integrity}}}
			data, _ := json.Marshal(lock)
			return nil, os.WriteFile(filepath.Join(dir, "package-lock.json"), data, 0600)
		}
		return nil, errors.New("unknown package")
	}
	for provider, spec := range providerSpecs {
		if !strings.Contains(name, string(filepath.Separator)+provider+string(filepath.Separator)) {
			continue
		}
		if len(args) == 1 && args[0] == "--version" {
			data, err := os.ReadFile(name)
			if err != nil {
				return nil, err
			}
			return []byte(spec.Binary + " " + strings.TrimSpace(strings.SplitN(string(data), "\n", 3)[1])), nil
		}
		if provider == f.failHelp {
			return []byte("--missing-every-required-flag"), nil
		}
		return []byte(strings.Join(spec.Flags, " ")), nil
	}
	return nil, errors.New("unexpected command " + name)
}

func (f *updaterFixture) setVersion(version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for provider := range f.versions {
		f.versions[provider] = version
	}
}

func (f *updaterFixture) state(t *testing.T) State {
	t.Helper()
	state, err := ReadState(f.manager.Root)
	must(t, err)
	return state
}

func (f *updaterFixture) update(t *testing.T, wantError bool) Report {
	t.Helper()
	report, err := f.manager.Update(context.Background())
	if (err != nil) != wantError {
		t.Fatalf("update err=%v report=%+v", err, report)
	}
	return report
}

func (f *updaterFixture) pin(t *testing.T, key string) Inventory {
	t.Helper()
	inventory, err := PinInventory(f.manager.Root, key)
	must(t, err)
	return inventory
}

func TestUpdatePinsRollbackAndIdempotence(t *testing.T) {
	f := newUpdaterFixture(t)
	f.pin(t, "empty")
	for _, result := range f.update(t, false).Results {
		if result.Status != "updated" || result.Validation != "version-and-required-arguments" {
			t.Fatalf("result=%+v", result)
		}
	}
	first := f.pin(t, "run1")
	for _, result := range f.update(t, false).Results {
		if result.Status != "unchanged" || f.installs[result.Provider] != 1 {
			t.Fatalf("reinstalled: %+v", result)
		}
	}
	f.setVersion("2.0.0")
	f.update(t, false)
	second := f.pin(t, "run2")
	_, err := f.manager.Rollback(context.Background(), "codex")
	must(t, err)
	state := f.state(t)
	if state.Current["codex"] != first["codex"] || state.Rejected["codex"] != "2.0.0" {
		t.Fatalf("rollback=%+v", state)
	}
	for key, want := range map[string]Inventory{"run1": first, "run2": second, "empty": {}} {
		if !reflect.DeepEqual(f.pin(t, key), want) {
			t.Fatalf("pin %s moved", key)
		}
		for _, installation := range want {
			_, err := os.Stat(installation.Binary)
			must(t, err) // No GC of old versions/pins, including the rejected version.
		}
	}
	f.update(t, false)
	if f.state(t).Current["codex"].Version != "1.0.0" {
		t.Fatal("automatic update retried rejected release")
	}
	f.setVersion("3.0.0")
	f.update(t, false)
	if f.state(t).Current["codex"].Version != "3.0.0" {
		t.Fatal("new release did not advance beyond rejected release")
	}
	_, err = os.Stat(filepath.Join(f.manager.Root, "last-report.json"))
	must(t, err)
}

func TestUpdateFailuresAndCandidateReuse(t *testing.T) {
	for _, mode := range []string{"help", "integrity-before-hook", "integrity-after-hook", "offline", "maintenance"} {
		t.Run(mode, func(t *testing.T) {
			f := newUpdaterFixture(t)
			f.update(t, false)
			f.setVersion("2.0.0")
			var lease *ClaimLease
			switch mode {
			case "help":
				f.failHelp = "codex"
			case "integrity-before-hook":
				f.badIntegrity = true
			case "integrity-after-hook":
				f.tamperLock = true
			case "offline":
				f.manager.Client = &http.Client{Transport: failingTransport{}}
			case "maintenance":
				var err error
				lease, err = BeginClaim(context.Background(), f.manager.Root)
				must(t, err)
				defer lease.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if _, err := f.manager.Update(ctx); err == nil {
				t.Fatal("expected update failure")
			}
			state := f.state(t)
			if state.Current["codex"].Version != "1.0.0" {
				t.Fatal("failed candidate activated")
			}
			if mode == "help" {
				if state.Current["codebuddy"].Version != "2.0.0" {
					t.Fatal("one provider blocked healthy updates")
				}
				f.update(t, true) // repeated failed smoke test reuses its cache too
				f.failHelp = ""
			}
			if mode == "maintenance" || mode == "help" {
				must(t, lease.Close())
				f.update(t, false)
				if f.installs["codex"] != 2 || f.state(t).Candidates["codex"].Version != "2.0.0" {
					t.Fatal("complete candidate was downloaded again")
				}
			}
			if mode == "integrity-before-hook" && f.rebuildCalls != 3 {
				t.Fatal("hooks executed before integrity check")
			}
		})
	}
}

func TestActivationCancellationBeforePublication(t *testing.T) {
	f := newUpdaterFixture(t)
	f.update(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel after inventory validation, not merely while waiting for locks.
	f.manager.Now = func() time.Time { cancel(); return time.Now() }
	err := WithUpdateLock(ctx, f.manager.Root, func() error {
		return f.manager.activate(ctx, State{Current: Inventory{}})
	})
	if !errors.Is(err, context.Canceled) || len(f.state(t).Current) != 3 {
		t.Fatalf("canceled activation published state: %v", err)
	}
}

func TestObservationAndDowngrade(t *testing.T) {
	f := newUpdaterFixture(t)
	f.setVersion("2.0.0")
	f.update(t, false)
	for _, tc := range []struct {
		version   string
		age       time.Duration
		wantError bool
	}{
		{"3.0.0", time.Hour, false},
		{"3.0.0-beta.1", 48 * time.Hour, true},
		{"1.0.0", 48 * time.Hour, false},
	} {
		f.setVersion(tc.version)
		f.mu.Lock()
		f.published = f.manager.Now().Add(-tc.age)
		f.mu.Unlock()
		f.update(t, tc.wantError)
		want := "2.0.0"
		if f.state(t).Current["codex"].Version != want {
			t.Fatalf("observation/downgrade safety ignored: %+v", tc)
		}
	}
}

func TestMissingRollback(t *testing.T) {
	f := newUpdaterFixture(t)
	for _, provider := range []string{"codex", "shell"} {
		if _, err := f.manager.Rollback(context.Background(), provider); err == nil {
			t.Fatal("missing/unknown rollback accepted")
		}
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network unavailable")
}

func TestUpdaterEnvironment(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEBUDDY_API_KEY", "AGENTFLOW_WORKER_CONTROL_TOKEN", "AGENTFLOW_PLATFORM_MCP_TOKEN", "AGENTFLOW_API_KEY", "AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", "NODE_OPTIONS", "NPM_TOKEN", "NPM_CONFIG_REGISTRY", "npm_config_registry", "NPM_CONFIG_USERCONFIG", "NPM_CONFIG_GLOBALCONFIG", "AGENTFLOW_MANAGED_CLI_CONFIG"} {
		t.Setenv(key, "not-for-updater")
	}
	f := newUpdaterFixture(t)
	f.update(t, false)
	for _, env := range f.environments {
		joined := strings.Join(env, "\n")
		if strings.Contains(joined, "not-for-updater") {
			t.Fatal("task/host environment leaked")
		}
		for _, required := range []string{"HOME=" + filepath.Join(f.manager.Root, "updater-home"), "DISABLE_AUTOUPDATER=1", "DISABLE_UPDATES=1"} {
			if !strings.Contains(joined, required) {
				t.Fatalf("missing isolated environment %s", required)
			}
		}
	}
}

// Only the transport redirects requests: production always constructs the
// official registry URL and has no configurable endpoint or env override.
func fixtureClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	endpoint, err := url.Parse(server.URL)
	must(t, err)
	client := server.Client()
	transport := client.Transport
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "registry.npmjs.org" {
			return nil, fmt.Errorf("unexpected production registry URL: %s", req.URL)
		}
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host = endpoint.Scheme, endpoint.Host
		return transport.RoundTrip(local)
	})
	return client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
