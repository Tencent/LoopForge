package runtime

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

type codexProvider struct{ baseProvider }

func (codexProvider) Kind() string { return "codex" }

func (codexProvider) Materialize(ctx *providerMaterializeContext) error {
	ctx.Compiled.Args = append(ctx.Compiled.Args, codexConfigArgs(string(ctx.Content), ctx.SkillPaths, ctx.MCPServers)...)
	ctx.Compiled.Args = append(ctx.Compiled.Args, codexHookConfigArgs(ctx.Compiled.Hooks)...)
	return nil
}

func (codexProvider) Prepare(in providerInvocation) (CommandSpec, error) {
	argv := buildProviderArgs(in)
	argv = dropCodexResumeArgs(argv)
	// exec options must precede the `resume` subcommand, so the effort config
	// override is appended here rather than after the resume session ID.
	if effort := strings.TrimSpace(in.Executor.Effort); effort != "" && !codexHasReasoningEffort(argv) {
		argv = append(argv, "-c", "model_reasoning_effort="+strconv.Quote(effort))
	}
	if len(in.Compiled.Hooks) > 0 && !hasArg(argv, "--dangerously-bypass-hook-trust") {
		argv = append(argv, "--dangerously-bypass-hook-trust")
	}
	if in.Driver == "local" {
		for _, flag := range []string{"--ignore-user-config", "--ignore-rules"} {
			if !hasArg(argv, flag) {
				argv = append(argv, flag)
			}
		}
	}
	if sessionID := strings.TrimSpace(in.Options.ResumeSessionID); sessionID != "" {
		// `resume` is a subcommand of `codex exec`; all exec options must precede it.
		argv = append(argv, "resume", sessionID)
	}
	return CommandSpec{Argv: append(argv, positionalPrompt(in)), Provider: "codex", CredentialBindings: []CredentialBinding{BindCodex, BindGit, BindMCP}, PrivateHome: true}, nil
}

func dropCodexResumeArgs(argv []string) []string {
	for i := 0; i < len(argv); i++ {
		if argv[i] != "resume" {
			continue
		}
		end := i + 1
		if end < len(argv) {
			end++
		}
		return append(append([]string{}, argv[:i]...), argv[end:]...)
	}
	return argv
}

func codexHasReasoningEffort(argv []string) bool {
	for _, arg := range argv {
		if strings.Contains(arg, "model_reasoning_effort") {
			return true
		}
	}
	return false
}

// withCodexIsolationEnv copies process-sandbox isolation variables into Codex
// tool subprocesses. inherit=core only keeps PATH/HOME/USER/SHELL/TERM/LANG,
// so ZDOTDIR never reaches `zsh -lc` unless it is explicitly set here.
func withCodexIsolationEnv(argv []string, overlay map[string]string) []string {
	if len(argv) == 0 || len(overlay) == 0 {
		return argv
	}
	keys := make([]string, 0, len(overlay))
	for key, value := range overlay {
		if !codexIsolationEnvKey(key) || strings.TrimSpace(value) == "" {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return argv
	}
	sort.Strings(keys)
	extra := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		extra = append(extra, "-c", "shell_environment_policy.set."+key+"="+strconv.Quote(overlay[key]))
	}
	out := make([]string, 0, len(argv)+len(extra))
	out = append(out, argv[:len(argv)-1]...)
	out = append(out, extra...)
	return append(out, argv[len(argv)-1])
}

func codexIsolationEnvKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	upper := strings.ToUpper(key)
	if upper == "GIT_CEILING_DIRECTORIES" {
		return true // a non-secret workspace boundary, needed by tool shells
	}
	for _, needle := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "AUTHORIZATION"} {
		if strings.Contains(upper, needle) {
			return false
		}
	}
	switch {
	case strings.HasPrefix(upper, "CODEBUDDY_"), strings.HasPrefix(upper, "GIT_"), strings.HasPrefix(upper, "SSH_"):
		return false
	default:
		return true
	}
}

func (codexProvider) Protocol(ex domain.ExecutorDefinition, sink EventSink) *protocolAdapter {
	return makeProtocolAdapter(ex, sink, protocolCodex, "codex-jsonl-v1")
}

func (codexProvider) SandboxRequirements() SandboxRequirements {
	return SandboxRequirements{PrivateHome: true}
}

func codexConfigArgs(instructions string, skillPaths []string, servers []domain.MCPServerDefinition) []string {
	args := []string{
		"-c", "developer_instructions=" + strconv.Quote(instructions),
		// core keeps PATH/HOME/USER/SHELL/TERM/LANG and drops everything else,
		// including ZDOTDIR. Isolation vars are re-injected at Exec via set.*.
		"-c", `shell_environment_policy.inherit="core"`,
	}
	if len(skillPaths) > 0 {
		entries := make([]string, 0, len(skillPaths))
		for _, skillPath := range skillPaths {
			entries = append(entries, fmt.Sprintf("{ path = %s, enabled = true }", strconv.Quote(skillPath)))
		}
		args = append(args, "-c", "skills.config=["+strings.Join(entries, ", ")+"]")
	}
	for _, m := range servers {
		prefix := "mcp_servers." + safeID(m.ID) + "."
		if m.Transport == "http" {
			args = append(args, "-c", prefix+"url="+strconv.Quote(m.URL))
		} else {
			args = append(args, "-c", prefix+"command="+strconv.Quote(m.Command))
			if cwd := MCPRuntimeWorkingDirectory(m); cwd != "" {
				args = append(args, "-c", prefix+"cwd="+strconv.Quote(cwd))
			}
			if len(m.Args) > 0 {
				parts := make([]string, len(m.Args))
				for i, value := range m.Args {
					parts[i] = strconv.Quote(value)
				}
				args = append(args, "-c", prefix+"args=["+strings.Join(parts, ",")+"]")
			}
			env := resolveMap(m.Environment)
			if len(env) > 0 {
				keys := make([]string, 0, len(env))
				for key := range env {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				pairs := make([]string, 0, len(keys))
				for _, key := range keys {
					pairs = append(pairs, fmt.Sprintf("%s = %s", key, strconv.Quote(env[key])))
				}
				args = append(args, "-c", prefix+"env={ "+strings.Join(pairs, ", ")+" }")
			}
		}
		if m.TimeoutSec > 0 {
			args = append(args, "-c", fmt.Sprintf("%stool_timeout_sec=%d.0", prefix, m.TimeoutSec))
		}
	}
	return args
}
