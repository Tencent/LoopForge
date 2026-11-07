package managedcli

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestConcurrentPinsAndCorruption(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inv, err := PinInventory(root, "same/run")
			if err != nil || len(inv) != 0 {
				t.Errorf("pin=%v err=%v", inv, err)
			}
		}()
	}
	wg.Wait()
	files, err := os.ReadDir(filepath.Join(root, "pins"))
	must(t, err)
	if len(files) != 1 {
		t.Fatalf("concurrent pins=%v", files)
	}
	for _, data := range []string{"broken", "null"} {
		writeTestFile(t, filepath.Join(root, "pins", files[0].Name()), data)
		if _, err := PinInventory(root, "same/run"); err == nil {
			t.Fatal("corrupt pin silently reset")
		}
	}
	m, err := NewManager(root)
	must(t, err)
	m.Now = func() time.Time { return time.Date(2026, 1, 1, 8, 0, 0, 0, beijing) }
	for _, data := range []string{"null", `{"lastCheck":"bad"}`, `{} {}`} {
		writeTestFile(t, filepath.Join(root, "state.json"), data)
		if _, err := m.Check(context.Background()); err == nil {
			t.Fatal("corrupt state permitted a new daily claim")
		}
	}
}

func TestRejectUnsafeRootsAndManifestPaths(t *testing.T) {
	link := filepath.Join(t.TempDir(), "root-link")
	must(t, os.Symlink(t.TempDir(), link))
	for _, root := range []string{"", "/", "relative", "/tmp/../tmp/cli", link} {
		if _, err := ReadInventory(root); err == nil {
			t.Fatalf("accepted root %q", root)
		}
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "codex")
	writeTestFile(t, outside, "#!/bin/sh")
	must(t, prepareDirectory(root, "providers/codex/1.0.0"))
	binary := filepath.Join(root, "providers/codex/1.0.0/codex")
	must(t, os.Symlink(outside, binary))
	for _, path := range []string{outside, binary} {
		if err := validateInventory(root, Inventory{"codex": {Version: "1.0.0", Binary: path}}); err == nil {
			t.Fatal("outside/escaping executable accepted")
		}
	}
	must(t, os.Symlink(outside, filepath.Join(root, "state.json")))
	if _, err := ReadState(root); err == nil {
		t.Fatal("symlink state accepted")
	}
	if err := atomicJSON(filepath.Join(root, "state.json"), State{}); err == nil {
		t.Fatal("symlink state overwritten")
	}
	for _, rel := range []string{"updater-home", "tmp", "npm-cache", "providers", "providers/codex", "pins"} {
		root, external := t.TempDir(), t.TempDir()
		path := filepath.Join(root, rel)
		must(t, os.MkdirAll(filepath.Dir(path), 0700))
		must(t, os.Symlink(external, path))
		if err := prepareDirectory(root, rel); err == nil {
			t.Fatalf("accepted symlink directory %s", rel)
		}
		entries, err := os.ReadDir(external)
		must(t, err)
		if len(entries) != 0 {
			t.Fatal("wrote outside managed root")
		}
	}
}
