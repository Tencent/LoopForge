package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsDockerGateNeverUsesNativeExecutable(t *testing.T) {
	for _, name := range []string{"agentflow-gate-mcp.exe", "renamed-windows-helper"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			native := filepath.Join(directory, name)
			if err := os.WriteFile(native, []byte("MZ native Windows executable"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTFLOW_GATE_MCP_BINARY", native)
			if path, err := resolveGateMCPBinary("linux", "amd64"); err == nil {
				t.Fatalf("Windows executable selected for Linux: %s", path)
			}
			linux := filepath.Join(directory, "agentflow-gate-mcp-linux-amd64")
			if err := os.WriteFile(linux, []byte("\x7fELF Linux helper"), 0600); err != nil {
				t.Fatal(err)
			}
			if path, err := resolveGateMCPBinary("linux", "amd64"); err != nil || path != linux {
				t.Fatalf("Linux bundle helper not selected: %s: %v", path, err)
			}
			if path, err := resolveGateMCPBinary("windows", "amd64"); err != nil || path != native {
				t.Fatalf("native Windows helper changed: %s: %v", path, err)
			}
		})
	}
}
