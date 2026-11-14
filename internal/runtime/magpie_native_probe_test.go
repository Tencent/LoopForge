package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMagpieNativeRaw(t *testing.T) {
	if os.Getenv("LOOPFORGE_MAGPIE_REAL_TEST") != "1" {
		t.Skip("opt-in")
	}
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".agentflow", "config", "probe")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	gate := ".agentflow/gate-results/n/attempt-1.json"
	m, err := materializeGateMCP(workspace, root, ".agentflow/config/probe/gate-mcp", gate, "local")
	if err != nil {
		t.Fatal(err)
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "--sandbox", "workspace-write", "--model", os.Getenv("LOOPFORGE_MAGPIE_MODEL"), "-c", `model_provider="magpie"`, "-c", `model_providers.magpie.name="Magpie"`, "-c", `model_providers.magpie.base_url="` + os.Getenv("LOOPFORGE_MAGPIE_BASE_URL") + `"`, "-c", `model_providers.magpie.env_key="PROBE_GATEWAY_KEY"`, "-c", `model_providers.magpie.wire_api="responses"`, "-c", `mcp_servers.agentflow-gate.tools.submit_gate_result.approval_mode="approve"`}
	args = append(args, codexMCPServerArgs(m)...)
	args = append(args, "Calculate 2+2 and call the submit_gate_result MCP tool with decision pass and an accurate reason. Do not call shell or other tools.")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(), "CODEX_HOME="+filepath.Join(workspace, "codex"), "PROBE_GATEWAY_KEY=local-gateway")
	if err := os.MkdirAll(filepath.Join(workspace, "codex"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("codex: %v: %s", err, exit.Stderr)
		}
		t.Fatalf("codex: %v", err)
	}
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		item, _ := ev["item"].(map[string]any)
		if item["type"] == "mcp_tool_call" {
			t.Logf("native event: %s", line)
			found = true
		}
	}
	if !found {
		t.Fatal("no mcp_tool_call")
	}
	if _, err := os.Stat(filepath.Join(workspace, gate)); err != nil {
		t.Fatal(err)
	}
}
