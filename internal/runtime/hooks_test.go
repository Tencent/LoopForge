package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/observability"
)

func TestAgentHooksCompileForAllCLIsAndStayInvocationScoped(t *testing.T) {
	hook := domain.AgentHook{ID: "audit", Name: "Audit", Event: "PreToolUse", Matcher: "Bash", Language: "python", Script: "import json, sys\nprint(json.load(sys.stdin)['hook_event_name'])", TimeoutSec: 7}
	agent := domain.AgentDefinition{ID: "reviewer", Name: "Reviewer", Hooks: []domain.AgentHook{hook}}
	for _, kind := range []string{"claude-code", "codebuddy", "codex"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			compiled, err := materialize(workspace, "invoke-1", domain.ExecutorDefinition{Kind: kind, Binary: kind}, agent, domain.AgentAssetBundle{})
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(workspace, ".agentflow", "config", "reviewer", "hooks", "audit.py")
			if raw, err := os.ReadFile(script); err != nil || !strings.Contains(string(raw), "hook_event_name") {
				t.Fatalf("materialized hook = %q err=%v", raw, err)
			}
			wrapper := filepath.Join(workspace, ".agentflow", "config", "reviewer", "hooks", "audit-agentflow.py")
			if raw, err := os.ReadFile(wrapper); err != nil || !strings.Contains(string(raw), "_agentflow_hook_id") {
				t.Fatalf("materialized wrapper = %q err=%v", raw, err)
			}
			if compiled.HookFile == "" || compiled.HookOutput == "" {
				t.Fatalf("hook observation path missing: %+v", compiled)
			}
			command := exec.Command("sh", "-lc", compiled.Hooks[0].Command)
			command.Dir = workspace
			command.Env = append(os.Environ(), "AGENTFLOW_HOOK_OUTPUT="+compiled.HookOutput)
			command.Stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`)
			if output, err := command.CombinedOutput(); err != nil || !strings.Contains(string(output), "PreToolUse") {
				t.Fatalf("wrapped hook output = %q err=%v", output, err)
			}
			observed, err := os.ReadFile(compiled.HookFile)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(observed)), "\n")
			if len(lines) != 2 {
				t.Fatalf("expected start and finish records, got %d: %s", len(lines), observed)
			}
			started, startOK := observability.HookEvent([]byte(lines[0]))
			finished, finishOK := observability.HookEvent([]byte(lines[1]))
			if !startOK || started.Name != "hook.start" || !finishOK || finished.Name != "hook.finish" || finished.Source.Provider != kind || finished.Attributes["hookId"] != "audit" {
				t.Fatalf("observed start=%+v ok=%v finish=%+v ok=%v raw=%s", started, startOK, finished, finishOK, observed)
			}
			if finished.Attributes["exitCode"] != int64(0) || !strings.Contains(finished.Attributes["stdout"].(string), "PreToolUse") {
				t.Fatalf("finish result metadata missing: %+v", finished)
			}
			switch kind {
			case "claude-code":
				settings := filepath.Join(workspace, ".agentflow", "config", "reviewer", "claude-settings.json")
				assertHookSettings(t, settings, "PreToolUse", "python3 '.agentflow/config/reviewer/hooks/audit-agentflow.py'")
				if !containsArgPair(compiled.Args, "--settings", ".agentflow/config/reviewer/claude-settings.json") {
					t.Fatalf("Claude settings not attached: %v", compiled.Args)
				}
			case "codebuddy":
				settings := filepath.Join(workspace, ".agentflow", "hooks", "codebuddy-settings.json")
				assertHookSettings(t, settings, "PreToolUse", "python3 '.agentflow/config/reviewer/hooks/audit-agentflow.py'")
			case "codex":
				joined := strings.Join(compiled.Args, " ")
				if !strings.Contains(joined, "hooks.PreToolUse=") || !strings.Contains(joined, "audit-agentflow.py") {
					t.Fatalf("Codex inline hooks not attached: %v", compiled.Args)
				}
				prepared, err := (codexProvider{}).Prepare(providerInvocation{Executor: domain.ExecutorDefinition{Kind: kind, Binary: "codex"}, Compiled: compiled})
				if err != nil || !hasArg(prepared.Argv, "--dangerously-bypass-hook-trust") {
					t.Fatalf("Codex hook trust was not scoped to invocation: %v err=%v", prepared.Argv, err)
				}
			}
		})
	}

	workspace := t.TempDir()
	plain := domain.AgentDefinition{ID: "plain", Name: "Plain"}
	compiled, err := materialize(workspace, "invoke-2", domain.ExecutorDefinition{Kind: "codex", Binary: "codex"}, plain, domain.AgentAssetBundle{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := (codexProvider{}).Prepare(providerInvocation{Executor: domain.ExecutorDefinition{Kind: "codex", Binary: "codex"}, Compiled: compiled})
	if err != nil {
		t.Fatal(err)
	}
	if hasArg(prepared.Argv, "--dangerously-bypass-hook-trust") || strings.Contains(strings.Join(prepared.Argv, " "), "hooks.") {
		t.Fatalf("plain invocation inherited hooks: %v", prepared.Argv)
	}
}

func TestRepositoryScopedHookRunsInEveryRepository(t *testing.T) {
	workspace := t.TempDir()
	for _, dir := range []string{"frontend", "backend"} {
		if err := os.MkdirAll(filepath.Join(workspace, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hook := domain.AgentHook{
		ID: "repo-audit", Name: "Repository audit", Event: "PreToolUse", Language: "shell",
		ExecutionScope: domain.ExecutionScopeRepositories,
		Script:         `printf '%s|%s' "$AGENTFLOW_REPOSITORY_NAME" "$PWD" > "$AGENTFLOW_WORKSPACE/hook-$AGENTFLOW_REPOSITORY_ID.txt"`,
		TimeoutSec:     7,
	}
	agent := domain.AgentDefinition{ID: "reviewer", Name: "Reviewer", Hooks: []domain.AgentHook{hook}}
	compiled, err := materialize(workspace, "invoke-repositories", domain.ExecutorDefinition{Kind: "codex", Binary: "codex"}, agent, domain.AgentAssetBundle{})
	if err != nil {
		t.Fatal(err)
	}
	repositories := []domain.RepositoryContext{
		{RepositoryID: "frontend-id", Name: "Frontend", WorkspaceDir: "frontend"},
		{RepositoryID: "backend-id", Name: "Backend", WorkspaceDir: "backend"},
	}
	environment := hookExecutionEnvironment(repositories)
	command := exec.Command("sh", "-lc", compiled.Hooks[0].Command)
	command.Dir = workspace
	command.Env = append(os.Environ(), "AGENTFLOW_HOOK_OUTPUT="+compiled.HookOutput, "AGENTFLOW_REPOSITORIES_JSON="+environment["AGENTFLOW_REPOSITORIES_JSON"])
	command.Stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`)
	if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("repository hook output = %q err=%v", output, err)
	}
	for _, repository := range repositories {
		content, err := os.ReadFile(filepath.Join(workspace, "hook-"+repository.RepositoryID+".txt"))
		if err != nil || !strings.Contains(string(content), filepath.Join(workspace, repository.WorkspaceDir)) {
			t.Fatalf("repository hook did not run in %s: %q err=%v", repository.WorkspaceDir, content, err)
		}
	}
	rawEvents, err := os.ReadFile(compiled.HookFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(rawEvents)), "\n")
	if len(lines) != 4 {
		t.Fatalf("repository hook event count = %d, want 4: %s", len(lines), rawEvents)
	}
	for index, repository := range repositories {
		for _, line := range lines[index*2 : index*2+2] {
			event, ok := observability.HookEvent([]byte(line))
			if !ok || event.Attributes["repositoryId"] != repository.RepositoryID || event.Attributes["repositoryDir"] != repository.WorkspaceDir {
				t.Fatalf("repository hook telemetry mismatch: %#v", event)
			}
		}
	}
}

func TestMergeAgentHookDefinitionsUsesSystemThenRoleAndRejectsIDConflicts(t *testing.T) {
	systemHook := domain.AgentHook{ID: "system-audit", Event: "Stop", Language: "shell", Script: "true"}
	roleHook := domain.AgentHook{ID: "role-audit", Event: "Stop", Language: "shell", Script: "true"}
	merged, err := mergeAgentHookDefinitions([]domain.AgentHook{systemHook}, []domain.AgentHook{roleHook})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 2 || merged[0].ID != systemHook.ID || merged[1].ID != roleHook.ID {
		t.Fatalf("unexpected effective hook order: %+v", merged)
	}
	if _, err := mergeAgentHookDefinitions([]domain.AgentHook{systemHook}, []domain.AgentHook{{ID: systemHook.ID}}); err == nil {
		t.Fatal("expected system/role hook id conflict")
	}
	emptySystem, err := mergeAgentHookDefinitions(nil, []domain.AgentHook{roleHook})
	if err != nil || len(emptySystem) != 1 || emptySystem[0].ID != roleHook.ID {
		t.Fatalf("empty system hooks must still use merge path: %+v err=%v", emptySystem, err)
	}
}

func assertHookSettings(t *testing.T, path, event, command string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), event) || !strings.Contains(string(raw), command) {
		t.Fatalf("settings missing hook: %s", raw)
	}
}
