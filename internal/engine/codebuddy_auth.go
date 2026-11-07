package engine

import (
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
// platform sponsor's key when the user has none (see repo.CodeBuddySponsor).
func resolveCodeBuddyCredential(s *repo.Store, userID int64) runtime.CodeBuddyCredential {
	if s == nil || userID <= 0 {
		return runtime.CodeBuddyCredential{}
	}
	user, ok := s.User(userID)
	if !ok {
		return runtime.CodeBuddyCredential{}
	}
	if strings.TrimSpace(user.CodeBuddyAPIKey) != "" {
		return runtime.CodeBuddyCredential{APIKey: user.CodeBuddyAPIKey, InternetEnvironment: user.CodeBuddyInternetEnvironment}
	}
	sponsor, ok := s.CodeBuddySponsor()
	if !ok || sponsor.ID == user.ID {
		return runtime.CodeBuddyCredential{InternetEnvironment: user.CodeBuddyInternetEnvironment}
	}
	return runtime.CodeBuddyCredential{APIKey: sponsor.CodeBuddyAPIKey, InternetEnvironment: sponsor.CodeBuddyInternetEnvironment, Sponsored: true}
}

const sponsoredRunEvent = "model.sponsored"

// sponsoredExecutor pins a CodeBuddy node running on the sponsor's key to the
// sponsored model, and tells the user why the role's own model is not used.
func (e *Engine) sponsoredExecutor(runID string, ex domain.ExecutorDefinition, sink func(domain.TelemetryEvent)) domain.ExecutorDefinition {
	configured := strings.TrimSpace(ex.Model)
	ex.Model = repo.SponsoredCodeBuddyModel
	message := "责任人未配置个人 CodeBuddy API Key，本节点使用平台共享模型 Deepseek-V4.1-Flash"
	if configured != "" && configured != ex.Model {
		message += "（角色配置的 " + configured + " 本次不生效）"
	}
	sink(domain.TelemetryEvent{Category: "model", Name: sponsoredRunEvent, Phase: "start", Message: message, Provider: ex.Kind, Model: ex.Model, Attributes: map[string]any{"configuredModel": configured}})
	if current, ok := e.repo.RunState(runID); ok && !hasRunEvent(current, sponsoredRunEvent) {
		e.updateRun(runID, func(r *domain.Run) {
			if !hasRunEvent(*r, sponsoredRunEvent) {
				e.event(r, "", sponsoredRunEvent, "责任人未配置个人 CodeBuddy API Key，本次运行使用平台共享模型 Deepseek-V4.1-Flash。在个人配置中配置自己的 Key 后，可使用角色配置的模型")
			}
		})
	}
	return ex
}

func hasRunEvent(run domain.Run, typ string) bool {
	for _, event := range run.Events {
		if event.Type == typ {
			return true
		}
	}
	return false
}
