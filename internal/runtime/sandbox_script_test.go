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

func TestProcessSandboxUsesSamePolicyForScriptsAndAgents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("repo instructions"), 0o600); err != nil {
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
			if strings.Contains(text, filepath.Join(root, "CLAUDE.md")) {
				t.Fatalf("%s/%s masks repository instructions: %s", driver, provider, text)
			}
			if driver == "bwrap" && !strings.Contains(text, "--ro-bind / /") {
				t.Fatal("command lost host filesystem boundary")
			}
			if driver == "seatbelt" && !strings.Contains(text, "(deny file-write*)") {
				t.Fatal("command lost host write boundary")
			}
		}
	}
}

func dockerScriptTestSandbox(t *testing.T) (context.Context, *dockerSandbox) {
	t.Helper()
	if os.Getenv("AGENTFLOW_DOCKER_SCRIPT_SMOKE") != "1" {
		t.Skip("set AGENTFLOW_DOCKER_SCRIPT_SMOKE=1 for local Docker regression")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	root := os.Getenv("AGENTFLOW_DOCKER_TEST_ROOT")
	if root == "" {
		root = "."
	}
	workspace, err := os.MkdirTemp(root, ".docker-script-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "alpine:3.20")
	t.Setenv("AGENTFLOW_DOCKER_NETWORK", "none")
	name := fmt.Sprintf("agentflow-script-qa-%d", time.Now().UnixNano())
	sb := &dockerSandbox{id: name, name: name, workspace: workspace,
		profile:    domain.SandboxProfile{Driver: "docker"},
		dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile},
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return ctx, sb
}

func dockerScriptTestConversationRoot(t *testing.T, sb *dockerSandbox) string {
	t.Helper()
	root, err := os.MkdirTemp(filepath.Dir(sb.workspace), ".docker-conversation-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("AGENTFLOW_CONVERSATION_RELAY_HOST", "host.docker.internal")
	t.Setenv("AGENTFLOW_CONVERSATION_DOCKER_NETWORK", "none")
	return root
}

func TestDockerWorkspaceScriptsAndAgentsShareEnvironment(t *testing.T) {
	ctx, sb := dockerScriptTestSandbox(t)
	path := filepath.Join(sb.workspace, "repo", "dependency")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "AGENTS.md"), []byte("repository instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A conversation without preparation scripts can start using just the main container.
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", filepath.Join(sb.workspace, "absent-gate-helper"))
	conversationRoot := dockerScriptTestConversationRoot(t, sb)
	if err := ConfigureConversationSandbox(ctx, sb, conversationRoot); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{}
	if state, err := EnsureWorkspacePreparation(ctx, sb, wf, nil, nil); err != nil || state.Status != "ready" {
		t.Fatalf("prepare conversation workspace: %+v %v", state, err)
	}
	identity, err := ReadSandboxEnvironmentIdentity(ctx, sb)
	if err != nil {
		t.Fatal(err)
	}
	read := CommandSpec{Provider: "cursor", WorkingDir: filepath.Join(conversationRoot, "ws"),
		Argv: []string{"cat", "repo/dependency/AGENTS.md"},
	}
	if out, err := sb.Exec(ctx, read, nil); err != nil || strings.TrimSpace(out) != "repository instructions" {
		t.Fatalf("workspace CLI reads repository instruction: %q %v", out, err)
	}
	guard := filepath.Join(conversationRoot, "home", ".cursor", "hooks.json")
	if out, err := sb.Exec(ctx, CommandSpec{Provider: "cursor", Argv: []string{"sh", "-ec",
		`test "$(cat "$1")" = '{}' && ! (printf changed > "$1") 2>/dev/null`, "guard", guard}}, nil); err != nil {
		t.Fatalf("conversation approval configuration is writable: %q %v", out, err)
	}

	script := `test "$(cat repo/dependency/AGENTS.md)" = 'repository instructions'
rm -r repo/dependency
mkdir repo/dependency
printf replacement > repo/dependency/AGENTS.md
printf prepared > /usr/local/script-marker
sleep 120 </dev/null >/dev/null 2>&1 &
echo $! > script-service-pid`
	if out, err := sb.Exec(ctx, CommandSpec{Provider: "script", Argv: []string{"sh", "-ec", script}}, nil); err != nil {
		t.Fatalf("workspace script: %q %v", out, err)
	}
	check := `test "$(cat repo/dependency/AGENTS.md)" = replacement
test "$(cat /usr/local/script-marker)" = prepared
kill -0 "$(cat script-service-pid)"
grep -q '^CapEff:[[:space:]]*0000000000000000$' /proc/self/status
grep -q '^NoNewPrivs:[[:space:]]*1$' /proc/self/status
grep -q '^Seccomp:[[:space:]]*2$' /proc/self/status`
	if out, err := sb.Exec(ctx, CommandSpec{Provider: "codex", Argv: []string{"sh", "-ec", check}}, nil); err != nil {
		t.Fatalf("script state unavailable to agent: %q %v", out, err)
	}
	if after, err := ReadSandboxEnvironmentIdentity(ctx, sb); err != nil || after != identity {
		t.Fatalf("script changed the environment identity: %q %v", after, err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "inspect", sb.name+legacyDockerScriptSuffix).CombinedOutput(); !dockerObjectGone(string(out), err) {
		t.Fatalf("script companion exists: %s %v", out, err)
	}
}

func TestDockerWorkspaceRecreatesLegacyMaskedContainer(t *testing.T) {
	ctx, sb := dockerScriptTestSandbox(t)
	workspace := sb.workspace
	mask, err := EnsureControlDirectory(workspace, "isolation")
	if err != nil {
		t.Fatal(err)
	}
	emptyFile := filepath.Join(mask, "empty-file")
	if err := os.WriteFile(emptyFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	instructions := filepath.Join(workspace, "AGENTS.md")
	if err := os.WriteFile(instructions, []byte("keep existing instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "prepared-count"), []byte("once\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bind := []string{"run", "--pull=never", "--network", "none", "-d", "-v", workspace + ":/workspace"}
	legacy := append(append([]string{}, bind...), "--name", sb.name,
		"-v", emptyFile+":/workspace/.agentflow/isolation/empty-file:ro", "-v", emptyFile+":/workspace/AGENTS.md:ro",
		"alpine:3.20", "sleep", "120")
	companion := append(append([]string{}, bind...), "--name", sb.name+legacyDockerScriptSuffix, "alpine:3.20", "sleep", "120")
	for _, args := range [][]string{legacy, companion} {
		if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("legacy fixture: %s %v", out, err)
		}
	}
	oldIdentity, err := ReadSandboxEnvironmentIdentity(ctx, sb)
	if err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{
		ID: "prepare", Name: "Prepare", Type: "script", RunOnWorkspaceRebuild: true,
		Script: "printf 'again\\n' >> prepared-count; printf ready > /tmp/prepared",
	}}}
	if _, err := RecordWorkspacePreparation(ctx, sb, wf, nil, "prepare", ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", sb.name+"-missing-image:local")
	if _, err := EnsureWorkspacePreparation(ctx, sb, wf, nil, nil); err == nil {
		t.Fatal("legacy upgrade unexpectedly succeeded without a replacement image")
	}
	if err := ConfigureConversationSandbox(ctx, sb, dockerScriptTestConversationRoot(t, sb)); err == nil || sb.requirements.ConversationRoot != "" {
		t.Fatalf("failed conversation configuration was applied: %v", err)
	}
	if identity, err := ReadSandboxEnvironmentIdentity(ctx, sb); err != nil || identity != oldIdentity {
		t.Fatalf("failed upgrade discarded the old workspace: %s %v", identity, err)
	}
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "alpine:3.20")
	for range 2 {
		state, err := EnsureWorkspacePreparation(ctx, sb, wf, nil, nil)
		if err != nil || state.Status != "ready" || state.Environment == oldIdentity {
			t.Fatalf("legacy workspace was not rebuilt: %+v %v", state, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "prepared-count")); err != nil || string(data) != "once\nagain\n" {
		t.Fatalf("workspace preparation replayed incorrectly: %q %v", data, err)
	}
	if out, err := sb.Exec(ctx, CommandSpec{Argv: []string{"sh", "-ec",
		`test "$(cat AGENTS.md)" = 'keep existing instructions'; test "$(cat /tmp/prepared)" = ready`}}, nil); err != nil {
		t.Fatalf("recreated workspace lost files or prep: %s %v", out, err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "inspect", sb.name+legacyDockerScriptSuffix).CombinedOutput(); !dockerObjectGone(string(out), err) {
		t.Fatalf("legacy companion not reclaimed: %s %v", out, err)
	}
}

func TestDockerWorkspaceRecoversExternallyStoppedOrRemovedContainer(t *testing.T) {
	ctx, sb := dockerScriptTestSandbox(t)
	identity, err := SandboxEnvironmentIdentity(ctx, sb)
	if err != nil {
		t.Fatal(err)
	}
	for _, remove := range [][]string{{"stop", "-t", "0", sb.name}, {"rm", "-f", sb.name}} {
		if out, err := exec.CommandContext(ctx, "docker", remove...).CombinedOutput(); err != nil {
			t.Fatalf("docker %s: %s %v", remove[0], out, err)
		}
		next, err := SandboxEnvironmentIdentity(ctx, sb)
		if err != nil || next == identity {
			t.Fatalf("after docker %s: identity %q (was %q) %v", remove[0], next, identity, err)
		}
		if out, err := sb.Exec(ctx, CommandSpec{Argv: []string{"true"}}, nil); err != nil {
			t.Fatalf("after docker %s: exec %q %v", remove[0], out, err)
		}
		identity = next
	}
}

func TestDockerWorkspaceScriptPreservesNonRootUser(t *testing.T) {
	ctx, sb := dockerScriptTestSandbox(t)
	if err := os.Chmod(sb.workspace, 0o777); err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--pull=never", "--network", "none", "-d", "--name", sb.name,
		"--user", "65534:65534", "--cap-drop=ALL", "--security-opt", "no-new-privileges",
		"-v", sb.workspace + ":/workspace", "alpine:3.20", "sleep", "120"}
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("non-root fixture: %s %v", out, err)
	}
	script := `test "$(id -u):$(id -g)" = 65534:65534
printf done > user-output
grep -q '^CapBnd:[[:space:]]*0000000000000000$' /proc/self/status`
	if out, err := sb.Exec(ctx, CommandSpec{Provider: "script", Argv: []string{"sh", "-ec", script}}, nil); err != nil {
		t.Fatalf("non-root script: %s %v", out, err)
	}
	if data, err := os.ReadFile(filepath.Join(sb.workspace, "user-output")); err != nil || string(data) != "done" {
		t.Fatalf("non-root script output: %q %v", data, err)
	}
}
