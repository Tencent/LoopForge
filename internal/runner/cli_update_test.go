package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"agentflow/internal/domain"
)

func TestNativeCLIUpdateRunsDiscoveredCommandAndRefreshesVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PATH", dir)
	t.Setenv("CURSOR_API_KEY", "not-for-the-updater")
	mode := filepath.Join(dir, "install-mode")
	version := filepath.Join(dir, "version")
	binary := filepath.Join(dir, "cursor-agent")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  --version) IFS= read -r v < %q; printf '%%s\n' "$v" ;;
  --help) printf 'Usage: agent\n\nCommands:\n  update [options]  Update Cursor\n' ;;
  update)
    test -z "${CURSOR_API_KEY:-}" || exit 2
    if test -f %q; then printf 'Use your package manager\n'; exit 1; fi
    printf '2.0.0\n' > %q ;;
  *) exit 3 ;;
esac
`, version, mode, version)
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(version, []byte("1.0.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d := &Daemon{cfg: Config{ExtraBinaries: map[string]string{"cursor": binary}}, heartbeatWake: make(chan struct{}, 1)}
	d.refreshRuntimes(ctx)
	rt, ok := d.cliUpdateRuntime("cursor")
	if !ok || !slices.Contains(rt.Capabilities, domain.RuntimeCapabilityNativeUpdate) || rt.Version != "1.0.0" {
		t.Fatalf("discovery: %+v", rt)
	}
	result := d.applyCLIUpdate(ctx, "cursor")
	if result.Status != "updated" || result.Version != "2.0.0" {
		t.Fatalf("native update: %+v", result)
	}
	if err := os.WriteFile(mode, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if result = d.applyCLIUpdate(ctx, "cursor"); result.Status != "manual" {
		t.Fatalf("unsupported installation: %+v", result)
	}
}
