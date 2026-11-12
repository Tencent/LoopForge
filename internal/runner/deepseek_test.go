package runner

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestDeepSeekRunnerUsesCodexRuntimeAndPersonalCredentials(t *testing.T) {
	rt := domain.RunnerRuntime{Provider: "codex", BinaryPath: "/usr/bin/codex", AuthMode: domain.RuntimeAuthLocal}
	runner := domain.Runner{RunnerVersion: "0.9.2", Runtimes: []domain.RunnerRuntime{rt}}
	if runner.SupportsProvider("deepseek") || !runner.SupportsProvider("codex") {
		t.Fatal("older runners should only accept Codex tasks")
	}
	runner.RunnerVersion = "0.9.3"
	if runner.SupportsProvider("deepseek") {
		t.Fatal("a runner version alone does not establish DeepSeek support")
	}
	runner.RunnerVersion = "0.9.2"
	rt.Capabilities = []string{domain.RunnerCapabilityDeepSeek}
	runner.Runtimes = []domain.RunnerRuntime{rt}
	if !runner.SupportsProvider("deepseek") || runner.RuntimeAuthMode("deepseek") != domain.RuntimeAuthPersonal {
		t.Fatal("DeepSeek must reuse Codex binary with its own key")
	}
	disabled := rt
	disabled.Status = domain.RuntimeStatusDisabled
	runner.Runtimes = []domain.RunnerRuntime{disabled}
	if runner.SupportsProvider("deepseek") {
		t.Fatal("a disabled Codex runtime must not receive DeepSeek tasks")
	}
	d := &Daemon{runtimes: []domain.RunnerRuntime{rt}}
	if !strings.Contains(strings.Join(d.availableProviders(), ","), "deepseek") {
		t.Fatal("runner cannot claim DeepSeek tasks")
	}
	if binary, ok := d.taskProviderBinary(domain.RunnerTask{Provider: "deepseek"}); !ok || binary != rt.BinaryPath {
		t.Fatal("DeepSeek binary not resolved")
	}
	inv := &domain.RunnerInvocation{CredentialSource: domain.RuntimeAuthPersonal, AgentCLIAPIKey: "deepseek-runner-secret", CodexAPIKey: "different-secret", ClaudeCodeAPIKey: "another-secret"}
	argv, env, err := bindInvocationCredentials(inv, []runtime.CredentialBinding{runtime.BindDeepSeek}, []string{"codex", "exec", "prompt"})
	if err != nil || env["CODEX_API_KEY"] != inv.AgentCLIAPIKey || !strings.Contains(strings.Join(argv, " "), domain.DeepSeekDefaultBaseURL) || env["ANTHROPIC_AUTH_TOKEN"] != "" {
		t.Fatal("DeepSeek credentials not isolated", err)
	}
	inv.AgentCLIBaseURL = "https://deepseek.example/v1"
	public := domain.PublicRunnerTask(domain.RunnerTask{Invocation: inv})
	if public.Invocation.AgentCLIAPIKey != "" || public.Invocation.AgentCLIBaseURL != "" || inv.AgentCLIAPIKey == "" || inv.AgentCLIBaseURL == "" {
		t.Fatal("management response leaked or mutated the credential")
	}
	inv.AgentCLIAPIKey = ""
	if _, _, err := bindInvocationCredentials(inv, []runtime.CredentialBinding{runtime.BindDeepSeek}, []string{"codex", "exec", "prompt"}); err == nil {
		t.Fatal("DeepSeek used a different provider key")
	}
}
