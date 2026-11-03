package managedcli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmptyNPMConfigAndPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")
	env := (&Manager{Root: dir}).commandEnvironment()
	if !strings.Contains(strings.Join(env, "\n"), "PATH=/usr/local/bin:/usr/bin:/bin") {
		t.Fatalf("fallback path env = %#v", env)
	}
	path := filepath.Join(dir, "npmrc")
	if err := emptyNPMConfig(path); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || len(raw) == 0 {
		t.Fatalf("config = %q, %v", raw, err)
	}
	if err := emptyNPMConfig(path); err != nil {
		t.Fatalf("replace regular config: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := emptyNPMConfig(path); err == nil {
		t.Fatal("directory config accepted")
	}
	for _, root := range []string{"relative", "/", dir + "/x/.."} {
		if validRoot(root) == nil {
			t.Fatalf("root %q accepted", root)
		}
	}
	root := filepath.Join(dir, "root")
	if err := prepareDirectory(root, filepath.Join("a", "b")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"/abs", "..", "../x"} {
		if prepareDirectory(root, rel) == nil {
			t.Fatalf("rel %q accepted", rel)
		}
	}
	if within(root, "relative") || within(root, root) || !within(root, filepath.Join(root, "a")) {
		t.Fatal("within")
	}
	symlinkRoot := filepath.Join(dir, "root-link")
	if err := os.Symlink(root, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if validRoot(symlinkRoot) == nil {
		t.Fatal("symlink root accepted")
	}
	if err := os.Symlink(dir, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if prepareDirectory(root, "escape/child") == nil {
		t.Fatal("symlink subdirectory accepted")
	}
}

func TestRegistryCandidateRejections(t *testing.T) {
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	cvSMetadata := func(entry release, published json.RawMessage) packageMetadata {
		return packageMetadata{
			Name: providerSpecs["codex"].Package, Tags: map[string]string{"latest": "1.2.3"},
			Versions: map[string]release{"1.2.3": entry}, Times: map[string]json.RawMessage{"1.2.3": published},
		}
	}
	entry := release{Name: providerSpecs["codex"].Package, Version: "1.2.3"}
	entry.Dist.Integrity = testIntegrity()
	published, _ := json.Marshal(now.Add(-48 * time.Hour))
	deprecated := entry
	deprecated.Deprecated = "old"
	badIntegrity := entry
	badIntegrity.Dist.Integrity = "sha512-bad"
	tests := []struct {
		name   string
		status int
		body   any
	}{
		{"http", http.StatusBadGateway, map[string]any{}},
		{"malformed", http.StatusOK, json.RawMessage(`{`)},
		{"wrong package", http.StatusOK, packageMetadata{Name: "wrong", Tags: map[string]string{"latest": "1.2.3"}}},
		{"missing release", http.StatusOK, packageMetadata{Name: providerSpecs["codex"].Package, Tags: map[string]string{"latest": "1.2.3"}, Versions: map[string]release{}, Times: map[string]json.RawMessage{}}},
		{"deprecated", http.StatusOK, cvSMetadata(deprecated, published)},
		{"integrity", http.StatusOK, cvSMetadata(badIntegrity, published)},
		{"publication", http.StatusOK, cvSMetadata(entry, json.RawMessage(`null`))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				if raw, ok := tc.body.(json.RawMessage); ok {
					_, _ = w.Write(raw)
				} else {
					_ = json.NewEncoder(w).Encode(tc.body)
				}
			}))
			defer server.Close()
			m, err := NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			m.Now = func() time.Time { return now }
			m.Client = fixtureClient(t, server)
			if _, err := m.candidate(context.Background(), "codex"); err == nil {
				t.Fatal("bad registry response accepted")
			}
		})
	}
	if _, err := NewManager("relative"); err == nil {
		t.Fatal("relative manager root accepted")
	}
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	insecure, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
	if err := m.Client.CheckRedirect(insecure, nil); err == nil {
		t.Fatal("insecure redirect accepted")
	}
	secure, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if err := m.Client.CheckRedirect(secure, []*http.Request{{}, {}, {}, {}, {}}); err == nil {
		t.Fatal("excessive redirects accepted")
	}
	if err := m.Client.CheckRedirect(secure, nil); err != nil {
		t.Fatalf("secure redirect rejected: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stop := Start(ctx, t.TempDir())
	stop()
}

func TestManagedBinaryValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	base := filepath.Join(root, "providers", "codex", "1.2.3")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(base, "codex")
	flags := strings.Join(providerSpecs["codex"].Flags, " ")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex 1.2.3'; else echo '" + flags + "'; fi\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	installed := Installation{Version: "1.2.3", Binary: binary}
	if err := m.validate(context.Background(), "codex", installed); err != nil {
		t.Fatal(err)
	}
	installed.Version = "1.2.4"
	if err := m.validate(context.Background(), "codex", installed); err == nil {
		t.Fatal("wrong binary version accepted")
	}
	installed.Version = "1.2.3"
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.validate(context.Background(), "codex", installed); err == nil {
		t.Fatal("failed version probe accepted")
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 1.2.3; fi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.validate(context.Background(), "codex", installed); err == nil {
		t.Fatal("missing help flags accepted")
	}
}

func TestInventoryAndPackageLockValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	base := filepath.Join(root, "providers", "codex", "1.2.3")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(base, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	good := Installation{Version: "1.2.3", Binary: binary}
	if err := validateInventory(root, Inventory{"codex": good}); err != nil {
		t.Fatal(err)
	}
	bad := []Inventory{
		nil,
		{"unknown": good},
		{"codex": {Version: "latest", Binary: binary}},
		{"codex": {Version: "1.2.3", Binary: filepath.Join(root, "escape")}},
		{"codex": {Version: "1.2.3", Binary: filepath.Join(base, "missing")}},
	}
	for i, inventory := range bad {
		if validateInventory(root, inventory) == nil {
			t.Fatalf("bad inventory %d accepted", i)
		}
	}
	escape := filepath.Join(t.TempDir(), "escape")
	if err := os.WriteFile(escape, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(escape, link); err != nil {
		t.Fatal(err)
	}
	if validateInventory(root, Inventory{"codex": {Version: "1.2.3", Binary: link}}) == nil {
		t.Fatal("escaping executable symlink accepted")
	}

	release := release{Version: "1.2.3"}
	release.Dist.Integrity = "sha512-" + strings.Repeat("A", 88)
	lockDir := t.TempDir()
	if verifyPackageLock(lockDir, "@openai/codex", release) == nil {
		t.Fatal("missing lock accepted")
	}
	lock := `{"packages":{"node_modules/@openai/codex":{"version":"1.2.3","integrity":"` + release.Dist.Integrity + `"}}}`
	if err := os.WriteFile(filepath.Join(lockDir, "package-lock.json"), []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyPackageLock(lockDir, "@openai/codex", release); err != nil {
		t.Fatal(err)
	}
	if err := verifyPackageLock(lockDir, "@openai/other", release); err == nil {
		t.Fatal("mismatched package accepted")
	}
	for _, pair := range [][2]string{{"1.2.3", "1.2.3"}, {"1.2.2", "1.2.3"}, {"2.0.0", "1.9.9"}, {"1.10.0", "1.2.0"}, {"1.2.0", "1.10.0"}} {
		_ = compareVersions(pair[0], pair[1])
	}
	for _, value := range []string{"", "sha256-AA==", "sha512-!", "sha512-" + strings.Repeat("A", 88)} {
		_ = validIntegrity(value)
	}
}

func TestConfigureCommandGroupCancellation(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "while :; do :; done")
	if err := configureCommandGroup(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("cancel before start = %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("cancel running = %v", err)
	}
	_ = cmd.Wait()
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("cancel finished = %v", err)
	}
}

func TestJSONHelpers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	value := map[string]string{"x": "y"}
	if err := atomicJSON(path, value); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := readJSON(path, &got); err != nil || got["x"] != "y" {
		t.Fatalf("read = %#v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("{}{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readJSON(path, &got); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(path, value); err == nil {
		t.Fatal("directory state replaced")
	}
	if _, err := writeTemporaryJSON(dir, make(chan int)); err == nil {
		t.Fatal("unmarshalable JSON accepted")
	}
	if _, err := writeTemporaryJSON(filepath.Join(dir, "missing"), value); err == nil {
		t.Fatal("missing temporary directory accepted")
	}
	if _, err := PinInventory(filepath.Join(dir, "root"), " "); err == nil {
		t.Fatal("empty pin key accepted")
	}
	large := filepath.Join(dir, "large.json")
	if err := os.WriteFile(large, make([]byte, (1<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readJSON(large, &got); err == nil {
		t.Fatal("oversized JSON accepted")
	}
	nullRoot := filepath.Join(dir, "null-root")
	if err := os.MkdirAll(nullRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nullRoot, "state.json"), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadState(nullRoot); err == nil {
		t.Fatal("null state accepted")
	}
	if inventory, err := ReadInventory(filepath.Join(dir, "new-root")); err != nil || len(inventory) != 0 {
		t.Fatalf("new inventory = %#v, %v", inventory, err)
	}
	if err := atomicJSON(filepath.Join(dir, "absent", "state.json"), value); err == nil {
		t.Fatal("atomic JSON in missing directory accepted")
	}
	if err := atomicJSON(filepath.Join(dir, "channel.json"), make(chan int)); err == nil {
		t.Fatal("atomic unmarshalable JSON accepted")
	}
}
