package runtime

import "agentflow/internal/domain"

// DeepSeek uses the Responses API and Codex tool runtime with its own credentials.
type deepSeekProvider struct{ codexProvider }

func (deepSeekProvider) Kind() string { return "deepseek" }

func (p deepSeekProvider) Prepare(in providerInvocation) (CommandSpec, error) {
	if in.Executor.Model == "" || in.Executor.Model == "default" {
		in.Executor.Model = domain.DeepSeekDefaultModel
	}
	command, err := p.codexProvider.Prepare(in)
	if err != nil {
		return CommandSpec{}, err
	}
	command.Provider = "deepseek"
	command.CredentialBindings = []CredentialBinding{BindDeepSeek, BindGit, BindMCP}
	return command, nil
}
