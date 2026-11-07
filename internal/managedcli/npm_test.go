package managedcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNPMConfigurationUsesSeparateEmptyFiles(t *testing.T) {
	f := newUpdaterFixture(t)
	if err := f.manager.prepareTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	user := filepath.Join(f.manager.Root, "updater-home", "npmrc-user")
	global := filepath.Join(f.manager.Root, "updater-home", "npmrc-global")
	if user == global {
		t.Fatal("npm config source reused")
	}
	for _, path := range []string{user, global} {
		data, err := os.ReadFile(path)
		if err != nil || !strings.HasPrefix(string(data), "#") {
			t.Fatalf("invalid empty npm config %s: %v", path, err)
		}
	}
}

func TestDefaultToolDiscoveryAndNodePath(t *testing.T) {
	nodeDir, npmDir := t.TempDir(), t.TempDir()
	node, npm := filepath.Join(nodeDir, "node"), filepath.Join(npmDir, "npm")
	writeTestFile(t, node, "not executed: injected command runner")
	writeTestFile(t, npm, "not executed: injected command runner")
	path := npmDir + string(os.PathListSeparator) + nodeDir
	t.Setenv("PATH", path)
	m, err := NewManager(t.TempDir())
	must(t, err)
	calls := 0
	m.RunCommand = func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		calls++
		if name != node || !reflect.DeepEqual(args, []string{"--version"}) || dir != m.Root {
			t.Fatalf("unexpected Node discovery probe: %s %v %s", name, args, dir)
		}
		wantPath := "PATH=" + nodeDir + string(os.PathListSeparator) + path
		if !strings.Contains("\n"+strings.Join(env, "\n")+"\n", "\n"+wantPath+"\n") {
			t.Fatal("resolved Node directory was not first in child PATH")
		}
		deadline, ok := ctx.Deadline()
		if remaining := time.Until(deadline); !ok || remaining > 10*time.Minute || remaining < 9*time.Minute {
			t.Fatal("external command did not use fixed ten-minute deadline")
		}
		return []byte("v22.0.0\n"), nil
	}
	must(t, m.prepareTools(context.Background()))
	if calls != 1 || m.nodeBinary != node || m.npmBinary != npm {
		t.Fatalf("PATH discovery: node=%s npm=%s calls=%d", m.nodeBinary, m.npmBinary, calls)
	}
}

func TestNodeMinimumVersionIsFixed(t *testing.T) {
	for _, version := range []string{"v21.9.0", "v22.0.0", "v24.1.0", "v22.0.0-beta.1", "unknown"} {
		t.Run(version, func(t *testing.T) {
			f := newUpdaterFixture(t)
			f.manager.RunCommand = func(context.Context, string, []string, string, []string) ([]byte, error) {
				return []byte(version), nil
			}
			err := f.manager.prepareTools(context.Background())
			wantOK := version == "v22.0.0" || version == "v24.1.0"
			if (err == nil) != wantOK {
				t.Fatalf("Node version %s: %v", version, err)
			}
		})
	}
}

// This exercises npm's real configuration loader if a compatible toolchain is
// present. It only reads a registry setting: no packages, network or global
// configuration are modified.
func TestRealNPMConfigurationWithoutInstall(t *testing.T) {
	_, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm not installed")
	}
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.prepareTools(ctx); err != nil {
		t.Skipf("compatible Node/npm toolchain unavailable: %v", err)
	}
	out, err := m.command(ctx, npm, []string{"config", "get", "registry", "--registry", "https://registry.npmjs.org"}, m.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "https://registry.npmjs.org") {
		t.Fatalf("unexpected npm config: %s", out)
	}
}
