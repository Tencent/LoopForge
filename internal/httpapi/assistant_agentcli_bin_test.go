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

func TestResolveManagedPiBeforePATHAndPreserveOverrides(t *testing.T) {
	root, host, home := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", host)
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", root)
	t.Setenv("AGENTFLOW_PI_BIN", "")
	t.Setenv("AGENTFLOW_CURSOR_BIN", "")
	hostPi := filepath.Join(host, "pi")
	if err := os.WriteFile(hostPi, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(root, "providers", "pi", "1.5.2", "node_modules", ".bin", "pi")
	if err := os.MkdirAll(filepath.Dir(managed), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(managedcli.State{Current: managedcli.Inventory{"pi": {Version: "1.5.2", Binary: managed}}})
	if err := os.WriteFile(filepath.Join(root, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveAssistantEngineBin("pi"); err != nil || got != managed {
		t.Fatalf("managed Pi did not supersede the host binary: %q %v", got, err)
	}
	t.Setenv("AGENTFLOW_PI_BIN", hostPi)
	if got, err := resolveAssistantEngineBin("pi"); err != nil || got != hostPi {
		t.Fatalf("explicit Pi override was ignored: %q %v", got, err)
	}
	// An empty Cursor inventory still falls back to its installer location.
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
	if efforts := assistantModelEfforts(context.Background(), domain.User{}, "cursor:gpt-5.6-sol"); !slices.Equal(efforts, []string{"none", "low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("cursor family efforts: %v", efforts)
	}
	if err := validateAssistantEffort(context.Background(), domain.User{}, "cursor:kimi-k3", "medium"); err == nil {
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
	if got, pending := cachedCursorModels(key); got != nil || !pending {
		t.Fatalf("cold cache must answer at once with discovery pending: %+v, pending=%v", got, pending)
	}
	if got := <-calls; got != key {
		t.Fatalf("discovery used key %q", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, pending := cachedCursorModels(key)
		if !pending && len(got) == 1 && got[0].ID == "claude-opus-5-5-high" {
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
