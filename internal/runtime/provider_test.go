package runtime

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestProvidersBuildFinalArgvWithoutDuplicateManagedFlags(t *testing.T) {
	tests := []struct {
		kind   string
		driver string
		key    string
	}{
		{kind: "claude-code", driver: "local", key: "--system-prompt-file"},
		{kind: "codebuddy", driver: "local", key: "--permission-mode"},
		{kind: "codex", driver: "local", key: "--ignore-user-config"},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			compiled := compiledConfig{
				Args:      []string{test.key, "stale", test.key, "managed"},
				AgentRoot: ".agentflow/probe",
			}
			if test.kind == "codex" {
				compiled.Args = nil
			}
			command, err := providerForKind(test.kind).Prepare(providerInvocation{
				Executor: domain.ExecutorDefinition{Kind: test.kind, Binary: test.kind},
				Compiled: compiled,
				Prompt:   "probe",
				Driver:   test.driver,
			})
			if err != nil {
				t.Fatal(err)
			}
			argv := command.Argv
			count := 0
			for _, arg := range argv {
				if arg == test.key {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("%s count = %d in %v", test.key, count, argv)
			}
			if argv[len(argv)-1] != "probe" {
				t.Fatalf("prompt is not the final argument: %v", argv)
			}
			if test.kind == "codebuddy" && (len(argv) < 2 || argv[len(argv)-2] != "--") {
				t.Fatalf("CodeBuddy prompt is not separated from variadic options: %v", argv)
			}
		})
	}
}

func TestCodeBuddyPluginDirectoryCannotConsumePrompt(t *testing.T) {
	command, err := providerForKind("codebuddy").Prepare(providerInvocation{
		Executor: domain.ExecutorDefinition{Kind: "codebuddy", Binary: "codebuddy"},
		Compiled: compiledConfig{
			AgentRoot: ".agentflow/probe",
			HasSkills: true,
		},
		Prompt: "create fix-plan.md",
		Driver: "local",
	})
	if err != nil {
		t.Fatal(err)
	}
	argv := command.Argv
	if len(argv) < 5 || argv[len(argv)-4] != "--plugin-dir" || argv[len(argv)-3] != ".agentflow/probe" || argv[len(argv)-2] != "--" || argv[len(argv)-1] != "create fix-plan.md" {
		t.Fatalf("CodeBuddy plugin and prompt boundary is unsafe: %v", argv)
	}
}

func TestClaudePrintModeAddsVerboseForStreamJSONCompatibility(t *testing.T) {
	command, err := providerForKind("claude-code").Prepare(providerInvocation{
		Executor: domain.ExecutorDefinition{Kind: "claude-code", Binary: "claude", Args: []string{"-p", "--output-format", "stream-json"}},
		Compiled: compiledConfig{AgentRoot: ".agentflow/probe"},
		Prompt:   "probe",
		Driver:   "local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasArg(command.Argv, "--verbose") {
		t.Fatalf("Claude print-mode argv missing --verbose: %v", command.Argv)
	}
}

func TestProvidersResumeExistingCLISession(t *testing.T) {
	tests := []struct {
		kind   string
		binary string
		args   []string
		want   []string
	}{
		{kind: "codex", binary: "codex", args: []string{"exec", "--json"}, want: []string{"resume", "session-123", "continue"}},
		{kind: "claude-code", binary: "claude", args: []string{"-p", "--resume", "stale"}, want: []string{"--resume", "session-123", "continue"}},
		{kind: "codebuddy", binary: "codebuddy", args: []string{"-p", "--resume=stale"}, want: []string{"--resume", "session-123", "--", "continue"}},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			command, err := providerForKind(test.kind).Prepare(providerInvocation{
				Executor: domain.ExecutorDefinition{Kind: test.kind, Binary: test.binary, Args: test.args},
				Compiled: compiledConfig{AgentRoot: ".agentflow/probe"},
				Prompt:   "continue",
				Options:  InvocationOptions{ResumeSessionID: "session-123"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !hasContiguousArgs(command.Argv, test.want) {
				t.Fatalf("resume arguments not in the required order: %v", command.Argv)
			}
			if strings.Contains(strings.Join(command.Argv, " "), "stale") {
				t.Fatalf("persisted resume arguments were not replaced: %v", command.Argv)
			}
		})
	}
}

func hasContiguousArgs(argv, wanted []string) bool {
	for i := 0; i+len(wanted) <= len(argv); i++ {
		match := true
		for j := range wanted {
			if argv[i+j] != wanted[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestPositionalPromptKeepsShortTaskOnArgv(t *testing.T) {
	command, err := providerForKind("codebuddy").Prepare(providerInvocation{
		Executor:   domain.ExecutorDefinition{Kind: "codebuddy", Binary: "codebuddy"},
		Compiled:   compiledConfig{AgentRoot: ".agentflow/probe"},
		Prompt:     "create fix-plan.md",
		PromptFile: ".agentflow/runtime/x/task-prompt.md",
		Driver:     "local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.Argv[len(command.Argv)-1] != "create fix-plan.md" {
		t.Fatalf("short prompt must remain the user message: %v", command.Argv)
	}
}

func TestPositionalPromptPrefersTaskFileOverInlineBody(t *testing.T) {
	long := strings.Repeat("上游产物", 20_000)
	command, err := providerForKind("codebuddy").Prepare(providerInvocation{
		Executor:   domain.ExecutorDefinition{Kind: "codebuddy", Binary: "codebuddy"},
		Compiled:   compiledConfig{AgentRoot: ".agentflow/closer"},
		Prompt:     long,
		PromptFile: ".agentflow/runtime/close-3-1/task-prompt.md",
		Driver:     "local",
	})
	if err != nil {
		t.Fatal(err)
	}
	argv := command.Argv
	last := argv[len(argv)-1]
	if strings.Contains(last, long[:20]) {
		t.Fatalf("task body leaked into argv: %d bytes", len(last))
	}
	if !strings.Contains(last, ".agentflow/runtime/close-3-1/task-prompt.md") {
		t.Fatalf("argv should point at the task file: %v", argv)
	}
	if len(last) > 400 {
		t.Fatalf("positional prompt still too long: %d", len(last))
	}
}

func TestProvidersDeclareSandboxRequirements(t *testing.T) {
	tests := []struct {
		kind, state string
		bunFS       bool
	}{
		{kind: "claude-code", bunFS: true},
		{kind: "codebuddy", bunFS: true},
		{kind: "codex"},
		{kind: "mock"},
	}
	for _, test := range tests {
		requirements := providerForKind(test.kind).SandboxRequirements()
		state := ""
		if len(requirements.CLIStates) > 0 {
			state = requirements.CLIStates[0].Directory
		}
		if state != test.state || requirements.BunFS != test.bunFS {
			t.Fatalf("%s requirements = %+v", test.kind, requirements)
		}
	}
}

func TestEffortFlagInjectedPerProvider(t *testing.T) {
	tests := []struct {
		kind string
		want []string
	}{
		{kind: "claude-code", want: []string{"--effort", "high"}},
		{kind: "codebuddy", want: []string{"--effort", "high"}},
		{kind: "codex", want: []string{"-c", `model_reasoning_effort="high"`}},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			command, err := providerForKind(test.kind).Prepare(providerInvocation{
				Executor: domain.ExecutorDefinition{Kind: test.kind, Binary: test.kind, Effort: "high"},
				Compiled: compiledConfig{AgentRoot: ".agentflow/probe"},
				Prompt:   "probe",
				Driver:   "local",
			})
			if err != nil {
				t.Fatal(err)
			}
			if !hasContiguousArgs(command.Argv, test.want) {
				t.Fatalf("argv missing effort flag: %v", command.Argv)
			}
			if command.Argv[len(command.Argv)-1] != "probe" {
				t.Fatalf("prompt is not the final argument: %v", command.Argv)
			}
		})
	}
}

func TestEffortFlagPlacementBeforeCodexResume(t *testing.T) {
	command, err := providerForKind("codex").Prepare(providerInvocation{
		Executor: domain.ExecutorDefinition{Kind: "codex", Binary: "codex", Args: []string{"exec"}, Effort: "xhigh"},
		Compiled: compiledConfig{AgentRoot: ".agentflow/probe"},
		Prompt:   "continue",
		Options:  InvocationOptions{ResumeSessionID: "session-123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	argv := command.Argv
	effortIndex, resumeIndex := -1, -1
	for i, arg := range argv {
		if arg == `model_reasoning_effort="xhigh"` {
			effortIndex = i
		}
		if arg == "resume" {
			resumeIndex = i
		}
	}
	if effortIndex < 0 || resumeIndex < 0 || effortIndex > resumeIndex {
		t.Fatalf("effort override must precede the resume subcommand: %v", argv)
	}
}

func TestEffortFlagOmittedWithoutEffortAndNotDuplicated(t *testing.T) {
	command, err := providerForKind("codebuddy").Prepare(providerInvocation{
		Executor: domain.ExecutorDefinition{Kind: "codebuddy", Binary: "codebuddy", Args: []string{"--effort", "medium"}},
		Compiled: compiledConfig{AgentRoot: ".agentflow/probe"},
		Prompt:   "probe",
		Driver:   "local",
	})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, arg := range command.Argv {
		if arg == "--effort" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("--effort count = %d, want 1: %v", count, command.Argv)
	}
	if !hasContiguousArgs(command.Argv, []string{"--effort", "medium"}) {
		t.Fatalf("user-supplied effort value was changed: %v", command.Argv)
	}
}
