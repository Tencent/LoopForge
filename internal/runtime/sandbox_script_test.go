package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestScriptViewsPreserveProcessSandboxBoundaries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("repo instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, driver := range []string{"bwrap", "seatbelt"} {
		sb := &processSandbox{workspace: root, driver: driver, binary: "sandbox-tool"}
		for _, provider := range []string{"script", "claude-code", "codex", "verification", ""} {
			cmd, err := sb.command(context.Background(), CommandSpec{Provider: provider, Argv: []string{"sh", "-c", "true"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Join(cmd.Args, " ")
			masked := strings.Contains(text, filepath.Join(root, "CLAUDE.md"))
			if masked != (provider != "script") {
				t.Fatalf("%s/%s mask=%v: %s", driver, provider, masked, text)
			}
			if driver == "bwrap" && !strings.Contains(text, "--ro-bind / /") {
				t.Fatal("script lost host filesystem boundary")
			}
			if driver == "seatbelt" && !strings.Contains(text, "(deny file-write*)") {
				t.Fatal("script lost host write boundary")
			}
		}
	}
	args := strings.Join(dockerScriptCreateArgs("helper", "canonical", "/work", "image", "generation"), " ")
	for _, part := range []string{"--cap-drop=ALL", "no-new-privileges", "--pid=container:canonical", "--network=container:canonical"} {
		if !strings.Contains(args, part) {
			t.Fatal(args)
		}
	}
	if strings.Contains(args, "--privileged") {
		t.Fatal("helper must not be a privileged container")
	}
}

func TestDockerScriptViewDependencyRemoval(t *testing.T) {
	if os.Getenv("AGENTFLOW_DOCKER_SCRIPT_SMOKE") != "1" {
		t.Skip("set AGENTFLOW_DOCKER_SCRIPT_SMOKE=1 for local Docker regression")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	image := "alpine:3.20"
	if out, err := exec.CommandContext(ctx, "docker", "image", "inspect", image).CombinedOutput(); err != nil {
		t.Fatalf("local fixture image: %s %v", out, err)
	}
	root := os.Getenv("AGENTFLOW_DOCKER_TEST_ROOT")
	if root == "" {
		root = "."
	}
	workspace, err := os.MkdirTemp(root, ".script-view-test-")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)
	path := filepath.Join(workspace, "vendor", "test-package")
	if err = os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "CLAUDE.md"), []byte("dependency instructions"), 0644); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("agentflow-script-view-qa-%d", time.Now().UnixNano())
	masks, err := dockerAgentConfigMasks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--pull=never", "-d", "--name", name, "--cap-drop=ALL", "--security-opt", "no-new-privileges", "-v", workspace + ":/workspace"}
	args = append(args, masks...)
	args = append(args, image, "sh", "-c", "sleep 300")
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %s %v", out, err)
	}
	sb := &dockerSandbox{id: name, name: name, workspace: workspace, started: true, profile: domain.SandboxProfile{Driver: "docker"}}
	defer sb.Close(context.Background())
	defer os.RemoveAll(dockerIsolationMaskRoot(workspace))
	// A live runtime install and a service, absent from the image, must survive.
	if out, err := sb.Exec(ctx, CommandSpec{Argv: []string{"sh", "-c", "printf installed > /usr/local/runtime-installed; printf '%s' secret > /tmp/runtime-marker"}}, nil); err != nil {
		t.Fatalf("runtime marker: %s %v", out, err)
	}
	fail, err := sb.Exec(ctx, CommandSpec{Provider: "claude-code", Argv: []string{"sh", "-c", "test ! -s vendor/test-package/CLAUDE.md && rm -rf vendor/test-package"}}, nil)
	if err == nil {
		t.Fatalf("fixture did not reproduce protected mount: %s", fail)
	}
	hostname, err := sb.Exec(ctx, CommandSpec{Argv: []string{"hostname"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	script := `test "$(cat /usr/local/runtime-installed)" = installed
 test "$(cat /tmp/runtime-marker)" = secret
 test "$(cat vendor/test-package/CLAUDE.md)" = 'dependency instructions'
 rm -rf vendor/test-package
 mkdir -p vendor/test-package
 printf replacement > vendor/test-package/CLAUDE.md
 printf script-installed > /usr/local/script-installed
 for field in CapEff CapPrm CapInh CapBnd CapAmb; do grep -q "^$field:[[:space:]]*0000000000000000$" /proc/self/status; done
 grep -q '^NoNewPrivs:[[:space:]]*1$' /proc/self/status
 sleep 120 >/dev/null 2>&1 &
 echo $! > /workspace/script-service-pid
 `
	script = "test \"$(hostname)\" = " + shellSingleQuote(strings.TrimSpace(hostname)) + "\n" + script
	if out, err := sb.Exec(ctx, CommandSpec{Provider: "script", Argv: []string{"sh", "-ec", script}}, nil); err != nil {
		t.Fatalf("unmasked script: %s %v", out, err)
	}
	if data, err := os.ReadFile(filepath.Join(path, "CLAUDE.md")); err != nil || string(data) != "replacement" {
		t.Fatal(string(data), err)
	}
	// The script namespace is discarded; refresh masks before the next Agent so
	// replaced dependency directories cannot expose new instructions.
	if out, err := sb.Exec(ctx, CommandSpec{Provider: "claude-code", Argv: []string{"sh", "-ec", `test ! -s vendor/test-package/CLAUDE.md || { echo mask-lost; cat vendor/test-package/CLAUDE.md; exit 1; }; test "$(cat /usr/local/script-installed)" = script-installed || { echo install-lost; exit 1; }; kill -0 "$(cat script-service-pid)" || { echo service-lost; cat script-service-pid; exit 1; }; grep -q '^Seccomp:[[:space:]]*2$' /proc/self/status`}}, nil); err != nil {
		t.Fatalf("agent isolation/runtime: %s %v", out, err)
	}
	before, err := SandboxEnvironmentIdentity(ctx, sb)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := sb.Exec(ctx, CommandSpec{Provider: "script", Argv: []string{"sh", "-ec", `test "$(cat vendor/test-package/CLAUDE.md)" = replacement; kill -0 "$(cat script-service-pid)"`}}, nil); err != nil {
		t.Fatalf("second script: %s %v", out, err)
	}
	after, err := SandboxEnvironmentIdentity(ctx, sb)
	if err != nil || before != after {
		t.Fatal("warm environment identity changed", before, after, err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "rm", "-f", dockerScriptContainerName(name)).CombinedOutput(); err != nil {
		t.Fatalf("recreate helper: %s %v", out, err)
	}
	next, err := SandboxEnvironmentIdentity(ctx, sb)
	if err != nil || next == after {
		t.Fatal("helper loss did not invalidate preparation", next, err)
	}
	// A stopped canonical container must invalidate the cached handle so retry
	// can restart it, rather than failing permanently on the old started flag.
	if out, err := exec.CommandContext(ctx, "docker", "stop", "-t", "1", name).CombinedOutput(); err != nil {
		t.Fatalf("stop canonical: %s %v", out, err)
	}
	if _, err := sb.Exec(ctx, CommandSpec{Provider: "script", Argv: []string{"sh", "-c", "true"}}, nil); !IsSandboxInterrupt(err) {
		t.Fatalf("stopped container was not retryable: %v", err)
	}
	if out, err := sb.Exec(ctx, CommandSpec{Provider: "script", Argv: []string{"sh", "-c", "true"}}, nil); err != nil {
		t.Fatalf("restart canonical: %s %v", out, err)
	}
	// Old gate helpers accept unknown argv as MCP mode and may exit zero without
	// executing anything. The version handshake must reject that silent no-op.
	stale := filepath.Join(workspace, "old-helper")
	if err := os.WriteFile(stale, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", stale)
	if _, err := sb.Exec(ctx, CommandSpec{Provider: "script", Argv: []string{"sh", "-c", "touch never-executed"}}, nil); err == nil || !strings.Contains(err.Error(), "version is incompatible") {
		t.Fatalf("stale helper accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "never-executed")); !os.IsNotExist(err) {
		t.Fatalf("rejected script still ran: %v", err)
	}
	if err := sb.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "inspect", dockerScriptContainerName(name)).CombinedOutput(); err == nil || !dockerObjectGone(string(out), err) {
		t.Fatalf("helper leaked: %s %v", out, err)
	}

}

func TestDockerScriptViewPreservesNonRootUser(t *testing.T) {
	if os.Getenv("AGENTFLOW_DOCKER_SCRIPT_SMOKE") != "1" {
		t.Skip("set AGENTFLOW_DOCKER_SCRIPT_SMOKE=1 for local Docker regression")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root := os.Getenv("AGENTFLOW_DOCKER_TEST_ROOT")
	if root == "" {
		root = "."
	}
	workspace, err := os.MkdirTemp(root, ".script-user-test-")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)
	if err = os.Chmod(workspace, 0777); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(workspace, "CLAUDE.md"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	maskArgs, err := dockerAgentConfigMasks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dockerIsolationMaskRoot(workspace))
	name := fmt.Sprintf("agentflow-script-user-qa-%d", time.Now().UnixNano())
	args := []string{"run", "--pull=never", "-d", "--name", name, "--user", "65534:65534", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "-v", workspace + ":/workspace"}
	args = append(args, maskArgs...)
	args = append(args, "alpine:3.20", "sh", "-c", "sleep 120")
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("nonroot fixture: %s %v", out, err)
	}
	sb := &dockerSandbox{id: name, name: name, workspace: workspace, started: true}
	defer sb.Close(context.Background())
	if out, err := sb.Exec(ctx, CommandSpec{Provider: "script", Argv: []string{"sh", "-ec", `test "$(id -u)" = 65534; test "$(id -g)" = 65534; test "$(cat CLAUDE.md)" = original; printf done > user-output; grep -q '^CapBnd:[[:space:]]*0000000000000000$' /proc/self/status`}}, nil); err != nil {
		t.Fatalf("nonroot script: %s %v", out, err)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "user-output")); err != nil || string(data) != "done" {
		t.Fatal(string(data), err)
	}
}
