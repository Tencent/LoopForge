package httpapi

import (
	"agentflow/internal/managedcli"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCodeBuddyManagedInventory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", root)
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", "")
	// A working host CLI must not hide a missing/corrupt managed installation.
	host := t.TempDir()
	legacy := filepath.Join(host, "codebuddy")
	if err := os.WriteFile(legacy, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", host)
	if _, err := resolveCodeBuddyBin(); err == nil || !strings.Contains(err.Error(), "尚未安装") {
		t.Fatal("missing inventory fell back to host", err)
	}
	writeCurrent := func(version string) string {
		t.Helper()
		binary := filepath.Join(root, "providers", "codebuddy", version, "codebuddy")
		if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		state := managedcli.State{Current: managedcli.Inventory{"codebuddy": {Version: version, Binary: binary}}}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "state.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		return binary
	}
	first := writeCurrent("2.164.0")
	if got, err := resolveCodeBuddyBin(); err != nil || got != first {
		t.Fatal(got, err)
	}
	second := writeCurrent("2.165.0")
	if got, err := resolveCodeBuddyBin(); err != nil || got != second {
		t.Fatal("next call did not pick new version", got, err)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatal("previous invocation path removed", err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCodeBuddyBin(); err == nil || !strings.Contains(err.Error(), "清单失败") {
		t.Fatal("corrupt manifest fell back", err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", legacy)
	if got, err := resolveCodeBuddyBin(); err != nil || got != legacy {
		t.Fatal("explicit override ignored", got, err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", "")
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	if got, err := resolveCodeBuddyBin(); err != nil || got != legacy {
		t.Fatal("unmanaged fallback broken", got, err)
	}
}
