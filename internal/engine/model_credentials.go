package engine

import (
	"slices"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

const (
	codeBuddyAPIKeyEnv   = "CODEBUDDY_API_KEY"
	codeBuddyInternetEnv = "CODEBUDDY_INTERNET_ENVIRONMENT"
)

// resolveCodeBuddyCredential returns the responsible user's own key, or the
// platform's shared CodeBuddy key when the user has none (see
// repo.SharedModelKey).
func resolveCodeBuddyCredential(s *repo.Store, userID int64) runtime.CodeBuddyCredential {
	if s == nil || userID <= 0 {
		return runtime.CodeBuddyCredential{}
	}
	user, ok := s.User(userID)
	if !ok {
		return runtime.CodeBuddyCredential{}
	}
	if strings.TrimSpace(user.CodeBuddyAPIKey) != "" {
		return runtime.CodeBuddyUserCredential(user)
	}
	shared, ok := s.SharedModelKey(domain.SharedModelProviderCodeBuddy)
	if !ok || (shared.SponsorID != 0 && shared.SponsorID == user.ID) {
		return runtime.CodeBuddyCredential{InternetEnvironment: user.CodeBuddyInternetEnvironment}
	}
	return runtime.CodeBuddyCredential{APIKey: shared.APIKey, InternetEnvironment: shared.InternetEnvironment, Sponsored: true, SponsoredModels: shared.Models}
}

// resolveProviderCredential returns the responsible user's own Codex or
// Claude Code credential, or the platform's shared key for that CLI when the
// user configured none (see repo.SharedModelKey).
func resolveProviderCredential(s *repo.Store, userID int64, provider string, personal runtime.ProviderCredential) runtime.ProviderCredential {
	if strings.TrimSpace(personal.APIKey) != "" || s == nil || userID <= 0 {
		return personal
	}
	user, ok := s.User(userID)
	if !ok {
		return personal
	}
	shared, ok := s.SharedModelKey(provider)
	if !ok || strings.TrimSpace(shared.APIKey) == "" || len(shared.Models) == 0 {
		return personal
	}
	if shared.SponsorID != 0 && shared.SponsorID == user.ID {
		return personal
	}
	return runtime.ProviderCredential{APIKey: shared.APIKey, BaseURL: shared.BaseURL, Sponsored: true, SponsoredModels: shared.Models}
}

const sponsoredRunEvent = "model.sponsored"

// sponsoredExecutor limits a node running on a shared key to the models the
// administrator allowed for that CLI: the role's model when it is one of
// them, the default otherwise, and tells the user why the role's model is not
// used.
func (e *Engine) sponsoredExecutor(runID string, ex domain.ExecutorDefinition, provider string, models []string, sink func(domain.TelemetryEvent)) domain.ExecutorDefinition {
	configured := strings.TrimSpace(ex.Model)
	key := repo.SharedModelKey{Models: models}
	model, allowed := key.Model(configured)
	if !allowed {
		model, _ = key.Model("")
	}
	if model == "" {
		model = repo.SponsoredCodeBuddyModel
	}
	ex.Model = model
	label := sponsoredLabel(provider)
	message := "责任人未配置个人 " + label + " API Key，本节点使用平台共享 Key，模型 " + model
	if configured != "" && configured != model {
		message += "（角色配置的 " + configured + " 不在平台允许的模型中，本次不生效）"
	}
	sink(domain.TelemetryEvent{Category: "model", Name: sponsoredRunEvent, Phase: "start", Message: message, Provider: ex.Kind, Model: ex.Model, Attributes: map[string]any{"configuredModel": configured}})
	if current, ok := e.repo.RunState(runID); ok && !hasRunEvent(current, sponsoredRunEvent) {
		e.updateRun(runID, func(r *domain.Run) {
			if !hasRunEvent(*r, sponsoredRunEvent) {
				e.event(r, "", sponsoredRunEvent, "责任人未配置个人 "+label+" API Key，本次运行使用平台共享 Key，只能使用平台管理员允许的模型（"+strings.Join(models, "、")+"）。在个人配置中配置自己的 Key 后，可使用角色配置的任意模型")
			}
		})
	}
	return ex
}

// sponsoredLabel is the CLI name shown in run messages.
func sponsoredLabel(provider string) string {
	switch provider {
	case domain.SharedModelProviderCodex:
		return "Codex"
	case domain.SharedModelProviderClaude:
		return "Claude Code"
	}
	return "CodeBuddy"
}

// applySponsoredExecutor pins the model of a node whose credential came from
// the platform's shared key instead of the responsible user.
func (e *Engine) applySponsoredExecutor(runID string, ex domain.ExecutorDefinition, credentials runtime.RunCredentials, sink func(domain.TelemetryEvent)) domain.ExecutorDefinition {
	switch ex.Kind {
	case domain.SharedModelProviderCodeBuddy:
		if cb := credentials.CodeBuddy; cb.Sponsored && len(cb.SponsoredModels) > 0 {
			return e.sponsoredExecutor(runID, ex, domain.SharedModelProviderCodeBuddy, cb.SponsoredModels, sink)
		}
	case domain.SharedModelProviderCodex:
		if p := credentials.Codex; p.Sponsored && len(p.SponsoredModels) > 0 {
			return e.sponsoredExecutor(runID, ex, domain.SharedModelProviderCodex, p.SponsoredModels, sink)
		}
	case domain.SharedModelProviderClaude:
		if p := credentials.ClaudeCode; p.Sponsored && len(p.SponsoredModels) > 0 {
			return e.sponsoredExecutor(runID, ex, domain.SharedModelProviderClaude, p.SponsoredModels, sink)
		}
	}
	return ex
}

// runnerUsesLocalLogin reports whether a node of kind dispatched to runnerID
// runs with that machine's own CLI login (see dispatchRunnerTask), so no
// platform shared key reaches it.
func (e *Engine) runnerUsesLocalLogin(run domain.Run, runnerID, kind string) bool {
	if runnerID == "" {
		return false
	}
	runner, ok := e.repo.Runner(runnerID)
	return ok && sopRunnerAuthMode(run.ExecutionContext, runner, kind) == domain.RuntimeAuthLocal
}

func hasRunEvent(run domain.Run, typ string) bool {
	return slices.ContainsFunc(run.Events, func(event domain.RunEvent) bool { return event.Type == typ })
}
