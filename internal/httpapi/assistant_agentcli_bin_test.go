package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
)

func TestResolveUnmanagedCLIPreservesPathAndInstallerFallback(t *testing.T) {
	root, host, home := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", host)
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", root)
	t.Setenv("AGENTFLOW_DSH_BIN", "")
	t.Setenv("AGENTFLOW_CURSOR_BIN", "")
	hostDSH := filepath.Join(host, "dsh")
	if err := os.WriteFile(hostDSH, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(root, "providers", "dsh", "0.2.2-rc.1", "node_modules", ".bin", "dsh")
	if err := os.MkdirAll(filepath.Dir(managed), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(managedcli.State{Current: managedcli.Inventory{"dsh": {Version: "0.2.2-rc.1", Binary: managed}}})
	if err := os.WriteFile(filepath.Join(root, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveAssistantEngineBin("dsh"); err != nil || got != hostDSH {
		t.Fatalf("unmanaged dsh did not use PATH: %q %v", got, err)
	}
	// Cursor has no npm package: the managed root never hides ~/.local/bin.
	found, err := resolveAssistantEngineBin("cursor")
	// The integration image also installs Cursor globally. PATH and HOME
	// isolation deliberately do not disable the supported global fallback.
	global := found == "/usr/local/bin/cursor-agent" || found == "/usr/bin/cursor-agent"
	if !global && (!errors.Is(err, errAssistantCLIMissing) || !strings.Contains(err.Error(), "Cursor CLI")) {
		t.Fatalf("missing cursor-agent error: %v", err)
	}
	local := filepath.Join(home, ".local", "bin", "cursor-agent")
	if err := os.MkdirAll(filepath.Dir(local), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveAssistantEngineBin("cursor"); err != nil || got != local {
		t.Fatalf("installer location not found: %q %v", got, err)
	}
}

func TestParseCursorModels(t *testing.T) {
	output := "Available models\n\nauto - Auto (default)\ncomposer-2.5 - Composer 2.5\ngrok-4.7-low-fast - Grok 4.7  Low Fast\u200b\u200b\nTip: use --model\n"
	want := []domain.CLIModel{{ID: "auto", Name: "Auto"}, {ID: "composer-2.5", Name: "Composer 2.5"}, {ID: "grok-4.7-low-fast", Name: "Grok 4.7 Low Fast"}}
	if got := parseCursorModels(output); !reflect.DeepEqual(got, want) {
		t.Fatalf("models=%+v", got)
	}
}

func TestCursorModelFamilies(t *testing.T) {
	got := cursorModelFamilies([]domain.CLIModel{
		{ID: "grok-4.7-low-fast", Name: "Grok 4.7 Low Fast"}, {ID: "brand-new", Name: "Brand New"},
		{ID: "auto", Name: "Auto"}, {ID: "grok-4.7-high-fast"}, {ID: "kimi-k3-max"}, {ID: "brand-new"},
	})
	want := []domain.CLIModel{{ID: "auto", Name: "Auto"}, {ID: "grok-4.7-fast", Name: "Grok 4.7 Fast"}, {ID: "kimi-k3", Name: "Kimi K3"}, {ID: "brand-new", Name: "Brand New"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("families=%+v", got)
	}
	if efforts := assistantModelEfforts("cursor:gpt-5.6-sol"); !slices.Equal(efforts, []string{"none", "low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("cursor family efforts: %v", efforts)
	}
	if err := validateAssistantEffort("cursor:kimi-k3", "medium"); err == nil {
		t.Fatal("chat must offer only the family's own efforts")
	}
}

func TestCachedCursorModelsRefreshInBackground(t *testing.T) {
	calls := make(chan string, 4)
	previous := assistantCursorModels
	assistantCursorModels = func(_ context.Context, key string) ([]domain.CLIModel, error) {
		calls <- key
		return []domain.CLIModel{{ID: "claude-opus-5-5-high", Name: "Claude Opus 5.5 1M High"}, {ID: "bad id", Name: "x"}}, nil
	}
	t.Cleanup(func() { assistantCursorModels = previous })
	key := "crsr_test_" + t.Name()
	if got := cachedCursorModels(key); got != nil {
		t.Fatalf("cold cache must answer at once with the builtin catalog: %+v", got)
	}
	if got := <-calls; got != key {
		t.Fatalf("discovery used key %q", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := cachedCursorModels(key)
		if len(got) == 1 && got[0].ID == "claude-opus-5-5-high" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background discovery never filled the cache: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(calls) != 0 {
		t.Fatal("fresh cache re-ran cursor-agent")
	}
}
