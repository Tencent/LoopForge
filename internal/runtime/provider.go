package runtime

import (
	"context"
	"strings"

	"agentflow/internal/domain"
)

// SandboxRequirements describes the host state and runtime features a provider
// needs before a Run sandbox is created.
type SandboxRequirements struct {
	CLIStates   []CLIStateRequirement
	PrivateHome bool
	BunFS       bool
}

type CLIStateRequirement struct {
	Directory string
	Files     []string
}

type providerMaterializeContext struct {
	Workspace  string
	AgentID    string
	AgentRel   string
	RuntimeRel string
	ConfigRoot string
	AgentRoot  string
	Content    []byte
	SkillPaths []string
	MCPServers []domain.MCPServerDefinition
	Assets     domain.AgentAssetBundle
	Compiled   *compiledConfig
}

type providerInvocation struct {
	Executor   domain.ExecutorDefinition
	Compiled   compiledConfig
	Prompt     string
	PromptFile string
	Driver     string
	Options    InvocationOptions
}

// Provider owns all behavior that varies by executor kind. The runtime remains
// responsible for shared materialization, process execution and workspace safety.
type Provider interface {
	Kind() string
	Materialize(*providerMaterializeContext) error
	Prepare(providerInvocation) (CommandSpec, error)
	Protocol(domain.ExecutorDefinition, EventSink) *protocolAdapter
	SandboxRequirements() SandboxRequirements
	RunBuiltin(context.Context, domain.AgentDefinition, string, EventSink) (string, bool, error)
	StartObservation(context.Context, compiledConfig, EventSink) context.CancelFunc
	// SystemHooks returns provider-owned hooks that must be merged ahead of the
	// role's hooks and rendered as one effective provider configuration.
	SystemHooks() []domain.AgentHook
}

type baseProvider struct {
	kind    string
	parser  protocolParser
	adapter string
}

func (p baseProvider) Kind() string { return p.kind }

func (baseProvider) Materialize(*providerMaterializeContext) error { return nil }

func (p baseProvider) Prepare(in providerInvocation) (CommandSpec, error) {
	return CommandSpec{Argv: append(buildProviderArgs(in), positionalPrompt(in)), Provider: p.Kind(), PrivateHome: p.SandboxRequirements().PrivateHome}, nil
}

func buildProviderArgs(in providerInvocation) []string {
	argv := append([]string{in.Executor.Binary}, in.Executor.Args...)
	argv = append(argv, in.Compiled.Args...)
	if in.Executor.Model != "" && !hasArg(argv, "--model") {
		argv = append(argv, "--model", in.Executor.Model)
	}
	return argv
}

// appendEffortArgs injects the native --effort flag used by Claude Code and
// CodeBuddy. Codex maps the same setting onto a -c config override in its own
// Prepare, so this helper is intentionally Claude/CodeBuddy-only.
func appendEffortArgs(argv []string, effort string) []string {
	effort = strings.TrimSpace(effort)
	if effort == "" || hasArg(argv, "--effort") {
		return argv
	}
	return append(argv, "--effort", effort)
}

// maxArgvPromptBytes stays under Linux MAX_ARG_STRLEN (~128KiB per argument).
// Ordinary node task books stay on argv so existing SOP behavior is unchanged.
const maxArgvPromptBytes = 96 << 10

func positionalPrompt(in providerInvocation) string {
	if rel := strings.TrimSpace(in.PromptFile); rel != "" && len(in.Prompt) > maxArgvPromptBytes {
		return "执行系统提示中的「本次节点任务书」（完整正文也在 `" + rel + "`）。不要等待更多输入。"
	}
	return in.Prompt
}

func (p baseProvider) Protocol(ex domain.ExecutorDefinition, sink EventSink) *protocolAdapter {
	return makeProtocolAdapter(ex, sink, p.parser, p.adapter)
}

func (baseProvider) SandboxRequirements() SandboxRequirements { return SandboxRequirements{} }

func (baseProvider) RunBuiltin(context.Context, domain.AgentDefinition, string, EventSink) (string, bool, error) {
	return "", false, nil
}

func (baseProvider) StartObservation(context.Context, compiledConfig, EventSink) context.CancelFunc {
	return func() {}
}

func (baseProvider) SystemHooks() []domain.AgentHook { return nil }

var providers = map[string]Provider{
	"claude-code": claudeProvider{},
	"codebuddy":   codeBuddyProvider{},
	"codex":       codexProvider{},
	"mock":        mockProvider{},
}

func providerForKind(kind string) Provider {
	if provider := providers[strings.TrimSpace(kind)]; provider != nil {
		return provider
	}
	return baseProvider{kind: kind, parser: protocolGeneric, adapter: "generic-json-v1"}
}

func MergeSandboxRequirements(values ...SandboxRequirements) SandboxRequirements {
	var merged SandboxRequirements
	seen := map[string]bool{}
	for _, value := range values {
		merged.BunFS = merged.BunFS || value.BunFS
		states := append([]CLIStateRequirement(nil), value.CLIStates...)
		for _, state := range states {
			if state.Directory == "" || seen[state.Directory] {
				continue
			}
			seen[state.Directory] = true
			merged.CLIStates = append(merged.CLIStates, state)
		}
	}
	return merged
}

func ProviderRequirements(kind string) SandboxRequirements {
	return providerForKind(kind).SandboxRequirements()
}

// PrepareProviderCommand exposes the same provider flag and credential-binding
// policy to control-plane call sites that do not run a workflow node.
func PrepareProviderCommand(executor domain.ExecutorDefinition, prompt, driver string, options InvocationOptions) (CommandSpec, error) {
	if executor.Model == "default" {
		executor.Model = ""
	}
	return providerForKind(executor.Kind).Prepare(providerInvocation{
		Executor: executor,
		Prompt:   prompt,
		Driver:   driver,
		Options:  options,
	})
}
